package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An invalid pin refuses api and worker startup before any dependency is used,
// with an actionable error naming the issue.
func TestInvalidPluginPinRefusesStartup(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "quivr-plugin.yaml")
	body := "id: acme.markdown\nversion: 1.0.0\ncompatibility:\n  engine: \">=0.1.0 <0.2.0\"\n  plugin_api: \">=0.9.0 <1.0.0\"\ncontributions:\n  normalizer:\n    media_types: [text/markdown]\n"
	if err := os.WriteFile(manifest, []byte(body), 0o600); err != nil {
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
		if err == nil || !strings.Contains(err.Error(), "incompatible_plugin_api") {
			t.Fatalf("%s started with an incompatible pin: %v", command, err)
		}
	}
}
