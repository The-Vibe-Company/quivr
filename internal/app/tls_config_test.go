package app

import (
	"encoding/json"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Startup owns actionable refusal; no network dependency or valid pins are
// needed to catch ignored TLS fields, invalid files or downgrade settings.
func TestInvalidTLSRefusesStartup(t *testing.T) {
	for _, dep := range []string{"temporal", "weaviate", "postgres", "s3", "plugins"} {
		t.Run(dep, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.json")
			raw, _ := json.Marshal(map[string]any{"tls": map[string]any{dep: map[string]any{"enabled": true, "ca_file": file + ".missing"}}})
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(ConfigEnv, file)
			for _, cmd := range []string{"api", "worker", "migrate"} {
				err := Run(cmd)
				if err == nil || !strings.Contains(err.Error(), dep+" TLS") || !strings.Contains(err.Error(), "ca_file") {
					t.Fatalf("%s error=%v, want %s TLS ca_file error", cmd, err, dep)
				}
			}
		})
	}
}

// Newly registered plugins share the deployment switch: validating only the
// startup pins would allow a later registration to bypass mandatory TLS.
func TestPluginTLSSwitchAppliesToNewEndpoints(t *testing.T) {
	enabled := true
	cfg := Config{TLS: TLSConfig{Plugins: outbound.TLS{Enabled: &enabled}}}
	settings, err := cfg.validateTLS()
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", "http://localhost:1/v0/discovery", nil)
	_, err = settings.plugins.RoundTrip(req)
	if err == nil || !strings.Contains(err.Error(), "plugins TLS") {
		t.Fatalf("plaintext plugin error=%v", err)
	}
}
