package acceptance

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// awaitStats polls an admin stats read until ok finds what the flushed
// rollups should hold; the stack flushes every 200 ms.
func awaitStats(t *testing.T, token, path string, ok func(items []map[string]any) bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		body := request(t, "GET", path, token, nil, 200)
		var items []map[string]any
		for _, raw := range body["items"].([]any) {
			items = append(items, raw.(map[string]any))
		}
		if ok(items) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s never showed the expected series: %v", path, body)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func counted(item map[string]any) bool {
	summary, _ := item["summary"].(map[string]any)
	count, _ := summary["count"].(float64)
	_, timed := summary["p95_ms"].(float64)
	points, _ := item["points"].([]any)
	return count >= 1 && timed && len(points) >= 1
}

// TestObservabilityStats ingests a text and searches it in its own
// Organization, then reads it all back through the admin stats reads: the
// ingestion plugin's calls, the search by mode and profile, the processing
// steps, the document received from its source namespace and, since the
// stack records query text, the normalized query with its hourly count.
func TestObservabilityStats(t *testing.T) {
	token := os.Getenv("QUIVR_TEST_OBSERVER")
	if os.Getenv("QUIVR_TEST_URL") == "" || token == "" {
		t.Skip("make verify runs the stack with an observability:read key")
	}
	run := fmt.Sprint(time.Now().UnixNano())
	corpusID := request(t, "POST", "/v0/corpora", token, map[string]any{"name": "Observability", "idempotency_key": "observability-" + run}, 201)["corpus_id"].(string)
	request(t, "POST", "/v0/records", token, inlineCommand(corpusID, "observed-"+run, "observed-"+run, "Heliotrope lanterns "+run), 202)
	query := "  Heliotrope   LANTERNS " + run
	deadline := time.Now().Add(30 * time.Second)
	for {
		hits := request(t, "POST", "/v0/search", token, map[string]any{"query": query, "corpus_ids": []string{corpusID}, "mode": "lexical"}, 200)["items"].([]any)
		if len(hits) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the ingested text never became searchable")
		}
		time.Sleep(100 * time.Millisecond)
	}

	awaitStats(t, token, "/v0/admin/stats/searches?window=1h", func(items []map[string]any) bool {
		for _, item := range items {
			if item["mode"] == "lexical" && item["profile"] == "default" && counted(item) && item["results"].(float64) >= 1 {
				return true
			}
		}
		return false
	})
	awaitStats(t, token, "/v0/admin/stats/plugins?window=24h", func(items []map[string]any) bool {
		for _, item := range items {
			if item["plugin_id"] == "core.ingest" && item["operation"] == "segment_and_embed" && counted(item) {
				return true
			}
		}
		return false
	})
	awaitStats(t, token, "/v0/admin/stats/steps?window=7d", func(items []map[string]any) bool {
		steps := map[string]bool{}
		for _, item := range items {
			steps[item["step"].(string)] = counted(item)
		}
		return steps["baseline"] && steps["accepted_to_searchable"]
	})
	// The text was a new revision from inlineCommand's source namespace.
	awaitStats(t, token, "/v0/admin/stats/received?window=24h&limit=100", func(items []map[string]any) bool {
		for _, item := range items {
			if points, _ := item["points"].([]any); item["source_namespace"] == "example-feed" && item["count"].(float64) >= 1 && len(points) >= 1 {
				return true
			}
		}
		return false
	})
	normalized := strings.ToLower("heliotrope lanterns " + run)
	awaitStats(t, token, "/v0/admin/stats/top-queries?window=1h", func(items []map[string]any) bool {
		for _, item := range items {
			if points, _ := item["points"].([]any); item["query"] == normalized && len(points) >= 1 {
				return true
			}
		}
		return false
	})
}
