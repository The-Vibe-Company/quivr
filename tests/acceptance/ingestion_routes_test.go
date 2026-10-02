package acceptance

import (
	"os"
	"testing"
)

// The harness pins core.ingest beside the manifest-driven fakeplugin. PDFs
// normalize through pdf-text before reaching core.ingest; inline text reaches
// fixture; an unlisted source type falls back to core.ingest. This public
// journey owns the assembled source-routing and search contract.
const routedFixtureSpace = "example.fixture_ingest.small@1"

func TestIngestionSourceRoutes(t *testing.T) {
	run := os.Getenv("QUIVR_TEST_INGESTION_ROUTES")
	if run == "" {
		t.Skip("make verify pins two routed ingestion owners")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	corpusID := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Source routing", "idempotency_key": "source-routes-" + run}, 201)["corpus_id"].(string)
	inline := ingestEnriched(t, corpusID, "route-inline-"+run, "inline", "A heliotrope aurora glows above the harbour.")
	cursor := request(t, "GET", changesPath(corpusID, "", 0), admin, nil, 200)["next_cursor"].(string)
	pdf := awaitRetrievalReady(t, request(t, "POST", "/v0/records", admin, pdfCommand(corpusID, "route-pdf", uploadBlob(t, admin, pdfFixture(t), "application/pdf"), run), 202)["receipt_id"].(string))
	pdfVersion := pdf["version_id"].(string)
	awaitEnriched(t, admin, corpusID, cursor, pdf["record_id"].(string))
	blob := uploadBlob(t, admin, []byte("The heliotrope aurora illuminates the garden."), "text/html")
	fallbackCmd := inlineCommand(corpusID, "route-fallback-"+run, "fallback", "")
	fallbackCmd["content"] = map[string]any{"kind": "blob", "blob_id": blob, "media_type": "text/html"}
	cursor = request(t, "GET", changesPath(corpusID, "", 0), admin, nil, 200)["next_cursor"].(string)
	fallback := awaitRetrievalReady(t, request(t, "POST", "/v0/records", admin, fallbackCmd, 202)["receipt_id"].(string))
	fallbackVersion := fallback["version_id"].(string)
	awaitEnriched(t, admin, corpusID, cursor, fallback["record_id"].(string))
	spaces, total := vectorSpaces(t, corpusID)
	if spaces[coreIngestSpace]["role"] != "served" || spaces[routedFixtureSpace]["role"] != "served" || coverage(spaces[coreIngestSpace]) == 0 || coverage(spaces[routedFixtureSpace]) == 0 || coverage(spaces[coreIngestSpace])+coverage(spaces[routedFixtureSpace]) != total {
		t.Fatalf("source routes must split coverage between served owners: %v of %v", spaces, total)
	}
	for _, mode := range []string{"semantic", "hybrid"} {
		result := request(t, "POST", "/v0/search", admin, map[string]any{"query": "heliotrope aurora", "corpus_ids": []string{corpusID}, "mode": mode, "limit": 50}, 200)
		found := map[string]string{}
		for _, raw := range result["items"].([]any) {
			hit := raw.(map[string]any)
			found[hit["version_id"].(string)] = hit["vector_space_id"].(string)
		}
		if found[inline] != routedFixtureSpace || found[pdfVersion] != coreIngestSpace || found[fallbackVersion] != coreIngestSpace {
			t.Fatalf("%s search must find routed inline/PDF/fallback Versions through their owners; got %v, want %s/%s/%s", mode, found, inline, pdfVersion, fallbackVersion)
		}
	}
}
