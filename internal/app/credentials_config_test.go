package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

const (
	testCursorKey     = "test-cursor-key-0123456789abcdef-not-a-secret"
	testCredentialKey = "test-credential-key-0123456789abcdef-not-a-secret"
)

func TestCredentialKeyIsOptionalButNeverShort(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	sealer, err := Config{CursorKey: testCursorKey, CredentialKey: testCredentialKey}.connectorSealer(logger)
	if err != nil || !sealer.CanSeal() {
		t.Fatalf("keyed: %v can seal %v", err, sealer.CanSeal())
	}
	if logs.Len() != 0 {
		t.Fatalf("keyed deployment logged %q", logs.String())
	}

	sealer, err = Config{CursorKey: testCursorKey}.connectorSealer(logger)
	if err != nil || sealer.CanSeal() {
		t.Fatalf("keyless: %v can seal %v", err, sealer.CanSeal())
	}
	if _, err = sealer.Digest("create", []byte("{}")); err != nil {
		t.Fatalf("keyless digest: %v", err)
	}
	if n := strings.Count(logs.String(), "credential deposits disabled"); n != 1 {
		t.Fatalf("keyless startup logged %d times: %q", n, logs.String())
	}

	if _, err = (Config{CursorKey: testCursorKey, CredentialKey: "too-short"}).connectorSealer(logger); err == nil {
		t.Fatal("accepted a configured credential_key shorter than 32 bytes")
	}
}
