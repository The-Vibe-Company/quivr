package main

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Encoder calls the deployment's pinned text-embeddings-inference (TEI)
// service. Batch is how many windows one request carries (1 when unset). The
// same TEI yields the same float32 vectors as the engine's one-input requests
// before THE-777, batched or not (parity_test.go holds both).
type Encoder struct {
	Endpoint string
	Batch    int
}

// MaxBatch is the most windows one TEI request may carry: 32 inputs of at
// most 512 tokens stay within TEI's default max_client_batch_size (32) and
// max_batch_tokens (16384).
const MaxBatch = 32

// Incomplete reports a call that used its time budget with windows left to
// embed. What it embedded is kept, so the next call resumes.
type Incomplete struct{ Embedded, Total int }

func (e *Incomplete) Error() string {
	return fmt.Sprintf("%d of %d windows embedded", e.Embedded, e.Total)
}

var (
	// errInvalidInput is an input the pinned profile refuses.
	errInvalidInput = errors.New("invalid inference input")
	// errRefused is an input TEI refuses (413 or 422): it refuses it again.
	errRefused = errors.New("inference refused")
)

// Embed encodes one input, checking the serving profile first: the query path.
func (e Encoder) Embed(ctx context.Context, input string) ([]float32, error) {
	if err := e.verifyProfile(ctx); err != nil {
		return nil, err
	}
	vectors, err := e.embed(ctx, []string{input})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

// embed encodes a batch of inputs in one request, without the profile check.
func (e Encoder) embed(ctx context.Context, inputs []string) ([][]float32, error) {
	for _, input := range inputs {
		if (!strings.HasPrefix(input, "passage: ") && !strings.HasPrefix(input, "query: ")) || len(input) > 2<<20 {
			return nil, errInvalidInput
		}
	}
	body, _ := json.Marshal(map[string]any{"inputs": inputs, "normalize": true, "truncate": false})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(e.Endpoint, "/")+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("inference configuration invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	// Four seconds for one input, as before, and two more for each other one.
	timeout := time.Duration(2+2*len(inputs)) * time.Second
	res, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, errors.New("inference unavailable")
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return nil, errRefused
	default:
		return nil, errors.New("inference request failed")
	}
	var vectors [][]float32
	if err = json.NewDecoder(io.LimitReader(res.Body, int64(32768*len(inputs)))).Decode(&vectors); err != nil || len(vectors) != len(inputs) {
		return nil, errors.New("inference response invalid")
	}
	for _, v := range vectors {
		if err = checkUnit(v); err != nil {
			return nil, errors.New("inference vector invalid")
		}
	}
	return vectors, nil
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

// Passages embeds the model inputs of a Version's windows. It keeps what it
// embedded, so a call that ends early resumes where it stopped instead of
// starting over (THE-810); before THE-777 the engine got the same effect by
// storing each window's vector as it went. It starts no new request once
// budget has passed, and answers *Incomplete: a long Version then finishes
// over a few calls that each end well within the engine's deadline, and a
// worker is never held by one Version for long. Each call makes one request
// at least.
func (e Encoder) Passages(ctx context.Context, cache *VectorCache, inputs []string, budget time.Duration) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	var missing []int
	for i, input := range inputs {
		if v, ok := cache.get(e.Endpoint, input); ok {
			out[i] = v
		} else {
			missing = append(missing, i)
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	if err := e.verifyProfile(ctx); err != nil {
		return nil, err
	}
	size := min(max(e.Batch, 1), MaxBatch)
	started := time.Now()
	for start := 0; start < len(missing); start += size {
		if start > 0 && time.Since(started) >= budget {
			return nil, &Incomplete{Embedded: len(inputs) - len(missing) + start, Total: len(inputs)}
		}
		if err := e.batch(ctx, cache, inputs, missing[start:min(start+size, len(missing))], out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// batch embeds inputs[i] for each i of batch into out. A batch TEI refuses is
// split, so only an input it refuses alone is refused.
func (e Encoder) batch(ctx context.Context, cache *VectorCache, inputs []string, batch []int, out [][]float32) error {
	texts := make([]string, len(batch))
	for k, i := range batch {
		texts[k] = inputs[i]
	}
	vectors, err := e.embed(ctx, texts)
	if errors.Is(err, errRefused) && len(batch) > 1 {
		half := len(batch) / 2
		if err = e.batch(ctx, cache, inputs, batch[:half], out); err == nil {
			err = e.batch(ctx, cache, inputs, batch[half:], out)
		}
		return err
	}
	if err != nil {
		return err
	}
	for k, i := range batch {
		out[i] = vectors[k]
		cache.put(e.Endpoint, inputs[i], vectors[k])
	}
	return nil
}

// CachedVectors bounds the vectors a plugin process keeps: about 6 MB, sixteen
// Versions of the largest size (256 windows). The cache is per process: a
// retry that reaches another replica starts over.
const CachedVectors = 4096

// VectorCache keeps the most recently embedded window vectors of one plugin
// process, by TEI endpoint and model input. The pinned TEI answers the same
// vector for the same input, so a kept vector is the one TEI would answer.
type VectorCache struct {
	mu      sync.Mutex
	order   *list.List
	entries map[[32]byte]*list.Element
}

type cached struct {
	key    [32]byte
	vector []float32
}

func cacheKey(endpoint, input string) [32]byte {
	return sha256.Sum256([]byte(strings.TrimRight(endpoint, "/") + "\x00" + input))
}

func (c *VectorCache) get(endpoint, input string) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[cacheKey(endpoint, input)]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(e)
	return e.Value.(cached).vector, true
}

func (c *VectorCache) put(endpoint, input string, vector []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.order, c.entries = list.New(), map[[32]byte]*list.Element{}
	}
	key := cacheKey(endpoint, input)
	if e, ok := c.entries[key]; ok {
		c.order.MoveToFront(e)
		return
	}
	c.entries[key] = c.order.PushFront(cached{key: key, vector: vector})
	if c.order.Len() > CachedVectors {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(cached).key)
	}
}
