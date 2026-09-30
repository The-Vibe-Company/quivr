package acceptance

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestSemanticEnrichmentPreservesVersion(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Semantic enrichment", "idempotency_key": "semantic-corpus"}, 201)["corpus_id"].(string)
	command := inlineCommand(c, "semantic-first", "sailing", "Les ferries pour la Corse circulent normalement malgré la grève du port de Marseille.")
	r := awaitReceipt(t, request(t, "POST", "/v0/records", admin, command, 202)["receipt_id"].(string))
	before := awaitSearchable(t, r)
	q := map[string]any{"query": "Are passenger boats to Corsica running during the strike?", "corpus_ids": []string{c}, "mode": "semantic"}
	deadline := time.Now().Add(60 * time.Second)
	var hit map[string]any
	for {
		result := request(t, "POST", "/v0/search", admin, q, 200)
		if items := result["items"].([]any); len(items) > 0 {
			hit = items[0].(map[string]any)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("semantic enrichment never ready")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if hit["version_id"] != r["version_id"] || hit["record_id"] != r["record_id"] || hit["embedding_artifact_id"] == nil || hit["vector_space_id"] == nil {
		t.Fatal(hit)
	}
	after := request(t, "GET", "/v0/records/"+r["record_id"].(string)+"/versions/"+r["version_id"].(string), admin, nil, 200)
	if before["manifest"].(map[string]any)["parts"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"] != after["manifest"].(map[string]any)["parts"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"] {
		t.Fatal("enrichment changed source")
	}
	assertStepTimeline(t, admin, after)
	delete(q, "mode")
	hybrid := request(t, "POST", "/v0/search", admin, q, 200)["items"].([]any)
	if len(hybrid) != 1 || hybrid[0].(map[string]any)["embedding_artifact_id"] != hit["embedding_artifact_id"] {
		t.Fatal(hybrid)
	}
	command["idempotency_key"] = "semantic-replay"
	replay := awaitReceipt(t, request(t, "POST", "/v0/records", admin, command, 202)["receipt_id"].(string))
	if replay["version_id"] != r["version_id"] || replay["outcome"] != "duplicate" {
		t.Fatal(replay)
	}
	again := request(t, "POST", "/v0/search", admin, q, 200)["items"].([]any)
	if len(again) != 1 || again[0].(map[string]any)["embedding_artifact_id"] != hit["embedding_artifact_id"] {
		t.Fatal("unstable artifact", again)
	}
}

// assertStepTimeline checks that an enriched Version reports when each step
// finished, and that its admin timeline lists them in order, each timed from
// the step that caused it.
func assertStepTimeline(t *testing.T, token string, version map[string]any) {
	t.Helper()
	steps, _ := version["steps"].(map[string]any)
	for _, step := range []string{"accepted_at", "materialized_at", "segmented_at", "retrieval_ready_at", "enriched_at"} {
		if _, ok := steps[step].(string); !ok {
			t.Fatalf("version %s has no %s: %v", version["version_id"], step, steps)
		}
	}
	timeline := request(t, "GET", "/v0/admin/documents/"+version["version_id"].(string)+"/timeline", token, nil, 200)
	var order []string
	var previous time.Time
	for _, raw := range timeline["steps"].([]any) {
		step := raw.(map[string]any)
		at, err := time.Parse(time.RFC3339Nano, step["at"].(string))
		if err != nil || at.Before(previous) {
			t.Fatalf("timeline out of order at %v: %v", step, timeline)
		}
		previous = at
		if step["step"] != "accepted" && step["duration_ms"] == nil {
			t.Fatalf("step without a duration: %v", step)
		}
		order = append(order, step["step"].(string))
	}
	if got := strings.Join(order, ","); got != "accepted,materialized,segmented,retrieval_ready,enriched" {
		t.Fatalf("timeline steps %s: %v", got, timeline)
	}
	if doc := timeline["document"].(map[string]any); doc["state"] != "retrieval_ready" || doc["is_current"] != true {
		t.Fatalf("timeline document: %v", doc)
	}
}
