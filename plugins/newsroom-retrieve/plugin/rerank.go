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
	RawScore       *float64 `json:"raw_score"`
}

type rerankScores struct {
	probabilities []float64
	logits        []float64
}

func (r retriever) callReranker(ctx context.Context, cfg configuration, query string, docs []string) (rerankScores, error) {
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
			Query       string   `json:"query"`
			Texts       []string `json:"texts"`
			Truncate    bool     `json:"truncate"`
			Instruction string   `json:"instruction,omitempty"`
		}{query, docs, true, cfg.RerankInstruction}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return rerankScores{}, errors.New("could not encode reranker request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.RerankEndpoint, bytes.NewReader(body))
	if err != nil {
		return rerankScores{}, errors.New("could not construct reranker request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if r.apiKey != "" {
		if req.URL.Scheme != "https" {
			return rerankScores{}, errors.New("reranker authentication requires HTTPS")
		}
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return rerankScores{}, errors.New("reranker request timed out")
		}
		return rerankScores{}, errors.New("reranker request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return rerankScores{}, fmt.Errorf("reranker returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRerankResponseBytes+1))
	if err != nil {
		return rerankScores{}, errors.New("reranker read failed")
	}
	if len(raw) > maxRerankResponseBytes {
		return rerankScores{}, errors.New("reranker response exceeds size limit")
	}
	return decodeRerankScores(raw, cfg.RerankProtocol, len(docs))
}

func decodeRerankScores(raw []byte, protocol string, count int) (rerankScores, error) {
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
		return rerankScores{}, invalid
	}
	scores := rerankScores{probabilities: make([]float64, count), logits: make([]float64, count)}
	seen := make([]bool, count)
	for _, entry := range results {
		score := entry.Score
		if protocol == "cohere" {
			score = entry.RelevanceScore
		}
		if entry.Index == nil || *entry.Index < 0 || *entry.Index >= count ||
			seen[*entry.Index] || score == nil || math.IsNaN(*score) || math.IsInf(*score, 0) {
			return rerankScores{}, invalid
		}
		idx := *entry.Index
		scores.probabilities[idx] = *score
		scores.logits[idx] = probabilityLogit(*score)
		if entry.RawScore != nil {
			scores.logits[idx] = *entry.RawScore
		}
		seen[*entry.Index] = true
	}
	return scores, nil
}
