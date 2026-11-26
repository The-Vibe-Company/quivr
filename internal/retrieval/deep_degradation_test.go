package retrieval_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

type namedProfileMap struct {
	entries map[string]struct {
		ranker retrieval.Ranker
		local  string
	}
}

func (p namedProfileMap) Profiles() []retrieval.Profile { return nil }
func (p namedProfileMap) Resolve(name string) (retrieval.Ranker, string, bool) {
	entry, ok := p.entries[name]
	return entry.ranker, entry.local, ok
}

// Search owns graceful deep -> default degradation across tight deadline budgets
// and reranker timeouts/outages while preserving healthy deep reranking and
// keeping client cancellations and contract errors intact.
func TestDeepProfileDegradesToDefaultUnderBudgetAndRerankerFailure(t *testing.T) {
	newPair := func(rerankRound2 func(context.Context, plugins.SearchRequest) ([]byte, error), rerankRound1Err error, innerDelay time.Duration) (retrieval.Service, *atomic.Int32, *atomic.Int32) {
		var r1Calls, r2Calls atomic.Int32
		baseManifest := plugins.Manifest{
			ID:            "core.retrieve",
			Version:       "1.0.0",
			Compatibility: plugins.Compatibility{PluginAPI: ">=0.13.0 <0.14.0"},
			Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{
				Profiles: map[string]plugins.RetrievalProfile{
					"default": {MaxLatencyMS: 500, MaxCostCents: 0},
				},
			}},
		}
		deepManifest := plugins.Manifest{
			ID:            "example.rerank",
			Version:       "1.0.0",
			Compatibility: plugins.Compatibility{PluginAPI: ">=0.13.0 <0.14.0"},
			Requires:      []plugins.Requirement{{Plugin: "core.retrieve", Version: ">=1.0.0", Profiles: []string{"default"}}},
			Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{
				Profiles: map[string]plugins.RetrievalProfile{
					"deep": {MaxLatencyMS: 3000, MaxCostCents: 1},
				},
			}},
		}
		base := &profileRanker{
			m: baseManifest,
			round: func(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
				if q.Round == 1 {
					if innerDelay > 0 {
						select {
						case <-time.After(innerDelay):
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
					return answer(map[string]any{"requests": []any{
						map[string]any{"primitive": "bm25", "query_text": q.Query.Text, "k": q.Limit},
					}})
				}
				hits := make([]any, 0, len(q.Served[0].Candidates))
				for _, c := range q.Served[0].Candidates {
					hits = append(hits, map[string]any{"segment_id": c.SegmentID, "score": c.Score, "explanation": "default hybrid"})
				}
				return answer(map[string]any{"ranking": map[string]any{"hits": hits}})
			},
		}
		deep := &profileRanker{
			m: deepManifest,
			round: func(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
				if q.Round == 1 {
					r1Calls.Add(1)
					if rerankRound1Err != nil {
						return nil, rerankRound1Err
					}
					return answer(map[string]any{"requests": []any{
						map[string]any{"primitive": "profile", "profile": map[string]any{"name": "core.retrieve/default", "mode": "hybrid", "limit": 10}},
					}})
				}
				r2Calls.Add(1)
				return rerankRound2(ctx, q)
			},
		}
		projection := &fakeProjection{candidates: []content.Candidate{
			{SegmentID: "a", GenerationID: "gen", Score: 3},
			{SegmentID: "b", GenerationID: "gen", Score: 2},
			{SegmentID: "c", GenerationID: "gen", Score: 1},
		}}
		s := service(projection, &fakeEmbeddings{})
		s.Registry = nil
		s.MinRerankBudget = 250 * time.Millisecond
		s.ProfilesRouter = namedProfileMap{entries: map[string]struct {
			ranker retrieval.Ranker
			local  string
		}{
			"default":               {ranker: base, local: "default"},
			"core.retrieve/default": {ranker: base, local: "default"},
			"deep":                  {ranker: deep, local: "deep"},
			"example.rerank/deep":   {ranker: deep, local: "deep"},
		}}
		return s, &r1Calls, &r2Calls
	}

	// 1. Healthy deep returns full reranked order without degradation.
	t.Run("healthy deep keeps reranked order", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, r1, r2 := newPair(func(_ context.Context, q plugins.SearchRequest) ([]byte, error) {
				cands := q.Served[0].Candidates
				// Reverse default order to prove reranking ran.
				return answer(map[string]any{"ranking": map[string]any{"hits": []any{
					map[string]any{"segment_id": cands[1].SegmentID, "score": 0.99, "explanation": "reranked"},
					map[string]any{"segment_id": cands[0].SegmentID, "score": 0.50, "explanation": "reranked"},
				}}})
			}, nil, 0)
			res, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "harbour", Mode: "lexical", CorpusIDs: []string{"corpus"}, Profile: "deep", Limit: 2})
			if err != nil {
				t.Fatalf("healthy deep failed: %v", err)
			}
			if r1.Load() != 1 || r2.Load() != 1 {
				t.Fatalf("reranker rounds r1=%d r2=%d; want 1, 1", r1.Load(), r2.Load())
			}
			if res.Profile != "deep" || res.ProfileVersion != "plugin:example.rerank@1.0.0/deep" || len(res.Degraded) != 0 {
				t.Fatalf("healthy deep metadata: profile=%q version=%q degraded=%+v", res.Profile, res.ProfileVersion, res.Degraded)
			}
			if len(res.Hits) != 2 || res.Hits[0].Segment.ID != "b" || res.Hits[1].Segment.ID != "a" {
				t.Fatalf("healthy deep hits: %+v; want [b a]", res.Hits)
			}
		})
	})

	// 2. Remaining budget below reranker threshold after default retrieval skips Round 2.
	t.Run("tight budget after default skips reranker round", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, r1, r2 := newPair(func(_ context.Context, _ plugins.SearchRequest) ([]byte, error) {
				t.Fatal("reranker round 2 should be skipped when remaining budget is below threshold")
				return nil, nil
			}, nil, 300*time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), 450*time.Millisecond)
			defer cancel()
			res, err := s.Search(ctx, searchScope, retrieval.Request{Query: "harbour", Mode: "lexical", CorpusIDs: []string{"corpus"}, Profile: "deep", Limit: 2})
			if err != nil {
				t.Fatalf("tight-budget deep failed: %v", err)
			}
			if r1.Load() != 1 || r2.Load() != 0 {
				t.Fatalf("reranker rounds r1=%d r2=%d; want 1, 0", r1.Load(), r2.Load())
			}
			wantDegraded := []retrieval.Degradation{{Reason: retrieval.DegradationRerankerBudgetExceeded, CorpusIDs: []string{"corpus"}}}
			if res.Profile != "deep" || res.ProfileVersion != "plugin:core.retrieve@1.0.0/default" || !reflect.DeepEqual(res.Degraded, wantDegraded) {
				t.Fatalf("degraded metadata: profile=%q version=%q degraded=%+v", res.Profile, res.ProfileVersion, res.Degraded)
			}
			if len(res.Hits) != 2 || res.Hits[0].Segment.ID != "a" || res.Hits[1].Segment.ID != "b" {
				t.Fatalf("fallback hits: %+v; want default order [a b] sliced to limit 2", res.Hits)
			}
		})
	})

	// 3. Reranker timeout against the profile deadline falls back to already-retrieved default hits.
	t.Run("reranker timeout falls back to default hits", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, _, r2 := newPair(func(ctx context.Context, _ plugins.SearchRequest) ([]byte, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}, nil, 0)
			res, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "harbour", Mode: "lexical", CorpusIDs: []string{"corpus"}, Profile: "deep", Limit: 2})
			if err != nil {
				t.Fatalf("timeout deep failed: %v", err)
			}
			if r2.Load() != 1 {
				t.Fatalf("reranker round 2 calls=%d; want 1", r2.Load())
			}
			wantDegraded := []retrieval.Degradation{{Reason: retrieval.DegradationRerankerBudgetExceeded, CorpusIDs: []string{"corpus"}}}
			if res.Profile != "deep" || res.ProfileVersion != "plugin:core.retrieve@1.0.0/default" || !reflect.DeepEqual(res.Degraded, wantDegraded) {
				t.Fatalf("timeout degraded metadata: profile=%q version=%q degraded=%+v", res.Profile, res.ProfileVersion, res.Degraded)
			}
			if len(res.Hits) != 2 || res.Hits[0].Segment.ID != "a" || res.Hits[1].Segment.ID != "b" {
				t.Fatalf("timeout fallback hits: %+v; want [a b]", res.Hits)
			}
		})
	})

	// 4. Reranker outage / overload error during Round 2 falls back to default hits.
	t.Run("reranker error falls back to default hits", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, _, r2 := newPair(func(_ context.Context, _ plugins.SearchRequest) ([]byte, error) {
				return nil, errors.New("reranker overloaded 503")
			}, nil, 0)
			res, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "harbour", Mode: "lexical", CorpusIDs: []string{"corpus"}, Profile: "deep", Limit: 2})
			if err != nil {
				t.Fatalf("reranker outage deep failed: %v", err)
			}
			if r2.Load() != 1 {
				t.Fatalf("reranker round 2 calls=%d; want 1", r2.Load())
			}
			wantDegraded := []retrieval.Degradation{{Reason: retrieval.DegradationRerankerUnavailable, CorpusIDs: []string{"corpus"}}}
			if res.Profile != "deep" || res.ProfileVersion != "plugin:core.retrieve@1.0.0/default" || !reflect.DeepEqual(res.Degraded, wantDegraded) {
				t.Fatalf("outage degraded metadata: profile=%q version=%q degraded=%+v", res.Profile, res.ProfileVersion, res.Degraded)
			}
			if len(res.Hits) != 2 || res.Hits[0].Segment.ID != "a" || res.Hits[1].Segment.ID != "b" {
				t.Fatalf("outage fallback hits: %+v; want [a b]", res.Hits)
			}
		})
	})

	// 5. Pre-flight tight deadline skips deep entirely and runs default directly.
	t.Run("preflight tight budget runs default directly", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, r1, r2 := newPair(nil, errors.New("should not run"), 0)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			res, err := s.Search(ctx, searchScope, retrieval.Request{Query: "harbour", Mode: "lexical", CorpusIDs: []string{"corpus"}, Profile: "deep", Limit: 2})
			if err != nil {
				t.Fatalf("preflight fallback failed: %v", err)
			}
			if r1.Load() != 0 || r2.Load() != 0 {
				t.Fatalf("reranker called under tight preflight budget: r1=%d r2=%d", r1.Load(), r2.Load())
			}
			wantDegraded := []retrieval.Degradation{{Reason: retrieval.DegradationRerankerBudgetExceeded, CorpusIDs: []string{"corpus"}}}
			if res.Profile != "deep" || res.ProfileVersion != "plugin:core.retrieve@1.0.0/default" || !reflect.DeepEqual(res.Degraded, wantDegraded) {
				t.Fatalf("preflight degraded metadata: profile=%q version=%q degraded=%+v", res.Profile, res.ProfileVersion, res.Degraded)
			}
			if len(res.Hits) != 2 || res.Hits[0].Segment.ID != "a" {
				t.Fatalf("preflight fallback hits: %+v", res.Hits)
			}
		})
	})

	// 6. Round 1 reranker outage falls back to running default profile.
	t.Run("round 1 reranker unreachable falls back to default", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, r1, _ := newPair(nil, errors.New("connection refused"), 0)
			res, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "harbour", Mode: "lexical", CorpusIDs: []string{"corpus"}, Profile: "deep", Limit: 2})
			if err != nil {
				t.Fatalf("round 1 fallback failed: %v", err)
			}
			if r1.Load() != 1 {
				t.Fatalf("round 1 calls=%d; want 1", r1.Load())
			}
			wantDegraded := []retrieval.Degradation{{Reason: retrieval.DegradationRerankerUnavailable, CorpusIDs: []string{"corpus"}}}
			if res.Profile != "deep" || res.ProfileVersion != "plugin:core.retrieve@1.0.0/default" || !reflect.DeepEqual(res.Degraded, wantDegraded) {
				t.Fatalf("round 1 fallback metadata: profile=%q version=%q degraded=%+v", res.Profile, res.ProfileVersion, res.Degraded)
			}
			if len(res.Hits) != 2 || res.Hits[0].Segment.ID != "a" {
				t.Fatalf("round 1 fallback hits: %+v", res.Hits)
			}
		})
	})

	// 7. Caller cancellation, expired caller deadline, and plugin contract violations are never masked.
	t.Run("caller cancel, caller deadline, and contract errors are preserved", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			s, _, _ := newPair(func(rctx context.Context, _ plugins.SearchRequest) ([]byte, error) {
				cancel()
				return nil, rctx.Err()
			}, nil, 0)
			if _, err := s.Search(ctx, searchScope, retrieval.Request{Query: "harbour", Mode: "lexical", CorpusIDs: []string{"corpus"}, Profile: "deep", Limit: 2}); !errors.Is(err, retrieval.ErrUnavailable) {
				t.Fatalf("canceled deep: err=%v; want ErrUnavailable", err)
			}
			expiredCtx, expiredCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer expiredCancel()
			sExpired, _, _ := newPair(func(rctx context.Context, _ plugins.SearchRequest) ([]byte, error) {
				<-rctx.Done()
				return nil, rctx.Err()
			}, nil, 0)
			if _, err := sExpired.Search(expiredCtx, searchScope, retrieval.Request{Query: "harbour", Mode: "lexical", CorpusIDs: []string{"corpus"}, Profile: "deep", Limit: 2}); !errors.Is(err, retrieval.ErrDeadline) {
				t.Fatalf("caller deadline exceeded during deep: err=%v; want ErrDeadline", err)
			}
			sInvalid, _, _ := newPair(func(_ context.Context, _ plugins.SearchRequest) ([]byte, error) {
				return nil, retrieval.ErrPluginInvalid
			}, nil, 0)
			if _, err := sInvalid.Search(context.Background(), searchScope, retrieval.Request{Query: "harbour", Mode: "lexical", CorpusIDs: []string{"corpus"}, Profile: "deep", Limit: 2}); !errors.Is(err, retrieval.ErrPluginInvalid) {
				t.Fatalf("invalid plugin answer: err=%v; want ErrPluginInvalid", err)
			}
		})
	})

	// 8. Round 1 failure after a wait must keep the reranker wait out of Phases.Routing
	// and preserve the failed deep profile entry in Usage.Profiles.
	t.Run("round 1 delayed failure preserves routing phase and profile chain", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, _, _ := newPair(nil, nil, 0)
			// Wrap deep ranker with a 200ms delay before failing Round 1.
			base, _, _ := s.ProfilesRouter.Resolve("default")
			origDeep, _, _ := s.ProfilesRouter.Resolve("deep")
			delayedDeep := &profileRanker{
				m: *origDeep.Manifest(),
				round: func(ctx context.Context, _ plugins.SearchRequest) ([]byte, error) {
					time.Sleep(200 * time.Millisecond)
					return nil, errors.New("reranker 503")
				},
			}
			s.ProfilesRouter = namedProfileMap{entries: map[string]struct {
				ranker retrieval.Ranker
				local  string
			}{
				"default":               {ranker: base, local: "default"},
				"core.retrieve/default": {ranker: base, local: "default"},
				"deep":                  {ranker: delayedDeep, local: "deep"},
				"example.rerank/deep":   {ranker: delayedDeep, local: "deep"},
			}}
			res, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "harbour", Mode: "lexical", CorpusIDs: []string{"corpus"}, Profile: "deep", Limit: 2})
			if err != nil {
				t.Fatalf("delayed round 1 fallback failed: %v", err)
			}
			if res.Usage == nil {
				t.Fatal("expected usage on degraded fallback")
			}
			if res.Usage.Phases.Routing >= 100*time.Millisecond {
				t.Fatalf("Phases.Routing=%s absorbed reranker wait; want < 100ms", res.Usage.Phases.Routing)
			}
			if res.Usage.Phases.PluginRounds < 200*time.Millisecond {
				t.Fatalf("Phases.PluginRounds=%s missing 200ms reranker wait", res.Usage.Phases.PluginRounds)
			}
			if len(res.Usage.Profiles) != 2 || res.Usage.Profiles[0].Profile != "example.rerank/deep" || res.Usage.Profiles[1].Profile != "core.retrieve/default" {
				t.Fatalf("Usage.Profiles=%+v; want [example.rerank/deep, core.retrieve/default]", res.Usage.Profiles)
			}
		})
	})

	// 9. Scoped inner default profile call (overridden query) must not be cached as fallback.
	t.Run("overridden query inner default call is not reused as fallback", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var rewrittenRun atomic.Int32
			var fallbackRun atomic.Int32
			s, _, _ := newPair(nil, nil, 0)
			origBase, _, _ := s.ProfilesRouter.Resolve("default")
			origDeep, _, _ := s.ProfilesRouter.Resolve("deep")
			trackedBase := &profileRanker{
				m: *origBase.Manifest(),
				round: func(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
					if q.Round == 1 {
						switch q.Query.Text {
						case "narrow rewritten query":
							rewrittenRun.Add(1)
						case "harbour":
							fallbackRun.Add(1)
						}
					}
					return origBase.Round(ctx, q)
				},
			}
			rewritingDeep := &profileRanker{
				m: *origDeep.Manifest(),
				round: func(_ context.Context, q plugins.SearchRequest) ([]byte, error) {
					if q.Round == 1 {
						return answer(map[string]any{"requests": []any{
							map[string]any{"primitive": "profile", "profile": map[string]any{"name": "core.retrieve/default", "query": "narrow rewritten query", "mode": "hybrid", "limit": 10}},
						}})
					}
					return nil, errors.New("reranker down")
				},
			}
			s.ProfilesRouter = namedProfileMap{entries: map[string]struct {
				ranker retrieval.Ranker
				local  string
			}{
				"default":               {ranker: trackedBase, local: "default"},
				"core.retrieve/default": {ranker: trackedBase, local: "default"},
				"deep":                  {ranker: rewritingDeep, local: "deep"},
				"example.rerank/deep":   {ranker: rewritingDeep, local: "deep"},
			}}
			res, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "harbour", Mode: "lexical", CorpusIDs: []string{"corpus"}, Profile: "deep", Limit: 2})
			if err != nil {
				t.Fatalf("fallback search failed: %v", err)
			}
			if rewrittenRun.Load() != 1 {
				t.Fatalf("expected nested rewritten query call to run once, got %d", rewrittenRun.Load())
			}
			if fallbackRun.Load() != 1 {
				t.Fatalf("expected clean fallback with original query 'harbour' to run once, got %d", fallbackRun.Load())
			}
			if len(res.Degraded) != 1 || res.Degraded[0].Reason != retrieval.DegradationRerankerUnavailable {
				t.Fatalf("unexpected degradation: %+v", res.Degraded)
			}
		})
	})
}
