package pluginhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

const embedderManifest = `id: acme.embedder
version: 0.1.0
compatibility:
  engine: ">=0.1.0 <0.2.0"
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

// A plugin refuses a query terminally: query_too_long names its limit to the
// caller, any other code is a refusal of the query without detail.
func TestEncodeQueryPassesOnTheLimitAPluginNames(t *testing.T) {
	for _, c := range []struct {
		code, want string
		err        error
	}{
		{code: "query_too_long", err: retrieval.ErrQueryTooLong, want: "query exceeds 128 tokens"},
		{code: "unsupported_query", err: content.ErrInvalid},
	} {
		t.Run(c.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(422)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": c.code, "message": "query exceeds 128 tokens", "retryable": false})
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), plugins.ManifestFile)
			if err := os.WriteFile(path, []byte(embedderManifest), 0o600); err != nil {
				t.Fatal(err)
			}
			pin, err := plugins.LoadPin(plugins.PinConfig{Manifest: path, Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			_, err = pluginhttp.Ingestor{Pin: pin}.EncodeQuery(context.Background(), "org", plugins.SpaceKey("acme.embedder.small", "1"), "a long query")
			if !errors.Is(err, c.err) || publicerr.Detail(err) != c.want {
				t.Fatalf("error %v (detail %q), want %v (detail %q)", err, publicerr.Detail(err), c.err, c.want)
			}
		})
	}
}
