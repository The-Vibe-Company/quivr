package weaviate_test

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr/internal/buildinfo"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// Every request names the integration and, with a key, authenticates, on
// both transports the client library uses.
func TestRequestsIdentifyQuivrAndAuthenticate(t *testing.T) {
	for _, key := range []string{"", "index-secret"} {
		t.Run("key="+strconv.FormatBool(key != ""), func(t *testing.T) {
			fake := serve(t, func(w http.ResponseWriter, r *http.Request, _ wireRequest) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"version":"1.39.14","grpcMaxMessageSize":104858000}`))
			}, func(context.Context, wireRequest, []byte) ([]byte, error) {
				return nil, status.Error(codes.Internal, "the fake index answers no query")
			})
			fake.APIKey = key
			store := fake.open(t)
			if err := store.Ready(context.Background()); err != nil {
				t.Fatal(err)
			}
			g := content.Generation{ID: "generation", Collection: "QuivrTextV4", ProfileVersion: retrieval.ProfileVersion}
			if _, err := store.Search(context.Background(), []retrieval.Route{{CorpusID: "corpus", Generation: g}}, corpus.Scope{Organization: "org"}, retrieval.Request{Query: "lantern", Mode: "lexical"}); err == nil {
				t.Fatal("the fake index refused the query")
			}
			transports := map[string]bool{}
			for _, request := range fake.Requests() {
				transports[request.Transport] = true
				if got := request.Header.Get(weaviate.IntegrationHeader); got != "quivr/"+buildinfo.Version {
					t.Fatalf("%s %s: integration header %q", request.Transport, request.Name, got)
				}
				want := ""
				if key != "" {
					want = "Bearer " + key
				}
				if got := request.Header.Get("Authorization"); got != want {
					t.Fatalf("%s %s: authorization header %q, want %q", request.Transport, request.Name, got, want)
				}
			}
			if !transports["rest"] || !transports["grpc"] {
				t.Fatalf("requests seen on %v, want REST and gRPC", transports)
			}
		})
	}
}

func TestConfigRefusesWhatTheClientCannotHonour(t *testing.T) {
	for name, cfg := range map[string]weaviate.Config{
		"not a URL":          {URL: "index.example:8080"},
		"path prefix":        {URL: "https://index.example/weaviate"},
		"credentials in URL": {URL: "https://user:pass@index.example"},
		"custom CA":          {URL: "https://index.example", TLS: outbound.TLS{CAFile: "/etc/ca.pem"}},
		"client certificate": {URL: "https://index.example", TLS: outbound.TLS{CertFile: "/etc/c.pem", KeyFile: "/etc/k.pem"}},
		"server name":        {URL: "https://index.example", TLS: outbound.TLS{ServerName: "index.internal"}},
		"gRPC without port":  {URL: "https://index.example", GRPCAddress: "index.example"},
	} {
		_, err := weaviate.New(cfg)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		// Only a gRPC address failure names that setting to the operator.
		if errors.Is(err, weaviate.ErrGRPCAddress) != (name == "gRPC without port") {
			t.Errorf("%s: reported as %v", name, err)
		}
	}
	for name, cfg := range map[string]weaviate.Config{
		"plain":          {URL: "http://index.example:8080"},
		"system trust":   {URL: "https://index.example"},
		"separate gRPC":  {URL: "https://index.example", GRPCAddress: "grpc.index.example:443"},
		"IPv6 literal":   {URL: "http://[::1]:8080", GRPCAddress: "[::1]:50051"},
		"trailing slash": {URL: "http://index.example:8080/"},
	} {
		if _, err := weaviate.New(cfg); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A full ingest, search and purge sends nothing to the GraphQL endpoint.
func TestNoRequestUsesGraphQL(t *testing.T) {
	proxy := realIndex(t).proxy(t)
	store := proxy.open(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	collection := "QuivrWire" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := store.Bootstrap(ctx, collection); err != nil {
		t.Fatal(err)
	}
	g := content.Generation{ID: "generation-wire", Collection: collection, ProfileVersion: retrieval.ProfileVersion, SpaceID: "space-wire"}
	seg := content.Segmentation{ID: "segmentation-wire", VersionID: "version-wire", Segments: []content.Segment{{ID: "segment-wire", PartKey: "body", Text: "a lantern on the wire"}}}
	if err := store.Publish(ctx, g, "org-wire", "corpus-wire", "example-feed", content.Version{ID: seg.VersionID}, seg); err != nil {
		t.Fatal(err)
	}
	vector := unitVector(3)
	raw, err := content.VectorBytes(vector)
	if err != nil {
		t.Fatal(err)
	}
	embedding := content.EmbeddingData{Artifact: content.Embedding{Organization: "org-wire", SpaceID: g.SpaceID, SegmentID: "segment-wire", Payload: content.Blob{SHA256: content.Hash(raw)}}, Vector: vector}
	if err = store.PublishEmbeddings(ctx, g, "org-wire", []content.EmbeddingData{embedding}); err != nil {
		t.Fatal(err)
	}
	routes := []retrieval.Route{{CorpusID: "corpus-wire", Generation: g}}
	for _, mode := range []string{"lexical", "semantic", "hybrid"} {
		rows, err := store.Search(ctx, routes, corpus.Scope{Organization: "org-wire"}, retrieval.Request{Query: "lantern", Vector: vector, Mode: mode, K: 5})
		if err != nil || len(rows) == 0 {
			t.Fatalf("%s search: %v %v", mode, rows, err)
		}
	}
	// Each purge needs objects to select, so the version is published again
	// between them.
	if purged, err := store.PurgeGeneration(ctx, collection, "org-wire", "corpus-wire", g.ID); err != nil || purged.Deleted == 0 {
		t.Fatalf("generation purge: %+v %v", purged, err)
	}
	if err = store.Publish(ctx, g, "org-wire", "corpus-wire", "example-feed", content.Version{ID: seg.VersionID}, seg); err != nil {
		t.Fatal(err)
	}
	if purged, err := store.PurgeVersion(ctx, collection, "org-wire", seg.VersionID); err != nil || purged.Deleted == 0 {
		t.Fatalf("version purge: %+v %v", purged, err)
	}
	searches := 0
	for _, request := range proxy.Requests() {
		if strings.Contains(strings.ToLower(request.Name), "graphql") {
			t.Fatalf("request to %s", request.Name)
		}
		if request.Name == "/weaviate.v1.Weaviate/Search" {
			searches++
		}
	}
	if searches == 0 {
		t.Fatalf("no search went over gRPC: %+v", proxy.Requests())
	}
	if err = errors.Join(ctx.Err()); err != nil {
		t.Fatal(err)
	}
}
