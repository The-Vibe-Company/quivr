package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSecretBindings(t *testing.T) {
	const apiToken = "fixture-bearer-key-with-sufficient-length"
	const db = "postgres://user:fixture-password@db/quivr?sslmode=disable"
	const cursor = "fixture-cursor-secret-with-sufficient-length"
	const access = "fixture-s3-user"
	const secret = "fixture-s3-password"
	const credential = "fixture-encryption-key-at-least-32-bytes"
	for name, value := range map[string]string{
		"DATABASE_URL":     db,
		"QUIVR_CURSOR_KEY": cursor,
		"S3_SECRET_KEY":    secret,
		"QUIVR_API_KEYS":   `{"` + apiToken + `":{"organization":"example","actions":["corpora:read"],"corpora":["*"]}}`,
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "value")
		if err := os.WriteFile(path, []byte(value+"\r\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(name, "")
		t.Setenv(name+"_FILE", path)
	}
	for name, value := range map[string]string{
		"S3_ACCESS_KEY": access, "QUIVR_CREDENTIAL_KEY": credential, "WEAVIATE_API_KEY": "",
		"QUIVR_TELEMETRY_HEADERS": `{"authorization":"fixture-collector-token"}`,
	} {
		t.Setenv(name, value)
		t.Setenv(name+"_FILE", "")
	}
	var cfg Config
	cfg.WeaviateAPIKey = "legacy-index-key"
	cfg.DatabaseURL = "postgres://legacy-db"
	if err := cfg.loadSecrets(false); err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != db || cfg.CursorKey != cursor || cfg.S3.AccessKey != access ||
		cfg.S3.SecretKey != secret || cfg.CredentialKey != credential ||
		cfg.WeaviateAPIKey != "legacy-index-key" ||
		cfg.Telemetry.Headers["authorization"] != "fixture-collector-token" {
		t.Fatal("resolved credentials differ from their dedicated bindings or legacy fallback")
	}
	scope, ok := cfg.Keys[apiToken]
	if !ok || len(cfg.Keys) != 1 || !scope.Allows("corpora:read") || scope.Organization != "example" {
		t.Fatal("API secret binding did not preserve the bearer token's scope")
	}
}
