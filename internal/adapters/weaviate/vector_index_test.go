package weaviate_test

import (
	"encoding/json"
	"net/http"
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
	space, large := "example.small@1", "example.large@1"
	before := content.Generation{ID: "generation-before", Collection: f.gen.Collection, ProfileVersion: retrieval.ProfileVersion, SpaceID: space, SourceNamespaceProjected: true, SpacesProjected: true,
		Spaces: []content.GenerationSpace{{ID: space, Metric: "cosine"}}}
	after := content.Generation{ID: "generation-after", Collection: f.gen.Collection, ProfileVersion: retrieval.ProfileVersion, SpaceID: space, SourceNamespaceProjected: true, SpacesProjected: true,
		Spaces: []content.GenerationSpace{
			{ID: space, Metric: "cosine", Index: &content.VectorIndex{Quantization: content.QuantizationRQ1, RescoreLimit: 64}},
			{ID: large, Metric: "dot", Index: &content.VectorIndex{Quantization: content.QuantizationNone}},
		}}
	f.publish(before, f.segmentation("before", "harbour strike at dawn"))
	f.publish(after, f.segmentation("after", "harbour strike at dusk"))
	if err := f.store.PublishEmbeddings(f.ctx, before, f.org, []content.EmbeddingData{spaceEmbedding(f, space, "before", smallVector(1, 4))}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PublishEmbeddings(f.ctx, after, f.org, []content.EmbeddingData{spaceEmbedding(f, space, "after", smallVector(2, 4)), spaceEmbedding(f, large, "after", smallVector(2, 6))}); err != nil {
		t.Fatal(err)
	}

	res, err := http.Get(f.url + "/v1/schema/" + f.gen.Collection)
	if err != nil {
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
	err = json.NewDecoder(res.Body).Decode(&schema)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
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
		{"rq-1 with a rescore limit", after, space, true, 1, 64},
		{"none", after, large, false, 0, 0},
	} {
		vector := f.store.VectorName(want.g, want.space)
		got, ok := schema.VectorConfig[vector]
		if !ok {
			t.Fatalf("%s: named vector %s missing from %v", want.name, vector, schema.VectorConfig)
		}
		rq := got.VectorIndexConfig.RQ
		if got.VectorIndexType != "hnsw" || rq.Enabled != want.rq || (want.rq && rq.Bits != want.bits) || (want.rescore > 0 && rq.RescoreLimit != want.rescore) || got.VectorIndexConfig.SkipDefaultQuantization == want.rq {
			t.Fatalf("%s: %s index %+v, want hnsw rq=%v bits=%d rescore=%d", want.name, vector, got, want.rq, want.bits, want.rescore)
		}
	}
	if f.store.VectorName(before, space) == f.store.VectorName(after, space) {
		t.Fatal("generations with different index settings share a named vector")
	}

	routes := []retrieval.Route{{CorpusID: f.corpusID, Generation: before}, {CorpusID: f.corpusID, Generation: after}}
	scope := corpus.Scope{Organization: f.org, Corpora: []string{"*"}}
	for _, mode := range []string{"semantic", "hybrid"} {
		candidates, err := f.store.Search(f.ctx, routes, scope, retrieval.Request{Query: "harbour", Mode: mode, Vector: smallVector(1, 4), Space: space})
		found := map[string]bool{}
		for _, c := range candidates {
			found[c.SegmentID] = true
		}
		if err != nil || !found["before"] || !found["after"] {
			t.Fatalf("%s search over both generations: %+v, %v; want both segments", mode, candidates, err)
		}
	}
}
