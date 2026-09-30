package retrieval_test

import (
	"context"
	"errors"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
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

// A query vector always comes from the model that made the document vectors:
// search encodes it with the owner of the space it ranks in, and refuses a
// space a routed generation does not carry.
func TestSearchEncodesTheQueryWithTheSpaceOwner(t *testing.T) {
	routing := spaceRouting{
		"plugin":   pluginGeneration("gen-plugin", true, "example.small@1", "example.large@1"),
		"builtin":  pluginGeneration("gen-builtin", true, "space"),
		"legacy":   pluginGeneration("gen-legacy", false, "space"),
		"orphaned": pluginGeneration("gen-orphaned", true, "retired.space@2"),
	}
	for _, c := range []struct {
		name      string
		request   retrieval.Request
		noPlugin  bool
		encodeErr error
		wantErr   error
		space     string // resolved space the projection ranks in
		encoded   string // what the plugin encoded
		builtin   int    // built-in encoder calls
	}{
		{name: "plugin-served corpus", request: retrieval.Request{CorpusIDs: []string{"plugin"}, Mode: "hybrid"}, space: "example.small@1", encoded: "org/example.small@1/lanterne"},
		{name: "evaluation space by name", request: retrieval.Request{CorpusIDs: []string{"plugin"}, Mode: "semantic", Space: "example.large@1"}, space: "example.large@1", encoded: "org/example.large@1/lanterne"},
		{name: "built-in corpus", request: retrieval.Request{CorpusIDs: []string{"builtin", "legacy"}, Mode: "hybrid"}, space: "space", builtin: 1},
		{name: "named space on a generation built before named spaces", request: retrieval.Request{CorpusIDs: []string{"builtin", "legacy"}, Mode: "semantic", Space: "space"}, wantErr: retrieval.ErrSpaceUnavailable},
		{name: "named space the generation does not carry", request: retrieval.Request{CorpusIDs: []string{"builtin"}, Mode: "semantic", Space: "example.small@1"}, wantErr: retrieval.ErrSpaceUnavailable},
		{name: "corpora served by different spaces", request: retrieval.Request{CorpusIDs: []string{"plugin", "builtin"}, Mode: "lexical"}, wantErr: retrieval.ErrUnsupported},
		{name: "no owner pinned for semantic search", request: retrieval.Request{CorpusIDs: []string{"orphaned"}, Mode: "semantic"}, wantErr: retrieval.ErrUnsupported},
		{name: "no owner needed for lexical search", request: retrieval.Request{CorpusIDs: []string{"orphaned"}, Mode: "lexical"}, space: "retired.space@2"},
		{name: "plugin unpinned", request: retrieval.Request{CorpusIDs: []string{"plugin"}, Mode: "hybrid"}, noPlugin: true, wantErr: retrieval.ErrUnsupported},
		{name: "plugin refuses the query", request: retrieval.Request{CorpusIDs: []string{"plugin"}, Mode: "hybrid"}, encodeErr: content.ErrInvalid, wantErr: retrieval.ErrUnsupported, encoded: "org/example.small@1/lanterne"},
		{name: "plugin names its query limit", request: retrieval.Request{CorpusIDs: []string{"plugin"}, Mode: "hybrid"}, encodeErr: publicerr.WithDetail(retrieval.ErrQueryTooLong, "query exceeds 128 tokens"), wantErr: retrieval.ErrQueryTooLong, encoded: "org/example.small@1/lanterne"},
		{name: "plugin unavailable", request: retrieval.Request{CorpusIDs: []string{"plugin"}, Mode: "hybrid"}, encodeErr: errors.New("connection refused"), wantErr: retrieval.ErrUnavailable, encoded: "org/example.small@1/lanterne"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &fakeProjection{}
			calls := 0
			encoder := &pluginEncoder{err: c.encodeErr}
			s := retrieval.Service{Embedder: countingEmbedder{calls: &calls}, QueryNormalizer: fakeNormalizer{}, Routing: routing, Projection: p, Spaces: encoder,
				Content: content.Service{Repository: fakeRecords{}, Baseline: fakeBaseline{}, Blobs: fakeBlobs{}, Embeddings: &fakeEmbeddings{}}}
			if c.noPlugin {
				s.Spaces = nil
			}
			q := c.request
			q.Query = "  lanterne\r\n"
			_, err := s.Search(context.Background(), searchScope, q)
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
