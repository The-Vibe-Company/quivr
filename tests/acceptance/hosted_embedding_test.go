package acceptance

import (
	"os"
	"testing"
	"unicode/utf8"
)

// This owns the transport/lifecycle proof unit provider tests cannot see:
// configured manifests really pin a space used by ingestion and query encoding.
func TestHostedEmbeddingPinFindsText(t *testing.T) {
	space := os.Getenv("QUIVR_TEST_HOSTED_SPACE")
	if space == "" {
		t.Skip("hosted embedding stack step")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := monitoringRun()
	corpus := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Hosted embeddings", "idempotency_key": "hosted-" + run}, 201)["corpus_id"].(string)
	sentence := "The library opens downtown on Wednesday."
	second := "Une bibliothèque 🌌 ouvre ses portes."
	start := request(t, "GET", changesPath(corpus, "", 0), admin, nil, 200)["next_cursor"].(string)
	command := inlineCommand(corpus, "hosted-"+run, "Library", sentence)
	command["content"] = map[string]any{"kind": "manifest", "parts": []any{
		manifestPart("headline", "title", "text", "Library"), manifestPart("paragraph-1", "body", "text", sentence), manifestPart("paragraph-2", "body", "text", second),
	}}
	accepted := request(t, "POST", "/v0/records", admin, command, 202)
	receipt := awaitRetrievalReady(t, accepted["receipt_id"].(string))
	awaitEnriched(t, admin, corpus, start, receipt["record_id"].(string))
	ready := receipt["version_id"].(string)
	// Bounded continuation embeds each source Part independently. Queries for
	// the headline and final Part must resolve to their own complete vectors.
	for _, passage := range []struct{ key, text string }{
		{"headline", "Library"}, {"paragraph-1", sentence}, {"paragraph-2", second},
	} {
		result := request(t, "POST", "/v0/search", admin, map[string]any{"query": passage.text, "corpus_ids": []string{corpus}, "mode": "semantic"}, 200)
		items := result["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("configured space %s returned %v", space, result)
		}
		hit := items[0].(map[string]any)
		if hit["version_id"] != ready || hit["vector_space_id"] != space+"@1" || hit["embedding_artifact_id"] == nil {
			t.Fatalf("search did not use configured embedding: %v", hit)
		}
		excerpt := hit["excerpt"].(map[string]any)
		if hit["part_key"] != passage.key || excerpt["text"] != passage.text || excerpt["start"] != float64(0) || excerpt["end"] != float64(utf8.RuneCountInString(passage.text)) || hit["passage_text"] != passage.text {
			t.Fatalf("source passage %s changed: %v", passage.key, hit)
		}
		sources := hit["source_excerpts"].([]any)
		if len(sources) != 1 {
			t.Fatalf("source excerpts: %v", sources)
		}
		source := sources[0].(map[string]any)
		if source["part_key"] != passage.key || source["text"] != passage.text || source["start"] != float64(0) || source["end"] != float64(utf8.RuneCountInString(passage.text)) || source["coordinate_system"] != "unicode_codepoint" {
			t.Fatalf("canonical source %s: %v", passage.key, source)
		}
	}
}
