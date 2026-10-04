package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// XList polls the posts of one X list through the X API v2 with an app-only
// bearer token. The list endpoint has no since_id, so the checkpoint is a
// client-side watermark: each run sweeps the timeline newest first until it
// reaches the newest post of the previous completed sweep. Posts collected
// within the recheck window are periodically looked up by id; deleted or
// protected posts are withdrawn, as X's developer policy requires.
//
// The checkpoint is the one the former built-in kind wrote, field for field,
// so an instance continues where the built-in kind stopped.
type XList struct {
	// HTTP is the client for X requests; nil uses a 20 s timeout.
	HTTP *http.Client
}

const (
	defaultRecheckWindow = 24 * time.Hour
	defaultRecheckEvery  = 10 * time.Minute
	minRecheckEvery      = 60 // seconds, unless the pin allows short rechecks
	maxBackfill          = 7 * 24 * time.Hour
	lookupBatch          = 100
	maxRecheckPosts      = 2000
	withdrawnRevision    = "x:withdrawn"
	listPostFields       = "id,text,created_at,author_id,lang,entities,referenced_tweets,attachments,edit_history_tweet_ids,conversation_id,note_tweet"
	listExpansions       = "author_id,attachments.media_keys"
	listUserFields       = "username,name"
	listMediaFields      = "type,url,preview_image_url,alt_text"
	noticeCapReached     = "daily_read_cap_reached"
)

type config struct {
	ListID          string        `json:"list_id"`
	BackfillSince   *time.Time    `json:"backfill_since"`
	MaxReadsPerDay  int64         `json:"max_reads_per_day"`
	RecheckWindow   int64         `json:"recheck_window_seconds"`
	RecheckInterval int64         `json:"recheck_interval_seconds"`
	Webhook         webhookConfig `json:"webhook"`
}

func (c config) window() time.Duration {
	if c.RecheckWindow > 0 {
		return time.Duration(c.RecheckWindow) * time.Second
	}
	return defaultRecheckWindow
}

func (c config) interval() time.Duration {
	if c.RecheckInterval > 0 {
		return time.Duration(c.RecheckInterval) * time.Second
	}
	return defaultRecheckEvery
}

// configuration is the plugin configuration of the pin.
type configuration struct {
	APIEndpoint string `json:"api_endpoint"`
	// AllowShortRecheck accepts recheck intervals below 60 s, for test stacks
	// only: it holds only with a loopback api_endpoint, never against X.
	AllowShortRecheck bool `json:"allow_short_recheck"`
}

// checkpoint is the Acquisition Checkpoint of an x_list instance.
type checkpoint struct {
	// Floor excludes posts created before the instance started (or its backfill).
	Floor time.Time `json:"floor"`
	// Watermark is the newest post id of the last completed sweep.
	Watermark string   `json:"watermark,omitempty"`
	Sweep     *sweep   `json:"sweep,omitempty"`
	Recent    []recent `json:"recent,omitempty"`
	// Dropped counts posts that left the recheck set early because it was full.
	Dropped     int64      `json:"dropped,omitempty"`
	LastRecheck *time.Time `json:"last_recheck_at,omitempty"`
	Recheck     *recheck   `json:"recheck,omitempty"`
	// ListReadDay is the UTC day of the last list request: X bills a post once
	// per UTC day, so only the first list page of a day re-bills known posts.
	ListReadDay string `json:"list_read_day,omitempty"`
	// Push is the webhook setup, only while webhook mode is on.
	Push *pushState `json:"push,omitempty"`
}

type sweep struct {
	Top   string `json:"top"`
	Token string `json:"token,omitempty"`
}

type recent struct {
	Root   string    `json:"root"`
	Latest string    `json:"latest"`
	At     time.Time `json:"at"`
	// Read is the UTC day this post was last read (collected or rechecked).
	Read string `json:"read,omitempty"`
}

type recheck struct {
	Started time.Time `json:"started"`
	After   string    `json:"after,omitempty"`
}

// track adds or updates a collected post in the recheck set, dropping the
// oldest entry when the set is full.
func (cp *checkpoint) track(root, latest string, at time.Time, day string) {
	for i := range cp.Recent {
		if cp.Recent[i].Root == root {
			cp.Recent[i].Latest, cp.Recent[i].Read = latest, day
			return
		}
	}
	cp.Recent = append(cp.Recent, recent{Root: root, Latest: latest, At: at, Read: day})
	for len(cp.Recent) > maxRecheckPosts {
		oldest := 0
		for i, r := range cp.Recent {
			if r.At.Before(cp.Recent[oldest].At) {
				oldest = i
			}
		}
		cp.Recent = append(cp.Recent[:oldest], cp.Recent[oldest+1:]...)
		cp.Dropped++
	}
}

func (cp *checkpoint) prune(now time.Time, window time.Duration) {
	kept := cp.Recent[:0]
	for _, r := range cp.Recent {
		if !r.At.Before(now.Add(-window)) {
			kept = append(kept, r)
		}
	}
	cp.Recent = kept
}

func (cp *checkpoint) recheckDue(now time.Time, every time.Duration) bool {
	return cp.Recheck != nil || cp.LastRecheck == nil || !now.Before(cp.LastRecheck.Add(every))
}

// idAfter reports whether post id a is newer than b (ids are decimal snowflakes).
func idAfter(a, b string) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a > b
}

func (x XList) client(req *quivrplugin.FetchRequest) (client, error) {
	var conf configuration
	if len(req.Configuration) > 0 {
		if err := json.Unmarshal(req.Configuration, &conf); err != nil {
			return client{}, quivrplugin.SourceError("invalid_configuration", "the plugin configuration cannot be read")
		}
	}
	base := strings.TrimRight(conf.APIEndpoint, "/")
	if base == "" {
		base = DefaultAPI
	}
	if conf.AllowShortRecheck && !loopback(base) {
		return client{}, quivrplugin.SourceError("invalid_configuration", "allow_short_recheck needs a loopback api_endpoint: it is for test fakes only")
	}
	var secret struct {
		BearerToken string `json:"bearer_token"`
	}
	if req.Credential.Decode(&secret) != nil || secret.BearerToken == "" {
		return client{}, quivrplugin.AccessError("unauthorized", "the credential has no bearer_token")
	}
	h := x.HTTP
	if h == nil {
		h = &http.Client{Timeout: 20 * time.Second}
	}
	return client{base: base, token: secret.BearerToken, http: h, shortRecheck: conf.AllowShortRecheck}, nil
}

func loopback(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
}

// Fetch sweeps the list for posts newer than the watermark, then, when due,
// looks the tracked posts up and withdraws the ones X no longer serves.
func (x XList) Fetch(ctx context.Context, req *quivrplugin.FetchRequest) (*quivrplugin.Page, error) {
	var cfg config
	if err := req.Connector.DecodeConfig(&cfg); err != nil || cfg.ListID == "" {
		return nil, quivrplugin.SourceError("invalid_config", "the instance configuration cannot be read")
	}
	c, err := x.client(req)
	if err != nil {
		return nil, err
	}
	if cfg.RecheckInterval > 0 && cfg.RecheckInterval < minRecheckEvery && !c.shortRecheck {
		return nil, quivrplugin.SourceError("invalid_config", "recheck_interval_seconds must be at least 60")
	}
	var cp checkpoint
	if err := req.DecodeCheckpoint(&cp); err != nil {
		return nil, quivrplugin.SourceError("invalid_checkpoint", "the checkpoint is not an x_list checkpoint")
	}
	now := req.Now
	if cp.Floor.IsZero() {
		// backfill_since reaches at most 7 days back, checked at the first poll.
		if cfg.BackfillSince != nil && (cfg.BackfillSince.After(now) || cfg.BackfillSince.Before(now.Add(-maxBackfill))) {
			return nil, quivrplugin.SourceError("invalid_config", "backfill_since must be within the 7 days before the first poll")
		}
		cp.Floor = now
		if cfg.BackfillSince != nil {
			cp.Floor = *cfg.BackfillSince
		}
		started := now
		cp.LastRecheck = &started
	}
	cp.prune(now, cfg.window())
	page := &quivrplugin.Page{}
	capped := cfg.MaxReadsPerDay > 0 && req.ReadsToday >= cfg.MaxReadsPerDay
	if capped {
		// The spend cap stops polling for new posts; deletion rechecks go on.
		page.Notice = noticeCapReached
		cp.Sweep = nil
	} else if req.PageInRun == 0 && cp.Sweep == nil {
		cp.Sweep = &sweep{}
	}
	scope := Scope{CorpusID: req.Connector.CorpusID, Namespace: req.Connector.SourceNamespace}
	switch {
	case cfg.Webhook.Enabled && cp.Push == nil:
		cp.Push = &pushState{}
	case !cfg.Webhook.Enabled && cp.Push != nil:
		// Webhook mode was turned off: report it once and forget the setup.
		// The rules and the webhook stay at X until an operator removes them.
		cp.Push = nil
		page.Push = quivrplugin.PushIsPending("webhook_disabled")
	}
	setupDue := cp.Push != nil && cp.Push.due(now, cfg.Webhook.resyncEvery())
	switch {
	case cp.Sweep != nil:
		err = c.sweep(ctx, cfg, &cp, now, scope, page)
		page.More = page.More || cp.Sweep == nil && setupDue
	case cp.recheckDue(now, cfg.interval()):
		if cp.Recheck == nil {
			cp.Recheck = &recheck{Started: now}
		}
		err = c.recheck(ctx, &cp, now, page)
		page.More = page.More || cp.Recheck == nil && setupDue
	case setupDue:
		c.stepSetup(ctx, cp.Push, cfg.Webhook, req.Connector.ID, req.Connector.WebhookURL, cfg.ListID, now)
		page.More = cp.Push.Step != ""
	}
	if err != nil {
		return nil, err
	}
	if cp.Push != nil {
		page.Push = cp.Push.status(cfg.Webhook)
	}
	page.Checkpoint = cp
	page.Diagnostics = diagnostics(cfg, cp)
	return page, nil
}

// CheckCredential answers ok without calling X: X bills every request, and
// a refused token surfaces at the first fetch as access unauthorized.
func (XList) CheckCredential(context.Context, *quivrplugin.CredentialRequest) (*quivrplugin.CredentialStatus, error) {
	return &quivrplugin.CredentialStatus{}, nil
}

func diagnostics(cfg config, cp checkpoint) map[string]any {
	d := map[string]any{
		"recheck_window_seconds":   int64(cfg.window() / time.Second),
		"recheck_interval_seconds": int64(cfg.interval() / time.Second),
		"recheck_tracked_posts":    len(cp.Recent),
		"recheck_dropped_posts":    cp.Dropped,
	}
	if cp.LastRecheck != nil {
		d["last_recheck_at"] = cp.LastRecheck.UTC().Format(time.RFC3339)
	}
	pushDiagnostics(d, cp.Push)
	return d
}

type listResponse struct {
	Data     []Post    `json:"data"`
	Includes Includes  `json:"includes"`
	Errors   []problem `json:"errors"`
	Meta     struct {
		NextToken string `json:"next_token"`
	} `json:"meta"`
}

func (c client) sweep(ctx context.Context, cfg config, cp *checkpoint, now time.Time, scope Scope, page *quivrplugin.Page) error {
	q := url.Values{"max_results": {"100"}, "tweet.fields": {listPostFields}, "expansions": {listExpansions}, "user.fields": {listUserFields}, "media.fields": {listMediaFields}}
	if cp.Sweep.Token != "" {
		q.Set("pagination_token", cp.Sweep.Token)
	}
	var resp listResponse
	err := c.get(ctx, "/2/lists/"+url.PathEscape(cfg.ListID)+"/tweets", q, now, &resp)
	if err == errBadRequest && cp.Sweep.Token != "" {
		// An expired pagination token restarts the sweep from the newest post;
		// re-read posts replay their Receipts.
		cp.Sweep = &sweep{}
		page.More = true
		return nil
	}
	if err != nil {
		return err
	}
	if len(resp.Data) == 0 {
		for _, p := range resp.Errors {
			if p.ResourceType == "list" && p.is(problemNotFound) {
				return quivrplugin.AccessError("list_not_found", "X does not know the list")
			}
			if p.ResourceType == "list" && p.is(problemUnauthorized) {
				return quivrplugin.AccessError("forbidden", "X refused access to the list")
			}
		}
	}
	// Estimated billed reads: X deduplicates a post within a UTC day, so after
	// the day's first list page only posts newer than the watermark are new.
	today := now.UTC().Format(time.DateOnly)
	firstOfDay := cp.ListReadDay != today
	cp.ListReadDay = today
	for _, post := range resp.Data {
		if firstOfDay || cp.Watermark == "" || idAfter(post.ID, cp.Watermark) {
			page.Reads++
		}
	}
	done := false
	for _, post := range resp.Data {
		if created, err := time.Parse(time.RFC3339, post.CreatedAt); err == nil && cp.Push.held(created, now) {
			// The webhook may still deliver it; the next sweep reads it.
			continue
		}
		if cp.Sweep.Top == "" || idAfter(post.ID, cp.Sweep.Top) {
			cp.Sweep.Top = post.ID
		}
		if cp.Watermark != "" && !idAfter(post.ID, cp.Watermark) {
			done = true
			break
		}
		created, err := time.Parse(time.RFC3339, post.CreatedAt)
		if err == nil && created.Before(cp.Floor) {
			done = true
			break
		}
		if err != nil {
			created = now
		}
		item := MapPost(post, resp.Includes, scope)
		page.Items = append(page.Items, item)
		cp.track(item.RecordKey, post.ID, created, today)
	}
	if !done && resp.Meta.NextToken != "" {
		cp.Sweep.Token = resp.Meta.NextToken
		page.More = true
		return nil
	}
	if cp.Sweep.Top != "" && (cp.Watermark == "" || idAfter(cp.Sweep.Top, cp.Watermark)) {
		cp.Watermark = cp.Sweep.Top
	}
	cp.Sweep = nil
	page.More = cp.recheckDue(now, cfg.interval())
	return nil
}

func (c client) recheck(ctx context.Context, cp *checkpoint, now time.Time, page *quivrplugin.Page) error {
	batch := make([]recent, 0, lookupBatch)
	sorted := append([]recent(nil), cp.Recent...)
	sort.Slice(sorted, func(i, j int) bool { return idAfter(sorted[j].Root, sorted[i].Root) })
	for _, r := range sorted {
		if cp.Recheck.After == "" || idAfter(r.Root, cp.Recheck.After) {
			batch = append(batch, r)
			if len(batch) == lookupBatch {
				break
			}
		}
	}
	if len(batch) == 0 {
		started := cp.Recheck.Started
		cp.LastRecheck, cp.Recheck = &started, nil
		return nil
	}
	ids := make([]string, len(batch))
	for i, r := range batch {
		ids[i] = r.Latest
	}
	var resp struct {
		Data   []Post    `json:"data"`
		Errors []problem `json:"errors"`
	}
	if err := c.get(ctx, "/2/tweets", url.Values{"ids": {strings.Join(ids, ",")}, "tweet.fields": {"id,edit_history_tweet_ids"}}, now, &resp); err != nil {
		return err
	}
	// A post already read this UTC day (collected or rechecked) is not billed again.
	today := now.UTC().Format(time.DateOnly)
	returned := map[string]bool{}
	for _, p := range resp.Data {
		returned[p.ID] = true
	}
	for i := range cp.Recent {
		if returned[cp.Recent[i].Latest] && cp.Recent[i].Read != today {
			page.Reads++
			cp.Recent[i].Read = today
		}
	}
	gone := map[string]bool{}
	for _, p := range resp.Errors {
		id := p.ResourceID
		if id == "" {
			id = p.Value
		}
		if p.ResourceType == "tweet" && (p.is(problemNotFound) || p.is(problemUnauthorized)) {
			gone[id] = true
		}
	}
	for _, r := range batch {
		if gone[r.Latest] {
			page.Items = append(page.Items, quivrplugin.Item{RecordKey: r.Root, Revision: withdrawnRevision, Withdraw: true})
		}
	}
	kept := cp.Recent[:0]
	for _, r := range cp.Recent {
		if !gone[r.Latest] {
			kept = append(kept, r)
		}
	}
	cp.Recent = kept
	cp.Recheck.After = batch[len(batch)-1].Root
	page.More = true
	return nil
}
