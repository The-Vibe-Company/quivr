// Package tei adapts the pinned, offline E5 inference service.
package tei

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"io"
	"net/http"
	"strings"
	"time"
)

//go:embed space.json
var manifest []byte

//go:embed encoder.go
var producerSource []byte

const Image = "ghcr.io/huggingface/text-embeddings-inference@sha256:ad950d30878eceb72aaf32024d26fa2b1d04a75304fa0b4776b49aa1941fea07"

func Space() content.VectorSpace {
	return content.VectorSpace{ID: content.Hash(append([]byte("quivr/vector-space/v1\x00"), manifest...)), Manifest: append([]byte(nil), manifest...)}
}

type Encoder struct{ Endpoint string }

func (e Encoder) Space() content.VectorSpace { return Space() }
func (e Encoder) Producer() string {
	return Image + ";linux/amd64;CPU;ONNX;float32;mean;L2;batch=1;client-sha256=" + content.Hash(producerSource)
}
func (e Encoder) Embed(ctx context.Context, input string) ([]float32, error) {
	if (!strings.HasPrefix(input, "passage: ") && !strings.HasPrefix(input, "query: ")) || len(input) > 2<<20 {
		return nil, content.ErrInvalid
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
	if _, err = content.VectorBytes(vectors[0]); err != nil {
		return nil, errors.New("inference vector invalid")
	}
	return vectors[0], nil
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
