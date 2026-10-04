package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// The candidate request is what makes the ranking the engine's former one:
// the Runner's fixtures see the primitive, not the field, weight, fusion or k
// the index query receives.
func TestAsksForTheEnginesFormerQuery(t *testing.T) {
	for mode, want := range map[string]string{
		"lexical":  `{"primitive":"bm25","query_text":"grève du port","field":"source","k":7}`,
		"semantic": `{"primitive":"near_vector","query_text":"grève du port","space":"core.ingest.e5-small@1","k":7}`,
		"hybrid":   `{"primitive":"hybrid","query_text":"grève du port","space":"core.ingest.e5-small@1","field":"source","alpha":0.5,"fusion":"relative_score","k":7}`,
	} {
		req := &quivrplugin.SearchRequest{Round: 1, Limit: 7, Spaces: []quivrplugin.SearchSpace{{ID: "other@1", Role: "evaluation"}, {ID: "core.ingest.e5-small@1", Role: "served"}}}
		req.Query.Text, req.Query.Mode = "grève du port", mode
		answer, err := retriever{}.Search(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := json.Marshal(answer.Requests)
		if string(got) != "["+want+"]" {
			t.Errorf("%s asks %s, want [%s]", mode, got, want)
		}
	}
}

// Configuration changes the real candidate request, including the two
// endpoints of the dense weight. The caller's result limit is independent.
func TestConfiguredCandidateRequests(t *testing.T) {
	for _, tc := range []struct {
		mode, config, want string
	}{
		{"lexical", `{"candidate_count":100}`, `{"primitive":"bm25","query_text":"harbour","field":"source","k":100}`},
		{"semantic", `{"candidate_count":1}`, `{"primitive":"near_vector","query_text":"harbour","space":"text@1","k":1}`},
		{"hybrid", `{"dense_weight":0}`, `{"primitive":"hybrid","query_text":"harbour","space":"text@1","field":"source","alpha":0,"fusion":"relative_score","k":7}`},
		{"hybrid", `{"dense_weight":1,"candidate_count":100,"hybrid_fusion":"ranked"}`, `{"primitive":"hybrid","query_text":"harbour","space":"text@1","field":"source","alpha":1,"fusion":"ranked","k":100}`},
		{"hybrid", `{"dense_weight":0.7,"candidate_count":30,"hybrid_fusion":"relative_score"}`, `{"primitive":"hybrid","query_text":"harbour","space":"text@1","field":"source","alpha":0.7,"fusion":"relative_score","k":30}`},
	} {
		req := &quivrplugin.SearchRequest{Round: 1, Limit: 7, Configuration: json.RawMessage(tc.config), Spaces: []quivrplugin.SearchSpace{{ID: "text@1", Role: "served"}}}
		req.Query.Text, req.Query.Mode = "harbour", tc.mode
		answer, err := (retriever{}).Search(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := json.Marshal(answer.Requests)
		if string(got) != "["+tc.want+"]" {
			t.Errorf("%s with %s asks %s, want [%s]", tc.mode, tc.config, got, tc.want)
		}
	}
}

// The manifest owns installer validation, which the SDK also enforces on
// each HTTP invocation. These failures must be configuration refusals.
func TestConfigurationAtHTTPBoundary(t *testing.T) {
	p, err := quivrplugin.New("quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Retrieval(retriever{}); err != nil {
		t.Fatal(err)
	}
	handler, err := p.Handler()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../../contracts/plugins/v0/fixtures/requests/retrieval/search-round-1.json")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err = json.Unmarshal(fixture, &body); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		config string
		valid  bool
	}{
		{`{}`, true},
		{`{"dense_weight":0,"candidate_count":1,"hybrid_fusion":"relative_score"}`, true},
		{`{"dense_weight":1,"candidate_count":100,"hybrid_fusion":"ranked"}`, true},
		{`{"dense_weight":-0.01}`, false}, {`{"dense_weight":1.01}`, false},
		{`{"dense_weight":"0.5"}`, false}, {`{"dense_weight":null}`, false},
		{`{"candidate_count":0}`, false}, {`{"candidate_count":101}`, false},
		{`{"candidate_count":1.5}`, false}, {`{"candidate_count":"30"}`, false},
		{`{"candidate_count":null}`, false}, {`{"hybrid_fusion":"rrf"}`, false},
		{`{"hybrid_fusion":null}`, false}, {`{"alpha":0.5}`, false},
	} {
		body["configuration"] = json.RawMessage(tc.config)
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v0/contributions/retrieval/search", bytes.NewReader(raw)))
		want := http.StatusBadRequest
		if tc.valid {
			want = http.StatusOK
		}
		if rec.Code != want || (!tc.valid && !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"invalid_configuration"`))) {
			t.Errorf("configuration %s: %d %s, want status %d and configuration validation", tc.config, rec.Code, rec.Body, want)
		}
	}
}

// Without a served space only a keyword search can be answered; the others
// are refused for good (422 unsupported_search), as the engine refused them.
func TestRefusesVectorSearchWithoutAServedSpace(t *testing.T) {
	for _, mode := range []string{"semantic", "hybrid"} {
		req := &quivrplugin.SearchRequest{Round: 1, Limit: 10, Spaces: []quivrplugin.SearchSpace{}}
		req.Query.Text, req.Query.Mode = "grève", mode
		var e *quivrplugin.SearchError
		if _, err := (retriever{}).Search(t.Context(), req); !errors.As(err, &e) || e.Retryable {
			t.Errorf("%s without a served space: %v, want a terminal refusal", mode, err)
		}
	}
}

// The ranking is the served order, except that candidates of equal score,
// which the index returns in the order their objects were written, rank by
// segment id: the same search over the same Records ranks the same way.
func TestRanksTheServedOrderWithEqualScoresBySegment(t *testing.T) {
	req := &quivrplugin.SearchRequest{Round: 2, Limit: 4}
	served := quivrplugin.ServedRequest{Request: quivrplugin.CandidateRequest{Primitive: quivrplugin.PrimitiveBM25}}
	for _, c := range []struct {
		id    string
		score float64
	}{{"seg_d", 3}, {"seg_c", 1.5}, {"seg_a", 1.5}, {"seg_b", 0.5}, {"seg_e", 0.5}} {
		served.Candidates = append(served.Candidates, quivrplugin.Candidate{SegmentID: c.id, Score: c.score})
	}
	req.Served = []quivrplugin.ServedRequest{served}
	answer, err := retriever{}.Search(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range answer.Ranking {
		got = append(got, h.SegmentID)
	}
	if fmt.Sprint(got) != "[seg_d seg_a seg_c seg_b]" {
		t.Fatalf("ranking %v, want [seg_d seg_a seg_c seg_b]: served order, equal scores by segment id, cut to the limit", got)
	}
}

// Several ingestion owners serve disjoint text segments in the same Corpus.
// Vector searches query every served space and merge repeated candidates.
func TestSearchesEveryServedSpaceAndDeduplicatesHits(t *testing.T) {
	req := &quivrplugin.SearchRequest{Round: 1, Limit: 10, Spaces: []quivrplugin.SearchSpace{{ID: "text@1", Role: "served"}, {ID: "pdf@1", Role: "served"}, {ID: "trial@1", Role: "evaluation"}}}
	req.Query.Text, req.Query.Mode = "harbour", "hybrid"
	req.Configuration = json.RawMessage(`{"dense_weight":0.7,"candidate_count":30,"hybrid_fusion":"ranked"}`)
	answer, err := (retriever{}).Search(t.Context(), req)
	if err != nil || len(answer.Requests) != 2 || answer.Requests[0].Space != "text@1" || answer.Requests[1].Space != "pdf@1" {
		t.Fatalf("candidate requests = %+v (%v), want text and PDF served spaces", answer, err)
	}
	for _, c := range answer.Requests {
		if c.K != 30 || c.Alpha == nil || *c.Alpha != .7 || c.Fusion != "ranked" {
			t.Fatalf("space %s request = %+v, want configured depth, weight and fusion", c.Space, c)
		}
	}
	req.Round = 2
	req.Served = []quivrplugin.ServedRequest{
		{Request: answer.Requests[0], Candidates: []quivrplugin.Candidate{{SegmentID: "text", Score: .9}, {SegmentID: "pdf", Score: .1}}},
		{Request: answer.Requests[1], Candidates: []quivrplugin.Candidate{{SegmentID: "pdf", Score: .8}, {SegmentID: "text", Score: .2}}},
	}
	answer, err = (retriever{}).Search(t.Context(), req)
	if err != nil || len(answer.Ranking) != 2 || answer.Ranking[0].SegmentID != "text" || answer.Ranking[1].SegmentID != "pdf" || answer.Ranking[1].Score != .8 {
		t.Fatalf("ranking = %+v (%v), want each segment once with its best score", answer, err)
	}
	if answer.Ranking[1].Explanation != "keywords and vectors in pdf@1, alpha 0.7, ranked fusion (RRF)" {
		t.Fatalf("explanation = %q, want configured weight and fusion", answer.Ranking[1].Explanation)
	}
}

// The full search-protocol space boundary takes two candidate rounds and
// one ranking round, without dropping an owner or exceeding eight requests.
func TestSearchesTheFullSpaceBoundary(t *testing.T) {
	req := &quivrplugin.SearchRequest{Round: 1, Limit: 50}
	req.Query.Text, req.Query.Mode = "harbour", "semantic"
	for i := range 16 {
		req.Spaces = append(req.Spaces, quivrplugin.SearchSpace{ID: fmt.Sprintf("space%d@1", i), Role: "served"})
	}
	seen := map[string]bool{}
	for round := 1; round <= 2; round++ {
		req.Round = round
		answer, err := (retriever{}).Search(t.Context(), req)
		if err != nil || len(answer.Requests) != 8 {
			t.Fatalf("round %d: %+v (%v), want eight requests", round, answer, err)
		}
		for _, request := range answer.Requests {
			if seen[request.Space] {
				t.Fatalf("space %s requested twice", request.Space)
			}
			seen[request.Space] = true
			req.Served = append(req.Served, quivrplugin.ServedRequest{Request: request, Candidates: []quivrplugin.Candidate{{SegmentID: request.Space, Score: 1}}})
		}
	}
	req.Round = 3
	answer, err := (retriever{}).Search(t.Context(), req)
	if err != nil || len(answer.Requests) != 0 || len(answer.Ranking) != 16 || len(seen) != 16 {
		t.Fatalf("final ranking: %+v (%v), want all 16 owners", answer, err)
	}
}
