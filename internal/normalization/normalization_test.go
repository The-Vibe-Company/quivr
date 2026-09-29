package normalization_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/normalization"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

const manifestYAML = `id: acme.markdown
version: 1.0.0
compatibility:
  engine: ">=0.1.0 <0.2.0"
  plugin_api: ">=0.1.0 <0.2.0"
contributions:
  normalizer:
    media_types: [text/markdown]
    timeout_ms: 2000
    limits:
      max_parts: 4
configuration:
  schema:
    type: object
    properties:
      max_sections: {type: integer}
`

var input = []byte("# Title\n\nBody")

// fakePlugin serves Plugin Protocol v0 from a test HTTP server.
type fakePlugin struct {
	mu       sync.Mutex
	digest   string
	calls    int
	requests []map[string]any
	answer   func(n int) (int, any)
}

func (f *fakePlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v0/discovery":
		f.mu.Lock()
		digest := f.digest
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": "0.1.0", "plugin": map[string]any{"id": "acme.markdown", "version": "1.0.0"}, "manifest_digest": digest, "contributions": []string{"normalizer"}})
	case "/v0/contributions/normalizer":
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.calls++
		n := f.calls
		f.requests = append(f.requests, req)
		f.mu.Unlock()
		status, answer := f.answer(n)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(answer)
	default:
		http.NotFound(w, r)
	}
}

func textParts(texts ...string) map[string]any {
	parts := []any{}
	for i, text := range texts {
		parts = append(parts, map[string]any{"key": "section-" + string(rune('1'+i)), "role": "section", "content": map[string]any{"kind": "text", "text": text}})
	}
	return map[string]any{"manifest": map[string]any{"kind": "manifest", "parts": parts}}
}

type repository struct {
	content.Repository
	work     content.Work
	progress []string
}

func (r *repository) Work(context.Context, string, string) (content.Work, bool, error) {
	return r.work, false, nil
}
func (r *repository) Progress(_ context.Context, _, _, state, code string) error {
	r.progress = append(r.progress, state+":"+code)
	return nil
}

type blobSource struct{ blob content.VerifiedBlob }

func (b blobSource) VerifiedBlob(context.Context, string, string) (content.VerifiedBlob, error) {
	return b.blob, nil
}

type memoryBlobs struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (m *memoryBlobs) Put(_ context.Context, org string, data []byte) (content.Blob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := content.Blob{Key: org + "/" + content.Hash(data), SHA256: content.Hash(data), Size: int64(len(data))}
	m.objects[b.Key] = data
	return b, nil
}
func (m *memoryBlobs) Read(_ context.Context, b content.Blob) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.objects[b.Key], nil
}

type memoryStore struct {
	mu    sync.Mutex
	saved map[string]content.Normalized
	saves int
}

func (s *memoryStore) Normalized(_ context.Context, _, versionID string) (content.Normalized, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.saved[versionID]
	return n, ok, nil
}
func (s *memoryStore) SaveNormalized(_ context.Context, _, versionID string, n content.Normalized) (content.Normalized, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	if existing, ok := s.saved[versionID]; ok {
		return existing, nil
	}
	s.saved[versionID] = n
	return n, nil
}

type signer struct{ keys []string }

func (s *signer) PresignGet(_ context.Context, key string, ttl time.Duration) (string, time.Time, error) {
	s.keys = append(s.keys, key)
	return "https://objects.test/" + key + "?signature=x", time.Now().Add(ttl), nil
}

type fixture struct {
	plugin  *fakePlugin
	repo    *repository
	store   *memoryStore
	blobs   *memoryBlobs
	signer  *signer
	service normalization.Service
	pin     *plugins.Pin
}

func setup(t *testing.T, answer func(n int) (int, any)) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), plugins.ManifestFile)
	if err := os.WriteFile(path, []byte(manifestYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fixture{plugin: &fakePlugin{answer: answer}, store: &memoryStore{saved: map[string]content.Normalized{}}, blobs: &memoryBlobs{objects: map[string][]byte{}}, signer: &signer{}}
	server := httptest.NewServer(f.plugin)
	t.Cleanup(server.Close)
	pin, err := plugins.LoadPin(plugins.PinConfig{Manifest: path, Endpoint: server.URL, Configuration: json.RawMessage(`{"max_sections": 3}`), Routes: []plugins.RouteConfig{{MediaType: "text/markdown"}}})
	if err != nil {
		t.Fatal(err)
	}
	f.pin = pin
	f.plugin.digest = pin.ManifestDigest
	blob := content.VerifiedBlob{ID: "blob_md", MediaType: "text/markdown", Blob: content.Blob{Key: "org/blob", SHA256: content.Hash(input), Size: int64(len(input))}}
	command := content.Command{Key: "k", Source: content.Source{CorpusID: "corpus_1", Namespace: "docs", RecordKey: "guide"}, Content: content.Text{Kind: "blob", BlobID: "blob_md", MediaType: "text/markdown", BlobSHA256: blob.Blob.SHA256}, Provenance: map[string]any{"source_blob_ids": []any{"blob_md"}, "producer": "client"}}
	f.repo = &repository{work: content.Work{Organization: "org_a", ReceiptID: "receipt_1", RecordID: "record_1", VersionID: "version_1", Command: command}}
	f.service = normalization.Service{Content: content.Service{Repository: f.repo, Blobs: f.blobs, BlobSource: blobSource{blob}}, Store: f.store, Signer: f.signer, Plugin: pluginhttp.Client{Pin: pin}, Pin: pin}
	return f
}

func TestNormalizeRecordsTheValidatedOutputOnce(t *testing.T) {
	f := setup(t, func(int) (int, any) { return 200, textParts("Title", "Body") })
	ctx := context.Background()
	if err := f.service.Normalize(ctx, "org_a", "receipt_1"); err != nil {
		t.Fatal(err)
	}
	stored, ok := f.store.saved["version_1"]
	if !ok {
		t.Fatal("no normalized output recorded")
	}
	wantKey := normalization.IdempotencyKey("startup:acme.markdown@1.0.0#"+f.pin.ManifestDigest, "normalizer", "org_a", "version_1", content.Hash(input))
	p := stored.Provenance
	if p.PluginID != "acme.markdown" || p.PluginVersion != "1.0.0" || p.PluginAPI != plugins.PluginAPIVersion || p.Contribution != "normalizer" || p.IdempotencyKey != wantKey || p.InputSHA256 != content.Hash(input) || !strings.HasPrefix(p.InvocationID, "inv_") {
		t.Fatalf("provenance %+v", p)
	}
	var m content.Manifest
	if err := json.Unmarshal(f.blobs.objects[stored.Manifest.Key], &m); err != nil || len(m.Parts) != 2 || m.Parts[1].Content.Text != "Body" {
		t.Fatalf("stored manifest %+v %v", m, err)
	}
	// The request carries a signed reference and the invocation context, never the body.
	req := f.plugin.requests[0]
	in := req["input"].(map[string]any)
	ref := in["reference"].(map[string]any)
	if ref["kind"] != "signed_url" || !strings.HasPrefix(ref["url"].(string), "https://objects.test/org/blob") || ref["expires_at"] == nil {
		t.Fatalf("reference %+v", ref)
	}
	if in["sha256"] != content.Hash(input) || in["blob_id"] != "blob_md" || req["idempotency_key"] != wantKey || req["invocation_id"] != p.InvocationID || req["record_version_id"] != "version_1" {
		t.Fatalf("request %+v", req)
	}
	if req["configuration"].(map[string]any)["max_sections"] != float64(3) || req["provenance"].(map[string]any)["producer"] != "client" {
		t.Fatalf("request %+v", req)
	}
	raw, _ := json.Marshal(req)
	if strings.Contains(string(raw), "Body") {
		t.Fatal("the request inlines the input body")
	}

	// A re-run converges without calling the plugin again.
	if err := f.service.Normalize(ctx, "org_a", "receipt_1"); err != nil {
		t.Fatal(err)
	}
	if f.plugin.calls != 1 {
		t.Fatalf("re-run invoked the plugin again: %d calls", f.plugin.calls)
	}
}

func TestConcurrentRunsConvergeOnOneOutput(t *testing.T) {
	f := setup(t, func(int) (int, any) { return 200, textParts("Title", "Body") })
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- f.service.Normalize(context.Background(), "org_a", "receipt_1")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(f.store.saved) != 1 {
		t.Fatalf("outputs %d", len(f.store.saved))
	}
}

func TestDivergentOutputForTheSameKeyKeepsTheRecordedManifest(t *testing.T) {
	f := setup(t, func(n int) (int, any) { return 200, textParts("Title", "Body "+string(rune('0'+n))) })
	// Simulate a lost first attempt: the recorded output came from an earlier call.
	first := f.service
	if err := first.Normalize(context.Background(), "org_a", "receipt_1"); err != nil {
		t.Fatal(err)
	}
	recorded := f.store.saved["version_1"]
	f.store.saved = map[string]content.Normalized{}
	f.store.saved["version_1"] = recorded
	store := &conflictStore{memoryStore: f.store}
	f.service.Store = store
	// The racing attempt converges on the recorded output so the Version publishes it.
	if err := f.service.Normalize(context.Background(), "org_a", "receipt_1"); err != nil {
		t.Fatalf("got %v", err)
	}
	if f.plugin.calls != 2 {
		t.Fatalf("calls %d", f.plugin.calls)
	}
	if f.store.saved["version_1"].Manifest != recorded.Manifest {
		t.Fatal("conflicting output overwrote the recorded Manifest")
	}
}

// conflictStore hides the recorded output from the pre-check, like an attempt
// racing with another one, so the save path sees the divergence.
type conflictStore struct{ *memoryStore }

func (c *conflictStore) Normalized(context.Context, string, string) (content.Normalized, bool, error) {
	return content.Normalized{}, false, nil
}

func TestNormalizationFailuresAreClassified(t *testing.T) {
	for name, tc := range map[string]struct {
		answer   func(int) (int, any)
		digest   string
		terminal string
	}{
		"discovery digest mismatch": {func(int) (int, any) { return 200, textParts("x") }, "sha256:" + strings.Repeat("0", 64), ""},
		"retryable plugin error": {func(int) (int, any) {
			return 503, map[string]any{"code": "busy", "message": "later", "retryable": true}
		}, "", ""},
		"unavailable without envelope": {func(int) (int, any) { return 502, "bad gateway" }, "", ""},
		"terminal plugin error": {func(int) (int, any) {
			return 422, map[string]any{"code": "bad_input", "message": "no", "retryable": false}
		}, "", "normalizer_failed"},
		"schema-invalid output": {func(int) (int, any) {
			return 200, map[string]any{"manifest": map[string]any{"kind": "manifest", "parts": []any{}}}
		}, "", "normalizer_invalid_output"},
		"foreign blob part": {func(int) (int, any) {
			return 200, map[string]any{"manifest": map[string]any{"kind": "manifest", "parts": []any{map[string]any{"key": "b", "role": "source", "content": map[string]any{"kind": "blob", "blob_id": "blob_other", "media_type": "text/markdown"}}}}}
		}, "", "normalizer_invalid_output"},
		"too many parts": {func(int) (int, any) { return 200, textParts("a", "b", "c", "d", "e") }, "", "normalizer_invalid_output"},
		"version extensions": {func(int) (int, any) {
			r := textParts("a")
			r["extensions"] = map[string]any{"acme.markdown": map[string]any{"schema_version": "1", "data": map[string]any{}}}
			return 200, r
		}, "", "normalizer_invalid_output"},
		"oversized response": {func(int) (int, any) { return 200, textParts(strings.Repeat("x", normalization.MaxManifestBytes+10)) }, "", "normalizer_invalid_output"},
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t, tc.answer)
			if tc.digest != "" {
				f.plugin.digest = tc.digest
			}
			err := f.service.Normalize(context.Background(), "org_a", "receipt_1")
			if err == nil {
				t.Fatal("failure not reported")
			}
			var terminal *normalization.TerminalError
			isTerminal := errors.As(err, &terminal)
			if tc.terminal == "" && isTerminal {
				t.Fatalf("retryable failure reported terminal: %v", err)
			}
			if tc.terminal != "" && (!isTerminal || terminal.Code != tc.terminal) {
				t.Fatalf("got %v, want terminal %s", err, tc.terminal)
			}
			if len(f.store.saved) != 0 {
				t.Fatal("a failed invocation recorded output")
			}
		})
	}
}

func TestNonBlobContentIsNotNormalized(t *testing.T) {
	f := setup(t, func(int) (int, any) { return 200, textParts("x") })
	f.repo.work.Command.Content = content.Text{Kind: "text", Text: "inline"}
	if err := f.service.Normalize(context.Background(), "org_a", "receipt_1"); err != nil || f.plugin.calls != 0 {
		t.Fatalf("err %v calls %d", err, f.plugin.calls)
	}
}

// flakySource verifies the input once, then reports an outage.
type flakySource struct {
	blob  content.VerifiedBlob
	calls int
}

func (f *flakySource) VerifiedBlob(context.Context, string, string) (content.VerifiedBlob, error) {
	f.calls++
	if f.calls > 1 {
		return content.VerifiedBlob{}, errors.New("database unavailable")
	}
	return f.blob, nil
}

func TestBlobPartVerificationOutageRetries(t *testing.T) {
	f := setup(t, func(int) (int, any) {
		return 200, map[string]any{"manifest": map[string]any{"kind": "manifest", "parts": []any{
			map[string]any{"key": "source", "role": "source", "content": map[string]any{"kind": "blob", "blob_id": "blob_md", "media_type": "text/markdown"}},
			map[string]any{"key": "body", "role": "body", "content": map[string]any{"kind": "text", "text": "x"}},
		}}}
	})
	verified := f.service.Content.BlobSource.(blobSource).blob
	f.service.Content.BlobSource = &flakySource{blob: verified}
	err := f.service.Normalize(context.Background(), "org_a", "receipt_1")
	var terminal *normalization.TerminalError
	if err == nil || errors.As(err, &terminal) || len(f.store.saved) != 0 {
		t.Fatalf("a verification outage must retry: %v", err)
	}
	// Once verification is back, the input Blob Part is kept with its checksum.
	f.service.Content.BlobSource = blobSource{verified}
	if err := f.service.Normalize(context.Background(), "org_a", "receipt_1"); err != nil {
		t.Fatal(err)
	}
	var m content.Manifest
	_ = json.Unmarshal(f.blobs.objects[f.store.saved["version_1"].Manifest.Key], &m)
	if m.Parts[0].Content.BlobSHA256 != content.Hash(input) {
		t.Fatalf("stored %+v", m.Parts[0])
	}
}

var _ corpus.Scope
