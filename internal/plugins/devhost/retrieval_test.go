package devhost_test

import (
	"context"
	"encoding/json"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Offline profile fixtures model the ranking boundary without invoking a
// dependency. The requested limit, query and supplied score/explanation survive.
func TestProfileCandidateCatalogue(t *testing.T) {
	m := plugins.Validate([]byte(`id: example.rerank
version: 1.0.0
compatibility: {engine: ">=0.1.0 <0.2.0", plugin_api: ">=0.12.0 <0.13.0"}
contributions:
  retrieval:
    profiles:
      default: {max_latency_ms: 100, max_cost_cents: 1}
requires: [{plugin: core.retrieve, version: ">=1.0.0", profiles: [default]}]
`))
	if !m.Valid {
		t.Fatal(m.Errors)
	}
	for _, tc := range []struct{ name, query, order, want string }{
		{"inherit query", "", "", "a"},
		{"rewrite query", "second", "", "b"},
		{"explicit ranking", "", " ,\"order\":{\"profile\":[\"b\",\"a\"]}", "b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := `{"retrieval":{"query":"first","candidates":[{"segment_id":"a","record_id":"r-a","text":"first","score":0.7,"explanation":"inner a"},{"segment_id":"b","record_id":"r-b","text":"second","score":0.8,"explanation":"inner b"}]` + tc.order + `}}`
			run, issues := devhost.BuildRetrievalRun([]byte(fixture), m.Manifest)
			if len(issues) > 0 {
				t.Fatal(issues)
			}
			out := run.Serve(plugins.CandidateRequest{Primitive: plugins.PrimitiveProfile, Profile: &plugins.ProfileCandidates{Name: "core.retrieve/default", Query: tc.query, Limit: 1}})
			score := 0.7
			if tc.want == "b" {
				score = 0.8
			}
			if len(out) != 1 || out[0].SegmentID != tc.want || out[0].Score != score || out[0].Explanation != "inner "+tc.want {
				t.Fatalf("served %+v; want %s score %g", out, tc.want, score)
			}
		})
	}
}

// The offline runner judges one profile's reports across rounds, independently
// of the engine's dependency-chain accounting.
func TestDriveSearchDecimalBudget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		final float64
		over  bool
	}{
		{"equal decimal allowance", 0.2, false},
		{"real excess", 0.2001, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := plugins.Validate([]byte(`id: example.rank
version: 1.0.0
compatibility: {engine: ">=0.1.0 <0.2.0", plugin_api: ">=0.12.0 <0.13.0"}
contributions:
  retrieval:
    profiles:
      default: {max_latency_ms: 100, max_cost_cents: 0.3}
`))
			if !report.Valid {
				t.Fatal(report.Errors)
			}
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req plugins.SearchRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				var answer any
				if req.Round == 1 {
					answer = map[string]any{"requests": []any{map[string]any{"primitive": "bm25", "query_text": "q", "k": 1}}, "usage": map[string]any{"cost_cents": 0.1}}
				} else {
					answer = map[string]any{"ranking": map[string]any{"hits": []any{}}, "usage": map[string]any{"cost_cents": tc.final}}
				}
				if err := json.NewEncoder(w).Encode(answer); err != nil {
					t.Error(err)
				}
			}))
			defer endpoint.Close()
			outcome, err := devhost.DriveSearch(context.Background(), endpoint.URL, report.Manifest, plugins.SearchRequest{Profile: "default", Query: plugins.SearchQuery{Text: "q", Mode: "lexical"}, Limit: 1}, func(plugins.CandidateRequest) []plugins.Candidate { return []plugins.Candidate{} })
			if err != nil {
				t.Fatal(err)
			}
			if tc.over {
				if len(outcome.Issues) != 1 || outcome.Issues[0].Code != plugins.CodeOverBudget {
					t.Fatalf("issues %+v", outcome.Issues)
				}
			} else if len(outcome.Issues) != 0 || outcome.Rounds != 2 {
				t.Fatalf("outcome %+v", outcome)
			}
		})
	}
}
