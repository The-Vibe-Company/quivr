package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestNewEmitsJSONWithIdentityAndConfiguredLevel(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(&output, Options{
		Level:       "debug",
		Service:     "engine",
		Version:     "v-test",
		Instance:    "instance-test",
		Environment: "test",
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	logger.Debug("debug event")
	logger.With("version", "plugin-version").Info("info event", "service", "plugin", "level", "overridden")
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d JSON records, want 2: %q", len(lines), output.String())
	}
	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("record %d is not JSON: %v", i, err)
		}
		for _, key := range []string{"ts", "level", "msg", "service", "version", "instance", "environment"} {
			if _, ok := record[key]; !ok {
				t.Fatalf("record %d missing %q: %s", i, key, line)
			}
		}
		if _, ok := record["time"]; ok {
			t.Fatalf("record %d uses time instead of ts: %s", i, line)
		}
		if got, want := record["service"], "engine"; got != want {
			t.Fatalf("record %d service = %v, want %v", i, got, want)
		}
		if got, want := record["version"], "v-test"; got != want {
			t.Fatalf("record %d version = %v, want %v", i, got, want)
		}
		if got, want := record["instance"], "instance-test"; got != want {
			t.Fatalf("record %d instance = %v, want %v", i, got, want)
		}
		if got, want := record["environment"], "test"; got != want {
			t.Fatalf("record %d environment = %v, want %v", i, got, want)
		}
		wantLevel := "DEBUG"
		if i == 1 {
			wantLevel = "INFO"
		}
		if got := record["level"]; got != wantLevel {
			t.Fatalf("record %d level = %v, want %s", i, got, wantLevel)
		}
	}
}

func TestNewDefaultsIdentityAndInfoLevel(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(&output, Options{})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	logger.Debug("hidden")
	logger.Info("visible")

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d JSON records, want one info record: %q", len(lines), output.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("record is not JSON: %v", err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	wantInstance := host + ":" + strconv.Itoa(os.Getpid())
	for key, want := range map[string]string{
		"service":     "quivr",
		"version":     "unspecified",
		"instance":    wantInstance,
		"environment": "unspecified",
	} {
		if got := record[key]; got != want {
			t.Fatalf("%s = %v, want %q", key, got, want)
		}
	}
}

func TestNewRejectsUnknownLevel(t *testing.T) {
	var output bytes.Buffer
	if _, err := New(&output, Options{Level: "verbose-secret"}); err == nil {
		t.Fatal("New accepted an unknown level")
	}
	if output.Len() != 0 {
		t.Fatalf("New wrote output while rejecting level: %q", output.String())
	}
}

func TestContextRequestIDIsAttachedToInfoContextAndLogAttrs(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(&output, Options{Service: "quivr", Version: "test"})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	ctx := WithRequestID(context.Background(), "request-123")
	if got := RequestID(ctx); got != "request-123" {
		t.Fatalf("RequestID = %q, want request-123", got)
	}
	logger.InfoContext(ctx, "context event")
	logger.LogAttrs(ctx, 0, "attrs event")
	logger.InfoContext(ctx, "explicit request ID is overridden", "request_id", "wrong-request")
	logger.WithGroup("worker").With("task", "running").InfoContext(ctx, "grouped event")

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d JSON records, want 4: %q", len(lines), output.String())
	}
	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("record %d is not JSON: %v", i, err)
		}
		if got, want := record["request_id"], "request-123"; got != want {
			t.Fatalf("record %d request_id = %v, want %v", i, got, want)
		}
		if count := strings.Count(line, `"request_id"`); count != 1 {
			t.Fatalf("record %d contains %d request_id fields, want one: %s", i, count, line)
		}
	}
}

func TestLoggerRedactsSecretsSensitiveFieldsErrorsAndArbitraryAny(t *testing.T) {
	const secret = "sentinel-credential-123"
	const unknownSecret = "unknown-password-value"
	const errorText = "upstream failed with password=unknown-password-value"
	var output bytes.Buffer
	logger, err := New(&output, Options{Version: "test", Secrets: []string{secret}})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	logger.With(
		"with_secret", secret,
		"configuration", map[string]any{"nested": secret, "other": unknownSecret},
	).Info(
		"request failed "+secret+" https://user:pass@example.test/path?password="+secret,
		"message_secret", secret,
		"provider_detail", "Authorization: Bearer "+unknownSecret+" trailing-credential-value",
		"authorization", "Bearer "+unknownSecret,
		"apiKey", "unknown-camel-key",
		"privateKey", "unknown-private-key",
		"cookie_detail", "Cookie: session=unknown-cookie-token; alternate=unknown-cookie-second",
		"python_detail", `{'password':'unknown-single-quoted'}`,
		"prefixed_detail", `{"access_token":"unknown-access-token","dbPassword":"unknown-db-password","privateKey":"unknown-json-private-key"}`,
		"serialized_detail", `{"password":"json-password-value","apiKey":"json-api-key","client_secret":"escaped-\"secret-value"}`,
		"nested", map[string]any{"safe-looking": unknownSecret},
		"group", groupWithSecret(secret),
		"error", errors.New(errorText),
		"errors", []error{errors.New("second error " + secret)},
		"cause", errors.New("cause contains "+secret),
		"api_key_id", "opaque-fingerprint",
		"credential_deposits", true,
	)

	text := output.String()
	for _, forbidden := range []string{
		secret,
		unknownSecret,
		"trailing-credential-value",
		"unknown-cookie-token", "unknown-cookie-second", "unknown-single-quoted",
		"unknown-access-token", "unknown-db-password", "unknown-json-private-key",
		"unknown-camel-key", "unknown-private-key", "json-password-value", "json-api-key", "secret-value",
		"user:pass@example.test",
		"?password=",
		errorText,
		"second error",
		"cause contains",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("log output contains %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "\"msg\":\"request failed [REDACTED]") {
		t.Fatalf("message was not visibly redacted: %s", text)
	}
	for _, safe := range []string{`"api_key_id":"opaque-fingerprint"`, `"credential_deposits":true`} {
		if !strings.Contains(text, safe) {
			t.Fatalf("safe metadata %q was redacted: %s", safe, text)
		}
	}
}

func groupWithSecret(secret string) any {
	return slog.GroupValue(slog.Group("inner", slog.String("secret_value", secret)))
}
