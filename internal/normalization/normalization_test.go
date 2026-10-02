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
	"unicode/utf8"

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
    timeout_ms: 1000
    retry:
      max_attempts: 2
    limits:
      max_parts: 4
configuration:
  schema:
    type: object
    properties:
      max_sections: {type: integer}
extensions:
  acme.markdown.outline:
    "1":
      type: object
      additionalProperties: false
      required: [heading_count]
      properties:
        heading_count: {type: integer, minimum: 0}
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
	mu sync.Mutex
	content.SubmissionStore
	content.MaterializationStore
	work     content.Work
	progress []string
}

func (r *repository) Work(context.Context, string, string) (content.Work, bool, error) {
	return r.work, false, nil
}
func (r *repository) Progress(_ context.Context, _, _, state, code string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	mu        sync.Mutex
	saved     map[string]content.Normalized
	saves     int
	attempts  map[string]int
	conflicts []content.NormalizationConflict
}

func (s *memoryStore) RecordConflict(_ context.Context, _, versionID string, c content.NormalizationConflict) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conflicts = append(s.conflicts, c)
	return nil
}
func (s *memoryStore) CountAttempt(_ context.Context, _, versionID, _, _ string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempts == nil {
		s.attempts = map[string]int{}
	}
	s.attempts[versionID]++
	return s.attempts[versionID], nil
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

// fixed routes every media type its pin routes, whatever the work.
type fixed struct{ pin *plugins.Pin }

func (f fixed) Normalizer(_ context.Context, mediaType string) (*plugins.Pin, plugins.RouteConfig, bool) {
	return f.pin.Normalizer(mediaType)
}

type signer struct {
	mu   sync.Mutex
	keys []string
}

func (s *signer) PresignGet(_ context.Context, key string, ttl time.Duration) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	f.service = normalization.Service{Content: content.Service{Submissions: f.repo, Materialization: f.repo, Blobs: f.blobs, BlobSource: blobSource{blob}}, Store: f.store, Signer: f.signer, Plugin: pluginhttp.Normalizer{}, Pin: fixed{pin}}
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
	if p.PluginID != "acme.markdown" || p.PluginVersion != "1.0.0" || p.PluginAPI != "0.1.0" || p.Contribution != "normalizer" || p.IdempotencyKey != wantKey || p.InputSHA256 != content.Hash(input) || !strings.HasPrefix(p.InvocationID, "inv_") {
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

func outline(version string, count any) map[string]any {
	return map[string]any{"acme.markdown.outline": map[string]any{"schema_version": version, "data": map[string]any{"heading_count": count}}}
}

// Extensions in the plugin's declared namespaces are validated and recorded:
// top-level ones beside the Manifest, Part ones inside it.
func TestNormalizerExtensionsAreRecorded(t *testing.T) {
	f := setup(t, func(int) (int, any) {
		r := textParts("Title", "Body")
		r["extensions"] = outline("1", 2)
		r["manifest"].(map[string]any)["parts"].([]any)[1].(map[string]any)["extensions"] = outline("1", 1)
		return 200, r
	})
	if err := f.service.Normalize(context.Background(), "org_a", "receipt_1"); err != nil {
		t.Fatal(err)
	}
	stored := f.store.saved["version_1"]
	if ext, ok := stored.Extensions["acme.markdown.outline"]; !ok || ext.SchemaVersion != "1" || ext.Data["heading_count"] != float64(2) {
		t.Fatalf("recorded extensions %+v", stored.Extensions)
	}
	var m content.Manifest
	if err := json.Unmarshal(f.blobs.objects[stored.Manifest.Key], &m); err != nil || m.Parts[1].Extensions["acme.markdown.outline"].Data["heading_count"] != float64(1) {
		t.Fatalf("Part extensions not kept in the Manifest: %+v %v", m, err)
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

func TestDivergentOutputForTheSameKeyIsARecordedConflict(t *testing.T) {
	f := setup(t, func(n int) (int, any) { return 200, textParts("Title", "Body "+string(rune('0'+n))) })
	// Simulate a lost first attempt: the recorded output came from an earlier call.
	first := f.service
	if err := first.Normalize(context.Background(), "org_a", "receipt_1"); err != nil {
		t.Fatal(err)
	}
	recorded := f.store.saved["version_1"]
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
	if len(f.store.conflicts) != 1 || f.store.conflicts[0].InvocationID == recorded.Provenance.InvocationID || f.store.conflicts[0].ManifestSHA256 == recorded.Manifest.SHA256 {
		t.Fatalf("conflict not recorded: %+v", f.store.conflicts)
	}
	// The same Manifest with other extensions is a divergent output too.
	e := setup(t, func(n int) (int, any) {
		r := textParts("Title", "Body")
		r["extensions"] = outline("1", n)
		return 200, r
	})
	if err := e.service.Normalize(context.Background(), "org_a", "receipt_1"); err != nil {
		t.Fatal(err)
	}
	e.service.Store = &conflictStore{memoryStore: e.store}
	if err := e.service.Normalize(context.Background(), "org_a", "receipt_1"); err != nil || len(e.store.conflicts) != 1 {
		t.Fatalf("divergent extensions: err %v conflicts %+v", err, e.store.conflicts)
	}
	if got := e.store.saved["version_1"].Extensions["acme.markdown.outline"].Data["heading_count"]; got != float64(1) {
		t.Fatalf("the recorded extensions were overwritten: %v", got)
	}
	// An identical output for the same key is no conflict.
	g := setup(t, func(int) (int, any) { return 200, textParts("Title", "Body") })
	if err := g.service.Normalize(context.Background(), "org_a", "receipt_1"); err != nil {
		t.Fatal(err)
	}
	g.service.Store = &conflictStore{memoryStore: g.store}
	if err := g.service.Normalize(context.Background(), "org_a", "receipt_1"); err != nil || len(g.store.conflicts) != 0 {
		t.Fatalf("err %v conflicts %+v", err, g.store.conflicts)
	}
}

// conflictStore hides the recorded output from the pre-check, like an attempt
// racing with another one, so the save path sees the divergence.
type conflictStore struct{ *memoryStore }

func (c *conflictStore) Normalized(context.Context, string, string) (content.Normalized, bool, error) {
	return content.Normalized{}, false, nil
}

var issueCodes = map[string]string{
	"undeclared namespace":          plugins.CodeUndeclaredNamespace,
	"undeclared schema version":     plugins.CodeUndeclaredSchemaVersion,
	"schema-invalid extension":      plugins.CodeInvalidExtension,
	"schema-invalid Part extension": plugins.CodeInvalidExtension,
}

// outageAnswer answers like an unavailable plugin: 502 without an envelope.
func outageAnswer(int) (int, any) { return 502, "bad gateway" }

// Unavailability retries without limit and never records an outcome.
func TestUnavailablePluginNeverQuarantines(t *testing.T) {
	for name, edit := range map[string]func(*fixture){
		"discovery digest mismatch": func(f *fixture) { f.plugin.digest = "sha256:" + strings.Repeat("0", 64) },
		"5xx without an envelope":   func(f *fixture) { f.plugin.answer = outageAnswer },
		"connection refused": func(f *fixture) {
			f.pin.Endpoint = "http://127.0.0.1:1"
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t, func(int) (int, any) { return 200, textParts("x") })
			edit(f)
			for i := 0; i < normalization.MaxAttemptsCap+2; i++ {
				if err := f.service.Normalize(context.Background(), "org_a", "receipt_1"); err == nil {
					t.Fatal("unavailability reported success")
				}
			}
			if len(f.store.saved) != 0 || len(f.store.attempts) != 0 {
				t.Fatalf("unavailability recorded %+v, counted %+v", f.store.saved, f.store.attempts)
			}
			if last := f.repo.progress[len(f.repo.progress)-1]; last != "retrying:plugin_unavailable" {
				t.Fatalf("progress %v", f.repo.progress)
			}
		})
	}
}

// Terminal errors and invalid output record a failed outcome at once, with a
// structured reason naming the invocation; nothing from the plugin is stored.
func TestTerminalFailuresRecordAFailedOutcome(t *testing.T) {
	for name, tc := range map[string]struct {
		answer func(int) (int, any)
		code   string
	}{
		"terminal plugin error": {func(int) (int, any) {
			return 422, map[string]any{"code": "bad_input", "message": "no", "retryable": false}
		}, "normalizer_failed"},
		"schema-invalid output": {func(int) (int, any) {
			return 200, map[string]any{"manifest": map[string]any{"kind": "manifest", "parts": []any{}}}
		}, "normalizer_invalid_output"},
		"malformed part": {func(int) (int, any) { return 200, textParts("broken\x00text") }, "normalizer_invalid_output"},
		"duplicate part keys": {func(int) (int, any) {
			r := textParts("a", "b")
			parts := r["manifest"].(map[string]any)["parts"].([]any)
			parts[1].(map[string]any)["key"] = parts[0].(map[string]any)["key"]
			return 200, r
		}, "normalizer_invalid_output"},
		"undeclared schema version": {func(int) (int, any) {
			r := textParts("a")
			r["extensions"] = outline("2", 1)
			return 200, r
		}, "normalizer_invalid_output"},
		"schema-invalid extension": {func(int) (int, any) {
			r := textParts("a")
			r["extensions"] = outline("1", "many")
			return 200, r
		}, "normalizer_invalid_output"},
		"schema-invalid Part extension": {func(int) (int, any) {
			r := textParts("a")
			r["manifest"].(map[string]any)["parts"].([]any)[0].(map[string]any)["extensions"] = outline("1", -1)
			return 200, r
		}, "normalizer_invalid_output"},
		"bad checksum (foreign blob part)": {func(int) (int, any) {
			return 200, map[string]any{"manifest": map[string]any{"kind": "manifest", "parts": []any{map[string]any{"key": "b", "role": "source", "content": map[string]any{"kind": "blob", "blob_id": "blob_other", "media_type": "text/markdown"}}}}}
		}, "normalizer_invalid_output"},
		"too many parts": {func(int) (int, any) { return 200, textParts("a", "b", "c", "d", "e") }, "normalizer_invalid_output"},
		"undeclared namespace": {func(int) (int, any) {
			r := textParts("a")
			r["extensions"] = map[string]any{"another-plugin.stats": map[string]any{"schema_version": "1", "data": map[string]any{}}}
			return 200, r
		}, "normalizer_invalid_output"},
		"oversized response": {func(int) (int, any) { return 200, textParts(strings.Repeat("x", normalization.MaxManifestBytes+10)) }, "normalizer_invalid_output"},
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t, tc.answer)
			if err := f.service.Normalize(context.Background(), "org_a", "receipt_1"); err != nil {
				t.Fatalf("a terminal failure must record an outcome, got %v", err)
			}
			got, ok := f.store.saved["version_1"]
			if !ok || got.Outcome != content.OutcomeFailed || got.Failure == nil || got.Failure.Code != tc.code || got.Failure.Message == "" || got.Failure.Retryable {
				t.Fatalf("outcome %+v failure %+v", got, got.Failure)
			}
			if got.Manifest != (content.Blob{}) || len(f.blobs.objects) != 0 {
				t.Fatal("plugin output stored for a failed invocation")
			}
			p := got.Provenance
			if p.PluginID != "acme.markdown" || p.Contribution != "normalizer" || !strings.HasPrefix(p.InvocationID, "inv_") || got.InputBlobID != "blob_md" || p.InputSHA256 != content.Hash(input) {
				t.Fatalf("reprocessing reference %+v", got)
			}
			if f.plugin.calls != 1 {
				t.Fatalf("calls %d", f.plugin.calls)
			}
			// Invalid extensions keep their structured issue in the diagnostic.
			if want := issueCodes[name]; want != "" && !strings.Contains(got.Failure.Message, want) {
				t.Fatalf("message %q does not name the structured issue %s", got.Failure.Message, want)
			}
		})
	}
}

// Retryable errors and timeouts retry until the declared budget is spent.
func TestRetryBudget(t *testing.T) {
	for name, tc := range map[string]struct {
		answer func(int) (int, any)
		code   string
	}{
		"retryable plugin error": {func(int) (int, any) {
			return 503, map[string]any{"code": "busy", "message": "later", "retryable": true}
		}, "normalizer_retries_exhausted"},
		"timeout": {func(int) (int, any) { time.Sleep(1500 * time.Millisecond); return 200, textParts("late") }, "normalizer_timeout"},
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t, tc.answer)
			// The fixture declares retry.max_attempts: 2.
			if err := f.service.Normalize(context.Background(), "org_a", "receipt_1"); err == nil {
				t.Fatal("the first budgeted failure must retry")
			}
			if len(f.store.saved) != 0 || f.repo.progress[len(f.repo.progress)-1] != "retrying:normalizer_retrying" {
				t.Fatalf("saved %+v progress %v", f.store.saved, f.repo.progress)
			}
			if err := f.service.Normalize(context.Background(), "org_a", "receipt_1"); err != nil {
				t.Fatalf("an exhausted budget must record an outcome, got %v", err)
			}
			got := f.store.saved["version_1"]
			if got.Outcome != content.OutcomeFailed || got.Failure.Code != tc.code || !got.Failure.Retryable {
				t.Fatalf("outcome %+v %+v", got, got.Failure)
			}
		})
	}
	if got := normalization.MaxAttempts(&plugins.Normalizer{Retry: plugins.Retry{MaxAttempts: 10}}); got != normalization.MaxAttemptsCap {
		t.Fatalf("the engine cap is not applied: %d", got)
	}
}

func optionalSetup(t *testing.T, answer func(int) (int, any)) *fixture {
	t.Helper()
	f := setup(t, answer)
	pin, err := plugins.LoadPin(plugins.PinConfig{Manifest: f.pin.Path, Endpoint: f.pin.Endpoint, Configuration: f.pin.Configuration, Routes: []plugins.RouteConfig{{MediaType: "text/markdown", Mode: plugins.RouteOptional}}})
	if err != nil {
		t.Fatal(err)
	}
	f.service.Pin = fixed{pin}
	f.service.Plugin = pluginhttp.Normalizer{}
	f.blobs.objects["org/blob"] = input
	return f
}

// An optional route falls back to the built-in text path: the recorded
// Manifest is the verified bytes as one body Part, and the provenance names
// the failed invocation.
func TestOptionalRouteFallsBackToTheBuiltinTextPath(t *testing.T) {
	f := optionalSetup(t, func(int) (int, any) {
		return 422, map[string]any{"code": "bad_input", "message": "no", "retryable": false}
	})
	if err := f.service.Normalize(context.Background(), "org_a", "receipt_1"); err != nil {
		t.Fatal(err)
	}
	got := f.store.saved["version_1"]
	if got.Outcome != content.OutcomeFallback || got.Failure == nil || got.Failure.Code != "normalizer_failed" || got.Provenance.Fallback == nil || got.Provenance.Fallback.Code != "normalizer_failed" || !strings.HasPrefix(got.Provenance.InvocationID, "inv_") {
		t.Fatalf("outcome %+v", got)
	}
	var m content.Manifest
	if err := json.Unmarshal(f.blobs.objects[got.Manifest.Key], &m); err != nil || len(m.Parts) != 1 || m.Parts[0].Role != "body" || m.Parts[0].Content.Text != string(input) {
		t.Fatalf("fallback manifest %+v %v", m, err)
	}
	// Unavailability still retries on an optional route.
	g := optionalSetup(t, outageAnswer)
	if err := g.service.Normalize(context.Background(), "org_a", "receipt_1"); err == nil || len(g.store.saved) != 0 {
		t.Fatalf("an optional route fell back on an outage: %v", err)
	}
}

type supersession struct{ withdrawn, superseded bool }

func (s supersession) Superseded(context.Context, string, string, string) (bool, bool, error) {
	return s.withdrawn, s.superseded, nil
}

// Withdrawn Records, superseded Versions and media types that are no longer
// routed never call the plugin and record nothing: publication decides.
func TestSkippedNormalizationsNeverCallThePlugin(t *testing.T) {
	for name, edit := range map[string]func(*fixture){
		"withdrawn":     func(f *fixture) { f.service.Content.Supersession = supersession{withdrawn: true} },
		"superseded":    func(f *fixture) { f.service.Content.Supersession = supersession{superseded: true} },
		"route removed": func(f *fixture) { f.service.Pin = nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t, func(int) (int, any) { return 200, textParts("x") })
			edit(f)
			if err := f.service.Normalize(context.Background(), "org_a", "receipt_1"); err != nil || f.plugin.calls != 0 || len(f.store.saved) != 0 {
				t.Fatalf("err %v calls %d saved %+v", err, f.plugin.calls, f.store.saved)
			}
		})
	}
}

// A plugin's own error text is quoted in the failure message: it is stored
// and published as valid UTF-8 without NUL and within the length bound, so a
// terminal failure always records its outcome.
func TestFailureMessagesAreBoundedText(t *testing.T) {
	long := strings.Repeat("é", 1000) + "\x00x"
	f := setup(t, func(int) (int, any) {
		return 422, map[string]any{"code": "bad_input", "message": long, "retryable": false}
	})
	if err := f.service.Normalize(context.Background(), "org_a", "receipt_1"); err != nil {
		t.Fatal(err)
	}
	m := f.store.saved["version_1"].Failure.Message
	if !utf8.ValidString(m) || strings.Contains(m, "\x00") || utf8.RuneCountInString(m) > 1000 || !strings.HasSuffix(m, "… [truncated]") {
		t.Fatalf("message %q", m)
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
	if err == nil || len(f.store.saved) != 0 {
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
