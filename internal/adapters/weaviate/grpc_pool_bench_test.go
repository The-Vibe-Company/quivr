package weaviate_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

type grpcBenchServer struct {
	pb.UnimplementedWeaviateServer
	reply *pb.SearchReply
	calls atomic.Int64
}

func newGRPCBenchServer(rows int) *grpcBenchServer {
	passage := strings.Repeat("passage text ", 80) // ~1040 bytes
	results := make([]*pb.SearchResult, rows)
	for i := range rows {
		results[i] = &pb.SearchResult{
			Properties: &pb.PropertiesResult{
				NonRefProps: &pb.Properties{
					Fields: map[string]*pb.Value{
						"segmentId":    {Kind: &pb.Value_TextValue{TextValue: fmt.Sprintf("seg-%d", i)}},
						"generationId": {Kind: &pb.Value_TextValue{TextValue: "generation"}},
						"versionId":    {Kind: &pb.Value_TextValue{TextValue: fmt.Sprintf("ver-%d", i)}},
						"passageText":  {Kind: &pb.Value_TextValue{TextValue: passage}},
					},
				},
			},
			Metadata: &pb.MetadataResult{
				Score:           0.5,
				ScorePresent:    true,
				Distance:        0.1,
				DistancePresent: true,
			},
		}
	}
	reply := &pb.SearchReply{Results: results}
	// Pre-populate protobuf cached size before concurrent marshaling.
	_, _ = proto.Marshal(reply)
	return &grpcBenchServer{reply: reply}
}

func (s *grpcBenchServer) Search(_ context.Context, _ *pb.SearchRequest) (*pb.SearchReply, error) {
	s.calls.Add(1)
	return s.reply, nil
}

type grpcBenchHarness struct {
	store  *weaviate.Store
	server *grpc.Server
	fake   *grpcBenchServer
	routes []retrieval.Route
	scope  corpus.Scope
	req    retrieval.Request
}

func newGRPCBenchHarness(tb testing.TB, rows int) *grpcBenchHarness {
	tb.Helper()

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	tb.Cleanup(httpServer.Close)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	fake := newGRPCBenchServer(rows)
	grpcServer := grpc.NewServer()
	pb.RegisterWeaviateServer(grpcServer, fake)
	go func() { _ = grpcServer.Serve(listener) }()
	tb.Cleanup(grpcServer.Stop)

	store, closeStore, err := weaviate.NewWithTransport(httpServer.URL, listener.Addr().String(), outbound.TLS{}, "bench-api-key")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(closeStore)

	gen := content.Generation{
		ID:                    "generation",
		Collection:            "ItemSearch",
		ItemKeywordsProjected: true,
		Fields: []corpus.Field{
			{Name: "title", PartRole: "title", Type: "string", Roles: []string{"search"}},
			{Name: "body", PartRole: "body", Type: "string", Roles: []string{"search"}, Analyzer: "folded"},
		},
	}
	routes := []retrieval.Route{{CorpusID: "corpus", Generation: gen}}
	scope := corpus.Scope{Organization: "organization"}
	req := retrieval.Request{
		Query:   "harbour authority",
		Mode:    "hybrid",
		Vector:  make([]float32, 768),
		GroupBy: "record",
		K:       rows,
	}

	// Warm up schema caches and establish gRPC connection(s).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hits, err := store.Search(ctx, routes, scope, req)
	if err != nil {
		tb.Fatalf("warmup search failed: %v", err)
	}
	if len(hits) != rows {
		tb.Fatalf("warmup hits = %d, want %d", len(hits), rows)
	}
	fake.calls.Store(0)

	return &grpcBenchHarness{
		store:  store,
		server: grpcServer,
		fake:   fake,
		routes: routes,
		scope:  scope,
		req:    req,
	}
}

func percentileDuration(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

type latencySummary struct {
	p50           time.Duration
	p95           time.Duration
	p99           time.Duration
	throughput    float64
	allocBytesAvg float64
	grpcCalls     int64
}

func runLatencyProfile(tb testing.TB, h *grpcBenchHarness, concurrency, perWorker int) latencySummary {
	tb.Helper()
	totalSearches := concurrency * perWorker
	latencies := make([]time.Duration, totalSearches)
	var errMu sync.Mutex
	var firstErr error

	h.fake.calls.Store(0)
	var memBefore, memAfter runtime.MemStats
	if concurrency == 1 {
		runtime.GC()
		runtime.ReadMemStats(&memBefore)
	}

	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for w := range concurrency {
		go func() {
			defer wg.Done()
			base := w * perWorker
			for i := range perWorker {
				t0 := time.Now()
				_, err := h.store.Search(context.Background(), h.routes, h.scope, h.req)
				latencies[base+i] = time.Since(t0)
				if err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if firstErr != nil {
		tb.Fatalf("conc=%d search failed: %v", concurrency, firstErr)
	}
	var allocAvg float64
	if concurrency == 1 {
		runtime.ReadMemStats(&memAfter)
		allocAvg = float64(memAfter.TotalAlloc-memBefore.TotalAlloc) / float64(totalSearches)
	}

	slices.Sort(latencies)
	return latencySummary{
		p50:           percentileDuration(latencies, 0.50),
		p95:           percentileDuration(latencies, 0.95),
		p99:           percentileDuration(latencies, 0.99),
		throughput:    float64(totalSearches) / elapsed.Seconds(),
		allocBytesAvg: allocAvg,
		grpcCalls:     h.fake.calls.Load(),
	}
}

func BenchmarkGRPCPool(b *testing.B) {
	const rows = 150
	for _, conc := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("rows=%d/conc=%d", rows, conc), func(b *testing.B) {
			h := newGRPCBenchHarness(b, rows)
			latencies := make([]time.Duration, b.N)
			var nextIdx atomic.Int64
			var errMu sync.Mutex
			var firstErr error

			b.ReportAllocs()
			b.ResetTimer()
			start := time.Now()
			if conc == 1 {
				for i := range b.N {
					t0 := time.Now()
					_, err := h.store.Search(context.Background(), h.routes, h.scope, h.req)
					latencies[i] = time.Since(t0)
					if err != nil {
						b.Fatalf("search failed: %v", err)
					}
				}
			} else {
				var wg sync.WaitGroup
				wg.Add(conc)
				for range conc {
					go func() {
						defer wg.Done()
						for {
							idx := int(nextIdx.Add(1) - 1)
							if idx >= b.N {
								return
							}
							t0 := time.Now()
							_, err := h.store.Search(context.Background(), h.routes, h.scope, h.req)
							latencies[idx] = time.Since(t0)
							if err != nil {
								errMu.Lock()
								if firstErr == nil {
									firstErr = err
								}
								errMu.Unlock()
								return
							}
						}
					}()
				}
				wg.Wait()
			}
			elapsed := time.Since(start)
			b.StopTimer()
			if firstErr != nil {
				b.Fatalf("search failed: %v", firstErr)
			}
			slices.Sort(latencies)
			b.ReportMetric(float64(percentileDuration(latencies, 0.50).Microseconds())/1000.0, "p50-ms")
			b.ReportMetric(float64(percentileDuration(latencies, 0.95).Microseconds())/1000.0, "p95-ms")
			b.ReportMetric(float64(percentileDuration(latencies, 0.99).Microseconds())/1000.0, "p99-ms")
			b.ReportMetric(float64(b.N)/elapsed.Seconds(), "search/s")
		})
	}
}

func TestGRPCLatencyProfile(t *testing.T) {
	const rows = 150
	const perWorker = 40
	h := newGRPCBenchHarness(t, rows)

	for _, conc := range []int{1, 8, 32} {
		res := runLatencyProfile(t, h, conc, perWorker)
		wantCalls := int64(conc * perWorker * 4)
		if res.grpcCalls != wantCalls {
			t.Fatalf("conc=%d grpc calls = %d, want %d", conc, res.grpcCalls, wantCalls)
		}
		t.Logf("rows=%d conc=%d total=%d p50=%.2fms p95=%.2fms p99=%.2fms throughput=%.1f search/s alloc=%.2fMB/search",
			rows, conc, conc*perWorker,
			float64(res.p50.Microseconds())/1000.0,
			float64(res.p95.Microseconds())/1000.0,
			float64(res.p99.Microseconds())/1000.0,
			res.throughput,
			res.allocBytesAvg/(1024*1024),
		)
	}
}
