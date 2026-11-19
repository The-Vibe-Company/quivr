package weaviate_test

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// A space added to an existing collection is stored as its generation's
// index setting says, and a space recorded without one is compressed with
// the default. A generation built with another setting stores the space
// under another named vector, and one search over Corpora on both
// generations still answers from both.
func TestSpaceAddedLaterGetsItsIndexSetting(t *testing.T) {
	f := newAttachFixture(t)
	space, large, rescored := "example.small@1", "example.large@1", "example.rescored@1"
	before := content.Generation{ID: "generation-before", Collection: f.gen.Collection, ProfileVersion: retrieval.ProfileVersion, SpaceID: space, SourceNamespaceProjected: true, SpacesProjected: true,
		Spaces: []content.GenerationSpace{{ID: space, Metric: "cosine"},
			{ID: rescored, Metric: "cosine", Index: &content.VectorIndex{Quantization: content.QuantizationRQ8, RescoreLimit: 64}}}}
	after := content.Generation{ID: "generation-after", Collection: f.gen.Collection, ProfileVersion: retrieval.ProfileVersion, SpaceID: space, SourceNamespaceProjected: true, SpacesProjected: true,
		Spaces: []content.GenerationSpace{
			{ID: space, Metric: "cosine", Index: &content.VectorIndex{Quantization: content.QuantizationRQ1, RescoreLimit: 64}},
			{ID: large, Metric: "dot", Index: &content.VectorIndex{Quantization: content.QuantizationNone}},
		}}
	for _, g := range []content.Generation{before, after} {
		seg := f.segmentation(strings.TrimPrefix(g.ID, "generation-"), "harbour strike")
		seg.Segments[0].Derivation.LexicalText = "harbour strike"
		f.publish(g, seg)
	}
	if err := f.store.PublishEmbeddings(f.ctx, before, f.org, []content.EmbeddingData{spaceEmbedding(f, space, "before", smallVector(1, 4))}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PublishEmbeddings(f.ctx, after, f.org, []content.EmbeddingData{spaceEmbedding(f, space, "after", smallVector(2, 4)), spaceEmbedding(f, large, "after", smallVector(2, 6))}); err != nil {
		t.Fatal(err)
	}

	var schema struct {
		VectorConfig map[string]struct {
			VectorIndexType   string `json:"vectorIndexType"`
			VectorIndexConfig struct {
				SkipDefaultQuantization bool `json:"skipDefaultQuantization"`
				RQ                      struct {
					Enabled      bool `json:"enabled"`
					Bits         int  `json:"bits"`
					RescoreLimit int  `json:"rescoreLimit"`
				} `json:"rq"`
			} `json:"vectorIndexConfig"`
		} `json:"vectorConfig"`
	}
	if status := f.index.rest(t, http.MethodGet, "/v1/schema/"+f.gen.Collection, nil, &schema); status != http.StatusOK {
		t.Fatalf("read schema: status %d", status)
	}
	for _, want := range []struct {
		name    string
		g       content.Generation
		space   string
		rq      bool
		bits    int
		rescore int
	}{
		{"recorded without a setting", before, space, true, 8, 0},
		{"rq-8 with explicit rescoring", before, rescored, true, 8, 64},
		{"rq-1 with a rescore limit", after, space, true, 1, 64},
		{"none", after, large, false, 0, 0},
	} {
		vector := f.store.VectorName(want.g, want.space)
		got, ok := schema.VectorConfig[vector]
		if !ok {
			t.Fatalf("%s: named vector %s missing from %v", want.name, vector, schema.VectorConfig)
		}
		rq := got.VectorIndexConfig.RQ
		if got.VectorIndexType != "hnsw" || rq.Enabled != want.rq || (want.rq && rq.Bits != want.bits) || (want.rq && rq.RescoreLimit != want.rescore) || got.VectorIndexConfig.SkipDefaultQuantization == want.rq {
			t.Fatalf("%s: %s index %+v, want hnsw rq=%v bits=%d rescore=%d", want.name, vector, got, want.rq, want.bits, want.rescore)
		}
	}
	if f.store.VectorName(before, space) == f.store.VectorName(after, space) {
		t.Fatal("generations with different index settings share a named vector")
	}

	// Reopen the same populated collection as one created before the zero
	// default. Bootstrap owns only the legacy vector; the generation owns its
	// other settings. A rejected update must fail search and remain retryable.
	readSchema := func() map[string]any {
		t.Helper()
		var class map[string]any
		if status := f.index.rest(t, http.MethodGet, "/v1/schema/"+f.gen.Collection, nil, &class); status != http.StatusOK {
			t.Fatalf("read schema: status %d", status)
		}
		return class
	}
	class := readSchema()
	configs := class["vectorConfig"].(map[string]any)
	defaultName := f.store.VectorName(before, space)
	for _, name := range []string{"semantic_text_v1", defaultName} {
		configs[name].(map[string]any)["vectorIndexConfig"].(map[string]any)["rq"].(map[string]any)["rescoreLimit"] = 20
	}
	if status := f.index.rest(t, http.MethodPut, "/v1/schema/"+f.gen.Collection, class, nil); status != http.StatusOK {
		t.Fatalf("restore old schema: status %d", status)
	}
	reopened := f.index.open(t)
	if err := reopened.Bootstrap(f.ctx, f.gen.Collection); err != nil {
		t.Fatal(err)
	}
	configs = readSchema()["vectorConfig"].(map[string]any)
	for _, want := range []struct {
		name  string
		limit float64
	}{{"semantic_text_v1", 0}, {defaultName, 20}, {f.store.VectorName(before, rescored), 64}} {
		got := configs[want.name].(map[string]any)["vectorIndexConfig"].(map[string]any)["rq"].(map[string]any)["rescoreLimit"]
		if got != want.limit {
			t.Fatalf("bootstrap %s: rescoreLimit=%v, want %v", want.name, got, want.limit)
		}
	}
	// Only schema updates are intercepted, and they are REST: queries keep
	// going straight to the index over gRPC.
	upstream, _ := url.Parse(f.index.URL)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	var updates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && r.URL.Path == "/v1/schema/"+f.gen.Collection {
			switch updates.Add(1) {
			case 1:
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			case 2:
				// Acknowledged but unapplied: readback must catch it too.
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	defer server.Close()
	f.store = index{URL: server.URL, GRPC: f.index.GRPC, APIKey: f.index.APIKey}.open(t)
	scope := corpus.Scope{Organization: f.org, Corpora: []string{"*"}}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: before}}, scope,
			retrieval.Request{Query: "harbour", Mode: "semantic", Space: space, Vector: smallVector(1, 4)}); err == nil {
			t.Fatalf("search accepted failed or unapplied index setting update on attempt %d", attempt)
		}
	}

	routes := []retrieval.Route{{CorpusID: f.corpusID, Generation: before}, {CorpusID: f.corpusID, Generation: after}}
	for _, q := range []retrieval.Request{
		{Mode: "semantic", Vector: smallVector(1, 4), Space: space},
		{Mode: "hybrid", Vector: smallVector(1, 4), Space: space},
		{Mode: "lexical", Field: retrieval.FieldLexical},
	} {
		q.Query = "harbour"
		candidates, err := f.store.Search(f.ctx, routes, scope, q)
		found := map[string]bool{}
		for _, c := range candidates {
			found[c.SegmentID] = true
		}
		if err != nil || !found["before"] || !found["after"] {
			t.Fatalf("%s %s search over both generations: %+v, %v; want both segments", q.Mode, q.Field, candidates, err)
		}
	}
	configs = readSchema()["vectorConfig"].(map[string]any)
	if got := configs[defaultName].(map[string]any)["vectorIndexConfig"].(map[string]any)["rq"].(map[string]any)["rescoreLimit"]; got != float64(0) {
		t.Fatalf("existing named vector %s still rescores: %v", defaultName, got)
	}
}
