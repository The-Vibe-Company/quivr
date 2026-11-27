package plugin

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func searchRequest(mode, config string) *quivrplugin.SearchRequest {
	req := &quivrplugin.SearchRequest{Round: 1, Limit: 3, Configuration: json.RawMessage(config), Spaces: []quivrplugin.SearchSpace{{ID: "text@1", Role: "served", Indexes: []string{"text"}}}}
	req.Query.Text, req.Query.Mode = "harbour strike", mode
	return req
}

// Passage aggregation adds one ungrouped near_vector request in the served
// space; keyword-only searches and the default configuration ask nothing more.
func TestPassageRequest(t *testing.T) {
	for _, tc := range []struct {
		mode, config, want string
	}{
		{"hybrid", `{"passage_weight":0.4}`, `[{"primitive":"hybrid","query_text":"harbour strike","space":"text@1","field":"source","alpha":0.5,"fusion":"relative_score","k":3,"group_by":"record"},{"primitive":"near_vector","query_text":"harbour strike","space":"text@1","k":100}]`},
		{"semantic", `{"passage_weight":0.4,"passage_candidates":20}`, `[{"primitive":"near_vector","query_text":"harbour strike","space":"text@1","k":3,"group_by":"record"},{"primitive":"near_vector","query_text":"harbour strike","space":"text@1","k":20}]`},
		{"lexical", `{"passage_weight":0.4}`, `[{"primitive":"bm25","query_text":"harbour strike","field":"source","k":3,"group_by":"record"}]`},
		{"hybrid", `{}`, `[{"primitive":"hybrid","query_text":"harbour strike","space":"text@1","field":"source","alpha":0.5,"fusion":"relative_score","k":3,"group_by":"record"}]`},
	} {
		answer, err := (retriever{}).Search(t.Context(), searchRequest(tc.mode, tc.config))
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := json.Marshal(answer.Requests); string(got) != tc.want {
			t.Errorf("%s with %s asks %s, want %s", tc.mode, tc.config, got, tc.want)
		}
	}
}

// A Record whose one passage mentions the query in passing ranks first by
// the first round, but a Record matching in its lead and a second passage
// overtakes it once passage evidence is blended in.
func TestPassageAggregationDemotesPassingMention(t *testing.T) {
	served := func(config string) []quivrplugin.RankedHit {
		req := searchRequest("hybrid", config)
		req.Round = 2
		hybrid := quivrplugin.CandidateRequest{Primitive: quivrplugin.PrimitiveHybrid, Space: "text@1", GroupBy: "record"}
		passages := quivrplugin.CandidateRequest{Primitive: quivrplugin.PrimitiveNearVector, Space: "text@1"}
		req.Served = []quivrplugin.ServedRequest{
			{Round: 1, Request: hybrid, Candidates: []quivrplugin.Candidate{
				{SegmentID: "seg_passing_3", RecordID: "rec_passing", Start: 3000, Score: 1.0},
				{SegmentID: "seg_story_0", RecordID: "rec_story", Start: 0, Score: 0.9},
				{SegmentID: "seg_other_0", RecordID: "rec_other", Start: 0, Score: 0.2},
			}},
			{Round: 1, RequestIndex: 1, Request: passages, Candidates: []quivrplugin.Candidate{
				{SegmentID: "seg_passing_3", RecordID: "rec_passing", Start: 3000, Score: 0.80},
				{SegmentID: "seg_story_0", RecordID: "rec_story", Start: 0, Score: 0.78},
				{SegmentID: "seg_story_1", RecordID: "rec_story", Start: 900, Score: 0.74},
				{SegmentID: "seg_other_0", RecordID: "rec_other", Start: 0, Score: 0.50},
			}},
		}
		answer, err := (retriever{}).Search(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		return answer.Ranking
	}
	if got := served(`{}`); got[0].SegmentID != "seg_passing_3" {
		t.Fatalf("default ranking starts with %s, want the first-round order", got[0].SegmentID)
	}
	got := served(`{"passage_weight":0.4,"passage_top2_weight":1,"passage_lead_weight":2}`)
	if len(got) != 3 || got[0].SegmentID != "seg_story_0" || got[1].SegmentID != "seg_passing_3" {
		t.Fatalf("aggregated ranking %+v, want the lead story first", got)
	}
}

// MaxP, Top2Mean and FirstP use passage scores normalized over the pool; a
// second passage not served counts as the pool's lowest score.
func TestPassageScores(t *testing.T) {
	served := []quivrplugin.ServedRequest{{Request: quivrplugin.CandidateRequest{Primitive: quivrplugin.PrimitiveNearVector}, Candidates: []quivrplugin.Candidate{
		{SegmentID: "a0", RecordID: "a", Start: 0, Score: 0.9},
		{SegmentID: "a1", RecordID: "a", Start: 500, Score: 0.7},
		{SegmentID: "b1", RecordID: "b", Start: 500, Score: 0.9},
		{SegmentID: "c0", RecordID: "c", Start: 0, Score: 0.5},
		{SegmentID: "a0", RecordID: "a", Start: 0, Score: 0.9},
	}}}
	for _, tc := range []struct {
		config  configuration
		a, b, c float64
	}{
		{configuration{PassageMaxWeight: 1}, 1, 1, 0},
		{configuration{PassageTop2Weight: 1}, 0.75, 0.5, 0},
		{configuration{PassageLeadWeight: 1}, 1, 0, 0},
		{configuration{PassageMaxWeight: 1, PassageTop2Weight: 1, PassageLeadWeight: 2}, 0.9375, 0.375, 0},
		{configuration{}, 0, 0, 0},
	} {
		got := passageScores(served, tc.config)
		for id, want := range map[string]float64{"a": tc.a, "b": tc.b, "c": tc.c} {
			if math.Abs(got[id]-want) > 1e-9 {
				t.Errorf("%+v: %s scores %g, want %g", tc.config, id, got[id], want)
			}
		}
	}
}
