package retrieval_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
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
		Limits:   plugins.RetrievalLimits{MaxRounds: 3, MaxRequests: 2, MaxCandidates: 20},
	}}}
}
func (r *scriptedRanker) Configuration() json.RawMessage { return json.RawMessage(`{}`) }
func (r *scriptedRanker) Round(ctx context.Context, q plugins.SearchRequest) ([]byte, error) {
	r.sent = append(r.sent, q)
	return r.answer(ctx, q)
}

func answer(v any) ([]byte, error) { return json.Marshal(v) }

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
	p := &fakeProjection{candidates: []content.Candidate{{SegmentID: "stale-1", GenerationID: "gen"}, {SegmentID: "vec-a", GenerationID: "gen", Score: 2}, {SegmentID: "vec-a", GenerationID: "gen", Score: 1}, {SegmentID: "b", GenerationID: "gen", Score: 0.5}, {SegmentID: "vec-c", GenerationID: "gen"}}}
	r := &scriptedRanker{answer: fusion}
	result, err := rankedService(p, r).Search(context.Background(), searchScope, retrieval.Request{Query: " lanterne ", CorpusIDs: []string{"corpus"}, Profile: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.searched) != 2 || p.searched[0].Mode != "lexical" || p.searched[0].K != 6 || p.searched[1].Mode != "semantic" || p.searched[1].Space != "space" || len(p.searched[1].Vector) != 1 {
		t.Fatalf("projection searched %+v; want bm25 then near_vector, k oversampled", p.searched)
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
}

// The engine enforces the contract on every plugin answer, and the profile's
// deadline on the whole search.
func TestPluginAnswersTheEngineRefuses(t *testing.T) {
	p := &fakeProjection{candidates: []content.Candidate{{SegmentID: "a", GenerationID: "gen"}}}
	for _, c := range []struct {
		name    string
		profile string
		answer  func(context.Context, plugins.SearchRequest) ([]byte, error)
		want    error
		rounds  int
	}{
		{name: "a candidate never served", answer: func(context.Context, plugins.SearchRequest) ([]byte, error) {
			return answer(map[string]any{"ranking": map[string]any{"hits": []any{map[string]any{"segment_id": "a", "score": 1}}}})
		}, want: retrieval.ErrPluginInvalid, rounds: 1},
		{name: "more candidates in the last round", answer: func(context.Context, plugins.SearchRequest) ([]byte, error) {
			return answer(map[string]any{"requests": []any{map[string]any{"primitive": "bm25", "query_text": "x", "k": 1}}})
		}, want: retrieval.ErrPluginInvalid, rounds: 3},
		{name: "k over the declared limit", answer: func(context.Context, plugins.SearchRequest) ([]byte, error) {
			return answer(map[string]any{"requests": []any{map[string]any{"primitive": "bm25", "query_text": "x", "k": 21}}})
		}, want: retrieval.ErrPluginInvalid, rounds: 1},
		{name: "the plugin refuses the query", answer: func(context.Context, plugins.SearchRequest) ([]byte, error) {
			return nil, content.ErrInvalid
		}, want: retrieval.ErrUnsupported, rounds: 1},
		{name: "the plugin is unreachable", answer: func(context.Context, plugins.SearchRequest) ([]byte, error) {
			return nil, errors.New("connection refused")
		}, want: retrieval.ErrUnavailable, rounds: 1},
		{name: "the profile's deadline passes", profile: "deep", answer: func(ctx context.Context, _ plugins.SearchRequest) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, want: retrieval.ErrDeadline, rounds: 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &scriptedRanker{answer: c.answer}
			_, err := rankedService(p, r).Search(context.Background(), searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: []string{"corpus"}, Profile: c.profile})
			if !errors.Is(err, c.want) || len(r.sent) != c.rounds {
				t.Fatalf("error %v after %d rounds, want %v after %d", err, len(r.sent), c.want, c.rounds)
			}
		})
	}
}

// Profiles resolve against what the deployment answers: the built-in path
// answers default only, a pinned plugin its declared profiles; balanced is
// the deprecated name of default.
func TestSearchResolvesProfiles(t *testing.T) {
	for _, c := range []struct {
		profile string
		plugin  bool
		want    string
		err     error
	}{
		{profile: "", want: "default"},
		{profile: "balanced", want: "default"},
		{profile: "deep", err: retrieval.ErrUnsupportedProfile},
		{profile: "", plugin: true, want: "default"},
		{profile: "balanced", plugin: true, want: "default"},
		{profile: "deep", plugin: true, want: "deep"},
		{profile: "fast", plugin: true, err: retrieval.ErrUnsupportedProfile},
	} {
		s := service(&fakeProjection{}, &fakeEmbeddings{})
		if c.plugin {
			s = rankedService(&fakeProjection{}, &scriptedRanker{answer: func(context.Context, plugins.SearchRequest) ([]byte, error) {
				return answer(map[string]any{"ranking": map[string]any{"hits": []any{}}})
			}})
		}
		result, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "lanterne", CorpusIDs: []string{"corpus"}, Profile: c.profile})
		if !errors.Is(err, c.err) || (c.err == nil && result.Profile != c.want) {
			t.Errorf("profile %q (plugin %v): %q, %v; want %q, %v", c.profile, c.plugin, result.Profile, err, c.want, c.err)
		}
	}
}
