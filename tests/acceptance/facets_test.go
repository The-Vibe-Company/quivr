package acceptance

import (
	"os"
	"testing"
)

// Owns facet lifecycle through the public API: publication, a correction's
// current metadata, and immediate withdrawal across two independent Corpora.
func TestFacetsFollowCurrentDocumentsAcrossCorpora(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	create := func(name string) string {
		return request(t, "POST", "/v0/corpora", admin, map[string]any{"name": name, "idempotency_key": run + name}, 201)["corpus_id"].(string)
	}
	a, b := create("Facets A"), create("Facets B")
	publish := func(id, key, revision, language string) {
		t.Helper()
		cmd := inlineCommand(id, run+id+revision, key, "The harbour ferry report.")
		cmd["source_position"] = revision
		cmd["extensions"] = map[string]any{"quivr.metadata": map[string]any{"schema_version": "1", "data": map[string]any{"language": language}}}
		receipt := request(t, "POST", "/v0/records", admin, cmd, 202)
		awaitRetrievalReady(t, receipt["receipt_id"].(string))
	}
	publish(a, "one", "1", "en")
	publish(b, "two", "1", "en")
	body := map[string]any{"corpus_ids": []string{a, b}, "fields": []any{map[string]any{"field": "metadata.language"}}}
	counts := func() map[string]int {
		t.Helper()
		result := request(t, "POST", "/v0/facets", admin, body, 200)
		items := result["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("facet items: %v", result)
		}
		got := map[string]int{}
		for _, raw := range items[0].(map[string]any)["buckets"].([]any) {
			bucket := raw.(map[string]any)
			got[bucket["value"].(string)] = int(bucket["count"].(float64))
		}
		return got
	}
	if got := counts(); len(got) != 1 || got["en"] != 2 {
		t.Fatalf("published counts %v, want en=2", got)
	}
	publish(a, "one", "2", "fr")
	if got := counts(); len(got) != 2 || got["en"] != 1 || got["fr"] != 1 {
		t.Fatalf("corrected counts %v, want en=1 fr=1", got)
	}
	request(t, "POST", "/v0/records/withdrawals", admin, withdrawalCommand(b, run+"withdraw", "example-feed", "two", "source retraction"), 202)
	if got := counts(); len(got) != 1 || got["fr"] != 1 {
		t.Fatalf("withdrawn counts %v, want fr=1", got)
	}
	request(t, "POST", "/v0/facets", os.Getenv("QUIVR_TEST_SCOPED"), body, 404)
}
