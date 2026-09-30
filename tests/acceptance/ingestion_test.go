package acceptance

import (
	"os"
	"testing"
	"time"
)

func ingestionCorpus(t *testing.T) string {
	t.Helper()
	c := request(t, "POST", "/v0/corpora", os.Getenv("QUIVR_TEST_ADMIN"), map[string]any{"name": "Ingestion", "idempotency_key": "ingestion-corpus"}, 201)
	return c["corpus_id"].(string)
}
func inlineCommand(corpus, key, record, text string) map[string]any {
	return map[string]any{"idempotency_key": key, "source": map[string]any{"corpus_id": corpus, "namespace": "example-feed", "record_key": record}, "content": map[string]any{"kind": "text", "text": text}}
}
func awaitReceipt(t *testing.T, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		r := request(t, "GET", "/v0/ingestion-receipts/"+id, os.Getenv("QUIVR_TEST_ADMIN"), nil, 200)
		if r["state"] == "resolved" {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("Receipt never resolved: %v", r)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
func TestInlineMaterialization(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := ingestionCorpus(t)
	cmd := inlineCommand(c, "inline-1", "article-1", "Bonjour 🌞\nTexte intégral.")
	accepted := request(t, "POST", "/v0/records", admin, cmd, 202)
	id := accepted["receipt_id"].(string)
	replay := request(t, "POST", "/v0/records", admin, cmd, 202)
	if replay["receipt_id"] != id {
		t.Fatal("replay changed Receipt")
	}
	resolved := awaitReceipt(t, id)
	if resolved["outcome"] != "created" {
		t.Fatal(resolved)
	}
	recordID := resolved["record_id"].(string)
	versionID := resolved["version_id"].(string)
	v := request(t, "GET", "/v0/records/"+recordID+"/versions/"+versionID, admin, nil, 200)
	part := v["manifest"].(map[string]any)["parts"].([]any)[0].(map[string]any)
	if part["content"].(map[string]any)["text"] != "Bonjour 🌞\nTexte intégral." {
		t.Fatal("canonical content changed", v)
	}
	availability := v["availability"].(map[string]any)
	if availability["searchable"] == true && (availability["state"] != "retrieval_ready" || availability["is_current"] != true) {
		t.Fatal("inconsistent availability", v)
	}
	// The read says when the revision was accepted (the adapter test owns its value).
	if at, _ := v["accepted_at"].(string); at == "" {
		t.Fatal("Version read has no accepted_at", v)
	} else if _, err := time.Parse(time.RFC3339Nano, at); err != nil {
		t.Fatal("accepted_at is not a timestamp", at)
	}
}

func TestInlineIdentityAndAuthorization(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := ingestionCorpus(t)
	command := inlineCommand(c, "revision-first", "revised-article", "Original immutable text")
	command["source_revision"] = "revision-1"
	command["source_position"] = "000000000000000000000000000000000000000000003"
	a := request(t, "POST", "/v0/records", admin, command, 202)
	first := awaitReceipt(t, a["receipt_id"].(string))
	command["source_position"] = "3"
	request(t, "POST", "/v0/records", admin, command, 202)
	command["idempotency_key"] = "revision-duplicate"
	b := request(t, "POST", "/v0/records", admin, command, 202)
	duplicate := awaitReceipt(t, b["receipt_id"].(string))
	if duplicate["outcome"] != "duplicate" || duplicate["version_id"] != first["version_id"] {
		t.Fatal("revision replay did not converge", duplicate)
	}
	command["content"] = map[string]any{"kind": "text", "text": "Conflicting text"}
	request(t, "POST", "/v0/records", admin, command, 409)
	command["idempotency_key"] = "revision-conflict"
	conflict := request(t, "POST", "/v0/records", admin, command, 202)
	conflict = awaitReceipt(t, conflict["receipt_id"].(string))
	if conflict["outcome"] != "conflict" {
		t.Fatal(conflict)
	}
	if _, ok := conflict["version_id"]; ok {
		t.Fatal("conflict exposed a new Version")
	}
	command["idempotency_key"] = "revision-second"
	command["source_revision"] = "revision-2"
	command["source_position"] = "4"
	second := request(t, "POST", "/v0/records", admin, command, 202)
	second = awaitReceipt(t, second["receipt_id"].(string))
	if second["version_id"] == first["version_id"] || second["record_id"] != first["record_id"] {
		t.Fatal("source history collapsed")
	}
	path := "/v0/records/" + first["record_id"].(string) + "/versions/" + first["version_id"].(string)
	historical := request(t, "GET", path, admin, nil, 200)
	if historical["manifest"].(map[string]any)["parts"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"] != "Original immutable text" {
		t.Fatal("history overwritten")
	}
	for _, key := range []string{"OTHER", "SCOPED"} {
		token := os.Getenv("QUIVR_TEST_" + key)
		request(t, "GET", path, token, nil, 404)
		request(t, "GET", "/v0/ingestion-receipts/"+first["receipt_id"].(string), token, nil, 404)
		request(t, "POST", "/v0/records", token, inlineCommand(c, "unauthorized", "x", "Forbidden"), 404)
	}
	request(t, "POST", "/v0/records", os.Getenv("QUIVR_TEST_READER"), command, 403)
	writer := os.Getenv("QUIVR_TEST_WRITER")
	writeResult := request(t, "POST", "/v0/records", writer, inlineCommand(c, "write-only", "write-only", "Write-only input"), 202)
	if _, ok := writeResult["record_id"]; ok {
		t.Fatal("write-only acceptance leaked linked read view")
	}
	request(t, "GET", "/v0/ingestion-receipts/"+writeResult["receipt_id"].(string), writer, nil, 403)
	digestCmd := inlineCommand(c, "digest-first", "digest-article", "No revision")
	d := request(t, "POST", "/v0/records", admin, digestCmd, 202)
	d = awaitReceipt(t, d["receipt_id"].(string))
	digestCmd["idempotency_key"] = "digest-duplicate"
	e := request(t, "POST", "/v0/records", admin, digestCmd, 202)
	e = awaitReceipt(t, e["receipt_id"].(string))
	if e["outcome"] != "duplicate" || e["version_id"] != d["version_id"] {
		t.Fatal("digest identity did not converge")
	}
}
func TestInlineProducerDoesNotChangeManifestIdentity(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	corpusID := ingestionCorpus(t)
	for _, revision := range []string{"", "external-v1"} {
		cmd := inlineCommand(corpusID, "producer-first"+revision, "producer-record"+revision, "Stable source content")
		if revision != "" {
			cmd["source_revision"] = revision
		}
		cmd["provenance"] = map[string]any{"producer": "normalizer", "producer_version": "1"}
		first := request(t, "POST", "/v0/records", admin, cmd, 202)
		first = awaitReceipt(t, first["receipt_id"].(string))
		cmd["idempotency_key"] = "producer-second" + revision
		cmd["provenance"] = map[string]any{"producer": "normalizer", "producer_version": "2"}
		second := request(t, "POST", "/v0/records", admin, cmd, 202)
		second = awaitReceipt(t, second["receipt_id"].(string))
		if second["outcome"] != "duplicate" || second["version_id"] != first["version_id"] {
			t.Fatal("producer version changed Manifest identity", second)
		}
		version := request(t, "GET", "/v0/records/"+first["record_id"].(string)+"/versions/"+first["version_id"].(string), admin, nil, 200)
		if version["provenance"].(map[string]any)["producer_version"] != "1" {
			t.Fatal("duplicate overwrote first publication provenance")
		}
	}
}

func TestInlineRejections(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	c := ingestionCorpus(t)
	unverified := inlineCommand(c, "unsupported-blob", "x", "x")
	unverified["content"] = map[string]any{"kind": "blob", "blob_id": "not-verified", "media_type": "text/plain"}
	request(t, "POST", "/v0/records", admin, unverified, 422)
	cmd := inlineCommand(c, "unknown", "x", "x")
	cmd["unknown"] = true
	request(t, "POST", "/v0/records", admin, cmd, 422)
	delete(cmd, "unknown")
	cmd["extensions"] = map[string]any{"uninstalled": map[string]any{"schema_version": "1", "data": map[string]any{}}}
	request(t, "POST", "/v0/records", admin, cmd, 422)
}
