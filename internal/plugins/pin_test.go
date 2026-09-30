package plugins_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

const pinManifest = `id: acme.markdown
version: 1.2.0
compatibility:
  engine: ">=0.1.0 <0.2.0"
  plugin_api: ">=0.1.0 <0.2.0"
contributions:
  normalizer:
    media_types: [text/markdown, text/x-rst, application/pdf]
    timeout_ms: 5000
configuration:
  schema:
    type: object
    additionalProperties: false
    properties:
      max_sections: {type: integer, minimum: 1}
`

func writePinManifest(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), plugins.ManifestFile)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func pinConfig(manifest string, edit func(*plugins.PinConfig)) plugins.PinConfig {
	c := plugins.PinConfig{
		Manifest:      manifest,
		Endpoint:      "http://127.0.0.1:9901",
		Configuration: json.RawMessage(`{"max_sections": 4}`),
		Routes:        []plugins.RouteConfig{{MediaType: "text/markdown"}},
	}
	if edit != nil {
		edit(&c)
	}
	return c
}

func TestLoadPinAcceptsAValidPin(t *testing.T) {
	path := writePinManifest(t, pinManifest)
	pin, err := plugins.LoadPin(pinConfig(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	if pin.Manifest.ID != "acme.markdown" || pin.Manifest.Version != "1.2.0" || !strings.HasPrefix(pin.ManifestDigest, "sha256:") {
		t.Fatalf("pin %+v", pin)
	}
	if !pin.Routed("text/markdown") || pin.Routed("Text/Markdown") || pin.Routed("text/x-rst") || pin.Routed("text/plain") {
		t.Fatal("routing does not follow the configured routes")
	}
	if route, ok := pin.Route("text/markdown"); !ok || route.Mode != plugins.RouteRequired {
		t.Fatalf("default mode %+v", route)
	}
	if got := pin.Generation(); got != "startup:acme.markdown@1.2.0#"+pin.ManifestDigest {
		t.Fatalf("generation %q", got)
	}
	if string(pin.Configuration) != `{"max_sections": 4}` {
		t.Fatalf("configuration %s", pin.Configuration)
	}
}

func TestLoadPinRefusesInvalidPins(t *testing.T) {
	good := writePinManifest(t, pinManifest)
	incompatible := writePinManifest(t, strings.Replace(pinManifest, `plugin_api: ">=0.1.0 <0.2.0"`, `plugin_api: ">=9.0.0 <10.0.0"`, 1))
	oldEngine := writePinManifest(t, strings.Replace(pinManifest, `engine: ">=0.1.0 <0.2.0"`, `engine: ">=1.0.0"`, 1))
	foreign := writePinManifest(t, pinManifest+"extensions:\n  other.outline:\n    \"1\": {type: object}\n")
	clash := writePinManifest(t, strings.Replace(pinManifest, "id: acme.markdown", "id: example", 1)+"extensions:\n  example.editorial:\n    \"1\": {type: object}\n")
	for name, tc := range map[string]struct {
		config plugins.PinConfig
		want   string
	}{
		"missing manifest": {pinConfig(filepath.Join(t.TempDir(), "absent.yaml"), nil), plugins.CodeUnreadable},
		"plugin api range": {pinConfig(incompatible, nil), plugins.CodeIncompatiblePluginAPI},
		"engine range":     {pinConfig(oldEngine, nil), plugins.CodeIncompatibleEngine},
		"config schema":    {pinConfig(good, func(c *plugins.PinConfig) { c.Configuration = json.RawMessage(`{"max_sections": 0}`) }), plugins.CodeInvalidConfiguration},
		"unknown config":   {pinConfig(good, func(c *plugins.PinConfig) { c.Configuration = json.RawMessage(`{"other": 1}`) }), plugins.CodeInvalidConfiguration},
		"endpoint":         {pinConfig(good, func(c *plugins.PinConfig) { c.Endpoint = "unix:///tmp/plugin" }), plugins.CodeInvalidPin},
		"no routes":        {pinConfig(good, func(c *plugins.PinConfig) { c.Routes = nil }), plugins.CodeInvalidPin},
		"undeclared route": {pinConfig(good, func(c *plugins.PinConfig) { c.Routes = []plugins.RouteConfig{{MediaType: "image/png"}} }), plugins.CodeRouteConflict},
		"duplicate route": {pinConfig(good, func(c *plugins.PinConfig) {
			c.Routes = append(c.Routes, plugins.RouteConfig{MediaType: "text/markdown"})
		}), plugins.CodeRouteConflict},
		"optional non-text route": {pinConfig(good, func(c *plugins.PinConfig) {
			c.Routes = []plugins.RouteConfig{{MediaType: "application/pdf", Mode: "optional"}}
		}), plugins.CodeInvalidPin},
		"unknown route mode": {pinConfig(good, func(c *plugins.PinConfig) { c.Routes[0].Mode = "sometimes" }), plugins.CodeInvalidPin},
		"foreign namespace":  {pinConfig(foreign, nil), plugins.CodeForeignNamespace},
		"built-in namespace": {pinConfig(clash, nil), plugins.CodeNamespaceConflict},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := plugins.LoadPin(tc.config)
			if err == nil {
				t.Fatal("invalid pin accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %s", err, tc.want)
			}
		})
	}
}

func TestLoadPinAcceptsOptionalTextRoutes(t *testing.T) {
	path := writePinManifest(t, pinManifest)
	pin, err := plugins.LoadPin(pinConfig(path, func(c *plugins.PinConfig) {
		c.Routes = []plugins.RouteConfig{{MediaType: "text/markdown", Mode: "optional"}, {MediaType: "application/pdf"}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if route, _ := pin.Route("text/markdown"); route.Mode != plugins.RouteOptional {
		t.Fatalf("route %+v", route)
	}
	if route, _ := pin.Route("application/pdf"); route.Mode != plugins.RouteRequired {
		t.Fatalf("route %+v", route)
	}
}

func TestLoadPinDefaultsConfigurationToAnEmptyObject(t *testing.T) {
	path := writePinManifest(t, pinManifest)
	pin, err := plugins.LoadPin(pinConfig(path, func(c *plugins.PinConfig) { c.Configuration = nil }))
	if err != nil {
		t.Fatal(err)
	}
	if string(pin.Configuration) != `{}` {
		t.Fatalf("configuration %s", pin.Configuration)
	}
}

// The pinned plugin's declared namespaces are registered beside the built-in
// ones: clients may no longer write them, and retrieval mappings may.
func TestPinRegistersItsExtensionNamespaces(t *testing.T) {
	path := writePinManifest(t, pinManifest+"extensions:\n  acme.markdown.outline:\n    \"1\": {type: object}\n")
	pin, err := plugins.LoadPin(pinConfig(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := plugins.ExtensionRegistry(pin)
	if err != nil {
		t.Fatal(err)
	}
	if owner, ok := registry.Owner("acme.markdown.outline"); !ok || owner != "acme.markdown" {
		t.Fatalf("owner %q %t", owner, ok)
	}
	if !registry.Declared("example.editorial") || !registry.Declared("acme.markdown.outline") {
		t.Fatal("built-in or plugin namespace not declared")
	}
	none, err := plugins.ExtensionRegistry(nil)
	if err != nil || none.Declared("acme.markdown.outline") || !none.Declared("example.editorial") {
		t.Fatalf("without a pin: %v", err)
	}
}

const kindsManifest = `id: acme.alerts
version: 0.2.0
compatibility:
  engine: ">=0.1.0 <0.2.0"
  plugin_api: ">=0.2.0 <0.3.0"
contributions:
  subscription:
    expression_schema:
      oneOf:
        - {type: object, required: [kind], properties: {kind: {const: keywords}}}
        - {type: object, required: [kind], properties: {kind: {const: described}}}
`

// An installation may offer a subset of the declared alert kinds; the core
// then refuses the others when a Subscription pins them.
func TestPinOffersTheConfiguredKinds(t *testing.T) {
	path := writePinManifest(t, kindsManifest)
	load := func(kinds []string) (*plugins.Pin, error) {
		return plugins.LoadPin(plugins.PinConfig{Manifest: path, Endpoint: "http://127.0.0.1:9901", Kinds: kinds})
	}
	all, err := load(nil)
	if err != nil {
		t.Fatal(err)
	}
	some, err := load([]string{"keywords"})
	if err != nil {
		t.Fatal(err)
	}
	keywords, described := map[string]any{"kind": "keywords"}, map[string]any{"kind": "described"}
	if !all.Offers(keywords) || !all.Offers(described) || !some.Offers(keywords) || some.Offers(described) || some.Offers(map[string]any{}) {
		t.Fatal("offered kinds do not follow the pin")
	}
	for name, kinds := range map[string][]string{"empty": {}, "undeclared": {"vectors"}, "duplicate": {"keywords", "keywords"}} {
		t.Run(name, func(t *testing.T) {
			if _, err := load(kinds); err == nil || !strings.Contains(err.Error(), plugins.CodeInvalidPin) || !strings.Contains(err.Error(), "/kinds") {
				t.Fatalf("kinds %v: %v", kinds, err)
			}
		})
	}
	// A normalizer, or a rule whose schema has no kinds, cannot restrict kinds.
	if _, err := plugins.LoadPin(pinConfig(writePinManifest(t, pinManifest), func(c *plugins.PinConfig) { c.Kinds = []string{"keywords"} })); err == nil || !strings.Contains(err.Error(), "/kinds") {
		t.Fatalf("kinds on a normalizer: %v", err)
	}
}
