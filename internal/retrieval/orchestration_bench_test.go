package retrieval_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
)

// latentControlPlane is the routing and space registry of a search, each call
// answering after delay(corpus). It counts calls and the most in flight.
type latentControlPlane struct {
	delay func(corpusID string) time.Duration
	// evaluation gives each Corpus its own evaluation space, so the order of
	// the spaces offered to the ranker shows the order routes were resolved in.
	evaluation bool
	// refuse fails a Corpus's routing read; stale routes it to a generation of
	// another recipe.
	refuse map[string]bool
	stale  map[string]bool

	authorizations, generations, coverage atomic.Int64
	inFlight, peak                        atomic.Int64
}

func (c *latentControlPlane) wait(ctx context.Context, id string) error {
	n := c.inFlight.Add(1)
	defer c.inFlight.Add(-1)
	for p := c.peak.Load(); n > p && !c.peak.CompareAndSwap(p, n); p = c.peak.Load() {
	}
	if c.delay == nil {
		return nil
	}
	timer := time.NewTimer(c.delay(id))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *latentControlPlane) Authorize(ctx context.Context, _ corpus.Scope, _ []string) error {
	c.authorizations.Add(1)
	return c.wait(ctx, "")
}

func (c *latentControlPlane) generation(id string) content.Generation {
	g := pluginGeneration("gen-"+id, true, "space")
	g.Spaces[0].Role = content.SpaceServed
	if c.evaluation {
		g.Spaces = append(g.Spaces, content.GenerationSpace{ID: "eval-" + id, Metric: "cosine", Role: content.SpaceEvaluation})
	}
	if c.stale[id] {
		g.ProfileVersion = "another.recipe.v0"
	}
	return g
}

func (c *latentControlPlane) Generation(ctx context.Context, _ string, id string) (content.Generation, error) {
	c.generations.Add(1)
	if err := c.wait(ctx, id); err != nil {
		return content.Generation{}, err
	}
	if c.refuse[id] {
		return content.Generation{}, errors.New("routing store unavailable")
	}
	return c.generation(id), nil
}

func (c *latentControlPlane) VectorSpaces(ctx context.Context, _ string, id string) (content.Generation, []content.SpaceCoverage, int64, error) {
	c.coverage.Add(1)
	if err := c.wait(ctx, id); err != nil {
		return content.Generation{}, nil, 0, err
	}
	g := c.generation(id)
	out := []content.SpaceCoverage{}
	for _, sp := range g.Spaces {
		cov := content.SpaceCoverage{GenerationRole: sp.Role, Segments: 1}
		cov.ID, cov.Metric, cov.QueryModalities = sp.ID, "cosine", []string{"text"}
		cov.VectorSpace.Dimensions = 1
		out = append(out, cov)
	}
	return g, out, 1, nil
}

func controlPlaneService(cp *latentControlPlane, r *scriptedRanker) retrieval.Service {
	s := service(&fakeProjection{}, &fakeEmbeddings{})
	s.Routing, s.Registry, s.Ranker = cp, cp, r
	return s
}

func corpusIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = "c" + strconv.Itoa(i)
	}
	return ids
}

// A search reads each Corpus's routing and space coverage. Those reads are
// independent, so with 2 ms of storage latency each, a 16-Corpus search must
// not pay 33 serial round trips (authorization, then routing and coverage
// per Corpus), while issuing exactly as many reads, at
// most four at once, and offering the ranker routes in request order however
// the reads interleave. The fake clock makes the latency budget exact.
func TestSerialControlPlane(t *testing.T) {
	const latency = 2 * time.Millisecond
	for _, tc := range []struct {
		corpora int
		budget  time.Duration
	}{
		// One Corpus: authorize, route, cover; the fan-out must add nothing.
		{corpora: 1, budget: 3*latency + time.Millisecond},
		{corpora: 4, budget: 12 * time.Millisecond},
		{corpora: 16, budget: 25 * time.Millisecond},
	} {
		t.Run(fmt.Sprintf("%d corpora", tc.corpora), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cp := &latentControlPlane{delay: func(string) time.Duration { return latency }}
				r := &scriptedRanker{answer: passthrough}
				started := time.Now()
				_, err := controlPlaneService(cp, r).Search(t.Context(), searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: corpusIDs(tc.corpora)})
				elapsed := time.Since(started)
				if err != nil {
					t.Fatal(err)
				}
				if a, g, c := cp.authorizations.Load(), cp.generations.Load(), cp.coverage.Load(); a != 1 || g != int64(tc.corpora) || c != int64(tc.corpora) {
					t.Fatalf("control plane reads: %d authorizations, %d routings, %d coverages; want 1, %d, %d", a, g, c, tc.corpora, tc.corpora)
				}
				if peak := cp.peak.Load(); peak > 4 {
					t.Fatalf("%d control-plane reads in flight at once, want at most 4", peak)
				}
				t.Logf("%d corpora: control plane %v (budget %v), %d reads in flight at most", tc.corpora, elapsed, tc.budget, cp.peak.Load())
				if elapsed > tc.budget {
					t.Fatalf("control plane of %d corpora took %v at %v per read, want at most %v", tc.corpora, elapsed, latency, tc.budget)
				}
			})
		})
	}

	t.Run("routes keep request order", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ids := corpusIDs(6)
			// Later Corpora answer first.
			cp := &latentControlPlane{evaluation: true, delay: func(id string) time.Duration {
				i, _ := strconv.Atoi(strings.TrimPrefix(id, "c"))
				return time.Duration(len(ids)-i) * time.Millisecond
			}}
			r := &scriptedRanker{answer: passthrough}
			if _, err := controlPlaneService(cp, r).Search(t.Context(), searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: ids}); err != nil {
				t.Fatal(err)
			}
			want := []string{"space"}
			for _, id := range ids {
				want = append(want, "eval-"+id)
			}
			var got []string
			for _, sp := range r.sent[0].Spaces {
				got = append(got, sp.ID)
			}
			if !slices.Equal(got, want) || !slices.Equal(r.sent[0].Scope.CorpusIDs, ids) {
				t.Fatalf("ranker offered spaces %v for corpora %v; want %v for %v", got, r.sent[0].Scope.CorpusIDs, want, ids)
			}
		})
	})

	// The lowest failing Corpus decides the error, as when routes were read
	// one by one, even though a later one fails first.
	t.Run("lowest corpus error wins", func(t *testing.T) {
		for _, tc := range []struct {
			name          string
			refuse, stale string
			want          error
		}{
			{name: "unavailable before stale", refuse: "c1", stale: "c3", want: retrieval.ErrUnavailable},
			{name: "stale before unavailable", refuse: "c3", stale: "c1", want: retrieval.ErrUnsupported},
		} {
			t.Run(tc.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ids := corpusIDs(6)
					cp := &latentControlPlane{refuse: map[string]bool{tc.refuse: true}, stale: map[string]bool{tc.stale: true}, delay: func(id string) time.Duration {
						i, _ := strconv.Atoi(strings.TrimPrefix(id, "c"))
						return time.Duration(len(ids)-i) * time.Millisecond
					}}
					_, err := controlPlaneService(cp, &scriptedRanker{answer: passthrough}).Search(t.Context(), searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: ids})
					if !errors.Is(err, tc.want) {
						t.Fatalf("search error %v, want %v from the lowest failing corpus", err, tc.want)
					}
				})
			})
		}
	})
}

// principalProjection records the principals each keyword query ran under.
type principalProjection struct {
	*rebuildProjection
	mu      sync.Mutex
	keyword [][]string
}

func (p *principalProjection) Search(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	if q.Mode == "lexical" {
		principals, _ := visibility.Principals(ctx)
		p.mu.Lock()
		p.keyword = append(p.keyword, principals)
		p.mu.Unlock()
	}
	return p.rebuildProjection.Search(ctx, routes, scope, q)
}

// A hybrid search over a Corpus without vectors falls back to keywords for
// it once per healthy space the ranker queries. Under enforced visibility the
// identical fallback must still be asked once per search, under the caller's
// principals, never shared with another search or another filter.
func TestEnforcedFallbackDedupe(t *testing.T) {
	for _, tc := range []struct {
		name        string
		otherFilter bool
		want        int
	}{
		{name: "identical fallbacks share one query", want: 1},
		{name: "distinct filters keep their own query", otherFilter: true, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			healthy := pluginGeneration("healthy-generation", true, "space", "example.large@1")
			healthy.Spaces[1].Role = content.SpaceServed
			routes := spaceRouting{"healthy": healthy, "rebuilding": pluginGeneration("rebuilt-generation", true, "example.small@1")}
			missing := map[string]bool{"rebuilding": true}
			projection := &principalProjection{rebuildProjection: &rebuildProjection{missing: missing}}
			s := service(&projection.fakeProjection, &fakeEmbeddings{})
			s.Projection, s.Routing = projection, routes
			s.Registry = rebuildingRegistry{routing: routes, missing: missing}
			s.Spaces = &pluginEncoder{}
			s.Ranker = &scriptedRanker{answer: func(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
				if q.Round != 1 {
					return passthrough(ctx, q)
				}
				requests := []any{}
				for _, space := range q.Spaces {
					if space.Role == content.SpaceServed {
						r := map[string]any{"primitive": "hybrid", "space": space.ID, "query_text": q.Query.Text, "k": q.Limit}
						if tc.otherFilter && len(requests) > 0 {
							r["filter"] = map[string]any{"source_namespaces": []string{"feed-a"}}
						}
						requests = append(requests, r)
					}
				}
				return answer(map[string]any{"requests": requests})
			}}
			q := retrieval.Request{Query: "lanterne", CorpusIDs: []string{"healthy", "rebuilding"}}
			for search, principal := range []string{"user:first", "user:second"} {
				ctx := visibility.WithPrincipals(t.Context(), []string{principal})
				result, err := s.Search(ctx, searchScope, q)
				if err != nil || len(result.Hits) != 2 {
					t.Fatalf("search %d: %d hits, error %v; want healthy vectors and fallback keywords", search, len(result.Hits), err)
				}
				projection.mu.Lock()
				queries := slices.Clone(projection.keyword)
				projection.keyword = nil
				projection.mu.Unlock()
				if len(queries) != tc.want {
					t.Fatalf("search %d issued %d keyword fallback queries, want %d", search, len(queries), tc.want)
				}
				for _, principals := range queries {
					if !slices.Equal(principals, []string{principal}) {
						t.Fatalf("search %d keyword fallback ran under principals %v, want [%s]", search, principals, principal)
					}
				}
			}
		})
	}

	// Request 0 waits for the fallback request 2 leads; request 1 then fails
	// and cancels request 2. Request 0 must ask again rather than inherit that
	// cancellation, so request 1's error is the search's, as when each request
	// asked on its own.
	t.Run("a canceled leader does not decide the error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			healthy := pluginGeneration("healthy-generation", true, "space", "example.large@1")
			healthy.Spaces[1].Role = content.SpaceServed
			routes := spaceRouting{"healthy": healthy, "rebuilding": pluginGeneration("rebuilt-generation", true, "example.small@1")}
			missing := map[string]bool{"rebuilding": true}
			s := service(&fakeProjection{}, &fakeEmbeddings{})
			s.Projection = &stallingKeywords{rebuildProjection: &rebuildProjection{missing: missing}}
			s.Routing, s.Registry = routes, rebuildingRegistry{routing: routes, missing: missing}
			s.Spaces = slowRefusal{}
			s.Ranker = &wideRanker{scriptedRanker{answer: func(_ context.Context, q plugins.SearchRequest) ([]byte, error) {
				return answer(map[string]any{"requests": []any{
					map[string]any{"primitive": "hybrid", "space": "space", "query_text": q.Query.Text, "k": q.Limit},
					map[string]any{"primitive": "near_vector", "space": "example.large@1", "query_text": "refused", "k": q.Limit},
					map[string]any{"primitive": "hybrid", "space": "example.large@1", "query_text": q.Query.Text, "k": q.Limit},
				}})
			}}}
			ctx := visibility.WithPrincipals(t.Context(), []string{"user:first"})
			_, err := s.Search(ctx, searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: []string{"healthy", "rebuilding"}})
			if !errors.Is(err, retrieval.ErrQueryTooLong) {
				t.Fatalf("search error %v, want request 1's ErrQueryTooLong", err)
			}
		})
	})
}

// wideRanker is a scriptedRanker that may ask three candidate requests a round.
type wideRanker struct{ scriptedRanker }

func (r *wideRanker) Manifest() *plugins.Manifest {
	m := r.scriptedRanker.Manifest()
	m.Contributions.Retrieval.Limits.MaxRequests = 3
	return m
}

// slowRefusal encodes the example spaces at once, but refuses the query
// "refused" as too long after 1 ms.
type slowRefusal struct{}

func (slowRefusal) Owns(space string) bool {
	return space == "example.small@1" || space == "example.large@1"
}
func (slowRefusal) EncodeQuery(_ context.Context, _, _, query string) ([]float32, error) {
	if query == "refused" {
		time.Sleep(time.Millisecond)
		return nil, retrieval.ErrQueryTooLong
	}
	return []float32{0.5, 0.5}, nil
}

// stallingKeywords answers vectors of the served space after 0.5 ms, and
// stalls its first keyword query until that query is canceled.
type stallingKeywords struct {
	*rebuildProjection
	keywordQueries atomic.Int64
}

func (p *stallingKeywords) Search(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	switch {
	case q.Mode == "lexical" && p.keywordQueries.Add(1) == 1:
		<-ctx.Done()
		return nil, ctx.Err()
	case q.Mode != "lexical" && q.Space == "space":
		time.Sleep(time.Millisecond / 2)
	}
	return p.rebuildProjection.Search(ctx, routes, scope, q)
}

// quietRanker is passthrough without a transcript, so a benchmark's memory
// stays flat.
type quietRanker struct{ scriptedRanker }

func (*quietRanker) Round(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
	return passthrough(ctx, q)
}

// BenchmarkOrchestration measures a search's control plane (authorization,
// then routing and space coverage per Corpus) with no and with 2 ms of
// latency per storage read. Benchmarks run outside the unit lane, so the
// latency is real.
func BenchmarkOrchestration(b *testing.B) {
	for _, latency := range []time.Duration{0, 2 * time.Millisecond} {
		for _, n := range []int{1, 4, 16} {
			b.Run(fmt.Sprintf("latency=%v/corpora=%d", latency, n), func(b *testing.B) {
				cp := &latentControlPlane{}
				if latency > 0 {
					cp.delay = func(string) time.Duration { return latency }
				}
				s := controlPlaneService(cp, nil)
				s.Ranker = &quietRanker{}
				q := retrieval.Request{Query: "lanterne", CorpusIDs: corpusIDs(n)}
				b.ReportAllocs()
				for b.Loop() {
					if _, err := s.Search(context.Background(), searchScope, q); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
