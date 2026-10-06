package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/changes"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

// memoryJournal is an in-memory commit-ordered journal. Setting expired makes
// every read report the caller's position as past retention; the retention
// window itself is postgres TestChangeJournalWindowsAndRetention's.
type memoryJournal struct {
	mu      sync.Mutex
	events  []changes.Event
	expired bool
	fail    error
}

func (j *memoryJournal) append(corpusID, kind, id string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	p := int64(len(j.events) + 1)
	j.events = append(j.events, changes.Event{Position: p, ID: "event_" + id + "_" + kind, Type: kind, CorpusID: corpusID, ResourceKind: "record", ResourceID: id, OccurredAt: time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)})
}

func (j *memoryJournal) ReadChanges(_ context.Context, _, corpusID string, after int64, limit int, _ time.Duration) (changes.Window, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.fail != nil {
		return changes.Window{}, j.fail
	}
	head := int64(len(j.events))
	w := changes.Window{Head: head, Through: after, Expired: j.expired}
	if limit <= 0 {
		return w, nil
	}
	w.Through = head
	for _, e := range j.events {
		if e.Position <= after || e.CorpusID != corpusID {
			continue
		}
		if len(w.Events) == limit {
			w.Through = w.Events[limit-1].Position
			break
		}
		w.Events = append(w.Events, e)
	}
	return w, nil
}

type knownCorpora struct{}

type readCorpora struct {
	knownCorpora
	fail error
}

func (s *readCorpora) Read(ctx context.Context, org, id string) (corpus.Corpus, error) {
	if s.fail != nil {
		return corpus.Corpus{}, s.fail
	}
	return s.knownCorpora.Read(ctx, org, id)
}

func (knownCorpora) Create(context.Context, string, corpus.CreateInput) (corpus.Corpus, bool, error) {
	return corpus.Corpus{}, false, corpus.ErrForbidden
}
func (knownCorpora) Read(_ context.Context, org, id string) (corpus.Corpus, error) {
	if org == "org_a" && (id == "corpus_a" || id == "corpus_b") {
		return corpus.Corpus{ID: id, Name: "Example corpus", Retrieval: map[string]any{}}, nil
	}
	return corpus.Corpus{}, corpus.ErrNotFound
}
func (knownCorpora) List(_ context.Context, _ corpus.Scope, after string, limit int) ([]corpus.Corpus, error) {
	out := []corpus.Corpus{}
	for _, id := range []string{"corpus_a", "corpus_b"} {
		if id > after && len(out) < limit {
			out = append(out, corpus.Corpus{ID: id, Name: "Example corpus", Retrieval: map[string]any{}})
		}
	}
	return out, nil
}

const (
	feedReader  = "feed-reader-token-0123456789abcdef0123456789"
	otherScope  = "feed-scoped-token-0123456789abcdef0123456789"
	foreignFeed = "feed-foreign-token-0123456789abcdef0123456"
	noFeed      = "feed-denied-token-0123456789abcdef0123456789"
)

func changeServer(t *testing.T, journal *memoryJournal, stores ...corpus.Store) *httptest.Server {
	t.Helper()
	keys := map[string]corpus.Scope{
		feedReader:  {Organization: "org_a", Actions: []string{"changes:read"}, Corpora: []string{"*"}},
		otherScope:  {Organization: "org_a", Actions: []string{"changes:read"}, Corpora: []string{"corpus_a"}},
		foreignFeed: {Organization: "org_b", Actions: []string{"changes:read"}, Corpora: []string{"*"}},
		noFeed:      {Organization: "org_a", Actions: []string{"content:read"}, Corpora: []string{"*"}},
	}
	key := []byte("cursor-key-0123456789abcdef0123456789")
	feed := changes.Service{Journal: journal, Key: key, Retention: time.Second}
	// Streams read the journal every 5 ms, like a deployment's change_stream_poll.
	var store corpus.Store = knownCorpora{}
	if len(stores) > 0 {
		store = stores[0]
	}
	handler, err := httpapi.New(store, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, key, httpapi.WithChanges(feed, 5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(checkedAPI(t, handler))
	t.Cleanup(server.Close)
	return server
}

func getJSON(t *testing.T, server *httptest.Server, path, token string, want int) map[string]any {
	t.Helper()
	req, _ := http.NewRequest("GET", server.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	if err = json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != want {
		t.Fatalf("GET %s: got %d want %d: %v", path, res.StatusCode, want, body)
	}
	return body
}

type frame struct{ id, event, data string }

type sse struct {
	res     *http.Response
	scanner *bufio.Scanner
}

// openStream gives the stream a deadline, so a stream that never sends or
// never closes fails the test instead of hanging it.
func openStream(t *testing.T, server *httptest.Server, path, token, lastEventID string) (*sse, *http.Response) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return &sse{res: res, scanner: bufio.NewScanner(res.Body)}, res
}

// next returns the next dispatched frame, skipping comments; ok is false when
// the server closed the stream. A read error, such as the deadline, fails the
// test: it is not a close.
func (s *sse) next(t *testing.T) (frame, bool) {
	t.Helper()
	var f frame
	seen := false
	for s.scanner.Scan() {
		line := s.scanner.Text()
		switch {
		case line == "":
			if seen {
				return f, true
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "id: "):
			f.id, seen = strings.TrimPrefix(line, "id: "), true
		case strings.HasPrefix(line, "event: "):
			f.event, seen = strings.TrimPrefix(line, "event: "), true
		case strings.HasPrefix(line, "data: "):
			f.data, seen = strings.TrimPrefix(line, "data: "), true
		}
	}
	if err := s.scanner.Err(); err != nil {
		t.Fatalf("stream read: %v", err)
	}
	return f, false
}

func TestPollingStartsNowAndPagesVisibleEvents(t *testing.T) {
	journal := &memoryJournal{}
	journal.append("corpus_a", "record.accepted", "before")
	server := changeServer(t, journal)

	start := getJSON(t, server, "/v0/changes?corpus_id=corpus_a", feedReader, 200)
	if items := start["items"].([]any); len(items) != 0 || start["has_more"] != false || start["next_cursor"] == "" {
		t.Fatal("start-now page replayed history", start)
	}
	journal.append("corpus_b", "record.accepted", "elsewhere")
	empty := getJSON(t, server, "/v0/changes?corpus_id=corpus_a&cursor="+start["next_cursor"].(string), feedReader, 200)
	if len(empty["items"].([]any)) != 0 || empty["next_cursor"] == start["next_cursor"] {
		t.Fatal("empty visible page did not advance", empty)
	}
	journal.append("corpus_a", "record.accepted", "one")
	journal.append("corpus_a", "record.withdrawn", "one")
	first := getJSON(t, server, "/v0/changes?limit=1&corpus_id=corpus_a&cursor="+empty["next_cursor"].(string), feedReader, 200)
	items := first["items"].([]any)
	if len(items) != 1 || first["has_more"] != true {
		t.Fatal("limit not honoured", first)
	}
	event := items[0].(map[string]any)
	resource := event["resource"].(map[string]any)
	if event["type"] != "record.accepted" || resource["kind"] != "record" || resource["id"] != "one" || resource["corpus_id"] != "corpus_a" || event["event_id"] == "" || event["cursor"] != first["next_cursor"] || event["schema_version"] != "1" {
		t.Fatal("compact reference mismatch", event)
	}
	second := getJSON(t, server, "/v0/changes?corpus_id=corpus_a&cursor="+first["next_cursor"].(string), feedReader, 200)
	if items := second["items"].([]any); len(items) != 1 || items[0].(map[string]any)["type"] != "record.withdrawn" || second["has_more"] != false {
		t.Fatal("resume lost or duplicated events", second)
	}
}

func TestPollingRejectsForeignCursorsAndScopes(t *testing.T) {
	journal := &memoryJournal{}
	server := changeServer(t, journal)
	cursor := getJSON(t, server, "/v0/changes?corpus_id=corpus_a", feedReader, 200)["next_cursor"].(string)

	for _, tc := range []struct {
		path, token string
		status      int
		code        string
	}{
		{"/v0/changes?corpus_id=corpus_a", noFeed, 403, "forbidden"},
		{"/v0/changes?corpus_id=corpus_b", otherScope, 404, "not_found"},
		{"/v0/changes?corpus_id=corpus_a", foreignFeed, 404, "not_found"},
		{"/v0/changes?corpus_id=missing", feedReader, 404, "not_found"},
		{"/v0/changes", feedReader, 422, "invalid_query"},
		{"/v0/changes?corpus_id=corpus_a&cursor=", feedReader, 422, "invalid_cursor"},
		{"/v0/changes?corpus_id=corpus_a&limit=0", feedReader, 422, "invalid_limit"},
		{"/v0/changes?corpus_id=corpus_a&limit=101", feedReader, 422, "invalid_limit"},
		{"/v0/changes?corpus_id=corpus_a&limit=", feedReader, 422, "invalid_limit"},
		// A key in the query string is refused, never used.
		{"/v0/changes?corpus_id=corpus_a&api_key=" + feedReader, feedReader, 422, "invalid_query"},
		{"/v0/changes?corpus_id=corpus_a&cursor=x" + cursor, feedReader, 422, "invalid_cursor"},
	} {
		if e := getJSON(t, server, tc.path, tc.token, tc.status); e["code"] != tc.code {
			t.Errorf("GET %s: code %v, want %s", tc.path, e["code"], tc.code)
		}
	}
	// A changed Corpus filter or authorization scope discards the traversal and
	// names the Record catalog to resynchronize from.
	for _, tc := range []struct{ corpusID, token string }{{"corpus_b", feedReader}, {"corpus_a", otherScope}} {
		e := getJSON(t, server, "/v0/changes?corpus_id="+tc.corpusID+"&cursor="+cursor, tc.token, 409)
		if e["code"] != "cursor_scope_changed" || e["resync_url"] != "/v0/records?corpus_id="+tc.corpusID {
			t.Fatal(e)
		}
	}
}

func TestExpiredCursorBeforeStreamIs410WithResync(t *testing.T) {
	journal := &memoryJournal{}
	server := changeServer(t, journal)
	cursor := getJSON(t, server, "/v0/changes?corpus_id=corpus_a", feedReader, 200)["next_cursor"].(string)
	journal.append("corpus_a", "record.accepted", "aged")
	journal.expired = true

	e := getJSON(t, server, "/v0/changes?corpus_id=corpus_a&cursor="+cursor, feedReader, 410)
	if e["code"] != "cursor_expired" || e["resync_url"] != "/v0/records?corpus_id=corpus_a" {
		t.Fatal(e)
	}
	_, res := openStream(t, server, "/v0/changes/stream?corpus_id=corpus_a", feedReader, cursor)
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	if res.StatusCode != 410 || body["code"] != "cursor_expired" || body["resync_url"] != "/v0/records?corpus_id=corpus_a" {
		t.Fatal("stream did not reject expired cursor before headers", res.StatusCode, body)
	}
}

func TestStreamCheckpointsChangesAndResumesFromLastEventID(t *testing.T) {
	journal := &memoryJournal{}
	journal.append("corpus_a", "record.accepted", "history")
	server := changeServer(t, journal)

	stream, res := openStream(t, server, "/v0/changes/stream?corpus_id=corpus_a", feedReader, "")
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal(res.StatusCode, res.Header)
	}
	checkpoint, ok := stream.next(t)
	var data map[string]any
	if !ok || checkpoint.event != "checkpoint" || json.Unmarshal([]byte(checkpoint.data), &data) != nil || data["cursor"] != checkpoint.id {
		t.Fatal("missing start-now checkpoint", checkpoint)
	}
	journal.append("corpus_a", "record.accepted", "one")
	journal.append("corpus_a", "record.accepted", "two")
	var got []frame
	for len(got) < 2 {
		f, ok := stream.next(t)
		if !ok {
			t.Fatal("stream closed early")
		}
		if f.event == "change" {
			got = append(got, f)
		}
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(got[0].data), &event); err != nil || event["cursor"] != got[0].id || event["resource"].(map[string]any)["id"] != "one" {
		t.Fatal("change frame mismatch", got[0])
	}

	// Last-Event-ID wins over a stale query cursor and resumes after the first change.
	resumed, res := openStream(t, server, "/v0/changes/stream?corpus_id=corpus_a&cursor=bogus", feedReader, got[0].id)
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	f, ok := resumed.next(t)
	if !ok || f.event != "change" || f.data != got[1].data || f.id != got[1].id {
		t.Fatal("resume lost or invented an event", f, got[1])
	}
	// The start-now checkpoint is itself a resume point: resuming from it
	// delivers the first change again (identity across replays is acceptance
	// TestChangeStreamResumesWithoutLoss's).
	replay, _ := openStream(t, server, "/v0/changes/stream?corpus_id=corpus_a", feedReader, checkpoint.id)
	if f, ok := replay.next(t); !ok || f.data != got[0].data {
		t.Fatal("replay changed event identity", f)
	}
}

func TestStreamErrorOnInStreamExpiryDoesNotAdvanceID(t *testing.T) {
	journal := &memoryJournal{}
	server := changeServer(t, journal)
	stream, _ := openStream(t, server, "/v0/changes/stream?corpus_id=corpus_a", feedReader, "")
	if f, ok := stream.next(t); !ok || f.event != "checkpoint" {
		t.Fatal(f)
	}
	journal.mu.Lock()
	journal.expired = true
	journal.mu.Unlock()
	journal.append("corpus_a", "record.accepted", "late")
	f, ok := stream.next(t)
	var e map[string]any
	if !ok || f.event != "stream_error" || f.id != "" || json.Unmarshal([]byte(f.data), &e) != nil || e["code"] != "cursor_expired" || e["resync_url"] != "/v0/records?corpus_id=corpus_a" {
		t.Fatal("expected stream_error without id", f)
	}
	if f, ok := stream.next(t); ok {
		t.Fatal("stream stayed open after stream_error", f)
	}
}

func TestChangeReadOutages(t *testing.T) {
	for _, stage := range []string{"corpus read", "journal poll"} {
		t.Run(stage, func(t *testing.T) {
			store := &readCorpora{}
			journal := &memoryJournal{}
			server := changeServer(t, journal, store)
			outage := errors.New("storage offline")
			if stage == "corpus read" {
				store.fail = outage
			} else {
				journal.fail = outage
			}
			body := getJSON(t, server, "/v0/changes?corpus_id=corpus_a", feedReader, 503)
			if body["code"] != "changes_unavailable" || body["retryable"] != true {
				t.Fatalf("%s: want retryable changes_unavailable, got %v", stage, body)
			}
		})
	}
}

func TestStreamStorageErrorDoesNotAdvanceID(t *testing.T) {
	journal := &memoryJournal{}
	server := changeServer(t, journal)
	stream, _ := openStream(t, server, "/v0/changes/stream?corpus_id=corpus_a", feedReader, "")
	if checkpoint, ok := stream.next(t); !ok || checkpoint.event != "checkpoint" {
		t.Fatalf("want initial checkpoint, got %+v", checkpoint)
	}
	journal.mu.Lock()
	journal.fail = errors.New("storage offline")
	journal.mu.Unlock()
	got, ok := stream.next(t)
	var body map[string]any
	if !ok || got.event != "stream_error" || got.id != "" || json.Unmarshal([]byte(got.data), &body) != nil || body["code"] != "changes_unavailable" || body["retryable"] != true {
		t.Fatalf("want retryable stream_error without id, got %+v", got)
	}
	if got, ok := stream.next(t); ok {
		t.Fatalf("stream stays open after storage failure: %+v", got)
	}
}

func TestStreamCheckpointsInvisiblePositions(t *testing.T) {
	journal := &memoryJournal{}
	server := changeServer(t, journal)
	stream, _ := openStream(t, server, "/v0/changes/stream?corpus_id=corpus_a", feedReader, "")
	initial, ok := stream.next(t)
	if !ok || initial.event != "checkpoint" {
		t.Fatalf("want initial checkpoint, got %+v", initial)
	}
	journal.append("corpus_b", "record.accepted", "invisible")
	got, ok := stream.next(t)
	var body map[string]any
	if !ok || got.event != "checkpoint" || got.id == "" || got.id == initial.id || json.Unmarshal([]byte(got.data), &body) != nil || body["cursor"] != got.id {
		t.Fatalf("want advanced checkpoint without an invisible event, got %+v", got)
	}
	resumed, _ := openStream(t, server, "/v0/changes/stream?corpus_id=corpus_a", feedReader, got.id)
	journal.append("corpus_a", "record.accepted", "visible")
	if got, ok := resumed.next(t); !ok || got.event != "change" || !strings.Contains(got.data, `"id":"visible"`) {
		t.Fatalf("want next visible change after checkpoint, got %+v", got)
	}
}
