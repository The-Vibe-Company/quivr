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

// Several ingestion owners serve disjoint text segments in the same Corpus.
// Vector searches query every served space and merge repeated candidates.
func TestSearchesEveryServedSpaceAndDeduplicatesHits(t *testing.T) {
	req := &quivrplugin.SearchRequest{Round: 1, Limit: 10, Spaces: []quivrplugin.SearchSpace{{ID: "text@1", Role: "served"}, {ID: "pdf@1", Role: "served"}, {ID: "trial@1", Role: "evaluation"}}}
	req.Query.Text, req.Query.Mode = "harbour", "hybrid"
	answer, err := (retriever{}).Search(t.Context(), req)
	if err != nil || len(answer.Requests) != 2 || answer.Requests[0].Space != "text@1" || answer.Requests[1].Space != "pdf@1" {
		t.Fatalf("candidate requests = %+v (%v), want text and PDF served spaces", answer, err)
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
