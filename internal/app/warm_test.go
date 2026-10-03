package app

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

const warmManifest = `id: acme.embedder
version: 0.1.0
compatibility:
  engine: ">=0.1.0 <0.3.0"
  plugin_api: ">=0.6.0 <0.7.0"
contributions:
  ingestion:
    spaces:
      acme.embedder.small:
        version: "1"
        model: acme/small
        dimensions: 2
        metric: cosine
        indexes: [text]
        query_modalities: [text]
`

func warmPin(t *testing.T, endpoint string) *plugins.Pin {
	t.Helper()
	path := filepath.Join(t.TempDir(), plugins.ManifestFile)
	if err := os.WriteFile(path, []byte(warmManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	pin, err := plugins.LoadPin(plugins.PinConfig{Manifest: path, Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	return pin
}

// awaitWarm fails the test unless done closes before a generous deadline.
func awaitWarm(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the warm-up never ended: the api would never report ready")
	}
}

// The api reports ready only once the ingestion plugin answered its warm-up
// embed_query (THE-813), and an answer that says the embedding service is
// down still ends the warm-up at once: readiness never waits for that
// service, only for the plugin's own first-use loading.
func TestReadinessWaitsForTheQueryEncoderWarmUp(t *testing.T) {
	received := make(chan map[string]any, 1)
	release := make(chan struct{})
	var pin *plugins.Pin
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/discovery" {
			_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": "0.6.0", "plugin": map[string]string{"id": pin.Manifest.ID, "version": pin.Manifest.Version}, "manifest_digest": pin.ManifestDigest, "contributions": []string{"ingestion"}})
			return
		}
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		select {
		case received <- request:
		default:
		}
		<-release // the plugin is loading its tokenizer
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "inference_unavailable", "message": "the embedding service is unavailable", "retryable": true})
	}))
	defer server.Close()
	var unblock sync.Once
	defer unblock.Do(func() { close(release) }) // before Close, so a failing test does not hang on the handler
	// A bound far beyond the test's deadline: only the plugin's answer can end the warm-up.
	pin = warmPin(t, server.URL)
	done := warmQueries(t.Context(), pluginhttp.Ingestor{Pin: pin}.Warm, time.Hour, time.Millisecond)
	var request map[string]any
	select {
	case request = <-received:
	case <-time.After(10 * time.Second):
		t.Fatal("no warm-up embed_query reached the plugin")
	}
	if warmed(done) {
		t.Fatal("ready while the plugin is still loading")
	}
	if request["organization_id"] != pluginhttp.WarmUpOrganization || request["space"] != "acme.embedder.small" {
		t.Fatalf("warm-up request %v, want organization %q and space acme.embedder.small", request, pluginhttp.WarmUpOrganization)
	}
	unblock.Do(func() { close(release) })
	awaitWarm(t, done)
}

// A sidecar that starts after the api is warmed once it answers: the
// attempts it drops are retried within the bound.
func TestWarmUpRetriesUntilThePluginAnswers(t *testing.T) {
	var attempts atomic.Int32
	var pin *plugins.Pin
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/discovery" {
			_ = json.NewEncoder(w).Encode(map[string]any{"plugin_api": "0.6.0", "plugin": map[string]string{"id": pin.Manifest.ID, "version": pin.Manifest.Version}, "manifest_digest": pin.ManifestDigest, "contributions": []string{"ingestion"}})
			return
		}
		if attempts.Add(1) <= 2 {
			// Not serving yet: the connection closes without an answer.
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"vector": []float64{0.6, 0.8}})
	}))
	defer server.Close()
	pin = warmPin(t, server.URL)
	done := warmQueries(t.Context(), pluginhttp.Ingestor{Pin: pin}.Warm, time.Hour, time.Millisecond)
	awaitWarm(t, done)
	if n := attempts.Load(); n != 3 {
		t.Fatalf("%d warm-up attempts, want 3: two dropped, one answered", n)
	}
}

// A plugin that never answers (its sidecar did not start) delays readiness by
// the bound only.
func TestReadinessGivesUpOnAPluginThatNeverAnswers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + listener.Addr().String()
	_ = listener.Close() // nothing listens there: every attempt is refused
	done := warmQueries(t.Context(), pluginhttp.Ingestor{Pin: warmPin(t, endpoint)}.Warm, 50*time.Millisecond, time.Millisecond)
	awaitWarm(t, done)
}
