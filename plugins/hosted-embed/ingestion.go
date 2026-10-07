package main

import (
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

type ingester struct {
	config    configuration
	provider  provider
	queries   *provider
	documents *documentBatcher
	mu        sync.Mutex
	cache     map[[32]byte]*list.Element
	order     *list.List
	bytes     int
}
type cachedVector struct {
	key    [32]byte
	vector []float32
}

func newIngester(c configuration, key string, log *slog.Logger) *ingester {
	i := &ingester{config: c, provider: provider{config: c, key: key, log: log, gate: &providerGate{slots: make(chan struct{}, c.MaxConcurrentRequests)}}, cache: map[[32]byte]*list.Element{}, order: list.New()}
	i.documents = &documentBatcher{provider: i.provider, slots: make(chan struct{}, min(256, c.MaxConcurrentRequests*c.BatchSize)), pending: map[string]*documentBatch{}}
	return i
}
func (i *ingester) get(key [32]byte) []float32 {
	i.mu.Lock()
	defer i.mu.Unlock()
	if e, ok := i.cache[key]; ok {
		i.order.MoveToFront(e)
		return append([]float32(nil), e.Value.(cachedVector).vector...)
	}
	return nil
}
func (i *ingester) put(key [32]byte, v []float32) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.cache[key]; ok {
		return
	}
	i.cache[key] = i.order.PushFront(cachedVector{key, append([]float32(nil), v...)})
	i.bytes += len(v) * 4
	for i.order.Len() > 4096 || i.bytes > 32<<20 {
		e := i.order.Back()
		item := e.Value.(cachedVector)
		delete(i.cache, item.key)
		i.order.Remove(e)
		i.bytes -= len(item.vector) * 4
	}
}
func (i *ingester) SegmentAndEmbed(ctx context.Context, req *quivrplugin.IngestRequest) ([]quivrplugin.Segment, error) {
	c := i.config
	segments, inputs, err := c.segments(req.Parts)
	if err != nil {
		return nil, err
	}
	if len(req.Spaces) == 0 {
		return segments, nil
	}
	if len(req.Spaces) != 1 || req.Spaces[0] != c.spaceID() {
		return nil, quivrplugin.TerminalIngestError("unknown_space", "request must name the configured space")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.CallBudgetMS)*time.Millisecond)
	defer cancel()
	vectors := make([][]float32, len(inputs))
	keys := make([][32]byte, len(inputs))
	missing := []int{}
	for n, input := range inputs {
		keys[n] = sha256.Sum256([]byte(req.OrganizationID + "\x00" + input))
		vectors[n] = i.get(keys[n])
		if vectors[n] == nil {
			missing = append(missing, n)
		}
	}
	for start := 0; start < len(missing); {
		end, tokens := start, 0
		batch := []string{}
		for end < len(missing) && end-start < c.BatchSize {
			input := inputs[missing[end]]
			cost := len(input) + specialTokens
			if tokens+cost > c.BatchTokens {
				break
			}
			tokens += cost
			batch = append(batch, input)
			end++
		}
		var v [][]float32
		var err error
		if c.BatchWaitMS == 0 {
			v, err = i.provider.embed(ctx, batch, "document", req.InvocationID)
		} else {
			v, err = i.documents.embed(ctx, req.OrganizationID, batch, req.InvocationID)
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				err = quivrplugin.RetryableIngestError("provider_unavailable", "document embedding cancelled")
			}
			var e *quivrplugin.IngestError
			if errors.As(err, &e) {
				if e.Retryable {
					return nil, quivrplugin.RetryableIngestError("embedding_incomplete", "document embedding is incomplete; completed batches are kept for retry")
				}
				return nil, e
			}
			return nil, err
		}
		for n, k := range missing[start:end] {
			vectors[k] = v[n]
			i.put(keys[k], v[n])
		}
		start = end
	}
	for n := range segments {
		segments[n].Vectors[c.spaceID()] = vectors[n]
	}
	return segments, nil
}
func (i *ingester) EmbedQuery(ctx context.Context, req *quivrplugin.QueryRequest) ([]float32, error) {
	c := i.config
	if req.Space != c.spaceID() {
		return nil, quivrplugin.TerminalIngestError("unknown_space", "request must name the configured space")
	}
	input := c.QueryPrefix + req.Query.Text
	if req.Query.Modality != "text" || !utf8.ValidString(input) || strings.ContainsRune(input, 0) || strings.TrimSpace(req.Query.Text) == "" {
		return nil, quivrplugin.TerminalIngestError("invalid_query", "query must contain valid text")
	}
	if len(input)+specialTokens > c.MaxTokens {
		return nil, quivrplugin.TerminalIngestError("query_limit", "query exceeds max_tokens_per_segment including its prefix and special token reserve")
	}
	encoder := &i.provider
	if i.queries != nil {
		encoder = i.queries
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(encoder.config.RequestTimeoutMS)*time.Millisecond)
		defer cancel()
	}
	vectors, err := encoder.embed(ctx, []string{input}, "query", req.InvocationID)
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}
