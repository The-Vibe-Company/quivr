package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/buildinfo"
)

// Startup failures occur before config loading, so the configured process
// identity must already be present at this executable boundary.
func TestBootstrapHonorsEnvironmentIdentityWithInvalidLevel(t *testing.T) {
	previousVersion := buildinfo.Version
	buildinfo.Version = "2.0.0-alpha.7"
	t.Cleanup(func() { buildinfo.Version = previousVersion })
	t.Setenv("QUIVR_INSTANCE", "replica-7")
	t.Setenv("QUIVR_ENVIRONMENT", "staging")
	t.Setenv("QUIVR_LOG_LEVEL", "invalid-credential-sentinel")
	output, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	original := os.Stdout
	os.Stdout = output
	logger := bootstrapLogger()
	os.Stdout = original
	logger.Error("startup refused")
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(output)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("invalid-credential-sentinel")) {
		t.Fatal("bootstrap exposed invalid level")
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record["instance"] != "replica-7" || record["environment"] != "staging" || record["version"] != "2.0.0-alpha.7" {
		t.Fatalf("bootstrap identity: %v", record)
	}
}

// This executable boundary owns configuration refusal diagnostics, including
// the nonzero exit and the bootstrap/configured logger handoff. No services run.
func TestConfigurationRefusalDiagnostics(t *testing.T) {
	if os.Getenv("QUIVR_TEST_STARTUP") == "1" {
		os.Args = []string{"quivr", "migrate"}
		main()
		return
	}
	const required = `"database_url":"postgres://localhost/db?sslmode=disable","cursor_key":"01234567890123456789012345678901","keys":{"01234567890123456789012345678901":{"organization":"example","actions":["corpora:read"],"corpora":["*"]}}`
	for _, tc := range []struct{ name, raw, code, field string }{
		{"missing", `{}`, "config_missing", "database_url"},
		{"missing-config-env", "", "config_missing", "QUIVR_CONFIG"},
		{"missing-config-file", "", "config_invalid", "QUIVR_CONFIG"},
		{"malformed-json", `{"log_level":"secret-value-sentinel"`, "config_invalid", "QUIVR_CONFIG"},
		{"invalid", `{"log_level":"secret-value-sentinel"}`, "config_invalid", "log_level"},
		{"conflict", `{"weaviate_url":"http://localhost:1","tls":{"weaviate":{"enabled":true}}}`, "config_conflict", "tls.weaviate"},
		{"wrong-type", `{"log_level":42}`, "config_invalid", "log_level"},
		{"nested-type", `{"s3":{"secret_key":42}}`, "config_invalid", "s3.secret_key"},
		{"unknown-key", `{"secret-value-sentinel":true}`, "config_invalid", "QUIVR_CONFIG"},
		{"code-matches-secret", `{"database_url":"postgres://config:secret-value-sentinel@localhost/db","weaviate_url":"http://localhost:1","tls":{"weaviate":{"enabled":true}}}`, "config_conflict", "tls.weaviate"},
		{"field-matches-secret", `{"database_url":"postgres://weaviate:secret-value-sentinel@localhost/db","weaviate_url":"http://localhost:1","tls":{"weaviate":{"enabled":true}}}`, "config_conflict", "tls.weaviate"},
		{"tls-disabled", `{"tls":{"temporal":{"enabled":false,"ca_file":"secret-value-sentinel"}}}`, "config_conflict", "tls.temporal"},
		{"tls-default-disabled", `{"tls":{"temporal":{"ca_file":"secret-value-sentinel"}}}`, "config_conflict", "tls.temporal"},
		{"tls-certificate", `{"tls":{"temporal":{"enabled":true,"ca_file":"/missing/secret-value-sentinel"}}}`, "config_invalid", "tls.temporal"},
		{"missing-keys", `{"database_url":"postgres://localhost/db","cursor_key":"01234567890123456789012345678901"}`, "config_missing", "keys"},
		{"retry-conflict", `{` + required + `,"delivery":{"retry_initial":"2s","retry_max":"1s"}}`, "config_conflict", "delivery.retry_initial"},
		{"prune-conflict", `{` + required + `,"change_retention":"24h","change_prune":{"retention":"1h"}}`, "config_conflict", "change_prune.retention"},
		{"nested-duration", `{` + required + `,"delivery":{"timeout":"secret-value-sentinel"}}`, "config_invalid", "delivery.timeout"},
		{"event-matches-secret", `{"database_url":"postgres://quivr:secret-value-sentinel@localhost/db","cursor_key":"secret-value-sentinel"}`, "config_invalid", "cursor_key"},
		{"secret", `{"database_url":"postgres://user:secret-value-sentinel@localhost/db","cursor_key":"secret-value-sentinel"}`, "config_invalid", "cursor_key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.json")
			if tc.raw != "" {
				if err := os.WriteFile(file, []byte(tc.raw), 0600); err != nil {
					t.Fatal(err)
				}
			} else if tc.name == "missing-config-env" {
				file = ""
			} else {
				file = filepath.Join(filepath.Dir(file), "secret-value-sentinel.json")
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestConfigurationRefusalDiagnostics$")
			// Keep inherited credentials and logging overrides out of the child.
			cmd.Env = []string{"QUIVR_TEST_STARTUP=1", "QUIVR_CONFIG=" + file, "TZ=Etc/GMT-2"}
			output, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
				t.Fatalf("exit = %v, want 1; output: %s", err, output)
			}
			if bytes.Contains(output, []byte("secret-value-sentinel")) {
				t.Fatal("configuration value leaked")
			}
			var failure map[string]any
			for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
				var record map[string]any
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatalf("invalid JSON log: %s", line)
				}
				if ts, ok := record["ts"].(string); !ok || !strings.HasSuffix(ts, "Z") {
					t.Fatalf("timestamp = %v, want UTC Z", record["ts"])
				}
				if record["event"] == "quivr.failed" {
					failure = record
				}
			}
			if problem, ok := failure["problem"].(string); !ok || problem == "" {
				t.Fatalf("missing safe problem: %v", failure)
			}
			if failure["error_code"] != tc.code || failure["field"] != tc.field {
				t.Fatalf("failure = %v, want code %s field %s", failure, tc.code, tc.field)
			}
		})
	}
}

// Bare storage usage belongs to the executable boundary and must work without
// a deployment configuration or database.
func TestStorageUsageDoesNotLoadConfiguration(t *testing.T) {
	if os.Getenv("QUIVR_TEST_USAGE") == "1" {
		os.Args = []string{"quivr", "storage"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStorageUsageDoesNotLoadConfiguration$")
	cmd.Env = []string{"QUIVR_TEST_USAGE=1"}
	output, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		t.Fatalf("exit = %v, want usage exit 2; output: %s", err, output)
	}
	if !bytes.Contains(output, []byte("usage: quivr")) || bytes.Contains(output, []byte("config_missing")) {
		t.Fatalf("expected storage usage without configuration refusal: %s", output)
	}
}
