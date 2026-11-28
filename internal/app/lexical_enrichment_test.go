package app

import (
	"encoding/json"
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

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"enabled_without_source", `{"lexical_enrichment":{"enabled":true}}`},
		{"negative_max_runes", `{"lexical_enrichment":{"enabled":true,"max_lexical_text_runes":-1,"synonyms":{"a":["b"]}}}`},
		{"excessive_max_runes", `{"lexical_enrichment":{"enabled":true,"max_lexical_text_runes":16385,"synonyms":{"a":["b"]}}}`},
		{"invalid_cosine_high", `{"lexical_enrichment":{"enabled":true,"min_cosine_salience":1.5,"synonyms":{"a":["b"]}}}`},
		{"invalid_cosine_low", `{"lexical_enrichment":{"enabled":true,"min_cosine_salience":-1.5,"synonyms":{"a":["b"]}}}`},
		{"excessive_queries", `{"lexical_enrichment":{"enabled":true,"max_synthetic_queries":65,"synonyms":{"a":["b"]}}}`},
		{"invalid_endpoint", `{"lexical_enrichment":{"enabled":true,"endpoint":"ftp://invalid.example"}}`},
		{"invalid_timeout", `{"lexical_enrichment":{"enabled":true,"endpoint":"https://expand.example/v1","timeout":"90s"}}`},
		{"conflicting_api_keys", `{"lexical_enrichment":{"enabled":true,"endpoint":"https://expand.example/v1","api_key":"a","api_key_env":"B"}}`},
		{"empty_synonym_values", `{"lexical_enrichment":{"enabled":true,"synonyms":{"solar":[]}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			if err := json.Unmarshal([]byte(tc.raw), &cfg); err != nil {
				t.Fatal(err)
			}
			if _, _, err := cfg.LexicalEnrichment.build(nil); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}
