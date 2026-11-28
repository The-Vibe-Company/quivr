package retrieval_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
)

// scriptedRanker answers each round with answer(request) and records what it
// was sent.
type scriptedRanker struct {
	answer func(context.Context, plugins.SearchRequest) ([]byte, error)
	sent   []plugins.SearchRequest
}

func (r *scriptedRanker) Manifest() *plugins.Manifest {
	return &plugins.Manifest{ID: "example.fusion", Version: "0.1.0", Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{
		Profiles: map[string]plugins.RetrievalProfile{"default": {MaxLatencyMS: 2000}, "deep": {MaxLatencyMS: 50, MaxCostCents: 1}},
		Limits:   plugins.RetrievalLimits{MaxRounds: 3, MaxRequests: 2, MaxCandidates: 50},
	}}}
}
func (r *scriptedRanker) Configuration() json.RawMessage { return json.RawMessage(`{}`) }
func (r *scriptedRanker) Round(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
	r.sent = append(r.sent, q)
	return r.answer(ctx, q)
}

func answer(v any) ([]byte, error) { return json.Marshal(v) }

// passthrough asks for the candidates of the search's mode, k = limit, as
// core.retrieve does, and ranks them as served.
func passthrough(_ context.Context, q plugins.SearchRequest) ([]byte, error) {
	if q.Round == 1 {
		request := map[string]any{"primitive": "bm25", "query_text": q.Query.Text, "k": q.Limit}
		served := slices.IndexFunc(q.Spaces, func(sp plugins.SearchSpace) bool { return sp.Role == content.SpaceServed })
		switch q.Query.Mode {
		case "semantic":
			request = map[string]any{"primitive": "near_vector", "space": q.Spaces[served].ID, "query_text": q.Query.Text, "k": q.Limit}
		case "hybrid":
			request = map[string]any{"primitive": "hybrid", "space": q.Spaces[served].ID, "query_text": q.Query.Text, "alpha": 0.5, "fusion": "relative_score", "k": q.Limit}
		}
		return answer(map[string]any{"requests": []any{request}})
	}
	hits := []any{}
	for _, c := range q.Served[0].Candidates {
		hits = append(hits, map[string]any{"segment_id": c.SegmentID, "score": c.Score})
	}
	return answer(map[string]any{"ranking": map[string]any{"hits": hits}})
}

// fusion asks for keyword and vector candidates, then ranks every served
// candidate once, vector candidates first, with an explanation.
func fusion(_ context.Context, q plugins.SearchRequest) ([]byte, error) {
	if q.Round == 1 {
		return answer(map[string]any{"requests": []any{
			map[string]any{"primitive": "bm25", "query_text": q.Query.Text, "k": 2},
			map[string]any{"primitive": "near_vector", "space": "space", "query_text": q.Query.Text, "k": 2},
		}})
	}
	hits := []any{}
	seen := map[string]bool{}
	for i := len(q.Served) - 1; i >= 0; i-- {
		for _, c := range q.Served[i].Candidates {
			if seen[c.SegmentID] {
				continue
			}
			seen[c.SegmentID] = true
			hits = append(hits, map[string]any{"segment_id": c.SegmentID, "score": c.Score, "explanation": "served by " + q.Served[i].Request.Primitive})
		}
	}
	return answer(map[string]any{"ranking": map[string]any{"hits": hits}, "usage": map[string]any{"paid_calls": 1, "cost_cents": 0.25}})
}

type spaceList struct{}

func (spaceList) VectorSpaces(context.Context, string, string) (content.Generation, []content.SpaceCoverage, int64, error) {
	space := content.SpaceCoverage{GenerationRole: content.SpaceServed, Segments: 3}
	space.ID, space.Model, space.Metric, space.QueryModalities = "space", "example/e5", "cosine", []string{"text"}
	space.VectorSpace.Dimensions = 1
	return content.Generation{}, []content.SpaceCoverage{space}, 4, nil
}

func rankedService(p *fakeProjection, r *scriptedRanker) retrieval.Service {
	s := service(p, &fakeEmbeddings{})
	s.Ranker, s.Registry = r, spaceList{}
	return s
}

// A pinned retrieval plugin ranks: search serves the candidates it asks for,
// authorized and hydrated (a withdrawn segment never reaches it), and returns
// its ranking with its explanations, the profile and what it spent.
func TestPluginRanksServedCandidates(t *testing.T) {
	for _, secondary := range []bool{false, true} {
		t.Run(fmt.Sprintf("secondary served space %t", secondary), func(t *testing.T) {
			p := &fakeProjection{candidates: []content.Candidate{{SegmentID: "stale-1", GenerationID: "gen"}, {SegmentID: "vec-a", GenerationID: "gen", Score: 2}, {SegmentID: "vec-a", GenerationID: "gen", Score: 1}, {SegmentID: "b", GenerationID: "gen", Score: 0.5}, {SegmentID: "vec-c", GenerationID: "gen"}}}
			r := &scriptedRanker{answer: fusion}
			s := rankedService(p, r)
			if secondary {
				s.Routing = spaceRouting{"corpus": {ID: "gen", Collection: "Shared", ProfileVersion: retrieval.ProfileVersion, SpaceID: "other-owner-space", SourceNamespaceProjected: true, SpacesProjected: true, Spaces: []content.GenerationSpace{{ID: "other-owner-space", Role: content.SpaceServed, OwnerPluginID: "example.primary"}, {ID: "space", Role: content.SpaceServed, OwnerPluginID: "example.secondary"}}}}
			}
			result, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: " lanterne ", CorpusIDs: []string{"corpus"}, Profile: "default"})
			if err != nil {
				t.Fatal(err)
			}
			if len(p.searched) != 2 {
				t.Fatalf("projection searched %+v; want 2 requests", p.searched)
			}
			var hasLexical, hasSemantic bool
			for _, sq := range p.searched {
				if sq.Mode == "lexical" && sq.K == retrieval.CandidateLimit {
					hasLexical = true
				}
				if sq.Mode == "semantic" && sq.Space == "space" && len(sq.Vector) == 1 {
					hasSemantic = true
				}
			}
			if !hasLexical || !hasSemantic {
				t.Fatalf("projection searched %+v; want bm25 and near_vector, k oversampled", p.searched)
			}
			first := r.sent[0]
			if first.Profile != "default" || first.Query.Text != "lanterne" || first.Query.Mode != "hybrid" || len(first.Spaces) != 1 || first.Spaces[0].Coverage != (plugins.SpaceCoverage{Segments: 3, Total: 4}) || len(first.Served) != 0 {
				t.Fatalf("round 1 request %+v", first)
			}
			served := r.sent[1].Served
			ids := func(s plugins.ServedRequest) (out []string) {
				for _, c := range s.Candidates {
					out = append(out, c.SegmentID)
				}
				return out
			}
			if len(served) != 2 || fmt.Sprint(ids(served[0])) != "[vec-a b]" || served[0].Candidates[0].Score != 2 || served[0].Candidates[0].Text != segmentText || fmt.Sprint(ids(served[1])) != "[vec-a vec-c]" {
				t.Fatalf("round 2 served %+v; want keywords [vec-a b] and vectors [vec-a vec-c]: deduplicated, the withdrawn one absent, vectors only with coverage, k respected", served)
			}
			if len(result.Hits) != 3 || result.Hits[0].Segment.ID != "vec-a" || result.Hits[2].Segment.ID != "b" || result.Hits[0].Explanation != "served by near_vector" || result.Profile != "default" || result.ProfileVersion != "plugin:example.fusion@0.1.0/default" {
				t.Fatalf("result %+v", result)
			}
			if u := result.Usage; u == nil || u.Rounds != 2 || u.PaidCalls != 1 || u.CostCents != 0.25 {
				t.Fatalf("usage %+v", result.Usage)
			}
		})
	}
}

// A search reports the time it spent in each phase (THE-873): only a search
// with a vector encodes its query, and the phases never add up to more than
// the search took.
func TestUsageReportsTheTimeOfEachPhase(t *testing.T) {
	p := &fakeProjection{candidates: []content.Candidate{{SegmentID: "a", GenerationID: "gen"}}}
	s := rankedService(p, &scriptedRanker{answer: passthrough})
	for mode, encodes := range map[string]bool{"lexical": false, "semantic": true} {
		result, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "lanterne", Mode: mode, CorpusIDs: []string{"corpus"}, Profile: "default"})
		if err != nil {
			t.Fatal(err)
		}
		u := result.Usage
		if u == nil {
			t.Fatalf("%s: no usage", mode)
		}
		ph := u.Phases
		sum := ph.Routing + ph.Coverage + ph.PluginRounds + ph.QueryEncoding + ph.IndexQuery + ph.Hydration
		if (ph.QueryEncoding > 0) != encodes || ph.IndexQuery <= 0 || ph.Hydration <= 0 || ph.PluginRounds <= 0 || sum > u.Elapsed {
			t.Fatalf("%s: phases %+v in %s; want query encoding only when the search has a vector, index, hydration and rounds timed, and no more than the search took", mode, ph, u.Elapsed)
		}
	}
}

// The engine enforces the contract on every plugin answer, and a deadline on
// the whole search.
func TestPluginAnswersTheEngineRefuses(t *testing.T) {
	p := &fakeProjection{candidates: []content.Candidate{{SegmentID: "a", GenerationID: "gen"}}}
	for _, c := range []struct {
		name    string
		profile string
		answer  func(context.Context, plugins.SearchRequest) ([]byte, error)
		want    error
		rounds  int
		// timeout is the caller's deadline, which stands in for the profile's
		// hard bound (at least 2 s) so the test does not wait for it.
		timeout time.Duration
	}{
		{name: "a candidate never served", answer: func(context.Context, plugins.SearchRequest) ([]byte, error) {
			return answer(map[string]any{"ranking": map[string]any{"hits": []any{map[string]any{"segment_id": "a", "score": 1}}}})
		}, want: retrieval.ErrPluginInvalid, rounds: 1},
		{name: "more candidates in the last round", answer: func(context.Context, plugins.SearchRequest) ([]byte, error) {
			return answer(map[string]any{"requests": []any{map[string]any{"primitive": "bm25", "query_text": "x", "k": 1}}})
		}, want: retrieval.ErrPluginInvalid, rounds: 3},
		{name: "k over the declared limit", answer: func(context.Context, plugins.SearchRequest) ([]byte, error) {
			return answer(map[string]any{"requests": []any{map[string]any{"primitive": "bm25", "query_text": "x", "k": 51}}})
		}, want: retrieval.ErrPluginInvalid, rounds: 1},
		{name: "the plugin refuses the query", answer: func(context.Context, plugins.SearchRequest) ([]byte, error) {
			return nil, content.ErrInvalid
		}, want: retrieval.ErrUnsupported, rounds: 1},
		{name: "the plugin is unreachable", answer: func(context.Context, plugins.SearchRequest) ([]byte, error) {
			return nil, errors.New("connection refused")
		}, want: retrieval.ErrUnavailable, rounds: 1},
		{name: "the search's deadline passes", answer: func(ctx context.Context, _ plugins.SearchRequest) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, want: retrieval.ErrDeadline, rounds: 1, timeout: 20 * time.Millisecond},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &scriptedRanker{answer: c.answer}
			ctx := context.Background()
			if c.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, c.timeout)
				defer cancel()
			}
			_, err := rankedService(p, r).Search(ctx, searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: []string{"corpus"}, Profile: c.profile})
			if !errors.Is(err, c.want) || len(r.sent) != c.rounds {
				t.Fatalf("error %v after %d rounds, want %v after %d", err, len(r.sent), c.want, c.rounds)
			}
		})
	}
}

// max_latency_ms is the profile's objective, not its deadline (THE-813): a
// search may run past it, up to the profile's hard bound, four times the
// objective and at least 2 s. The plugin's rounds see how long they have left.
// A search past its objective still answers, and is flagged so the search
// rollups count it (THE-828).
func TestASearchMayRunPastItsObjective(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &fakeProjection{candidates: []content.Candidate{{SegmentID: "a", GenerationID: "gen"}}}
		left := make(map[string]time.Duration)
		answer := func(ctx context.Context, request plugins.SearchRequest) ([]byte, error) {
			if deadline, ok := ctx.Deadline(); ok && left[request.Profile] == 0 {
				left[request.Profile] = time.Until(deadline)
			}
			if request.Profile == "deep" {
				// deep's objective is 50 ms, the shortest a manifest declares:
				// the plugin outlasts it.
				// Advance the bubble's virtual clock, without a wall-clock wait.
				<-time.After(60 * time.Millisecond)
			}
			return passthrough(ctx, request)
		}
		s := rankedService(p, &scriptedRanker{answer: answer})
		for profile, over := range map[string]bool{"deep": true, "default": false} {
			result, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: []string{"corpus"}, Profile: profile})
			if err != nil {
				t.Fatal(err)
			}
			if result.Usage == nil || result.Usage.OverObjective != over {
				t.Fatalf("profile %s: usage %+v; want over its objective %v", profile, result.Usage, over)
			}
		}
		if left["deep"] != 2*time.Second {
			t.Fatalf("deep’s first round had %s left under a 50 ms objective; want its 2 s hard bound", left["deep"])
		}
	})
}

// Profiles resolve against what the pinned retrieval plugin declares;
// without a plugin nothing answers.
func TestSearchResolvesProfiles(t *testing.T) {
	for _, c := range []struct {
		profile  string
		unpinned bool
		want     string
		err      error
	}{
		{profile: "", want: "default"},
		{profile: "deep", want: "deep"},
		{profile: "fast", err: retrieval.ErrUnsupportedProfile},
		{profile: "", unpinned: true, err: retrieval.ErrUnavailable},
	} {
		s := rankedService(&fakeProjection{}, &scriptedRanker{answer: func(context.Context, plugins.SearchRequest) ([]byte, error) {
			return answer(map[string]any{"ranking": map[string]any{"hits": []any{}}})
		}})
		if c.unpinned {
			s.Ranker = nil
		}
		result, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: []string{"corpus"}, Profile: c.profile})
		if !errors.Is(err, c.err) || (c.err == nil && result.Profile != c.want) {
			t.Errorf("profile %q (unpinned %v): %q, %v; want %q, %v", c.profile, c.unpinned, result.Profile, err, c.want, c.err)
		}
	}
}

// stalledEmbedder is an embedding service that never answers.
type stalledEmbedder struct{ fakeEmbedder }

func (stalledEmbedder) Embed(ctx context.Context, _ string) ([]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// When the engine cannot serve candidates before the search's deadline (the
// profile's hard bound; the caller's shorter one stands in for it), a
// query model is unavailable and retryable, 503, even at the hard deadline.
func TestAStalledQueryModelKeepsItsUnavailableCodeAtTheDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := rankedService(&fakeProjection{}, &scriptedRanker{answer: passthrough})
		s.Embedder = stalledEmbedder{}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := s.Search(ctx, searchScope, retrieval.Request{Query: "lanterne", Mode: "semantic", CorpusIDs: []string{"corpus"}, Profile: "deep"})
		if code, _ := publicerr.Code(err); code != "model_unavailable" {
			t.Fatalf("error %v, want model_unavailable", err)
		}
	})
}

type barrierProjection struct {
	search func(context.Context, []retrieval.Route, corpus.Scope, retrieval.Request) ([]content.Candidate, error)
}

func (barrierProjection) Publish(context.Context, content.Generation, string, string, string, content.Version, content.Segmentation) error {
	return nil
}
func (barrierProjection) PublishEmbeddings(context.Context, content.Generation, string, []content.EmbeddingData) error {
	return nil
}
func (b barrierProjection) Search(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	return b.search(ctx, routes, scope, q)
}

// Multi-request rounds execute candidate fetching concurrently (proven via a
// synchronization barrier), coalesce overlapping SegmentIDs into one hydration
// batch per wave, preserve deterministic served order and lowest-index error
// precedence, and keep wall-clock phase totals within Elapsed.
func TestMultiRequestRoundExecutesConcurrentlyAndCoalescesHydration(t *testing.T) {
	t.Run("concurrent fetch, deterministic order, and coalesced hydration", func(t *testing.T) {
		for _, enforced := range []bool{false, true} {
			name := "unrestricted"
			if enforced {
				name = "enforced_visibility"
			}
			t.Run(name, func(t *testing.T) {
				var arrived atomic.Int32
				bothArrived := make(chan struct{})
				proj := barrierProjection{
					search: func(ctx context.Context, _ []retrieval.Route, _ corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
						if arrived.Add(1) == 2 {
							close(bothArrived)
						}
						select {
						case <-bothArrived:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
						if q.Mode == "lexical" {
							return []content.Candidate{
								{SegmentID: "vec-shared", GenerationID: "gen", Score: 3},
								{SegmentID: "lex-only", GenerationID: "gen", Score: 1},
							}, nil
						}
						return []content.Candidate{
							{SegmentID: "vec-shared", GenerationID: "gen", Score: 0.9},
							{SegmentID: "vec-only", GenerationID: "gen", Score: 0.8},
						}, nil
					},
				}
				lookups := &atomic.Int64{}
				reads := &atomic.Int64{}
				r := &scriptedRanker{answer: fusion}
				s := retrieval.Service{
					Embedder:   fakeEmbedder{},
					Routing:    fakeRouting{},
					Projection: proj,
					Ranker:     r,
					Registry:   spaceList{},
					Content: content.Service{
						RecordStore: fakeRecords{},
						Baseline:    fakeBaseline{lookups: lookups},
						Blobs:       countingBlobs{reads: reads},
						Embeddings:  &fakeEmbeddings{},
					},
				}
				ctx := t.Context()
				if enforced {
					ctx = visibility.WithPrincipals(ctx, []string{"user:reader"})
				}
				res, err := s.Search(ctx, searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: []string{"corpus"}, Profile: "default"})
				if err != nil {
					t.Fatalf("search failed: %v", err)
				}
				// Served[0] must be bm25 and Served[1] must be near_vector regardless of completion order.
				served := r.sent[1].Served
				if len(served) != 2 || served[0].Request.Primitive != plugins.PrimitiveBM25 || served[1].Request.Primitive != plugins.PrimitiveNearVector {
					t.Fatalf("served order = %+v, want [bm25, near_vector]", served)
				}
				// Final ranking must be deterministic: vec-shared, vec-only, lex-only.
				var gotIDs []string
				for _, h := range res.Hits {
					gotIDs = append(gotIDs, h.Segment.ID)
				}
				if want := []string{"vec-shared", "vec-only", "lex-only"}; !slices.Equal(gotIDs, want) {
					t.Fatalf("hits = %v, want %v", gotIDs, want)
				}
				// Three distinct segments across 2 overlapping requests must read each blob once
				// (not 4 times) via one unified wave hydration.
				if got := reads.Load(); got != 3 {
					t.Fatalf("blob reads = %d, want 3 (vec-shared coalesced across both requests)", got)
				}
				// fakeBaseline does not implement HydrationRechecker, so Content.Hydrate calls
				// Baseline.Hydrate twice (lookup + recheck) for the single unified wave, plus
				// recheckServed and recheckResult when visibility is enforced.
				wantLookups := int64(2)
				if enforced {
					wantLookups = 4
				}
				if got := lookups.Load(); got != wantLookups {
					t.Fatalf("baseline lookups = %d, want %d (single coalesced hydration wave)", got, wantLookups)
				}
			})
		}
	})

	t.Run("lowest failing request index wins error precedence", func(t *testing.T) {
		newFailingLeg1Projection := func() barrierProjection {
			leg1Failed := make(chan struct{})
			return barrierProjection{
				search: func(ctx context.Context, _ []retrieval.Route, _ corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
					if q.Mode == "semantic" {
						close(leg1Failed)
						return nil, errors.New("index leg 1 failed")
					}
					select {
					case <-leg1Failed:
						if ctx.Err() != nil {
							return nil, fmt.Errorf("leg 0 was canceled by higher-index failure: %w", ctx.Err())
						}
						return []content.Candidate{{SegmentID: "a", GenerationID: "gen", Score: 1}}, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				},
			}
		}
		// Case A: index 0 fails with ErrSourceFilterUnavailable while index 1 fails in projection.
		// Index 0 must win even if index 1 fails first.
		s := retrieval.Service{
			Embedder:   fakeEmbedder{},
			Routing:    fakeRouting{unprojected: true},
			Projection: newFailingLeg1Projection(),
			Registry:   spaceList{},
			Ranker: &scriptedRanker{answer: func(_ context.Context, q plugins.SearchRequest) ([]byte, error) {
				return answer(map[string]any{"requests": []any{
					map[string]any{"primitive": "bm25", "query_text": q.Query.Text, "k": 2, "filter": map[string]any{"source_namespaces": []string{"feed"}}},
					map[string]any{"primitive": "near_vector", "space": "space", "query_text": q.Query.Text, "k": 2},
				}})
			}},
			Content: content.Service{RecordStore: fakeRecords{}, Baseline: fakeBaseline{}, Blobs: fakeBlobs{}, Embeddings: &fakeEmbeddings{}},
		}
		_, err := s.Search(t.Context(), searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: []string{"corpus"}, Profile: "default"})
		if !errors.Is(err, retrieval.ErrSourceFilterUnavailable) {
			t.Fatalf("error = %v, want ErrSourceFilterUnavailable from index 0", err)
		}

		// Case B: index 0 succeeds after leg 1 fails -> index 1's ErrUnavailable is returned.
		s.Routing = fakeRouting{}
		s.Projection = newFailingLeg1Projection()
		s.Ranker = &scriptedRanker{answer: fusion}
		_, err = s.Search(t.Context(), searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: []string{"corpus"}, Profile: "default"})
		if !errors.Is(err, retrieval.ErrUnavailable) {
			t.Fatalf("error = %v, want ErrUnavailable from index 1", err)
		}
	})

	t.Run("concurrent wave phases never exceed elapsed time", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			proj := barrierProjection{
				search: func(ctx context.Context, _ []retrieval.Route, _ corpus.Scope, _ retrieval.Request) ([]content.Candidate, error) {
					select {
					case <-time.After(40 * time.Millisecond):
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					return []content.Candidate{{SegmentID: "vec-a", GenerationID: "gen", Score: 1}}, nil
				},
			}
			slowEmb := testQueryEmbedder{
				run: func(ctx context.Context, _ string) ([]float32, error) {
					select {
					case <-time.After(30 * time.Millisecond):
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					return []float32{1}, nil
				},
			}
			s := retrieval.Service{
				Embedder:   slowEmb,
				Routing:    fakeRouting{},
				Projection: proj,
				Ranker:     &scriptedRanker{answer: fusion},
				Registry:   spaceList{},
				Content:    content.Service{RecordStore: fakeRecords{}, Baseline: fakeBaseline{}, Blobs: fakeBlobs{}, Embeddings: &fakeEmbeddings{}},
			}
			res, err := s.Search(t.Context(), searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: []string{"corpus"}, Profile: "default"})
			if err != nil {
				t.Fatal(err)
			}
			ph := res.Usage.Phases
			sum := ph.Routing + ph.Coverage + ph.PluginRounds + ph.QueryEncoding + ph.IndexQuery + ph.Hydration
			if sum > res.Usage.Elapsed {
				t.Fatalf("phase sum %s exceeds elapsed %s (phases=%+v)", sum, res.Usage.Elapsed, ph)
			}
			if ph.QueryEncoding <= 0 || ph.IndexQuery <= 0 {
				t.Fatalf("expected non-zero encoding and index phases, got %+v", ph)
			}
		})
	})
}

type testQueryEmbedder struct {
	run func(context.Context, string) ([]float32, error)
}

func (e testQueryEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return e.run(ctx, text)
}

func (testQueryEmbedder) Space() content.VectorSpace { return content.VectorSpace{ID: "space"} }
