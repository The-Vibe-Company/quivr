package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/sdks/go/quivrplugin"
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
