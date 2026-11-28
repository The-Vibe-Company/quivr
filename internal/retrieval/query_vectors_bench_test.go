package retrieval

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
)

const benchQueryDimensions = 768

func benchQueryService() Service {
	return Service{QueryVectors: NewQueryVectorCache(), Embedder: testQueryEmbedder{
		run: func(context.Context, string) ([]float32, error) {
			return make([]float32, benchQueryDimensions), nil
		},
	}}
}

// BenchmarkEncodeHotParallel measures concurrent hits on one cached query, the
// shape of a popular search repeated by many tenants' requests at once.
func BenchmarkEncodeHotParallel(b *testing.B) {
	service := benchQueryService()
	ctx := b.Context()
	const text = "how to index a record"
	if _, err := service.EncodeQuery(ctx, "org", "test-space", text); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			vector, err := service.EncodeQuery(ctx, "org", "test-space", text)
			if err != nil || len(vector) != benchQueryDimensions {
				b.Error(err)
				return
			}
		}
	})
}

// BenchmarkEncodeMixedParallel measures concurrent lookups over a warm working
// set across several orgs, with one in sixteen lookups a never-seen query.
func BenchmarkEncodeMixedParallel(b *testing.B) {
	service := benchQueryService()
	ctx := b.Context()
	const hotKeys = 256
	orgs := [...]string{"org-a", "org-b", "org-c", "org-d"}
	texts := make([]string, hotKeys)
	for i := range texts {
		texts[i] = "how to index record " + strconv.Itoa(i)
		if _, err := service.EncodeQuery(ctx, orgs[i%len(orgs)], "test-space", texts[i]); err != nil {
			b.Fatal(err)
		}
	}
	var cold, worker atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := int(worker.Add(1)) * 7919
		for pb.Next() {
			i++
			org, text := orgs[i%hotKeys%len(orgs)], texts[i%hotKeys]
			if i%16 == 0 {
				org, text = orgs[i%len(orgs)], "cold query "+strconv.FormatInt(cold.Add(1), 10)
			}
			vector, err := service.EncodeQuery(ctx, org, "test-space", text)
			if err != nil || len(vector) != benchQueryDimensions {
				b.Error(err)
				return
			}
		}
	})
}
