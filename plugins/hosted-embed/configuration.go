package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

const pluginID = "hosted.embed"

type configuration struct {
	PluginID           string                  `json:"plugin_id"`
	Format             string                  `json:"format"`
	BaseURL            string                  `json:"base_url"`
	Auth               string                  `json:"auth"`
	Model              string                  `json:"model"`
	Dimensions         int                     `json:"dimensions"`
	SendDimensions     bool                    `json:"send_dimensions"`
	Metric             string                  `json:"metric"`
	Revision           string                  `json:"model_revision"`
	PluginVersion      string                  `json:"plugin_version"`
	QueryPrefix        string                  `json:"query_prefix"`
	DocumentPrefix     string                  `json:"document_prefix"`
	MaxTokens          int                     `json:"max_tokens_per_segment"`
	BodyTokens         int                     `json:"body_tokens"`
	MaxChunks          int                     `json:"max_chunks"`
	RebalanceTail      bool                    `json:"rebalance_tail"`
	TailMinFraction    float64                 `json:"tail_min_fraction"`
	TitleSource        string                  `json:"title_source"`
	TitleContextParts  []string                `json:"title_context_parts,omitempty"`
	DocumentTemplate   string                  `json:"document_template"`
	QueryTemplate      string                  `json:"query_template"`
	Tokenizer          *tokenizerConfiguration `json:"tokenizer,omitempty"`
	TokenizerProcesses int                     `json:"tokenizer_processes"`
	// BatchWaitMS collects concurrent document inputs; zero disables collection.
	BatchWaitMS           int      `json:"batch_wait_ms"`
	BatchSize             int      `json:"batch_size"`
	BatchTokens           int      `json:"max_batch_tokens"`
	RequestTimeoutMS      int      `json:"request_timeout_ms"`
	CallBudgetMS          int      `json:"call_budget_ms"`
	MaxConcurrentRequests int      `json:"max_concurrent_requests"`
	MaxRetries            int      `json:"max_retries"`
	InputPrice            *float64 `json:"usd_per_million_tokens,omitempty"`
}

func parseConfiguration(raw []byte) (configuration, error) {
	c := configuration{PluginID: pluginID, SendDimensions: true, Metric: "cosine", Revision: "1", PluginVersion: "1.2.0", MaxTokens: 512, BatchSize: 16, BatchWaitMS: 25, BatchTokens: 8192, RequestTimeoutMS: 4000, CallBudgetMS: 30000, MaxRetries: 2, MaxConcurrentRequests: 4, BodyTokens: 512, MaxChunks: 4, RebalanceTail: true, TailMinFraction: 0.25, TitleSource: "title", DocumentTemplate: "{prefix}{text}", QueryTemplate: "{prefix}{query}"}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("configuration must contain only declared fields")
	}
	if dec.Decode(new(any)) != io.EOF {
		return c, fmt.Errorf("configuration must be one JSON object")
	}
	if len(c.PluginID) > 40 || !regexp.MustCompile(`^[a-z][a-z0-9_-]*(?:\.[a-z][a-z0-9_-]*)*$`).MatchString(c.PluginID) {
		return c, fmt.Errorf("plugin_id must be a plugin identifier of at most 40 characters")
	}
	if c.BodyTokens < 8 || c.BodyTokens > 32768 || c.MaxChunks < 1 || c.MaxChunks > 256 || c.TailMinFraction < 0 || c.TailMinFraction > 0.5 {
		return c, fmt.Errorf("invalid packing budget, cap or tail settings")
	}
	if c.TitleSource != "title" && c.TitleSource != "none" && c.TitleSource != "inline" {
		return c, fmt.Errorf("invalid title_source")
	}
	if err := validateTemplate(c.DocumentTemplate, "text", c.DocumentPrefix, "title"); err != nil {
		return c, fmt.Errorf("document_template: %w", err)
	}
	if err := validateTemplate(c.QueryTemplate, "query", c.QueryPrefix); err != nil {
		return c, fmt.Errorf("query_template: %w", err)
	}
	if len(c.TitleContextParts) == 0 {
		c.TitleContextParts = nil
	}
	if len(c.TitleContextParts) > 16 {
		return c, fmt.Errorf("too many title_context_parts")
	}
	for _, key := range c.TitleContextParts {
		if key == "" || strings.ContainsRune(key, 0) {
			return c, fmt.Errorf("invalid title context key")
		}
	}
	if c.Tokenizer != nil && (c.Tokenizer.Python == "" || c.Tokenizer.Model == "" || strings.ContainsRune(c.Tokenizer.Python, 0) || strings.ContainsRune(c.Tokenizer.Model, 0) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(c.Tokenizer.SHA256)) {
		return c, fmt.Errorf("tokenizer requires python, model and sha256")
	}
	if c.TokenizerProcesses < 0 || c.TokenizerProcesses > 32 {
		return c, fmt.Errorf("tokenizer_processes must be between 0 and 32")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return c, fmt.Errorf("base_url must be an HTTP(S) base without credentials, query or fragment")
	}
	if c.Format != "openai" && c.Format != "cohere" {
		return c, fmt.Errorf("format must be openai or cohere")
	}
	if c.Auth != "bearer" && c.Auth != "api-key" && c.Auth != "none" {
		return c, fmt.Errorf("auth must be bearer, api-key or none")
	}
	if c.Model == "" || len(c.Model) > 256 || c.Revision == "" || !regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,31}$`).MatchString(c.Revision) {
		return c, fmt.Errorf("model and model_revision must be nonempty and bounded")
	}
	if !regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`).MatchString(c.PluginVersion) {
		return c, fmt.Errorf("plugin_version must be MAJOR.MINOR.PATCH")
	}
	if c.Dimensions < 1 || c.Dimensions > 4096 || (c.Metric != "cosine" && c.Metric != "dot" && c.Metric != "l2") {
		return c, fmt.Errorf("invalid dimensions or metric")
	}
	if c.MaxTokens < 8 || c.MaxTokens > 32768 || c.BatchSize < 1 || c.BatchSize > 32 || c.BatchTokens < c.MaxTokens || c.BatchTokens > 1048576 {
		return c, fmt.Errorf("invalid segment or batch limits")
	}
	// Without a tokenizer, UTF-8 bytes conservatively count as tokens. The
	// actual expanded title and source input are checked during packing/querying.
	if c.Tokenizer == nil && (len(c.documentInput("", ""))+specialTokens >= c.MaxTokens || len(c.queryInput(""))+specialTokens >= c.MaxTokens) {
		return c, fmt.Errorf("templates and prefixes leave no text budget")
	}
	if c.RequestTimeoutMS < 100 || c.RequestTimeoutMS > 10000 || c.CallBudgetMS < 100 || c.CallBudgetMS > 90000 || c.MaxRetries < 0 || c.MaxRetries > 5 {
		return c, fmt.Errorf("invalid timeout or retry bounds")
	}
	if c.BatchWaitMS < 0 || c.BatchWaitMS > 100 {
		return c, fmt.Errorf("batch_wait_ms must be between 0 and 100")
	}
	if c.BatchWaitMS >= c.CallBudgetMS {
		return c, fmt.Errorf("batch_wait_ms must be less than call_budget_ms")
	}
	if c.MaxConcurrentRequests < 1 || c.MaxConcurrentRequests > 32 {
		return c, fmt.Errorf("max_concurrent_requests must be between 1 and 32")
	}
	if c.InputPrice != nil && (*c.InputPrice < 0 || *c.InputPrice > 1000000) {
		return c, fmt.Errorf("invalid input price")
	}
	for _, s := range []string{c.Model, c.Revision, c.QueryPrefix, c.DocumentPrefix} {
		if strings.ContainsRune(s, 0) {
			return c, fmt.Errorf("configuration strings must not contain NUL")
		}
	}
	return c, nil
}

func (c configuration) spaceID() string {
	// Endpoint, credentials and execution tuning do not change vector meaning.
	contextParts := c.TitleContextParts
	if len(contextParts) == 0 {
		contextParts = nil
	}
	semantics := []any{c.Format, c.Model, c.Dimensions, c.Metric, c.Revision, c.QueryPrefix, c.DocumentPrefix, c.DocumentTemplate, c.QueryTemplate, c.TitleSource, contextParts, "separate_passages:v1"}
	raw, _ := json.Marshal(semantics)
	sum := sha256.Sum256(raw)
	slug := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(c.Model), "-"), "-")
	if slug == "" {
		slug = "model"
	}
	if slug[0] < 'a' || slug[0] > 'z' {
		slug = "m-" + slug
	}
	owner := c.PluginID
	if owner == "" {
		owner = pluginID
	}
	suffix := fmt.Sprintf("-%d-%s", c.Dimensions, hex.EncodeToString(sum[:8]))
	limit := min(28, 64-len(owner)-1-len(suffix))
	if len(slug) > limit {
		slug = slug[:limit]
	}
	return owner + "." + slug + suffix
}

func (c configuration) manifest(command []string) ([]byte, error) {
	raw, _ := json.Marshal(c)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	props := map[string]any{}
	for k, v := range fields {
		props[k] = map[string]any{"const": v}
	}
	// Empty optional context has one semantic identity, while both explicit
	// null/empty arrays and omission remain valid installer input.
	if len(c.TitleContextParts) == 0 {
		props["title_context_parts"] = map[string]any{"enum": []any{nil, []string{}}}
	}
	if c.Tokenizer == nil {
		props["tokenizer"] = map[string]any{"const": nil}
	}
	if c.InputPrice == nil {
		props["usd_per_million_tokens"] = map[string]any{"const": nil}
	}
	schema := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"format", "base_url", "auth", "model", "dimensions"}, "properties": props}
	space := map[string]any{"version": c.Revision, "model": c.Model, "dimensions": c.Dimensions, "metric": c.Metric, "indexes": []string{"text"}, "query_modalities": []string{"text"}}
	if c.InputPrice != nil {
		space["input_price"] = map[string]any{"usd_per_million_tokens": *c.InputPrice}
	}
	m := map[string]any{"id": c.PluginID, "version": c.PluginVersion, "description": "Text passages embedded with a configured hosted or OpenAI-compatible model.", "compatibility": map[string]string{"engine": ">=0.1.0 <0.3.0", "plugin_api": ">=0.18.0 <0.19.0"}, "contributions": map[string]any{"ingestion": map[string]any{"paging": true, "spaces": map[string]any{c.spaceID(): space}, "timeout_ms": 120000, "query_timeout_ms": 10000, "limits": map[string]int{"max_segments": 256}}}, "configuration": map[string]any{"schema": schema, "execution_keys": []string{"tokenizer_processes", "max_concurrent_requests", "batch_size", "max_batch_tokens", "request_timeout_ms", "call_budget_ms", "batch_wait_ms", "max_retries"}}, "secrets": []any{map[string]any{"name": "EMBED_API_KEY", "required": c.Auth != "none", "description": "Provider key from the plugin environment; required by bearer and api-key authentication. Never put it in configuration."}}, "run": map[string]any{"command": command}}
	return json.MarshalIndent(m, "", "  ")
}

// Templates are deliberately limited to named substitutions, with no recursive
// expansion or expressions. Validate before any input reaches a provider.
func validateTemplate(template, required, prefix string, optional ...string) error {
	if strings.ContainsRune(template, 0) {
		return fmt.Errorf("must not contain NUL")
	}
	allowed := map[string]bool{required: true, "prefix": true}
	for _, key := range optional {
		allowed[key] = true
	}
	seen := map[string]bool{}
	for rest := template; rest != ""; {
		start := strings.IndexAny(rest, "{}")
		if start < 0 {
			break
		}
		if rest[start] != '{' {
			return fmt.Errorf("unmatched template brace")
		}
		key, tail, ok := strings.Cut(rest[start+1:], "}")
		if !ok || !allowed[key] {
			return fmt.Errorf("unsupported or unclosed placeholder")
		}
		seen[key] = true
		rest = tail
	}
	if !seen[required] {
		return fmt.Errorf("must contain {%s}", required)
	}
	if prefix != "" && !seen["prefix"] {
		return fmt.Errorf("nonempty prefix requires {prefix}")
	}
	return nil
}
