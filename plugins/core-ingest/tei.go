package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Encoder calls the deployment's pinned text-embeddings-inference (TEI)
// service with one input per request, exactly as the engine did before
// THE-777, so the same TEI yields the same float32 vectors.
type Encoder struct{ Endpoint string }

// errInvalidInput is an input the pinned profile refuses.
var errInvalidInput = errors.New("invalid inference input")

func (e Encoder) Embed(ctx context.Context, input string) ([]float32, error) {
	if (!strings.HasPrefix(input, "passage: ") && !strings.HasPrefix(input, "query: ")) || len(input) > 2<<20 {
		return nil, errInvalidInput
	}
	if err := e.verifyProfile(ctx); err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{"inputs": []string{input}, "normalize": true, "truncate": false})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(e.Endpoint, "/")+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("inference configuration invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
	if err != nil {
		return nil, errors.New("inference unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, errors.New("inference request failed")
	}
	var vectors [][]float32
	if err = json.NewDecoder(io.LimitReader(res.Body, 32768)).Decode(&vectors); err != nil || len(vectors) != 1 {
		return nil, errors.New("inference response invalid")
	}
	if err = checkUnit(vectors[0]); err != nil {
		return nil, errors.New("inference vector invalid")
	}
	return vectors[0], nil
}

// checkUnit accepts the pinned model's vectors only: 384 finite values of unit norm.
func checkUnit(vector []float32) error {
	if len(vector) != 384 {
		return errInvalidInput
	}
	for _, x := range vector {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return errInvalidInput
		}
	}
	norm := 0.0
	for _, x := range vector {
		norm += float64(x) * float64(x)
	}
	if math.Abs(math.Sqrt(norm)-1) > .001 {
		return errInvalidInput
	}
	return nil
}

// TEI cannot attest weight hashes over HTTP. Preparation verifies every mounted
// file, and this check rejects incompatible serving parameters at query time.
func (e Encoder) verifyProfile(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(e.Endpoint, "/")+"/info", nil)
	if err != nil {
		return errors.New("inference configuration invalid")
	}
	res, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		return errors.New("inference profile unavailable")
	}
	defer res.Body.Close()
	var info struct {
		Version      string `json:"version"`
		SHA          string `json:"sha"`
		DType        string `json:"model_dtype"`
		MaxInput     int    `json:"max_input_length"`
		AutoTruncate bool   `json:"auto_truncate"`
		ModelType    struct {
			Embedding struct {
				Pooling string `json:"pooling"`
			} `json:"embedding"`
		} `json:"model_type"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 8192)).Decode(&info) != nil {
		return errors.New("inference profile invalid")
	}
	if info.Version != "1.9.3" || info.SHA != "06670157fb6c1523482219bdb2d1660277d38088" || info.DType != "float32" || info.MaxInput != 512 || info.AutoTruncate || info.ModelType.Embedding.Pooling != "mean" {
		return errors.New("inference profile mismatch")
	}
	return nil
}
