package app

import (
	"math"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/processing"
)

// LexicalEnrichmentConfig configures opt-in async Phase-2 lexical enrichment
// (synonyms and/or cosine-salience-gated synthetic queries appended to
// Segment.LexicalText during Stage-2 Temporal enrichment). Disabled by default.
type LexicalEnrichmentConfig struct {
	Enabled             bool                `json:"enabled"`
	MaxLexicalTextRunes int                 `json:"max_lexical_text_runes,omitempty"`
	MinCosineSalience   *float64            `json:"min_cosine_salience,omitempty"`
	MaxSyntheticQueries int                 `json:"max_synthetic_queries,omitempty"`
	Synonyms            map[string][]string `json:"synonyms,omitempty"`
	Endpoint            string              `json:"endpoint,omitempty"`
	APIKey              string              `json:"api_key,omitempty"`
	APIKeyEnv           string              `json:"api_key_env,omitempty"`
	Timeout             string              `json:"timeout,omitempty"`
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c LexicalEnrichmentConfig) resolveAPIKey(resolveEnv bool) (string, error) {
	if c.APIKey != "" && c.APIKeyEnv != "" {
		return "", badConfig(configConflict, "lexical_enrichment.api_key", "set either lexical_enrichment.api_key or lexical_enrichment.api_key_env, not both")
	}
	key := c.APIKey
	if c.APIKeyEnv != "" && resolveEnv {
		var err error
		key, err = boundSecret(c.APIKeyEnv, "lexical_enrichment.api_key_env")
		if err != nil {
			return "", err
		}
		if key == "" {
			return "", badConfig(configMissing, "lexical_enrichment.api_key_env", "lexical_enrichment.api_key_env must resolve to a non-empty value")
		}
	}
	if key != "" && invalidBearerToken(key) {
		return "", badConfig(configInvalid, "lexical_enrichment.api_key", "lexical_enrichment API key must not contain whitespace or control characters")
	}
	return key, nil
}

// validate checks LexicalEnrichmentConfig at process startup. When resolveEnv
// is false (non-worker commands such as api and migrate), worker-only
// api_key_env bindings are not required in the process environment.
func (c LexicalEnrichmentConfig) validate(resolveEnv bool) error {
	_, _, err := c.buildWithOptions(nil, resolveEnv)
	return err
}

// build validates LexicalEnrichmentConfig and constructs the opt-in Stage-2
// enricher and options for the worker. When disabled, it returns (nil, zero, nil)
// with zero runtime overhead.
func (c LexicalEnrichmentConfig) build(encoder processing.LexicalQueryEncoder) (processing.LexicalEnricher, processing.LexicalEnrichmentOptions, error) {
	return c.buildWithOptions(encoder, true)
}

func (c LexicalEnrichmentConfig) buildWithOptions(encoder processing.LexicalQueryEncoder, resolveEnv bool) (processing.LexicalEnricher, processing.LexicalEnrichmentOptions, error) {
	if !c.Enabled {
		return nil, processing.LexicalEnrichmentOptions{}, nil
	}
	if c.MaxLexicalTextRunes < 0 || c.MaxLexicalTextRunes > content.MaxLexicalTextRunes {
		return nil, processing.LexicalEnrichmentOptions{}, badConfig(configInvalid, "lexical_enrichment.max_lexical_text_runes", "lexical_enrichment.max_lexical_text_runes must be between 0 and 16384")
	}
	if c.MinCosineSalience != nil {
		v := *c.MinCosineSalience
		if math.IsNaN(v) || math.IsInf(v, 0) || v < -1 || v > 1 {
			return nil, processing.LexicalEnrichmentOptions{}, badConfig(configInvalid, "lexical_enrichment.min_cosine_salience", "lexical_enrichment.min_cosine_salience must be a finite number between -1 and 1")
		}
	}
	if c.MaxSyntheticQueries < 0 || c.MaxSyntheticQueries > processing.MaxSyntheticQueriesCap {
		return nil, processing.LexicalEnrichmentOptions{}, badConfig(configInvalid, "lexical_enrichment.max_synthetic_queries", "lexical_enrichment.max_synthetic_queries must be between 0 and 64")
	}
	var timeout time.Duration
	if c.Timeout != "" {
		var err error
		timeout, err = time.ParseDuration(c.Timeout)
		if err != nil || timeout <= 0 || timeout > processing.MaxLexicalEnrichmentTimeout {
			return nil, processing.LexicalEnrichmentOptions{}, badConfig(configInvalid, "lexical_enrichment.timeout", "lexical_enrichment.timeout must be a positive duration no greater than 60s")
		}
	}

	var enrichers []processing.LexicalEnricher
	if len(c.Synonyms) > 0 {
		syn, err := processing.NewSynonymLexicalEnricher(c.Synonyms)
		if err != nil {
			return nil, processing.LexicalEnrichmentOptions{}, invalidConfig("lexical_enrichment.synonyms", "invalid lexical_enrichment.synonyms", err)
		}
		enrichers = append(enrichers, syn)
	}
	if strings.TrimSpace(c.Endpoint) != "" {
		u, err := url.Parse(strings.TrimSpace(c.Endpoint))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return nil, processing.LexicalEnrichmentOptions{}, badConfig(configInvalid, "lexical_enrichment.endpoint", "lexical_enrichment.endpoint must be a valid http or https URL without userinfo or fragment")
		}
		hasKey := c.APIKey != "" || c.APIKeyEnv != ""
		if hasKey && u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
			return nil, processing.LexicalEnrichmentOptions{}, badConfig(configInvalid, "lexical_enrichment.endpoint", "lexical_enrichment.endpoint with an API key must use https or a loopback host")
		}
		apiKey, err := c.resolveAPIKey(resolveEnv)
		if err != nil {
			return nil, processing.LexicalEnrichmentOptions{}, err
		}
		httpEnricher, err := processing.NewHTTPLexicalEnricher(processing.HTTPLexicalEnricherConfig{
			Endpoint:   u.String(),
			APIKey:     apiKey,
			Timeout:    timeout,
			MaxQueries: c.MaxSyntheticQueries,
		})
		if err != nil {
			return nil, processing.LexicalEnrichmentOptions{}, invalidConfig("lexical_enrichment.endpoint", "invalid lexical_enrichment HTTP endpoint", err)
		}
		enrichers = append(enrichers, httpEnricher)
	}
	if len(enrichers) == 0 {
		return nil, processing.LexicalEnrichmentOptions{}, badConfig(configInvalid, "lexical_enrichment", "lexical_enrichment.enabled requires synonyms and/or endpoint")
	}

	var enricher processing.LexicalEnricher
	if len(enrichers) == 1 {
		enricher = enrichers[0]
	} else {
		enricher = processing.ChainLexicalEnricher{Enrichers: enrichers}
	}
	opts := processing.LexicalEnrichmentOptions{
		MaxLexicalTextRunes: c.MaxLexicalTextRunes,
		MinCosineSalience:   c.MinCosineSalience,
		MaxSyntheticQueries: c.MaxSyntheticQueries,
		QueryEncoder:        encoder,
	}
	return enricher, opts, nil
}
