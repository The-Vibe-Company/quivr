package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
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
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var testNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// allowPrivate is the local-stack plugin configuration: the tests' feeds are on loopback.
const allowPrivate = `{"allow_private_addresses":true}`

// request builds a fetch request the way the SDK decodes one from the core.
func request(t *testing.T, configuration, config, credential string, checkpoint json.RawMessage, now time.Time) *quivrplugin.FetchRequest {
	t.Helper()
	if credential == "" {
		credential = "null"
	}
	if checkpoint == nil {
		checkpoint = json.RawMessage("null")
	}
	body := fmt.Sprintf(`{"invocation_id":"inv","contribution":"connector","organization_id":"org","configuration":%s,"connector":{"instance_id":"c","kind":"rss","config":%s},"credential":%s,"checkpoint":%s,"now":%q,"page_in_run":0,"reads_today":0}`,
		configuration, config, credential, checkpoint, now.Format(time.RFC3339))
	var req quivrplugin.FetchRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	return &req
}

func rssFetch(t *testing.T, f feed, config, credential string, checkpoint json.RawMessage) (*quivrplugin.Page, error) {
	t.Helper()
	return f.Fetch(context.Background(), request(t, allowPrivate, config, credential, checkpoint, testNow))
}

func cfg(url string, extra ...string) string {
	return `{"url":"` + url + `"` + strings.Join(extra, "") + `}`
}

func cp(p *quivrplugin.Page) json.RawMessage { return p.Checkpoint.(json.RawMessage) }

func part(t *testing.T, item quivrplugin.Item, key string) quivrplugin.Part {
	t.Helper()
	for _, p := range item.Content.Parts {
		if p.Key == key {
			return p
		}
	}
	t.Fatalf("item %s has no part %q: %+v", item.RecordKey, key, item.Content.Parts)
	return quivrplugin.Part{}
}

func hasPart(item quivrplugin.Item, key string) bool {
	for _, p := range item.Content.Parts {
		if p.Key == key {
			return true
		}
	}
	return false
}

func rssData(t *testing.T, item quivrplugin.Item) map[string]any {
	t.Helper()
	ext, ok := item.Extensions[Extension]
	if !ok || ext.SchemaVersion != "1" {
		t.Fatalf("missing %s extension: %+v", Extension, item.Extensions)
	}
	return ext.Data
}

func errorOf(err error) *quivrplugin.Error {
	var typed *quivrplugin.Error
	if errors.As(err, &typed) {
		return typed
	}
	return &quivrplugin.Error{}
}

func TestMapsRSS2ItemsToStructuredManifests(t *testing.T) {
	srv := newFeedServer(t, testdata(t, "rss2.xml"))
	page, err := rssFetch(t, feed{}, cfg(srv.URL+"/feed"), "", nil)
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
	if second.Content.Kind != "manifest" || part(t, second, "title").Role != "title" || part(t, second, "title").Content.Text != "Second story" {
		t.Fatalf("title %+v", second.Content)
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
		t.Fatalf("source_html only when markup exists: %+v / %+v", second.Content.Parts, first.Content.Parts)
	}
	data := rssData(t, second)
	item, feedMeta := data["item"].(map[string]any), data["feed"].(map[string]any)
	if item["guid"] != "example-2" || item["link"] != "https://news.example.org/second" || item["published"] != "2026-09-01T10:00:00Z" {
		t.Fatalf("item metadata %v", item)
	}
	if fmt.Sprint(item["categories"]) != "[Economy Europe]" || fmt.Sprint(item["authors"]) != "[map[name:Jane Reporter]]" {
		t.Fatalf("item categories/authors %v", item)
	}
	if fmt.Sprint(item["enclosures"]) != "[map[length:1234 type:audio/mpeg url:https://news.example.org/audio.mp3]]" {
		t.Fatalf("enclosures %v", item["enclosures"])
	}
	common, ok := second.Extensions[quivrplugin.CommonMetadataNamespace]
	if !ok || common.SchemaVersion != quivrplugin.CommonMetadataVersion {
		t.Fatalf("common metadata extension missing: %+v", second.Extensions)
	}
	wantCommon := map[string]any{"language": "fr", "published_at": "2026-09-01T10:00:00Z", "source_type": "rss", "source": "https://news.example.org/", "author": []string{"Jane Reporter"}, "tags": []string{"Economy", "Europe"}}
	if !reflect.DeepEqual(common.Data, wantCommon) {
		t.Fatalf("common metadata %v, want %v", common.Data, wantCommon)
	}
	if feedMeta["format"] != "rss" || feedMeta["version"] != "2.0" || feedMeta["title"] != "Example Newsroom" || feedMeta["language"] != "fr" {
		t.Fatalf("feed metadata %v", feedMeta)
	}
	// Nothing but the feed document is fetched: no linked page, no enclosure.
	if fmt.Sprint(srv.paths) != "[/feed]" {
		t.Fatalf("fetched %v", srv.paths)
	}
}

func TestKeyFallsBackToAContentHash(t *testing.T) {
	doc := `<rss version="2.0"><channel><title>T</title><item><title>Only a title</title><description>Body</description></item></channel></rss>`
	srv := newFeedServer(t, doc)
	page, err := rssFetch(t, feed{}, cfg(srv.URL), "", nil)
	if err != nil || len(page.Items) != 1 || !strings.HasPrefix(page.Items[0].RecordKey, "sha256:") {
		t.Fatalf("%v %+v", err, page)
	}
	long := `<rss version="2.0"><channel><title>T</title><item><guid>` + strings.Repeat("g", 2000) + `</guid><title>x</title></item></channel></rss>`
	srv.set(long, `"long"`)
	page, err = rssFetch(t, feed{}, cfg(srv.URL), "", nil)
	if err != nil || len(page.Items) != 1 || !strings.HasPrefix(page.Items[0].RecordKey, "sha256:") {
		t.Fatalf("oversized guid must be hashed: %v %+v", err, page)
	}
}

func TestEmitsOnlyNewOrChangedItemsAndIgnoresFeedMetadata(t *testing.T) {
	doc := testdata(t, "rss2.xml")
	srv := newFeedServer(t, doc)
	first, err := rssFetch(t, feed{}, cfg(srv.URL, `,"honor_ttl":false`), "", nil)
	if err != nil || len(first.Items) != 2 {
		t.Fatalf("%v %+v", err, first)
	}
	// Feed metadata changes (new ETag, full body): no item changed, nothing emitted.
	srv.set(strings.Replace(doc, "Example Newsroom", "Renamed Newsroom", 1), `"v2"`)
	again, err := rssFetch(t, feed{}, cfg(srv.URL, `,"honor_ttl":false`), "", cp(first))
	if err != nil || len(again.Items) != 0 {
		t.Fatalf("unchanged items re-emitted: %v %+v", err, again.Items)
	}
	// An item dropping out of the feed produces nothing (no withdrawal).
	srv.set(`<rss version="2.0"><channel><title>T</title></channel></rss>`, `"v4"`)
	gone, err := rssFetch(t, feed{}, cfg(srv.URL, `,"honor_ttl":false`), "", cp(again))
	if err != nil || len(gone.Items) != 0 {
		t.Fatalf("dropped item %v %+v", err, gone.Items)
	}
}

func TestHonorTTLFalseIgnoresTheFeedTTL(t *testing.T) {
	srv := newFeedServer(t, testdata(t, "rss2.xml"))
	page, err := rssFetch(t, feed{}, cfg(srv.URL, `,"honor_ttl":false`), "", nil)
	var at checkpoint
	if _ = json.Unmarshal(cp(page), &at); err != nil || at.NotBefore != nil {
		t.Fatalf("honor_ttl=false must ignore ttl: %v %v", err, at.NotBefore)
	}
	srv.set(strings.Replace(testdata(t, "rss2.xml"), "<ttl>15</ttl>", "<ttl>1440</ttl>", 1), `"day"`)
	page, err = rssFetch(t, feed{}, cfg(srv.URL), "", nil)
	if _ = json.Unmarshal(cp(page), &at); err != nil || at.NotBefore == nil || !at.NotBefore.Equal(testNow.Add(time.Hour)) {
		t.Fatalf("ttl must be capped at 60 min: %v %v", err, at.NotBefore)
	}
}

func TestMapsFailuresToHealthClasses(t *testing.T) {
	srv := newFeedServer(t, "")
	for status, want := range map[int]quivrplugin.Error{
		401: {Class: quivrplugin.ClassAccess, Code: "unauthorized"},
		403: {Class: quivrplugin.ClassAccess, Code: "forbidden"},
		410: {Class: quivrplugin.ClassAccess, Code: "gone"},
		404: {Class: quivrplugin.ClassSource, Code: "not_found"},
		418: {Class: quivrplugin.ClassSource, Code: "http_status"},
		429: {Class: quivrplugin.ClassTransient, Code: "rate_limited"},
		500: {Class: quivrplugin.ClassTransient, Code: "server_error"},
		503: {Class: quivrplugin.ClassTransient, Code: "server_error"},
	} {
		srv.mu.Lock()
		srv.status = status
		srv.mu.Unlock()
		_, err := rssFetch(t, feed{}, cfg(srv.URL), "", nil)
		if got := errorOf(err); got.Class != want.Class || got.Code != want.Code {
			t.Errorf("status %d: got %v want %v", status, err, want)
		}
	}
	for name, body := range map[string]string{
		"html page": `<!doctype html><html><body>Not a feed</body></html>`,
		"truncated": `<rss version="2.0"><channel><title>T</title><item><title>a</title>`,
		"empty":     ``,
	} {
		srv.mu.Lock()
		srv.status = 0
		srv.mu.Unlock()
		srv.set(body, `"`+name+`"`)
		if _, err := rssFetch(t, feed{}, cfg(srv.URL), "", nil); errorOf(err).Class != quivrplugin.ClassSource || errorOf(err).Code != "malformed_feed" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := rssFetch(t, feed{}, cfg("http://feed.invalid/rss"), "", nil); errorOf(err).Class != quivrplugin.ClassTransient || errorOf(err).Code != "dns_error" {
		t.Errorf("dns: %v", err)
	}
	// A bad checkpoint is a source error, not a crash.
	if _, err := rssFetch(t, feed{}, cfg(srv.URL), "", json.RawMessage(`"nope"`)); errorOf(err).Code != "invalid_checkpoint" {
		t.Errorf("checkpoint: %v", err)
	}
}

func TestBoundsSizeRedirectsAndTime(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<rss version="2.0"><channel><title>`+strings.Repeat("x", maxResponseSize)+`</title></channel></rss>`)
	}))
	defer big.Close()
	if _, err := rssFetch(t, feed{}, cfg(big.URL), "", nil); errorOf(err).Code != "response_too_large" {
		t.Errorf("size: %v", err)
	}
	var loop *httptest.Server
	loop = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, loop.URL+r.URL.Path+"x", http.StatusFound)
	}))
	defer loop.Close()
	if _, err := rssFetch(t, feed{}, cfg(loop.URL+"/r"), "", nil); errorOf(err).Code != "too_many_redirects" {
		t.Errorf("redirects: %v", err)
	}
	// The server waits for the client to give up, never on a clock of its own.
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	if _, err := rssFetch(t, feed{timeout: 50 * time.Millisecond}, cfg(slow.URL), "", nil); errorOf(err).Class != quivrplugin.ClassTransient || errorOf(err).Code != "timeout" {
		t.Errorf("timeout: %v", err)
	}
}

func TestRefusesPrivateAddressesByDefault(t *testing.T) {
	srv := newFeedServer(t, testdata(t, "rss2.xml"))
	for _, configuration := range []string{`{}`, `null`} {
		_, err := feed{}.Fetch(context.Background(), request(t, configuration, cfg(srv.URL), "", nil, testNow))
		if errorOf(err).Class != quivrplugin.ClassSource || errorOf(err).Code != "address_not_allowed" {
			t.Fatalf("loopback fetched with configuration %s: %v", configuration, err)
		}
	}
	if len(srv.paths) != 0 {
		t.Fatalf("request reached a private address: %v", srv.paths)
	}
}

func TestSendsDepositedHTTPCredentials(t *testing.T) {
	srv := newFeedServer(t, testdata(t, "rss2.xml"))
	if _, err := rssFetch(t, feed{}, cfg(srv.URL), `{"username":"reader","password":"pw-test"}`, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := rssFetch(t, feed{}, cfg(srv.URL), `{"token":"tok-test"}`, nil); err != nil {
		t.Fatal(err)
	}
	if srv.auth[0] != "Basic cmVhZGVyOnB3LXRlc3Q=" || srv.auth[1] != "Bearer tok-test" {
		t.Fatalf("auth headers %v", srv.auth)
	}
}

func TestNeverDowngradesACredentialToPlainHTTP(t *testing.T) {
	plain := newFeedServer(t, testdata(t, "rss2.xml"))
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer secure.Close()
	f := feed{transport: secure.Client().Transport.(*http.Transport).Clone()}
	if _, err := rssFetch(t, f, cfg(secure.URL), `{"token":"tok-test"}`, nil); errorOf(err).Code != "insecure_redirect" {
		t.Fatalf("downgrade followed: %v", err)
	}
	if len(plain.auth) != 0 {
		t.Fatalf("credential reached the plain-HTTP target: %v", plain.auth)
	}
}

func TestBoundsOversizedItemsAndMetadata(t *testing.T) {
	var cats strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&cats, "<category>%d-%s</category>", i, strings.Repeat("c", 2000))
	}
	huge := `<rss version="2.0"><channel><title>T</title><item><guid>big</guid><title>Big</title>` + cats.String() +
		`<description><![CDATA[<p>` + strings.Repeat("word ", 200000) + `</p>]]></description></item></channel></rss>`
	srv := newFeedServer(t, huge)
	page, err := rssFetch(t, feed{}, cfg(srv.URL), "", nil)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("%v %+v", err, page)
	}
	it := page.Items[0]
	total := 0
	for _, p := range it.Content.Parts {
		total += len(p.Content.Text)
		if !validText(p.Content.Text) {
			t.Fatalf("invalid text in %s", p.Key)
		}
	}
	if total > maxItemText || hasPart(it, "body_html") {
		t.Fatalf("item text %d bytes (limit %d), html kept %v", total, maxItemText, hasPart(it, "body_html"))
	}
	if rssData(t, it)["item"].(map[string]any)["truncated"] != true {
		t.Fatalf("truncation not recorded: %v", rssData(t, it)["item"])
	}
	raw, _ := json.Marshal(it.Extensions)
	if len(raw) > maxExtensionBytes {
		t.Fatalf("extension %d bytes", len(raw))
	}
}

// A page stops at maxPageItemBytes of encoded items, under the manifest's
// max_response_bytes; the next pages return the rest, and together they are
// every item once.
func TestCutsAPageByBytesAndResumes(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<rss version="2.0"><channel><title>Heavy</title>`)
	paragraph := strings.Repeat("<p>Lorem ipsum dolor sit amet.</p>", 5000) // about 170 KiB of HTML per item, under the 10 MiB feed bound in total
	for i := 0; i < 55; i++ {
		fmt.Fprintf(&b, `<item><guid>h%d</guid><title>Heavy %d</title><description><![CDATA[%s]]></description></item>`, i, i, paragraph)
	}
	b.WriteString(`</channel></rss>`)
	srv := newFeedServer(t, b.String())
	seenKeys := map[string]int{}
	var checkpointIn json.RawMessage
	pages := 0
	for {
		page, err := rssFetch(t, feed{}, cfg(srv.URL, `,"honor_ttl":false`), "", checkpointIn)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		encoded, _ := json.Marshal(page.Items)
		if len(encoded) > maxPageItemBytes+(1<<20) {
			t.Fatalf("page %d encodes to %d bytes", pages, len(encoded))
		}
		for _, it := range page.Items {
			seenKeys[it.RecordKey]++
		}
		checkpointIn = cp(page)
		if !page.More {
			break
		}
	}
	if pages < 2 || len(seenKeys) != 55 {
		t.Fatalf("%d pages, %d distinct items", pages, len(seenKeys))
	}
	for key, n := range seenKeys {
		if n != 1 {
			t.Fatalf("%s emitted %d times", key, n)
		}
	}
}

// The checkpoint stays under the declared max_checkpoint_bytes: the oldest
// revisions of items no longer in the feed are dropped first, the current feed's are kept.
func TestBoundsTheCheckpointBytes(t *testing.T) {
	var old []seen
	for i := 0; i < seenCap; i++ {
		// Longer than real entries, so the byte bound applies before the 2000-entry cap.
		old = append(old, seen{Key: fmt.Sprintf("%064x", i), Revision: fmt.Sprintf("%016x", i)})
	}
	start, _ := json.Marshal(checkpoint{Seen: old})
	srv := newFeedServer(t, testdata(t, "rss2.xml"))
	page, err := rssFetch(t, feed{}, cfg(srv.URL), "", start)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("%v %+v", err, page)
	}
	if n := len(cp(page)); n > maxCheckpointBytes {
		t.Fatalf("checkpoint %d bytes", n)
	}
	again, err := rssFetch(t, feed{}, cfg(srv.URL, `,"honor_ttl":false`), "", cp(page))
	if err != nil || len(again.Items) != 0 {
		t.Fatalf("the current feed's revisions were dropped: %v %d items", err, len(again.Items))
	}
}

func TestCheckCredentialRefusesOnlyOn401And403(t *testing.T) {
	srv := newFeedServer(t, testdata(t, "rss2.xml"))
	check := func() error {
		var req quivrplugin.CredentialRequest
		body := `{"invocation_id":"inv","contribution":"connector","organization_id":"org","configuration":` + allowPrivate +
			`,"connector":{"instance_id":"c","kind":"rss","config":` + cfg(srv.URL) + `},"credential":{"token":"tok-test"},"now":"2026-09-28T12:00:00Z"}`
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		_, err := feed{}.CheckCredential(context.Background(), &req)
		return err
	}
	for status, code := range map[int]string{0: "", 401: "unauthorized", 403: "forbidden", 404: "", 500: ""} {
		srv.mu.Lock()
		srv.status = status
		srv.mu.Unlock()
		err := check()
		if code == "" && err != nil || code != "" && (errorOf(err).Class != quivrplugin.ClassAccess || errorOf(err).Code != code) {
			t.Errorf("status %d: %v", status, err)
		}
	}
	if srv.auth[0] != "Bearer tok-test" {
		t.Fatalf("the credential was not sent: %v", srv.auth)
	}
}
