package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

// Owns AIMD concurrency adaptation, [min, max] clamping, bounded FIFO queueing,
// Retry-After shedding, and leak-free cancellation under a virtual clock.
func TestSearchAdmissionAIMDAdaptsBoundsAndShedsExcess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := httpapi.NewSearchAdmission(httpapi.SearchAdmissionConfig{
			InitialLimit:  8,
			MinLimit:      4,
			MaxLimit:      10,
			MaxQueue:      2,
			QueueTimeout:  50 * time.Millisecond,
			TargetLatency: 200 * time.Millisecond,
			BackoffRatio:  0.75,
			Window:        200 * time.Millisecond,
		})

		// Low utilization (< limit/2) must not inflate limit (app-limited).
		for i := 0; i < 4; i++ {
			lease, _, ok := limiter.Acquire(context.Background())
			if !ok {
				t.Fatal("failed to acquire idle slot")
			}
			time.Sleep(40 * time.Millisecond)
			lease.Release(false, false)
			time.Sleep(210 * time.Millisecond)
		}
		if stats := limiter.Stats(); stats.Limit != 8 {
			t.Fatalf("low-utilization limit=%d; want unchanged 8", stats.Limit)
		}

		// High utilization (>= limit/2) with healthy latency increases additively up to MaxLimit.
		for step := 0; step < 5; step++ {
			var leases []*httpapi.AdmissionLease
			for i := 0; i < 6; i++ {
				lease, _, ok := limiter.Acquire(context.Background())
				if !ok {
					t.Fatalf("step %d slot %d refused unexpectedly", step, i)
				}
				leases = append(leases, lease)
			}
			time.Sleep(40 * time.Millisecond)
			for _, lease := range leases {
				lease.Release(false, false)
			}
			time.Sleep(210 * time.Millisecond)
		}
		if stats := limiter.Stats(); stats.Limit != 10 {
			t.Fatalf("healthy high-utilization limit=%d; want clamped MaxLimit 10", stats.Limit)
		}

		// Slow downstream / overload decreases multiplicatively down to MinLimit.
		for step := 0; step < 6; step++ {
			lease, _, ok := limiter.Acquire(context.Background())
			if !ok {
				t.Fatalf("backoff step %d acquire failed", step)
			}
			time.Sleep(350 * time.Millisecond) // exceeds 200ms TargetLatency
			lease.Release(false, false)
		}
		if stats := limiter.Stats(); stats.Limit != 4 {
			t.Fatalf("slow downstream limit=%d; want clamped MinLimit 4", stats.Limit)
		}

		// Fill all 4 active slots + 2 bounded queue slots; 7th request must shed immediately.
		active := make([]*httpapi.AdmissionLease, 4)
		for i := range active {
			lease, _, ok := limiter.Acquire(context.Background())
			if !ok {
				t.Fatalf("active slot %d refused", i)
			}
			active[i] = lease
		}
		queued1Done := make(chan bool, 1)
		queued2Done := make(chan bool, 1)
		q2Ctx, q2Cancel := context.WithCancel(context.Background())
		go func() {
			lease, _, ok := limiter.Acquire(context.Background())
			if ok {
				lease.Release(false, false)
			}
			queued1Done <- ok
		}()
		go func() {
			lease, _, ok := limiter.Acquire(q2Ctx)
			if ok {
				lease.Release(false, false)
			}
			queued2Done <- ok
		}()
		synctest.Wait()
		if stats := limiter.Stats(); stats.InUse != 4 || stats.Queued != 2 || stats.Available != 0 {
			t.Fatalf("saturated stats=%+v; want in_use=4 queued=2 available=0", stats)
		}

		// Excess beyond bounded queue sheds immediately with Retry-After >= 1.
		shedStart := time.Now()
		_, retryAfter, admitted := limiter.Acquire(context.Background())
		if admitted || retryAfter < 1 || time.Since(shedStart) != 0 {
			t.Fatalf("excess request: admitted=%t retryAfter=%d elapsed=%s; want immediate shed with Retry-After>=1", admitted, retryAfter, time.Since(shedStart))
		}

		// Cancel queued #2 -> removed cleanly without leaking; release 1 active slot -> queued #1 admitted FIFO.
		q2Cancel()
		if ok := <-queued2Done; ok {
			t.Fatal("canceled waiter should not be admitted")
		}
		active[0].Release(false, true)
		if ok := <-queued1Done; !ok {
			t.Fatal("FIFO waiter #1 should be admitted when slot frees before QueueTimeout")
		}
		for _, lease := range active[1:] {
			lease.Release(false, true)
		}
		if stats := limiter.Stats(); stats.InUse != 0 || stats.Queued != 0 || stats.Available != stats.Limit {
			t.Fatalf("post-drain stats=%+v; want clean zero in_use/queued", stats)
		}
	})
}

type slowSearchDep struct {
	delay time.Duration
}

func (slowSearchDep) Authorize(context.Context, corpus.Scope, []string) error { return nil }
func (slowSearchDep) Generation(context.Context, string, string) (content.Generation, error) {
	return content.Generation{ID: "gen", ProfileVersion: retrieval.ProfileVersion, SpaceID: "space"}, nil
}
func (slowSearchDep) Publish(context.Context, content.Generation, string, string, string, content.Version, content.Segmentation) error {
	return nil
}
func (slowSearchDep) PublishEmbeddings(context.Context, content.Generation, string, []content.EmbeddingData) error {
	return nil
}
func (s slowSearchDep) Search(ctx context.Context, _ []retrieval.Route, _ corpus.Scope, _ retrieval.Request) ([]content.Candidate, error) {
	select {
	case <-time.After(s.delay):
		return []content.Candidate{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (slowSearchDep) Manifest() *plugins.Manifest {
	return &plugins.Manifest{
		ID:      "core.retrieve",
		Version: "1.0.0",
		Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{
			Profiles: map[string]plugins.RetrievalProfile{"default": {MaxLatencyMS: 500}},
		}},
	}
}
func (slowSearchDep) Configuration() json.RawMessage { return json.RawMessage(`{}`) }
func (slowSearchDep) Round(_ context.Context, q plugins.SearchRequest) ([]byte, error) {
	if q.Round == 1 {
		return json.Marshal(map[string]any{"requests": []any{map[string]any{"primitive": "bm25", "query_text": q.Query.Text, "k": q.Limit}}})
	}
	return json.Marshal(map[string]any{"ranking": map[string]any{"hits": []any{}}})
}

// Simulates a 100 req/s overload burst against a slow downstream backend (600ms)
// through the real HTTP boundary + OpenAPI validator. Proves AIMD shrinks
// concurrency, sheds excess load cleanly with 503 Retry-After, keeps tail
// latency bounded, and incurs zero cascading 504 timeouts.
func TestSearchAdmissionPeakOverloadBurstBoundsTailLatency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dep := slowSearchDep{delay: 600 * time.Millisecond}
		limiter := httpapi.NewSearchAdmission(httpapi.SearchAdmissionConfig{
			InitialLimit:  32,
			MinLimit:      4,
			MaxLimit:      64,
			MaxQueue:      8,
			QueueTimeout:  80 * time.Millisecond,
			TargetLatency: 200 * time.Millisecond,
			BackoffRatio:  0.75,
			Window:        200 * time.Millisecond,
		})
		metrics := telemetry.NewLoadMetrics()
		keys := map[string]corpus.Scope{
			observer: {Organization: "org_a", Actions: []string{"search:query", "content:read"}, Corpora: []string{"*"}},
		}
		handler, err := httpapi.New(
			knownCorpora{}, content.Service{},
			retrieval.Service{Ranker: dep, Routing: dep, Projection: dep},
			uploads.Service{}, keys, catalogCursorKey,
			httpapi.WithLoadMetrics(metrics),
			httpapi.WithSearchAdmission(limiter),
		)
		if err != nil {
			t.Fatal(err)
		}
		api := checkedAPI(t, handler)

		// 100 req/s burst over 1.2s (120 total requests, 10ms apart).
		const totalRequests = 120
		var (
			wg         sync.WaitGroup
			okCount    atomic.Int32
			shedCount  atomic.Int32
			timeout504 atomic.Int32
			otherErr   atomic.Int32
			mu         sync.Mutex
			maxShed    time.Duration
			maxAdmit   time.Duration
		)
		for i := 0; i < totalRequests; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				started := time.Now()
				req := httptest.NewRequest(http.MethodPost, "/v0/search", strings.NewReader(`{"query":"harbour","corpus_ids":["corpus_a"],"mode":"lexical"}`))
				req.Header.Set("Authorization", "Bearer "+observer)
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				api.ServeHTTP(w, req)
				elapsed := time.Since(started)
				switch w.Code {
				case http.StatusOK:
					okCount.Add(1)
					mu.Lock()
					if elapsed > maxAdmit {
						maxAdmit = elapsed
					}
					mu.Unlock()
				case http.StatusServiceUnavailable:
					retryAfter, convErr := strconv.Atoi(w.Header().Get("Retry-After"))
					if convErr != nil || retryAfter < 1 {
						otherErr.Add(1)
						return
					}
					shedCount.Add(1)
					mu.Lock()
					if elapsed > maxShed {
						maxShed = elapsed
					}
					mu.Unlock()
				case http.StatusGatewayTimeout:
					timeout504.Add(1)
				default:
					otherErr.Add(1)
				}
			}()
			time.Sleep(10 * time.Millisecond) // 100 req/s arrival rate
		}
		wg.Wait()

		if timeout504.Load() != 0 || otherErr.Load() != 0 {
			t.Fatalf("overload burst errors: 200=%d 503_shed=%d 504_timeout=%d other=%d", okCount.Load(), shedCount.Load(), timeout504.Load(), otherErr.Load())
		}
		if okCount.Load() == 0 || shedCount.Load() == 0 {
			t.Fatalf("expected both admitted successes and clean sheds; got 200=%d 503_shed=%d", okCount.Load(), shedCount.Load())
		}
		if maxShed > 100*time.Millisecond {
			t.Fatalf("shed tail latency %s exceeded bounded queue wait", maxShed)
		}
		if maxAdmit > 750*time.Millisecond {
			t.Fatalf("admitted tail latency %s exceeded bounded execution (queue + backend 600ms)", maxAdmit)
		}
		stats := limiter.Stats()
		if stats.Limit >= 32 || stats.Limit < 4 || stats.InUse != 0 || stats.Queued != 0 {
			t.Fatalf("AIMD post-overload stats=%+v; want 4 <= limit < 32 and drained in_use/queued", stats)
		}
		var prom strings.Builder
		metrics.Write(&prom)
		for _, want := range []string{
			"quivr_search_admission_capacity " + strconv.Itoa(stats.Limit),
			"quivr_search_admission_in_use 0",
			"quivr_search_admission_queued 0",
			"quivr_search_admission_refused_total " + strconv.Itoa(int(shedCount.Load())),
		} {
			if !strings.Contains(prom.String(), want+"\n") {
				t.Fatalf("missing metric %q in:\n%s", want, prom.String())
			}
		}
	})
}

type deepWireRouter struct {
	deepErr error
}

type simpleRanker struct {
	m     plugins.Manifest
	round func(context.Context, plugins.SearchRequest) ([]byte, error)
}

func (r simpleRanker) Manifest() *plugins.Manifest    { return &r.m }
func (r simpleRanker) Configuration() json.RawMessage { return json.RawMessage(`{}`) }
func (r simpleRanker) Round(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
	return r.round(ctx, q)
}

func (r deepWireRouter) Profiles() []retrieval.Profile { return nil }
func (r deepWireRouter) Resolve(name string) (retrieval.Ranker, string, bool) {
	base := simpleRanker{
		m: plugins.Manifest{
			ID: "core.retrieve", Version: "1.0.0",
			Compatibility: plugins.Compatibility{PluginAPI: ">=0.13.0 <0.14.0"},
			Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{Profiles: map[string]plugins.RetrievalProfile{"default": {MaxLatencyMS: 500}}}},
		},
		round: func(_ context.Context, q plugins.SearchRequest) ([]byte, error) {
			if q.Round == 1 {
				return json.Marshal(map[string]any{"requests": []any{map[string]any{"primitive": "bm25", "query_text": q.Query.Text, "k": q.Limit}}})
			}
			return json.Marshal(map[string]any{"ranking": map[string]any{"hits": []any{}}})
		},
	}
	deep := simpleRanker{
		m: plugins.Manifest{
			ID: "example.rerank", Version: "1.0.0",
			Compatibility: plugins.Compatibility{PluginAPI: ">=0.13.0 <0.14.0"},
			Requires:      []plugins.Requirement{{Plugin: "core.retrieve", Version: ">=1.0.0", Profiles: []string{"default"}}},
			Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{Profiles: map[string]plugins.RetrievalProfile{"deep": {MaxLatencyMS: 3000}}}},
		},
		round: func(_ context.Context, q plugins.SearchRequest) ([]byte, error) {
			if q.Round == 1 {
				return json.Marshal(map[string]any{"requests": []any{map[string]any{"primitive": "profile", "profile": map[string]any{"name": "core.retrieve/default", "mode": "hybrid", "limit": q.Limit}}}})
			}
			return nil, r.deepErr
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

// Owns the HTTP OpenAPI wire contract and bounded Prometheus counter for
// deep -> default degradations (reranker_unavailable and reranker_budget_exceeded).
func TestDeepDegradationWireContractAndMetrics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dep := slowSearchDep{}
		router := &deepWireRouter{deepErr: errors.New("reranker 503")}
		metrics := telemetry.NewLoadMetrics()
		keys := map[string]corpus.Scope{
			observer: {Organization: "org_a", Actions: []string{"search:query", "content:read"}, Corpora: []string{"*"}},
		}
		handler, err := httpapi.New(
			knownCorpora{}, content.Service{},
			retrieval.Service{ProfilesRouter: router, Routing: dep, Projection: dep, MinRerankBudget: 250 * time.Millisecond},
			uploads.Service{}, keys, catalogCursorKey,
			httpapi.WithLoadMetrics(metrics),
		)
		if err != nil {
			t.Fatal(err)
		}
		api := checkedAPI(t, handler)

		call := func(ctx context.Context, wantReason string) {
			t.Helper()
			req := httptest.NewRequest(http.MethodPost, "/v0/search", strings.NewReader(`{"query":"harbour","corpus_ids":["corpus_a"],"mode":"lexical","profile":"deep"}`)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+observer)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			api.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s; want 200 degraded fallback", w.Code, w.Body.String())
			}
			if gotDegraded, gotReason := w.Header().Get("X-Quivr-Degraded"), w.Header().Get("X-Quivr-Degraded-Reason"); gotDegraded != "true" || gotReason != wantReason {
				t.Fatalf("degraded headers = (%q, %q); want (\"true\", %q)", gotDegraded, gotReason, wantReason)
			}
			var body struct {
				RetrievalProfile struct {
					Name     string `json:"name"`
					Version  string `json:"version"`
					Degraded []struct {
						Reason    string   `json:"reason"`
						CorpusIDs []string `json:"corpus_ids"`
					} `json:"degraded"`
				} `json:"retrieval_profile"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			wantDegraded := []struct {
				Reason    string   `json:"reason"`
				CorpusIDs []string `json:"corpus_ids"`
			}{{Reason: wantReason, CorpusIDs: []string{"corpus_a"}}}
			if body.RetrievalProfile.Name != "deep" || body.RetrievalProfile.Version != "plugin:core.retrieve@1.0.0/default" || !reflect.DeepEqual(body.RetrievalProfile.Degraded, wantDegraded) {
				t.Fatalf("unexpected profile response: %+v", body.RetrievalProfile)
			}
		}

		// Case 1: Reranker unavailable.
		call(context.Background(), retrieval.DegradationRerankerUnavailable)

		// Case 2: Tight deadline budget skips reranker.
		tightCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		call(tightCtx, retrieval.DegradationRerankerBudgetExceeded)

		var prom strings.Builder
		metrics.Write(&prom)
		for _, want := range []string{
			`quivr_search_degradations_total{reason="reranker_unavailable"} 1`,
			`quivr_search_degradations_total{reason="reranker_budget_exceeded"} 1`,
		} {
			if !strings.Contains(prom.String(), want+"\n") {
				t.Fatalf("missing degradation metric %q in:\n%s", want, prom.String())
			}
		}
	})
}

// Owns regression protection against false AIMD collapse under healthy mixed
// latency traffic (sub-10ms lexical/error responses + 60ms hybrid bursts) and
// healthy slow-objective profiles (e.g., deep 1200ms alongside default 50ms).
func TestSearchAdmissionMixedHealthyTrafficAndDeepProfileDoNotCollapseLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := httpapi.NewSearchAdmission(httpapi.SearchAdmissionConfig{
			InitialLimit:  64,
			MinLimit:      4,
			MaxLimit:      128,
			TargetLatency: 300 * time.Millisecond,
			GradientRatio: 3.0,
			BackoffRatio:  0.75,
			Window:        250 * time.Millisecond,
		})

		// Fast 5ms lexical hits establish low minRTT; subsequent 80ms healthy hybrid
		// burst (< target/2 = 150ms) must not trip gradient backoff.
		for i := 0; i < 4; i++ {
			lease, _, ok := limiter.Acquire(context.Background())
			if !ok {
				t.Fatal("failed to acquire fast lexical slot")
			}
			time.Sleep(5 * time.Millisecond)
			lease.Release(false, false)
		}
		for wave := 0; wave < 4; wave++ {
			var leases []*httpapi.AdmissionLease
			for i := 0; i < 16; i++ {
				lease, _, ok := limiter.Acquire(context.Background())
				if !ok {
					t.Fatalf("wave %d slot %d refused", wave, i)
				}
				leases = append(leases, lease)
			}
			time.Sleep(80 * time.Millisecond)
			for _, lease := range leases {
				lease.Release(false, false)
			}
			time.Sleep(260 * time.Millisecond)
		}
		if stats := limiter.Stats(); stats.Limit < 64 {
			t.Fatalf("healthy 80ms hybrid burst collapsed limit to %d; want >= 64", stats.Limit)
		}

		// Non-overload 403/422 errors (skipObserve=true) and healthy 1200ms deep
		// requests (3000ms profile objective) must not degrade default capacity.
		for i := 0; i < 12; i++ {
			lease, _, ok := limiter.Acquire(context.Background())
			if !ok {
				t.Fatal("failed to acquire validation-error slot")
			}
			time.Sleep(1 * time.Millisecond)
			lease.ReleaseWithTarget(false, true, 0)
		}
		for i := 0; i < 12; i++ {
			lease, _, ok := limiter.Acquire(context.Background())
			if !ok {
				t.Fatalf("deep slot %d refused", i)
			}
			time.Sleep(1200 * time.Millisecond)
			lease.ReleaseWithTarget(false, false, 3*time.Second)
		}
		if stats := limiter.Stats(); stats.Limit < 64 {
			t.Fatalf("healthy deep profile traffic collapsed limit to %d; want >= 64", stats.Limit)
		}
	})
}
