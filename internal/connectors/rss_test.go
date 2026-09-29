package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// feedServer serves a mutable feed document with validators, recording the
// conditional headers and paths it receives.
type feedServer struct {
	mu       sync.Mutex
	body     string
	etag     string
	status   int
	paths    []string
	inm      []string
	auth     []string
	notModif int
	*httptest.Server
}

func newFeedServer(t *testing.T, body string) *feedServer {
	t.Helper()
	f := &feedServer{body: body, etag: `"v1"`}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.paths = append(f.paths, r.URL.Path)
		f.inm = append(f.inm, r.Header.Get("If-None-Match"))
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		if f.etag != "" && r.Header.Get("If-None-Match") == f.etag {
			f.notModif++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", f.etag)
		w.Header().Set("Last-Modified", "Tue, 01 Sep 2026 10:00:00 GMT")
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, f.body)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *feedServer) set(body, etag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body, f.etag = body, etag
}

func testdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/rss/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var testNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func rssFetch(t *testing.T, r RSS, config string, credential string, checkpoint json.RawMessage) (Page, error) {
	t.Helper()
	req := FetchRequest{Config: json.RawMessage(config), Checkpoint: checkpoint, Now: testNow}
	if credential != "" {
		req.Credential = json.RawMessage(credential)
	}
	return r.Fetch(context.Background(), req)
}

func local() RSS { return RSS{AllowPrivateAddresses: true} }

func cfg(url string, extra ...string) string {
	return `{"url":"` + url + `"` + strings.Join(extra, "") + `}`
}

func part(t *testing.T, item Item, key string) content.Part {
	t.Helper()
	for _, p := range item.Manifest.Parts {
		if p.Key == key {
			return p
		}
	}
	t.Fatalf("item %s has no part %q: %+v", item.RecordKey, key, item.Manifest.Parts)
	return content.Part{}
}

func hasPart(item Item, key string) bool {
	for _, p := range item.Manifest.Parts {
		if p.Key == key {
			return true
		}
	}
	return false
}

func rssData(t *testing.T, item Item) map[string]any {
	t.Helper()
	ext, ok := item.Extensions[RSSExtension]
	if !ok || ext.SchemaVersion != "1" {
		t.Fatalf("missing %s extension: %+v", RSSExtension, item.Extensions)
	}
	if err := (content.BuiltinExtensions{}).Validate(context.Background(), item.Extensions); err != nil {
		t.Fatalf("extension does not validate against the declared schema: %v", err)
	}
	return ext.Data
}

func TestRSSMapsRSS2ItemsToStructuredManifests(t *testing.T) {
	srv := newFeedServer(t, testdata(t, "rss2.xml"))
	page, err := rssFetch(t, local(), cfg(srv.URL+"/feed"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.More {
		t.Fatalf("items %d more %v", len(page.Items), page.More)
	}
	// Oldest first: the item without guid falls back to its link.
	first, second := page.Items[0], page.Items[1]
	if first.RecordKey != "https://news.example.org/first" || second.RecordKey != "example-2" {
		t.Fatalf("keys %q %q", first.RecordKey, second.RecordKey)
	}
	if !strings.HasPrefix(second.Revision, "sha256:") || first.Revision == second.Revision {
		t.Fatalf("revisions %q %q", first.Revision, second.Revision)
	}
	if second.Manifest.Kind != "manifest" || part(t, second, "title").Role != "title" || part(t, second, "title").Content.Text != "Second story" {
		t.Fatalf("title %+v", second.Manifest)
	}
	body := part(t, second, "body")
	if body.Role != "body" || body.Content.Text != "Full body text & details.\nSecond paragraph." {
		t.Fatalf("body %q", body.Content.Text)
	}
	if s := part(t, second, "summary"); s.Role != "summary" || s.Content.Text != "Short summary of the story." {
		t.Fatalf("summary %+v", s)
	}
	html := part(t, second, "body_html")
	if html.Role != "source_html" || html.ParentKey != "body" || !strings.Contains(html.Content.Text, "<script>alert(1)</script>") {
		t.Fatalf("original html not preserved: %+v", html)
	}
	if !hasPart(second, "summary_html") || hasPart(first, "body_html") || hasPart(first, "summary") {
		t.Fatalf("source_html only when markup exists: %+v / %+v", second.Manifest.Parts, first.Manifest.Parts)
	}
	data := rssData(t, second)
	item, feed := data["item"].(map[string]any), data["feed"].(map[string]any)
	if item["guid"] != "example-2" || item["link"] != "https://news.example.org/second" || item["published"] != "2026-09-01T10:00:00Z" {
		t.Fatalf("item metadata %v", item)
	}
	if fmt.Sprint(item["categories"]) != "[Economy Europe]" || fmt.Sprint(item["authors"]) != "[map[name:Jane Reporter]]" {
		t.Fatalf("item categories/authors %v", item)
	}
	if fmt.Sprint(item["enclosures"]) != "[map[length:1234 type:audio/mpeg url:https://news.example.org/audio.mp3]]" {
		t.Fatalf("enclosures %v", item["enclosures"])
	}
	if feed["format"] != "rss" || feed["version"] != "2.0" || feed["title"] != "Example Newsroom" || feed["language"] != "fr" {
		t.Fatalf("feed metadata %v", feed)
	}
	// Nothing but the feed document is fetched: no linked page, no enclosure.
	if fmt.Sprint(srv.paths) != "[/feed]" {
		t.Fatalf("fetched %v", srv.paths)
	}
}

func TestRSSParsesRSS1AndAtom(t *testing.T) {
	rdf := newFeedServer(t, testdata(t, "rss1.rdf"))
	page, err := rssFetch(t, local(), cfg(rdf.URL), "", nil)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("rdf %v %+v", err, page)
	}
	it := page.Items[0]
	if it.RecordKey != "https://journal.example.org/a" || part(t, it, "body").Content.Text != "RDF item description." {
		t.Fatalf("rdf item %+v", it)
	}
	if d := rssData(t, it); d["feed"].(map[string]any)["version"] != "1.0" || d["item"].(map[string]any)["published"] != "2026-09-02T08:30:00Z" {
		t.Fatalf("rdf data %v", d)
	}

	atom := newFeedServer(t, testdata(t, "atom.xml"))
	page, err = rssFetch(t, local(), cfg(atom.URL), "", nil)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("atom %v %+v", err, page)
	}
	it = page.Items[0]
	if it.RecordKey != "urn:uuid:1225c695-cfb8-4ebb-aaaa-80da344efa6a" || part(t, it, "title").Content.Text != "Atom entry" {
		t.Fatalf("atom item %+v", it)
	}
	if part(t, it, "body").Content.Text != "Entry content." || part(t, it, "summary").Content.Text != "Entry summary." {
		t.Fatalf("atom body %+v", it.Manifest.Parts)
	}
	d := rssData(t, it)
	if d["feed"].(map[string]any)["format"] != "atom" || fmt.Sprint(d["item"].(map[string]any)["authors"]) != "[map[email:alex@example.org name:Alex Writer]]" || d["item"].(map[string]any)["updated"] != "2026-09-03T12:00:00Z" {
		t.Fatalf("atom data %v", d)
	}
}

func TestRSSKeyFallsBackToAContentHash(t *testing.T) {
	feed := `<rss version="2.0"><channel><title>T</title><item><title>Only a title</title><description>Body</description></item></channel></rss>`
	srv := newFeedServer(t, feed)
	page, err := rssFetch(t, local(), cfg(srv.URL), "", nil)
	if err != nil || len(page.Items) != 1 || !strings.HasPrefix(page.Items[0].RecordKey, "sha256:") {
		t.Fatalf("%v %+v", err, page)
	}
	long := `<rss version="2.0"><channel><title>T</title><item><guid>` + strings.Repeat("g", 2000) + `</guid><title>x</title></item></channel></rss>`
	srv.set(long, `"long"`)
	page, err = rssFetch(t, local(), cfg(srv.URL), "", nil)
	if err != nil || len(page.Items) != 1 || !strings.HasPrefix(page.Items[0].RecordKey, "sha256:") {
		t.Fatalf("oversized guid must be hashed: %v %+v", err, page)
	}
}

func TestRSSEmitsOnlyNewOrChangedItemsAndIgnoresFeedMetadata(t *testing.T) {
	feed := testdata(t, "rss2.xml")
	srv := newFeedServer(t, feed)
	first, err := rssFetch(t, local(), cfg(srv.URL, `,"honor_ttl":false`), "", nil)
	if err != nil || len(first.Items) != 2 {
		t.Fatalf("%v %+v", err, first)
	}
	// Feed metadata changes (new ETag, full body): no item changed, nothing emitted.
	srv.set(strings.Replace(feed, "Example Newsroom", "Renamed Newsroom", 1), `"v2"`)
	again, err := rssFetch(t, local(), cfg(srv.URL, `,"honor_ttl":false`), "", first.Checkpoint)
	if err != nil || len(again.Items) != 0 {
		t.Fatalf("unchanged items re-emitted: %v %+v", err, again.Items)
	}
	// An edited item is emitted again with a new revision (a correction).
	srv.set(strings.Replace(feed, "Plain first story text.", "Plain first story text, corrected.", 1), `"v3"`)
	edited, err := rssFetch(t, local(), cfg(srv.URL, `,"honor_ttl":false`), "", again.Checkpoint)
	if err != nil || len(edited.Items) != 1 || edited.Items[0].RecordKey != first.Items[0].RecordKey || edited.Items[0].Revision == first.Items[0].Revision {
		t.Fatalf("correction %v %+v", err, edited.Items)
	}
	// An item dropping out of the feed produces nothing (no withdrawal).
	srv.set(`<rss version="2.0"><channel><title>T</title></channel></rss>`, `"v4"`)
	gone, err := rssFetch(t, local(), cfg(srv.URL, `,"honor_ttl":false`), "", edited.Checkpoint)
	if err != nil || len(gone.Items) != 0 {
		t.Fatalf("dropped item %v %+v", err, gone.Items)
	}
}

func TestRSSUsesConditionalGetAndTreats304AsASuccessfulPoll(t *testing.T) {
	srv := newFeedServer(t, testdata(t, "rss2.xml"))
	first, err := rssFetch(t, local(), cfg(srv.URL, `,"honor_ttl":false`), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var cp rssCheckpoint
	if err = json.Unmarshal(first.Checkpoint, &cp); err != nil || cp.ETag != `"v1"` || cp.LastModified == "" {
		t.Fatalf("validators not kept: %v %s", err, first.Checkpoint)
	}
	second, err := rssFetch(t, local(), cfg(srv.URL, `,"honor_ttl":false`), "", first.Checkpoint)
	if err != nil || len(second.Items) != 0 || srv.notModif != 1 || srv.inm[1] != `"v1"` {
		t.Fatalf("304 path: %v items=%d 304s=%d inm=%v", err, len(second.Items), srv.notModif, srv.inm)
	}
	if string(second.Checkpoint) != string(first.Checkpoint) {
		t.Fatalf("304 changed the checkpoint: %s -> %s", first.Checkpoint, second.Checkpoint)
	}
}

func TestRSSHonorsTheFeedTTLUpToAnHour(t *testing.T) {
	srv := newFeedServer(t, testdata(t, "rss2.xml")) // ttl 15 minutes
	page, err := rssFetch(t, local(), cfg(srv.URL), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var cp rssCheckpoint
	_ = json.Unmarshal(page.Checkpoint, &cp)
	if cp.NotBefore == nil || !cp.NotBefore.Equal(testNow.Add(15*time.Minute)) {
		t.Fatalf("not_before %v", cp.NotBefore)
	}
	if _, err = rssFetch(t, local(), cfg(srv.URL), "", page.Checkpoint); !errors.Is(err, ErrNotDue) || len(srv.paths) != 1 {
		t.Fatalf("a run inside the ttl must be skipped without polling: %v %v", err, srv.paths)
	}
	srv.set(strings.Replace(testdata(t, "rss2.xml"), "<ttl>15</ttl>", "<ttl>1440</ttl>", 1), `"day"`)
	page, err = rssFetch(t, local(), cfg(srv.URL), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(page.Checkpoint, &cp)
	if cp.NotBefore == nil || !cp.NotBefore.Equal(testNow.Add(time.Hour)) {
		t.Fatalf("ttl must be capped at 60 min: %v", cp.NotBefore)
	}
	page, err = rssFetch(t, local(), cfg(srv.URL, `,"honor_ttl":false`), "", nil)
	cp = rssCheckpoint{}
	if _ = json.Unmarshal(page.Checkpoint, &cp); err != nil || cp.NotBefore != nil {
		t.Fatalf("honor_ttl=false must ignore ttl: %v %v", err, cp.NotBefore)
	}
}

func TestRSSMapsFailuresToHealthClasses(t *testing.T) {
	srv := newFeedServer(t, "")
	for status, want := range map[int]*Error{
		401: {Class: ClassAccess, Code: "unauthorized"},
		403: {Class: ClassAccess, Code: "forbidden"},
		410: {Class: ClassAccess, Code: "gone"},
		404: {Class: ClassSource, Code: "not_found"},
		418: {Class: ClassSource, Code: "http_status"},
		429: {Class: ClassTransient, Code: "rate_limited"},
		500: {Class: ClassTransient, Code: "server_error"},
		503: {Class: ClassTransient, Code: "server_error"},
	} {
		srv.mu.Lock()
		srv.status = status
		srv.mu.Unlock()
		_, err := rssFetch(t, local(), cfg(srv.URL), "", nil)
		var typed *Error
		if !errors.As(err, &typed) || typed.Class != want.Class || typed.Code != want.Code {
			t.Errorf("status %d: got %v want %v", status, err, want)
		}
	}
	for name, tc := range map[string]struct {
		body string
		code string
	}{
		"html page": {`<!doctype html><html><body>Not a feed</body></html>`, "malformed_feed"},
		"truncated": {`<rss version="2.0"><channel><title>T</title><item><title>a</title>`, "malformed_feed"},
		"empty":     {``, "malformed_feed"},
	} {
		srv.mu.Lock()
		srv.status = 0
		srv.mu.Unlock()
		srv.set(tc.body, `"`+name+`"`)
		page, err := rssFetch(t, local(), cfg(srv.URL), "", nil)
		var typed *Error
		if !errors.As(err, &typed) || typed.Class != ClassSource || typed.Code != tc.code || len(page.Items) != 0 {
			t.Errorf("%s: %v %+v", name, err, page)
		}
	}
	// Unresolvable host.
	_, err := rssFetch(t, local(), cfg("http://feed.invalid/rss"), "", nil)
	var typed *Error
	if !errors.As(err, &typed) || typed.Class != ClassTransient || typed.Code != "dns_error" {
		t.Errorf("dns: %v", err)
	}
	// A bad checkpoint is a source error, not a crash.
	if _, err = rssFetch(t, local(), cfg(srv.URL), "", json.RawMessage(`"nope"`)); !errors.As(err, &typed) || typed.Code != "invalid_checkpoint" {
		t.Errorf("checkpoint: %v", err)
	}
}

func TestRSSBoundsSizeRedirectsAndTime(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<rss version="2.0"><channel><title>`+strings.Repeat("x", 64<<10)+`</title></channel></rss>`)
	}))
	defer big.Close()
	small := RSS{AllowPrivateAddresses: true, MaxBytes: 32 << 10}
	var typed *Error
	if _, err := rssFetch(t, small, cfg(big.URL), "", nil); !errors.As(err, &typed) || typed.Code != "response_too_large" {
		t.Errorf("size: %v", err)
	}
	var loop *httptest.Server
	loop = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, loop.URL+r.URL.Path+"x", http.StatusFound)
	}))
	defer loop.Close()
	if _, err := rssFetch(t, local(), cfg(loop.URL+"/r"), "", nil); !errors.As(err, &typed) || typed.Code != "too_many_redirects" {
		t.Errorf("redirects: %v", err)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	quick := RSS{AllowPrivateAddresses: true, Timeout: 100 * time.Millisecond}
	if _, err := rssFetch(t, quick, cfg(slow.URL), "", nil); !errors.As(err, &typed) || typed.Class != ClassTransient || typed.Code != "timeout" {
		t.Errorf("timeout: %v", err)
	}
}

func TestRSSRefusesPrivateAddressesByDefault(t *testing.T) {
	srv := newFeedServer(t, testdata(t, "rss2.xml"))
	var typed *Error
	if _, err := rssFetch(t, RSS{}, cfg(srv.URL), "", nil); !errors.As(err, &typed) || typed.Class != ClassSource || typed.Code != "address_not_allowed" {
		t.Fatalf("loopback fetched: %v", err)
	}
	if len(srv.paths) != 0 {
		t.Fatalf("request reached a private address: %v", srv.paths)
	}
}

func TestRSSSendsDepositedHTTPCredentials(t *testing.T) {
	srv := newFeedServer(t, testdata(t, "rss2.xml"))
	if _, err := rssFetch(t, local(), cfg(srv.URL), `{"username":"reader","password":"pw-test"}`, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := rssFetch(t, local(), cfg(srv.URL), `{"token":"tok-test"}`, nil); err != nil {
		t.Fatal(err)
	}
	if srv.auth[0] != "Basic cmVhZGVyOnB3LXRlc3Q=" || srv.auth[1] != "Bearer tok-test" {
		t.Fatalf("auth headers %v", srv.auth)
	}
}

func TestRSSPagesALargeFeedAndKeepsValidatorsUntilDrained(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<rss version="2.0"><channel><title>Big</title>`)
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, `<item><guid>g%d</guid><title>Item %d</title></item>`, i, i)
	}
	b.WriteString(`</channel></rss>`)
	srv := newFeedServer(t, b.String())
	first, err := rssFetch(t, local(), cfg(srv.URL), "", nil)
	if err != nil || len(first.Items) != rssItemsPerPage || !first.More {
		t.Fatalf("first page %v %d %v", err, len(first.Items), first.More)
	}
	var cp rssCheckpoint
	_ = json.Unmarshal(first.Checkpoint, &cp)
	if cp.ETag != "" || !cp.Partial {
		t.Fatalf("validators committed before the feed was drained: %s", first.Checkpoint)
	}
	second, err := rssFetch(t, local(), cfg(srv.URL), "", first.Checkpoint)
	if err != nil || len(second.Items) != 50 || second.More || srv.inm[1] != "" {
		t.Fatalf("second page %v %d %v inm=%q", err, len(second.Items), second.More, srv.inm[1])
	}
	cp = rssCheckpoint{}
	_ = json.Unmarshal(second.Checkpoint, &cp)
	if cp.ETag != `"v1"` || cp.Partial {
		t.Fatalf("drained checkpoint %s", second.Checkpoint)
	}
}

func TestRSSSchemasValidateConfigAndCredential(t *testing.T) {
	registry, err := NewRegistry(RSS{})
	if err != nil {
		t.Fatal(err)
	}
	ok := func(config, secret string) error {
		var s json.RawMessage
		if secret != "" {
			s = json.RawMessage(secret)
		}
		return registry.validate("rss", json.RawMessage(config), s, "/credential/secret")
	}
	for _, c := range []string{`{"url":"https://news.example.org/rss"}`, `{"url":"http://news.example.org/rss","honor_ttl":false}`} {
		if err := ok(c, ""); err != nil {
			t.Errorf("%s: %v", c, err)
		}
	}
	for _, c := range []string{`{}`, `{"url":"ftp://news.example.org/rss"}`, `{"url":"https://user:pw@news.example.org/rss"}`, `{"url":"https://news.example.org/rss","extra":1}`} {
		if err := ok(c, ""); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s accepted: %v", c, err)
		}
	}
	for _, s := range []string{`{"username":"u","password":"p"}`, `{"token":"t"}`} {
		if err := ok(`{"url":"https://news.example.org/rss"}`, s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{`{"username":"u"}`, `{"token":"t","username":"u","password":"p"}`, `{}`} {
		if err := ok(`{"url":"https://news.example.org/rss"}`, s); !errors.Is(err, ErrInvalidCredential) {
			t.Errorf("%s accepted: %v", s, err)
		}
	}
}

func TestRSSNeverDowngradesACredentialToPlainHTTP(t *testing.T) {
	plain := newFeedServer(t, testdata(t, "rss2.xml"))
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer secure.Close()
	r := local()
	r.transport = secure.Client().Transport.(*http.Transport).Clone()
	var typed *Error
	if _, err := rssFetch(t, r, cfg(secure.URL), `{"token":"tok-test"}`, nil); !errors.As(err, &typed) || typed.Code != "insecure_redirect" {
		t.Fatalf("downgrade followed: %v", err)
	}
	if len(plain.auth) != 0 {
		t.Fatalf("credential reached the plain-HTTP target: %v", plain.auth)
	}
}

func TestRSSBoundsOversizedItemsAndMetadata(t *testing.T) {
	var cats strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&cats, "<category>%d-%s</category>", i, strings.Repeat("c", 2000))
	}
	huge := `<rss version="2.0"><channel><title>T</title><item><guid>big</guid><title>Big</title>` + cats.String() +
		`<description><![CDATA[<p>` + strings.Repeat("word ", 200000) + `</p>]]></description></item></channel></rss>`
	srv := newFeedServer(t, huge)
	page, err := rssFetch(t, RSS{AllowPrivateAddresses: true}, cfg(srv.URL), "", nil)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("%v %+v", err, page)
	}
	it := page.Items[0]
	total := 0
	for _, p := range it.Manifest.Parts {
		total += len(p.Content.Text)
		if !content.ValidText(p.Content.Text) {
			t.Fatalf("invalid text in %s", p.Key)
		}
	}
	if total > rssMaxItemText || hasPart(it, "body_html") {
		t.Fatalf("item text %d bytes (limit %d), html kept %v", total, rssMaxItemText, hasPart(it, "body_html"))
	}
	data := rssData(t, it)
	if data["item"].(map[string]any)["truncated"] != true {
		t.Fatalf("truncation not recorded: %v", data["item"])
	}
	raw, _ := json.Marshal(it.Extensions)
	if len(raw) > rssMaxExtensionBytes {
		t.Fatalf("extension %d bytes", len(raw))
	}
}
