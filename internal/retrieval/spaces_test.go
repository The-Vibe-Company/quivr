package retrieval_test

import (
	"context"
	"errors"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

// spaceRouting routes each Corpus to its own generation.
type spaceRouting map[string]content.Generation

func (spaceRouting) Authorize(context.Context, corpus.Scope, []string) error { return nil }
func (r spaceRouting) Generation(_ context.Context, _ string, id string) (content.Generation, error) {
	return r[id], nil
}

// pluginEncoder owns the example spaces and records what it encodes.
type pluginEncoder struct {
	err     error
	encoded []string
}

func (*pluginEncoder) Owns(space string) bool {
	return space == "example.small@1" || space == "example.large@1"
}
func (e *pluginEncoder) EncodeQuery(_ context.Context, org, space, query string) ([]float32, error) {
	e.encoded = append(e.encoded, org+"/"+space+"/"+query)
	return []float32{0.5, 0.5}, e.err
}

// countingEmbedder is the built-in space's encoder.
type countingEmbedder struct{ calls *int }

func (e countingEmbedder) Embed(context.Context, string) ([]float32, error) {
	*e.calls++
	return []float32{1}, nil
}
func (countingEmbedder) Space() content.VectorSpace { return content.VectorSpace{ID: "space"} }

func pluginGeneration(id string, projected bool, spaces ...string) content.Generation {
	g := content.Generation{ID: id, Collection: "Shared", ProfileVersion: retrieval.ProfileVersion, SpaceID: spaces[0], SourceNamespaceProjected: true, SpacesProjected: projected}
	for _, s := range spaces {
		g.Spaces = append(g.Spaces, content.GenerationSpace{ID: s, Metric: "cosine"})
	}
	return g
}

// spaceRegistry lists each routed generation's spaces, the served one first.
type spaceRegistry spaceRouting

func (r spaceRegistry) VectorSpaces(_ context.Context, _ string, id string) (content.Generation, []content.SpaceCoverage, int64, error) {
	g := r[id]
	out := []content.SpaceCoverage{}
	for _, space := range g.VectorSpaces() {
		c := content.SpaceCoverage{GenerationRole: content.SpaceEvaluation}
		if space == g.SpaceID {
			c.GenerationRole = content.SpaceServed
		}
		c.ID, c.Metric, c.QueryModalities = space, "cosine", []string{"text"}
		c.VectorSpace.Dimensions = 2
		out = append(out, c)
	}
	return g, out, 1, nil
}

// asking asks for one candidate request, then ranks what was served.
func asking(request map[string]any) func(context.Context, plugins.SearchRequest) ([]byte, error) {
	return func(_ context.Context, q plugins.SearchRequest) ([]byte, error) {
		if q.Round == 1 {
			return answer(map[string]any{"requests": []any{request}})
		}
		return answer(map[string]any{"ranking": map[string]any{"hits": []any{}}})
	}
}

// A query vector always comes from the model that made the document vectors:
// serving encodes it with the owner of the space the plugin names, and a
// space a routed generation does not carry is never offered.
func TestServingEncodesTheQueryWithTheSpaceOwner(t *testing.T) {
	routing := spaceRouting{
		"plugin":   pluginGeneration("gen-plugin", true, "example.small@1", "example.large@1"),
		"builtin":  pluginGeneration("gen-builtin", true, "space"),
		"legacy":   pluginGeneration("gen-legacy", false, "space"),
		"orphaned": pluginGeneration("gen-orphaned", true, "retired.space@2"),
	}
	hybrid := func(space string) map[string]any {
		return map[string]any{"primitive": "hybrid", "space": space, "query_text": "  lanterne\r\n", "k": 10}
	}
	for _, c := range []struct {
		name      string
		corpora   []string
		request   map[string]any
		noPlugin  bool
		encodeErr error
		wantErr   error
		space     string // space the projection ranks in
		encoded   string // what the plugin encoded
		builtin   int    // built-in encoder calls
	}{
		{name: "plugin-served corpus", corpora: []string{"plugin"}, request: hybrid("example.small@1"), space: "example.small@1", encoded: "org/example.small@1/lanterne"},
		{name: "evaluation space", corpora: []string{"plugin"}, request: map[string]any{"primitive": "near_vector", "space": "example.large@1", "query_text": "lanterne", "k": 10}, space: "example.large@1", encoded: "org/example.large@1/lanterne"},
		{name: "corpora not rebuilt since the built-in space", corpora: []string{"builtin", "legacy"}, request: hybrid("space"), space: "space", builtin: 1},
		{name: "a space the generation does not carry", corpora: []string{"builtin"}, request: hybrid("example.small@1"), wantErr: retrieval.ErrPluginInvalid},
		{name: "corpora served by different spaces", corpora: []string{"plugin", "builtin"}, request: hybrid("space"), wantErr: retrieval.ErrUnsupported},
		{name: "no owner pinned for a vector search", corpora: []string{"orphaned"}, request: hybrid("retired.space@2"), wantErr: retrieval.ErrUnsupported},
		{name: "no owner needed for a keyword search", corpora: []string{"orphaned"}, request: map[string]any{"primitive": "bm25", "query_text": "lanterne", "k": 10}, space: ""},
		{name: "ingestion plugin unpinned", corpora: []string{"plugin"}, request: hybrid("example.small@1"), noPlugin: true, wantErr: retrieval.ErrUnsupported},
		{name: "plugin refuses the query", corpora: []string{"plugin"}, request: hybrid("example.small@1"), encodeErr: content.ErrInvalid, wantErr: retrieval.ErrUnsupported, encoded: "org/example.small@1/lanterne"},
		{name: "plugin names its query limit", corpora: []string{"plugin"}, request: hybrid("example.small@1"), encodeErr: publicerr.WithDetail(retrieval.ErrQueryTooLong, "query exceeds 128 tokens"), wantErr: retrieval.ErrQueryTooLong, encoded: "org/example.small@1/lanterne"},
		{name: "plugin unavailable", corpora: []string{"plugin"}, request: hybrid("example.small@1"), encodeErr: errors.New("connection refused"), wantErr: retrieval.ErrUnavailable, encoded: "org/example.small@1/lanterne"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &fakeProjection{}
			calls := 0
			encoder := &pluginEncoder{err: c.encodeErr}
			s := retrieval.Service{Embedder: countingEmbedder{calls: &calls}, Routing: routing, Projection: p, Spaces: encoder,
				Ranker: &scriptedRanker{answer: asking(c.request)}, Registry: spaceRegistry(routing),
				Content: content.Service{Repository: fakeRecords{}, Baseline: fakeBaseline{}, Blobs: fakeBlobs{}, Embeddings: &fakeEmbeddings{}}}
			if c.noPlugin {
				s.Spaces = nil
			}
			_, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: c.corpora})
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("error %v, want %v", err, c.wantErr)
			}
			if got := len(encoder.encoded) > 0; got != (c.encoded != "") || (got && encoder.encoded[0] != c.encoded) {
				t.Fatalf("plugin encoded %v, want %q", encoder.encoded, c.encoded)
			}
			if calls != c.builtin {
				t.Fatalf("built-in encoder called %d times, want %d", calls, c.builtin)
			}
			if c.wantErr == nil && (len(p.searched) != 1 || p.searched[0].Space != c.space) {
				t.Fatalf("projection searched %+v, want space %q", p.searched, c.space)
			}
		})
	}
}
