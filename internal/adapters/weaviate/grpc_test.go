package weaviate_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/testutil/tlsfixture"
)

type grpcSearchFixture struct {
	pb.UnimplementedWeaviateServer
}

func (grpcSearchFixture) Search(ctx context.Context, request *pb.SearchRequest) (*pb.SearchReply, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if bearer := md.Get("authorization"); len(bearer) != 1 || bearer[0] != "Bearer example-api-key" {
		return nil, status.Error(codes.Unauthenticated, "bearer credential missing")
	}
	if request.GetCollection() != "Example" {
		return nil, status.Error(codes.InvalidArgument, "collection missing")
	}
	for _, filter := range request.GetFilters().GetFilters() {
		if filter.GetTarget().GetProperty() == "organization" && filter.GetValueText() == "organization" {
			return searchFixtureReply(), nil
		}
	}
	return nil, status.Error(codes.InvalidArgument, "organization filter missing")
}

func searchFixtureReply() *pb.SearchReply {
	return &pb.SearchReply{Results: []*pb.SearchResult{{
		Properties: &pb.PropertiesResult{NonRefProps: &pb.Properties{Fields: map[string]*pb.Value{
			"segmentId":    {Kind: &pb.Value_TextValue{TextValue: "segment"}},
			"generationId": {Kind: &pb.Value_TextValue{TextValue: "generation"}},
		}}},
		Metadata: &pb.MetadataResult{ScorePresent: true, Score: 2.5},
	}}}
}

func TestGRPCSearchUsesMutualTLSBearerAndProtocolMetrics(t *testing.T) {
	f := tlsfixture.New(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{f.Server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: f.Roots,
	})))
	pb.RegisterWeaviateServer(server, grpcSearchFixture{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	store, closeStore, err := weaviate.NewWithTransport("https://dependency.test", listener.Addr().String(),
		outbound.TLS{CAFile: f.CAFile, ServerName: "dependency.test", CertFile: f.CertFile, KeyFile: f.KeyFile}, "example-api-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeStore)
	metrics := telemetry.NewDependencyMetrics()
	store.SetMetrics(metrics)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	routes := []retrieval.Route{{CorpusID: "corpus", Generation: content.Generation{ID: "generation", Collection: "Example"}}}
	hits, err := store.Search(ctx, routes, corpus.Scope{Organization: "organization"}, retrieval.Request{Mode: "lexical", Query: "example"})
	if err != nil || len(hits) != 1 || hits[0].SegmentID != "segment" || hits[0].Score != 2.5 {
		t.Fatalf("gRPC search = %+v, %v", hits, err)
	}
	var out bytes.Buffer
	metrics.Write(&out)
	if !strings.Contains(out.String(), `quivr_dependency_calls_total{dependency="weaviate",protocol="grpc",operation="query"} 1`) {
		t.Fatalf("gRPC query counter missing:\n%s", &out)
	}
}

type grpcPublishFixture struct {
	pb.UnimplementedWeaviateServer
	mu               sync.Mutex
	anchorBatchCalls int
	anchors          map[string]*pb.Properties
	inserted         []*pb.BatchObject
}

func (f *grpcPublishFixture) Search(_ context.Context, req *pb.SearchRequest) (*pb.SearchReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.GetProperties().GetReturnAllNonrefProperties() {
		f.anchorBatchCalls++
		ids := req.GetFilters().GetValueTextArray().GetValues()
		results := make([]*pb.SearchResult, 0, len(ids))
		for _, id := range ids {
			if props, ok := f.anchors[id]; ok {
				results = append(results, &pb.SearchResult{
					Metadata:   &pb.MetadataResult{Id: id},
					Properties: &pb.PropertiesResult{NonRefProps: props},
				})
			}
		}
		return &pb.SearchReply{Results: results}, nil
	}
	return &pb.SearchReply{}, nil
}

func (f *grpcPublishFixture) BatchObjects(_ context.Context, req *pb.BatchObjectsRequest) (*pb.BatchObjectsReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inserted = append(f.inserted, req.GetObjects()...)
	return &pb.BatchObjectsReply{}, nil
}

// PublishEmbeddings with gRPC configured fetches all anchor properties in one
// batched gRPC Search call (ReturnAllNonrefProperties) with zero REST object GETs.
func TestPublishEmbeddingsUsesSingleGRPCBatchForAnchors(t *testing.T) {
	var restObjectGets atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/schema/BatchGRPC":
			_, _ = w.Write([]byte(`{"properties":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/schema/BatchGRPC/properties":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/objects/"):
			restObjectGets.Add(1)
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer httpServer.Close()

	g := content.Generation{ID: "gen-1", Collection: "BatchGRPC", SpaceID: "space"}
	fixture := &grpcPublishFixture{anchors: map[string]*pb.Properties{}}
	var data []content.EmbeddingData
	for _, segID := range []string{"seg-1", "seg-2", "seg-3"} {
		anchorID := weaviateAnchorIDForTest("org-1", g.ID, segID)
		fixture.anchors[anchorID] = &pb.Properties{Fields: map[string]*pb.Value{
			"organization": {Kind: &pb.Value_TextValue{TextValue: "org-1"}},
			"corpusId":     {Kind: &pb.Value_TextValue{TextValue: "corpus-1"}},
			"generationId": {Kind: &pb.Value_TextValue{TextValue: g.ID}},
			"segmentId":    {Kind: &pb.Value_TextValue{TextValue: segID}},
			"title":        {Kind: &pb.Value_TextValue{TextValue: "Title " + segID}},
			"body":         {Kind: &pb.Value_TextValue{TextValue: "Body " + segID}},
			"m_tag":        {Kind: &pb.Value_TextValue{TextValue: "news"}},
			"recordAcl":    {Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{Kind: &pb.ListValue_TextValues{TextValues: &pb.TextValues{Values: []string{"group:a"}}}}}},
		}}
		vec := []float32{0.5, 0.5}
		raw, err := content.VectorBytes(vec)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, content.EmbeddingData{
			Artifact: content.Embedding{
				Organization: "org-1", SpaceID: "space", SegmentID: segID,
				Payload: content.Blob{SHA256: content.Hash(raw)},
			},
			Vector: vec,
		})
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	pb.RegisterWeaviateServer(grpcServer, fixture)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)

	store, closeStore, err := weaviate.NewWithTransport(httpServer.URL, listener.Addr().String(), outbound.TLS{}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeStore)

	if err := store.PublishEmbeddings(context.Background(), g, "org-1", data); err != nil {
		t.Fatalf("PublishEmbeddings failed: %v", err)
	}
	if restObjectGets.Load() != 0 {
		t.Fatalf("expected 0 REST GET /v1/objects calls with gRPC, got %d", restObjectGets.Load())
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.anchorBatchCalls != 1 {
		t.Fatalf("expected 1 batched gRPC anchor Search, got %d", fixture.anchorBatchCalls)
	}
	if len(fixture.inserted) != 3 {
		t.Fatalf("expected 3 inserted embedding objects, got %d", len(fixture.inserted))
	}
	for _, obj := range fixture.inserted {
		fields := obj.GetProperties().GetNonRefProperties().GetFields()
		if fields["m_tag"].GetStringValue() != "news" || !strings.HasPrefix(fields["title"].GetStringValue(), "Title seg-") {
			t.Fatalf("anchor properties not copied to enriched object: %v", fields)
		}
		if len(obj.GetProperties().GetTextArrayProperties()) != 1 || obj.GetProperties().GetTextArrayProperties()[0].GetPropName() != "recordAcl" {
			t.Fatalf("recordAcl text array not preserved: %v", obj.GetProperties().GetTextArrayProperties())
		}
	}
}

func weaviateAnchorIDForTest(org, gen, seg string) string {
	h := content.Hash([]byte(content.StableID("projection", org, gen, seg)))
	return h[:8] + "-" + h[8:12] + "-5" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}

type blockingSearchFixture struct {
	pb.UnimplementedWeaviateServer
	entered    chan struct{}
	returnCode codes.Code
	reply      *pb.SearchReply
}

func (f *blockingSearchFixture) Search(ctx context.Context, _ *pb.SearchRequest) (*pb.SearchReply, error) {
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
	}
	if f.returnCode != codes.OK {
		return nil, status.Error(f.returnCode, "injected status error")
	}
	if f.reply != nil {
		return f.reply, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestGRPCContextCancelAndDeadlinePreserved(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &blockingSearchFixture{entered: make(chan struct{}, 1)}
	srv := grpc.NewServer()
	pb.RegisterWeaviateServer(srv, fixture)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	store, closeStore, err := weaviate.NewWithTransport("http://weaviate.test", lis.Addr().String(), outbound.TLS{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeStore)

	routes := []retrieval.Route{{CorpusID: "corpus", Generation: content.Generation{ID: "generation", Collection: "Example"}}}
	scope := corpus.Scope{Organization: "organization"}
	req := retrieval.Request{Mode: "lexical", Query: "example"}

	// 1. Caller cancellation preserves context.Canceled.
	cancelCtx, cancel := context.WithCancel(context.Background())
	go func() {
		<-fixture.entered
		cancel()
	}()
	_, err = store.Search(cancelCtx, routes, scope, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled search err = %v, want errors.Is(context.Canceled)", err)
	}

	// 2. Caller deadline preserves context.DeadlineExceeded.
	deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer deadlineCancel()
	_, err = store.Search(deadlineCtx, routes, scope, req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline search err = %v, want errors.Is(context.DeadlineExceeded)", err)
	}

	// 3. Server-returned codes.DeadlineExceeded preserves context.DeadlineExceeded.
	lis2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv2 := grpc.NewServer()
	pb.RegisterWeaviateServer(srv2, &blockingSearchFixture{returnCode: codes.DeadlineExceeded})
	go func() { _ = srv2.Serve(lis2) }()
	t.Cleanup(srv2.Stop)

	store2, closeStore2, err := weaviate.NewWithTransport("http://weaviate.test", lis2.Addr().String(), outbound.TLS{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeStore2)

	_, err = store2.Search(context.Background(), routes, scope, req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("server DeadlineExceeded err = %v, want errors.Is(context.DeadlineExceeded)", err)
	}

	// 4. Server-returned codes.Canceled without caller cancellation must not be
	// misclassified as context.Canceled.
	lis3, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv3 := grpc.NewServer()
	pb.RegisterWeaviateServer(srv3, &blockingSearchFixture{returnCode: codes.Canceled})
	go func() { _ = srv3.Serve(lis3) }()
	t.Cleanup(srv3.Stop)

	store3, closeStore3, err := weaviate.NewWithTransport("http://weaviate.test", lis3.Addr().String(), outbound.TLS{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeStore3)

	_, err = store3.Search(context.Background(), routes, scope, req)
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("server Canceled without caller cancel err = %v, want non-nil error not matching context.Canceled", err)
	}
}

func TestGRPCServerKilledMidStreamReturnsUnavailable(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &blockingSearchFixture{entered: make(chan struct{}, 1)}
	srv := grpc.NewServer()
	pb.RegisterWeaviateServer(srv, fixture)
	go func() { _ = srv.Serve(lis) }()

	store, closeStore, err := weaviate.NewWithTransport("http://weaviate.test", lis.Addr().String(), outbound.TLS{})
	if err != nil {
		srv.Stop()
		t.Fatal(err)
	}
	t.Cleanup(closeStore)

	routes := []retrieval.Route{{CorpusID: "corpus", Generation: content.Generation{ID: "generation", Collection: "Example"}}}
	scope := corpus.Scope{Organization: "organization"}
	req := retrieval.Request{Mode: "lexical", Query: "example"}

	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, searchErr := store.Search(ctx, routes, scope, req)
		errCh <- searchErr
	}()

	select {
	case <-fixture.entered:
	case <-time.After(2 * time.Second):
		srv.Stop()
		t.Fatal("request did not reach server")
	}

	// Hard-kill server mid-RPC.
	srv.Stop()

	select {
	case err = <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("search hung after server was killed mid-stream")
	}
	if !errors.Is(err, retrieval.ErrUnavailable) {
		t.Fatalf("mid-stream kill err = %v, want errors.Is(retrieval.ErrUnavailable)", err)
	}
}

type peerRecordingSearchFixture struct {
	pb.UnimplementedWeaviateServer
	reply       *pb.SearchReply
	barrierSize int32
	arrived     atomic.Int32
	release     chan struct{}
	releaseOnce sync.Once

	mu       sync.Mutex
	rpcPeers map[string]int
}

func (f *peerRecordingSearchFixture) Search(ctx context.Context, _ *pb.SearchRequest) (*pb.SearchReply, error) {
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		f.mu.Lock()
		if f.rpcPeers == nil {
			f.rpcPeers = make(map[string]int)
		}
		f.rpcPeers[p.Addr.String()]++
		f.mu.Unlock()
	}
	if f.barrierSize > 0 {
		if f.arrived.Add(1) >= f.barrierSize {
			f.releaseOnce.Do(func() { close(f.release) })
		}
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.reply, nil
}

func (f *peerRecordingSearchFixture) distinctRPCPeers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rpcPeers)
}

func TestGRPCPoolRoundRobinDistributesAcrossConnections(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Hold the first wave of 16 concurrent RPCs behind a barrier so inFlight > 4
	// is guaranteed and round-robin must dispatch actual RPCs across all 4 pooled
	// TCP connections (not merely eager dials).
	const workers = 16
	fixture := &peerRecordingSearchFixture{
		reply:       searchFixtureReply(),
		barrierSize: workers,
		release:     make(chan struct{}),
	}
	srv := grpc.NewServer()
	pb.RegisterWeaviateServer(srv, fixture)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	store, closeStore, err := weaviate.NewWithTransport("http://weaviate.test", lis.Addr().String(), outbound.TLS{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeStore)

	routes := []retrieval.Route{{CorpusID: "corpus", Generation: content.Generation{ID: "generation", Collection: "Example"}}}
	scope := corpus.Scope{Organization: "organization"}
	req := retrieval.Request{Mode: "lexical", Query: "example"}

	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	recordErr := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			hits, searchErr := store.Search(ctx, routes, scope, req)
			if searchErr != nil {
				recordErr(searchErr)
				return
			}
			if len(hits) != 1 {
				recordErr(fmt.Errorf("search returned %d hits, want 1", len(hits)))
				return
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		t.Fatalf("concurrent search failed: %v", firstErr)
	}
	if got := fixture.distinctRPCPeers(); got < 4 {
		t.Fatalf("server handled RPCs on %d distinct client peers, want >= 4 from pool fan-out", got)
	}
}

type trackingListener struct {
	net.Listener
	accepting     chan struct{}
	acceptingOnce sync.Once

	mu     sync.Mutex
	active int
	zeroCh chan struct{}
}

func (l *trackingListener) Accept() (net.Conn, error) {
	l.acceptingOnce.Do(func() { close(l.accepting) })
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.active++
	l.mu.Unlock()
	return &trackedConn{Conn: c, done: l.connClosed}, nil
}

func (l *trackingListener) connClosed() {
	l.mu.Lock()
	l.active--
	if l.active == 0 && l.zeroCh != nil {
		close(l.zeroCh)
		l.zeroCh = nil
	}
	l.mu.Unlock()
}

func (l *trackingListener) waitConnsClosed(timeout time.Duration) bool {
	l.mu.Lock()
	if l.active == 0 {
		l.mu.Unlock()
		return true
	}
	ch := make(chan struct{})
	l.zeroCh = ch
	l.mu.Unlock()

	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		l.mu.Lock()
		if l.zeroCh == ch {
			l.zeroCh = nil
		}
		l.mu.Unlock()
		return false
	}
}

type trackedConn struct {
	net.Conn
	done func()
	once sync.Once
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.done)
	return err
}

func TestGRPCPoolCloseReleasesGoroutines(t *testing.T) {
	rawLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lis := &trackingListener{
		Listener:  rawLis,
		accepting: make(chan struct{}),
	}
	srv := grpc.NewServer()
	pb.RegisterWeaviateServer(srv, &blockingSearchFixture{reply: searchFixtureReply()})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	select {
	case <-lis.accepting:
	case <-time.After(2 * time.Second):
		t.Fatal("gRPC server did not enter Accept")
	}
	runtime.GC()
	baseline := runtime.NumGoroutine()

	store, closeStore, err := weaviate.NewWithTransport("http://weaviate.test", lis.Addr().String(), outbound.TLS{})
	if err != nil {
		t.Fatal(err)
	}

	routes := []retrieval.Route{{CorpusID: "corpus", Generation: content.Generation{ID: "generation", Collection: "Example"}}}
	scope := corpus.Scope{Organization: "organization"}
	req := retrieval.Request{Mode: "lexical", Query: "example"}

	const totalSearches = 1000
	const workers = 16
	var next atomic.Int64
	var errMu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for {
				if next.Add(1) > totalSearches {
					return
				}
				if _, err := store.Search(context.Background(), routes, scope, req); err != nil {
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
	if firstErr != nil {
		closeStore()
		t.Fatalf("concurrent search failed: %v", firstErr)
	}

	// Closing twice must be idempotent and release every pooled connection's goroutines.
	closeStore()
	closeStore()

	if !lis.waitConnsClosed(2 * time.Second) {
		t.Fatal("server-side connections did not close after closeStore")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		runtime.GC()
		if cur := runtime.NumGoroutine(); cur <= baseline+2 {
			t.Logf("goroutines: baseline=%d after_close=%d", baseline, cur)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine leak after closeFn: baseline=%d current=%d (want <= %d)", baseline, runtime.NumGoroutine(), baseline+2)
		}
		runtime.Gosched()
	}
}
