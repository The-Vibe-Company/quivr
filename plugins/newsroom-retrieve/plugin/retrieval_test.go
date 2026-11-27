package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func TestExtractEntityQuery(t *testing.T) {
	q := "Crise budgétaire en France\nQuelles mesures fiscales le ministre Eric Lombard a-t-il annoncées pour le budget 2025 selon l'UE ?"
	sub, ok := extractEntityQuery(q, 6)
	if !ok {
		t.Fatalf("expected focused sub-query to differ from full query")
	}
	lower := strings.ToLower(sub)
	for _, want := range []string{"2025", "ue", "lombard"} {
		if !strings.Contains(lower, want) {
			t.Errorf("expected focused entity sub-query %q to contain %q", sub, want)
		}
	}
	if strings.Contains(lower, "quelles") {
		t.Errorf("expected stopword 'quelles' to be stripped from %q", sub)
	}
}

func TestProximityAndAspectCoverage(t *testing.T) {
	aspects := queryAspects("accord commercial agricole Union européenne")
	tight := tokenizeContent("Un accord commercial agricole majeur a été signé par l'Union européenne mardi.")
	scattered := tokenizeContent("Un accord a été évoqué. Plusieurs paragraphes sur le sport et la météo puis un mot sur le commerce et ailleurs l'agriculture loin de l'Union.")
	missing := tokenizeContent("Le gouvernement présente son budget annuel au Parlement européen.")

	sets := []map[string]bool{tokenSetOf(strings.Join(tight, " ")), tokenSetOf(strings.Join(scattered, " ")), tokenSetOf(strings.Join(missing, " "))}
	idfs := computePoolIDF(aspects, sets)

	covTight := scoreAspectCoverage(aspects, idfs, sets[0])
	covMissing := scoreAspectCoverage(aspects, idfs, sets[2])
	if covTight <= covMissing {
		t.Fatalf("expected tight doc coverage (%.3f) > missing-aspect doc (%.3f)", covTight, covMissing)
	}

	proxTight := scoreProximity(aspects, idfs, tight, 25)
	proxScattered := scoreProximity(aspects, idfs, scattered, 10)
	if proxTight <= proxScattered {
		t.Fatalf("expected tight proximity (%.3f) > scattered proximity (%.3f)", proxTight, proxScattered)
	}
}

func TestFirstPMaxPRatioDoesNotPenalizeSinglePassage(t *testing.T) {
	req := &quivrplugin.SearchRequest{
		Profile: "default",
		Round:   2,
		Limit:   5,
	}
	req.Query.Text = "grève portuaire"
	req.Query.Mode = "hybrid"

	cfg, err := readConfig(json.RawMessage(`{"dense_weight":0.7,"lexical_weight":0.3,"passage_ratio_weight":0.3}`))
	if err != nil {
		t.Fatal(err)
	}

	// Single-passage article (Start=0) with dense=0.80 vs deep passing mention (Start=2500) with dense=0.82 whose lead did not match.
	denseGrouped := []quivrplugin.Candidate{
		{SegmentID: "deep_seg", RecordID: "rec_deep", PartKey: "body", Start: 2500, End: 4000, Text: "Mention rapide d'une grève portuaire en fin d'article.", Score: 0.82},
		{SegmentID: "lead_seg", RecordID: "rec_lead", PartKey: "body", Start: 0, End: 800, Text: "Une grève portuaire paralyse le trafic maritime dès l'aube.", Score: 0.80},
		{SegmentID: "floor_seg", RecordID: "rec_other", PartKey: "body", Start: 0, End: 500, Text: "Autre dépêche.", Score: 0.40},
	}
	denseUngrouped := []quivrplugin.Candidate{
		{SegmentID: "deep_seg", RecordID: "rec_deep", PartKey: "body", Start: 2500, End: 4000, Text: "Mention rapide d'une grève portuaire en fin d'article.", Score: 0.82},
		{SegmentID: "lead_seg", RecordID: "rec_lead", PartKey: "body", Start: 0, End: 800, Text: "Une grève portuaire paralyse le trafic maritime dès l'aube.", Score: 0.80},
		{SegmentID: "floor_seg", RecordID: "rec_other", PartKey: "body", Start: 0, End: 500, Text: "Autre dépêche.", Score: 0.40},
	}

	bundles := fuseAndScore(req, cfg, denseGrouped, nil, nil, denseUngrouped)
	if len(bundles) < 2 {
		t.Fatalf("expected >= 2 bundles, got %d", len(bundles))
	}
	if bundles[0].RecordID != "rec_lead" {
		t.Fatalf("expected single-passage lead doc to outrank deep passing mention; got %s (%.4f) over %s (%.4f)",
			bundles[0].RecordID, bundles[0].FinalScore, bundles[1].RecordID, bundles[1].FinalScore)
	}
}

func TestRound1AsksThreeLegsWhenEntityEnabled(t *testing.T) {
	r := New()
	req := &quivrplugin.SearchRequest{
		Profile:       "default",
		Round:         1,
		Limit:         10,
		Configuration: json.RawMessage(`{"entity_leg":true,"passage_ratio_weight":0.2}`),
		Spaces:        []quivrplugin.SearchSpace{{ID: "sp1", Role: "served"}},
	}
	req.Query.Text = "Budget 2025\nQuelles annonces du ministre Eric Lombard ?"
	req.Query.Mode = "hybrid"

	ans, err := r.Search(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Requests) != 4 {
		t.Fatalf("expected 4 Round-1 requests (dense grouped, bm25 full, bm25 entity, dense ungrouped), got %d", len(ans.Requests))
	}
}

func TestSDMSpanAndExpSalience(t *testing.T) {
	q := "accord commercial agricole"
	aspects := queryAspects(q)
	qseq := tokenizeContent(q)
	tight := tokenizeContent("Un accord commercial agricole majeur est signé.")
	loose := tokenizeContent("Un accord est discuté sur le climat le transport le sport le budget et enfin le secteur commercial puis agricole.")

	if scoreSDMSpanProximity(qseq, aspects, tight) <= scoreSDMSpanProximity(qseq, aspects, loose) {
		t.Fatalf("expected tight SDM span > loose SDM span")
	}

	leadSeg := quivrplugin.Candidate{SegmentID: "s1", RecordID: "r1", PartKey: "body", Start: 0, Text: "Un accord commercial agricole majeur est signé aujourd'hui."}
	deepSeg := quivrplugin.Candidate{SegmentID: "s2", RecordID: "r2", PartKey: "body", Start: 1200, Text: "En marge de la réunion, un accord commercial agricole a été évoqué."}
	idfs := []float64{1.0, 1.5, 2.0}
	bLead := &recordBundle{RecordID: "r1", BestSeg: leadSeg, HasBestSeg: true, LeadSeg: &leadSeg, PassageCount: 1}
	bDeep := &recordBundle{RecordID: "r2", BestSeg: deepSeg, HasBestSeg: true, DeepBestSeg: &deepSeg, PassageCount: 1}

	salLead, tailLead := scoreExpSalience(aspects, idfs, bLead)
	salDeep, tailDeep := scoreExpSalience(aspects, idfs, bDeep)
	if salLead <= salDeep {
		t.Fatalf("expected lead exp salience (%.3f) > deep (%.3f)", salLead, salDeep)
	}
	if tailLead || !tailDeep {
		t.Fatalf("expected tailOnly=(false, true), got (%v, %v)", tailLead, tailDeep)
	}
}

func TestBuildInvertedPyramidSurrogate(t *testing.T) {
	lead := quivrplugin.Candidate{SegmentID: "s0", RecordID: "r1", PartKey: "body", Start: 0, Text: "Le gouvernement annonce un plan budgétaire pour 2025."}
	deep := quivrplugin.Candidate{SegmentID: "s2", RecordID: "r1", PartKey: "body", Start: 2400, Text: "En fin de discours, une mesure fiscale est mentionnée."}

	bPair := &recordBundle{RecordID: "r1", BestSeg: deep, HasBestSeg: true, LeadSeg: &lead, DeepBestSeg: &deep}
	sur := buildSurrogate(bPair, "inverted_pyramid", 2000)
	if !strings.Contains(sur, "[LEAD]:") || !strings.Contains(sur, "[MATCHED DEEP PASSAGE]:") {
		t.Fatalf("expected paired lead+deep surrogate, got %q", sur)
	}

	bDeepOnly := &recordBundle{RecordID: "r2", BestSeg: deep, HasBestSeg: true, DeepBestSeg: &deep}
	surDeep := buildSurrogate(bDeepOnly, "inverted_pyramid", 2000)
	if !strings.Contains(surDeep, "[DEEP BODY EXCERPT") {
		t.Fatalf("expected deep-only marker when lead absent, got %q", surDeep)
	}
}


