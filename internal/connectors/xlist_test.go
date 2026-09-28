package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// fakeX is a minimal X API v2 serving one list timeline (newest first, paged
// by opaque tokens) and post lookup by ids, with switchable failures.
type fakeX struct {
	mu        sync.Mutex
	posts     map[string]map[string]any
	deleted   map[string]bool
	protected map[string]bool
	status    int
	reset     int64
	listError string
	requests  []string
	pageSize  int
}

func newFakeX(t *testing.T) (*fakeX, *httptest.Server) {
	f := &fakeX{posts: map[string]map[string]any{}, deleted: map[string]bool{}, protected: map[string]bool{}, pageSize: 3}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeX) post(id, text string, created time.Time, history ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(history) == 0 {
		history = []string{id}
	}
	// A newer version replaces earlier ones in timelines.
	for _, old := range history[:len(history)-1] {
		delete(f.posts, old)
	}
	f.posts[id] = map[string]any{"id": id, "text": text, "created_at": created.UTC().Format(time.RFC3339), "author_id": "42", "lang": "fr", "edit_history_tweet_ids": history}
}

func (f *fakeX) visible() []map[string]any {
	var list []map[string]any
	for id, p := range f.posts {
		if !f.deleted[id] && !f.protected[id] {
			list = append(list, p)
		}
	}
	sort.Slice(list, func(i, j int) bool { return idAfter(list[i]["id"].(string), list[j]["id"].(string)) })
	return list
}

func (f *fakeX) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer x-test-token" {
		w.WriteHeader(401)
		return
	}
	if f.status != 0 {
		if f.reset != 0 {
			w.Header().Set("x-rate-limit-reset", strconv.FormatInt(f.reset, 10))
		}
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"title":"failure","type":"https://api.x.com/2/problems/credits-depleted"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasPrefix(r.URL.Path, "/2/lists/") && strings.HasSuffix(r.URL.Path, "/tweets"):
		if f.listError != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"resource_type": "list", "type": "https://api.twitter.com/2/problems/" + f.listError}}})
			return
		}
		if r.URL.Query().Get("expansions") == "" || !strings.Contains(r.URL.Query().Get("tweet.fields"), "edit_history_tweet_ids") {
			w.WriteHeader(400)
			return
		}
		list := f.visible()
		start := 0
		if tok := r.URL.Query().Get("pagination_token"); tok != "" {
			n, err := strconv.Atoi(strings.TrimPrefix(tok, "p"))
			if err != nil || !strings.HasPrefix(tok, "p") {
				w.WriteHeader(400)
				return
			}
			start = n
		}
		end := start + f.pageSize
		if end > len(list) {
			end = len(list)
		}
		body := map[string]any{"data": list[start:end], "includes": map[string]any{"users": []any{map[string]any{"id": "42", "username": "newsdesk", "name": "News Desk"}}}, "meta": map[string]any{"result_count": end - start}}
		if end < len(list) {
			body["meta"].(map[string]any)["next_token"] = fmt.Sprintf("p%d", end)
		}
		_ = json.NewEncoder(w).Encode(body)
	case r.URL.Path == "/2/tweets":
		var data, errs []any
		for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
			switch {
			case f.deleted[id] || f.posts[id] == nil:
				errs = append(errs, map[string]any{"resource_id": id, "value": id, "resource_type": "tweet", "type": "https://api.twitter.com/2/problems/resource-not-found"})
			case f.protected[id]:
				errs = append(errs, map[string]any{"resource_id": id, "value": id, "resource_type": "tweet", "type": "https://api.twitter.com/2/problems/not-authorized-for-resource"})
			default:
				data = append(data, map[string]any{"id": id, "edit_history_tweet_ids": f.posts[id]["edit_history_tweet_ids"]})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "errors": errs})
	default:
		w.WriteHeader(404)
	}
}

func (f *fakeX) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.requests {
		if strings.HasPrefix(p, prefix) {
			n++
		}
	}
	return n
}

var xNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func xAcquirer(t *testing.T, srv *httptest.Server, config string) (*Acquirer, *fakeRuns, *fakeIngest, *time.Time) {
	t.Helper()
	x := XList{BaseURL: srv.URL, PageSize: 3}
	registry, err := NewRegistry(x)
	if err != nil {
		t.Fatal(err)
	}
	sealer, _ := NewSealer(testDeploymentKey)
	sealed, _ := sealer.Seal("org_a", "connector_x", []byte(`{"bearer_token":"x-test-token"}`))
	runs := &fakeRuns{target: Target{Instance: Instance{Organization: "org_a", ID: "connector_x", CorpusID: "corpus_1", Namespace: "x", Kind: "x_list", Config: json.RawMessage(config), Enabled: true}, RunSequence: 1, Sealed: &sealed}}
	ingest := &fakeIngest{}
	now := xNow
	a := &Acquirer{Store: runs, Registry: registry, Sealer: sealer, Ingest: ingest, Now: func() time.Time { return now }}
	return a, runs, ingest, &now
}

func run(t *testing.T, a *Acquirer, runs *fakeRuns) *RunError {
	t.Helper()
	runs.finished = nil
	if err := a.Run(context.Background(), "org_a", runs.target.ID, runs.target.RunSequence); err != nil {
		t.Fatal(err)
	}
	for _, p := range runs.progress {
		runs.target.ReadsToday += p.Reads
	}
	runs.progress = nil
	return runs.finished[0]
}

func keys(cmds []content.Command) []string {
	var out []string
	for _, c := range cmds {
		out = append(out, c.Source.RecordKey)
	}
	return out
}

func TestXListConfigAndCredentialAreValidated(t *testing.T) {
	registry, _ := NewRegistry(XList{})
	ok := []string{`{"list_id":"1234567890123456789"}`, `{"list_id":"1","max_reads_per_day":1000,"recheck_window_seconds":3600,"recheck_interval_seconds":60}`}
	for _, c := range ok {
		if err := registry.validate("x_list", json.RawMessage(c), json.RawMessage(`{"bearer_token":"t","consumer_secret":"s"}`), "/credential/secret"); err != nil {
			t.Fatalf("%s: %v", c, err)
		}
	}
	bad := []string{`{}`, `{"list_id":"abc"}`, `{"list_id":"12345678901234567890"}`, `{"list_id":"1","extra":1}`, `{"list_id":"1","recheck_window_seconds":60}`, `{"list_id":"1","recheck_window_seconds":700000}`, `{"list_id":"1","recheck_interval_seconds":10}`}
	for _, c := range bad {
		if err := registry.validate("x_list", json.RawMessage(c), nil, "/credential/secret"); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("%s accepted: %v", c, err)
		}
	}
	if err := registry.validate("x_list", json.RawMessage(`{"list_id":"1"}`), json.RawMessage(`{"token":"t"}`), "/credential/secret"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("credential: %v", err)
	}
	check := XList{}
	now := time.Now()
	for _, since := range []time.Time{now.Add(-8 * 24 * time.Hour), now.Add(time.Hour)} {
		if check.CheckConfig(json.RawMessage(`{"list_id":"1","backfill_since":"`+since.Format(time.RFC3339)+`"}`), now) == nil {
			t.Fatalf("backfill_since %s accepted", since)
		}
	}
	if err := check.CheckConfig(json.RawMessage(`{"list_id":"1","backfill_since":"`+now.Add(-6*24*time.Hour).Format(time.RFC3339)+`"}`), now); err != nil {
		t.Fatal(err)
	}
	if check.CheckConfig(json.RawMessage(`{"list_id":"1","backfill_since":"yesterday"}`), now) == nil {
		t.Fatal("unparseable backfill_since accepted")
	}
	if (XList{}).DefaultInterval() != 2*time.Minute {
		t.Fatal("default interval")
	}
}

func TestXListStartsNowThenCollectsNewPostsDownToTheWatermark(t *testing.T) {
	f, srv := newFakeX(t)
	f.post("1000000000000000001", "Old post", xNow.Add(-time.Hour))
	f.post("1000000000000000002", "Older still visible", xNow.Add(-time.Minute))
	a, runs, ingest, now := xAcquirer(t, srv, `{"list_id":"77"}`)
	if e := run(t, a, runs); e != nil {
		t.Fatalf("first run %+v", e)
	}
	if len(ingest.accepted) != 0 {
		t.Fatalf("posts before creation were ingested: %v", keys(ingest.accepted))
	}
	for i := 3; i <= 9; i++ {
		f.post(fmt.Sprintf("100000000000000000%d", i), fmt.Sprintf("Post %d", i), xNow.Add(time.Duration(i)*time.Second))
	}
	*now = xNow.Add(time.Minute)
	before := f.count("/2/lists/")
	if e := run(t, a, runs); e != nil {
		t.Fatalf("second run %+v", e)
	}
	got := keys(ingest.accepted)
	if len(got) != 7 || got[0] != "1000000000000000009" || got[6] != "1000000000000000003" {
		t.Fatalf("collected %v", got)
	}
	// Seven new posts at three per page: three pages, stopping at the watermark.
	if n := f.count("/2/lists/") - before; n != 3 {
		t.Fatalf("list requests %d", n)
	}
	// Estimated billed reads: the day's first page (2 posts), then only new posts (7);
	// posts already read today are deduplicated by X.
	if runs.target.ReadsToday != 2+7 {
		t.Fatalf("reads %d", runs.target.ReadsToday)
	}
	// An idle poll reads one page, ingests nothing and adds no billed read.
	ingest.accepted = nil
	if e := run(t, a, runs); e != nil || len(ingest.accepted) != 0 || runs.target.ReadsToday != 9 {
		t.Fatalf("idle poll %+v %v reads %d", e, keys(ingest.accepted), runs.target.ReadsToday)
	}
	// On a new UTC day the first page (3) and the recheck of the 7 tracked posts are billed again.
	*now = xNow.Add(13 * time.Hour)
	runs.target.ReadsToday = 0
	if e := run(t, a, runs); e != nil || runs.target.ReadsToday != 3+7 {
		t.Fatalf("next day %+v reads %d", e, runs.target.ReadsToday)
	}
}

func TestXListBackfillsAndResumesAnInterruptedSweepFromItsToken(t *testing.T) {
	f, srv := newFakeX(t)
	for i := 1; i <= 8; i++ {
		f.post(fmt.Sprintf("200000000000000000%d", i), fmt.Sprintf("Post %d", i), xNow.Add(-time.Duration(10-i)*time.Hour))
	}
	since := xNow.Add(-5 * time.Hour).Format(time.RFC3339) // posts 5..8 are inside the backfill window
	a, runs, ingest, _ := xAcquirer(t, srv, `{"list_id":"77","backfill_since":"`+since+`"}`)
	a.MaxPages = 1
	run(t, a, runs)
	if got := keys(ingest.accepted); len(got) != 3 {
		t.Fatalf("first page %v", got)
	}
	var cp xCheckpoint
	_ = json.Unmarshal(runs.target.Checkpoint, &cp)
	if cp.Sweep == nil || cp.Sweep.Token == "" || cp.Watermark != "" {
		t.Fatalf("interrupted sweep must keep its token and not advance the watermark: %s", runs.target.Checkpoint)
	}
	run(t, a, runs)
	if got := keys(ingest.accepted); len(got) != 4 || got[3] != "2000000000000000005" {
		t.Fatalf("resumed %v", got)
	}
	cp = xCheckpoint{}
	_ = json.Unmarshal(runs.target.Checkpoint, &cp)
	if cp.Sweep != nil || cp.Watermark != "2000000000000000008" {
		t.Fatalf("completed sweep %s", runs.target.Checkpoint)
	}
}

func TestXListRestartsASweepWhoseTokenExpired(t *testing.T) {
	f, srv := newFakeX(t)
	a, runs, ingest, _ := xAcquirer(t, srv, `{"list_id":"77"}`)
	runs.target.Checkpoint = json.RawMessage(`{"floor":"2026-09-28T11:00:00Z","sweep":{"top":"","token":"expired"}}`)
	f.post("3000000000000000001", "Fresh", xNow)
	if e := run(t, a, runs); e != nil {
		t.Fatalf("%+v", e)
	}
	if got := keys(ingest.accepted); len(got) != 1 {
		t.Fatalf("restarted sweep %v", got)
	}
}

func TestXListMapsPostsAndTurnsEditsIntoCorrections(t *testing.T) {
	post := XPost{ID: "5000000000000000002", Text: "short", CreatedAt: "2026-09-28T11:59:00Z", AuthorID: "42", Lang: "fr", ConversationID: "5000000000000000000",
		EditHistory: []string{"5000000000000000001", "5000000000000000002"}, NoteTweet: &XNoteTweet{Text: "the full long text"},
		ReferencedTweets: []XReference{{Type: "quoted", ID: "4000000000000000001"}, {Type: "replied_to", ID: "4000000000000000002"}},
		Attachments:      &XAttachments{MediaKeys: []string{"3_1"}}, Entities: map[string]any{"hashtags": []any{map[string]any{"tag": "news"}}}}
	inc := XIncludes{Users: []XUser{{ID: "42", Username: "newsdesk", Name: "News Desk"}}, Media: []XMedia{{MediaKey: "3_1", Type: "photo", URL: "https://pbs.example/1.jpg", AltText: "A photo"}}}
	item := MapPost(post, inc)
	if item.RecordKey != "5000000000000000001" || item.Revision != "5000000000000000002" || item.Position != "5000000000000000002" {
		t.Fatalf("identity %+v", item)
	}
	if item.Manifest == nil || len(item.Manifest.Parts) != 1 || item.Manifest.Parts[0].Content.Text != "the full long text" {
		t.Fatalf("manifest %+v", item.Manifest)
	}
	rel := item.Manifest.Relations
	if len(rel) != 2 || rel[0].Type != "quotes" || rel[0].Target.RecordKey != "4000000000000000001" || rel[1].Type != "replies_to" {
		t.Fatalf("relations %+v", rel)
	}
	ext := item.Extensions[XExtensionNamespace]
	if ext.SchemaVersion != "1" {
		t.Fatalf("extension %+v", ext)
	}
	raw, _ := json.Marshal(ext.Data)
	for _, want := range []string{`"username":"newsdesk"`, `"lang":"fr"`, `"alt_text":"A photo"`, `"edit_history_post_ids":["5000000000000000001","5000000000000000002"]`, `"tag":"news"`, `"url":"https://x.com/newsdesk/status/5000000000000000002"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("extension lacks %s: %s", want, raw)
		}
	}
	if err := (content.BuiltinExtensions{}).Validate(context.Background(), item.Extensions); err != nil {
		t.Fatalf("extension schema: %v", err)
	}
	if empty := MapPost(XPost{ID: "9", EditHistory: []string{"9"}}, XIncludes{}); empty.Manifest.Parts[0].Content.Text == "" {
		t.Fatal("a post without text must still carry a body")
	}

	f, srv := newFakeX(t)
	a, runs, ingest, now := xAcquirer(t, srv, `{"list_id":"77"}`)
	run(t, a, runs)
	f.post("5000000000000000001", "Original", xNow.Add(time.Second))
	*now = xNow.Add(time.Minute)
	run(t, a, runs)
	f.post("5000000000000000003", "Edited", xNow.Add(2*time.Minute), "5000000000000000001", "5000000000000000003")
	*now = xNow.Add(3 * time.Minute)
	run(t, a, runs)
	if len(ingest.accepted) != 2 {
		t.Fatalf("accepted %v", keys(ingest.accepted))
	}
	orig, edit := ingest.accepted[0], ingest.accepted[1]
	if orig.Source != edit.Source || orig.Revision == edit.Revision || edit.Revision != "5000000000000000003" || edit.Position != "5000000000000000003" || orig.Key == edit.Key {
		t.Fatalf("edit must be a correction of the same Record: %+v / %+v", orig, edit)
	}
	if edit.Manifest.Relations != nil && edit.Manifest.Relations[0].Target.Namespace == "" {
		t.Fatal("relation target not bound to the instance namespace")
	}
}

func TestXListRecheckWithdrawsDeletedAndProtectedPosts(t *testing.T) {
	f, srv := newFakeX(t)
	a, runs, ingest, now := xAcquirer(t, srv, `{"list_id":"77","recheck_interval_seconds":600}`)
	run(t, a, runs)
	for i := 1; i <= 5; i++ {
		f.post(fmt.Sprintf("600000000000000000%d", i), fmt.Sprintf("Post %d", i), xNow.Add(time.Duration(i)*time.Second))
	}
	*now = xNow.Add(time.Minute)
	run(t, a, runs)
	if len(ingest.accepted) != 5 {
		t.Fatalf("accepted %v", keys(ingest.accepted))
	}
	f.mu.Lock()
	f.deleted["6000000000000000002"] = true
	f.protected["6000000000000000004"] = true
	f.mu.Unlock()
	// Not yet due: no lookup.
	*now = xNow.Add(5 * time.Minute)
	run(t, a, runs)
	if f.count("/2/tweets") != 0 || len(ingest.withdrawn) != 0 {
		t.Fatal("recheck ran before its interval")
	}
	*now = xNow.Add(12 * time.Minute)
	if e := run(t, a, runs); e != nil {
		t.Fatalf("%+v", e)
	}
	var gone []string
	for _, w := range ingest.withdrawn {
		gone = append(gone, w.Source.RecordKey)
	}
	sort.Strings(gone)
	if fmt.Sprint(gone) != "[6000000000000000002 6000000000000000004]" || ingest.withdrawn[0].Reason != "source_withdrawn" {
		t.Fatalf("withdrawn %v", gone)
	}
	var diag map[string]any
	var cp xCheckpoint
	_ = json.Unmarshal(runs.target.Checkpoint, &cp)
	if len(cp.Recent) != 3 || cp.LastRecheck == nil {
		t.Fatalf("recheck state %s", runs.target.Checkpoint)
	}
	// Diagnostics expose the recheck coverage.
	a2, runs2, _, _ := xAcquirer(t, srv, `{"list_id":"77"}`)
	runs2.target.Checkpoint = runs.target.Checkpoint
	runs2.finished = nil
	_ = a2.Run(context.Background(), "org_a", "connector_x", 1)
	_ = json.Unmarshal(runs2.progress[len(runs2.progress)-1].Diagnostics, &diag)
	if diag["recheck_window_seconds"] != float64(86400) || diag["recheck_tracked_posts"] != float64(3) || diag["last_recheck_at"] == nil || diag["recheck_interval_seconds"] != float64(600) {
		t.Fatalf("diagnostics %v", diag)
	}
	// Posts older than the window are no longer tracked.
	*now = xNow.Add(25 * time.Hour)
	run(t, a, runs)
	cp = xCheckpoint{}
	_ = json.Unmarshal(runs.target.Checkpoint, &cp)
	if len(cp.Recent) != 0 {
		t.Fatalf("expired posts still tracked: %s", runs.target.Checkpoint)
	}
}

func TestXListBoundsTheRecheckSet(t *testing.T) {
	cp := xCheckpoint{}
	for i := 0; i < maxRecheckPosts+5; i++ {
		cp.track(fmt.Sprintf("%d", 7000000000000000000+i), fmt.Sprintf("%d", 7000000000000000000+i), xNow.Add(time.Duration(i)*time.Second), "2026-09-28")
	}
	if len(cp.Recent) != maxRecheckPosts || cp.Dropped != 5 || cp.Recent[0].Root != "7000000000000000005" {
		t.Fatalf("bounded %d dropped %d first %s", len(cp.Recent), cp.Dropped, cp.Recent[0].Root)
	}
	// Tracking an edit updates the entry instead of adding one.
	cp.track("7000000000000000005", "7100000000000000000", xNow, "2026-09-28")
	if len(cp.Recent) != maxRecheckPosts || cp.Recent[0].Latest != "7100000000000000000" {
		t.Fatalf("edit tracking %+v", cp.Recent[0])
	}
}

func TestXListMapsSourceFailuresToHealth(t *testing.T) {
	cases := []struct {
		status int
		class  ErrorClass
		code   string
	}{{401, ClassAccess, "unauthorized"}, {403, ClassAccess, "forbidden"}, {402, ClassAccess, "credits_depleted"}, {404, ClassAccess, "list_not_found"}, {503, ClassTransient, "source_unavailable"}}
	for _, c := range cases {
		f, srv := newFakeX(t)
		f.status = c.status
		a, runs, _, _ := xAcquirer(t, srv, `{"list_id":"77"}`)
		if e := run(t, a, runs); e == nil || e.Class != c.class || e.Code != c.code {
			t.Fatalf("%d: %+v", c.status, e)
		}
	}
	f, srv := newFakeX(t)
	f.status, f.reset = 429, xNow.Add(7*time.Minute).Unix()
	a, runs, _, _ := xAcquirer(t, srv, `{"list_id":"77"}`)
	if e := run(t, a, runs); e == nil || e.Code != "rate_limited" || e.Class != ClassTransient || e.RetryAfter != 7*time.Minute {
		t.Fatalf("429 %+v", e)
	}
	f.reset = 0
	if e := run(t, a, runs); e == nil || e.RetryAfter != time.Minute {
		t.Fatalf("429 without reset %+v", e)
	}
	f.reset = xNow.Add(3 * time.Hour).Unix()
	if e := run(t, a, runs); e == nil || e.RetryAfter != 15*time.Minute {
		t.Fatalf("429 reset is capped %+v", e)
	}
	f.status = 0
	for errType, code := range map[string]string{"resource-not-found": "list_not_found", "not-authorized-for-resource": "forbidden"} {
		f.listError = errType
		if e := run(t, a, runs); e == nil || e.Class != ClassAccess || e.Code != code {
			t.Fatalf("%s: %+v", errType, e)
		}
	}
	// A refused credential never reaches the source.
	a, runs, _, _ = xAcquirer(t, srv, `{"list_id":"77"}`)
	sealed, _ := a.Sealer.Seal("org_a", "connector_x", []byte(`{"bearer_token":"wrong"}`))
	runs.target.Sealed = &sealed
	f.listError = ""
	if e := run(t, a, runs); e == nil || e.Code != "unauthorized" {
		t.Fatalf("wrong token %+v", e)
	}
}

func TestXListStopsPollingAtTheDailyReadCapButKeepsRechecking(t *testing.T) {
	f, srv := newFakeX(t)
	a, runs, ingest, now := xAcquirer(t, srv, `{"list_id":"77","max_reads_per_day":100,"recheck_interval_seconds":60}`)
	run(t, a, runs)
	f.post("8000000000000000001", "Post", xNow.Add(time.Second))
	*now = xNow.Add(time.Minute)
	run(t, a, runs)
	f.post("8000000000000000002", "Over the cap", xNow.Add(2*time.Second))
	f.mu.Lock()
	f.deleted["8000000000000000001"] = true
	f.mu.Unlock()
	runs.target.ReadsToday = 100
	lists := f.count("/2/lists/")
	*now = xNow.Add(3 * time.Minute)
	e := run(t, a, runs)
	if e == nil || e.Code != "daily_read_cap_reached" || !e.Completed || e.Class != ClassSource {
		t.Fatalf("cap %+v", e)
	}
	if f.count("/2/lists/") != lists || len(ingest.accepted) != 1 {
		t.Fatal("polled past the daily cap")
	}
	if len(ingest.withdrawn) != 1 || ingest.withdrawn[0].Source.RecordKey != "8000000000000000001" {
		t.Fatalf("deletion recheck must continue at the cap: %v", ingest.withdrawn)
	}
}
