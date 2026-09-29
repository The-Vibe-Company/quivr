package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An invalid pin refuses api and worker startup before any dependency is used,
// with an actionable error naming the issue: incompatible ranges, extension
// namespaces not prefixed by the plugin id, and namespaces clashing with a
// built-in one.
func TestInvalidPluginPinRefusesStartup(t *testing.T) {
	const compatible = "compatibility:\n  engine: \">=0.1.0 <0.2.0\"\n  plugin_api: \">=0.1.0 <0.2.0\"\ncontributions:\n  normalizer:\n    media_types: [text/markdown]\n"
	for name, tc := range map[string]struct{ body, code string }{
		"plugin api range":   {"id: acme.markdown\nversion: 1.0.0\n" + strings.Replace(compatible, "plugin_api: \">=0.1.0 <0.2.0\"", "plugin_api: \">=0.9.0 <1.0.0\"", 1), "incompatible_plugin_api"},
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
