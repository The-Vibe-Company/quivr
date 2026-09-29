package quivrplugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// mailSource holds attachment bytes by ref and counts how often it is read.
type mailSource struct {
	mu    sync.Mutex
	bytes map[string]string
	opens int
}

func (*mailSource) Fetch(context.Context, *FetchRequest) (*Page, error) { return &Page{}, nil }
func (*mailSource) CheckCredential(context.Context, *CredentialRequest) (*CredentialStatus, error) {
	return &CredentialStatus{}, nil
}

func (s *mailSource) OpenAttachment(_ context.Context, r *AttachmentRequest) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opens++
	return io.NopCloser(strings.NewReader(s.bytes[r.Attachment.Ref])), nil
}

func (s *mailSource) SkippedAttachment(_ context.Context, r *AttachmentRequest, reason string) (map[string]Extension, error) {
	return map[string]Extension{"sdk-mail": {SchemaVersion: "1", Data: map[string]any{"skipped": r.Attachment.Key + ":" + reason}}}, nil
}

// storage is a grant endpoint that stores what a PUT with the expected
// checksum header sends, and refuses anything else.
type storage struct {
	mu     sync.Mutex
	stored map[string]string
}

func (s *storage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	sum := sha256.Sum256(body)
	if r.Method != http.MethodPut || r.Header.Get("X-Checksum") != hex.EncodeToString(sum[:]) || r.ContentLength != int64(len(body)) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.stored[r.URL.Path] = string(body)
	s.mu.Unlock()
}

const grantSignature = "grant-signature-not-a-secret"

func mailPlugin(t *testing.T, src *mailSource) (http.Handler, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	p, err := New("testdata/attachments.yaml", WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.MustConnector("mail", src).Handler()
	if err != nil {
		t.Fatal(err)
	}
	return h, logs
}

func attachmentBody(ref, now string, grant map[string]any) []byte {
	body := map[string]any{
		"invocation_id": "inv-a", "contribution": "connector", "organization_id": "org-1", "configuration": map[string]any{},
		"connector":  map[string]any{"instance_id": "c-1", "kind": "mail", "config": map[string]any{}},
		"credential": map[string]any{"token": secret}, "now": now,
		"item":       map[string]any{"record_key": "<m1@example.org>", "revision": "r1"},
		"attachment": map[string]any{"key": "attachment-01", "role": "attachment", "media_type": "application/pdf", "ref": ref},
	}
	if grant != nil {
		body["grant"] = grant
	}
	raw, _ := json.Marshal(body)
	return raw
}

func grantFor(url, data string) map[string]any {
	sum := sha256.Sum256([]byte(data))
	digest := hex.EncodeToString(sum[:])
	return map[string]any{"url": url + "?signature=" + grantSignature, "method": "PUT", "headers": map[string]any{"X-Checksum": digest},
		"size_bytes": len(data), "sha256": digest, "media_type": "application/pdf", "expires_at": "2026-09-29T09:15:00Z"}
}

const describeRoute, uploadRoute = "/v0/contributions/connector/describe_attachment", "/v0/contributions/connector/upload_attachment"

func TestDescribedBytesAreUploadedToTheGrantWithoutReadingTheSourceAgain(t *testing.T) {
	src := &mailSource{bytes: map[string]string{"att:1": "%PDF-1.7 small"}}
	h, _ := mailPlugin(t, src)
	store := &storage{stored: map[string]string{}}
	server := httptest.NewServer(store)
	defer server.Close()
	status, out, raw := call(h, describeRoute, attachmentBody("att:1", "2026-09-29T09:00:00Z", nil))
	sum := sha256.Sum256([]byte("%PDF-1.7 small"))
	if status != 200 || out["size_bytes"] != 14.0 || out["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("describe %d %s", status, raw)
	}
	if err := validate("plugins/v0/connector-describe-attachment-response.schema.json", []byte(raw)); err != nil {
		t.Fatal(err)
	}
	status, _, raw = call(h, uploadRoute, attachmentBody("att:1", "2026-09-29T09:01:00Z", grantFor(server.URL+"/object-1", "%PDF-1.7 small")))
	if status != 200 || strings.TrimSpace(raw) != `{"status":"uploaded"}` {
		t.Fatalf("upload %d %s", status, raw)
	}
	if store.stored["/object-1"] != "%PDF-1.7 small" || src.opens != 1 {
		t.Fatalf("stored %q after %d reads of the source", store.stored["/object-1"], src.opens)
	}
}

func TestOversizedOrEmptyAttachmentsAreSkippedWithTheItemsExtensions(t *testing.T) {
	src := &mailSource{bytes: map[string]string{"att:big": strings.Repeat("x", 17), "att:empty": ""}}
	h, _ := mailPlugin(t, src)
	for ref, reason := range map[string]string{"att:big": SkipTooLarge, "att:empty": SkipEmpty} {
		status, out, raw := call(h, describeRoute, attachmentBody(ref, "2026-09-29T09:00:00Z", nil))
		ext, _ := out["item_extensions"].(map[string]any)
		if status != 200 || out["skip"] != reason || ext["sdk-mail"] == nil {
			t.Fatalf("%s: %d %s", ref, status, raw)
		}
	}
}

// Without its spool (a restart, or expiry by the core's clock) the upload
// reads the source again, and uploads only bytes that match the grant.
func TestAnUploadRereadsTheSourceAndRefusesChangedBytes(t *testing.T) {
	src := &mailSource{bytes: map[string]string{"att:1": "first bytes"}}
	h, _ := mailPlugin(t, src)
	store := &storage{stored: map[string]string{}}
	server := httptest.NewServer(store)
	defer server.Close()
	call(h, describeRoute, attachmentBody("att:1", "2026-09-29T09:00:00Z", nil))
	// 20 minutes later the spooled bytes have expired.
	status, _, raw := call(h, uploadRoute, attachmentBody("att:1", "2026-09-29T09:20:00Z", grantFor(server.URL+"/object-1", "first bytes")))
	if status != 200 || src.opens != 2 || store.stored["/object-1"] != "first bytes" {
		t.Fatalf("upload after expiry %d %s (opens %d)", status, raw, src.opens)
	}
	src.bytes["att:1"] = "edited bytes"
	status, out, raw := call(h, uploadRoute, attachmentBody("att:1", "2026-09-29T09:21:00Z", grantFor(server.URL+"/object-2", "first bytes")))
	if status != 422 || out["code"] != CodeAttachmentChanged || out["error_class"] != "source" || store.stored["/object-2"] != "" {
		t.Fatalf("changed bytes %d %s", status, raw)
	}
}

func TestAGrantNeverAppearsInAnswersOrLogs(t *testing.T) {
	src := &mailSource{bytes: map[string]string{"att:1": "bytes"}}
	h, logs := mailPlugin(t, src)
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer refusing.Close()
	grant := grantFor(refusing.URL+"/object", "bytes")
	status, out, raw := call(h, uploadRoute, attachmentBody("att:1", "2026-09-29T09:00:00Z", grant))
	if status != 422 || out["code"] != "upload_refused" {
		t.Fatalf("refused upload %d %s", status, raw)
	}
	for name, text := range map[string]string{"answer": raw, "logs": logs.String()} {
		if strings.Contains(text, grantSignature) || strings.Contains(text, secret) {
			t.Fatalf("the grant or the credential leaked in the %s: %s", name, text)
		}
	}
}

func TestAttachmentsNeedTheManifestBlock(t *testing.T) {
	h, _ := newTestPlugin(t, func(*FetchRequest) (*Page, error) {
		return &Page{Items: []Item{{RecordKey: "a", Content: NewManifest(TextPart("body", "body", "x")), Attachments: []Attachment{{Key: "p", Role: "attachment", MediaType: "image/png", Ref: "r"}}}}}, nil
	})
	status, out, raw := call(h, "/v0/contributions/connector/fetch", fetchBody(nil))
	if status != 500 || out["code"] != "invalid_response" {
		t.Fatalf("%d %s", status, raw)
	}
	if status, _, _ := call(h, describeRoute, attachmentBody("r", "2026-09-29T09:00:00Z", nil)); status != 404 && status != 405 {
		t.Fatalf("describe_attachment is served only when the manifest declares attachments: %d", status)
	}
}
