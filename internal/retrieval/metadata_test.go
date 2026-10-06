package retrieval_test

import (
	"context"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"testing"
)

// Owns route exclusion and request inheritance into plugin candidate rounds.
// Engine adapter tests own matching; this test never fakes that behavior.
func TestMetadataFiltersExcludeRoutesAndSurvivePluginRounds(t *testing.T) {
	p := &fakeProjection{}
	s := service(p, &fakeEmbeddings{})
	a := content.Generation{ID: "gen", Collection: "Shared", ProfileVersion: retrieval.ProfileVersion, SpaceID: "space", MetadataProjected: true, Fields: []corpus.Field{{Name: "urgency", Type: "number", Roles: []string{"filter"}}}}
	b := a
	b.Fields = nil
	s.Routing = spaceRouting{"a": a, "b": b}
	q := retrieval.Request{Query: "lanterne", Mode: "lexical", CorpusIDs: []string{"a", "b"}, Metadata: []corpus.MetadataFilter{{Field: "urgency", AnyOf: []any{2.0}}}}
	got, err := s.Search(context.Background(), searchScope, q)
	if err != nil || len(got.ExcludedCorpora) != 1 || got.ExcludedCorpora[0].CorpusID != "b" {
		t.Fatalf("exclusions %v %v", got.ExcludedCorpora, err)
	}
	if len(p.searched) != 1 || len(p.searched[0].Metadata) != 1 || p.searched[0].Metadata[0].Field != "urgency" || len(p.searched[0].CorpusIDs) != 1 || p.searched[0].CorpusIDs[0] != "a" {
		t.Fatalf("plugin candidate request lost scope/filter: %+v", p.searched)
	}
	a.MetadataProjected = false
	s.Routing = spaceRouting{"a": a, "b": b}
	if _, err = s.Search(context.Background(), searchScope, q); !errors.Is(err, retrieval.ErrMetadataFilterUnavailable) {
		t.Fatalf("legacy generation error %v", err)
	}
	q.Metadata = []corpus.MetadataFilter{{Field: "absent", AnyOf: []any{true}}}
	calls := len(p.searched)
	got, err = s.Search(context.Background(), searchScope, q)
	if err != nil || len(got.Hits) != 0 || len(got.ExcludedCorpora) != 2 || len(p.searched) != calls {
		t.Fatalf("all routes excluded: %+v %v, projection calls %d", got, err, len(p.searched))
	}
}
