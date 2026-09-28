package acceptance

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeFeed is a local RSS server the worker polls. It honours If-None-Match
// and can be switched to an error status or a malformed body.
type fakeFeed struct {
	mu          sync.Mutex
	body, etag  string
	status      int
	requests    int
	notModified int
	*httptest.Server
}

func newFakeFeed(t *testing.T, body string) *fakeFeed {
	t.Helper()
	f := &fakeFeed{body: body, etag: `"1"`}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		if r.Header.Get("If-None-Match") == f.etag {
			f.notModified++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", f.etag)
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, f.body)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeFeed) serve(body, etag string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body, f.etag, f.status = body, etag, status
}

func (f *fakeFeed) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, f.notModified
}

// awaitPolls waits until the worker has polled n more times.
func (f *fakeFeed) awaitPolls(t *testing.T, n int) {
	t.Helper()
	start, _ := f.counts()
	for deadline := time.Now().Add(45 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if now, _ := f.counts(); now >= start+n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("feed not polled %d more times", n)
		}
	}
}

func rssDocument(items ...string) string {
	return `<?xml version="1.0"?><rss version="2.0"><channel><title>Example Wire</title><link>https://wire.example.org/</link><description>Neutral test feed</description>` + strings.Join(items, "") + `</channel></rss>`
}

func rssConnector(key, corpusID, namespace, url string, silentAfter int) map[string]any {
	return map[string]any{"idempotency_key": key + "-" + connectorRun, "corpus_id": corpusID, "source_namespace": namespace, "kind": "rss",
		"config": map[string]any{"url": url, "honor_ttl": false}, "schedule": map[string]any{"interval_seconds": 1},
		"health_policy": map[string]any{"silent_after_seconds": silentAfter}}
}

// currentVersion waits until the Record's current Version satisfies ok.
func rssCurrentVersion(t *testing.T, token, recordID string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	var v map[string]any
	for deadline := time.Now().Add(120 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		r := request(t, "GET", "/v0/records/"+recordID, token, nil, 200)
		if r["withdrawn"] == true {
			t.Fatalf("record withdrawn: %v", r)
		}
		if id, has := r["current_version_id"].(string); has {
			v = request(t, "GET", "/v0/records/"+recordID+"/versions/"+id, token, nil, 200)
			if ok(v) {
				return v
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("version never matched: %v", v)
		}
	}
}

func partText(v map[string]any, role string) string {
	for _, p := range v["manifest"].(map[string]any)["parts"].([]any) {
		part := p.(map[string]any)
		if part["role"] == role {
			return part["content"].(map[string]any)["text"].(string)
		}
	}
	return ""
}

func TestConnectorRSSCollectsAFeedWithConditionalPolling(t *testing.T) {
	token := connectorToken(t)
	corpusID, cursor := connectorCorpus(t, token, "rss")
	alpha := `<item><guid>wire-alpha</guid><title>Alpha headline</title><link>https://wire.example.org/alpha</link><pubDate>Mon, 28 Sep 2026 08:00:00 GMT</pubDate><category>Economy</category><description>&lt;p&gt;Alpha &lt;b&gt;dispatch&lt;/b&gt; body.&lt;/p&gt;</description><enclosure url="https://wire.example.org/alpha.jpg" type="image/jpeg" length="10"/></item>`
	beta := `<item><title>Beta headline</title><link>https://wire.example.org/beta</link><pubDate>Mon, 28 Sep 2026 09:00:00 GMT</pubDate><description>Beta dispatch body.</description></item>`
	feed := newFakeFeed(t, rssDocument(beta, alpha))

	created := request(t, "POST", "/v0/connectors", token, rssConnector("rss-lifecycle", corpusID, "wire", feed.URL+"/feed.xml", 86400), 201)
	id := created["connector_id"].(string)
	if created["kind"] != "rss" || created["schedule"].(map[string]any)["interval_seconds"].(float64) != 1 {
		t.Fatalf("created %v", created)
	}

	// Creation ingests the items present in the feed; the key falls back to the link.
	byKey, _ := recordsByKey(t, token, corpusID, cursor, map[string]int{"wire-alpha": 1, "https://wire.example.org/beta": 1})
	v := rssCurrentVersion(t, token, byKey["wire-alpha"], func(v map[string]any) bool {
		return v["availability"].(map[string]any)["searchable"] == true
	})
	if partText(v, "title") != "Alpha headline" || partText(v, "body") != "Alpha dispatch body." || !strings.Contains(partText(v, "source_html"), "<b>dispatch</b>") {
		t.Fatalf("manifest %v", v["manifest"])
	}
	ext := v["extensions"].(map[string]any)["connector.rss"].(map[string]any)["data"].(map[string]any)
	item, meta := ext["item"].(map[string]any), ext["feed"].(map[string]any)
	if item["link"] != "https://wire.example.org/alpha" || fmt.Sprint(item["categories"]) != "[Economy]" || meta["title"] != "Example Wire" {
		t.Fatalf("extension %v", ext)
	}
	if enc := item["enclosures"].([]any)[0].(map[string]any); enc["url"] != "https://wire.example.org/alpha.jpg" {
		t.Fatalf("enclosure %v", enc)
	}
	if p := v["provenance"].(map[string]any); p["producer"] != id || p["producer_version"] != "rss/v1" {
		t.Fatalf("provenance %v", p)
	}

	// Re-polls are conditional: the unchanged feed answers 304 and creates nothing.
	for deadline := time.Now().Add(45 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if _, notModified := feed.counts(); notModified >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no conditional re-poll observed")
		}
	}

	// An edited item becomes a correction; the untouched item keeps one Version.
	feed.serve(rssDocument(beta, strings.Replace(alpha, "Alpha &lt;b&gt;dispatch&lt;/b&gt; body.", "Alpha dispatch body, corrected.", 1)), `"2"`, 0)
	byKey, events := recordsByKey(t, token, corpusID, cursor, map[string]int{"wire-alpha": 2})
	rssCurrentVersion(t, token, byKey["wire-alpha"], func(v map[string]any) bool { return partText(v, "body") == "Alpha dispatch body, corrected." })

	// Beta drops out of the feed: it is not withdrawn and never duplicated.
	feed.serve(rssDocument(strings.Replace(alpha, "Alpha &lt;b&gt;dispatch&lt;/b&gt; body.", "Alpha dispatch body, corrected.", 1)), `"3"`, 0)
	feed.awaitPolls(t, 3)
	betaRecord := request(t, "GET", "/v0/records/"+byKey["https://wire.example.org/beta"], token, nil, 200)
	if betaRecord["withdrawn"] != false {
		t.Fatalf("dropped item withdrawn: %v", betaRecord)
	}
	more, _ := drain(t, token, corpusID, cursor, 0)
	if n := typed(more, "record.materialized", byKey["https://wire.example.org/beta"]); n != 1 {
		t.Fatalf("beta materialized %d times; events %v", n, events)
	}
	if n := typed(more, "record.materialized", byKey["wire-alpha"]); n != 2 {
		t.Fatalf("alpha materialized %d times", n)
	}

	// The server refuses access: access_error with its code, then recovery to active.
	feed.serve(rssDocument(), `"4"`, http.StatusForbidden)
	denied := awaitHealth(t, token, id, state("access_error"))
	if e := denied["health"].(map[string]any)["last_error"].(map[string]any); e["code"] != "forbidden" {
		t.Fatalf("last_error %v", e)
	}
	feed.serve(rssDocument(), `"5"`, 0)
	awaitHealth(t, token, id, state("active"))
	request(t, "POST", "/v0/connectors/"+id+"/disable", token, map[string]any{"idempotency_key": "rss-stop"}, 200)
}

func TestConnectorRSSReportsMalformedAndSilentFeeds(t *testing.T) {
	token := connectorToken(t)
	corpusID, cursor := connectorCorpus(t, token, "rss-health")

	// A malformed feed is a bounded source error that creates no Record.
	broken := newFakeFeed(t, `<rss version="2.0"><channel><title>Broken</title><item><title>half`)
	b := request(t, "POST", "/v0/connectors", token, rssConnector("rss-malformed", corpusID, "broken", broken.URL, 86400), 201)
	bad := awaitHealth(t, token, b["connector_id"].(string), func(h map[string]any) bool { return h["last_error"] != nil })
	if h := bad["health"].(map[string]any); h["last_error"].(map[string]any)["code"] != "malformed_feed" || h["state"] == "access_error" {
		t.Fatalf("malformed health %v", h)
	}

	// A valid feed with no new items turns silent: re-polls are not activity.
	quiet := newFakeFeed(t, rssDocument(`<item><guid>quiet-1</guid><title>Only item</title><description>Nothing new after this.</description></item>`))
	q := request(t, "POST", "/v0/connectors", token, rssConnector("rss-quiet", corpusID, "quiet", quiet.URL, 3), 201)
	qid := q["connector_id"].(string)
	recordsByKey(t, token, corpusID, cursor, map[string]int{"quiet-1": 1})
	quiet.serve(rssDocument(`<item><guid>quiet-1</guid><title>Only item</title><description>Nothing new after this.</description></item>`), `"refetched"`, 0)
	silent := awaitHealth(t, token, qid, state("silent"))
	if silent["health"].(map[string]any)["last_success_at"] == nil {
		t.Fatalf("silent source must still report successful polls: %v", silent)
	}
	events, _ := drain(t, token, corpusID, cursor, 0)
	for _, e := range events {
		if res := e["resource"].(map[string]any); res["kind"] == "record" {
			r := request(t, "GET", "/v0/records/"+res["id"].(string), token, nil, 200)
			if r["source"].(map[string]any)["namespace"] == "broken" {
				t.Fatalf("malformed feed produced a Record: %v", r)
			}
		}
	}
	for _, id := range []string{b["connector_id"].(string), qid} {
		request(t, "POST", "/v0/connectors/"+id+"/disable", token, map[string]any{"idempotency_key": "rss-health-stop-" + id}, 200)
	}
}
