package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// wordTokenizer stands in for the pinned tokenizer: one token per word, with
// code point offsets, so a long text makes many windows without Python.
type wordTokenizer struct{}

func (wordTokenizer) Encode(_ context.Context, inputs []TokenInput) ([]Encoding, error) {
	out := make([]Encoding, len(inputs))
	for i, in := range inputs {
		start := -1
		for n, r := range append([]rune(in.Text), ' ') {
			switch {
			case !unicode.IsSpace(r) && start < 0:
				start = n
			case unicode.IsSpace(r) && start >= 0:
				out[i].Offsets = append(out[i].Offsets, [2]int{start, n})
				start = -1
			}
		}
		out[i].Tokens = len(out[i].Offsets)
	}
	return out, nil
}

// fakeTEI answers /info with the pinned profile and /embed with unit vectors
// for up to allowance inputs; the request that would go over it hangs until
// its caller gives up, the way a plugin call ends at the engine's deadline.
type fakeTEI struct {
	mu        sync.Mutex
	allowance int
	stalled   chan struct{}
	sizes     []int
	refuse    string // an input TEI refuses with 413, alone or in a batch
}

func (f *fakeTEI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/info" {
		_, _ = w.Write([]byte(`{"version":"1.9.3","sha":"06670157fb6c1523482219bdb2d1660277d38088","model_dtype":"float32","max_input_length":512,"auto_truncate":false,"model_type":{"embedding":{"pooling":"mean"}}}`))
		return
	}
	var body struct{ Inputs []string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	for _, input := range body.Inputs {
		if f.refuse != "" && input == f.refuse {
			f.mu.Unlock()
			http.Error(w, `{"error":"Input validation error","error_type":"validation"}`, http.StatusRequestEntityTooLarge)
			return
		}
	}
	if len(body.Inputs) > f.allowance {
		f.mu.Unlock()
		f.stalled <- struct{}{}
		<-r.Context().Done()
		return
	}
	f.allowance -= len(body.Inputs)
	f.sizes = append(f.sizes, len(body.Inputs))
	f.mu.Unlock()
	vectors := make([][]float32, len(body.Inputs))
	for i := range vectors {
		vectors[i] = make([]float32, 384)
		vectors[i][0] = 1
	}
	_ = json.NewEncoder(w).Encode(vectors)
}

func teiIngester(t *testing.T, tei *fakeTEI, budget time.Duration) (*ingester, json.RawMessage) {
	t.Helper()
	server := httptest.NewServer(tei)
	t.Cleanup(server.Close)
	c := configuration{TEIURL: server.URL, Tokenizer: TokenizerConfig{Python: "unused", Model: "unused"}, BatchSize: 8}
	raw, _ := json.Marshal(c)
	i := &ingester{budget: budget, backends: map[string]*backend{string(raw): {windows: TokenWindows{Tokenizer: wordTokenizer{}}, encoder: Encoder{Endpoint: server.URL, Batch: c.BatchSize}}}}
	return i, raw
}

// longBody is a body Part of about windows token windows, each different.
func longBody(windows int) string {
	var b strings.Builder
	for p := 0; p < windows*4; p++ {
		for w := 0; w < 84; w++ {
			fmt.Fprintf(&b, "mot%d ", p)
		}
		b.WriteString("fin.\n\n")
	}
	return b.String()
}

// A long Version whose windows cannot all be embedded in one call still gets
// its vectors: each call resumes where the last one stopped instead of
// starting again (THE-810; before, every attempt re-embedded the first
// windows and the Version retried forever). A call ends either at the
// engine's deadline or, first, at its own budget with the retryable
// embedding_incomplete. Requests stay within TEI's max_client_batch_size.
func TestLongVersionGetsItsVectorsOverSeveralCalls(t *testing.T) {
	for _, c := range []struct {
		name    string
		perCall int           // inputs TEI answers before the engine's deadline ends a call
		budget  time.Duration // the call's own budget
	}{
		{name: "engine deadline", perCall: 12, budget: time.Hour},
		{name: "own budget", perCall: 1 << 20, budget: 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			tei := &fakeTEI{stalled: make(chan struct{})}
			impl, config := teiIngester(t, tei, c.budget)
			req := &quivrplugin.IngestRequest{Configuration: config, Spaces: []string{space},
				Parts: []quivrplugin.IngestPart{{Key: "body", Role: "body", Text: longBody(40)}}}
			var segments []quivrplugin.Segment
			calls := 0
			for segments == nil {
				calls++
				if calls > 10 {
					t.Fatalf("no vectors after %d calls; TEI answered batches %v", calls-1, tei.sizes)
				}
				tei.mu.Lock()
				tei.allowance = c.perCall
				tei.mu.Unlock()
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() {
					var err error
					segments, err = impl.SegmentAndEmbed(ctx, req)
					done <- err
				}()
				var err error
				select {
				case err = <-done:
				case <-tei.stalled:
					cancel()
					if err = <-done; err == nil {
						t.Fatalf("call %d succeeded after its deadline", calls)
					}
				}
				cancel()
				var ie *quivrplugin.IngestError
				if err != nil && (!errors.As(err, &ie) || !ie.Retryable) {
					t.Fatalf("call %d: %v, want a retryable error", calls, err)
				}
			}
			if len(segments) < 30 {
				t.Fatalf("%d segments, want a long Version of about 40", len(segments))
			}
			for n, s := range segments {
				if len(s.Vectors[space]) != 384 {
					t.Fatalf("segment %d has no vector", n)
				}
			}
			answered := 0
			for _, n := range tei.sizes {
				answered += n
				if n > MaxBatch {
					t.Errorf("a request of %d inputs exceeds TEI's max_client_batch_size", n)
				}
			}
			if answered >= 2*len(segments) {
				t.Errorf("TEI embedded %d inputs for %d segments: the calls repeated their work", answered, len(segments))
			}
			t.Logf("%d segments in %d calls, TEI batches %v", len(segments), calls, tei.sizes)
		})
	}
}

// An input TEI refuses is a terminal refusal of the Version, not an outage
// retried forever; the other windows of its batch are not what TEI refused.
func TestRefusedInputIsTerminal(t *testing.T) {
	tei := &fakeTEI{stalled: make(chan struct{}, 1), allowance: 1 << 20}
	impl, config := teiIngester(t, tei, time.Hour)
	body := longBody(3)
	windows, err := TokenWindows{Tokenizer: wordTokenizer{}}.Process(context.Background(), []Part{{Key: "body", Role: "body", Text: body}})
	if err != nil || len(windows) < 2 {
		t.Fatalf("%d windows, %v", len(windows), err)
	}
	tei.refuse = windows[1].Derivation.ModelInput
	_, err = impl.SegmentAndEmbed(context.Background(), &quivrplugin.IngestRequest{Configuration: config, Spaces: []string{space},
		Parts: []quivrplugin.IngestPart{{Key: "body", Role: "body", Text: body}}})
	var ie *quivrplugin.IngestError
	if !errors.As(err, &ie) || ie.Retryable || ie.Code != "inference_refused" {
		t.Fatalf("got %#v, want the terminal error inference_refused", err)
	}
}
