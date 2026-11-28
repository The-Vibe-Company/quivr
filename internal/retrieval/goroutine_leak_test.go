package retrieval_test

import (
	"context"
	"errors"
	"io/fs"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

type cancelPhase int

const (
	phaseRouting cancelPhase = iota
	phaseCoverage
	phaseRankerRound1
	phaseQueryEncoding
	phaseProjection
	phaseHydration
	phaseCount
)

type cancelPlanKey struct{}

type cancelPlan struct {
	target   cancelPhase
	cancel   context.CancelFunc
	canceled <-chan struct{}
	async    bool
}

// maybeCancel triggers cancellation when a search reaches its randomly assigned
// lifecycle phase. Half of the plans yield after cancelling to race against
// active concurrent legs; the other half wait on the caller's own cancellation
// channel (never a detached context.WithoutCancel context) and return an error.
func maybeCancel(ctx context.Context, phase cancelPhase) error {
	plan, ok := ctx.Value(cancelPlanKey{}).(*cancelPlan)
	if !ok || plan.target != phase {
		return ctx.Err()
	}
	plan.cancel()
	if plan.async {
		runtime.Gosched()
	} else {
		<-plan.canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return context.Canceled
}

type cancelAwareRouting struct{}

func (cancelAwareRouting) Authorize(ctx context.Context, _ corpus.Scope, _ []string) error {
	return maybeCancel(ctx, phaseRouting)
}

func (cancelAwareRouting) Generation(ctx context.Context, _, id string) (content.Generation, error) {
	if err := maybeCancel(ctx, phaseRouting); err != nil {
		return content.Generation{}, err
	}
	return content.Generation{
		ID:                       "gen-" + id,
		Collection:               "Search",
		ProfileVersion:           retrieval.ProfileVersion,
		SpaceID:                  "space",
		SourceNamespaceProjected: true,
		SpacesProjected:          true,
		Spaces:                   []content.GenerationSpace{{ID: "space", Role: content.SpaceServed, OwnerPluginID: "core.ingest"}},
	}, nil
}

type cancelAwareRegistry struct{}

func (cancelAwareRegistry) VectorSpaces(ctx context.Context, _, _ string) (content.Generation, []content.SpaceCoverage, int64, error) {
	if err := maybeCancel(ctx, phaseCoverage); err != nil {
		return content.Generation{}, nil, 0, err
	}
	space := content.SpaceCoverage{GenerationRole: content.SpaceServed, OwnerPluginID: "core.ingest", Segments: 2}
	space.ID, space.Model, space.Metric, space.QueryModalities = "space", "example/e5", "cosine", []string{"text"}
	space.VectorSpace.Dimensions = 1
	return content.Generation{ID: "gen-corpus_a", SpaceID: "space"}, []content.SpaceCoverage{space}, 2, nil
}

type cancelAwareEncoder struct{}

func (cancelAwareEncoder) Owns(space string) bool { return space == "space" }

func (cancelAwareEncoder) EncodeQuery(ctx context.Context, _, _, _ string) ([]float32, error) {
	if err := maybeCancel(ctx, phaseQueryEncoding); err != nil {
		return nil, err
	}
	return []float32{1}, nil
}

func (e cancelAwareEncoder) ResolveQueryEncoder(ctx context.Context, space string) (retrieval.QueryEncoder, string, error) {
	if !e.Owns(space) {
		return nil, "", nil
	}
	return e, "pin-v1", nil
}

type cancelAwareProjection struct{}

func (cancelAwareProjection) Publish(context.Context, content.Generation, string, string, string, content.Version, content.Segmentation) error {
	return nil
}
func (cancelAwareProjection) PublishEmbeddings(context.Context, content.Generation, string, []content.EmbeddingData) error {
	return nil
}
func (cancelAwareProjection) Search(ctx context.Context, routes []retrieval.Route, _ corpus.Scope, _ retrieval.Request) ([]content.Candidate, error) {
	if err := maybeCancel(ctx, phaseProjection); err != nil {
		return nil, err
	}
	out := make([]content.Candidate, 0, len(routes)*2)
	for _, r := range routes {
		out = append(out,
			content.Candidate{SegmentID: "vec-a-" + r.CorpusID, GenerationID: r.Generation.ID, Score: 2},
			content.Candidate{SegmentID: "b-" + r.CorpusID, GenerationID: r.Generation.ID, Score: 1},
		)
	}
	return out, nil
}

type cancelAwareBaseline struct{ content.BaselineRepository }

func (cancelAwareBaseline) Hydrate(ctx context.Context, _ corpus.Scope, cs []content.Candidate) (map[int]content.Located, error) {
	if err := maybeCancel(ctx, phaseHydration); err != nil {
		return nil, err
	}
	out := make(map[int]content.Located, len(cs))
	for i, c := range cs {
		h := content.Hydrated{
			GenerationID: c.GenerationID,
			RecordID:     "rec-" + c.SegmentID,
			VersionID:    "ver-" + c.SegmentID,
			TextSHA256:   content.Hash([]byte(segmentText)),
			Segment:      content.Segment{ID: c.SegmentID, PartKey: "body", End: len([]rune(segmentText))},
		}
		if strings.HasPrefix(c.SegmentID, "vec-") {
			h.EmbeddingID, h.SpaceID = "emb-"+c.SegmentID, "space"
		}
		out[i] = content.Located{Hydrated: h, Blob: content.Blob{Key: "blob-" + c.SegmentID}}
	}
	return out, nil
}

type cancelAwareBlobs struct{ content.Blobs }

func (cancelAwareBlobs) Read(ctx context.Context, _ content.Blob) ([]byte, error) {
	if err := maybeCancel(ctx, phaseHydration); err != nil {
		return nil, err
	}
	return []byte(segmentText), nil
}

type cancelAwareRouter struct{}

func (cancelAwareRouter) Profiles() []retrieval.Profile { return nil }

func (cancelAwareRouter) Resolve(name string) (retrieval.Ranker, string, bool) {
	baseManifest := plugins.Manifest{
		ID:            "core.retrieve",
		Version:       "1.0.0",
		Compatibility: plugins.Compatibility{PluginAPI: ">=0.13.0 <0.14.0"},
		Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{
			Profiles: map[string]plugins.RetrievalProfile{"default": {MaxLatencyMS: 2000}},
			Limits:   plugins.RetrievalLimits{MaxRounds: 3, MaxRequests: 4, MaxCandidates: 50},
		}},
	}
	deepManifest := plugins.Manifest{
		ID:            "example.rerank",
		Version:       "1.0.0",
		Compatibility: plugins.Compatibility{PluginAPI: ">=0.13.0 <0.14.0"},
		Requires:      []plugins.Requirement{{Plugin: "core.retrieve", Version: ">=1.0.0", Profiles: []string{"default"}}},
		Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{
			Profiles: map[string]plugins.RetrievalProfile{"deep": {MaxLatencyMS: 3000, MaxCostCents: 1}},
			Limits:   plugins.RetrievalLimits{MaxRounds: 3, MaxRequests: 4, MaxCandidates: 50},
		}},
	}
	// Multi-leg base ranker issues concurrent bm25 + near_vector requests to
	// exercise parallel serveRound goroutines and QueryVectorCache flights.
	base := &profileRanker{
		m: baseManifest,
		round: func(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
			if q.Round == 1 {
				if err := maybeCancel(ctx, phaseRankerRound1); err != nil {
					return nil, err
				}
				return answer(map[string]any{"requests": []any{
					map[string]any{"primitive": "bm25", "query_text": q.Query.Text, "k": q.Limit},
					map[string]any{"primitive": "near_vector", "space": "space", "query_text": q.Query.Text, "k": q.Limit},
				}})
			}
			hits := []any{}
			seen := map[string]bool{}
			for _, served := range q.Served {
				for _, c := range served.Candidates {
					if !seen[c.SegmentID] {
						seen[c.SegmentID] = true
						hits = append(hits, map[string]any{"segment_id": c.SegmentID, "score": c.Score})
					}
				}
			}
			return answer(map[string]any{"ranking": map[string]any{"hits": hits}})
		},
	}
	deep := &profileRanker{
		m: deepManifest,
		round: func(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
			if q.Round == 1 {
				if err := maybeCancel(ctx, phaseRankerRound1); err != nil {
					return nil, err
				}
				return answer(map[string]any{"requests": []any{
					map[string]any{"primitive": "profile", "profile": map[string]any{
						"name":  "core.retrieve/default",
						"mode":  q.Query.Mode,
						"limit": q.Limit,
					}},
					map[string]any{"primitive": "bm25", "query_text": q.Query.Text, "k": q.Limit},
				}})
			}
			hits := []any{}
			seen := map[string]bool{}
			for _, served := range q.Served {
				for _, c := range served.Candidates {
					if !seen[c.SegmentID] {
						seen[c.SegmentID] = true
						hits = append(hits, map[string]any{"segment_id": c.SegmentID, "score": c.Score})
					}
				}
			}
			return answer(map[string]any{"ranking": map[string]any{"hits": hits}})
		},
	}
	switch name {
	case "default", "core.retrieve/default":
		return base, "default", true
	case "deep", "example.rerank/deep":
		return deep, "deep", true
	default:
		return nil, "", false
	}
}

func openFDCount() (int, error) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

// Owns leak-freedom across 500 concurrent searches cancelled at randomly chosen
// lifecycle phases (routing, coverage, ranker round 1, shared QueryVectorCache
// encoding, multi-leg projection search, and canonical hydration): runs inside
// a deterministic synctest bubble so every spawned goroutine must quiesce and
// exit cleanly with NumGoroutine delta <= 2 and open FD delta <= 2.
func TestCancelledSearchesDoNotLeakGoroutinesOrFDs(t *testing.T) {
	synctest.Test(t, testCancelledSearchesDoNotLeakGoroutinesOrFDs)
}

func testCancelledSearchesDoNotLeakGoroutinesOrFDs(t *testing.T) {
	const (
		totalSearches = 500
		workers       = 16
	)
	svc := retrieval.Service{
		Routing:        cancelAwareRouting{},
		Registry:       cancelAwareRegistry{},
		Spaces:         cancelAwareEncoder{},
		QueryVectors:   retrieval.NewQueryVectorCache(),
		Projection:     cancelAwareProjection{},
		ProfilesRouter: cancelAwareRouter{},
		Content: content.Service{
			RecordStore: fakeRecords{},
			Baseline:    cancelAwareBaseline{},
			Blobs:       cancelAwareBlobs{},
		},
	}

	// Warm up one-time runtime/schema initialization before capturing baselines.
	if _, err := svc.Search(t.Context(), searchScope, retrieval.Request{
		Query:     "warmup",
		CorpusIDs: []string{"corpus_a"},
		Mode:      "hybrid",
		Profile:   "deep",
		Limit:     4,
	}); err != nil {
		t.Fatalf("warmup search failed: %v", err)
	}
	synctest.Wait()

	runtime.GC()
	baseGoroutines := runtime.NumGoroutine()
	baseFDs, fdErr := openFDCount()
	hasFDs := true
	if fdErr != nil {
		if errors.Is(fdErr, fs.ErrNotExist) {
			hasFDs = false
		} else {
			t.Fatalf("read baseline open FD count: %v", fdErr)
		}
	}

	rng := rand.New(rand.NewPCG(2591, 9001))
	plans := make([]struct {
		phase   cancelPhase
		profile string
		async   bool
		query   string
	}, totalSearches)
	var phaseHits [phaseCount]int
	for i := range plans {
		ph := cancelPhase(rng.IntN(int(phaseCount)))
		phaseHits[ph]++
		profile := "default"
		if i%2 == 1 {
			profile = "deep"
		}
		plans[i] = struct {
			phase   cancelPhase
			profile string
			async   bool
			query   string
		}{
			phase:   ph,
			profile: profile,
			async:   i%2 == 0,
			// Unique queries force fresh QueryVectorCache flight goroutines.
			query: "cancel-query-" + strconv.Itoa(i),
		}
	}
	for ph, n := range phaseHits {
		if n == 0 {
			t.Fatalf("phase %d was never sampled in %d searches", ph, totalSearches)
		}
	}

	var next atomic.Int32
	var completed atomic.Int32
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for {
				idx := int(next.Add(1) - 1)
				if idx >= totalSearches {
					return
				}
				p := plans[idx]
				ctx, cancel := context.WithCancel(t.Context())
				plan := &cancelPlan{
					target:   p.phase,
					cancel:   cancel,
					canceled: ctx.Done(),
					async:    p.async,
				}
				ctx = context.WithValue(ctx, cancelPlanKey{}, plan)
				_, _ = svc.Search(ctx, searchScope, retrieval.Request{
					Query:     p.query,
					CorpusIDs: []string{"corpus_a"},
					Mode:      "hybrid",
					Profile:   p.profile,
					Limit:     4,
				})
				cancel()
				completed.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := completed.Load(); got != totalSearches {
		t.Fatalf("completed=%d; want %d", got, totalSearches)
	}

	// Deterministic quiescence: every spawned search leg and QueryVectorCache
	// flight goroutine in the bubble must exit before synctest.Wait returns.
	synctest.Wait()
	runtime.GC()

	curGoroutines := runtime.NumGoroutine()
	if gDelta := curGoroutines - baseGoroutines; gDelta > 2 {
		t.Fatalf("goroutine leak after %d cancelled searches: baseline=%d current=%d delta=%d (want <= 2)",
			totalSearches, baseGoroutines, curGoroutines, gDelta)
	}
	if hasFDs {
		curFDs, err := openFDCount()
		if err != nil {
			t.Fatalf("read open FD count after %d cancelled searches: %v", totalSearches, err)
		}
		if fdDelta := curFDs - baseFDs; fdDelta > 2 {
			t.Fatalf("open FD leak after %d cancelled searches: baseline=%d current=%d delta=%d (want <= 2)",
				totalSearches, baseFDs, curFDs, fdDelta)
		}
	}
}
