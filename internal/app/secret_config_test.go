package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadSecretBindings(t *testing.T) {
	const apiToken = "fixture-bearer-key-with-sufficient-length"
	const adminToken = "fixture-admin-key-with-sufficient-length!"
	const db = "postgres://user:fixture-password@db/quivr?sslmode=disable"
	const cursor = "fixture-cursor-secret-with-sufficient-length"
	const access = "fixture-s3-user"
	const secret = "fixture-s3-password"
	const credential = "fixture-encryption-key-at-least-32-bytes"
	for name, value := range map[string]string{
		"DATABASE_URL":     db,
		"QUIVR_CURSOR_KEY": cursor,
		"S3_SECRET_KEY":    secret,
		"QUIVR_API_KEYS":   `{"` + apiToken + `":{"organization":"example","actor_id":"0123456789abcdef0123456789abcdef","visibility_mode":"enforce","actions":["corpora:read"],"corpora":["*"]}}`,
		"QUIVR_ADMIN_KEYS": `{"` + adminToken + `":{"organization":"example","actor_id":"fedcba9876543210fedcba9876543210","actions":["plugins:admin"],"corpora":["*"]}}`,
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
	if !ok || len(cfg.Keys) != 1 || !scope.Allows("corpora:read") || scope.Organization != "example" || scope.VisibilityMode != "enforce" || scope.ActorID != "0123456789abcdef0123456789abcdef" {
		t.Fatal("API secret binding did not preserve the bearer token's scope")
	}
	invalidScope := scope
	invalidScope.VisibilityMode = "enforced"
	cfg.Keys[apiToken] = invalidScope
	var invalid *configError
	if err := cfg.validateAccessKeys(); !errors.As(err, &invalid) || invalid.code != configInvalid || invalid.field != "keys.visibility_mode" {
		t.Fatalf("invalid API key visibility mode: %v", err)
	}
	adminScope, ok := cfg.AdminKeys[adminToken]
	if !ok || len(cfg.AdminKeys) != 1 || !adminScope.Allows("plugins:admin") || adminScope.Organization != "example" || adminScope.VisibilityMode != "" || adminScope.ActorID != "fedcba9876543210fedcba9876543210" {
		t.Fatal("admin secret binding did not preserve the bearer token's scope")
	}
}

func TestRejectSecretBindingsWithoutLeakingInput(t *testing.T) {
	const secret = "fixture-private-value"
	for _, tc := range []struct {
		name, binding, field, inline, file string
		mount, exists                      bool
		code                               configCode
	}{
		{name: "conflict", binding: "DATABASE_URL", field: "database_url", inline: secret, file: secret, mount: true, exists: true, code: configConflict},
		{name: "missing file", binding: "DATABASE_URL", field: "database_url", mount: true, code: configInvalid},
		{name: "empty file", binding: "DATABASE_URL", field: "database_url", mount: true, exists: true, code: configInvalid},
		{name: "NUL in file", binding: "DATABASE_URL", field: "database_url", file: secret + "\x00", mount: true, exists: true, code: configInvalid},
		{name: "malformed JSON", binding: "QUIVR_API_KEYS", field: "keys", inline: `{"` + secret + `":`, code: configInvalid},
		{name: "unknown field", binding: "QUIVR_API_KEYS", field: "keys", inline: `{"fixture":{"` + secret + `":true}}`, code: configInvalid},
		{name: "duplicate tokens", binding: "QUIVR_API_KEYS", field: "keys", inline: `{"` + secret + `":{},"` + secret + `":{}}`, code: configInvalid},
		{name: "case variant scope field", binding: "QUIVR_API_KEYS", field: "keys", inline: `{"fixture":{"organization":"example","Organization":"` + secret + `"}}`, code: configInvalid},
		{name: "admin keys conflict", binding: "QUIVR_ADMIN_KEYS", field: "admin_keys", inline: `{}`, file: `{}`, mount: true, exists: true, code: configConflict},
		{name: "admin keys malformed JSON", binding: "QUIVR_ADMIN_KEYS", field: "admin_keys", inline: `{"` + secret + `":`, code: configInvalid},
		{name: "admin keys unknown field", binding: "QUIVR_ADMIN_KEYS", field: "admin_keys", inline: `{"fixture":{"` + secret + `":true}}`, code: configInvalid},
		{name: "admin keys duplicate tokens", binding: "QUIVR_ADMIN_KEYS", field: "admin_keys", inline: `{"` + secret + `":{},"` + secret + `":{}}`, code: configInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "")
			t.Setenv("DATABASE_URL_FILE", "")
			path := ""
			if tc.mount {
				path = filepath.Join(t.TempDir(), "fixture-private-path")
			}
			if tc.exists {
				if err := os.WriteFile(path, []byte(tc.file), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(tc.binding, tc.inline)
			t.Setenv(tc.binding+"_FILE", path)
			var cfg Config
			err := cfg.loadSecrets(false)
			var diagnostic *configError
			if !errors.As(err, &diagnostic) || diagnostic.code != tc.code || diagnostic.field != tc.field {
				t.Fatalf("expected a %s diagnostic for %s", tc.code, tc.field)
			}
			if strings.Contains(err.Error(), secret) || (tc.mount && strings.Contains(err.Error(), path)) {
				t.Fatal("diagnostic included the supplied secret or mount path")
			}
		})
	}
}

func TestStartupRequiresCurrentlyValidUnambiguousSigningRings(t *testing.T) {
	const secret = "c3ludGhldGljLXNpZ25pbmcta2V5LXdpdGgtMzItYnl0ZXM"
	const primary = `{"id":"primary","secret":"` + secret + `"`
	const secondary = `{"id":"secondary","secret":"` + secret + `"}`
	now := time.Unix(100, 0)
	for _, tc := range []struct {
		name, active, key string
		valid             bool
	}{
		{"active at start boundary", `"active":"primary"`, primary + `,"not_before":100,"not_after":101}`, true},
		{"expired despite valid secondary", `"active":"primary"`, primary + `,"not_after":100}`, false},
		{"future despite valid secondary", `"active":"primary"`, primary + `,"not_before":101}`, false},
		{"case variant ring field", `"active":"secondary","Active":"primary"`, primary + `}`, false},
		{"case variant key field", `"active":"primary"`, primary + `,"Secret":"` + secret + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("QUIVR_ENGINE_PLUGIN_KEYS_FILE", "")
			t.Setenv("QUIVR_ENGINE_PLUGIN_KEYS", `{"example":{`+tc.active+`,"keys":[`+tc.key+`,`+secondary+`]}}`)
			err := validateEngineSigning(nil, now)
			if tc.valid {
				if err != nil {
					t.Fatal("currently valid ring was refused")
				}
				return
			}
			var diagnostic *configError
			if !errors.As(err, &diagnostic) || diagnostic.code != configInvalid || diagnostic.field != "QUIVR_ENGINE_PLUGIN_KEYS" {
				t.Fatal("startup accepted the ring or returned the wrong diagnostic")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("signing diagnostic included the secret")
			}
		})
	}
}
