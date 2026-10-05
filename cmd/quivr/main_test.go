package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"
)

// Startup failures occur before config loading, so the configured process
// identity must already be present at this executable boundary.
func TestBootstrapHonorsEnvironmentIdentityWithInvalidLevel(t *testing.T) {
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
	if record["instance"] != "replica-7" || record["environment"] != "staging" {
		t.Fatalf("bootstrap identity: %v", record)
	}
}
