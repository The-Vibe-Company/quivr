package weaviate_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// Schema creation and in-place reconciliation preserve the zero rq-8 default
// while honoring opt-in rescoreLimit and HNSW ef on both Bootstrap and named vectors.
func TestVectorIndexSchemaSerializationAndReconciliation(t *testing.T) {
	var mu sync.Mutex
	var schema map[string]any
	var puts int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/schema/IndexConfigTest":
			if schema == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(schema)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/schema":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			schema = body
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.Path == "/v1/schema/IndexConfigTest":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			schema = body
			puts++
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/graphql":
			_, _ = w.Write([]byte(`{"data":{"Get":{"IndexConfigTest":[]}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// 1. Default store creates legacy semantic_text_v1 with rq-8 rescoreLimit=0 and no ef override.
	store := weaviate.New(server.URL)
	if err := store.Bootstrap(context.Background(), "IndexConfigTest"); err != nil {
		t.Fatalf("default bootstrap failed: %v", err)
	}
	legacyCfg := func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		vc := schema["vectorConfig"].(map[string]any)["semantic_text_v1"].(map[string]any)
		return vc["vectorIndexConfig"].(map[string]any)
	}
	if cfg := legacyCfg(); cfg["rq"].(map[string]any)["rescoreLimit"] != float64(0) || cfg["ef"] != nil {
		t.Fatalf("default legacy vectorIndexConfig = %v; want rq.rescoreLimit=0 and omitted ef", cfg)
	}

	// 2. Opt-in DefaultIndex reconciles rescoreLimit and HNSW ef on existing collection.
	optIn := weaviate.New(server.URL)
	optIn.DefaultIndex = &content.VectorIndex{Quantization: content.QuantizationRQ8, RescoreLimit: 200, EF: 128}
	if err := optIn.Bootstrap(context.Background(), "IndexConfigTest"); err != nil {
		t.Fatalf("opt-in bootstrap reconciliation failed: %v", err)
	}
	if cfg := legacyCfg(); cfg["rq"].(map[string]any)["rescoreLimit"] != float64(200) || cfg["ef"] != float64(128) {
		t.Fatalf("reconciled legacy vectorIndexConfig = %v; want rq.rescoreLimit=200 ef=128", cfg)
	}

	// 3. Named vector added via Search(ensureVectors) serializes rescoreLimit and ef,
	// and reconciles ef in place when the recorded index setting changes.
	space := "example.space@1"
	gen := content.Generation{
		ID: "gen-1", Collection: "IndexConfigTest", SpaceID: space, SpacesProjected: true,
		Spaces: []content.GenerationSpace{{
			ID: space, Metric: "cosine",
			Index: &content.VectorIndex{Quantization: content.QuantizationRQ8, RescoreLimit: 64, EF: 256},
		}},
	}
	scope := corpus.Scope{Organization: "org-1", Corpora: []string{"*"}}
	if _, err := optIn.Search(context.Background(), []retrieval.Route{{CorpusID: "c1", Generation: gen}}, scope,
		retrieval.Request{Query: "test", Mode: "semantic", Space: space, Vector: []float32{1, 0}}); err != nil {
		t.Fatalf("semantic search ensureVectors failed: %v", err)
	}
	vecName := optIn.VectorName(gen, space)
	mu.Lock()
	namedCfg := schema["vectorConfig"].(map[string]any)[vecName].(map[string]any)["vectorIndexConfig"].(map[string]any)
	mu.Unlock()
	if namedCfg["rq"].(map[string]any)["rescoreLimit"] != float64(64) || namedCfg["ef"] != float64(256) {
		t.Fatalf("named vectorIndexConfig = %v; want rescoreLimit=64 ef=256", namedCfg)
	}

	// Reopen store with a higher EF on the same named vector and verify in-place PUT reconciliation.
	reopened := weaviate.New(server.URL)
	gen.Spaces[0].Index = &content.VectorIndex{Quantization: content.QuantizationRQ8, RescoreLimit: 64, EF: 512}
	if _, err := reopened.Search(context.Background(), []retrieval.Route{{CorpusID: "c1", Generation: gen}}, scope,
		retrieval.Request{Query: "test", Mode: "semantic", Space: space, Vector: []float32{1, 0}}); err != nil {
		t.Fatalf("reconciled semantic search failed: %v", err)
	}
	mu.Lock()
	namedCfg = schema["vectorConfig"].(map[string]any)[vecName].(map[string]any)["vectorIndexConfig"].(map[string]any)
	gotPuts := puts
	mu.Unlock()
	if namedCfg["ef"] != float64(512) || gotPuts < 3 {
		t.Fatalf("after ef reconciliation: cfg=%v puts=%d; want ef=512", namedCfg, gotPuts)
	}
}
