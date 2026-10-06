package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
	"github.com/mmcdole/gofeed"
	"github.com/mmcdole/gofeed/rss"
)

// Extension is the namespaced extension carrying item and feed metadata. It
// equals the plugin id, which owns it.
const Extension = "connector.rss"

// Collection bounds, unchanged from the connector built into the core before
// it moved here, except the page and checkpoint byte bounds the plugin
// protocol adds.
const (
	defaultTimeout  = 20 * time.Second
	maxResponseSize = 10 << 20
	maxRedirects    = 5
	itemsPerPage    = 250
	maxFeedItems    = 1000
	seenCap         = 2000
	ttlCap          = time.Hour
	maxKeyBytes     = 1024
	maxField        = 2048
	maxCategories   = 50
	maxEnclosures   = 20
	// maxItemText bounds the text of one item's Parts, well under the
	// public 1 MiB command limit.
	maxItemText = 768 << 10
	maxBodyText = 384 << 10
	maxSummary  = 64 << 10
	// maxExtensionBytes keeps the extension under the 64 KiB generic JSON bound.
	maxExtensionBytes = 48 << 10
	// maxPageItemBytes cuts a page well under the manifest's 16 MiB
	// max_response_bytes; the rest follows on the next page.
	maxPageItemBytes = 12 << 20
	// maxCheckpointBytes keeps the checkpoint under the manifest's 128 KiB
	// max_checkpoint_bytes (2000 revisions take about 90 KiB); the oldest
	// revisions of items no longer in the feed go first.
	maxCheckpointBytes = 120 << 10
)

// feed collects RSS 0.9x/2.0, RSS 1.0 (RDF) and Atom feeds. Only the feed
// document is fetched: linked articles and enclosures are never downloaded.
// Each poll emits only items that are new or whose content changed since the
// checkpoint; an item leaving the feed is not withdrawn.
type feed struct {
	// timeout overrides the request timeout (tests).
	timeout time.Duration
	// transport, when set, is the base HTTP transport (tests with TLS servers).
	transport *http.Transport
}

// configuration is the plugin configuration an installer sets in the pin.
type configuration struct {
	// AllowPrivateAddresses lifts the refusal of loopback, private and
	// link-local destinations. Local and CI deployments only.
	AllowPrivateAddresses bool `json:"allow_private_addresses"`
}

type feedConfig struct {
	URL      string `json:"url"`
	HonorTTL *bool  `json:"honor_ttl"`
}

type feedCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Token    string `json:"token"`
}

// checkpoint resumes polling: HTTP validators, the ttl deadline and the
// revision last emitted per Record Key (hashed and bounded). Its JSON is the
// one the built-in connector wrote, so existing instances resume unchanged.
type checkpoint struct {
	ETag         string     `json:"etag,omitempty"`
	LastModified string     `json:"last_modified,omitempty"`
	TTLSeconds   int        `json:"ttl_seconds,omitempty"`
	NotBefore    *time.Time `json:"not_before,omitempty"`
	// Partial marks a feed whose changed items did not fit one page: the
	// next page refetches unconditionally.
	Partial bool   `json:"partial,omitempty"`
	Seen    []seen `json:"seen,omitempty"`
}

type seen struct {
	Key      string `json:"k"`
	Revision string `json:"r"`
}

var (
	errTooManyRedirects = errors.New("too_many_redirects")
	errInsecureRedirect = errors.New("insecure_redirect")
)

func sourceError(code string) error    { return quivrplugin.SourceError(code, "") }
func accessError(code string) error    { return quivrplugin.AccessError(code, "") }
func transientError(code string) error { return quivrplugin.TransientError(code, "") }

func decodeRequest(configurationJSON json.RawMessage, instance quivrplugin.Instance, credential quivrplugin.Credential) (configuration, feedConfig, *feedCredential, error) {
	var conf configuration
	if len(configurationJSON) > 0 && string(configurationJSON) != "null" {
		if err := json.Unmarshal(configurationJSON, &conf); err != nil {
			return conf, feedConfig{}, nil, sourceError("invalid_configuration")
		}
	}
	var cfg feedConfig
	if err := instance.DecodeConfig(&cfg); err != nil || cfg.URL == "" {
		return conf, cfg, nil, sourceError("invalid_config")
	}
	if credential.IsNull() {
		return conf, cfg, nil, nil
	}
	cred := &feedCredential{}
	if err := credential.Decode(cred); err != nil {
		return conf, cfg, nil, accessError("credential_unreadable")
	}
	return conf, cfg, cred, nil
}

func (f feed) Fetch(ctx context.Context, req *quivrplugin.FetchRequest) (*quivrplugin.Page, error) {
	conf, cfg, cred, err := decodeRequest(req.Configuration, req.Connector, req.Credential)
	if err != nil {
		return nil, err
	}
	var cp checkpoint
	if err := req.DecodeCheckpoint(&cp); err != nil {
		return nil, sourceError("invalid_checkpoint")
	}
	honorTTL := cfg.HonorTTL == nil || *cfg.HonorTTL
	if honorTTL && cp.NotBefore != nil && req.Now.Before(*cp.NotBefore) {
		return nil, quivrplugin.ErrNotDue
	}

	resp, body, err := f.get(ctx, conf, cfg.URL, cred, cp, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotModified {
		cp.NotBefore = notBefore(honorTTL, cp.TTLSeconds, req.Now)
		return &quivrplugin.Page{Checkpoint: encode(cp)}, nil
	}
	parser := gofeed.NewParser()
	parser.KeepOriginalFeed = true
	parsed, err := parser.Parse(strings.NewReader(string(body)))
	if err != nil || parsed == nil {
		return nil, sourceError("malformed_feed")
	}
	ttl := 0
	if original, ok := parsed.OriginalFeed().(*rss.Feed); ok && original.TTL != "" {
		if minutes, err := strconv.Atoi(strings.TrimSpace(original.TTL)); err == nil && minutes > 0 {
			ttl = minutes * 60
		}
	}

	candidates := mapFeed(parsed)
	known := make(map[string]string, len(cp.Seen))
	for _, s := range cp.Seen {
		known[s.Key] = s.Revision
	}
	var changed []candidate
	for _, c := range candidates {
		if known[c.hash] != c.short {
			changed = append(changed, c)
		}
	}
	page := &quivrplugin.Page{Items: []quivrplugin.Item{}}
	size := 0
	for i, c := range changed {
		if i == itemsPerPage {
			page.More = true
			break
		}
		encoded, _ := json.Marshal(c.item)
		if size += len(encoded); size > maxPageItemBytes && i > 0 {
			page.More = true
			break
		}
		page.Items = append(page.Items, c.item)
		known[c.hash] = c.short
	}
	cp.Seen = boundSeen(cp.Seen, candidates, known)
	cp.Partial = page.More
	if page.More {
		cp.ETag, cp.LastModified, cp.NotBefore = "", "", nil
	} else {
		cp.ETag, cp.LastModified = resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
		cp.TTLSeconds = ttl
		cp.NotBefore = notBefore(honorTTL, ttl, req.Now)
	}
	for len(cp.Seen) > 0 && len(encode(cp)) > maxCheckpointBytes {
		cp.Seen = cp.Seen[min(len(cp.Seen), 64):]
	}
	page.Checkpoint = encode(cp)
	return page, nil
}

// CheckCredential makes one request to the feed with the credential, if any. Only a
// 401 or a 403 refuses it: any other outcome, such as an outage, leaves the
// next fetch to report it, so a flaky feed does not block a deposit.
func (f feed) CheckCredential(ctx context.Context, req *quivrplugin.CredentialRequest) (*quivrplugin.CredentialStatus, error) {
	conf, cfg, cred, err := decodeRequest(req.Configuration, req.Connector, req.Credential)
	if err != nil {
		return nil, err
	}
	if cred == nil {
		// Without a credential there is nothing to check.
		return &quivrplugin.CredentialStatus{}, nil
	}
	_, _, err = f.get(ctx, conf, cfg.URL, cred, checkpoint{}, false)
	var classified *quivrplugin.Error
	if errors.As(err, &classified) && classified.Class == quivrplugin.ClassAccess && (classified.Code == "unauthorized" || classified.Code == "forbidden") {
		return nil, err
	}
	return &quivrplugin.CredentialStatus{}, nil
}

// notBefore turns a feed ttl into the earliest next poll, capped so an odd
// ttl cannot silence a source for long.
func notBefore(honor bool, ttlSeconds int, now time.Time) *time.Time {
	if !honor || ttlSeconds <= 0 {
		return nil
	}
	wait := min(time.Duration(ttlSeconds)*time.Second, ttlCap)
	t := now.Add(wait).UTC()
	return &t
}

// encode is the checkpoint JSON, byte for byte what the built-in connector wrote.
func encode(cp checkpoint) json.RawMessage {
	b, _ := json.Marshal(cp)
	return b
}

// boundSeen keeps the revisions of the keys present in the current feed plus
// the most recent others, up to seenCap entries.
func boundSeen(previous []seen, current []candidate, known map[string]string) []seen {
	inFeed := make(map[string]bool, len(current))
	for _, c := range current {
		inFeed[c.hash] = true
	}
	out := make([]seen, 0, len(known))
	for _, s := range previous {
		if !inFeed[s.Key] {
			out = append(out, seen{Key: s.Key, Revision: known[s.Key]})
		}
	}
	for _, c := range current {
		if r, ok := known[c.hash]; ok && inFeed[c.hash] {
			out = append(out, seen{Key: c.hash, Revision: r})
			delete(inFeed, c.hash)
		}
	}
	if len(out) > seenCap {
		out = out[len(out)-seenCap:]
	}
	return out
}

// get fetches the feed. body reads the document; the credential check only
// needs the status.
func (f feed) get(ctx context.Context, conf configuration, rawURL string, cred *feedCredential, cp checkpoint, body bool) (*http.Response, []byte, error) {
	target, err := url.Parse(rawURL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.User != nil {
		return nil, nil, sourceError("invalid_config")
	}
	timeout := f.timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	dialer := &net.Dialer{Timeout: timeout}
	if !conf.AllowPrivateAddresses {
		dialer.Control = refusePrivate
	}
	transport := &http.Transport{}
	if f.transport != nil {
		transport = f.transport.Clone()
	}
	transport.DialContext, transport.Proxy = dialer.DialContext, nil
	transport.TLSHandshakeTimeout, transport.ResponseHeaderTimeout = timeout, timeout
	transport.MaxIdleConns, transport.DisableKeepAlives = 1, true
	client := &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return errTooManyRedirects
		}
		if cred != nil && via[0].URL.Scheme == "https" && next.URL.Scheme != "https" {
			return errInsecureRedirect
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, nil, sourceError("invalid_config")
	}
	request.Header.Set("User-Agent", "Quivr-Connector/1.0 (rss)")
	request.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/rdf+xml, application/xml;q=0.9, text/xml;q=0.8, */*;q=0.5")
	if !cp.Partial {
		if cp.ETag != "" {
			request.Header.Set("If-None-Match", cp.ETag)
		}
		if cp.LastModified != "" {
			request.Header.Set("If-Modified-Since", cp.LastModified)
		}
	}
	if cred != nil {
		if cred.Token != "" {
			request.Header.Set("Authorization", "Bearer "+cred.Token)
		} else {
			request.SetBasicAuth(cred.Username, cred.Password)
		}
	}
	resp, err := client.Do(request)
	if err != nil {
		return nil, nil, classifyTransport(ctx, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return resp, nil, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, nil, accessError("unauthorized")
	case resp.StatusCode == http.StatusForbidden:
		return nil, nil, accessError("forbidden")
	case resp.StatusCode == http.StatusGone:
		return nil, nil, accessError("gone")
	case resp.StatusCode == http.StatusNotFound:
		return nil, nil, sourceError("not_found")
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, nil, quivrplugin.TransientError("rate_limited", "").WithRetryAfter(retryAfter(resp.Header.Get("Retry-After")))
	case resp.StatusCode >= 500:
		return nil, nil, quivrplugin.TransientError("server_error", "").WithRetryAfter(retryAfter(resp.Header.Get("Retry-After")))
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, nil, sourceError("http_status")
	}
	if !body {
		return resp, nil, nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, nil, classifyTransport(ctx, err)
	}
	if len(b) > maxResponseSize {
		return nil, nil, sourceError("response_too_large")
	}
	return resp, b, nil
}

func retryAfter(v string) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	return 0
}

func classifyTransport(ctx context.Context, err error) error {
	var dns *net.DNSError
	var certInvalid x509.CertificateInvalidError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var verification *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	var alert tls.AlertError
	var netErr net.Error
	switch {
	case errors.Is(err, errAddressRefused):
		return sourceError("address_not_allowed")
	case errors.Is(err, errTooManyRedirects):
		return sourceError("too_many_redirects")
	case errors.Is(err, errInsecureRedirect):
		return sourceError("insecure_redirect")
	case ctx.Err() != nil:
		return err // the invocation itself was cancelled
	case errors.As(err, &dns):
		return transientError("dns_error")
	case errors.As(err, &certInvalid), errors.As(err, &unknownAuthority), errors.As(err, &hostname), errors.As(err, &verification), errors.As(err, &record), errors.As(err, &alert):
		return transientError("tls_error")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return transientError("timeout")
	default:
		return transientError("connection_error")
	}
}

type candidate struct {
	item  quivrplugin.Item
	hash  string // hashed Record Key, the checkpoint identity
	short string // shortened revision, the checkpoint change marker
	when  *time.Time
	order int
}

// itemFields are the item-level fields a revision covers. The emitted common
// metadata is added to the revision below so changes to feed language or link
// cannot leave a stored item's source metadata stale.
type itemFields struct {
	GUID       string      `json:"guid,omitempty"`
	Link       string      `json:"link,omitempty"`
	Links      []string    `json:"links,omitempty"`
	Title      string      `json:"title,omitempty"`
	Content    string      `json:"content,omitempty"`
	Summary    string      `json:"summary,omitempty"`
	Authors    []person    `json:"authors,omitempty"`
	Published  string      `json:"published,omitempty"`
	Updated    string      `json:"updated,omitempty"`
	Categories []string    `json:"categories,omitempty"`
	Enclosures []enclosure `json:"enclosures,omitempty"`
	// Truncated records that the item's text exceeded the per-item bound.
	Truncated bool `json:"truncated,omitempty"`
}

type person struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

type enclosure struct {
	URL    string `json:"url"`
	Type   string `json:"type,omitempty"`
	Length string `json:"length,omitempty"`
}

type feedFields struct {
	Format   string `json:"format"`
	Version  string `json:"version,omitempty"`
	Title    string `json:"title,omitempty"`
	Link     string `json:"link,omitempty"`
	Language string `json:"language,omitempty"`
}

// mapFeed maps every item (up to maxFeedItems) to a candidate in submission
// order: oldest first when every item is dated, otherwise the reverse of
// document order (feeds list newest first). Duplicate keys keep their first
// occurrence.
func mapFeed(parsed *gofeed.Feed) []candidate {
	meta := feedFields{Format: parsed.FeedType, Version: parsed.FeedVersion, Title: clip(textOnly(parsed.Title)), Link: clip(parsed.Link), Language: clip(parsed.Language)}
	items := parsed.Items
	if len(items) > maxFeedItems {
		items = items[:maxFeedItems]
	}
	var out []candidate
	keys := map[string]bool{}
	allDated := true
	for i, it := range items {
		if it == nil {
			continue
		}
		c, ok := mapItem(it, meta)
		if !ok || keys[c.hash] {
			continue
		}
		keys[c.hash] = true
		c.order = -i
		if c.when == nil {
			allDated = false
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(a, b int) bool {
		if allDated && !out[a].when.Equal(*out[b].when) {
			return out[a].when.Before(*out[b].when)
		}
		return out[a].order < out[b].order
	})
	return out
}

func mapItem(it *gofeed.Item, meta feedFields) (candidate, bool) {
	f := itemFields{GUID: clip(strings.TrimSpace(it.GUID)), Link: clip(strings.TrimSpace(it.Link)), Title: it.Title, Content: it.Content, Summary: it.Description,
		Published: rfc3339(it.PublishedParsed), Updated: rfc3339(it.UpdatedParsed)}
	for _, l := range it.Links {
		if l = strings.TrimSpace(l); l != "" && l != f.Link && len(f.Links) < maxEnclosures {
			f.Links = append(f.Links, clip(l))
		}
	}
	authors := it.Authors
	if len(authors) == 0 && it.Author != nil {
		authors = []*gofeed.Person{it.Author}
	}
	for _, a := range authors {
		if a != nil && (a.Name != "" || a.Email != "") && len(f.Authors) < maxEnclosures {
			f.Authors = append(f.Authors, person{Name: clip(textOnly(a.Name)), Email: clip(a.Email)})
		}
	}
	for _, c := range it.Categories {
		if c = strings.TrimSpace(c); c != "" && len(f.Categories) < maxCategories {
			f.Categories = append(f.Categories, clip(c))
		}
	}
	for _, e := range it.Enclosures {
		if e != nil && e.URL != "" && len(f.Enclosures) < maxEnclosures {
			f.Enclosures = append(f.Enclosures, enclosure{URL: clip(e.URL), Type: clip(e.Type), Length: clip(e.Length)})
		}
	}

	title := textOnly(f.Title)
	bodySource, summarySource := f.Content, f.Summary
	if strings.TrimSpace(bodySource) == "" {
		bodySource, summarySource = f.Summary, ""
	}
	body, bodyMarkup := htmlToText(bodySource)
	summary, summaryMarkup := htmlToText(summarySource)
	if summary == body {
		summary = ""
	}
	var cut bool
	if title, cut = truncate(title, maxField); cut {
		f.Truncated = true
	}
	if body, cut = truncate(body, maxBodyText); cut {
		f.Truncated = true
	}
	if summary, cut = truncate(summary, maxSummary); cut {
		f.Truncated = true
	}
	// Original markup is preserved only while the item stays within bounds.
	budget := maxItemText - len(title) - len(body) - len(summary)
	keepBodyHTML := bodyMarkup && body != "" && validText(bodySource) && len(bodySource) <= budget
	if keepBodyHTML {
		budget -= len(bodySource)
	}
	keepSummaryHTML := summaryMarkup && summary != "" && validText(summarySource) && len(summarySource) <= budget
	if (bodyMarkup && body != "" && !keepBodyHTML) || (summaryMarkup && summary != "" && !keepSummaryHTML) {
		f.Truncated = true
	}
	if title == "" && body == "" {
		return candidate{}, false
	}
	var parts []quivrplugin.Part
	if title != "" {
		parts = append(parts, quivrplugin.TextPart("title", "title", title))
	}
	if body != "" {
		parts = append(parts, quivrplugin.TextPart("body", "body", body))
		if keepBodyHTML {
			html := quivrplugin.TextPart("body_html", "source_html", bodySource)
			html.ParentKey = "body"
			parts = append(parts, html)
		}
	}
	if summary != "" {
		parts = append(parts, quivrplugin.TextPart("summary", "summary", summary))
		if keepSummaryHTML {
			html := quivrplugin.TextPart("summary_html", "source_html", summarySource)
			html.ParentKey = "summary"
			parts = append(parts, html)
		}
	}

	commonAuthors := make([]string, 0, len(f.Authors))
	for _, author := range f.Authors {
		name := strings.TrimSpace(author.Name)
		if name == "" {
			name = strings.TrimSpace(author.Email)
		}
		if name != "" {
			commonAuthors = append(commonAuthors, name)
		}
	}
	publishedAt := f.Published
	if publishedAt == "" {
		publishedAt = f.Updated
	}
	commonMetadata := quivrplugin.CommonMetadataExtension(quivrplugin.CommonMetadata{
		Language: meta.Language, PublishedAt: publishedAt, SourceType: "rss", Source: meta.Link,
		Author: commonAuthors, Tags: append([]string(nil), f.Categories...),
	})
	// Checkpoints written by older RSS versions contain the item-only revision.
	// After a successful feed fetch they can replay each existing item once to
	// backfill common metadata; a 304 leaves the old checkpoint untouched. The
	// returned checkpoint stores this revision and later polls are stable.
	canonical, _ := json.Marshal(struct {
		Item   itemFields     `json:"item"`
		Common map[string]any `json:"common"`
	}{Item: f, Common: commonMetadata.Data})
	revision := "sha256:" + hash(canonical)
	key := f.GUID
	if key == "" {
		key = f.Link
	}
	if key == "" {
		identity, _ := json.Marshal([]string{title, body})
		key = "sha256:" + hash(identity)
	}
	if len(key) > maxKeyBytes || !validText(key) {
		key = "sha256:" + hash([]byte(key))
	}
	itemData := map[string]any{}
	metaData := map[string]any{}
	// Title and text live in Manifest Parts; the extension keeps the rest.
	described := f
	described.Title, described.Content, described.Summary = "", "", ""
	roundTrip(meta, &metaData)
	for {
		roundTrip(described, &itemData)
		size, _ := json.Marshal(map[string]any{"item": itemData, "feed": metaData})
		if len(size) <= maxExtensionBytes || !shrink(&described) {
			break
		}
		itemData = map[string]any{}
	}
	item := quivrplugin.Item{RecordKey: key, Revision: revision, Content: quivrplugin.NewManifest(parts...),
		Extensions: map[string]quivrplugin.Extension{
			Extension:                           {SchemaVersion: "1", Data: map[string]any{"item": itemData, "feed": metaData}},
			quivrplugin.CommonMetadataNamespace: commonMetadata,
		}}
	sum := sha256.Sum256([]byte(key))
	var when *time.Time
	if it.PublishedParsed != nil {
		when = it.PublishedParsed
	} else if it.UpdatedParsed != nil {
		when = it.UpdatedParsed
	}
	return candidate{item: item, hash: hex.EncodeToString(sum[:8]), short: revision[len("sha256:") : len("sha256:")+16], when: when}, true
}

// shrink halves the longest metadata list; it reports false when nothing is left to drop.
func shrink(f *itemFields) bool {
	f.Truncated = true
	switch n := max(len(f.Categories), len(f.Links), len(f.Authors), len(f.Enclosures)); {
	case n == 0:
		return false
	case len(f.Categories) == n:
		f.Categories = f.Categories[:n/2]
	case len(f.Enclosures) == n:
		f.Enclosures = f.Enclosures[:n/2]
	case len(f.Links) == n:
		f.Links = f.Links[:n/2]
	default:
		f.Authors = f.Authors[:n/2]
	}
	return true
}

// truncate bounds s to at most n bytes on a rune boundary.
func truncate(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// roundTrip converts a typed value into the generic JSON object an Extension carries.
func roundTrip(v any, out *map[string]any) {
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, out)
}

func textOnly(s string) string {
	t, _ := htmlToText(s)
	return strings.Join(strings.Fields(t), " ")
}

func rfc3339(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// clip bounds one metadata string so the extension stays well within the
// generic JSON limit.
func clip(s string) string {
	s, _ = truncate(s, maxField)
	return s
}

// hash is the lowercase hex SHA-256, as the core computes content hashes.
func hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// validText is the core's text rule: valid UTF-8 without NUL.
func validText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }
