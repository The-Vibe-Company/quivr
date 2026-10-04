package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Stale fixture settings must fail before dependency access rather than
// silently enabling or ignoring an engine-owned test path.
func TestRemovedFixtureSettingsRefuseStartup(t *testing.T) {
	for _, key := range []string{"connector_fixtures", "monitoring_fixture_evaluator"} {
		t.Run(key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(`{"`+key+`":true}`), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(ConfigEnv, path)
			for _, command := range []string{"api", "worker", "migrate"} {
				if err := Run(command); err == nil || !strings.Contains(err.Error(), `unknown field "`+key+`"`) {
					t.Fatalf("%s: expected removed field refusal, got %v", command, err)
				}
			}
		})
	}
}

// The existing operator allowances remain usable, but startup identifies the
// disabled guards and their risks. A later invalid duration avoids live I/O.
func TestOperatorAllowancesWarnAtStartup(t *testing.T) {
	for _, command := range []string{"api", "worker", "migrate"} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", command, enabled), func(t *testing.T) {
				var logs bytes.Buffer
				earlier := slog.Default()
				slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
				t.Cleanup(func() { slog.SetDefault(earlier) })
				cfg := Config{DatabaseURL: "postgres://127.0.0.1:1/unused", CursorKey: strings.Repeat("c", 32), CredentialKey: strings.Repeat("s", 32),
					Keys: map[string]corpus.Scope{strings.Repeat("k", 32): {Organization: "org_a", Actions: []string{"content:read"}, Corpora: []string{"*"}}}, ProjectionPurgeGrace: "invalid"}
				cfg.Delivery.AllowPrivateDestinations = enabled
				cfg.ChangePrune.AllowShortRetention = enabled
				cfg.ChangePrune.Organizations = []string{"org_a"}
				path := filepath.Join(t.TempDir(), "config.json")
				raw, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv(ConfigEnv, path)
				if err := Run(command); err == nil || !strings.Contains(err.Error(), "projection_purge_grace") {
					t.Fatalf("did not reach later config validation: %v", err)
				}
				for _, message := range []string{"delivery.allow_private_destinations", "SSRF", "change_prune.allow_short_retention", "data loss"} {
					if strings.Contains(logs.String(), message) != enabled {
						t.Fatalf("enabled=%t, expected warning %q: %s", enabled, message, logs.String())
					}
				}
				if enabled && strings.Count(logs.String(), `"level":"WARN"`) != 2 {
					t.Fatalf("expected two WARN records: %s", logs.String())
				}
			})
		}
	}
}
