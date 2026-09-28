package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/changes"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

// memoryJournal is an in-memory commit-ordered journal with a controllable expiry.
type memoryJournal struct {
	mu      sync.Mutex
	events  []changes.Event
	expired func(after int64) bool
}

func (j *memoryJournal) append(corpusID, kind, id string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	p := int64(len(j.events) + 1)
	j.events = append(j.events, changes.Event{Position: p, ID: "event_" + id + "_" + kind, Type: kind, CorpusID: corpusID, ResourceKind: "record", ResourceID: id, OccurredAt: time.Now().UTC()})
}

func (j *memoryJournal) ReadChanges(_ context.Context, _, corpusID string, after int64, limit int, _ time.Duration) (changes.Window, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	head := int64(len(j.events))
	w := changes.Window{Head: head, Through: after, Expired: j.expired != nil && after < head && j.expired(after)}
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

func (knownCorpora) Create(context.Context, string, corpus.CreateInput) (corpus.Corpus, bool, error) {
	return corpus.Corpus{}, false, corpus.ErrForbidden
}
func (knownCorpora) Read(_ context.Context, org, id string) (corpus.Corpus, error) {
	if org == "org_a" && (id == "corpus_a" || id == "corpus_b") {
		return corpus.Corpus{ID: id}, nil
	}
	return corpus.Corpus{}, corpus.ErrNotFound
}
func (knownCorpora) List(_ context.Context, _ corpus.Scope, after string, limit int) ([]corpus.Corpus, error) {
	out := []corpus.Corpus{}
	for _, id := range []string{"corpus_a", "corpus_b"} {
		if id > after && len(out) < limit {
			out = append(out, corpus.Corpus{ID: id})
		}
	}
	return out, nil
}

const (
	feedReader = "feed-reader-token-0123456789abcdef0123456789"
	otherScope = "feed-scoped-token-0123456789abcdef0123456789"
	noFeed     = "feed-denied-token-0123456789abcdef0123456789"
)

func changeServer(t *testing.T, journal *memoryJournal) *httptest.Server {
	t.Helper()
	keys := map[string]corpus.Scope{
		feedReader: {Organization: "org_a", Actions: []string{"changes:read"}, Corpora: []string{"*"}},
		otherScope: {Organization: "org_a", Actions: []string{"changes:read"}, Corpora: []string{"corpus_a"}},
		noFeed:     {Organization: "org_a", Actions: []string{"content:read"}, Corpora: []string{"*"}},
	}
	key := []byte("cursor-key-0123456789abcdef0123456789")
	feed := changes.Service{Journal: journal, Key: key, Retention: time.Second}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, key, httpapi.WithChanges(feed))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
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

func openStream(t *testing.T, server *httptest.Server, path, token, lastEventID string) (*sse, *http.Response) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
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

// next returns the next dispatched frame, skipping comments; ok is false at EOF.
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

	getJSON(t, server, "/v0/changes?corpus_id=corpus_a", noFeed, 403)
	getJSON(t, server, "/v0/changes?corpus_id=corpus_b", otherScope, 404)
	getJSON(t, server, "/v0/changes?corpus_id=missing", feedReader, 404)
	getJSON(t, server, "/v0/changes", feedReader, 422)
	getJSON(t, server, "/v0/changes?corpus_id=corpus_a&cursor=", feedReader, 422)
	getJSON(t, server, "/v0/changes?corpus_id=corpus_a&limit=101", feedReader, 422)
	getJSON(t, server, "/v0/changes?corpus_id=corpus_a&api_key="+feedReader, feedReader, 422)
	if e := getJSON(t, server, "/v0/changes?corpus_id=corpus_b&cursor="+cursor, feedReader, 409); e["code"] != "cursor_scope_changed" {
		t.Fatal(e)
	}
	if e := getJSON(t, server, "/v0/changes?corpus_id=corpus_a&cursor="+cursor, otherScope, 409); e["code"] != "cursor_scope_changed" {
		t.Fatal(e)
	}
	if e := getJSON(t, server, "/v0/changes?corpus_id=corpus_a&cursor=x"+cursor, feedReader, 422); e["code"] != "invalid_cursor" {
		t.Fatal(e)
	}
}

func TestExpiredCursorBeforeStreamIs410WithResync(t *testing.T) {
	journal := &memoryJournal{}
	server := changeServer(t, journal)
	cursor := getJSON(t, server, "/v0/changes?corpus_id=corpus_a", feedReader, 200)["next_cursor"].(string)
	journal.append("corpus_a", "record.accepted", "aged")
	journal.expired = func(int64) bool { return true }

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
	// Replaying from the checkpoint duplicates delivery with the same event identity.
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
	journal.expired = func(int64) bool { return true }
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
