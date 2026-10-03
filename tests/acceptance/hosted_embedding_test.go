package acceptance

import (
	"os"
	"testing"
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
	ready := ingestEnriched(t, corpus, "hosted-"+run, "Library", sentence)
	result := request(t, "POST", "/v0/search", admin, map[string]any{"query": sentence, "corpus_ids": []string{corpus}, "mode": "semantic"}, 200)
	items := result["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("configured space %s returned %v", space, result)
	}
	hit := items[0].(map[string]any)
	if hit["version_id"] != ready || hit["vector_space_id"] != space+"@1" || hit["embedding_artifact_id"] == nil {
		t.Fatalf("search did not use configured embedding: %v", hit)
	}
}
