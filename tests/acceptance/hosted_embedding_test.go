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
	// The configured owner packs both body Parts. Search must expose their
	// complete canonical sources while the legacy excerpt stays the first slice.
	for _, query := range []string{"Library", sentence, second} {
		result := request(t, "POST", "/v0/search", admin, map[string]any{"query": query, "corpus_ids": []string{corpus}, "mode": "semantic"}, 200)
		items := result["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("configured space %s returned %v", space, result)
		}
		hit := items[0].(map[string]any)
		if hit["version_id"] != ready || hit["vector_space_id"] != space+"@1" || hit["embedding_artifact_id"] == nil {
			t.Fatalf("search did not use configured embedding: %v", hit)
		}
		excerpt := hit["excerpt"].(map[string]any)
		if hit["part_key"] != "paragraph-1" || excerpt["text"] != sentence || excerpt["start"] != float64(0) || excerpt["end"] != float64(utf8.RuneCountInString(sentence)) || hit["passage_text"] != sentence+"\n\n"+second {
			t.Fatalf("packed source passage changed for query %q: %v", query, hit)
		}
		sources := hit["source_excerpts"].([]any)
		if len(sources) != 2 {
			t.Fatalf("source excerpts: %v", sources)
		}
		for n, part := range []struct{ key, text string }{{"paragraph-1", sentence}, {"paragraph-2", second}} {
			source := sources[n].(map[string]any)
			if source["part_key"] != part.key || source["text"] != part.text || source["start"] != float64(0) || source["end"] != float64(utf8.RuneCountInString(part.text)) || source["coordinate_system"] != "unicode_codepoint" {
				t.Fatalf("canonical source %s: %v", part.key, source)
			}
		}
	}
}
