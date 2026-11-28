package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
)

func buildSearchDecodePayload(b *testing.B) []byte {
	b.Helper()
	corpusIDs := make([]string, 16)
	for i := range corpusIDs {
		corpusIDs[i] = fmt.Sprintf("corpus_%02d", i)
	}
	payload, err := json.Marshal(map[string]any{
		"query":      "quarterly revenue forecast and regional supply chain risk analysis",
		"corpus_ids": corpusIDs,
		"mode":       "hybrid",
		"profile":    "default",
		"limit":      25,
		"filter": map[string]any{
			"source_namespaces": []string{"docs", "reports", "wiki", "filings"},
			"record_ids":        []string{"rec_01", "rec_02", "rec_03", "rec_04"},
			"version_ids":       []string{"ver_01", "ver_02"},
			"metadata": []map[string]any{
				{"field": "metadata.language", "any_of": []any{"en", "fr", "de"}},
				{"field": "metadata.tags", "any_of": []any{"finance", "risk", "supply-chain", "q4"}},
				{"field": "metadata.published_at", "gte": "2025-01-01T00:00:00Z", "lte": "2026-11-28T23:59:59Z"},
				{"field": "priority_tier", "any_of": []any{1.0, 2.0, 3.0}},
				{"field": "is_verified", "any_of": []any{true}},
				{"field": "metadata.source_type", "any_of": []any{"pdf", "markdown", "html"}},
			},
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	return payload
}

// BenchmarkSearchDecode measures HTTP SearchRequest decoding, OpenAPI contract
// validation, and retrieval.Request construction for a 16-corpus request with
// source, identity, and typed metadata filters.
func BenchmarkSearchDecode(b *testing.B) {
	api, scope := newBenchAPI(b)
	// Omit search:query so Retrieval.Search short-circuits immediately after
	// HTTP decode, validation, request mapping, and admission acquire/release.
	scope.Actions = nil

	payload := buildSearchDecodePayload(b)
	// Confirm the fixture satisfies the authoritative OpenAPI SearchRequest schema.
	var parsed any
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err != nil {
		b.Fatal(err)
	}
	if err := api.schemas["SearchRequest"].Validate(parsed); err != nil {
		b.Fatalf("benchmark payload violates SearchRequest schema: %v", err)
	}

	b.SetBytes(int64(len(payload)))
	reader := bytes.NewReader(payload)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "/v0/search", io.NopCloser(reader))
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
		api.search(w, req, scope)
		if w.status != http.StatusForbidden {
			b.Fatalf("unexpected status %d", w.status)
		}
	}
}
