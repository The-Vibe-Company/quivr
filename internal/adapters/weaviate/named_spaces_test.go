package weaviate_test

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// smallVector returns a distinct unit vector of any size per seed.
func smallVector(seed, dimensions int) []float32 {
	v := make([]float32, dimensions)
	n := 0.0
	for i := range v {
		v[i] = float32(math.Cos(float64(seed*5+i) + 0.25))
		n += float64(v[i]) * float64(v[i])
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / math.Sqrt(n))
	}
	return v
}

func spaceEmbedding(f *attachFixture, space, segmentID string, vector []float32) content.EmbeddingData {
	raw, err := content.VectorBytes(vector)
	if err != nil {
		f.t.Fatal(err)
	}
	return content.EmbeddingData{Artifact: content.Embedding{Organization: f.org, SpaceID: space, SegmentID: segmentID, Payload: content.Blob{SHA256: content.Hash(raw)}}, Vector: vector}
}

// A collection created before named spaces keeps serving its generations
// while it gains the lexical text property and, in place, one named vector
// per new space. A generation built with named spaces stores each space's
// vectors apart; an object without a space's vector never answers a vector
// query in that space; and a generation of the built-in space stays
// searchable in one query with a generation built before named spaces.
func TestNamedSpacesLiveBesideAGenerationBuiltBefore(t *testing.T) {
	f := newAttachFixture(t)
	f.store.LegacySpace = "builtin"
	// The collection as the engine created it before named spaces: one
	// vector, no lexical text property.
	collection := "QuivrLegacy" + strconv.FormatInt(time.Now().UnixNano(), 36)
	properties := []any{}
	for _, name := range []string{"organization", "corpusId", "generationId", "segmentId", "versionId", "segmentationId", "sourceNamespace"} {
		properties = append(properties, map[string]any{"name": name, "dataType": []string{"text"}, "tokenization": "field", "indexFilterable": true, "indexSearchable": false})
	}
	for _, name := range []string{"title", "body"} {
		properties = append(properties, map[string]any{"name": name, "dataType": []string{"text"}, "tokenization": "word", "indexSearchable": true})
	}
	schema, _ := json.Marshal(map[string]any{"class": collection, "properties": properties, "vectorConfig": map[string]any{"semantic_text_v1": map[string]any{"vectorizer": map[string]any{"none": nil}, "vectorIndexType": "hnsw", "vectorIndexConfig": map[string]any{"distance": "cosine"}}}})
	res, err := http.Post(f.url+"/v1/schema", "application/json", bytes.NewReader(schema))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("create the legacy collection: %v %v", res, err)
	}
	res.Body.Close()
	legacy := content.Generation{ID: "generation-legacy", Collection: collection, ProfileVersion: retrieval.ProfileVersion, SpaceID: "builtin", SourceNamespaceProjected: true}
	f.publish(legacy, f.segmentation("old", "harbour strike at dawn"))
	if err = f.store.PublishEmbeddings(f.ctx, legacy, f.org, []content.EmbeddingData{spaceEmbedding(f, "builtin", "old", unitVector(1))}); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Bootstrap(f.ctx, collection); err != nil {
		t.Fatal(err)
	}

	// A rebuild of the built-in space into a projected generation.
	builtin := content.Generation{ID: "generation-builtin", Collection: collection, ProfileVersion: retrieval.ProfileVersion, SpaceID: "builtin", SourceNamespaceProjected: true, SpacesProjected: true,
		Spaces: []content.GenerationSpace{{ID: "builtin", Metric: "cosine"}}}
	f.publish(builtin, f.segmentation("rebuilt", "harbour strike at dawn"))
	if err = f.store.PublishEmbeddings(f.ctx, builtin, f.org, []content.EmbeddingData{spaceEmbedding(f, "builtin", "rebuilt", unitVector(1))}); err != nil {
		t.Fatal(err)
	}
	scope := corpus.Scope{Organization: f.org, Corpora: []string{"*"}}
	search := func(routes []retrieval.Route, q retrieval.Request) []string {
		t.Helper()
		q.Query = "harbour"
		candidates, err := f.store.Search(f.ctx, routes, scope, q)
		if err != nil {
			t.Fatalf("%s search in %q: %v", q.Mode, q.Space, err)
		}
		seen := map[string]bool{}
		for _, c := range candidates {
			seen[c.SegmentID] = true
		}
		out := []string{}
		for id := range seen {
			out = append(out, id)
		}
		sort.Strings(out)
		return out
	}
	both := []retrieval.Route{{CorpusID: f.corpusID, Generation: legacy}, {CorpusID: f.corpusID, Generation: builtin}}
	if got := search(both, retrieval.Request{Mode: "semantic", Vector: unitVector(1), Space: "builtin"}); len(got) != 2 {
		t.Fatalf("one query over a generation built before named spaces and one after: %v", got)
	}

	// A plugin's generation: two spaces, added to the collection in place.
	named := content.Generation{ID: "generation-named", Collection: collection, ProfileVersion: retrieval.ProfileVersion, SpaceID: "example.small@1", SourceNamespaceProjected: true, SpacesProjected: true,
		Spaces: []content.GenerationSpace{{ID: "example.small@1", Metric: "cosine"}, {ID: "example.large@1", Metric: "dot"}}}
	both2 := f.segmentation("both", "harbour strike at dawn")
	both2.Segments[0].Derivation.LexicalText = "harbour strike dawn"
	f.publish(named, both2)
	f.publish(named, f.segmentation("served-only", "harbour reopened"))
	if err = f.store.PublishEmbeddings(f.ctx, named, f.org, []content.EmbeddingData{
		spaceEmbedding(f, "example.small@1", "both", smallVector(1, 4)), spaceEmbedding(f, "example.large@1", "both", smallVector(1, 6)),
		spaceEmbedding(f, "example.small@1", "served-only", smallVector(2, 4)),
	}); err != nil {
		t.Fatal(err)
	}
	route := []retrieval.Route{{CorpusID: f.corpusID, Generation: named}}
	if got := search(route, retrieval.Request{Mode: "semantic", Vector: smallVector(1, 4), Space: "example.small@1"}); len(got) != 2 {
		t.Fatalf("served space vectors: %v", got)
	}
	if got := search(route, retrieval.Request{Mode: "semantic", Vector: smallVector(1, 6), Space: "example.large@1"}); len(got) != 1 || got[0] != "both" {
		t.Fatalf("an object without the evaluation space's vector answered a vector query in it: %v", got)
	}
	if got := search(route, retrieval.Request{Mode: "hybrid", Vector: smallVector(1, 4), Space: "example.small@1"}); len(got) != 2 {
		t.Fatalf("hybrid in the served space: %v", got)
	}
	// The anchor keeps the lexical text apart from the source text.
	var object struct {
		Properties map[string]any `json:"properties"`
	}
	where, _ := json.Marshal(map[string]any{"query": `{Get{` + collection + `(where:{path:["segmentId"],operator:Equal,valueText:"both"}){body lexicalText generationId}}}`})
	res, err = http.Post(f.url+"/v1/graphql", "application/json", bytes.NewReader(where))
	if err != nil {
		t.Fatal(err)
	}
	var rows struct {
		Data struct {
			Get map[string][]map[string]any `json:"Get"`
		} `json:"data"`
	}
	_ = json.NewDecoder(res.Body).Decode(&rows)
	res.Body.Close()
	found := false
	for _, row := range rows.Data.Get[collection] {
		if row["generationId"] == named.ID && row["lexicalText"] == "harbour strike dawn" && row["body"] == "harbour strike at dawn" {
			found = true
		}
	}
	if !found {
		t.Fatalf("lexical text not projected beside the source text: %+v %+v", rows.Data.Get[collection], object)
	}

	// A retrieval plugin's candidate requests: the lexical field ranks the
	// lexical text alone, k bounds the candidates, and every candidate
	// carries its score, best first.
	if got := search(route, retrieval.Request{Mode: "lexical", Field: retrieval.FieldLexical}); len(got) != 1 || got[0] != "both" {
		t.Fatalf("the lexical field matched %v; only the segment with lexical text holds it", got)
	}
	for _, q := range []retrieval.Request{
		{Query: "harbour", Mode: "lexical", K: 1},
		{Query: "harbour", Mode: "semantic", Vector: smallVector(1, 4), Space: "example.small@1", K: 1},
		{Query: "harbour", Mode: "hybrid", Vector: smallVector(1, 4), Space: "example.small@1", K: 1, Hybrid: &retrieval.HybridOptions{Alpha: 0.9, Fusion: retrieval.FusionRanked}},
	} {
		candidates, err := f.store.Search(f.ctx, route, scope, q)
		if err != nil || len(candidates) != 1 || candidates[0].Score <= 0 {
			t.Fatalf("%s candidates with k=1: %+v, %v; want one scored candidate", q.Mode, candidates, err)
		}
	}
}

// A new install's generation is served by a plugin space whose named vector
// the collection gains with the first vector write. A semantic or hybrid
// search before that write (Versions still waiting for their vectors) finds
// what it can, never an error.
func TestSearchBeforeTheFirstVectorOfASpace(t *testing.T) {
	f := newAttachFixture(t)
	g := content.Generation{ID: "generation-first", Collection: f.gen.Collection, ProfileVersion: retrieval.ProfileVersion, SpaceID: "example.first@1", SourceNamespaceProjected: true, SpacesProjected: true,
		Spaces: []content.GenerationSpace{{ID: "example.first@1", Metric: "cosine"}}}
	f.publish(g, f.segmentation("waiting", "harbour strike at dawn"))
	route := []retrieval.Route{{CorpusID: f.corpusID, Generation: g}}
	scope := corpus.Scope{Organization: f.org, Corpora: []string{"*"}}
	for mode, want := range map[string]int{"semantic": 0, "hybrid": 1} {
		candidates, err := f.store.Search(f.ctx, route, scope, retrieval.Request{Query: "harbour", Mode: mode, Vector: smallVector(1, 4), Space: "example.first@1"})
		if err != nil || len(candidates) != want {
			t.Fatalf("%s search before any vector: %d candidates, %v; want %d", mode, len(candidates), err, want)
		}
	}
}
