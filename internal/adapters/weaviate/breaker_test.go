package weaviate

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/grpc"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

func TestCircuitBreakerStateMachine(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}

	b := newCircuitBreaker(3, 100*time.Millisecond, clock)

	// Two failures followed by a success resets the streak.
	for range 2 {
		ok, probe := b.Allow()
		if !ok || probe {
			t.Fatalf("closed Allow = (%v, %v), want (true, false)", ok, probe)
		}
		b.RecordFailure(probe)
	}
	b.RecordSuccess(false)
	if b.State() != breakerClosed {
		t.Fatalf("state after reset = %v, want closed", b.State())
	}

	// Three consecutive failures open the breaker.
	for range 3 {
		ok, probe := b.Allow()
		if !ok || probe {
			t.Fatalf("closed Allow = (%v, %v), want (true, false)", ok, probe)
		}
		b.RecordFailure(probe)
	}
	if b.State() != breakerOpen {
		t.Fatalf("state after threshold failures = %v, want open", b.State())
	}

	// Calls before cooldown are rejected; stale non-probe success cannot close it.
	if ok, _ := b.Allow(); ok {
		t.Fatal("Allow succeeded while open before cooldown")
	}
	b.RecordSuccess(false)
	if b.State() != breakerOpen {
		t.Fatalf("stale non-probe success changed open state to %v", b.State())
	}

	// Advance past cooldown -> half-open admits exactly one probe.
	advance(100 * time.Millisecond)
	ok, probe := b.Allow()
	if !ok || !probe {
		t.Fatalf("first half-open Allow = (%v, %v), want (true, true)", ok, probe)
	}
	if b.State() != breakerHalfOpen {
		t.Fatalf("state = %v, want half-open", b.State())
	}
	if ok2, probe2 := b.Allow(); ok2 || probe2 {
		t.Fatalf("concurrent half-open Allow = (%v, %v), want (false, false)", ok2, probe2)
	}

	// Neutral release frees the probe slot without closing.
	b.ReleaseProbe(probe)
	ok, probe = b.Allow()
	if !ok || !probe {
		t.Fatalf("re-acquired half-open Allow = (%v, %v), want (true, true)", ok, probe)
	}

	// Failed probe re-opens and restarts cooldown.
	b.RecordFailure(probe)
	if b.State() != breakerOpen {
		t.Fatalf("state after failed probe = %v, want open", b.State())
	}
	advance(50 * time.Millisecond)
	if ok, _ := b.Allow(); ok {
		t.Fatal("Allow succeeded before restarted cooldown elapsed")
	}

	// After second cooldown, a single healthy probe closes the breaker.
	advance(60 * time.Millisecond)
	ok, probe = b.Allow()
	if !ok || !probe {
		t.Fatalf("probe after second cooldown = (%v, %v), want (true, true)", ok, probe)
	}
	b.RecordSuccess(probe)
	if b.State() != breakerClosed || !b.fastClosed.Load() {
		t.Fatalf("state=%v fastClosed=%v, want closed+fastClosed", b.State(), b.fastClosed.Load())
	}
}

type controllableGRPCServer struct {
	pb.UnimplementedWeaviateServer
	hits    atomic.Int64
	entered chan struct{}
	release chan struct{}
}

func (s *controllableGRPCServer) Search(ctx context.Context, _ *pb.SearchRequest) (*pb.SearchReply, error) {
	s.hits.Add(1)
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &pb.SearchReply{Results: []*pb.SearchResult{{
		Properties: &pb.PropertiesResult{NonRefProps: &pb.Properties{Fields: map[string]*pb.Value{
			"segmentId":    {Kind: &pb.Value_TextValue{TextValue: "seg-1"}},
			"generationId": {Kind: &pb.Value_TextValue{TextValue: "gen-1"}},
		}}},
		Metadata: &pb.MetadataResult{ScorePresent: true, Score: 1.0},
	}}}, nil
}

func TestGRPCBreaker(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	srv1 := grpc.NewServer()
	pb.RegisterWeaviateServer(srv1, &controllableGRPCServer{})
	go func() { _ = srv1.Serve(lis) }()
	t.Cleanup(srv1.Stop)

	store, closeStore, err := NewWithTransport("http://weaviate.test", addr, outbound.TLS{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeStore)

	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}

	const cooldown = 5 * time.Second
	breaker := newCircuitBreaker(5, cooldown, clock)
	store.grpcPooled().breaker = breaker

	routes := []retrieval.Route{{CorpusID: "corpus", Generation: content.Generation{ID: "gen-1", Collection: "Example"}}}
	scope := corpus.Scope{Organization: "org"}
	req := retrieval.Request{Mode: "lexical", Query: "harbour"}

	// Healthy search succeeds.
	if hits, err := store.Search(context.Background(), routes, scope, req); err != nil || len(hits) != 1 {
		t.Fatalf("initial healthy search = %v, %v", hits, err)
	}

	// Stop server and drive consecutive failures to open the breaker.
	srv1.Stop()
	for i := range 5 {
		_, err := store.Search(context.Background(), routes, scope, req)
		if !errors.Is(err, retrieval.ErrUnavailable) {
			t.Fatalf("failure %d err = %v, want retrieval.ErrUnavailable", i+1, err)
		}
	}
	if breaker.State() != breakerOpen {
		t.Fatalf("breaker state = %v, want open", breaker.State())
	}

	// Already-canceled and expired callers keep context error identity even when
	// the circuit is open.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Search(canceledCtx, routes, scope, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("open breaker with canceled ctx err = %v, want context.Canceled", err)
	}
	expiredCtx, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()
	if _, err := store.Search(expiredCtx, routes, scope, req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("open breaker with expired ctx err = %v, want context.DeadlineExceeded", err)
	}

	// 200 searches after breaker opens must fast-fail with p99 <= 20 ms.
	// With the injected clock held constant, cooldown cannot expire mid-sample.
	const fastFailCount = 200
	latencies := make([]time.Duration, fastFailCount)
	for i := range fastFailCount {
		t0 := time.Now()
		_, err := store.Search(context.Background(), routes, scope, req)
		latencies[i] = time.Since(t0)
		if !errors.Is(err, ErrCircuitOpen) || !errors.Is(err, retrieval.ErrUnavailable) {
			t.Fatalf("fast-fail %d err = %v, want ErrCircuitOpen/ErrUnavailable", i, err)
		}
	}
	slices.Sort(latencies)
	p99 := latencies[int(float64(len(latencies)-1)*0.99)]
	t.Logf("breaker open fast-fail (n=%d): p50=%s p99=%s", fastFailCount, latencies[len(latencies)/2], p99)
	if p99 > 20*time.Millisecond {
		t.Fatalf("breaker fast-fail p99 = %s, want <= 20ms", p99)
	}

	// Restart a healthy server on the same address with a probe gate to verify
	// half-open admits exactly one probe.
	lis2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("re-listen %s: %v", addr, err)
	}
	probeGate := &controllableGRPCServer{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	srv2 := grpc.NewServer()
	pb.RegisterWeaviateServer(srv2, probeGate)
	go func() { _ = srv2.Serve(lis2) }()
	t.Cleanup(srv2.Stop)

	// Advance fake clock past cooldown without sleeping.
	advance(cooldown + time.Millisecond)

	// Launch the single half-open probe and wait until it reaches the server.
	probeDone := make(chan error, 1)
	go func() {
		_, err := store.Search(context.Background(), routes, scope, req)
		probeDone <- err
	}()
	select {
	case <-probeGate.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("half-open probe did not reach restarted server")
	}

	// While the single probe is in-flight, concurrent callers must be rejected.
	const concurrentDuringProbe = 16
	var rejected atomic.Int32
	var wg sync.WaitGroup
	wg.Add(concurrentDuringProbe)
	for range concurrentDuringProbe {
		go func() {
			defer wg.Done()
			_, err := store.Search(context.Background(), routes, scope, req)
			if errors.Is(err, ErrCircuitOpen) {
				rejected.Add(1)
			}
		}()
	}
	wg.Wait()
	if rejected.Load() != concurrentDuringProbe {
		t.Fatalf("half-open rejected %d/%d concurrent callers, want all rejected", rejected.Load(), concurrentDuringProbe)
	}
	if got := probeGate.hits.Load(); got != 1 {
		t.Fatalf("half-open admitted %d probes to server, want exactly 1", got)
	}

	// Release the single healthy probe -> circuit closes immediately.
	close(probeGate.release)
	if err := <-probeDone; err != nil {
		t.Fatalf("half-open probe failed: %v", err)
	}
	if breaker.State() != breakerClosed {
		t.Fatalf("breaker state after healthy probe = %v, want closed", breaker.State())
	}

	// Subsequent searches succeed normally.
	for i := range 10 {
		hits, err := store.Search(context.Background(), routes, scope, req)
		if err != nil || len(hits) != 1 {
			t.Fatalf("post-recovery search %d = %v, %v", i, hits, err)
		}
	}
}

type panickingWeaviateClient struct {
	pb.WeaviateClient
}

func (panickingWeaviateClient) Search(context.Context, *pb.SearchRequest, ...grpc.CallOption) (*pb.SearchReply, error) {
	panic("simulated grpc client panic")
}

func TestGRPCDecodePanicRecoveredToErrUnavailable(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterWeaviateServer(srv, &controllableGRPCServer{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	store, closeStore, err := NewWithTransport("http://weaviate.test", lis.Addr().String(), outbound.TLS{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeStore)

	routes := []retrieval.Route{{CorpusID: "corpus", Generation: content.Generation{ID: "gen-1", Collection: "Example"}}}
	scope := corpus.Scope{Organization: "org"}
	req := retrieval.Request{Mode: "lexical", Query: "harbour"}

	// 1. Panic inside result decode path recovers to retrieval.ErrUnavailable.
	panicFn := func(*pb.SearchResult) {
		panic("simulated decode panic")
	}
	grpcDecodeRowHook.Store(&panicFn)
	t.Cleanup(func() { grpcDecodeRowHook.Store(nil) })

	_, err = store.Search(context.Background(), routes, scope, req)
	if !errors.Is(err, retrieval.ErrUnavailable) {
		t.Fatalf("decode panic err = %v, want retrieval.ErrUnavailable", err)
	}
	grpcDecodeRowHook.Store(nil)

	// 2. Panic inside grpc client Search recovers to retrieval.ErrUnavailable.
	store.grpc = panickingWeaviateClient{}
	_, err = store.Search(context.Background(), routes, scope, req)
	if !errors.Is(err, retrieval.ErrUnavailable) {
		t.Fatalf("client panic err = %v, want retrieval.ErrUnavailable", err)
	}
}

func TestGRPCPoolSkipsUnavailableConnections(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterWeaviateServer(srv, &controllableGRPCServer{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	store, closeStore, err := NewWithTransport("http://weaviate.test", lis.Addr().String(), outbound.TLS{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeStore)

	pool := store.grpcPooled().pool
	// Close connections 0, 1, and 2 (Shutdown state), leaving only conn 3 usable.
	for i := range 3 {
		_ = pool.conns[i].Close()
	}
	pool.inFlight.Store(10)
	for i := range 12 {
		if got := pool.pick(); got != pool.conns[3] {
			t.Fatalf("pick %d returned unavailable conn %p, want healthy conn %p", i, got, pool.conns[3])
		}
	}
	// When no connection is usable, pick falls back to round-robin without panicking.
	_ = pool.conns[3].Close()
	if got := pool.pick(); got == nil {
		t.Fatal("fallback pick returned nil")
	}
}
