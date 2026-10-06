package acceptance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func changeCorpus(t *testing.T, key string) string {
	t.Helper()
	return request(t, "POST", "/v0/corpora", os.Getenv("QUIVR_TEST_ADMIN"), map[string]any{"name": "Changes " + key, "idempotency_key": "changes-" + key}, 201)["corpus_id"].(string)
}

func changesPath(corpusID, cursor string, limit int) string {
	q := url.Values{"corpus_id": {corpusID}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	return "/v0/changes?" + q.Encode()
}

// drain polls from cursor until has_more is false and returns items and the next cursor.
func drain(t *testing.T, token, corpusID, cursor string, limit int) ([]map[string]any, string) {
	t.Helper()
	var items []map[string]any
	for {
		page := request(t, "GET", changesPath(corpusID, cursor, limit), token, nil, 200)
		for _, item := range page["items"].([]any) {
			items = append(items, item.(map[string]any))
		}
		cursor = page["next_cursor"].(string)
		if page["has_more"] != true {
			return items, cursor
		}
	}
}

func typed(items []map[string]any, kind, id string) int {
	n := 0
	for _, item := range items {
		if item["type"] == kind && item["resource"].(map[string]any)["id"] == id {
			n++
		}
	}
	return n
}

// awaitChange keeps consuming from cursor until an event of kind for id appears.
func awaitChange(t *testing.T, token, corpusID, cursor, kind, id string) ([]map[string]any, string) {
	t.Helper()
	var seen []map[string]any
	deadline := time.Now().Add(30 * time.Second)
	for {
		items, next := drain(t, token, corpusID, cursor, 0)
		seen, cursor = append(seen, items...), next
		if typed(seen, kind, id) > 0 {
			return seen, cursor
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s for %s; saw %v", kind, id, seen)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestChangeFeedPollingInvalidatesRecords(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	a, b := changeCorpus(t, "poll-a"), changeCorpus(t, "poll-b")

	start := request(t, "GET", changesPath(a, "", 0), admin, nil, 200)
	request(t, "POST", "/v0/records", admin, inlineCommand(b, "changes-elsewhere", "elsewhere", "Autre corpus"), 202)
	empty := request(t, "GET", changesPath(a, start["next_cursor"].(string), 0), admin, nil, 200)
	if len(empty["items"].([]any)) != 0 || empty["next_cursor"] == start["next_cursor"] {
		t.Fatal("empty visible page did not advance over committed positions", empty)
	}

	command := inlineCommand(a, "changes-first", "first", "Première dépêche 📰")
	receipt := request(t, "POST", "/v0/records", admin, command, 202)["receipt_id"].(string)
	request(t, "POST", "/v0/records", admin, command, 202) // idempotent replay: no fresh fact
	resolved := awaitReceipt(t, receipt)
	recordID := resolved["record_id"].(string)
	seen, cursor := awaitChange(t, admin, a, empty["next_cursor"].(string), "record.materialized", recordID)
	if typed(seen, "receipt.pending", receipt) != 1 || typed(seen, "record.accepted", recordID) != 1 {
		t.Fatal("acceptance facts missing or duplicated by replay", seen)
	}
	for _, item := range seen {
		resource := item["resource"].(map[string]any)
		if resource["corpus_id"] != a || item["event_id"] == "" || item["cursor"] == "" {
			t.Fatal("event escaped its Corpus or lacks identity", item)
		}
	}
	request(t, "POST", "/v0/records", admin, command, 202)
	if later, _ := drain(t, admin, a, cursor, 0); typed(later, "receipt.pending", receipt)+typed(later, "record.accepted", recordID) != 0 {
		t.Fatal("no-op replay emitted a fresh fact", later)
	}

	withdrawal := request(t, "POST", "/v0/records/withdrawals", admin, withdrawalCommand(a, "changes-withdraw", "example-feed", "first", "retraction"), 202)
	awaitReceipt(t, withdrawal["receipt_id"].(string))
	awaitChange(t, admin, a, cursor, "record.withdrawn", recordID)

	// Small pages reconstruct exactly the same committed sequence.
	whole, _ := drain(t, admin, a, start["next_cursor"].(string), 100)
	paged, _ := drain(t, admin, a, start["next_cursor"].(string), 1)
	if len(paged) < len(whole) {
		t.Fatal("paging lost events", len(paged), len(whole))
	}
	for i := range whole {
		if paged[i]["event_id"] != whole[i]["event_id"] {
			t.Fatal("paging reordered events", i)
		}
	}
}

// post submits one inline command without failing from a non-test goroutine.
func post(token string, body any) (string, error) {
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", os.Getenv("QUIVR_TEST_URL")+"/v0/records", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var receipt map[string]any
	if err = json.NewDecoder(res.Body).Decode(&receipt); err != nil || res.StatusCode != 202 {
		return "", fmt.Errorf("status %d: %v %v", res.StatusCode, receipt, err)
	}
	return receipt["receipt_id"].(string), nil
}

func TestChangeFeedConcurrentCommits(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := changeCorpus(t, "concurrent")
	cursor := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)

	var mu sync.Mutex
	var receipts []string
	var failures []error
	var writers sync.WaitGroup
	for w := 0; w < 6; w++ {
		writers.Add(1)
		go func(w int) {
			defer writers.Done()
			for i := 0; i < 4; i++ {
				key := fmt.Sprintf("concurrent-%d-%d", w, i)
				id, err := post(admin, inlineCommand(c, key, key, "Concurrent "+key))
				mu.Lock()
				if err != nil {
					failures = append(failures, err)
				} else {
					receipts = append(receipts, id)
				}
				mu.Unlock()
			}
		}(w)
	}
	done := make(chan struct{})
	go func() { writers.Wait(); close(done) }()

	// One continuous consumer polls while writers commit concurrently.
	seen := map[string]int{}
	events := map[string]bool{}
	finished := false
	deadline := time.Now().Add(30 * time.Second)
	for {
		items, next := drain(t, admin, c, cursor, 5)
		cursor = next
		for _, item := range items {
			id := item["event_id"].(string)
			if events[id] {
				t.Fatal("continuous consumption repeated an event", id)
			}
			events[id] = true
			if item["type"] == "receipt.pending" {
				seen[item["resource"].(map[string]any)["id"].(string)]++
			}
		}
		select {
		case <-done:
			finished = true
		default:
		}
		mu.Lock()
		complete := finished && len(seen) >= len(receipts)
		mu.Unlock()
		if complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("concurrent commits never all observed", len(seen), len(receipts))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(failures) != 0 {
		t.Fatal(failures)
	}
	if len(receipts) != 24 || len(seen) != 24 {
		t.Fatal("lost or invented events", len(receipts), seen)
	}
	for _, id := range receipts {
		if seen[id] != 1 {
			t.Fatal("receipt observed", seen[id], "times", id)
		}
	}
}

type frame struct{ id, event, data string }

type stream struct {
	body    *http.Response
	scanner *bufio.Scanner
	cancel  context.CancelFunc
}

func openChangeStream(t *testing.T, base, token, path, lastEventID string) *stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	req, _ := http.NewRequestWithContext(ctx, "GET", base+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	s := &stream{body: res, scanner: bufio.NewScanner(res.Body), cancel: cancel}
	t.Cleanup(s.close)
	return s
}

func (s *stream) close() { s.cancel(); s.body.Body.Close() }

func (s *stream) next(t *testing.T) frame {
	t.Helper()
	var f frame
	seen := false
	for s.scanner.Scan() {
		line := s.scanner.Text()
		switch {
		case line == "" && seen:
			return f
		case strings.HasPrefix(line, "id: "):
			f.id, seen = strings.TrimPrefix(line, "id: "), true
		case strings.HasPrefix(line, "event: "):
			f.event, seen = strings.TrimPrefix(line, "event: "), true
		case strings.HasPrefix(line, "data: "):
			f.data, seen = strings.TrimPrefix(line, "data: "), true
		}
	}
	t.Fatal("stream ended", s.scanner.Err())
	return f
}

func (s *stream) nextChange(t *testing.T) (frame, map[string]any) {
	t.Helper()
	for {
		f := s.next(t)
		if f.event == "stream_error" {
			t.Fatal("unexpected stream_error", f.data)
		}
		if f.event == "change" {
			var event map[string]any
			if err := json.Unmarshal([]byte(f.data), &event); err != nil || event["cursor"] != f.id {
				t.Fatal("change frame mismatch", f)
			}
			return f, event
		}
	}
}

func TestChangeStreamResumesWithoutLoss(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	base := os.Getenv("QUIVR_TEST_URL")
	c := changeCorpus(t, "stream")
	path := "/v0/changes/stream?corpus_id=" + url.QueryEscape(c)

	live := openChangeStream(t, base, admin, path, "")
	if live.body.StatusCode != 200 || !strings.HasPrefix(live.body.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal(live.body.StatusCode, live.body.Header)
	}
	checkpoint := live.next(t)
	var data map[string]any
	if checkpoint.event != "checkpoint" || json.Unmarshal([]byte(checkpoint.data), &data) != nil || data["cursor"] != checkpoint.id {
		t.Fatal("missing start-now checkpoint", checkpoint)
	}
	request(t, "POST", "/v0/records", admin, inlineCommand(c, "stream-1", "stream-1", "Flux un"), 202)
	request(t, "POST", "/v0/records", admin, inlineCommand(c, "stream-2", "stream-2", "Flux deux"), 202)
	first, firstEvent := live.nextChange(t)
	second, _ := live.nextChange(t)
	live.close()

	// SSE and polling read the same journal from the same position.
	polled := request(t, "GET", changesPath(c, checkpoint.id, 1), admin, nil, 200)["items"].([]any)
	if len(polled) != 1 || polled[0].(map[string]any)["event_id"] != firstEvent["event_id"] {
		t.Fatal("polling and SSE disagree", polled, firstEvent)
	}
	// Last-Event-ID takes precedence over a stale query cursor.
	resumed := openChangeStream(t, base, admin, path+"&cursor=stale", first.id)
	if f, _ := resumed.nextChange(t); f.id != second.id || f.data != second.data {
		t.Fatal("reconnect lost or invented an event", f, second)
	}
	// Replaying from the checkpoint redelivers with the same deduplication identity.
	replay := openChangeStream(t, base, admin, path, checkpoint.id)
	if _, event := replay.nextChange(t); event["event_id"] != firstEvent["event_id"] {
		t.Fatal("duplicate replay changed event identity", event)
	}
}

// awaitExpired polls a change cursor on base until it answers 410, as it does
// once the event after it is older than that API's retention, and returns the
// error body.
func awaitExpired(t *testing.T, base, token, path string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		req, _ := http.NewRequest("GET", base+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		err = json.NewDecoder(res.Body).Decode(&body)
		res.Body.Close()
		switch {
		case err != nil || (res.StatusCode != 200 && res.StatusCode != 410):
			t.Fatal("cursor read", path, res.StatusCode, body, err)
		case res.StatusCode == 410:
			return body
		case time.Now().After(deadline):
			t.Fatal("cursor never expired", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestChangeCursorExpiry uses a second API process with short retention.
func TestChangeCursorExpiry(t *testing.T) {
	short := os.Getenv("QUIVR_TEST_SHORT_RETENTION_URL")
	if short == "" {
		t.Skip("make verify starts a short-retention API")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := changeCorpus(t, "expiry")
	cursor := request(t, "GET", changesPath(c, "", 0), admin, nil, 200)["next_cursor"].(string)
	request(t, "POST", "/v0/records", admin, inlineCommand(c, "expiry-1", "expiry-1", "Bientôt expiré"), 202)

	if e := awaitExpired(t, short, admin, changesPath(c, cursor, 0)); e["code"] != "cursor_expired" || e["resync_url"] == nil {
		t.Fatal("expired poll cursor", e)
	}
	s := openChangeStream(t, short, admin, "/v0/changes/stream?corpus_id="+url.QueryEscape(c), cursor)
	var e map[string]any
	if err := json.NewDecoder(s.body.Body).Decode(&e); err != nil || s.body.StatusCode != 410 || e["code"] != "cursor_expired" || e["resync_url"] == nil {
		t.Fatal("expired stream cursor before headers", s.body.StatusCode, e, err)
	}
	// The default seven-day window keeps the same cursor valid.
	request(t, "GET", changesPath(c, cursor, 0), admin, nil, 200)
}
