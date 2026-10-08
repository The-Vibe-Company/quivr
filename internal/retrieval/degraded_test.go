package retrieval_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// The storage boundary describes a corpus whose owner has no served projection,
// even before the asynchronous vector-coverage snapshot has completed.
type rebuildingRegistry struct {
	routing      spaceRouting
	missing      map[string]bool
	absent       bool
	zeroCoverage bool
}

func (r rebuildingRegistry) VectorSpaces(ctx context.Context, org, id string) (content.Generation, []content.SpaceCoverage, int64, error) {
	g, spaces, total, err := (spaceRegistry{routing: r.routing}).VectorSpaces(ctx, org, id)
	if r.absent {
		return g, nil, total, err
	}
	for i := range spaces {
		spaces[i].CoverageUnknown = true
		if r.zeroCoverage {
			one := int64(1)
			spaces[i].CoverageUnknown = false
			spaces[i].TotalSegments = &one
			spaces[i].ServingSegments = &one
			continue
		}
		if r.missing[id] {
			zero := int64(0)
			spaces[i].ServingSegments = &zero
		}
	}
	return g, spaces, total, err
}

type rebuildProjection struct {
	fakeProjection
	missing      map[string]bool
	keywordScore float64
	err          error
}

func (p *rebuildProjection) Search(_ context.Context, routes []retrieval.Route, _ corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	p.searched = append(p.searched, q)
	if p.err != nil {
		return nil, p.err
	}
	out := []content.Candidate{}
	for _, r := range routes {
		if r.CorpusID == "rebuilding" {
			if p.missing[r.CorpusID] && q.Mode != "lexical" {
				return nil, errors.New("no vectors in rebuilt generation")
			}
			score := p.keywordScore
			if score == 0 {
				score = 1
			}
			out = append(out, content.Candidate{SegmentID: "keyword", GenerationID: r.Generation.ID, Score: score})
		} else {
			out = append(out, content.Candidate{SegmentID: "vec-healthy", GenerationID: r.Generation.ID, Score: 2})
		}
	}
	return out, nil
}

// Search owns the fallback: dependency fakes provide only storage metadata and
// index rows. Losing the no-space fallback or routing a vector request to the
// rebuilding corpus must fail this test before any response-marker assertion.
func TestHybridSearchIncludesCorporaWithoutVectors(t *testing.T) {
	for _, tc := range []struct {
		name         string
		ids          []string
		mode         string
		sameSpace    bool
		absent       bool
		zeroCoverage bool
		keywordScore float64
		twoSpaces    bool
		otherFilter  bool
		indexErr     error
		wantErr      error
	}{
		{name: "rebuilt corpus", ids: []string{"rebuilding"}},
		{name: "no registered space", ids: []string{"rebuilding"}, absent: true},
		{name: "known zero vector coverage", ids: []string{"rebuilding"}, zeroCoverage: true},
		{name: "different spaces", ids: []string{"healthy", "rebuilding"}},
		{name: "different spaces reversed", ids: []string{"rebuilding", "healthy"}},
		{name: "incomparable keyword score scale", ids: []string{"healthy", "rebuilding"}, keywordScore: 1000},
		{name: "two healthy spaces share one keyword query", ids: []string{"healthy", "rebuilding"}, twoSpaces: true},
		{name: "distinct source filters keep distinct keyword queries", ids: []string{"healthy", "rebuilding"}, twoSpaces: true, otherFilter: true},
		{name: "shared space", ids: []string{"healthy", "rebuilding"}, sameSpace: true},
		{name: "shared space reversed", ids: []string{"rebuilding", "healthy"}, sameSpace: true},
		{name: "unknown coverage with a served projection", ids: []string{"healthy"}},
		{name: "explicit lexical across spaces", ids: []string{"rebuilding", "healthy"}, mode: "lexical"},
		{name: "explicit semantic remains unsupported", ids: []string{"rebuilding"}, mode: "semantic", wantErr: retrieval.ErrUnsupported},
		{name: "shared semantic remains unsupported", ids: []string{"healthy", "rebuilding"}, sameSpace: true, mode: "semantic", wantErr: retrieval.ErrUnsupported},
		{name: "shared semantic reversed remains unsupported", ids: []string{"rebuilding", "healthy"}, sameSpace: true, mode: "semantic", wantErr: retrieval.ErrUnsupported},
		{name: "keyword index failure stays unavailable", ids: []string{"rebuilding"}, indexErr: errors.New("index unavailable"), wantErr: retrieval.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids := tc.ids
			routes := spaceRouting{
				"healthy":    pluginGeneration("healthy-generation", true, "space"),
				"rebuilding": pluginGeneration("rebuilt-generation", true, "example.small@1"),
			}
			if tc.sameSpace {
				routes["rebuilding"] = pluginGeneration("rebuilt-generation", true, "space")
			}
			if tc.twoSpaces {
				g := pluginGeneration("healthy-generation", true, "space", "example.large@1")
				g.Spaces[1].Role = content.SpaceServed
				routes["healthy"] = g
			}
			missing := map[string]bool{"rebuilding": true}
			projection := &rebuildProjection{missing: missing, keywordScore: tc.keywordScore, err: tc.indexErr}
			s := service(&projection.fakeProjection, &fakeEmbeddings{})
			s.Projection = projection
			s.Routing = routes
			s.Registry = rebuildingRegistry{routing: routes, missing: missing, absent: tc.absent, zeroCoverage: tc.zeroCoverage}
			s.Spaces = &pluginEncoder{}
			if tc.twoSpaces {
				s.Ranker = &scriptedRanker{answer: func(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
					if q.Round != 1 {
						return passthrough(ctx, q)
					}
					requests := []any{}
					for _, space := range q.Spaces {
						if space.Role != content.SpaceServed {
							continue
						}
						r := map[string]any{"primitive": "hybrid", "space": space.ID, "query_text": q.Query.Text, "k": q.Limit}
						if tc.otherFilter && len(requests) > 0 {
							r["filter"] = map[string]any{"source_namespaces": []string{"feed-a"}}
						}
						requests = append(requests, r)
					}
					return answer(map[string]any{"requests": requests})
				}}
			}
			result, err := s.Search(t.Context(), searchScope, retrieval.Request{Query: "lanterne", Mode: tc.mode, CorpusIDs: ids})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("search error %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || len(result.Hits) != len(ids) {
				t.Fatalf("hybrid returned %d hits, error %v; want keyword results from every corpus (%v)", len(result.Hits), err, ids)
			}
			found := map[string]bool{}
			for _, h := range result.Hits {
				found[h.Segment.ID] = true
			}
			if ids[0] != "healthy" && !found["keyword"] || len(ids) == 2 && (!found["keyword"] || !found["vec-healthy"]) {
				t.Fatalf("hits %v; want rebuilt corpus keywords and healthy corpus vectors", found)
			}
			var degraded []retrieval.Degradation
			if tc.mode != "lexical" && (len(ids) == 2 || ids[0] == "rebuilding") {
				degraded = []retrieval.Degradation{{Reason: "vectors_unavailable", CorpusIDs: []string{"rebuilding"}}}
			}
			if !reflect.DeepEqual(result.Degraded, degraded) {
				t.Fatalf("degraded %+v, want %+v", result.Degraded, degraded)
			}
			if len(degraded) > 0 && len(ids) == 2 {
				// Both partitions supplied their best hit. Their different raw index
				// score scales must not decide the cross-partition ranking.
				if result.Hits[0].Score != result.Hits[1].Score {
					t.Fatalf("best keyword and hybrid candidates have incomparable scores %g and %g", result.Hits[0].Score, result.Hits[1].Score)
				}
			}
			if tc.twoSpaces {
				keywords := 0
				for _, query := range projection.searched {
					if query.Mode == "lexical" {
						keywords++
					}
				}
				want := 1
				if tc.otherFilter {
					want = 2
				}
				if keywords != want {
					t.Fatalf("%d keyword index queries for two healthy spaces, want %d", keywords, want)
				}
			}
			// The next search observes repaired routing without retaining a marker.
			if len(ids) == 1 && ids[0] == "rebuilding" && !tc.absent && !tc.zeroCoverage {
				delete(missing, "rebuilding")
				again, err := s.Search(t.Context(), searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: ids})
				if err != nil || len(again.Hits) != 1 || len(again.Degraded) != 0 {
					t.Fatalf("after repair: %d hits, degraded %+v, error %v", len(again.Hits), again.Degraded, err)
				}
			}
		})
	}
}
