package app

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

// Owns operator setting precedence and validation. Silent fallback on an invalid
// setting would start a process with the wrong verbosity or drain budget.
func TestProcessSettingsRespectEnvironmentAndRejectInvalidDurations(t *testing.T) {
	t.Setenv("QUIVR_LOG_LEVEL", "debug")
	t.Setenv("QUIVR_SHUTDOWN_GRACE", "12s")
	cfg := Config{LogLevel: "error", ShutdownGrace: "3s"}
	level, grace, err := cfg.processSettings()
	if err != nil || level != "debug" || grace != 12*time.Second {
		t.Fatalf("env precedence: level=%s grace=%s error=%v", level, grace, err)
	}
	t.Setenv("QUIVR_LOG_LEVEL", "")
	t.Setenv("QUIVR_SHUTDOWN_GRACE", "")
	level, grace, err = (Config{}).processSettings()
	if err != nil || level != "info" || grace != time.Minute {
		t.Fatalf("defaults: level=%s grace=%s error=%v", level, grace, err)
	}
	for _, value := range []string{"0s", "-1s", "invalid-secret-value"} {
		if _, _, err := (Config{ShutdownGrace: value}).processSettings(); err == nil {
			t.Fatalf("accepted invalid shutdown_grace %q", value)
		}
	}
}

// Owns real process stdout/file setup and safe startup diagnostics, beyond the
// logger's generic redaction tests: configuration supplies the secret inventory.
func TestProcessLogsKeepConfigurationCredentialsOutOfBothSinks(t *testing.T) {
	previousLogger, previousStdout := slog.Default(), os.Stdout
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = stdout
	t.Cleanup(func() { os.Stdout = previousStdout; slog.SetDefault(previousLogger); stdout.Close() })
	t.Setenv("QUIVR_LOG_LEVEL", "")
	t.Setenv("QUIVR_SHUTDOWN_GRACE", "")
	t.Setenv("EXAMPLE_WEBHOOK_SECRET", "sentinel-env-secret")
	const signingSecret = "c2lnbmluZy1zZWNyZXQtc2VudGluZWwtMzItYnl0ZXM"
	t.Setenv(plugins.EnvSigningKeys, `{"malformed-plugin":{"keys":"invalid"},"example-plugin":{"active":"primary","keys":[{"id":"primary","secret":"`+signingSecret+`"}]}}`)
	cfg := Config{LogDirectory: t.TempDir(), CursorKey: "sentinel-cursor-secret", CredentialKey: "sentinel-credential-secret",
		DatabaseURL: "postgres://user:sentinel-db-secret@localhost/db", Keys: map[string]corpus.Scope{"sentinel-bearer-secret": {}},
		Destinations: map[string]monitoring.Destination{"receiver": {SecretEnv: "EXAMPLE_WEBHOOK_SECRET"}},
		Plugin:       &plugins.PinConfig{Configuration: []byte(`{"settings":{"key":"sentinel-plugin-secret"}}`)}}
	cfg.S3.AccessKey, cfg.S3.SecretKey = "sentinel-access-secret", "sentinel-storage-secret"
	grace, err := cfg.configureProcess("worker")
	if err != nil {
		t.Fatal(err)
	}
	slog.Info("process starting", "event", "quivr.start", cfg.processSummary(grace))
	slog.Info("provider metadata", "detail", "sentinel-plugin-secret")
	slog.Info("signer metadata", "detail", signingSecret)
	LogFailure(errors.New("sentinel-db-secret sentinel-env-secret"))
	LogFailure(&plugins.PinError{Path: "sentinel-storage-secret", Issues: []plugins.Issue{{Code: plugins.CodeInvalidConfiguration, Message: "sentinel-bearer-secret"}}})
	LogFailure(&plugins.PinError{Path: "sentinel-storage-secret", Issues: []plugins.Issue{{Code: plugins.CodeNamespaceConflict, Message: "sentinel-bearer-secret"}}})
	fileLog, err := os.ReadFile(filepath.Join(cfg.LogDirectory, "worker.log"))
	if err != nil {
		t.Fatal(err)
	}
	stdoutLog, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fileLog, stdoutLog) || len(fileLog) == 0 {
		t.Fatal("stdout and opt-in file must contain identical JSON events")
	}
	for _, secret := range []string{signingSecret, "sentinel-cursor-secret", "sentinel-credential-secret", "sentinel-db-secret", "sentinel-bearer-secret", "sentinel-env-secret", "sentinel-plugin-secret", "sentinel-access-secret", "sentinel-storage-secret"} {
		if bytes.Contains(fileLog, []byte(secret)) {
			t.Fatalf("process log leaked %q", secret)
		}
	}
	if !bytes.Contains(fileLog, []byte(`"error_code":"invalid_configuration"`)) || !bytes.Contains(fileLog, []byte(`"error_code":"namespace_conflict"`)) || !bytes.Contains(fileLog, []byte(`"shutdown_grace":60000000000`)) {
		t.Fatalf("safe effective settings and issue codes missing: %s", fileLog)
	}
}
