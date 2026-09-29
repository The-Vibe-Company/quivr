package connectors

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

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/netguard"
	"github.com/mmcdole/gofeed"
	"github.com/mmcdole/gofeed/rss"
)

// RSSExtension is the namespaced extension carrying item and feed metadata.
const RSSExtension = "connector.rss"

// RSS collection bounds.
const (
	rssDefaultTimeout  = 20 * time.Second
	rssMaxResponseSize = 10 << 20
	rssMaxRedirects    = 5
	rssItemsPerPage    = 250
	rssMaxFeedItems    = 1000
	rssSeenCap         = 2000
	rssTTLCap          = time.Hour
	rssMaxKeyBytes     = 1024
	rssMaxField        = 2048
	rssMaxCategories   = 50
	rssMaxEnclosures   = 20
	// rssMaxItemText bounds the text of one item's Parts, well under the
	// public 1 MiB command limit the connector path would otherwise bypass.
	rssMaxItemText = 768 << 10
	rssMaxBodyText = 384 << 10
	rssMaxSummary  = 64 << 10
	// rssMaxExtensionBytes keeps the extension under the 64 KiB generic JSON bound.
	rssMaxExtensionBytes = 48 << 10
)

// RSS collects RSS 0.9x/2.0, RSS 1.0 (RDF) and Atom feeds. Only the feed
// document is fetched: linked articles and enclosures are never downloaded.
// Each poll emits only items that are new or whose content changed since the
// Acquisition Checkpoint; an item leaving the feed is not withdrawn.
type RSS struct {
	// AllowPrivateAddresses lifts the refusal of loopback, private and
	// link-local destinations. Local and CI deployments only.
	AllowPrivateAddresses bool
	// Timeout overrides the request timeout (tests).
	Timeout time.Duration
	// transport, when set, is the base HTTP transport (tests with TLS servers).
	transport *http.Transport
}

type rssConfig struct {
	URL      string `json:"url"`
	HonorTTL *bool  `json:"honor_ttl"`
}

type rssCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Token    string `json:"token"`
}

// rssCheckpoint resumes polling: HTTP validators, the ttl deadline and the
// revision last emitted per Record Key (hashed and bounded).
type rssCheckpoint struct {
	ETag         string     `json:"etag,omitempty"`
	LastModified string     `json:"last_modified,omitempty"`
	TTLSeconds   int        `json:"ttl_seconds,omitempty"`
	NotBefore    *time.Time `json:"not_before,omitempty"`
	// Partial marks a feed whose changed items did not fit one page: the
	// next page refetches unconditionally.
	Partial bool      `json:"partial,omitempty"`
	Seen    []rssSeen `json:"seen,omitempty"`
}

type rssSeen struct {
	Key      string `json:"k"`
	Revision string `json:"r"`
}

func (RSS) Kind() string                   { return "rss" }
func (RSS) DefaultInterval() time.Duration { return 5 * time.Minute }
func (RSS) ConfigSchema() []byte {
	return []byte(`{"title":"RSS or Atom feed","description":"Collects the entries of an RSS 2.0, RSS 1.0, Atom or JSON Feed document.","type":"object","additionalProperties":false,"required":["url"],"properties":{
"url":{"type":"string","maxLength":2048,"pattern":"^[hH][tT][tT][pP][sS]?://[^/?#@\\s]+([/?#][^\\s]*)?$","title":"Feed URL","description":"http or https address of the feed, without embedded credentials.","examples":["https://example.org/feed.xml"]},
"honor_ttl":{"type":"boolean","title":"Honor the feed's TTL","description":"Poll no more often than the feed's ttl element asks."}}}`)
}
func (RSS) CredentialSchema() []byte {
	return []byte(`{"title":"Feed credential","description":"Only for feeds that require authentication.","oneOf":[
{"title":"Username and password","type":"object","additionalProperties":false,"required":["username","password"],"properties":{"username":{"type":"string","minLength":1,"title":"Username"},"password":{"type":"string","minLength":1,"title":"Password","writeOnly":true}}},
{"title":"Bearer token","type":"object","additionalProperties":false,"required":["token"],"properties":{"token":{"type":"string","minLength":1,"title":"Token","writeOnly":true}}}]}`)
}

var (
	errTooManyRedirects = errors.New("too_many_redirects")
	errInsecureRedirect = errors.New("insecure_redirect")
)

func (r RSS) Fetch(ctx context.Context, req FetchRequest) (Page, error) {
	var cfg rssConfig
	if err := json.Unmarshal(req.Config, &cfg); err != nil || cfg.URL == "" {
		return Page{}, SourceError("invalid_config")
	}
	var cred *rssCredential
	if req.Credential != nil {
		cred = &rssCredential{}
		if err := json.Unmarshal(req.Credential, cred); err != nil {
			return Page{}, AccessError("credential_unreadable")
		}
	}
	var cp rssCheckpoint
	if len(req.Checkpoint) > 0 && string(req.Checkpoint) != "null" {
		if err := json.Unmarshal(req.Checkpoint, &cp); err != nil {
			return Page{}, SourceError("invalid_checkpoint")
		}
	}
	honorTTL := cfg.HonorTTL == nil || *cfg.HonorTTL
	if honorTTL && cp.NotBefore != nil && req.Now.Before(*cp.NotBefore) {
		return Page{}, ErrNotDue
	}

	resp, body, err := r.get(ctx, cfg.URL, cred, cp)
	if err != nil {
		return Page{}, err
	}
	if resp.StatusCode == http.StatusNotModified {
		cp.NotBefore = notBefore(honorTTL, cp.TTLSeconds, req.Now)
		return Page{Checkpoint: marshal(cp)}, nil
	}
	parser := gofeed.NewParser()
	parser.KeepOriginalFeed = true
	feed, err := parser.Parse(strings.NewReader(string(body)))
	if err != nil || feed == nil {
		return Page{}, SourceError("malformed_feed")
	}
	ttl := 0
	if original, ok := feed.OriginalFeed().(*rss.Feed); ok && original.TTL != "" {
		if minutes, err := strconv.Atoi(strings.TrimSpace(original.TTL)); err == nil && minutes > 0 {
			ttl = minutes * 60
		}
	}

	candidates := mapFeed(feed)
	seen := make(map[string]string, len(cp.Seen))
	for _, s := range cp.Seen {
		seen[s.Key] = s.Revision
	}
	var changed []rssCandidate
	for _, c := range candidates {
		if seen[c.hash] != c.short {
			changed = append(changed, c)
		}
	}
	page := Page{}
	if len(changed) > rssItemsPerPage {
		changed, page.More = changed[:rssItemsPerPage], true
	}
	for _, c := range changed {
		page.Items = append(page.Items, c.item)
		seen[c.hash] = c.short
	}
	cp.Seen = boundSeen(cp.Seen, candidates, seen)
	cp.Partial = page.More
	if page.More {
		cp.ETag, cp.LastModified, cp.NotBefore = "", "", nil
	} else {
		cp.ETag, cp.LastModified = resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
		cp.TTLSeconds = ttl
		cp.NotBefore = notBefore(honorTTL, ttl, req.Now)
	}
	page.Checkpoint = marshal(cp)
	return page, nil
}

// notBefore turns a feed ttl into the earliest next poll, capped so an odd
// ttl cannot silence a source for long.
func notBefore(honor bool, ttlSeconds int, now time.Time) *time.Time {
	if !honor || ttlSeconds <= 0 {
		return nil
	}
	wait := min(time.Duration(ttlSeconds)*time.Second, rssTTLCap)
	t := now.Add(wait).UTC()
	return &t
}

func marshal(cp rssCheckpoint) json.RawMessage {
	b, _ := json.Marshal(cp)
	return b
}

// boundSeen keeps the revisions of the keys present in the current feed plus
// the most recent others, up to rssSeenCap entries.
func boundSeen(previous []rssSeen, current []rssCandidate, seen map[string]string) []rssSeen {
	inFeed := make(map[string]bool, len(current))
	for _, c := range current {
		inFeed[c.hash] = true
	}
	out := make([]rssSeen, 0, len(seen))
	for _, s := range previous {
		if !inFeed[s.Key] {
			out = append(out, rssSeen{Key: s.Key, Revision: seen[s.Key]})
		}
	}
	for _, c := range current {
		if r, ok := seen[c.hash]; ok && inFeed[c.hash] {
			out = append(out, rssSeen{Key: c.hash, Revision: r})
			delete(inFeed, c.hash)
		}
	}
	if len(out) > rssSeenCap {
		out = out[len(out)-rssSeenCap:]
	}
	return out
}

func (r RSS) get(ctx context.Context, rawURL string, cred *rssCredential, cp rssCheckpoint) (*http.Response, []byte, error) {
	target, err := url.Parse(rawURL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.User != nil {
		return nil, nil, SourceError("invalid_config")
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = rssDefaultTimeout
	}
	dialer := &net.Dialer{Timeout: timeout}
	if !r.AllowPrivateAddresses {
		dialer.Control = netguard.Control
	}
	transport := &http.Transport{}
	if r.transport != nil {
		transport = r.transport.Clone()
	}
	transport.DialContext, transport.Proxy = dialer.DialContext, nil
	transport.TLSHandshakeTimeout, transport.ResponseHeaderTimeout = timeout, timeout
	transport.MaxIdleConns, transport.DisableKeepAlives = 1, true
	client := &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) > rssMaxRedirects {
			return errTooManyRedirects
		}
		if cred != nil && via[0].URL.Scheme == "https" && next.URL.Scheme != "https" {
			return errInsecureRedirect
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, nil, SourceError("invalid_config")
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
		return nil, nil, AccessError("unauthorized")
	case resp.StatusCode == http.StatusForbidden:
		return nil, nil, AccessError("forbidden")
	case resp.StatusCode == http.StatusGone:
		return nil, nil, AccessError("gone")
	case resp.StatusCode == http.StatusNotFound:
		return nil, nil, SourceError("not_found")
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, nil, &Error{Class: ClassTransient, Code: "rate_limited", RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	case resp.StatusCode >= 500:
		return nil, nil, &Error{Class: ClassTransient, Code: "server_error", RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, nil, SourceError("http_status")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, rssMaxResponseSize+1))
	if err != nil {
		return nil, nil, classifyTransport(ctx, err)
	}
	if len(body) > rssMaxResponseSize {
		return nil, nil, SourceError("response_too_large")
	}
	return resp, body, nil
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
	case errors.Is(err, netguard.ErrRefused):
		return SourceError("address_not_allowed")
	case errors.Is(err, errTooManyRedirects):
		return SourceError("too_many_redirects")
	case errors.Is(err, errInsecureRedirect):
		return SourceError("insecure_redirect")
	case ctx.Err() != nil:
		return err // the run itself was cancelled
	case errors.As(err, &dns):
		return TransientError("dns_error")
	case errors.As(err, &certInvalid), errors.As(err, &unknownAuthority), errors.As(err, &hostname), errors.As(err, &verification), errors.As(err, &record), errors.As(err, &alert):
		return TransientError("tls_error")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return TransientError("timeout")
	default:
		return TransientError("connection_error")
	}
}

type rssCandidate struct {
	item  Item
	hash  string // hashed Record Key, the checkpoint identity
	short string // shortened revision, the checkpoint change marker
	when  *time.Time
	order int
}

// rssItemFields are the item-level fields a revision covers. Feed metadata
// is deliberately excluded: renaming a feed must not correct every item.
type rssItemFields struct {
	GUID       string         `json:"guid,omitempty"`
	Link       string         `json:"link,omitempty"`
	Links      []string       `json:"links,omitempty"`
	Title      string         `json:"title,omitempty"`
	Content    string         `json:"content,omitempty"`
	Summary    string         `json:"summary,omitempty"`
	Authors    []rssPerson    `json:"authors,omitempty"`
	Published  string         `json:"published,omitempty"`
	Updated    string         `json:"updated,omitempty"`
	Categories []string       `json:"categories,omitempty"`
	Enclosures []rssEnclosure `json:"enclosures,omitempty"`
	// Truncated records that the item's text exceeded the per-item bound.
	Truncated bool `json:"truncated,omitempty"`
}

type rssPerson struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

type rssEnclosure struct {
	URL    string `json:"url"`
	Type   string `json:"type,omitempty"`
	Length string `json:"length,omitempty"`
}

type rssFeedFields struct {
	Format   string `json:"format"`
	Version  string `json:"version,omitempty"`
	Title    string `json:"title,omitempty"`
	Link     string `json:"link,omitempty"`
	Language string `json:"language,omitempty"`
}

// mapFeed maps every item (up to rssMaxFeedItems) to a candidate in
// submission order: oldest first when every item is dated, otherwise the
// reverse of document order (feeds list newest first). Duplicate keys keep
// their first occurrence.
func mapFeed(feed *gofeed.Feed) []rssCandidate {
	meta := rssFeedFields{Format: feed.FeedType, Version: feed.FeedVersion, Title: clip(textOnly(feed.Title)), Link: clip(feed.Link), Language: clip(feed.Language)}
	items := feed.Items
	if len(items) > rssMaxFeedItems {
		items = items[:rssMaxFeedItems]
	}
	var out []rssCandidate
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

func mapItem(it *gofeed.Item, meta rssFeedFields) (rssCandidate, bool) {
	f := rssItemFields{GUID: clip(strings.TrimSpace(it.GUID)), Link: clip(strings.TrimSpace(it.Link)), Title: it.Title, Content: it.Content, Summary: it.Description,
		Published: rfc3339(it.PublishedParsed), Updated: rfc3339(it.UpdatedParsed)}
	for _, l := range it.Links {
		if l = strings.TrimSpace(l); l != "" && l != f.Link && len(f.Links) < rssMaxEnclosures {
			f.Links = append(f.Links, clip(l))
		}
	}
	authors := it.Authors
	if len(authors) == 0 && it.Author != nil {
		authors = []*gofeed.Person{it.Author}
	}
	for _, a := range authors {
		if a != nil && (a.Name != "" || a.Email != "") && len(f.Authors) < rssMaxEnclosures {
			f.Authors = append(f.Authors, rssPerson{Name: clip(textOnly(a.Name)), Email: clip(a.Email)})
		}
	}
	for _, c := range it.Categories {
		if c = strings.TrimSpace(c); c != "" && len(f.Categories) < rssMaxCategories {
			f.Categories = append(f.Categories, clip(c))
		}
	}
	for _, e := range it.Enclosures {
		if e != nil && e.URL != "" && len(f.Enclosures) < rssMaxEnclosures {
			f.Enclosures = append(f.Enclosures, rssEnclosure{URL: clip(e.URL), Type: clip(e.Type), Length: clip(e.Length)})
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
	if title, cut = truncate(title, rssMaxField); cut {
		f.Truncated = true
	}
	if body, cut = truncate(body, rssMaxBodyText); cut {
		f.Truncated = true
	}
	if summary, cut = truncate(summary, rssMaxSummary); cut {
		f.Truncated = true
	}
	// Original markup is preserved only while the item stays within bounds.
	budget := rssMaxItemText - len(title) - len(body) - len(summary)
	keepBodyHTML := bodyMarkup && body != "" && content.ValidText(bodySource) && len(bodySource) <= budget
	if keepBodyHTML {
		budget -= len(bodySource)
	}
	keepSummaryHTML := summaryMarkup && summary != "" && content.ValidText(summarySource) && len(summarySource) <= budget
	if (bodyMarkup && body != "" && !keepBodyHTML) || (summaryMarkup && summary != "" && !keepSummaryHTML) {
		f.Truncated = true
	}
	if title == "" && body == "" {
		return rssCandidate{}, false
	}
	m := &content.Manifest{Kind: "manifest"}
	if title != "" {
		m.Parts = append(m.Parts, content.Part{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: title}})
	}
	if body != "" {
		m.Parts = append(m.Parts, content.Part{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: body}})
		if keepBodyHTML {
			m.Parts = append(m.Parts, content.Part{Key: "body_html", ParentKey: "body", Role: "source_html", Content: content.Text{Kind: "text", Text: bodySource}})
		}
	}
	if summary != "" {
		m.Parts = append(m.Parts, content.Part{Key: "summary", Role: "summary", Content: content.Text{Kind: "text", Text: summary}})
		if keepSummaryHTML {
			m.Parts = append(m.Parts, content.Part{Key: "summary_html", ParentKey: "summary", Role: "source_html", Content: content.Text{Kind: "text", Text: summarySource}})
		}
	}

	canonical, _ := json.Marshal(f)
	revision := "sha256:" + content.Hash(canonical)
	key := f.GUID
	if key == "" {
		key = f.Link
	}
	if key == "" {
		identity, _ := json.Marshal([]string{title, body})
		key = "sha256:" + content.Hash(identity)
	}
	if len(key) > rssMaxKeyBytes || !content.ValidText(key) {
		key = "sha256:" + content.Hash([]byte(key))
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
		if len(size) <= rssMaxExtensionBytes || !shrink(&described) {
			break
		}
		itemData = map[string]any{}
	}
	item := Item{RecordKey: key, Revision: revision, Manifest: m, Extensions: content.Extensions{RSSExtension: {SchemaVersion: "1", Data: map[string]any{"item": itemData, "feed": metaData}}}}
	sum := sha256.Sum256([]byte(key))
	var when *time.Time
	if it.PublishedParsed != nil {
		when = it.PublishedParsed
	} else if it.UpdatedParsed != nil {
		when = it.UpdatedParsed
	}
	return rssCandidate{item: item, hash: hex.EncodeToString(sum[:8]), short: revision[len("sha256:") : len("sha256:")+16], when: when}, true
}

// shrink halves the longest metadata list; it reports false when nothing is left to drop.
func shrink(f *rssItemFields) bool {
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
	for n > 0 && !utf8RuneStart(s[n]) {
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
	if len(s) <= rssMaxField {
		return s
	}
	cut := rssMaxField
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
