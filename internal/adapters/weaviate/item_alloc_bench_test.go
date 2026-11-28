package weaviate_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

func benchHybridStore(rowsN int, counts map[string]*atomic.Int64) *weaviate.Store {
	rows := make([]itemSearchRow, rowsN)
	for i := range rows {
		d := 0.1 + float64(i)/1000
		rows[i] = itemSearchRow{
			Segment:  fmt.Sprintf("seg-%d", i),
			Version:  fmt.Sprintf("ver-%d", i),
			Score:    "0.5",
			Distance: &d,
			Text:     strings.Repeat("passage text ", 80),
		}
	}
	body := itemSearchResponse(rows)
	store := weaviate.New("http://index.invalid")
	store.Client.Transport = itemSearchTransport(func(_ context.Context, query string) ([]byte, error) {
		switch {
		case strings.Contains(query, "nearVector:"):
			counts["dense"].Add(1)
		case strings.Contains(query, "bm25:"):
			counts["bm25"].Add(1)
		default:
			counts["passage/other"].Add(1)
		}
		return body, nil
	})
	return store
}

func BenchmarkHTTPHybridSearch(b *testing.B) {
	for _, grouped := range []string{"", "record"} {
		for _, n := range []int{50, 150} {
			b.Run(fmt.Sprintf("group=%s/rows=%d", grouped, n), func(b *testing.B) {
				counts := map[string]*atomic.Int64{
					"dense":         {},
					"bm25":          {},
					"passage/other": {},
				}
				store := benchHybridStore(n, counts)
				g := content.Generation{
					ID:                    "generation",
					Collection:            "ItemSearch",
					ItemKeywordsProjected: true,
					Fields: []corpus.Field{
						{Name: "title", PartRole: "title", Type: "string", Roles: []string{"search"}},
						{Name: "body", PartRole: "body", Type: "string", Roles: []string{"search"}, Analyzer: "folded"},
					},
				}
				routes := []retrieval.Route{{CorpusID: "corpus", Generation: g}}
				q := retrieval.Request{
					Query:   "harbour authority",
					Mode:    "hybrid",
					Vector:  make([]float32, 768),
					GroupBy: grouped,
					K:       150,
				}
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if _, err := store.Search(context.Background(), routes, corpus.Scope{Organization: "organization"}, q); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(counts["dense"].Load())/float64(b.N), "dense_calls/op")
				b.ReportMetric(float64(counts["bm25"].Load())/float64(b.N), "bm25_calls/op")
				b.ReportMetric(float64(counts["passage/other"].Load())/float64(b.N), "passage_calls/op")
			})
		}
	}
}
