package plugin

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func twoLegRequest(config string, round int, served ...quivrplugin.ServedRequest) *quivrplugin.SearchRequest {
	req := &quivrplugin.SearchRequest{Round: round, Limit: 3, Configuration: json.RawMessage(config), Served: served,
		Spaces: []quivrplugin.SearchSpace{{ID: "other@1", Role: "evaluation"}, {ID: "text@1", Role: "served"}}}
	req.Query.Text, req.Query.Mode = "harbour strike", "hybrid"
	return req
}

// Two-leg fusions ask for keywords and the served space's vectors separately, in one round.
func TestTwoLegAsksKeywordsAndVectors(t *testing.T) {
	for _, fusion := range []string{"dbsf", "min_max", "theoretical_min_max", "reciprocal_rank"} {
		answer, err := (retriever{}).Search(t.Context(), twoLegRequest(`{"hybrid_fusion":"`+fusion+`","candidate_count":40}`, 1))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := json.Marshal(answer.Requests)
		want := `[{"primitive":"bm25","query_text":"harbour strike","field":"source","k":40,"group_by":"record"},` +
			`{"primitive":"near_vector","query_text":"harbour strike","space":"text@1","k":40,"group_by":"record"}]`
		if string(got) != want {
			t.Errorf("%s asks %s, want %s", fusion, got, want)
		}
	}
}

func served(primitive string, scores map[string]float64) quivrplugin.ServedRequest {
	s := quivrplugin.ServedRequest{Round: 1, Request: quivrplugin.CandidateRequest{Primitive: primitive}}
	if primitive == quivrplugin.PrimitiveNearVector {
		s.Request.Space = "text@1"
	}
	for record, score := range scores {
		s.Candidates = append(s.Candidates, quivrplugin.Candidate{SegmentID: primitive + "-" + record, RecordID: record, Score: score})
	}
	return s
}

func rankedRecords(t *testing.T, config string, keywords, vectors map[string]float64) ([]string, []float64) {
	t.Helper()
	answer, err := (retriever{}).Search(t.Context(), twoLegRequest(config, 2,
		served(quivrplugin.PrimitiveBM25, keywords), served(quivrplugin.PrimitiveNearVector, vectors)))
	if err != nil {
		t.Fatal(err)
	}
	if len(answer.Requests) != 0 {
		t.Fatalf("round 2 asks again: %+v", answer.Requests)
	}
	ids, scores := []string{}, []float64{}
	for _, h := range answer.Ranking {
		ids = append(ids, h.SegmentID)
		scores = append(scores, h.Score)
	}
	return ids, scores
}

// Each Record counts once per leg, shows its vector passage when it has one,
// and the ranking is capped by the limit.
func TestTwoLegFusionRanks(t *testing.T) {
	keywords := map[string]float64{"a": 12, "b": 6, "c": 3}
	vectors := map[string]float64{"b": 0.80, "c": 0.78, "d": 0.60}
	for _, tc := range []struct {
		config string
		want   []string
		top    float64
	}{
		// min_max: a=0.5*1, b=0.5*(1/3)+0.5*1, c=0+0.5*0.9, d=0.
		{`{"hybrid_fusion":"min_max"}`, []string{"near_vector-b", "bm25-a", "near_vector-c"}, 2.0 / 3},
		// Keywords only: the vector-only Record falls behind every keyword hit.
		{`{"hybrid_fusion":"min_max","dense_weight":0}`, []string{"bm25-a", "near_vector-b", "near_vector-c"}, 1},
		// theoretical_min_max: keywords from 0 (a=1, b=0.5, c=0.25), cosine from -1 (b=1, c=0.989, d=0.889).
		{`{"hybrid_fusion":"theoretical_min_max"}`, []string{"near_vector-b", "near_vector-c", "bm25-a"}, 0.75},
		// reciprocal_rank k=1: rank r scores 2/(1+r); b=0.5*(2/3)+0.5*1.
		{`{"hybrid_fusion":"reciprocal_rank","rrf_k":1}`, []string{"near_vector-b", "near_vector-c", "bm25-a"}, 5.0 / 6},
		{`{"hybrid_fusion":"dbsf"}`, []string{"near_vector-b", "near_vector-c", "bm25-a"}, 0},
	} {
		ids, scores := rankedRecords(t, tc.config, keywords, vectors)
		if len(ids) != 3 || ids[0] != tc.want[0] || ids[1] != tc.want[1] || ids[2] != tc.want[2] {
			t.Errorf("%s ranks %v, want %v", tc.config, ids, tc.want)
		}
		if tc.top != 0 && math.Abs(scores[0]-tc.top) > 1e-9 {
			t.Errorf("%s top score %g, want %g", tc.config, scores[0], tc.top)
		}
	}
}

// DBSF maps mean - 3 sd to 0 and mean + 3 sd to 1, and a uniform list to 1.
func TestDBSFNormalizer(t *testing.T) {
	hits := map[string]legHit{"a": {score: 1}, "b": {score: 3}}
	norm := normalizer(configuration{HybridFusion: "dbsf"}, false, hits)
	// mean 2, sd 1: [-1, 5].
	if got := norm(legHit{score: 3}); math.Abs(got-4.0/6) > 1e-9 {
		t.Errorf("dbsf(3) = %g, want %g", got, 4.0/6)
	}
	if got := norm(legHit{score: 99}); got != 1 {
		t.Errorf("dbsf clamps to %g, want 1", got)
	}
	uniform := normalizer(configuration{HybridFusion: "dbsf"}, false, map[string]legHit{"a": {score: 2}, "b": {score: 2}})
	if got := uniform(legHit{score: 2}); got != 1 {
		t.Errorf("dbsf of a uniform list = %g, want 1", got)
	}
}

// Lexical and semantic searches, and the index-fused hybrid, keep one request.
func TestTwoLegOnlyForHybrid(t *testing.T) {
	for _, mode := range []string{"lexical", "semantic"} {
		req := twoLegRequest(`{"hybrid_fusion":"dbsf"}`, 1)
		req.Query.Mode = mode
		answer, err := (retriever{}).Search(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(answer.Requests) != 1 {
			t.Errorf("%s asks %d requests, want 1", mode, len(answer.Requests))
		}
	}
}
