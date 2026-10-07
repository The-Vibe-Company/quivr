package acceptance

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// HTTP acceptance owns reversible visibility, administrative permissions and audit.
func TestCorpusLifecycle(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	token := os.Getenv("QUIVR_TEST_ADMIN")
	run := fmt.Sprint(time.Now().UnixNano())
	c := request(t, "POST", "/v0/corpora", token, map[string]any{"name": "Disposable corpus", "idempotency_key": "lifecycle-" + run}, 201)["corpus_id"].(string)
	path := "/v0/corpora/" + c
	text := "Lifecycle lantern " + run
	blobID := uploadBlob(t, token, []byte("Source "+run), "application/xml")
	cursor := request(t, "GET", changesPath(c, "", 0), token, nil, 200)["next_cursor"].(string)
	cmd := manifestCommandBody(c, "lifecycle-"+run, "example", "document", []any{manifestPart("body", "body", "text", text), manifestPart("source", "source", "blob", blobID)}, nil, nil)
	cmd["provenance"] = map[string]any{"source_blob_ids": []string{blobID}}
	accepted := request(t, "POST", "/v0/records", token, cmd, 202)
	receipt := awaitRetrievalReady(t, accepted["receipt_id"].(string))
	record := receipt["record_id"].(string)
	version := receipt["version_id"].(string)
	awaitEnriched(t, token, c, cursor, record)
	query := map[string]any{"query": run, "mode": "lexical", "corpus_ids": []string{c}}
	if got := request(t, "POST", "/v0/search", token, query, 200); len(got["items"].([]any)) != 1 {
		t.Fatal(got)
	}
	// A write-only key cannot acquire any of the new administration permissions.
	writer := os.Getenv("QUIVR_TEST_CONFIGURER")
	request(t, "POST", path+"/archive", writer, nil, 403)
	request(t, "POST", path+"/unarchive", writer, nil, 403)
	request(t, "PATCH", path, writer, map[string]any{"name": "Denied"}, 403)
	if got := request(t, "PATCH", path, token, map[string]any{"name": "Renamed corpus"}, 200); got["name"] != "Renamed corpus" {
		t.Fatal(got)
	}
	request(t, "PATCH", path, token, map[string]any{"name": "   "}, 422)
	request(t, "PATCH", path, token, map[string]any{"name": "invalid\x00name"}, 422)
	request(t, "POST", path+"/archive", token, nil, 200)
	contains := func(page map[string]any, key, value string) bool {
		for _, item := range page["items"].([]any) {
			if item.(map[string]any)[key] == value {
				return true
			}
		}
		return false
	}
	if contains(request(t, "GET", "/v0/corpora?limit=100", token, nil, 200), "corpus_id", c) {
		t.Fatal("archived corpus listed")
	}
	if !contains(request(t, "GET", "/v0/corpora?include_archived=true&limit=100", token, nil, 200), "corpus_id", c) {
		t.Fatal("administrative listing lost archived corpus")
	}
	if got := request(t, "POST", "/v0/search", token, query, 409); got["code"] != "corpus_archived" {
		t.Fatal(got)
	}

	request(t, "GET", "/v0/records?corpus_id="+c, token, nil, 409)
	request(t, "GET", changesPath(c, cursor, 100), token, nil, 409)
	if contains(request(t, "GET", "/v0/admin/documents?limit=100", token, nil, 200), "version_id", version) {
		t.Fatal("archived document in explorer")
	}
	request(t, "GET", "/v0/admin/documents/"+version+"/timeline", token, nil, 404)
	request(t, "GET", "/v0/records/"+record, token, nil, 404)
	request(t, "GET", "/v0/records/"+record+"/versions/"+version, token, nil, 404)
	// Archive is a visibility change: canonical processing still accepts and
	// enriches retained work. Restoring must expose this document too.
	hidden := manifestCommandBody(c, "lifecycle-hidden-"+run, "example", "hidden", []any{manifestPart("body", "body", "text", text+" hidden")}, nil, nil)
	hiddenReceipt := request(t, "POST", "/v0/records", token, hidden, 202)
	hiddenReady := awaitRetrievalReady(t, hiddenReceipt["receipt_id"].(string))
	deadline := time.Now().Add(monitoringWait)
	for hiddenReady["processing"].(map[string]any)["state"] != "idle" {
		if time.Now().After(deadline) {
			t.Fatalf("retained processing stalled while archived: %v", hiddenReady)
		}
		time.Sleep(200 * time.Millisecond)
		hiddenReady = request(t, "GET", "/v0/ingestion-receipts/"+hiddenReceipt["receipt_id"].(string), token, nil, 200)
	}
	hiddenRecord := hiddenReady["record_id"].(string)
	request(t, "POST", path+"/unarchive", token, nil, 200)
	if got := request(t, "GET", "/v0/records/"+hiddenRecord+"/versions/"+hiddenReady["version_id"].(string), token, nil, 200); got["steps"].(map[string]any)["enriched_at"] == nil {
		t.Fatal("retained work finished without enrichment", got)
	}
	awaitEnriched(t, token, c, cursor, hiddenRecord)
	request(t, "GET", "/v0/admin/documents/"+version+"/timeline", token, nil, 200)
	request(t, "GET", "/v0/records/"+record, token, nil, 200)
	request(t, "GET", "/v0/records/"+record+"/versions/"+version, token, nil, 200)
	if !contains(request(t, "GET", "/v0/corpora?limit=100", token, nil, 200), "corpus_id", c) {
		t.Fatal("restored corpus hidden")
	}
	if got := request(t, "POST", "/v0/search", token, query, 200); len(got["items"].([]any)) != 2 {
		t.Fatal("restored search", got)
	}
	if got := request(t, "GET", "/v0/records?corpus_id="+c, token, nil, 200); len(got["items"].([]any)) != 2 {
		t.Fatal("restored catalog", got)
	}
	if got := request(t, "GET", changesPath(c, cursor, 100), token, nil, 200); len(got["items"].([]any)) == 0 {
		t.Fatal("restored feed", got)
	}
	audit := request(t, "GET", "/v0/admin/audit?limit=100", token, nil, 200)
	actions := map[string]bool{}
	for _, raw := range audit["items"].([]any) {
		item := raw.(map[string]any)
		if item["outcome"] == "accepted" && strings.Contains(fmt.Sprint(item), c) {
			actions[fmt.Sprint(item["action"])] = true
		}
	}
	for _, action := range []string{"corpus.archive", "corpus.unarchive", "corpus.rename"} {
		if !actions[action] {
			t.Fatalf("missing audit %s: %v", action, audit)
		}
	}
}
