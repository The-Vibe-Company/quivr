package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

type benchSubmissionStore struct{}

func (benchSubmissionStore) Accept(_ context.Context, _ corpus.Scope, c content.Command) (content.Receipt, error) {
	return content.Receipt{
		ID:         "receipt_bench",
		State:      "pending",
		RecordID:   "record_bench",
		Source:     c.Source,
		Processing: content.Processing{State: "queued", Phase: "materialization"},
	}, nil
}

func (benchSubmissionStore) Withdraw(context.Context, corpus.Scope, content.Withdrawal) (content.Receipt, error) {
	return content.Receipt{}, nil
}

type discardResponseWriter struct {
	header http.Header
	status int
}

func (w *discardResponseWriter) Header() http.Header         { return w.header }
func (w *discardResponseWriter) WriteHeader(statusCode int)  { w.status = statusCode }
func (w *discardResponseWriter) Write(p []byte) (int, error) { return len(p), nil }

func newBenchAPI(b *testing.B, options ...Option) (*API, corpus.Scope) {
	b.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	b.Cleanup(func() { slog.SetDefault(prev) })

	scope := corpus.Scope{
		Organization: "org_bench",
		Actions:      []string{"content:read", "content:write"},
		Corpora:      []string{"*"},
	}
	keys := map[string]corpus.Scope{"bench-key": scope}
	contents := content.Service{Submissions: benchSubmissionStore{}}
	api, err := buildAPI(nil, contents, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"), surfaceAll, options...)
	if err != nil {
		b.Fatal(err)
	}
	return api, scope
}

func buildBatchPayload(b *testing.B, count, textBytes int) []byte {
	b.Helper()
	text := strings.Repeat("a", textBytes)
	items := make([]map[string]any, count)
	for i := range items {
		items[i] = map[string]any{
			"idempotency_key": fmt.Sprintf("bench-key-%04d", i),
			"source": map[string]any{
				"corpus_id":  "corpus_bench",
				"namespace":  "feed",
				"record_key": fmt.Sprintf("rec-%04d", i),
			},
			"content": map[string]any{
				"kind": "text",
				"text": text,
			},
		}
	}
	payload, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		b.Fatal(err)
	}
	return payload
}

// BenchmarkBatchDecode measures end-to-end HTTP batch envelope + per-entry
// decoding and validation for 100 entries x ~4 KB (~400 KB payload).
func BenchmarkBatchDecode(b *testing.B) {
	api, scope := newBenchAPI(b)
	payload := buildBatchPayload(b, 100, 3960)
	b.SetBytes(int64(len(payload)))

	reader := bytes.NewReader(payload)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "/v0/records/batch", io.NopCloser(reader))
	if err != nil {
		b.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	w := &discardResponseWriter{header: make(http.Header, 4)}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reader.Reset(payload)
		req.Body = io.NopCloser(reader)
		req.ContentLength = int64(len(payload))
		w.status = 0
		api.handleIngestBatch(w, req, scope)
		if w.status != http.StatusOK {
			b.Fatalf("unexpected status %d", w.status)
		}
	}
}

// BenchmarkIngestAdmission measures concurrent ingest batch admission + decode
// throughput across goroutines.
func BenchmarkIngestAdmission(b *testing.B) {
	api, scope := newBenchAPI(b)
	payload := buildBatchPayload(b, 1, 256)
	b.SetBytes(int64(len(payload)))

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		reader := bytes.NewReader(payload)
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "/v0/records/batch", io.NopCloser(reader))
		if err != nil {
			b.Error(err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		w := &discardResponseWriter{header: make(http.Header, 4)}
		for pb.Next() {
			reader.Reset(payload)
			req.Body = io.NopCloser(reader)
			req.ContentLength = int64(len(payload))
			w.status = 0
			api.handleIngestBatch(w, req, scope)
			if w.status != http.StatusOK {
				b.Errorf("unexpected status %d", w.status)
				return
			}
		}
	})
}
