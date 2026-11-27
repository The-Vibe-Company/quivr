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

const maxRerankResponseBytes = 1 << 20

type rerankResult struct {
	Index          *int     `json:"index"`
	Score          *float64 `json:"score"`
	RelevanceScore *float64 `json:"relevance_score"`
}

func (r retriever) callReranker(ctx context.Context, cfg configuration, query string, docs []string) ([]float64, error) {
	var payload any
	switch cfg.RerankProtocol {
	case "cohere":
		payload = struct {
			Model     string   `json:"model"`
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
			TopN      int      `json:"top_n"`
		}{cfg.RerankModel, query, docs, len(docs)}
	default:
		payload = struct {
			Query    string   `json:"query"`
			Texts    []string `json:"texts"`
			Truncate bool     `json:"truncate"`
		}{query, docs, true}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("could not encode reranker request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.RerankEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("could not construct reranker request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if r.apiKey != "" {
		if req.URL.Scheme != "https" {
			return nil, errors.New("reranker authentication requires HTTPS")
		}
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("reranker request timed out")
		}
		return nil, errors.New("reranker request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("reranker returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRerankResponseBytes+1))
	if err != nil {
		return nil, errors.New("reranker read failed")
	}
	if len(raw) > maxRerankResponseBytes {
		return nil, errors.New("reranker response exceeds size limit")
	}
	return decodeRerankScores(raw, cfg.RerankProtocol, len(docs))
}

func decodeRerankScores(raw []byte, protocol string, count int) ([]float64, error) {
	var results []rerankResult
	var err error
	if protocol == "cohere" {
		var envelope struct {
			Results []rerankResult `json:"results"`
		}
		err = json.Unmarshal(raw, &envelope)
		results = envelope.Results
	} else {
		err = json.Unmarshal(raw, &results)
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
