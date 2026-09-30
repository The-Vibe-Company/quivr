package plugins_test

import (
	"os"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

const retrieverManifest = "../../sdks/go/examples/fusion-retriever/quivr-plugin.yaml"

// A retrieval-only pin needs no routes, applies the manifest's defaults, and
// a deployment pins one retrieval plugin.
func TestLoadPinsRoutesSearchToOneRetrievalPlugin(t *testing.T) {
	set, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: retrieverManifest, Endpoint: "http://127.0.0.1:9906"}})
	if err != nil {
		t.Fatal(err)
	}
	r := set.Retrieval().Manifest.Contributions.Retrieval
	if got := strings.Join(r.ProfileNames(), ","); got != "default,deep" || r.Limits.MaxResponseBytes != plugins.DefaultRetrievalMaxResponseBytes {
		t.Fatalf("profiles %s, limits %+v", got, r.Limits)
	}
	other := writePinManifest(t, strings.Replace(readFile(t, retrieverManifest), "id: example.fusion_retriever", "id: acme.other_retriever", 1))
	_, err = plugins.LoadPins([]plugins.PinConfig{{Manifest: retrieverManifest, Endpoint: "http://127.0.0.1:9906"}, {Manifest: other, Endpoint: "http://127.0.0.1:9907"}})
	assertPinCode(t, err, plugins.CodeRetrievalConflict)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
