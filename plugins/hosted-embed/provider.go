package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

type provider struct {
	config configuration
	key    string
	log    *slog.Logger
	gate   *providerGate
	local  bool
}

// localQueries is process execution tuning, outside immutable pin configuration.
// Readiness binds the local runtime to the same model revision and dimensions;
// routing never changes document derivations or the declared vector space.
func (i *ingester) localQueries(ctx context.Context, endpoint string) error {
	if endpoint == "" {
		return nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || !net.ParseIP(u.Hostname()).IsLoopback() || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimRight(u.Path, "/") != "/v1" || i.config.Format != "openai" {
		return fmt.Errorf("QUIVR_HOSTED_QUERY_URL requires an OpenAI loopback HTTP /v1 base without credentials, query or fragment")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ready := *u
	ready.Path = "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ready.String(), nil)
	if err != nil {
		return fmt.Errorf("invalid local encoder readiness URL")
	}
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("local encoder is not ready")
	}
	defer res.Body.Close()
	var metadata struct {
		Status     string `json:"status"`
		Model      string `json:"model"`
		Revision   string `json:"model_revision"`
		Source     string `json:"source_revision"`
		Dimensions int    `json:"dimensions"`
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 4097))
	if err != nil || len(data) > 4096 || res.StatusCode != 200 || json.Unmarshal(data, &metadata) != nil || metadata.Status != "ok" || metadata.Model != i.config.Model || metadata.Revision != i.config.Revision || metadata.Dimensions != i.config.Dimensions || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(metadata.Source) || len(i.config.Revision) < 16 || !strings.HasPrefix(metadata.Source, i.config.Revision) {
		return fmt.Errorf("local encoder readiness does not match the configured model, immutable revision and dimensions")
	}
	c := i.config
	c.BaseURL, c.Auth = strings.TrimRight(endpoint, "/"), "none"
	c.RequestTimeoutMS, c.MaxRetries = 1000, 0
	i.queries = &provider{config: c, log: i.provider.log, local: true, gate: &providerGate{slots: make(chan struct{}, 4)}}
	return nil
}

// inputRefusal retains the failure class internally while preserving the
// public ingestion error. Authentication and endpoint failures affect the whole
// request; only input validation/size statuses can justify isolating members.
type inputRefusal struct {
	*quivrplugin.IngestError
}

func (e *inputRefusal) Unwrap() error { return e.IngestError }

func (p provider) embed(ctx context.Context, inputs []string, mode string, invocation ...string) ([][]float32, error) {
	return p.request(ctx, inputs, mode, invocation, false)
}

func (p provider) request(ctx context.Context, inputs []string, mode string, invocations []string, admitted bool) ([][]float32, error) {
	c := p.config
	if c.Auth != "none" && p.key == "" {
		if admitted {
			p.gate.release()
		}
		return nil, quivrplugin.TerminalIngestError("provider_credentials", "AZURE_FOUNDRY_KEY is required")
	}
	body := map[string]any{"model": c.Model}
	path := "/embeddings"
	if c.Format == "openai" {
		body["input"] = inputs
		if c.SendDimensions {
			body["dimensions"] = c.Dimensions
		}
	} else {
		path = "/embed"
		body["texts"] = inputs
		if c.SendDimensions {
			body["output_dimension"] = c.Dimensions
		}
		body["embedding_types"] = []string{"float"}
		if mode == "query" {
			body["input_type"] = c.QueryInputType
		} else {
			body["input_type"] = c.DocumentInputType
		}
	}
	encoded, _ := json.Marshal(body)
	estimate := 0
	for _, s := range inputs {
		estimate += len(s) + specialTokens
	}
	client := &http.Client{Timeout: time.Duration(c.RequestTimeoutMS) * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if !admitted {
			if err := p.gate.acquire(ctx); err != nil {
				return nil, quivrplugin.RetryableIngestError("provider_unavailable", "provider admission cancelled")
			}
		}
		admitted = false
		req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(encoded))
		if err != nil {
			p.gate.release()
			return nil, quivrplugin.TerminalIngestError("invalid_configuration", "invalid provider URL")
		}
		req.Header.Set("Content-Type", "application/json")
		if p.local {
			remaining := time.Duration(c.RequestTimeoutMS) * time.Millisecond
			if deadline, ok := ctx.Deadline(); ok {
				remaining = min(remaining, time.Until(deadline))
			}
			req.Header.Set("X-Quivr-Timeout-Ms", strconv.FormatInt(max(1, remaining.Milliseconds()), 10))
		}
		if c.Auth == "bearer" {
			req.Header.Set("Authorization", "Bearer "+p.key)
		} else if c.Auth == "api-key" {
			req.Header.Set("api-key", p.key)
		}
		res, err := client.Do(req)
		if err != nil {
			p.gate.release()
			p.usage(invocations, len(inputs), mode, attempt, 0, estimate, true)
			return nil, quivrplugin.RetryableIngestError("provider_unavailable", "provider request failed or timed out")
		}
		data, readErr := io.ReadAll(io.LimitReader(res.Body, 16<<20+1))
		_ = res.Body.Close()
		delay := time.Duration(100*(1<<attempt)) * time.Millisecond
		if v, ok := retryAfter(res.Header.Get("Retry-After"), time.Now()); ok {
			delay = v
		}
		if res.StatusCode == 429 {
			p.gate.throttle(delay)
		}
		p.gate.release()
		if res.StatusCode == 200 && readErr == nil && len(data) <= 16<<20 {
			vectors, tokens, known, err := decodeVectors(data, c, len(inputs))
			if !known {
				tokens = estimate
			}
			p.usage(invocations, len(inputs), mode, attempt, res.StatusCode, tokens, !known)
			return vectors, err
		}
		p.usage(invocations, len(inputs), mode, attempt, res.StatusCode, estimate, true)
		if res.StatusCode == 200 {
			return nil, quivrplugin.RetryableIngestError("invalid_provider_response", "provider response is unreadable or too large")
		}
		retry := res.StatusCode == 429 || (res.StatusCode >= 500 && res.StatusCode <= 599)
		if !retry {
			refusal := quivrplugin.TerminalIngestError("inference_refused", "provider refused the request (HTTP "+strconv.Itoa(res.StatusCode)+")")
			if mode == "document" && isInputRefusal(res.StatusCode, data) {
				return nil, &inputRefusal{refusal}
			}
			return nil, refusal
		}
		if attempt == c.MaxRetries {
			break
		}
		// Never retry earlier than Retry-After. An excessive wait returns control
		// to the engine rather than holding its invocation indefinitely.
		if deadline, ok := ctx.Deadline(); ok && delay >= time.Until(deadline) {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, quivrplugin.RetryableIngestError("provider_unavailable", "provider retry cancelled")
		case <-timer.C:
		}
	}
	return nil, quivrplugin.RetryableIngestError("provider_unavailable", "provider exhausted bounded retries")
}

// Unknown validation errors remain request-wide: a bad model or dimension can
// use the same status as a bad input. Inspect only structured attribution and
// never retain or expose a provider's error text.
func isInputRefusal(status int, body []byte) bool {
	if status == http.StatusRequestEntityTooLarge {
		return true
	}
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return false
	}
	var payload struct {
		Error struct {
			Param string `json:"param"`
			Code  string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	param := payload.Error.Param
	if param != "" {
		return param == "input" || param == "texts" || strings.HasPrefix(param, "input[") || strings.HasPrefix(param, "texts[") || strings.HasPrefix(param, "input.") || strings.HasPrefix(param, "texts.")
	}
	switch payload.Error.Code {
	case "invalid_input", "invalid_text", "input_too_long", "text_too_long", "context_length_exceeded", "input_validation_error":
		return true
	}
	return false
}
func (p provider) usage(invocations []string, items int, mode string, attempt, status, tokens int, estimated bool) {
	attrs := []any{"event", "hosted_embedding_usage", "space", p.config.spaceID(), "mode", mode, "attempt", attempt + 1, "status", status, "input_tokens", tokens, "input_count", items, "estimated", estimated}
	if p.local {
		attrs = append(attrs, "backend", "local_cpu")
	}
	if len(invocations) == 1 {
		attrs = append(attrs, "invocation_id", invocations[0])
	} else {
		attrs = append(attrs, "invocation_ids", invocations)
	}
	p.log.Info("embedding_usage", attrs...)
}
func retryAfter(value string, now time.Time) (time.Duration, bool) {
	if seconds, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32); err == nil {
		return time.Duration(seconds) * time.Second, true
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(time.Duration(0), date.Sub(now)), true
	}
	return 0, false
}
func decodeVectors(raw []byte, c configuration, count int) ([][]float32, int, bool, error) {
	var response struct {
		Data []struct {
			Index     *int      `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Embeddings struct {
			Float [][]float32 `json:"float"`
		} `json:"embeddings"`
		Usage struct {
			PromptTokens *int `json:"prompt_tokens"`
		} `json:"usage"`
		Meta struct {
			Billed struct {
				InputTokens *int `json:"input_tokens"`
			} `json:"billed_units"`
		} `json:"meta"`
	}
	bad := quivrplugin.RetryableIngestError("invalid_provider_response", "provider returned invalid embedding vectors")
	if json.Unmarshal(raw, &response) != nil {
		return nil, 0, false, bad
	}
	tokens := response.Usage.PromptTokens
	vectors := make([][]float32, count)
	if c.Format == "cohere" {
		vectors = response.Embeddings.Float
		tokens = response.Meta.Billed.InputTokens
	} else {
		if len(response.Data) != count {
			return nil, 0, false, bad
		}
		for _, item := range response.Data {
			if item.Index == nil || *item.Index < 0 || *item.Index >= count || vectors[*item.Index] != nil {
				return nil, 0, false, bad
			}
			vectors[*item.Index] = item.Embedding
		}
	}
	known := tokens != nil && *tokens >= 0
	n := 0
	if known {
		n = *tokens
	}
	if len(vectors) != count {
		return nil, n, known, bad
	}
	for _, v := range vectors {
		if len(v) != c.Dimensions {
			return nil, n, known, bad
		}
		zero := true
		for _, f := range v {
			if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
				return nil, n, known, bad
			}
			zero = zero && f == 0
		}
		if zero && c.Metric == "cosine" {
			return nil, n, known, bad
		}
	}
	return vectors, n, known, nil
}
