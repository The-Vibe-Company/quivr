package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
)

const maxResponseBytes = 1 << 20

const (
	timeoutPhaseNone          = "none"
	timeoutPhaseBudget        = "budget"
	timeoutPhaseHTTPRoundTrip = "http_roundtrip"
	timeoutPhaseResponseRead  = "response_read"
)

type result struct {
	Index          *int     `json:"index"`
	Score          *float64 `json:"score"`
	RelevanceScore *float64 `json:"relevance_score"`
}

func (r retriever) rerank(ctx context.Context, cfg configuration, query string, docs []string) ([]float64, string, error) {
	var payload any
	switch cfg.Protocol {
	case "tei":
		payload = struct {
			Query    string   `json:"query"`
			Texts    []string `json:"texts"`
			Truncate bool     `json:"truncate"`
		}{query, docs, true}
	case "cohere":
		payload = struct {
			Model     string   `json:"model"`
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
			TopN      int      `json:"top_n"`
		}{cfg.Model, query, docs, len(docs)}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, timeoutPhaseNone, errors.New("could not encode reranker request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, timeoutPhaseNone, errors.New("could not construct reranker request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if r.apiKey != "" {
		if request.URL.Scheme != "https" {
			return nil, timeoutPhaseNone, errors.New("reranker authentication requires HTTPS")
		}
		request.Header.Set("Authorization", "Bearer "+r.apiKey)
	}
	response, err := r.client.Do(request)
	if err != nil {
		phase, failure := requestFailure(ctx, timeoutPhaseHTTPRoundTrip, err)
		return nil, phase, failure
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, timeoutPhaseNone, fmt.Errorf("reranker returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		phase, failure := requestFailure(ctx, timeoutPhaseResponseRead, err)
		return nil, phase, failure
	}
	if len(raw) > maxResponseBytes {
		return nil, timeoutPhaseNone, errors.New("reranker response exceeds the size limit")
	}
	scores, err := decodeScores(raw, cfg.Protocol, len(docs))
	return scores, timeoutPhaseNone, err
}

// Use fixed messages because transport errors can contain the configured URL
// or request headers, and upstream responses can reflect arbitrary input.
func requestFailure(ctx context.Context, phase string, err error) (string, error) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return phase, fmt.Errorf("reranker request timed out (%s)", phase)
	}
	if errors.Is(err, context.Canceled) {
		return timeoutPhaseNone, errors.New("reranker request was canceled")
	}
	return timeoutPhaseNone, errors.New("reranker request failed")
}

func decodeScores(raw []byte, protocol string, count int) ([]float64, error) {
	var results []result
	var err error
	if protocol == "tei" {
		err = json.Unmarshal(raw, &results)
	} else {
		var response struct {
			Results []result `json:"results"`
		}
		err = json.Unmarshal(raw, &response)
		results = response.Results
	}
	invalid := errors.New("reranker response must contain one valid score per document")
	if err != nil || len(results) != count {
		return nil, invalid
	}

	scores := make([]float64, count)
	seen := make([]bool, count)
	for _, entry := range results {
		score := entry.Score
		if protocol == "cohere" {
			score = entry.RelevanceScore
		}
		if entry.Index == nil || *entry.Index < 0 || *entry.Index >= count ||
			seen[*entry.Index] || score == nil || math.IsNaN(*score) || math.IsInf(*score, 0) {
			return nil, invalid
		}
		scores[*entry.Index] = *score
		seen[*entry.Index] = true
	}
	return scores, nil
}
