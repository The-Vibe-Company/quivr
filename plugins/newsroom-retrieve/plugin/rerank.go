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
	"sync"
)

const maxRerankResponseBytes = 1 << 20

type rerankResult struct {
	Index          *int     `json:"index"`
	Score          *float64 `json:"score"`
	RelevanceScore *float64 `json:"relevance_score"`
	RawScore       *float64 `json:"raw_score"`
}

// rerankScores carries calibrated probabilities plus, when the endpoint
// exposes them, unsaturated raw relevance margins (e.g. logit(yes)-logit(no)).
type rerankScores struct {
	Prob []float64
	Raw  []float64 // nil when the endpoint returns no raw margins
}

// logitSpace returns per-document log-odds: the raw margin when available,
// else log(p/(1-p)) with p clamped away from 0 and 1. Log-odds keep the full
// dynamic range among top candidates whose probabilities saturate near 1.
func (s rerankScores) logitSpace() []float64 {
	out := make([]float64, len(s.Prob))
	for i, p := range s.Prob {
		if s.Raw != nil {
			out[i] = s.Raw[i]
			continue
		}
		const eps = 1e-7
		p = math.Min(math.Max(p, eps), 1-eps)
		out[i] = math.Log(p / (1 - p))
	}
	return out
}

func (r retriever) callReranker(ctx context.Context, cfg configuration, query string, docs []string) ([]float64, error) {
	s, err := r.callRerankerScores(ctx, cfg, query, docs)
	if err != nil {
		return nil, err
	}
	return s.Prob, nil
}

// callRerankerSharded splits docs into n interleaved shards scored by
// concurrent requests (so a multi-container endpoint scores them on separate
// GPUs) and merges the results back into document order. Any shard failure
// fails the whole call so the caller's graceful fallback applies.
func (r retriever) callRerankerSharded(ctx context.Context, cfg configuration, query string, docs []string, n int) (rerankScores, error) {
	if n <= 1 || len(docs) < 2*n {
		return r.callRerankerScores(ctx, cfg, query, docs)
	}
	shards := make([][]int, n)
	for i := range docs {
		shards[i%n] = append(shards[i%n], i)
	}
	shardCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]rerankScores, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for s := range shards {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			part := make([]string, len(shards[s]))
			for j, idx := range shards[s] {
				part[j] = docs[idx]
			}
			results[s], errs[s] = r.callRerankerScores(shardCtx, cfg, query, part)
			if errs[s] != nil {
				cancel()
			}
		}(s)
	}
	wg.Wait()
	out := rerankScores{Prob: make([]float64, len(docs)), Raw: make([]float64, len(docs))}
	for s := range shards {
		if errs[s] != nil {
			return rerankScores{}, errs[s]
		}
		if results[s].Raw == nil {
			out.Raw = nil
		}
		for j, idx := range shards[s] {
			out.Prob[idx] = results[s].Prob[j]
			if out.Raw != nil {
				out.Raw[idx] = results[s].Raw[j]
			}
		}
	}
	return out, nil
}

func (r retriever) callRerankerScores(ctx context.Context, cfg configuration, query string, docs []string) (rerankScores, error) {
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
	return decodeRerankScoresFull(raw, cfg.RerankProtocol, len(docs))
}

func decodeRerankScores(raw []byte, protocol string, count int) ([]float64, error) {
	s, err := decodeRerankScoresFull(raw, protocol, count)
	if err != nil {
		return nil, err
	}
	return s.Prob, nil
}

func decodeRerankScoresFull(raw []byte, protocol string, count int) (rerankScores, error) {
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
	scores := make([]float64, count)
	rawScores := make([]float64, count)
	allRaw := count > 0
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
		scores[*entry.Index] = *score
		seen[*entry.Index] = true
		if entry.RawScore != nil && !math.IsNaN(*entry.RawScore) && !math.IsInf(*entry.RawScore, 0) {
			rawScores[*entry.Index] = *entry.RawScore
		} else {
			allRaw = false
		}
	}
	out := rerankScores{Prob: scores}
	if allRaw {
		out.Raw = rawScores
	}
	return out, nil
}
