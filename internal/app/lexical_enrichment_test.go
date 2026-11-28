package app

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

// Literal deployment keys, omission (default-off), secret redaction, and
// startup validation own opt-in Phase-2 lexical enrichment configuration.
func TestLexicalEnrichmentConfiguration(t *testing.T) {
	t.Run("omitted_is_disabled", func(t *testing.T) {
		var cfg Config
		if err := json.Unmarshal([]byte(`{}`), &cfg); err != nil {
			t.Fatal(err)
		}
		enricher, _, err := cfg.LexicalEnrichment.build(nil)
		if err != nil || enricher != nil {
			t.Fatalf("omitted lexical_enrichment = (%v, %v), want (nil, nil)", enricher, err)
		}
	})

	t.Run("synonyms_and_endpoint_literal_keys", func(t *testing.T) {
		t.Setenv("TEST_LEXICAL_KEY", "tok_test_lexical_secret_value")
		raw := `{
			"lexical_enrichment": {
				"enabled": true,
				"max_lexical_text_runes": 4096,
				"min_cosine_salience": 0.65,
				"max_synthetic_queries": 6,
				"synonyms": {"solar panel": ["photovoltaic array"]},
				"endpoint": "https://expand.example.test/v1/lexical",
				"api_key_env": "TEST_LEXICAL_KEY",
				"timeout": "10s"
			}
		}`
		var cfg Config
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatal(err)
		}
		enricher, opts, err := cfg.LexicalEnrichment.build(nil)
		if err != nil || enricher == nil {
			t.Fatalf("valid config failed: enricher=%v err=%v", enricher, err)
		}
		if opts.MaxLexicalTextRunes != 4096 || opts.MaxSyntheticQueries != 6 || opts.MinCosineSalience == nil || *opts.MinCosineSalience != 0.65 {
			t.Fatalf("unexpected options: %+v", opts)
		}
		if !slices.Contains(cfg.logSecrets(), "tok_test_lexical_secret_value") {
			t.Fatal("lexical_enrichment.api_key_env secret missing from logSecrets redaction list")
		}
	})

	t.Run("inline_api_key_and_endpoint_userinfo_redacted", func(t *testing.T) {
		var cfg Config
		raw := `{"lexical_enrichment":{"enabled":true,"endpoint":"https://lexuser:lexpass@expand.example.test/v1","api_key":"tok_inline_lexical_secret"}}`
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatal(err)
		}
		secrets := cfg.logSecrets()
		for _, want := range []string{"tok_inline_lexical_secret", "lexuser", "lexpass"} {
			if !slices.Contains(secrets, want) {
				t.Fatalf("secret %q missing from logSecrets redaction list", want)
			}
		}
	})

	t.Run("invalid_api_key_env_reports_env_field", func(t *testing.T) {
		t.Setenv("TEST_INVALID_LEXICAL_KEY", "has whitespace")
		var cfg Config
		raw := `{"lexical_enrichment":{"enabled":true,"endpoint":"https://expand.example.test/v1","api_key_env":"TEST_INVALID_LEXICAL_KEY"}}`
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatal(err)
		}
		_, _, err := cfg.LexicalEnrichment.build(nil)
		var ce *configError
		if !errors.As(err, &ce) || ce.field != "lexical_enrichment.api_key_env" {
			t.Fatalf("invalid env key err = %#v, want configError on lexical_enrichment.api_key_env", err)
		}
	})

	t.Run("non_worker_validate_skips_worker_env_secret", func(t *testing.T) {
		t.Setenv("UNSET_WORKER_LEXICAL_KEY", "")
		var cfg Config
		raw := `{"lexical_enrichment":{"enabled":true,"endpoint":"https://expand.example.test/v1","api_key_env":"UNSET_WORKER_LEXICAL_KEY"}}`
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatal(err)
		}
		if err := cfg.LexicalEnrichment.validate(false); err != nil {
			t.Fatalf("non-worker validation must not require worker api_key_env: %v", err)
		}
		if err := cfg.LexicalEnrichment.validate(true); err == nil {
			t.Fatal("worker validation must fail when api_key_env is unset")
		}
	})

	t.Run("conflicting_api_keys", func(t *testing.T) {
		t.Setenv("TEST_CONFLICT_LEXICAL_KEY", "non-empty-env-secret")
		var cfg Config
		raw := `{"lexical_enrichment":{"enabled":true,"endpoint":"https://expand.example.test/v1","api_key":"inline-secret","api_key_env":"TEST_CONFLICT_LEXICAL_KEY"}}`
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatal(err)
		}
		_, _, err := cfg.LexicalEnrichment.build(nil)
		var ce *configError
		if !errors.As(err, &ce) || ce.code != configConflict || ce.field != "lexical_enrichment.api_key" {
			t.Fatalf("conflicting_api_keys err = %#v, want configConflict on lexical_enrichment.api_key", err)
		}
	})

	for _, tc := range []struct {
		name      string
		raw       string
		wantField string
	}{
		{"enabled_without_source", `{"lexical_enrichment":{"enabled":true}}`, "lexical_enrichment"},
		{"negative_max_runes", `{"lexical_enrichment":{"enabled":true,"max_lexical_text_runes":-1,"synonyms":{"a":["b"]}}}`, "lexical_enrichment.max_lexical_text_runes"},
		{"excessive_max_runes", `{"lexical_enrichment":{"enabled":true,"max_lexical_text_runes":16385,"synonyms":{"a":["b"]}}}`, "lexical_enrichment.max_lexical_text_runes"},
		{"invalid_cosine_high", `{"lexical_enrichment":{"enabled":true,"min_cosine_salience":1.5,"synonyms":{"a":["b"]}}}`, "lexical_enrichment.min_cosine_salience"},
		{"invalid_cosine_low", `{"lexical_enrichment":{"enabled":true,"min_cosine_salience":-1.5,"synonyms":{"a":["b"]}}}`, "lexical_enrichment.min_cosine_salience"},
		{"excessive_queries", `{"lexical_enrichment":{"enabled":true,"max_synthetic_queries":65,"synonyms":{"a":["b"]}}}`, "lexical_enrichment.max_synthetic_queries"},
		{"invalid_endpoint_scheme", `{"lexical_enrichment":{"enabled":true,"endpoint":"ftp://invalid.example"}}`, "lexical_enrichment.endpoint"},
		{"missing_endpoint_hostname", `{"lexical_enrichment":{"enabled":true,"endpoint":"https://:8443/v1"}}`, "lexical_enrichment.endpoint"},
		{"endpoint_userinfo", `{"lexical_enrichment":{"enabled":true,"endpoint":"https://user:pass@expand.example.test/v1"}}`, "lexical_enrichment.endpoint"},
		{"endpoint_fragment", `{"lexical_enrichment":{"enabled":true,"endpoint":"https://expand.example.test/v1#frag"}}`, "lexical_enrichment.endpoint"},
		{"plaintext_key_non_loopback", `{"lexical_enrichment":{"enabled":true,"endpoint":"http://expand.example.test/v1","api_key":"secret"}}`, "lexical_enrichment.endpoint"},
		{"invalid_timeout", `{"lexical_enrichment":{"enabled":true,"endpoint":"https://expand.example.test/v1","timeout":"90s"}}`, "lexical_enrichment.timeout"},
		{"empty_synonym_values", `{"lexical_enrichment":{"enabled":true,"synonyms":{"solar":[]}}}`, "lexical_enrichment.synonyms"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			if err := json.Unmarshal([]byte(tc.raw), &cfg); err != nil {
				t.Fatal(err)
			}
			_, _, err := cfg.LexicalEnrichment.build(nil)
			var ce *configError
			if !errors.As(err, &ce) || ce.field != tc.wantField {
				t.Fatalf("%s: err = %#v, want configError on field %q", tc.name, err, tc.wantField)
			}
		})
	}
}
