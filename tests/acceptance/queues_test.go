package acceptance

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func queueScenario(t *testing.T) (string, string) {
	t.Helper()
	if os.Getenv("QUIVR_TEST_QUEUES") == "" {
		t.Skip("make verify runs separate live and bulk workers")
	}
	run := os.Getenv("QUIVR_TEST_QUEUE_RUN")
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	makeCorpus := func(name string) string {
		return request(t, "POST", "/v0/corpora", admin, map[string]any{"name": name, "idempotency_key": "queues-" + name + "-" + run}, 201)["corpus_id"].(string)
	}
	return makeCorpus("bulk"), makeCorpus("live")
}

func queueSnapshot(t *testing.T) map[string]any {
	t.Helper()
	return request(t, "GET", "/v0/admin/queues", os.Getenv("QUIVR_TEST_OPERATOR"), nil, 200)["queues"].(map[string]any)
}

// Setup runs before the harness adds the rebuild-only embedder. Both Corpora
// retain their original serving generation while the new rebuild is blocked.
func TestQueueIsolationSeed(t *testing.T) {
	bulk, _ := queueScenario(t)
	blob := uploadBlob(t, os.Getenv("QUIVR_TEST_ADMIN"), pdfFixture(t), "application/pdf")
	receipts := make([]string, 64)
	for i := range receipts {
		body := pdfCommand(bulk, fmt.Sprintf("queue-seed-%d", i), blob, os.Getenv("QUIVR_TEST_QUEUE_RUN"))
		receipts[i] = request(t, "POST", "/v0/records", os.Getenv("QUIVR_TEST_ADMIN"), body, 202)["receipt_id"].(string)
	}
	for _, id := range receipts {
		awaitRetrievalReady(t, id)
	}
}

func TestQueueIsolationDuring(t *testing.T) {
	bulk, live := queueScenario(t)
	_, location := postRebuild(t, os.Getenv("QUIVR_TEST_ADMIN"), bulk, "queues-rebuild-"+os.Getenv("QUIVR_TEST_QUEUE_RUN"), 202)
	// The bulk worker's embedder is held by the harness; it occupies a bulk
	// slot while live has its own process and capacity.
	deadline := time.Now().Add(5 * time.Second)
	var held map[string]any
	for {
		held = queueSnapshot(t)["bulk"].(map[string]any)
		if held["waiting"].(float64) >= 32 && held["in_progress"].(float64) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bulk worker did not admit its backlog: %v (%s)", held, location)
		}
		time.Sleep(20 * time.Millisecond)
	}
	started := time.Now()
	receipt := request(t, "POST", "/v0/records", os.Getenv("QUIVR_TEST_ADMIN"), inlineCommand(live, "queue-live-"+os.Getenv("QUIVR_TEST_QUEUE_RUN"), "live", "Fresh comet observatory report."), 202)["receipt_id"].(string)
	deadline = started.Add(5 * time.Second)
	for {
		r := request(t, "GET", "/v0/ingestion-receipts/"+receipt, os.Getenv("QUIVR_TEST_ADMIN"), nil, 200)
		if a, _ := r["availability"].(map[string]any); a["searchable"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("live item exceeded five seconds behind bulk backlog: %v", r)
		}
		time.Sleep(20 * time.Millisecond)
	}
	hits := request(t, "POST", "/v0/search", os.Getenv("QUIVR_TEST_ADMIN"), map[string]any{"query": "comet observatory", "corpus_ids": []string{live}, "mode": "lexical"}, 200)["items"].([]any)
	if len(hits) != 1 {
		t.Fatalf("live document is searchable while bulk is held: %v", hits)
	}
	held = queueSnapshot(t)["bulk"].(map[string]any)
	if held["waiting"].(float64) < 32 || held["in_progress"].(float64) < 1 {
		t.Fatalf("bulk backlog vanished before live completed: %v", held)
	}
	for _, url := range []string{os.Getenv("QUIVR_TEST_QUEUE_LIVE_METRICS"), os.Getenv("QUIVR_TEST_QUEUE_BULK_METRICS")} {
		res, err := (&http.Client{Timeout: 2 * time.Second}).Get(url)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("queue metrics: status %d error %v", res.StatusCode, err)
		}
		for field, name := range map[string]string{"waiting": "quivr_queue_waiting_documents", "in_progress": "quivr_queue_in_progress_documents"} {
			want := fmt.Sprintf("%s{queue=\"bulk\"} %g\n", name, held[field].(float64))
			if !strings.Contains(string(data), want) {
				t.Fatalf("missing same database gauge %q", want)
			}
		}
	}
	t.Logf("live became searchable in %s with %v waiting bulk documents", time.Since(started), held["waiting"])
}

func TestQueueIsolationDrains(t *testing.T) {
	bulk, _ := queueScenario(t)
	_, location := postRebuild(t, os.Getenv("QUIVR_TEST_ADMIN"), bulk, "queues-rebuild-"+os.Getenv("QUIVR_TEST_QUEUE_RUN"), 202)
	deadline := time.Now().Add(30 * time.Second)
	for {
		op := request(t, "GET", location, os.Getenv("QUIVR_TEST_ADMIN"), nil, 200)
		status := queueSnapshot(t)["bulk"].(map[string]any)
		if op["state"] == "succeeded" && status["waiting"] == float64(0) && status["in_progress"] == float64(0) && status["oldest_waiting_age_seconds"] == float64(0) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bulk backlog did not drain: operation %v queues %v", op, status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
