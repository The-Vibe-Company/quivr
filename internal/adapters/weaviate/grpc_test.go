package weaviate_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
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
