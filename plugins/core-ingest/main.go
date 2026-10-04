// Command quivr-core-ingest is the first-party ingestion plugin (core.ingest):
// the token-window segmentation and multilingual E5-small embedding the engine
// ran itself before THE-777. It cuts body Parts into windows of the pinned
// tokenizer's tokens, embeds each window with the deployment's TEI and encodes
// queries the same way; see README.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/sdks/go/quivrplugin"
)

// configuration is the pin configuration: where TEI answers and where the
// pinned tokenizer is.
type configuration struct {
	TEIURL    string          `json:"tei_url"`
	Tokenizer TokenizerConfig `json:"tokenizer"`
	// BatchSize is how many windows one TEI request carries; 1 when unset.
	BatchSize int `json:"batch_size,omitempty"`
}

// CallBudget is how long one segment_and_embed call keeps starting TEI
// requests before it answers the retryable embedding_incomplete: far within
// the manifest's timeout_ms, so a slow TEI makes a long Version take several
// calls instead of reaching the engine's deadline.
const CallBudget = 30 * time.Second

// backend is what one configuration needs: one tokenizer process, loaded once
// and kept for the life of the plugin, and the TEI client.
type backend struct {
	windows TokenWindows
	encoder Encoder
}

type ingester struct {
	mu       sync.Mutex
	backends map[string]*backend
	// vectors keeps the window vectors this process embedded, so a call the
	// engine retries resumes (Encoder.Passages).
	vectors VectorCache
	// budget is CallBudget; tests shorten it.
	budget time.Duration
}

func (i *ingester) backend(raw json.RawMessage) (*backend, error) {
	var c configuration
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	key, _ := json.Marshal(c)
	i.mu.Lock()
	defer i.mu.Unlock()
	if b, ok := i.backends[string(key)]; ok {
		return b, nil
	}
	b := &backend{windows: TokenWindows{Tokenizer: &Server{Config: c.Tokenizer}}, encoder: Encoder{Endpoint: c.TEIURL, Batch: c.BatchSize}}
	i.backends[string(key)] = b
	return b, nil
}

func (i *ingester) SegmentAndEmbed(ctx context.Context, req *quivrplugin.IngestRequest) ([]quivrplugin.Segment, error) {
	b, err := i.backend(req.Configuration)
	if err != nil {
		return nil, quivrplugin.TerminalIngestError("invalid_configuration", err.Error())
	}
	parts := make([]Part, len(req.Parts))
	for n, p := range req.Parts {
		parts[n] = Part{Key: p.Key, Role: p.Role, Text: p.Text}
	}
	windows, err := b.windows.Process(ctx, parts)
	var refusal *Refusal
	switch {
	case errors.As(err, &refusal):
		// The engine shows the code and message in the Version's diagnostic.
		return nil, quivrplugin.TerminalIngestError(refusal.Code, refusal.Message)
	case err != nil:
		return nil, quivrplugin.RetryableIngestError("tokenizer_unavailable", "the pinned tokenizer is unavailable")
	}
	var vectors [][]float32
	if len(req.Spaces) > 0 {
		inputs := make([]string, len(windows))
		for n, w := range windows {
			inputs[n] = w.Derivation.ModelInput
		}
		vectors, err = b.encoder.Passages(ctx, &i.vectors, inputs, i.budget)
		var incomplete *Incomplete
		switch {
		case errors.As(err, &incomplete):
			return nil, quivrplugin.RetryableIngestError("embedding_incomplete", incomplete.Error()+"; the next call resumes")
		case errors.Is(err, errRefused):
			return nil, quivrplugin.TerminalIngestError("inference_refused", "the embedding service refuses a window of this Version")
		case err != nil:
			return nil, quivrplugin.RetryableIngestError("inference_unavailable", "the embedding service is unavailable")
		}
	}
	segments := make([]quivrplugin.Segment, len(windows))
	for n, w := range windows {
		var provenance map[string]any
		raw, _ := json.Marshal(w.Derivation)
		_ = json.Unmarshal(raw, &provenance)
		spaces := map[string][]float32{}
		for _, space := range req.Spaces {
			spaces[space] = vectors[n]
		}
		segments[n] = quivrplugin.Segment{PartKey: w.PartKey, Start: w.Start, End: w.End, Vectors: spaces, Provenance: provenance}
	}
	return segments, nil
}

func (i *ingester) EmbedQuery(ctx context.Context, req *quivrplugin.QueryRequest) ([]float32, error) {
	b, err := i.backend(req.Configuration)
	if err != nil {
		return nil, quivrplugin.TerminalIngestError("invalid_configuration", err.Error())
	}
	query, err := b.windows.NormalizeQuery(ctx, req.Query.Text)
	switch {
	case errors.Is(err, errInvalidQuery):
		// The code and message the engine surfaces as 422 query_too_long.
		return nil, quivrplugin.TerminalIngestError("query_too_long", fmt.Sprintf("query exceeds %d tokens", Parameters.QueryTokens))
	case err != nil:
		return nil, quivrplugin.RetryableIngestError("tokenizer_unavailable", "the pinned tokenizer is unavailable")
	}
	vector, err := b.encoder.Embed(ctx, "query: "+query)
	if err != nil {
		return nil, quivrplugin.RetryableIngestError("inference_unavailable", "the embedding service is unavailable")
	}
	return vector, nil
}

func main() {
	plugin, err := quivrplugin.New("")
	if err == nil {
		err = plugin.Ingestion(&ingester{backends: map[string]*backend{}, budget: CallBudget})
	}
	if err == nil {
		m := plugin.Manifest()
		host, port := os.Getenv(quivrplugin.EnvHost), os.Getenv(quivrplugin.EnvPort)
		if host == "" {
			host = "127.0.0.1"
		}
		if port == "" {
			port = "8080"
		}
		// One startup line, so an operator sees the sidecar came up and what it serves.
		fmt.Fprintf(os.Stderr, "quivr-core-ingest: serving %s@%s (ingestion) on %s\n", m.ID, m.Version, net.JoinHostPort(host, port))
		err = plugin.Serve()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "quivr-core-ingest:", err)
		os.Exit(1)
	}
}
