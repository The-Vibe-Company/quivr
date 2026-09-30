package plugins_test

import (
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

const embedderManifest = `id: acme.embedder
version: 0.2.0
compatibility:
  engine: ">=0.1.0 <0.2.0"
  plugin_api: ">=0.6.0 <0.7.0"
contributions:
  ingestion:
    spaces:
      acme.embedder.small: {version: "1", model: m-small, dimensions: 8, metric: cosine, indexes: [text], query_modalities: [text]}
      acme.embedder.large: {version: "3", model: m-large, dimensions: 16, metric: dot, indexes: [text], query_modalities: [text]}
`

// An ingestion pin enables declared spaces, exactly one served, and a
// deployment pins one ingestion plugin.
func TestLoadPinsEnablesIngestionSpaces(t *testing.T) {
	embedder := writePinManifest(t, embedderManifest)
	pin := func(spaces map[string]string) plugins.PinConfig {
		return plugins.PinConfig{Manifest: embedder, Endpoint: "http://127.0.0.1:9904", Spaces: spaces}
	}
	set, err := plugins.LoadPins([]plugins.PinConfig{pin(map[string]string{"acme.embedder.small": "evaluation", "acme.embedder.large": "served"})})
	if err != nil {
		t.Fatal(err)
	}
	spaces := set.Ingestion().EnabledSpaces()
	if len(spaces) != 2 || spaces[0].Key != "acme.embedder.large@3" || spaces[0].Role != plugins.SpaceServed || spaces[1].Key != "acme.embedder.small@1" || spaces[1].Space.Dimensions != 8 {
		t.Fatalf("enabled spaces %+v", spaces)
	}
	only := writePinManifest(t, strings.Replace(embedderManifest, "      acme.embedder.large: {version: \"3\", model: m-large, dimensions: 16, metric: dot, indexes: [text], query_modalities: [text]}\n", "", 1))
	single, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: only, Endpoint: "http://127.0.0.1:9904"}})
	if err != nil {
		t.Fatal(err)
	}
	if s := single.Ingestion().EnabledSpaces(); len(s) != 1 || s[0].Role != plugins.SpaceServed {
		t.Fatalf("the only declared space is served by default: %+v", s)
	}
	for name, spaces := range map[string]map[string]string{
		"several declared, none chosen": nil,
		"undeclared space":              {"acme.embedder.small": "served", "acme.embedder.other": "evaluation"},
		"unknown role":                  {"acme.embedder.small": "served", "acme.embedder.large": "shadow"},
		"two served":                    {"acme.embedder.small": "served", "acme.embedder.large": "served"},
		"none served":                   {"acme.embedder.small": "evaluation"},
	} {
		_, err := plugins.LoadPins([]plugins.PinConfig{pin(spaces)})
		if err == nil || !strings.Contains(err.Error(), plugins.CodeInvalidPin) {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err = plugins.LoadPins([]plugins.PinConfig{{Manifest: writePinManifest(t, alertsManifest), Endpoint: "http://127.0.0.1:9902", Spaces: map[string]string{"acme.alerts": "served"}}})
	assertPinCode(t, err, plugins.CodeInvalidPin)
	other := writePinManifest(t, strings.ReplaceAll(embedderManifest, "acme.embedder", "acme.other"))
	_, err = plugins.LoadPins([]plugins.PinConfig{pin(map[string]string{"acme.embedder.small": "served"}), {Manifest: other, Endpoint: "http://127.0.0.1:9905", Spaces: map[string]string{"acme.other.small": "served"}}})
	assertPinCode(t, err, plugins.CodeIngestionConflict)
}
