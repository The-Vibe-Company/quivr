package postgres_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/normalization"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

type routedTypes map[string]bool

func (r routedTypes) Routed(mediaType string) bool { return r[mediaType] }

type verifiedSource struct{ blob content.VerifiedBlob }

func (v verifiedSource) VerifiedBlob(context.Context, string, string) (content.VerifiedBlob, error) {
	return v.blob, nil
}

type objectMemory struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (m *objectMemory) Put(_ context.Context, org string, data []byte) (content.Blob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := content.Blob{Key: content.Hash([]byte(org)) + "/sha256/" + content.Hash(data), SHA256: content.Hash(data), Size: int64(len(data))}
	m.objects[b.Key] = data
	return b, nil
}
func (m *objectMemory) Read(_ context.Context, b content.Blob) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[b.Key]
	if !ok {
		return nil, content.ErrArtifactMissing
	}
	return data, nil
}

type staticSigner struct{}

func (staticSigner) PresignGet(_ context.Context, key string, ttl time.Duration) (string, time.Time, error) {
	return "http://objects.invalid/" + key, time.Now().Add(ttl), nil
}

// TestNormalizationRerunsConvergeOnOnePublishedManifest re-runs and races the
// normalization and publication steps of one routed Blob Version, like a
// worker restarted mid-workflow, against the real canonical store: one
// recorded output, one published Version carrying normalization provenance.
func TestNormalizationRerunsConvergeOnOnePublishedManifest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	run := time.Now().UTC().Format("20060102T150405.000000000")
	org := "adapter-normalization-" + run
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "normalization", Name: "Normalization"})
	if err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(t.TempDir(), plugins.ManifestFile)
	if err := os.WriteFile(manifestPath, []byte("id: acme.markdown\nversion: 1.0.0\ncompatibility:\n  engine: \">=0.1.0 <0.2.0\"\n  plugin_api: \">=0.1.0 <0.2.0\"\ncontributions:\n  normalizer:\n    media_types: [text/markdown]\nextensions:\n  acme.markdown.outline:\n    \"1\": {type: object, properties: {heading_count: {type: integer}}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var digest atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v0/discovery" {
			_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": "0.1.0", "plugin": map[string]any{"id": "acme.markdown", "version": "1.0.0"}, "manifest_digest": digest.Load(), "contributions": []string{"normalizer"}})
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"manifest": map[string]any{"kind": "manifest", "parts": []any{
			map[string]any{"key": "title", "role": "title", "content": map[string]any{"kind": "text", "text": "Lighthouse guide"}},
			map[string]any{"key": "section-1", "role": "section", "content": map[string]any{"kind": "text", "text": "The keeper lights the lamp."}},
		}}, "extensions": map[string]any{"acme.markdown.outline": map[string]any{"schema_version": "1", "data": map[string]any{"heading_count": 1}}}})
	}))
	defer server.Close()
	pin, err := plugins.LoadPin(plugins.PinConfig{Manifest: manifestPath, Endpoint: server.URL, Routes: []plugins.RouteConfig{{MediaType: "text/markdown"}}})
	if err != nil {
		t.Fatal(err)
	}
	digest.Store(pin.ManifestDigest)

	input := []byte("# Lighthouse guide\n\nThe keeper lights the lamp.")
	blob := content.VerifiedBlob{ID: "blob_" + run, MediaType: "text/markdown", Blob: content.Blob{Key: "inputs/" + run, SHA256: content.Hash(input), Size: int64(len(input))}}
	store := postgres.ContentStore{Pool: pool}
	objects := &objectMemory{objects: map[string][]byte{}}
	contents := content.Service{Repository: store, Catalog: store, Blobs: objects, BlobSource: verifiedSource{blob}, Relations: store, Routes: liveOf(t, pin), Normalizations: store}
	receipt, err := contents.Accept(ctx, scope, content.Command{Key: "routed-" + run, Source: content.Source{CorpusID: c.ID, Namespace: "docs", RecordKey: "guide"}, Content: content.Text{Kind: "blob", BlobID: blob.ID, MediaType: "text/markdown"}})
	if err != nil {
		t.Fatal(err)
	}
	normalizer := normalization.Service{Content: contents, Store: store, Signer: staticSigner{}, Plugin: pluginhttp.Client{Pin: pin}, Pin: liveOf(t, pin)}

	// Publication before normalization waits; it never publishes the source Blob Part.
	if err := contents.Materialize(ctx, org, receipt.ID); err == nil {
		t.Fatal("published before normalization")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- normalizer.Normalize(ctx, org, receipt.ID) }()
	}
	wg.Wait()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := normalizer.Normalize(ctx, org, receipt.ID); err != nil {
				errs <- err
				return
			}
			errs <- contents.Materialize(ctx, org, receipt.ID)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var rows, versions int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM normalizations WHERE organization=$1", org).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM record_versions WHERE organization=$1", org).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || versions != 1 {
		t.Fatalf("normalizations %d, versions %d", rows, versions)
	}
	resolved, err := contents.Receipt(ctx, scope, receipt.ID)
	if err != nil || resolved.Outcome != "created" {
		t.Fatalf("receipt %+v %v", resolved, err)
	}
	v, err := contents.Version(ctx, scope, resolved.RecordID, resolved.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	if v.SourceMediaType != "text/markdown" {
		t.Fatalf("normalized Version source media type = %q, want text/markdown", v.SourceMediaType)
	}
	if len(v.Manifest.Parts) != 2 || v.Manifest.Parts[1].Content.Text != "The keeper lights the lamp." {
		t.Fatalf("published manifest %+v", v.Manifest)
	}
	n, _ := v.Provenance["normalization"].(map[string]any)
	recorded, _, _ := store.Normalized(ctx, org, resolved.VersionID)
	if n["invocation_id"] != recorded.Provenance.InvocationID || n["input_sha256"] != blob.Blob.SHA256 || n["plugin_id"] != "acme.markdown" {
		t.Fatalf("provenance %+v, recorded %+v", v.Provenance, recorded)
	}
	// The plugin's extensions are recorded once and published on the Version.
	if ext, ok := v.Extensions["acme.markdown.outline"]; !ok || ext.SchemaVersion != "1" || ext.Data["heading_count"] != float64(1) {
		t.Fatalf("published extensions %+v", v.Extensions)
	}
	if recorded.Extensions["acme.markdown.outline"].Data["heading_count"] != float64(1) {
		t.Fatalf("recorded extensions %+v", recorded.Extensions)
	}
	if ids, _ := v.Provenance["source_blob_ids"].([]any); len(ids) != 1 || ids[0] != blob.ID {
		t.Fatalf("source provenance %+v", v.Provenance)
	}
	if calls.Load() < 1 {
		t.Fatal("the plugin was never invoked")
	}
}
