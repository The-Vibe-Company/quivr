package plugin

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func TestRound1TwoLegHybrid(t *testing.T) {
	req := &quivrplugin.SearchRequest{
		Round: 1,
		Limit: 20,
		Spaces: []quivrplugin.SearchSpace{
			{ID: "hosted.embed.test@1", Role: "served", Indexes: []string{"text"}},
		},
	}
	req.Query.Text = "grève portuaire"
	req.Query.Mode = "hybrid"

	ans, err := New().Search(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Requests) != 2 {
		t.Fatalf("got %d requests, want 2 (bm25 + ungrouped near_vector)", len(ans.Requests))
	}
	if ans.Requests[0].Primitive != quivrplugin.PrimitiveBM25 || ans.Requests[0].GroupBy != "record" || ans.Requests[0].K != 60 {
		t.Fatalf("unexpected bm25 request: %+v", ans.Requests[0])
	}
	if ans.Requests[1].Primitive != quivrplugin.PrimitiveNearVector || ans.Requests[1].GroupBy != "" || ans.Requests[1].K != 100 {
		t.Fatalf("unexpected near_vector request: %+v", ans.Requests[1])
	}
}

func TestNormalizationEdgeCases(t *testing.T) {
	cfg := defaultConfig(10)
	cfg.Fusion = "dbsf"
	got := normalizeLeg(map[string]float64{"r1": 4.2}, cfg)
	if math.Abs(got["r1"]-0.5) > 1e-9 {
		t.Fatalf("singleton DBSF = %v, want 0.5", got["r1"])
	}
	got = normalizeLeg(map[string]float64{"lo": 1.0, "hi": 3.0}, cfg)
	if math.Abs(got["hi"]-(0.5+1.0/6.0)) > 1e-9 {
		t.Fatalf("hi DBSF = %v, want %v", got["hi"], 0.5+1.0/6.0)
	}

	cfg.Fusion = "convex"
	got = normalizeLeg(map[string]float64{"lo": 0.6, "hi": 0.9}, cfg)
	if math.Abs(got["lo"]) > 1e-9 || math.Abs(got["hi"]-1.0) > 1e-9 {
		t.Fatalf("convex min-max = %+v, want lo=0 hi=1", got)
	}
}

func TestTitleLeadAndConcentrationBeatPassingMention(t *testing.T) {
	req := &quivrplugin.SearchRequest{
		Round:         2,
		Limit:         5,
		Configuration: json.RawMessage(`{}`),
		Served: []quivrplugin.ServedRequest{
			{
				Round:   1,
				Request: quivrplugin.CandidateRequest{Primitive: quivrplugin.PrimitiveBM25, GroupBy: "record", K: 10},
				Candidates: []quivrplugin.Candidate{
					{SegmentID: "s_pm_lex", RecordID: "rec_passing", PartKey: "title", Text: "Bilan trimestriel du groupe", PassageText: "Bilan trimestriel du groupe\n\nLe chiffre d'affaires progresse en Europe.", Score: 10.0},
					{SegmentID: "s_main_lex", RecordID: "rec_central", PartKey: "title", Text: "Grève générale au port de Marseille", PassageText: "Grève générale au port de Marseille\n\nLes dockers bloquent les terminaux depuis l'aube.", Score: 9.5},
					{SegmentID: "s_bg_lex", RecordID: "rec_bg", PartKey: "title", Text: "Météo du weekend", PassageText: "Météo du weekend\n\nTemps calme sur la côte.", Score: 2.0},
				},
			},
			{
				Round:   1,
				Request: quivrplugin.CandidateRequest{Primitive: quivrplugin.PrimitiveNearVector, K: 10},
				Candidates: []quivrplugin.Candidate{
					{SegmentID: "s_pm_deep", RecordID: "rec_passing", PartKey: "body", Start: 2400, End: 3200, Text: "Un paragraphe isolé évoque la grève du port.", Score: 0.86},
					{SegmentID: "s_main_0", RecordID: "rec_central", PartKey: "body", Start: 0, End: 1600, Text: "Les dockers bloquent le port de Marseille lors d'une grève générale.", Score: 0.85},
					{SegmentID: "s_main_1", RecordID: "rec_central", PartKey: "body", Start: 1600, End: 3000, Text: "Les syndicats portuaires prolongent la grève.", Score: 0.82},
					{SegmentID: "s_bg_0", RecordID: "rec_bg", PartKey: "body", Start: 0, End: 400, Text: "Temps calme sur la côte.", Score: 0.40},
				},
			},
		},
	}
	req.Query.Text = "grève port Marseille"
	req.Query.Mode = "hybrid"

	ans, err := New().Search(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Ranking) != 3 {
		t.Fatalf("unexpected ranking: %+v", ans.Ranking)
	}
	if ans.Ranking[0].SegmentID != "s_main_0" {
		t.Fatalf("central dispatch ranked %q first instead of s_main_0 (scores: %+v)", ans.Ranking[0].SegmentID, ans.Ranking)
	}
}

func TestFrenchElisionAndAccentFolding(t *testing.T) {
	terms := contentTerms("L'économie d'énergie : grèves portuaires à l'œuvre")
	got := map[string]bool{}
	for _, term := range terms {
		got[term] = true
	}
	for _, want := range []string{"economie", "energie", "greve", "portuaire", "oeuvre"} {
		if !got[want] {
			t.Errorf("missing folded term %q in %v", want, terms)
		}
	}
	for _, unwanted := range []string{"l", "d", "a"} {
		if got[unwanted] {
			t.Errorf("stopword/elision prefix %q should be excluded from %v", unwanted, terms)
		}
	}
}
