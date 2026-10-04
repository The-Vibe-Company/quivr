package plugins_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

const embedderManifest = `id: acme.embedder
version: 0.2.0
compatibility:
  engine: ">=0.1.0 <0.3.0"
  plugin_api: ">=0.6.0 <0.7.0"
contributions:
  ingestion:
    spaces:
      acme.embedder.small: {version: "1", model: m-small, dimensions: 8, metric: cosine, indexes: [text], query_modalities: [text]}
      acme.embedder.large: {version: "3", model: m-large, dimensions: 16, metric: dot, indexes: [text], query_modalities: [text]}
`

// An ingestion pin enables declared spaces, exactly one served, and several
// ingestion plugins can be routed by the source media type.
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
	set, err = plugins.LoadPins([]plugins.PinConfig{pin(map[string]string{"acme.embedder.small": "served"}), {Manifest: other, Endpoint: "http://127.0.0.1:9905", Spaces: map[string]string{"acme.other.small": "served"}}})
	if err != nil {
		t.Fatal("several ingestion pins should be accepted:", err)
	}
	if got := set.Ingestions(); len(got) != 2 || got[0].Manifest.ID != "acme.embedder" || got[1].Manifest.ID != "acme.other" {
		t.Fatalf("ingestions %+v, want sorted by plugin id", got)
	}
	if err := set.ConfigureIngestion(plugins.IngestionRouting{Default: "acme.embedder", Routes: map[string]string{"application/pdf": "acme.other"}}); err != nil {
		t.Fatal("configure source routes:", err)
	}
	if set.Ingestion() != set.Ingestions()[0] || set.IngestionFor("application/pdf") != set.Ingestions()[1] || set.IngestionFor("text/plain") != set.Ingestions()[0] {
		t.Fatalf("configured routing default=%v pdf=%v text=%v", set.Ingestion(), set.IngestionFor("application/pdf"), set.IngestionFor("text/plain"))
	}
	for _, route := range []struct{ media, owner string }{{"application/pdf", "acme.other"}, {"text/plain", "acme.embedder"}, {"text/html", "acme.embedder"}, {"", "acme.embedder"}, {"APPLICATION/PDF", "acme.embedder"}} {
		if got := set.IngestionFor(route.media); got == nil || got.Manifest.ID != route.owner {
			t.Fatalf("source %q selected %v, want %s", route.media, got, route.owner)
		}
	}
	if owner := set.SpaceOwner("acme.embedder.small@1"); owner == nil || owner.Manifest.ID != "acme.embedder" {
		t.Fatalf("declared space owner %+v", owner)
	}
	routing := set.IngestionRouting()
	routing.Routes["text/plain"] = "acme.other"
	if set.IngestionFor("text/plain") != set.Ingestions()[0] {
		t.Fatal("routing snapshot was not defensive")
	}

	collisionBase := strings.ReplaceAll(embedderManifest, "acme.embedder", "acme.owner")
	collisionFirst := strings.Replace(collisionBase, "acme.owner.small", "acme.owner.shared.small", 1)
	collisionSecond := strings.ReplaceAll(embedderManifest, "acme.embedder", "acme.owner.shared")
	_, err = plugins.LoadPins([]plugins.PinConfig{{Manifest: writePinManifest(t, collisionFirst), Endpoint: "http://127.0.0.1:9904", Spaces: map[string]string{"acme.owner.shared.small": "served"}}, {Manifest: writePinManifest(t, collisionSecond), Endpoint: "http://127.0.0.1:9905", Spaces: map[string]string{"acme.owner.shared.small": "served"}}})
	assertPinCode(t, err, plugins.CodeSpaceConflict)
}

func TestConfigureIngestionRejectsAmbiguousOrUnknownRoutes(t *testing.T) {
	first := writePinManifest(t, strings.ReplaceAll(embedderManifest, "acme.embedder", "acme.first"))
	second := writePinManifest(t, strings.ReplaceAll(embedderManifest, "acme.embedder", "acme.second"))
	set, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: first, Endpoint: "http://127.0.0.1:9904", Spaces: map[string]string{"acme.first.small": "served", "acme.first.large": "evaluation"}}, {Manifest: second, Endpoint: "http://127.0.0.1:9905", Spaces: map[string]string{"acme.second.small": "served", "acme.second.large": "evaluation"}}})
	if err != nil {
		t.Fatal(err)
	}
	if set.Ingestion() != nil {
		t.Fatalf("ambiguous set selected default %s", set.Ingestion().Manifest.ID)
	}
	for name, routing := range map[string]plugins.IngestionRouting{
		"ambiguous default":                      {Routes: map[string]string{"application/pdf": "acme.first"}},
		"unknown default":                        {Default: "acme.missing"},
		"unknown route":                          {Default: "acme.first", Routes: map[string]string{"application/pdf": "acme.missing"}},
		"empty evaluation media type":            {Default: "acme.first", Evaluation: map[string][]string{"": {"acme.second"}}},
		"unknown evaluation target":              {Default: "acme.first", Evaluation: map[string][]string{"application/pdf": {"acme.missing"}}},
		"duplicate evaluation target":            {Default: "acme.first", Evaluation: map[string][]string{"application/pdf": {"acme.second", "acme.second"}}},
		"evaluation target is served by default": {Default: "acme.first", Evaluation: map[string][]string{"text/plain": {"acme.first"}}},
		"evaluation target is served by route":   {Default: "acme.first", Routes: map[string]string{"application/pdf": "acme.second"}, Evaluation: map[string][]string{"application/pdf": {"acme.second"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := set.ConfigureIngestion(routing); err == nil {
				t.Fatal("configuration unexpectedly accepted")
			}
		})
	}
}

func TestIngestionEvaluationRoutingIsDefensiveAndDistinguishesServing(t *testing.T) {
	first := writePinManifest(t, strings.ReplaceAll(embedderManifest, "acme.embedder", "acme.first"))
	second := writePinManifest(t, strings.ReplaceAll(embedderManifest, "acme.embedder", "acme.second"))
	set, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: first, Endpoint: "http://127.0.0.1:9904", Spaces: map[string]string{"acme.first.small": "served", "acme.first.large": "evaluation"}}, {Manifest: second, Endpoint: "http://127.0.0.1:9905", Spaces: map[string]string{"acme.second.small": "served", "acme.second.large": "evaluation"}}})
	if err != nil {
		t.Fatal(err)
	}
	routing := plugins.IngestionRouting{
		Default: "acme.first",
		Routes:  map[string]string{"application/pdf": "acme.second"},
		Evaluation: map[string][]string{
			"application/pdf": {"acme.first"},
			"text/plain":      {"acme.second"},
		},
	}
	if err := set.ConfigureIngestion(routing); err != nil {
		t.Fatal(err)
	}
	if got := set.EvaluationFor("application/pdf"); len(got) != 1 || got[0].Manifest.ID != "acme.first" {
		t.Fatalf("pdf evaluation %v", got)
	}
	if got := set.EvaluationFor(""); len(got) != 1 || got[0].Manifest.ID != "acme.second" {
		t.Fatalf("empty media type evaluation %v", got)
	}
	for _, pluginID := range []string{"acme.first", "acme.second"} {
		if !set.ServingIngestion(pluginID) {
			t.Fatalf("%s should be serving", pluginID)
		}
	}

	routing.Evaluation["application/pdf"][0] = "acme.second"
	routing.Evaluation["text/plain"] = nil
	snapshot := set.IngestionRouting()
	snapshot.Evaluation["application/pdf"][0] = "acme.second"
	snapshot.Evaluation["text/plain"] = nil
	if got := set.EvaluationFor("application/pdf"); len(got) != 1 || got[0].Manifest.ID != "acme.first" {
		t.Fatalf("evaluation routing snapshot was not defensive: %v", got)
	}
	got := set.EvaluationFor("application/pdf")
	got[0] = set.IngestionFor("application/pdf")
	if got = set.EvaluationFor("application/pdf"); len(got) != 1 || got[0].Manifest.ID != "acme.first" {
		t.Fatalf("evaluation accessor was not defensive: %v", got)
	}

	evaluationOnly, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: first, Endpoint: "http://127.0.0.1:9904", Spaces: map[string]string{"acme.first.small": "served"}}, {Manifest: second, Endpoint: "http://127.0.0.1:9905", Spaces: map[string]string{"acme.second.small": "served"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluationOnly.ConfigureIngestion(plugins.IngestionRouting{Default: "acme.first", Evaluation: map[string][]string{"application/pdf": {"acme.second"}}}); err != nil {
		t.Fatal(err)
	}
	if evaluationOnly.ServingIngestion("acme.second") || !evaluationOnly.ServingIngestion("acme.first") {
		t.Fatalf("serving status default=%v evaluation-only=%v", evaluationOnly.ServingIngestion("acme.first"), evaluationOnly.ServingIngestion("acme.second"))
	}
}

// The search protocol carries at most 16 enabled spaces across all owners.
// Refuse an oversized deployment at pinning instead of its first search.
func TestIngestionSpacesFitTheSearchProtocol(t *testing.T) {
	configs := []plugins.PinConfig{}
	for i := range 17 {
		id := fmt.Sprintf("example.owner%d", i)
		manifest := strings.ReplaceAll(embedderManifest, "acme.embedder", id)
		configs = append(configs, plugins.PinConfig{Manifest: writePinManifest(t, manifest), Endpoint: "http://127.0.0.1:9904", Spaces: map[string]string{id + ".small": "served"}})
	}
	if _, err := plugins.LoadPins(configs[:16]); err != nil {
		t.Fatalf("16 enabled spaces refused: %v", err)
	}
	if _, err := plugins.LoadPins(configs); err == nil || !strings.Contains(err.Error(), plugins.CodeInvalidPin) {
		t.Fatalf("17 enabled spaces: %v, want an invalid pin set", err)
	}
	configs[0].Spaces["example.owner0.large"] = plugins.SpaceEvaluation
	if _, err := plugins.LoadPins(configs[:15]); err != nil {
		t.Fatalf("16 spaces including evaluation refused: %v", err)
	}
	if _, err := plugins.LoadPins(configs[:16]); err == nil {
		t.Fatal("evaluation space did not count toward the protocol boundary")
	}
}
