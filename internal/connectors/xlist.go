package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// XList polls the posts of one X list through the X API v2 with an app-only
// bearer token. The list endpoint has no since_id, so the Acquisition
// Checkpoint is a client-side watermark: each run sweeps the timeline newest
// first until it reaches the newest post of the previous completed sweep.
// Posts collected within the recheck window are periodically looked up by id;
// deleted or protected posts are withdrawn, as X's developer policy requires.
type XList struct {
	// BaseURL is the X API origin (deployment configuration); default https://api.x.com.
	BaseURL string
	// PageSize is max_results per list request (1-100); default 100.
	PageSize int
	Client   *http.Client
}

// XExtensionNamespace is the Record Version extension holding the X post metadata.
const XExtensionNamespace = "connector.x_list"

const (
	// DefaultXAPI is the public X API origin.
	DefaultXAPI            = "https://api.x.com"
	xDefaultRecheckWindow  = 24 * time.Hour
	xDefaultRecheckEvery   = 10 * time.Minute
	xMaxRetryAfter         = 15 * time.Minute
	xDefaultRetryAfter     = time.Minute
	xLookupBatch           = 100
	maxRecheckPosts        = 2000
	xMaxResponseBytes      = 8 << 20
	xWithdrawnRevision     = "x:withdrawn"
	xListPostFields        = "id,text,created_at,author_id,lang,entities,referenced_tweets,attachments,edit_history_tweet_ids,conversation_id,note_tweet"
	xListExpansions        = "author_id,attachments.media_keys"
	xListUserFields        = "username,name"
	xListMediaFields       = "type,url,preview_image_url,alt_text"
	xProblemNotFound       = "resource-not-found"
	xProblemNotAuthorized  = "not-authorized-for-resource"
	xCompletedNoticeCapHit = "daily_read_cap_reached"
)

type xConfig struct {
	ListID          string     `json:"list_id"`
	BackfillSince   *time.Time `json:"backfill_since"`
	MaxReadsPerDay  int64      `json:"max_reads_per_day"`
	RecheckWindow   int64      `json:"recheck_window_seconds"`
	RecheckInterval int64      `json:"recheck_interval_seconds"`
}

func (c xConfig) window() time.Duration {
	if c.RecheckWindow > 0 {
		return time.Duration(c.RecheckWindow) * time.Second
	}
	return xDefaultRecheckWindow
}

func (c xConfig) interval() time.Duration {
	if c.RecheckInterval > 0 {
		return time.Duration(c.RecheckInterval) * time.Second
	}
	return xDefaultRecheckEvery
}

// xCheckpoint is the Acquisition Checkpoint of an x_list instance.
type xCheckpoint struct {
	// Floor excludes posts created before the instance started (or its backfill).
	Floor time.Time `json:"floor"`
	// Watermark is the newest post id of the last completed sweep.
	Watermark string    `json:"watermark,omitempty"`
	Sweep     *xSweep   `json:"sweep,omitempty"`
	Recent    []xRecent `json:"recent,omitempty"`
	// Dropped counts posts that left the recheck set early because it was full.
	Dropped     int64      `json:"dropped,omitempty"`
	LastRecheck *time.Time `json:"last_recheck_at,omitempty"`
	Recheck     *xRecheck  `json:"recheck,omitempty"`
	// ListReadDay is the UTC day of the last list request: X bills a post once
	// per UTC day, so only the first list page of a day re-bills known posts.
	ListReadDay string `json:"list_read_day,omitempty"`
}

type xSweep struct {
	Top   string `json:"top"`
	Token string `json:"token,omitempty"`
}

type xRecent struct {
	Root   string    `json:"root"`
	Latest string    `json:"latest"`
	At     time.Time `json:"at"`
	// Read is the UTC day this post was last read (collected or rechecked).
	Read string `json:"read,omitempty"`
}

type xRecheck struct {
	Started time.Time `json:"started"`
	After   string    `json:"after,omitempty"`
}

// track adds or updates a collected post in the recheck set, dropping the
// oldest entry when the set is full.
func (cp *xCheckpoint) track(root, latest string, at time.Time, day string) {
	for i := range cp.Recent {
		if cp.Recent[i].Root == root {
			cp.Recent[i].Latest, cp.Recent[i].Read = latest, day
			return
		}
	}
	cp.Recent = append(cp.Recent, xRecent{Root: root, Latest: latest, At: at, Read: day})
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

func (cp *xCheckpoint) prune(now time.Time, window time.Duration) {
	kept := cp.Recent[:0]
	for _, r := range cp.Recent {
		if !r.At.Before(now.Add(-window)) {
			kept = append(kept, r)
		}
	}
	cp.Recent = kept
}

func (cp *xCheckpoint) recheckDue(now time.Time, every time.Duration) bool {
	return cp.Recheck != nil || cp.LastRecheck == nil || !now.Before(cp.LastRecheck.Add(every))
}

// idAfter reports whether post id a is newer than b (ids are decimal snowflakes).
func idAfter(a, b string) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a > b
}

func (XList) Kind() string                   { return "x_list" }
func (XList) DefaultInterval() time.Duration { return 2 * time.Minute }
func (XList) ConfigSchema() []byte {
	return []byte(`{"type":"object","additionalProperties":false,"required":["list_id"],"properties":{
"list_id":{"type":"string","pattern":"^[0-9]{1,19}$"},
"backfill_since":{"type":"string","minLength":1},
"max_reads_per_day":{"type":"integer","minimum":100,"maximum":100000000},
"recheck_window_seconds":{"type":"integer","minimum":3600,"maximum":604800},
"recheck_interval_seconds":{"type":"integer","minimum":60,"maximum":86400}}}`)
}
func (XList) CredentialSchema() []byte {
	return []byte(`{"type":"object","additionalProperties":false,"required":["bearer_token"],"properties":{
"bearer_token":{"type":"string","minLength":1},"consumer_secret":{"type":"string","minLength":1}}}`)
}

// CheckConfig bounds backfill_since to the 7 days before creation.
func (XList) CheckConfig(raw json.RawMessage, now time.Time) error {
	var cfg xConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if cfg.BackfillSince != nil && (cfg.BackfillSince.After(now) || cfg.BackfillSince.Before(now.Add(-7*24*time.Hour))) {
		return errors.New("backfill_since must be within the last 7 days")
	}
	return nil
}

func (x XList) Fetch(ctx context.Context, r FetchRequest) (Page, error) {
	var cfg xConfig
	if err := json.Unmarshal(r.Config, &cfg); err != nil || cfg.ListID == "" {
		return Page{}, SourceError("invalid_config")
	}
	var secret struct {
		BearerToken string `json:"bearer_token"`
	}
	if r.Credential == nil || json.Unmarshal(r.Credential, &secret) != nil || secret.BearerToken == "" {
		return Page{}, AccessError("unauthorized")
	}
	var cp xCheckpoint
	if len(r.Checkpoint) > 0 && json.Unmarshal(r.Checkpoint, &cp) != nil {
		return Page{}, SourceError("invalid_checkpoint")
	}
	if cp.Floor.IsZero() {
		cp.Floor = r.Now
		if cfg.BackfillSince != nil {
			cp.Floor = *cfg.BackfillSince
		}
		started := r.Now
		cp.LastRecheck = &started
	}
	cp.prune(r.Now, cfg.window())
	page := Page{}
	capped := cfg.MaxReadsPerDay > 0 && r.ReadsToday >= cfg.MaxReadsPerDay
	if capped {
		// The spend cap stops polling for new posts; deletion rechecks go on.
		page.Notice = xCompletedNoticeCapHit
		cp.Sweep = nil
	} else if r.PageInRun == 0 && cp.Sweep == nil {
		cp.Sweep = &xSweep{}
	}
	var err error
	switch {
	case cp.Sweep != nil:
		err = x.sweep(ctx, secret.BearerToken, cfg, &cp, r.Now, &page)
	case cp.recheckDue(r.Now, cfg.interval()):
		if cp.Recheck == nil {
			cp.Recheck = &xRecheck{Started: r.Now}
		}
		err = x.recheck(ctx, secret.BearerToken, &cp, r.Now, &page)
	}
	if err != nil {
		return Page{}, err
	}
	page.Checkpoint, _ = json.Marshal(cp)
	page.Diagnostics = xDiagnostics(cfg, cp)
	return page, nil
}

func xDiagnostics(cfg xConfig, cp xCheckpoint) json.RawMessage {
	d := map[string]any{
		"recheck_window_seconds":   int64(cfg.window() / time.Second),
		"recheck_interval_seconds": int64(cfg.interval() / time.Second),
		"recheck_tracked_posts":    len(cp.Recent),
		"recheck_dropped_posts":    cp.Dropped,
	}
	if cp.LastRecheck != nil {
		d["last_recheck_at"] = cp.LastRecheck.UTC().Format(time.RFC3339)
	}
	b, _ := json.Marshal(d)
	return b
}

type xListResponse struct {
	Data     []XPost    `json:"data"`
	Includes XIncludes  `json:"includes"`
	Errors   []xProblem `json:"errors"`
	Meta     struct {
		NextToken string `json:"next_token"`
	} `json:"meta"`
}

type xProblem struct {
	Type         string `json:"type"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Value        string `json:"value"`
}

func (p xProblem) is(kind string) bool { return strings.HasSuffix(p.Type, "/"+kind) }

var errXBadRequest = &Error{Class: ClassSource, Code: "invalid_request"}

func (x XList) sweep(ctx context.Context, token string, cfg xConfig, cp *xCheckpoint, now time.Time, page *Page) error {
	size := x.PageSize
	if size <= 0 || size > 100 {
		size = 100
	}
	q := url.Values{"max_results": {strconv.Itoa(size)}, "tweet.fields": {xListPostFields}, "expansions": {xListExpansions}, "user.fields": {xListUserFields}, "media.fields": {xListMediaFields}}
	if cp.Sweep.Token != "" {
		q.Set("pagination_token", cp.Sweep.Token)
	}
	var resp xListResponse
	err := x.get(ctx, token, "/2/lists/"+url.PathEscape(cfg.ListID)+"/tweets", q, now, &resp)
	if err == errXBadRequest && cp.Sweep.Token != "" {
		// An expired pagination token restarts the sweep from the newest post;
		// re-read posts replay their Receipts.
		cp.Sweep = &xSweep{}
		page.More = true
		return nil
	}
	if err != nil {
		return err
	}
	if len(resp.Data) == 0 {
		for _, p := range resp.Errors {
			if p.ResourceType == "list" && p.is(xProblemNotFound) {
				return AccessError("list_not_found")
			}
			if p.ResourceType == "list" && p.is(xProblemNotAuthorized) {
				return AccessError("forbidden")
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
		item := MapPost(post, resp.Includes)
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

func (x XList) recheck(ctx context.Context, token string, cp *xCheckpoint, now time.Time, page *Page) error {
	batch := make([]xRecent, 0, xLookupBatch)
	sorted := append([]xRecent(nil), cp.Recent...)
	sort.Slice(sorted, func(i, j int) bool { return idAfter(sorted[j].Root, sorted[i].Root) })
	for _, r := range sorted {
		if cp.Recheck.After == "" || idAfter(r.Root, cp.Recheck.After) {
			batch = append(batch, r)
			if len(batch) == xLookupBatch {
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
		Data   []XPost    `json:"data"`
		Errors []xProblem `json:"errors"`
	}
	if err := x.get(ctx, token, "/2/tweets", url.Values{"ids": {strings.Join(ids, ",")}, "tweet.fields": {"id,edit_history_tweet_ids"}}, now, &resp); err != nil {
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
		if p.ResourceType == "tweet" && (p.is(xProblemNotFound) || p.is(xProblemNotAuthorized)) {
			gone[id] = true
		}
	}
	for _, r := range batch {
		if gone[r.Latest] {
			page.Items = append(page.Items, Item{RecordKey: r.Root, Revision: xWithdrawnRevision, Withdraw: true})
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

// get performs one authenticated X API request and maps failures to typed
// acquisition errors; response bodies are never surfaced.
func (x XList) get(ctx context.Context, token, path string, q url.Values, now time.Time, out any) error {
	base := strings.TrimRight(x.BaseURL, "/")
	if base == "" {
		base = DefaultXAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+q.Encode(), nil)
	if err != nil {
		return SourceError("invalid_request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	client := x.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return TransientError("source_unavailable")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		if json.NewDecoder(io.LimitReader(resp.Body, xMaxResponseBytes)).Decode(out) != nil {
			return SourceError("invalid_response")
		}
		return nil
	case resp.StatusCode == http.StatusBadRequest:
		return errXBadRequest
	case resp.StatusCode == http.StatusUnauthorized:
		return AccessError("unauthorized")
	case resp.StatusCode == http.StatusPaymentRequired:
		return AccessError("credits_depleted")
	case resp.StatusCode == http.StatusForbidden:
		return AccessError("forbidden")
	case resp.StatusCode == http.StatusNotFound:
		return AccessError("list_not_found")
	case resp.StatusCode == http.StatusTooManyRequests:
		return &Error{Class: ClassTransient, Code: "rate_limited", RetryAfter: xRetryAfter(resp.Header.Get("x-rate-limit-reset"), now)}
	default:
		return TransientError("source_unavailable")
	}
}

// xRetryAfter converts the x-rate-limit-reset epoch into a bounded delay.
func xRetryAfter(reset string, now time.Time) time.Duration {
	epoch, err := strconv.ParseInt(reset, 10, 64)
	if err != nil {
		return xDefaultRetryAfter
	}
	wait := time.Unix(epoch, 0).Sub(now).Round(time.Second)
	switch {
	case wait <= 0:
		return xDefaultRetryAfter
	case wait > xMaxRetryAfter:
		return xMaxRetryAfter
	}
	return wait
}

// XPost is an X API v2 post object with the fields the connector requests.
type XPost struct {
	ID               string         `json:"id"`
	Text             string         `json:"text"`
	CreatedAt        string         `json:"created_at,omitempty"`
	AuthorID         string         `json:"author_id,omitempty"`
	Lang             string         `json:"lang,omitempty"`
	ConversationID   string         `json:"conversation_id,omitempty"`
	EditHistory      []string       `json:"edit_history_tweet_ids,omitempty"`
	Entities         map[string]any `json:"entities,omitempty"`
	ReferencedTweets []XReference   `json:"referenced_tweets,omitempty"`
	Attachments      *XAttachments  `json:"attachments,omitempty"`
	NoteTweet        *XNoteTweet    `json:"note_tweet,omitempty"`
}

// XReference is a referenced post (quoted, replied_to or retweeted).
type XReference struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// XAttachments lists a post's media keys.
type XAttachments struct {
	MediaKeys []string `json:"media_keys,omitempty"`
}

// XNoteTweet carries the full text of a long post.
type XNoteTweet struct {
	Text string `json:"text"`
}

// XIncludes are the expanded users and media of a response.
type XIncludes struct {
	Users []XUser  `json:"users,omitempty"`
	Media []XMedia `json:"media,omitempty"`
}

// XUser is an expanded post author.
type XUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

// XMedia is an expanded media reference; media bytes are never downloaded.
type XMedia struct {
	MediaKey        string `json:"media_key"`
	Type            string `json:"type"`
	URL             string `json:"url,omitempty"`
	PreviewImageURL string `json:"preview_image_url,omitempty"`
	AltText         string `json:"alt_text,omitempty"`
}

var xRelationTypes = map[string]string{"quoted": "quotes", "replied_to": "replies_to", "retweeted": "reposts"}

// MapPost maps one X post (any version) to a connector Item. The Record Key
// is the original post id (first of the edit history), so an edit becomes a
// correction of the same Record; the latest version id is both the revision
// and the Source Position, so an older version never supersedes a newer one.
// It is pure so polling and webhook deliveries share it. Relation targets are
// left unbound: the Acquirer binds them to the instance's Corpus and Source
// Namespace.
func MapPost(post XPost, inc XIncludes) Item {
	root := post.ID
	history := post.EditHistory
	if len(history) == 0 {
		history = []string{post.ID}
	}
	root = history[0]
	text := post.Text
	if post.NoteTweet != nil && post.NoteTweet.Text != "" {
		text = post.NoteTweet.Text
	}
	if strings.TrimSpace(text) == "" || !content.ValidText(text) {
		text = "https://x.com/i/web/status/" + post.ID
	}
	data := map[string]any{"post_id": post.ID, "edit_history_post_ids": history}
	link := "https://x.com/i/web/status/" + post.ID
	for _, u := range inc.Users {
		if u.ID == post.AuthorID && post.AuthorID != "" {
			data["author"] = map[string]any{"id": u.ID, "username": u.Username, "name": u.Name}
			if u.Username != "" {
				link = "https://x.com/" + u.Username + "/status/" + post.ID
			}
		}
	}
	if _, ok := data["author"]; !ok && post.AuthorID != "" {
		data["author"] = map[string]any{"id": post.AuthorID}
	}
	data["url"] = link
	for k, v := range map[string]string{"created_at": post.CreatedAt, "lang": post.Lang, "conversation_id": post.ConversationID} {
		if v != "" {
			data[k] = v
		}
	}
	if len(post.Entities) > 0 {
		data["entities"] = post.Entities
	}
	var relations []content.Relation
	if len(post.ReferencedTweets) > 0 {
		refs := make([]any, 0, len(post.ReferencedTweets))
		for _, r := range post.ReferencedTweets {
			refs = append(refs, map[string]any{"type": r.Type, "id": r.ID})
			if t, ok := xRelationTypes[r.Type]; ok && r.ID != "" {
				relations = append(relations, content.Relation{Type: t, Target: content.Source{RecordKey: r.ID}})
			}
		}
		data["referenced_posts"] = refs
	}
	if post.Attachments != nil && len(post.Attachments.MediaKeys) > 0 {
		media := make([]any, 0, len(post.Attachments.MediaKeys))
		for _, key := range post.Attachments.MediaKeys {
			m := map[string]any{"media_key": key}
			for _, im := range inc.Media {
				if im.MediaKey == key {
					for k, v := range map[string]string{"type": im.Type, "url": im.URL, "preview_image_url": im.PreviewImageURL, "alt_text": im.AltText} {
						if v != "" {
							m[k] = v
						}
					}
				}
			}
			media = append(media, m)
		}
		data["media"] = media
	}
	// A round trip through JSON gives the extension plain JSON values.
	raw, _ := json.Marshal(data)
	var plain map[string]any
	_ = json.Unmarshal(raw, &plain)
	return Item{
		RecordKey:  root,
		Revision:   post.ID,
		Position:   post.ID,
		Manifest:   &content.Manifest{Kind: "manifest", Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: text}}}, Relations: relations},
		Extensions: content.Extensions{XExtensionNamespace: {SchemaVersion: "1", Data: plain}},
	}
}
