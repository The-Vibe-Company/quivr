package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The local harness pins the reference plugin plugins/pdf-text for
// application/pdf (scripts/normalizer_plugin.py). These scenarios read its
// input fixture as plain bytes and observe it through the public API only.

// pdfFixture is the three-page sample the plugin's own tests certify: page 1
// and page 2 carry text, page 3 has none.
func pdfFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "plugins", "pdf-text", "fixtures", "sample.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func pdfCommand(corpusID, key, blobID, run string) map[string]any {
	return map[string]any{
		"idempotency_key": "pdf-" + key + "-" + run,
		"source":          map[string]any{"corpus_id": corpusID, "namespace": "documents", "record_key": key},
		"content":         map[string]any{"kind": "blob", "blob_id": blobID, "media_type": "application/pdf"},
		"provenance":      map[string]any{"producer": "acceptance-client"},
	}
}

// TestPDFIsSearchableByPageText uploads a multi-page PDF, ingests it as a Blob
// and finds a phrase of page 2 in that page's Part.
func TestPDFIsSearchableByPageText(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := time.Now().UTC().Format("20060102T150405.000000")
	corpusID := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "PDF documents", "idempotency_key": "pdf-" + run}, 201)["corpus_id"].(string)
	data := pdfFixture(t)
	sum := sha256.Sum256(data)
	blobID := uploadBlob(t, admin, data, "application/pdf")

	accepted := request(t, "POST", "/v0/records", admin, pdfCommand(corpusID, "observatory-notes", blobID, run), 202)
	ready := awaitRetrievalReady(t, accepted["receipt_id"].(string))
	if ready["outcome"] != "created" {
		t.Fatal(ready)
	}

	hits := lexicalHits(t, []string{corpusID}, "heliotrope aurora")
	hit := hits[ready["version_id"].(string)]
	if len(hits) != 1 || hit["record_id"] != ready["record_id"] || hit["version_id"] != ready["version_id"] || hit["part_key"] != "page-2" {
		t.Fatalf("hit %v is not page 2 of the ingested PDF", hit)
	}
	if excerpt, _ := hit["excerpt"].(map[string]any); !strings.Contains(excerpt["text"].(string), "heliotrope") {
		t.Fatalf("excerpt %v", hit["excerpt"])
	}

	version := request(t, "GET", "/v0/records/"+ready["record_id"].(string)+"/versions/"+ready["version_id"].(string), admin, nil, 200)
	parts := map[string]map[string]any{}
	for _, raw := range version["manifest"].(map[string]any)["parts"].([]any) {
		part := raw.(map[string]any)
		parts[part["key"].(string)] = part
	}
	if len(parts) != 3 || parts["page-1"]["role"] != "body" || parts["page-2"]["role"] != "body" || parts["page-3"] != nil {
		t.Fatalf("one body Part per page with text: %v", version["manifest"])
	}
	if text := parts["page-2"]["content"].(map[string]any)["text"].(string); !strings.Contains(text, "heliotrope aurora") || strings.Contains(parts["page-1"]["content"].(map[string]any)["text"].(string), "heliotrope") {
		t.Fatalf("page text is not split by page: %v", parts)
	}
	if source := parts["source"]["content"].(map[string]any); source["kind"] != "blob" || source["blob_id"] != blobID || source["media_type"] != "application/pdf" {
		t.Fatalf("source Part %v", parts["source"])
	}

	// The plugin's own extension namespace is published on the Version.
	document, _ := version["extensions"].(map[string]any)["pdf-text.document"].(map[string]any)
	if data, _ := document["data"].(map[string]any); document["schema_version"] != "1" || data["page_count"] != float64(3) || data["text_pages"] != float64(2) {
		t.Fatalf("pdf-text.document extension %v", version["extensions"])
	}

	provenance := version["provenance"].(map[string]any)
	normalization, _ := provenance["normalization"].(map[string]any)
	if normalization["plugin_id"] != "pdf-text" || normalization["plugin_version"] != "0.1.0" || normalization["contribution"] != "normalizer" || normalization["input_sha256"] != hex.EncodeToString(sum[:]) || normalization["invocation_id"] == nil {
		t.Fatalf("normalization provenance %v", provenance)
	}
	if provenance["producer"] != "acceptance-client" || provenance["source_blob_ids"].([]any)[0] != blobID {
		t.Fatalf("provenance %v", provenance)
	}
}

// TestPDFCorruptIsQuarantined ingests bytes no PDF reader can open: the
// plugin's terminal error quarantines the Version with a structured
// diagnostic naming pdf-text, publishes none of its output and announces
// record.quarantined, while later ingestion keeps working.
func TestPDFCorruptIsQuarantined(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	run := time.Now().UTC().Format("20060102T150405.000000")
	corpusID := request(t, "POST", "/v0/corpora", admin, map[string]any{"name": "Corrupt PDF", "idempotency_key": "pdf-corrupt-" + run}, 201)["corpus_id"].(string)
	_, cursor := drain(t, admin, corpusID, "", 0)
	blobID := uploadBlob(t, admin, []byte("%PDF-1.4\nthis file was cut short before its first object\n"), "application/pdf")

	receipt := awaitReceipt(t, request(t, "POST", "/v0/records", admin, pdfCommand(corpusID, "damaged", blobID, run), 202)["receipt_id"].(string))
	availability, _ := receipt["availability"].(map[string]any)
	diagnostics, _ := receipt["diagnostics"].([]any)
	if receipt["outcome"] != "created" || availability["state"] != "quarantined" || availability["searchable"] != false || len(diagnostics) != 1 || diagnostics[0].(map[string]any)["code"] != "normalizer_failed" {
		t.Fatalf("receipt %v", receipt)
	}
	version := request(t, "GET", "/v0/records/"+receipt["record_id"].(string)+"/versions/"+receipt["version_id"].(string), admin, nil, 200)
	ds, _ := version["diagnostics"].([]any)
	if len(ds) != 1 {
		t.Fatalf("version diagnostics %v", version)
	}
	d := ds[0].(map[string]any)
	if d["code"] != "normalizer_failed" || d["retryable"] != false || d["plugin"] != "pdf-text" || d["contribution"] != "normalizer" || !strings.HasPrefix(d["invocation_id"].(string), "inv_") || !strings.Contains(d["message"].(string), "corrupt_pdf") {
		t.Fatalf("diagnostic %v", d)
	}
	// Nothing from the plugin was published: only the submitted input Blob Part.
	parts := version["manifest"].(map[string]any)["parts"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["content"].(map[string]any)["blob_id"] != blobID || version["provenance"].(map[string]any)["normalization"] != nil {
		t.Fatalf("published %v", version)
	}
	if events, _ := drain(t, admin, corpusID, cursor, 0); typed(events, "record.quarantined", receipt["record_id"].(string)) != 1 {
		t.Fatalf("record.quarantined %v", events)
	}

	// Ingestion after the failure is unaffected.
	accepted := request(t, "POST", "/v0/records", admin, inlineCommand(corpusID, "pdf-after-"+run, "after-the-failure", "A lantern glowed in the quiet harbour."), 202)
	ready := awaitRetrievalReady(t, accepted["receipt_id"].(string))
	if hits := lexicalHits(t, []string{corpusID}, "lantern"); len(hits) != 1 || hits[ready["version_id"].(string)] == nil {
		t.Fatalf("search after the failure %v", hits)
	}
}
