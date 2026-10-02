package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// An invalid pin refuses api and worker startup before any dependency is used,
// with an actionable error naming the issue: incompatible ranges, extension
// namespaces not prefixed by the plugin id, and namespaces clashing with a
// built-in one.
func TestInvalidPluginPinRefusesStartup(t *testing.T) {
	const compatible = "compatibility:\n  engine: \">=0.1.0 <0.2.0\"\n  plugin_api: \">=0.1.0 <0.2.0\"\ncontributions:\n  normalizer:\n    media_types: [text/markdown]\n"
	for name, tc := range map[string]struct{ body, code string }{
		"plugin api range":   {"id: acme.markdown\nversion: 1.0.0\n" + strings.Replace(compatible, "plugin_api: \">=0.1.0 <0.2.0\"", "plugin_api: \">=999.0.0 <1000.0.0\"", 1), "incompatible_plugin_api"},
		"foreign namespace":  {"id: acme.markdown\nversion: 1.0.0\n" + compatible + "extensions:\n  other.outline:\n    \"1\": {type: object}\n", "foreign_namespace"},
		"built-in namespace": {"id: example\nversion: 1.0.0\n" + compatible + "extensions:\n  example.editorial:\n    \"1\": {type: object}\n", "namespace_conflict"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			manifest := filepath.Join(dir, "quivr-plugin.yaml")
			if err := os.WriteFile(manifest, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(dir, "config.json")
			raw, _ := json.Marshal(map[string]any{"plugin": map[string]any{"manifest": manifest, "endpoint": "http://127.0.0.1:1", "routes": []any{map[string]any{"media_type": "text/markdown"}}}})
			if err := os.WriteFile(config, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("QUIVR_CONFIG", config)
			for _, command := range []string{"api", "worker"} {
				err := Run(command)
				if err == nil || !strings.Contains(err.Error(), tc.code) {
					t.Fatalf("%s started with an invalid pin: %v", command, err)
				}
			}
		})
	}
}

// Several plugins are pinned together through `plugins`, beside the single
// `plugin` form; a conflict between pins refuses api and worker startup.
func TestConflictingPluginPinsRefuseStartup(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	const normalizer = "compatibility:\n  engine: \">=0.1.0 <0.2.0\"\n  plugin_api: \">=0.1.0 <0.2.0\"\ncontributions:\n  normalizer:\n    media_types: [text/markdown]\n"
	markdown := write("markdown.yaml", "id: acme.markdown\nversion: 1.0.0\n"+normalizer)
	other := write("other.yaml", "id: acme.other\nversion: 1.0.0\n"+normalizer)
	route := []any{map[string]any{"media_type": "text/markdown"}}
	pin := func(manifest string) map[string]any {
		return map[string]any{"manifest": manifest, "endpoint": "http://127.0.0.1:1", "routes": route}
	}
	for name, tc := range map[string]struct {
		config map[string]any
		code   string
	}{
		"same plugin twice":  {map[string]any{"plugin": pin(markdown), "plugins": []any{pin(markdown)}}, "plugin_conflict"},
		"media type twice":   {map[string]any{"plugins": []any{pin(markdown), pin(other)}}, "route_conflict"},
		"invalid second pin": {map[string]any{"plugins": []any{pin(markdown), map[string]any{"manifest": other, "endpoint": "ftp://x", "routes": route}}}, "invalid_pin"},
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.config)
			t.Setenv("QUIVR_CONFIG", write("config.json", string(raw)))
			for _, command := range []string{"api", "worker"} {
				if err := Run(command); err == nil || !strings.Contains(err.Error(), tc.code) {
					t.Fatalf("%s started with conflicting pins: %v", command, err)
				}
			}
		})
	}
}

// Each connector kind resolves to one provider: a pinned plugin declaring a
// kind another pin provides refuses api and worker startup, naming both.
func TestAConnectorKindWithTwoProvidersRefusesStartup(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "quivr-plugin.yaml")
	body := "id: acme.mail\nversion: 1.0.0\ncompatibility:\n  engine: \">=0.1.0 <0.2.0\"\n  plugin_api: \">=0.3.0 <0.4.0\"\ncontributions:\n  connector:\n    kinds:\n      fixture:\n        config_schema: {type: object}\n        default_interval_seconds: 900\n        modes: [pull]\n"
	if err := os.WriteFile(manifest, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.yaml"), []byte(strings.Replace(body, "acme.mail", "acme.other", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"database_url": "postgres://127.0.0.1:1/unused", "cursor_key": strings.Repeat("c", 32),
		"keys":    map[string]any{strings.Repeat("k", 32): map[string]any{"organization": "org_a", "actions": []string{"connectors:read"}, "corpora": []string{"*"}}},
		"plugins": []any{map[string]any{"manifest": manifest, "endpoint": "http://127.0.0.1:1"}, map[string]any{"manifest": filepath.Join(dir, "other.yaml"), "endpoint": "http://127.0.0.1:2"}}})
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUIVR_CONFIG", config)
	for _, command := range []string{"api", "worker"} {
		err := Run(command)
		if err == nil || !strings.Contains(err.Error(), `connector kind "fixture"`) || !strings.Contains(err.Error(), "kind_conflict") || !strings.Contains(err.Error(), "acme.mail@1.0.0") {
			t.Fatalf("%s started with a kind provided twice: %v", command, err)
		}
	}
}

// The m365_mail kind moved to the connector.m365_mail plugin: a deployment
// that still configures the engine's m365 block refuses to start and says
// where its endpoints go now.
func TestTheEngineM365BlockRefusesStartup(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte(`{"m365":{"graph_endpoint":"http://127.0.0.1:1/v1.0"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUIVR_CONFIG", config)
	if err := Run("worker"); err == nil || !strings.Contains(err.Error(), "connector.m365_mail plugin") {
		t.Fatalf("started with an m365 block: %v", err)
	}
}

// Search alias configuration is validated before startup opens dependencies;
// this owns the config field wiring, beyond the PinSet's alias validation.
func TestInvalidSearchAliasRefusesStartup(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	raw, _ := json.Marshal(map[string]any{
		"plugins":   []any{map[string]any{"manifest": "../../plugins/core-retrieve/quivr-plugin.yaml", "endpoint": "http://127.0.0.1:1"}},
		"retrieval": map[string]any{"profiles": map[string]string{"default": "core.retrieve/missing"}},
	})
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUIVR_CONFIG", config)
	for _, command := range []string{"api", "worker"} {
		if err := Run(command); err == nil || !strings.Contains(err.Error(), "unknown profile") {
			t.Fatalf("%s must reject the profile before dependencies: %v", command, err)
		}
	}
}

// Migration registers valid ingestion pins even when serving routes are
// invalid; a routing typo must not retire every configured owner's spaces.
func TestMigrationKeepsSpacesWhenRoutingIsInvalid(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ingestion plugins.IngestionRouting
		retrieval RetrievalConfig
	}{
		{name: "unknown search profile", retrieval: RetrievalConfig{Profiles: map[string]string{"default": "core.retrieve/missing"}}},
		{name: "unknown ingestion default", ingestion: plugins.IngestionRouting{Default: "example.missing"}},
		{name: "unknown source route", ingestion: plugins.IngestionRouting{Routes: map[string]string{"application/pdf": "example.missing"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Plugins: []plugins.PinConfig{
				{Manifest: "../../plugins/core-ingest/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:1", Configuration: json.RawMessage(`{"tei_url":"http://127.0.0.1:1","tokenizer":{"python":"python3","model":"/unused/tokenizer.json"}}`)},
				{Manifest: "../../plugins/core-retrieve/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:1"},
				{Manifest: "../../sdks/go/examples/hash-embedder/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:1", Spaces: map[string]string{"example.hash_embedder.small": "served", "example.hash_embedder.large": "evaluation"}},
			}, Ingestion: tc.ingestion, Retrieval: tc.retrieval}
			owners := map[string]int{}
			for _, space := range DeploymentSpaces(cfg.migrationPins()) {
				owners[space.OwnerPluginID]++
			}
			if !reflect.DeepEqual(owners, map[string]int{"core.ingest": 1, "example.hash_embedder": 2}) {
				t.Fatalf("migration registers owners %v; want both configured ingestion owners and all enabled spaces", owners)
			}
		})
	}
}
