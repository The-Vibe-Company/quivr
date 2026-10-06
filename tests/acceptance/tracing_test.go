package acceptance

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Owns the assembled trace contract at public API + real OTLP collector: no
// reads of engine stores, and no fake supplied parentage. Lost outbox context,
// absent Temporal interceptors or dependency spans detach this real journey.
func TestTraceIngestionAndPluginSearch(t *testing.T) {
	collector := os.Getenv("QUIVR_TEST_OTLP_URL")
	if collector == "" {
		t.Skip("make verify core enables the collector for this journey")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	token := os.Getenv("QUIVR_TEST_OBSERVER")
	run := monitoringRun()
	corpus := request(t, "POST", "/v0/corpora", token, map[string]any{"name": "Trace journey", "idempotency_key": run}, 201)["corpus_id"].(string)
	traceID := func() string {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(id[:])
	}
	ingestTrace, searchTrace := traceID(), traceID()
	call := func(method, path, id string, body any, status int) map[string]any {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var input io.Reader
		if body != nil {
			input = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, os.Getenv("QUIVR_TEST_URL")+path, input)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("traceparent", "00-"+id+"-2222222222222222-01")
		req.Header.Set("X-Request-ID", "trace-journey-"+run)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var result map[string]any
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != status || response.Header.Get("X-Trace-ID") != id {
			t.Fatalf("%s: status %d trace %s body %v", path, response.StatusCode, response.Header.Get("X-Trace-ID"), result)
		}
		return result
	}
	const contentText = "Traceprivacy sentinel lantern"
	accepted := call("POST", "/v0/records", ingestTrace, inlineCommand(corpus, "trace-"+run, "trace-"+run, contentText), 202)
	// Wait for published availability before the first search. Querying an empty
	// Corpus caches zero space coverage for ten seconds, outside this test budget.
	// Poll only public receipt/version state, bounded by the same deadline.
	receiptID, ok := accepted["receipt_id"].(string)
	if !ok || receiptID == "" {
		t.Fatal("accepted command has no receipt ID")
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		receipt := call("GET", "/v0/ingestion-receipts/"+receiptID, ingestTrace, nil, 200)
		if receipt["state"] == "resolved" {
			version := call("GET", "/v0/records/"+receipt["record_id"].(string)+"/versions/"+receipt["version_id"].(string), ingestTrace, nil, 200)
			if version["availability"].(map[string]any)["searchable"] == true {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("receipt never became searchable: %v", receipt)
		case <-ticker.C:
		}
	}
	// Poll the public searchable condition, bounded by this test's one deadline.
	var hits []any
	for len(hits) == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("ingested record never became searchable")
		case <-ticker.C:
		}
		hits = call("POST", "/v0/search", searchTrace, map[string]any{"query": "Traceprivacy", "corpus_ids": []string{corpus}, "mode": "lexical"}, 200)["items"].([]any)
	}
	type span struct {
		Name       string         `json:"name"`
		ID         string         `json:"span_id"`
		Parent     string         `json:"parent_span_id"`
		Attributes map[string]any `json:"attributes"`
	}
	read := func(id string) []span {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, "GET", collector+"/traces/"+id, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var data struct {
			Spans []span `json:"spans"`
		}
		if err := json.NewDecoder(response.Body).Decode(&data); err != nil {
			t.Fatal(err)
		}
		return data.Spans
	}
	required := map[string][]string{ingestTrace: {"POST /v0/records", "temporal.StartWorkflow", "temporal.RunWorkflow", "temporal.RunActivity", "postgres.query", "s3.request", "weaviate.request", "plugin.call"}, searchTrace: {"POST /v0/search", "plugin.call", "weaviate.request", "postgres.query"}}
	for id, names := range required {
		var spans []span
		for {
			spans = read(id)
			found := map[string]bool{}
			for _, s := range spans {
				found[s.Name] = true
			}
			complete := true
			for _, name := range names {
				complete = complete && found[name]
			}
			if complete {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("trace %s missing required operations %v (%d spans observed)", id, names, len(spans))
			case <-ticker.C:
			}
		}
		ids := map[string]bool{"2222222222222222": true}
		for _, s := range spans {
			ids[s.ID] = true
		}
		for _, s := range spans {
			if s.Parent == "" || !ids[s.Parent] {
				t.Fatalf("detached span %s with parent %s in trace %s", s.ID, s.Parent, id)
			}
			values := s.Name + fmt.Sprint(s.Attributes)
			for _, private := range []string{contentText, "Traceprivacy", corpus, token} {
				if strings.Contains(values, private) {
					t.Fatalf("trace %s exports forbidden request data in span %s", id, s.ID)
				}
			}
		}
		if directory := os.Getenv("QUIVR_TEST_CAPTURES"); directory != "" {
			raw, _ := json.Marshal(spans)
			if err := os.WriteFile(directory+"/trace-"+id+".json", raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
