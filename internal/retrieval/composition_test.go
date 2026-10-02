package retrieval_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost/fakeplugin"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

func TestMain(m *testing.M) { fakeplugin.MaybeRun(); os.Exit(m.Run()) }

// Real plugin processes protect the new protocol's composition boundary:
// two identities, nested rounds, authorized ranking and per-level paid usage.
func TestProfileCandidatesReorderAnotherPluginRanking(t *testing.T) {
	start := func(id, mode, requires string) *plugins.Pin {
		t.Helper()
		dir := t.TempDir()
		raw := fmt.Sprintf(`id: %s
version: 1.0.0
compatibility: {engine: ">=0.1.0 <0.2.0", plugin_api: ">=0.12.0 <0.13.0"}
contributions:
  retrieval:
    profiles:
      default: {max_latency_ms: 100, max_cost_cents: 1}
%s`, id, requires)
		manifest := filepath.Join(dir, plugins.ManifestFile)
		if err := os.WriteFile(manifest, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		var logs strings.Builder
		p, err := devhost.Start(devhost.Options{Dir: dir, Manifest: manifest, Command: fakeplugin.Command(), Env: []string{fakeplugin.EnvEnable + "=1", fakeplugin.EnvMode + "=" + mode}, Output: &logs})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = p.Stop(time.Second)
			if t.Failed() {
				t.Log(logs.String())
			}
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.WaitHealthy(ctx); err != nil {
			t.Fatalf("%v: %s", err, logs.String())
		}
		pin, err := plugins.LoadPin(plugins.PinConfig{Manifest: manifest, Endpoint: p.BaseURL})
		if err != nil {
			t.Fatal(err)
		}
		return pin
	}
	base := start("core.retrieve", "retrieval-paid", "")
	outer := start("example.rerank", "retrieval-compose", "requires: [{plugin: core.retrieve, version: '>=1.0.0 <2.0.0', profiles: [default]}]\n")
	set, err := plugins.NewPinSet([]*plugins.Pin{outer, base})
	if err != nil {
		t.Fatal(err)
	}
	live, err := plugins.NewLive("first", set)
	if err != nil {
		t.Fatal(err)
	}
	projection := &fakeProjection{candidates: []content.Candidate{{SegmentID: "a", GenerationID: "gen", Score: 3}, {SegmentID: "stale-1", GenerationID: "gen"}, {SegmentID: "b", GenerationID: "gen", Score: 2}}}
	s := service(projection, &fakeEmbeddings{})
	s.Registry = nil
	s.ProfilesRouter = pluginhttp.LiveRetriever{Live: live, Aliases: map[string]string{"default": "example.rerank/default"}}
	result, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "original", Mode: "lexical", CorpusIDs: []string{"corpus"}, SourceNamespaces: []string{"public"}, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 2 || result.Hits[0].Segment.ID != "b" || result.Hits[1].Segment.ID != "a" || result.Hits[0].Score <= 0 || result.Hits[0].Explanation != "reordered: core.retrieve: reciprocal rank fusion" {
		t.Fatalf("hits %+v", result.Hits)
	}
	if len(projection.searched) != 1 || projection.searched[0].Query != "rewritten query" || fmt.Sprint(projection.searched[0].SourceNamespaces) != "[public]" {
		t.Fatalf("inner query %+v", projection.searched)
	}
	u := result.Usage
	if u == nil || u.PaidCalls != 2 || u.CostCents != 0.375 || len(u.Profiles) != 2 || u.Profiles[0].Profile != "example.rerank/default" || u.Profiles[1].Profile != "core.retrieve/default" || u.Profiles[0].CostCents != 0.25 || u.Profiles[1].CostCents != 0.125 {
		t.Fatalf("usage %+v", u)
	}
}

type profileRanker struct {
	m     plugins.Manifest
	round func(context.Context, plugins.SearchRequest) ([]byte, error)
}

func (r *profileRanker) Manifest() *plugins.Manifest    { return &r.m }
func (r *profileRanker) Configuration() json.RawMessage { return []byte(`{}`) }
func (r *profileRanker) Round(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
	return r.round(ctx, q)
}

type profileMap map[string]*profileRanker

func (p profileMap) Profiles() []retrieval.Profile { return nil }
func (p profileMap) Resolve(name string) (retrieval.Ranker, string, bool) {
	r, ok := p[name]
	return r, "default", ok
}

// Budget observations are synchronous: no waits for a clock to expire. The
// engine supplies the child with less time/cost and stops a violated chain.
func TestCompositionSharesDeadlineAndCost(t *testing.T) {
	for _, tc := range []struct {
		name               string
		outerMax, innerMax float64
		calls              int
		want               error
	}{
		{"shared remaining budget", 1, 1, 4, nil},
		{"decimal allowance", 0.3, 1, 4, nil},
		{"decimal allowance exceeded", 0.3, 1, 4, retrieval.ErrPluginInvalid},
		{"legacy inner receives old fields", 1, 1, 4, nil},
		{"inner mode override", 1, 1, 4, nil},
		{"mode override requires new API", 1, 1, 1, retrieval.ErrPluginInvalid},
		{"inner filter on legacy projection", 1, 1, 1, retrieval.ErrSourceFilterUnavailable},
		{"outer cost includes inner", 0.4, 1, 2, retrieval.ErrPluginInvalid},
		{"exhausted budget rejects paid final round", 0.5, 1, 4, retrieval.ErrPluginInvalid},
		{"exhausted budget permits free final round", 0.5, 1, 4, nil},
		{"free chain", 0, 0, 4, nil},
		{"inner own budget", 1, 0.1, 2, retrieval.ErrPluginInvalid},
		{"deadline expired during outer", 1, 1, 1, retrieval.ErrDeadline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := func(id string, cost float64) plugins.Manifest {
				return plugins.Manifest{ID: id, Version: "1.0.0", Compatibility: plugins.Compatibility{PluginAPI: ">=0.12.0 <0.13.0"}, Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{Profiles: map[string]plugins.RetrievalProfile{"default": {MaxLatencyMS: 2000, MaxCostCents: cost}}}}}
			}
			outer := &profileRanker{m: manifest("example.rerank", tc.outerMax)}
			if tc.name == "inner mode override" {
				outer.m.Compatibility.PluginAPI = ">=0.13.0 <0.14.0"
			}
			inner := &profileRanker{m: manifest("core.retrieve", tc.innerMax)}
			if tc.name == "legacy inner receives old fields" {
				inner.m.Compatibility.PluginAPI = ">=0.7.0 <0.13.0"
			}
			outer.m.Requires = []plugins.Requirement{{Plugin: "core.retrieve", Version: ">=1.0.0", Profiles: []string{"default"}}}
			outerCost, innerCost, finalCost := 0.2, 0.3, 0.1
			paidCalls := 3
			if tc.name == "decimal allowance" || tc.name == "decimal allowance exceeded" {
				outerCost, innerCost, finalCost, paidCalls = 0.1, 0.2, 0, 2
				if tc.name == "decimal allowance exceeded" {
					finalCost, paidCalls = 0.0001, 3
				}
			}
			if tc.name == "exhausted budget permits free final round" {
				finalCost, paidCalls = 0, 2
			}
			if tc.name == "free chain" {
				outerCost, innerCost, finalCost, paidCalls = 0, 0, 0, 0
			}
			usage := func(cost float64) map[string]any {
				calls := 0
				if cost > 0 {
					calls = 1
				}
				return map[string]any{"paid_calls": calls, "cost_cents": cost}
			}
			calls := 0
			var expire context.CancelCauseFunc
			outer.round = func(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
				calls++
				if q.Round == 1 {
					if tc.name == "deadline expired during outer" {
						expire(context.DeadlineExceeded)
					}
					request := map[string]any{"primitive": "profile", "profile": map[string]any{"name": "core.retrieve/default", "limit": 1}}
					if tc.name == "inner mode override" || tc.name == "mode override requires new API" {
						request["profile"].(map[string]any)["mode"] = "hybrid"
					}
					if tc.name == "inner filter on legacy projection" {
						request["filter"] = map[string]any{"source_namespaces": []string{"public"}}
					}
					return answer(map[string]any{"requests": []any{request}, "usage": usage(outerCost)})
				}
				wantRemaining := tc.outerMax - outerCost - innerCost
				if q.Budget == nil || math.Abs(q.Budget.RemainingCostCents-wantRemaining) > 1e-9 {
					t.Errorf("outer remaining budget: %+v, want %g", q.Budget, wantRemaining)
				}
				return answer(map[string]any{"ranking": map[string]any{"hits": []any{}}, "usage": usage(finalCost)})
			}
			parentDeadline := time.Now().Add(time.Second)
			inner.round = func(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
				calls++
				deadline, _ := ctx.Deadline()
				if deadline.After(parentDeadline.Add(-10 * time.Millisecond)) {
					t.Errorf("inner deadline %s exceeds parent minus margin %s", deadline, parentDeadline)
				}
				if q.Round == 1 {
					wantRemaining := min(tc.outerMax-outerCost, tc.innerMax)
					budgetOK := q.Budget != nil && math.Abs(q.Budget.RemainingCostCents-wantRemaining) <= 1e-9
					if tc.name == "legacy inner receives old fields" {
						budgetOK = q.Budget == nil
					}
					if q.Query.Text != "original" || !budgetOK {
						t.Errorf("inner request %+v; remaining %g", q, wantRemaining)
					}
					wantMode := "lexical"
					if tc.name == "inner mode override" {
						wantMode = "hybrid"
					}
					if q.Query.Mode != wantMode {
						t.Errorf("inner mode %q, want %q", q.Query.Mode, wantMode)
					}
					return answer(map[string]any{"requests": []any{map[string]any{"primitive": "bm25", "query_text": q.Query.Text, "k": 1}}, "usage": usage(innerCost)})
				}
				return answer(map[string]any{"ranking": map[string]any{"hits": []any{}}})
			}
			s := service(&fakeProjection{}, &fakeEmbeddings{})
			if tc.name == "inner filter on legacy projection" {
				s.Routing = fakeRouting{unprojected: true}
			}
			s.ProfilesRouter = profileMap{"default": outer, "core.retrieve/default": inner}
			ctx, cancel := context.WithDeadline(context.Background(), parentDeadline)
			defer cancel()
			ctx, expire = context.WithCancelCause(ctx)
			defer expire(nil)
			out, err := s.Search(ctx, searchScope, retrieval.Request{Query: "original", Mode: "lexical", CorpusIDs: []string{"corpus"}})
			if (tc.want == nil && err != nil) || (tc.want != nil && !errors.Is(err, tc.want)) || calls != tc.calls {
				t.Fatalf("result %+v error %v calls %d; want %v %d", out, err, calls, tc.want, tc.calls)
			}
			if err == nil && (out.Usage.PaidCalls != paidCalls || math.Abs(out.Usage.CostCents-(outerCost+innerCost+finalCost)) > 1e-9) {
				t.Fatalf("aggregate %+v", out.Usage)
			}
		})
	}
}
