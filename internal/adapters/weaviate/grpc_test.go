package weaviate_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"strings"
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

func (grpcSearchFixture) Search(ctx context.Context, _ *pb.SearchRequest) (*pb.SearchReply, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if bearer := md.Get("authorization"); len(bearer) != 1 || bearer[0] != "Bearer example-api-key" {
		return nil, status.Error(codes.Unauthenticated, "bearer credential missing")
	}
	return &pb.SearchReply{Results: []*pb.SearchResult{{
		Properties: &pb.PropertiesResult{NonRefProps: &pb.Properties{Fields: map[string]*pb.Value{
			"segmentId":    {Kind: &pb.Value_TextValue{TextValue: "segment"}},
			"generationId": {Kind: &pb.Value_TextValue{TextValue: "generation"}},
		}}},
		Metadata: &pb.MetadataResult{ScorePresent: true, Score: 2.5},
	}}}, nil
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
