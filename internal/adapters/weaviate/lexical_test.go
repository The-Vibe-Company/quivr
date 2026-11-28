package weaviate_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/grpc"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

type lexicalPatchHTTP struct {
	mu           sync.Mutex
	objects      map[string]map[string]any // objectID -> full stored object
	patchBodies  []map[string]any
	patchPaths   []string
	failPatch404 bool
}

func (h *lexicalPatchHTTP) RoundTrip(r *http.Request) (*http.Response, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	reply := func(status int, value any) (*http.Response, error) {
		raw, _ := json.Marshal(value)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
	}
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/schema/"):
		return reply(http.StatusOK, map[string]any{"properties": []any{map[string]any{"name": "lexicalText"}}})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/graphql":
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, err
		}
		collection := "LexicalTest"
		if strings.Contains(req.Query, weaviate.InitialCollection) {
			collection = weaviate.InitialCollection
		}
		rows := []map[string]any{}
		for id, obj := range h.objects {
			props, _ := obj["properties"].(map[string]any)
			segID, _ := props["segmentId"].(string)
			if !strings.Contains(req.Query, `"`+segID+`"`) {
				continue
			}
			lex, _ := props["lexicalText"].(string)
			rows = append(rows, map[string]any{
				"segmentId":   segID,
				"lexicalText": lex,
				"_additional": map[string]any{"id": id},
			})
		}
		return reply(http.StatusOK, map[string]any{"data": map[string]any{"Get": map[string]any{collection: rows}}})
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/objects/"):
		if h.failPatch404 {
			h.failPatch404 = false
			return reply(http.StatusNotFound, nil)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		h.patchBodies = append(h.patchBodies, body)
		h.patchPaths = append(h.patchPaths, r.URL.RequestURI())
		parts := strings.Split(r.URL.Path, "/")
		id := parts[len(parts)-1]
		obj, ok := h.objects[id]
		if !ok {
			return reply(http.StatusNotFound, nil)
		}
		props, _ := obj["properties"].(map[string]any)
		patchProps, _ := body["properties"].(map[string]any)
		for k, v := range patchProps {
			props[k] = v
		}
		return reply(http.StatusNoContent, nil)
	default:
		return reply(http.StatusNotFound, nil)
	}
}

// UpdateLexicalText patches only lexicalText (never vectors or source text),
// respects multi-tenant routing, skips already-converged objects on retry, and
// reports ErrProjectionMissing when a segment is absent or retired mid-patch.
func TestUpdateLexicalTextPatchesPropertyOnlyAndIsIdempotent(t *testing.T) {
	httpFake := &lexicalPatchHTTP{
		objects: map[string]map[string]any{
			"obj-1": {
				"id": "obj-1",
				"properties": map[string]any{
					"segmentId":   "seg-1",
					"passageText": "original canonical text",
					"lexicalText": "original",
				},
				"vectors": map[string][]float32{"s_vec": {1, 0, 0}},
			},
		},
	}
	store := weaviate.New("https://weaviate.test")
	store.Client.Transport = httpFake

	g := content.Generation{ID: "gen-1", Collection: weaviate.InitialCollection, SpaceID: "space@1", SpacesProjected: true, ItemKeywordsProjected: true}
	updates := []content.SegmentLexicalUpdate{{SegmentID: "seg-1", LexicalText: "original\nsynonym phrase"}}

	if err := store.UpdateLexicalText(context.Background(), g, "org-1", "corpus-1", updates); err != nil {
		t.Fatalf("UpdateLexicalText failed: %v", err)
	}
	if len(httpFake.patchBodies) != 1 {
		t.Fatalf("patch count = %d, want 1", len(httpFake.patchBodies))
	}
	body := httpFake.patchBodies[0]
	if _, hasVectors := body["vectors"]; hasVectors {
		t.Fatalf("PATCH body must never include vectors: %+v", body)
	}
	props, _ := body["properties"].(map[string]any)
	if len(props) != 1 || props["lexicalText"] != "original\nsynonym phrase" {
		t.Fatalf("PATCH must touch only lexicalText, got %+v", props)
	}
	if tenant, _ := body["tenant"].(string); !strings.Contains(httpFake.patchPaths[0], "tenant=") || tenant == "" {
		t.Fatalf("multi-tenant InitialCollection PATCH missing tenant: path=%q body=%+v", httpFake.patchPaths[0], body)
	}
	storedProps := httpFake.objects["obj-1"]["properties"].(map[string]any)
	if storedProps["passageText"] != "original canonical text" {
		t.Fatalf("source passageText mutated: %+v", storedProps)
	}

	// Idempotent retry: object already holds target lexicalText -> 0 PATCH calls.
	if err := store.UpdateLexicalText(context.Background(), g, "org-1", "corpus-1", updates); err != nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	if len(httpFake.patchBodies) != 1 {
		t.Fatalf("idempotent retry sent %d patches, want 0 additional", len(httpFake.patchBodies)-1)
	}

	// Missing segment returns ErrProjectionMissing.
	err := store.UpdateLexicalText(context.Background(), g, "org-1", "corpus-1", []content.SegmentLexicalUpdate{
		{SegmentID: "seg-missing", LexicalText: "expanded"},
	})
	if !errors.Is(err, retrieval.ErrProjectionMissing) {
		t.Fatalf("missing segment err = %v, want ErrProjectionMissing", err)
	}

	// Concurrent 404 during PATCH returns ErrProjectionMissing.
	httpFake.failPatch404 = true
	err = store.UpdateLexicalText(context.Background(), g, "org-1", "corpus-1", []content.SegmentLexicalUpdate{
		{SegmentID: "seg-1", LexicalText: "original\nsynonym phrase\nmore"},
	})
	if !errors.Is(err, retrieval.ErrProjectionMissing) {
		t.Fatalf("404 on PATCH err = %v, want ErrProjectionMissing", err)
	}
}

type grpcLexicalFixture struct {
	pb.UnimplementedWeaviateServer
	lexicalText string
}

func (f *grpcLexicalFixture) Search(_ context.Context, _ *pb.SearchRequest) (*pb.SearchReply, error) {
	return &pb.SearchReply{
		Results: []*pb.SearchResult{{
			Metadata: &pb.MetadataResult{Id: "obj-grpc-1"},
			Properties: &pb.PropertiesResult{NonRefProps: &pb.Properties{Fields: map[string]*pb.Value{
				"segmentId":   {Kind: &pb.Value_TextValue{TextValue: "seg-1"}},
				"lexicalText": {Kind: &pb.Value_TextValue{TextValue: f.lexicalText}},
			}}},
		}},
	}, nil
}

// grpcProjectionRows must populate LexicalText so gRPC retries recognize
// already-converged objects and skip redundant PATCH calls.
func TestUpdateLexicalTextGRPCReadsLexicalTextForIdempotency(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcFixture := &grpcLexicalFixture{lexicalText: "original\nsynonym phrase"}
	server := grpc.NewServer()
	pb.RegisterWeaviateServer(server, grpcFixture)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	httpFake := &lexicalPatchHTTP{
		objects: map[string]map[string]any{
			"obj-grpc-1": {
				"id": "obj-grpc-1",
				"properties": map[string]any{
					"segmentId":   "seg-1",
					"lexicalText": "original\nsynonym phrase",
				},
			},
		},
	}
	store, closeStore, err := weaviate.NewWithTransport("http://weaviate.test", listener.Addr().String(), outbound.TLS{}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeStore)
	store.Client.Transport = httpFake

	g := content.Generation{ID: "gen-1", Collection: "LexicalGRPC", SpaceID: "space@1", SpacesProjected: true}
	if err := store.UpdateLexicalText(context.Background(), g, "org-1", "corpus-1", []content.SegmentLexicalUpdate{
		{SegmentID: "seg-1", LexicalText: "original\nsynonym phrase"},
	}); err != nil {
		t.Fatalf("gRPC idempotent UpdateLexicalText failed: %v", err)
	}
	if len(httpFake.patchBodies) != 0 {
		t.Fatalf("gRPC retry sent %d patches for already-converged object, want 0", len(httpFake.patchBodies))
	}

	if err := store.UpdateLexicalText(context.Background(), g, "org-1", "corpus-1", []content.SegmentLexicalUpdate{
		{SegmentID: "seg-1", LexicalText: "original\nsynonym phrase\nadded"},
	}); err != nil {
		t.Fatalf("gRPC differing UpdateLexicalText failed: %v", err)
	}
	if len(httpFake.patchBodies) != 1 {
		t.Fatalf("gRPC differing update sent %d patches, want 1", len(httpFake.patchBodies))
	}
}

// Adapter verification against real Weaviate: updating LexicalText via
// UpdateLexicalText makes new expansion terms immediately searchable on BM25
// (FieldLexical) without recreating vectors or adding extra HNSW objects.
func TestUpdateLexicalTextSearchableOnBM25WithoutAddingVectors(t *testing.T) {
	f := newAttachFixture(t)
	g := content.Generation{
		ID:                       "generation-lexical-update",
		Collection:               f.gen.Collection,
		ProfileVersion:           retrieval.ProfileVersion,
		SpaceID:                  "example.lex@1",
		SourceNamespaceProjected: true,
		SpacesProjected:          true,
		ItemKeywordsProjected:    true,
		Spaces:                   []content.GenerationSpace{{ID: "example.lex@1", Metric: "cosine"}},
	}
	seg := f.segmentation("seg-solar", "solar panel installation")
	seg.Segments[0].Derivation.LexicalText = "solar panel installation"
	f.publish(g, seg)
	vec := smallVector(5, 4)
	if err := f.store.PublishEmbeddings(f.ctx, g, f.org, []content.EmbeddingData{spaceEmbedding(f, "example.lex@1", "seg-solar", vec)}); err != nil {
		t.Fatal(err)
	}
	route := []retrieval.Route{{CorpusID: f.corpusID, Generation: g}}
	scope := corpus.Scope{Organization: f.org, Corpora: []string{"*"}}

	// Before Phase-2 lexical enrichment, expansion term does not match BM25.
	before, err := f.store.Search(f.ctx, route, scope, retrieval.Request{Query: "photovoltaic", Mode: "lexical", Field: retrieval.FieldLexical})
	if err != nil || len(before) != 0 {
		t.Fatalf("pre-enrichment lexical search = %+v, %v; want 0 hits", before, err)
	}

	// Apply Phase-2 lexical update in-place without touching vectors.
	if err := f.store.UpdateLexicalText(f.ctx, g, f.org, f.corpusID, []content.SegmentLexicalUpdate{
		{SegmentID: "seg-solar", LexicalText: "solar panel installation\nphotovoltaic array"},
	}); err != nil {
		t.Fatalf("UpdateLexicalText: %v", err)
	}

	// Expansion term is immediately searchable on BM25.
	after, err := f.store.Search(f.ctx, route, scope, retrieval.Request{Query: "photovoltaic", Mode: "lexical", Field: retrieval.FieldLexical})
	if err != nil || len(after) != 1 || after[0].SegmentID != "seg-solar" {
		t.Fatalf("post-enrichment lexical search = %+v, %v; want seg-solar", after, err)
	}
	// Semantic search still returns the untouched dense embedding.
	sem, err := f.store.Search(f.ctx, route, scope, retrieval.Request{Query: "solar", Mode: "semantic", Vector: vec, Space: "example.lex@1"})
	if err != nil || len(sem) != 1 || sem[0].SegmentID != "seg-solar" {
		t.Fatalf("post-enrichment semantic search = %+v, %v; want seg-solar", sem, err)
	}
}
