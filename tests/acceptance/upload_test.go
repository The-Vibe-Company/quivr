package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func rawTransfer(t *testing.T, target string, headers map[string]string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest("PUT", target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		if strings.EqualFold(name, "content-length") || strings.EqualFold(name, "host") {
			continue
		}
		req.Header.Set(name, value)
	}
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

func uploadRequest(text string) (map[string]any, string, int) {
	sum := sha256.Sum256([]byte(text))
	return map[string]any{"size_bytes": len([]byte(text)), "sha256": hex.EncodeToString(sum[:]), "media_type": "text/plain"}, hex.EncodeToString(sum[:]), len([]byte(text))
}

func uploadHeaders(raw map[string]any) map[string]string {
	headers := map[string]string{}
	if values, ok := raw["upload_headers"].(map[string]any); ok {
		for name, value := range values {
			headers[name] = value.(string)
		}
	}
	return headers
}

func TestUploadedBlobIngestion(t *testing.T) {
	if os.Getenv("QUIVR_TEST_URL") == "" {
		t.Skip("make verify")
	}
	admin := os.Getenv("QUIVR_TEST_ADMIN")
	corpusID := ingestionCorpus(t)
	text := "Dépêche uploadée 📰\nDeuxième ligne préservée."
	body, sha, size := uploadRequest(text)

	created := request(t, "POST", "/v0/uploads", admin, body, 201)
	if created["state"] != "awaiting_upload" || created["upload_url"] == nil || created["upload_method"] != "PUT" {
		t.Fatal(created)
	}
	uploadID := created["upload_id"].(string)
	if replay := request(t, "POST", "/v0/uploads", admin, body, 201); replay["upload_id"] != uploadID {
		t.Fatal("upload replay did not converge")
	}
	// A transfer capability without its signed headers is not sufficient.
	if status := rawTransfer(t, created["upload_url"].(string), nil, []byte(text)); status < 400 {
		t.Fatalf("unsigned transfer accepted: %d", status)
	}
	if status := rawTransfer(t, created["upload_url"].(string), uploadHeaders(created), []byte(text)); status/100 != 2 {
		t.Fatalf("signed transfer refused: %d", status)
	}

	confirmed := request(t, "POST", "/v0/uploads/"+uploadID+"/confirm", admin, nil, 202)
	if confirmed["state"] != "verified" || confirmed["blob_id"] == nil {
		t.Fatal(confirmed)
	}
	blobID := confirmed["blob_id"].(string)
	blob := request(t, "GET", "/v0/blobs/"+blobID, admin, nil, 200)
	if blob["sha256"] != sha || int(blob["size_bytes"].(float64)) != size || blob["media_type"] != "text/plain" {
		t.Fatal(blob)
	}
	if repeated := request(t, "POST", "/v0/uploads/"+uploadID+"/confirm", admin, nil, 202); repeated["blob_id"] != blobID {
		t.Fatal("repeated confirmation created a second Blob")
	}

	// Possession of an ID is not access, and Blob actions are scoped.
	request(t, "GET", "/v0/blobs/"+blobID, os.Getenv("QUIVR_TEST_OTHER"), nil, 404)
	request(t, "POST", "/v0/uploads", os.Getenv("QUIVR_TEST_READER"), body, 403)
	request(t, "GET", "/v0/blobs/"+blobID, os.Getenv("QUIVR_TEST_DENIED"), nil, 403)

	command := map[string]any{
		"idempotency_key": "uploaded-1",
		"source":          map[string]any{"corpus_id": corpusID, "namespace": "uploads", "record_key": "uploaded-article"},
		"content":         map[string]any{"kind": "blob", "blob_id": blobID, "media_type": "text/plain"},
	}
	accepted := request(t, "POST", "/v0/records", admin, command, 202)
	resolved := awaitReceipt(t, accepted["receipt_id"].(string))
	if resolved["outcome"] != "created" {
		t.Fatal(resolved)
	}
	version := request(t, "GET", "/v0/records/"+resolved["record_id"].(string)+"/versions/"+resolved["version_id"].(string), admin, nil, 200)
	part := version["manifest"].(map[string]any)["parts"].([]any)[0].(map[string]any)
	if part["content"].(map[string]any)["text"] != text {
		t.Fatal("uploaded bytes were not preserved", version)
	}
	if version["provenance"].(map[string]any)["source_blob_ids"].([]any)[0] != blobID {
		t.Fatal("verified Blob provenance missing", version)
	}

	// Unverified and cross-Organization references are rejected without a Receipt.
	unverified := map[string]any{
		"idempotency_key": "uploaded-unverified",
		"source":          map[string]any{"corpus_id": corpusID, "namespace": "uploads", "record_key": "unverified"},
		"content":         map[string]any{"kind": "blob", "blob_id": "blob_not_verified", "media_type": "text/plain"},
	}
	request(t, "POST", "/v0/records", admin, unverified, 422)

	// Confirming before the transfer arrives stays retryable, then converges once
	// the bytes are present: verification never latches a transient failure.
	pendingText := "Transfert retardé 🌙"
	pendingBody, _, _ := uploadRequest(pendingText)
	pending := request(t, "POST", "/v0/uploads", admin, pendingBody, 201)
	pendingID := pending["upload_id"].(string)
	retryable := request(t, "POST", "/v0/uploads/"+pendingID+"/confirm", admin, nil, 202)
	if retryable["state"] == "verified" || retryable["error"] == nil {
		t.Fatal(retryable)
	}
	pendingHeaders := uploadHeaders(pending)
	refreshed := request(t, "GET", "/v0/uploads/"+pendingID, admin, nil, 200)
	if refreshed["state"] != "awaiting_upload" || refreshed["upload_url"] == nil {
		t.Fatal("retryable session stopped offering a transfer capability", refreshed)
	}
	if status := rawTransfer(t, refreshed["upload_url"].(string), pendingHeaders, []byte(pendingText)); status/100 != 2 {
		t.Fatalf("retry transfer refused: %d", status)
	}
	recovered := request(t, "POST", "/v0/uploads/"+pendingID+"/confirm", admin, nil, 202)
	if recovered["state"] != "verified" || recovered["blob_id"] == nil {
		t.Fatal("retry after transient failure did not reconcile", recovered)
	}
}
