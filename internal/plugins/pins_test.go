package plugins_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

const alertsManifest = `id: acme.alerts
version: 0.3.0
compatibility:
  engine: ">=0.1.0 <0.3.0"
  plugin_api: ">=0.2.0 <0.3.0"
contributions:
  subscription:
    expression_schema:
      type: object
      required: [text]
      properties:
        text: {type: string, minLength: 1}
    max_batch_size: 8
`

const connectorManifest = `id: acme.source
version: 1.0.0
compatibility:
  engine: ">=0.1.0 <0.3.0"
  plugin_api: ">=0.3.0 <0.4.0"
contributions:
  connector:
    kinds:
      feed:
        config_schema: {type: object}
        default_interval_seconds: 900
        modes: [pull]
`

// A connector-only plugin needs no routes and resolves its kinds; it
// receives credentials, so plain HTTP is allowed only on loopback.
func TestLoadPinsResolvesConnectorKinds(t *testing.T) {
	source := writePinManifest(t, connectorManifest)
	for _, endpoint := range []string{"http://127.0.0.1:9903", "http://[::1]:9903", "http://localhost:9903", "https://source.example:9903"} {
		set, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: source, Endpoint: endpoint}})
		if err != nil {
			t.Fatalf("%s: %v", endpoint, err)
		}
		if kinds := set.Connectors(); len(kinds) != 1 || kinds[0].Kind != "feed" || kinds[0].Pin.Manifest.ID != "acme.source" {
			t.Fatalf("%s: connector kinds %+v", endpoint, kinds)
		}
	}
	_, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: source, Endpoint: "http://source.example:9903"}})
	assertPinCode(t, err, plugins.CodeInvalidPin)
	if !strings.Contains(err.Error(), "https://") {
		t.Fatalf("the refusal does not say how to fix it: %v", err)
	}
	var none *plugins.PinSet
	if len(none.Connectors()) != 0 {
		t.Fatal("a nil set provides no connector kind")
	}
}

// Several plugins are pinned together: normalizers route by media type and
// subscription evaluators are listed with their plugin id and version.
func TestLoadPinsRoutesContributionsAcrossPlugins(t *testing.T) {
	markdown := writePinManifest(t, pinManifest)
	alerts := writePinManifest(t, alertsManifest)
	set, err := plugins.LoadPins([]plugins.PinConfig{
		pinConfig(markdown, nil),
		{Manifest: alerts, Endpoint: "http://127.0.0.1:9902"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pin, route, ok := set.Normalizer("text/markdown")
	if !ok || pin.Manifest.ID != "acme.markdown" || route.Mode != plugins.RouteRequired || !set.Routed("text/markdown") || set.Routed("application/pdf") {
		t.Fatalf("normalizer routing: %+v %+v %v", pin, route, ok)
	}
	// The normalizer-only plugin is not an evaluator.
	evaluators := set.Evaluators()
	if len(evaluators) != 1 || evaluators[0].Manifest.ID != "acme.alerts" || evaluators[0].Manifest.Version != "0.3.0" || evaluators[0].Endpoint != "http://127.0.0.1:9902" || evaluators[0].Manifest.Contributions.Subscription.MaxBatchSize != 8 {
		t.Fatalf("evaluators %+v", evaluators)
	}
	if got := len(set.Pins()); got != 2 {
		t.Fatalf("pins %d", got)
	}
	var none *plugins.PinSet
	if none.Routed("text/markdown") || len(none.Pins()) != 0 || len(none.Evaluators()) != 0 {
		t.Fatal("a nil set pins nothing")
	}
}

// A subscription-only plugin needs no routes, and routes on a manifest
// without a normalizer are refused.
func TestLoadPinsRoutesOnlyNormalizers(t *testing.T) {
	alerts := writePinManifest(t, alertsManifest)
	if _, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: alerts, Endpoint: "http://127.0.0.1:9902"}}); err != nil {
		t.Fatal("subscription-only pin refused:", err)
	}
	_, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: alerts, Endpoint: "http://127.0.0.1:9902", Routes: []plugins.RouteConfig{{MediaType: "text/markdown"}}}})
	assertPinCode(t, err, plugins.CodeInvalidPin)
}

// Conflicts between pins refuse startup with an actionable code.
func TestLoadPinsRefusesConflicts(t *testing.T) {
	markdown := writePinManifest(t, pinManifest)
	other := writePinManifest(t, strings.Replace(pinManifest, "id: acme.markdown", "id: acme.other", 1))
	source := writePinManifest(t, connectorManifest)
	sameKind := writePinManifest(t, strings.Replace(connectorManifest, "id: acme.source", "id: acme.mirror", 1))
	for name, tc := range map[string]struct {
		configs []plugins.PinConfig
		code    string
	}{
		"same plugin twice": {[]plugins.PinConfig{pinConfig(markdown, nil), pinConfig(markdown, func(c *plugins.PinConfig) {
			c.Routes = []plugins.RouteConfig{{MediaType: "text/x-rst"}}
		})}, plugins.CodePluginConflict},
		"media type routed twice": {[]plugins.PinConfig{pinConfig(markdown, nil), pinConfig(other, nil)}, plugins.CodeRouteConflict},
		"connector kind twice":    {[]plugins.PinConfig{{Manifest: source, Endpoint: "http://127.0.0.1:9903"}, {Manifest: sameKind, Endpoint: "http://127.0.0.1:9904"}}, plugins.CodeKindConflict},
		"invalid member pin":      {[]plugins.PinConfig{pinConfig(markdown, nil), pinConfig(other, func(c *plugins.PinConfig) { c.Endpoint = "ftp://x" })}, plugins.CodeInvalidPin},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := plugins.LoadPins(tc.configs)
			assertPinCode(t, err, tc.code)
		})
	}
}

// The extension registry owns the namespaces of every pinned plugin beside
// the built-in ones: clients may no longer write them, and retrieval mappings
// may.
func TestPinSetExtensionRegistry(t *testing.T) {
	withOutline := pinManifest + "extensions:\n  acme.markdown.outline:\n    \"1\": {type: object}\n"
	set, err := plugins.LoadPins([]plugins.PinConfig{pinConfig(writePinManifest(t, withOutline), nil), {Manifest: writePinManifest(t, alertsManifest), Endpoint: "http://127.0.0.1:9902"}})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := plugins.PinsExtensionRegistry(set)
	if err != nil {
		t.Fatal(err)
	}
	if owner, ok := registry.Owner("acme.markdown.outline"); !ok || owner != "acme.markdown" {
		t.Fatalf("owner %q %v", owner, ok)
	}
	if !registry.Declared("example.editorial") || !registry.Declared("acme.markdown.outline") {
		t.Fatal("built-in or plugin namespace not declared")
	}
	none, err := plugins.PinsExtensionRegistry(nil)
	if err != nil || none.Declared("acme.markdown.outline") || !none.Declared("example.editorial") {
		t.Fatalf("without pins: %v", err)
	}
}

func assertPinCode(t *testing.T, err error, code string) {
	t.Helper()
	var pinErr *plugins.PinError
	if !errors.As(err, &pinErr) || !strings.Contains(err.Error(), code) {
		t.Fatalf("want a pin error with %s, got %v", code, err)
	}
}
