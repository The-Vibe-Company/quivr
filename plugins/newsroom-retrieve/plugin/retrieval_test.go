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

	bundles := fuseAndScore(req, cfg, []denseViewLeg{{Name: "full", Weight: 1.0, Cands: denseGrouped}}, nil, nil, nil, denseUngrouped)
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

func TestMultiQueryDecomposition(t *testing.T) {
	q := "Crise budgétaire en France — Quelles mesures fiscales le ministre Eric Lombard a-t-il annoncées pour le budget 2025 ?"
	subj, ok := extractCoreQuery(q, "subject")
	if !ok || subj != "Crise budgétaire en France" {
		t.Fatalf("expected subject clause, got (%q, %v)", subj, ok)
	}
	quest, ok := extractCoreQuery(q, "question")
	if !ok || !strings.Contains(quest, "mesures fiscales") || strings.HasPrefix(strings.ToLower(quest), "quelles") {
		t.Fatalf("expected boilerplate-stripped question clause, got (%q, %v)", quest, ok)
	}
	stripped, ok := extractCoreQuery(q, "stripped")
	if !ok || strings.Contains(stripped, "—") || strings.Contains(strings.ToLower(stripped), "quelles") {
		t.Fatalf("expected stripped combined query, got (%q, %v)", stripped, ok)
	}
	a, b, ok := extractClausePair(q)
	if !ok || a == "" || b == "" || a == b {
		t.Fatalf("expected distinct clause pair, got (%q, %q, %v)", a, b, ok)
	}
	entQ, ok := extractDenseEntityQuery(q, 8)
	if !ok || !strings.Contains(entQ, "2025") || !strings.Contains(entQ, "Lombard") {
		t.Fatalf("expected dense entity query with 2025 & Lombard, got (%q, %v)", entQ, ok)
	}
}

func TestCombineDenseViewsConjunction(t *testing.T) {
	views := []denseViewLeg{
		{Name: "full", Weight: 1.0, Cands: []quivrplugin.Candidate{
			{RecordID: "both", Score: 0.90},
			{RecordID: "one_only", Score: 0.95},
			{RecordID: "floor", Score: 0.20},
		}},
		{Name: "clause_b", Weight: 1.0, Cands: []quivrplugin.Candidate{
			{RecordID: "both", Score: 0.88},
			{RecordID: "floor", Score: 0.20},
		}},
	}
	stats := []legStats{computeLegStats(views[0].Cands), computeLegStats(views[1].Cands)}
	bBoth := &recordBundle{RecordID: "both", DenseViewScores: []float64{0.90, 0.88}, DenseViewRanks: []int{2, 1}, HasDenseView: []bool{true, true}}
	bOne := &recordBundle{RecordID: "one_only", DenseViewScores: []float64{0.95, 0}, DenseViewRanks: []int{1, 0}, HasDenseView: []bool{true, false}}

	cfgSoftMin := configuration{Fusion: "convex", DenseViewFusion: "soft_min", DenseSoftMinLambda: 0.40}
	sBoth, _ := combineDenseViews(bBoth, views, stats, cfgSoftMin)
	sOne, _ := combineDenseViews(bOne, views, stats, cfgSoftMin)
	if sBoth <= sOne {
		t.Fatalf("soft_min should rank dual-view doc (%.3f) above single-view doc (%.3f)", sBoth, sOne)
	}

	cfgGeom := configuration{Fusion: "convex", DenseViewFusion: "geom"}
	gBoth, _ := combineDenseViews(bBoth, views, stats, cfgGeom)
	gOne, _ := combineDenseViews(bOne, views, stats, cfgGeom)
	if gBoth <= gOne {
		t.Fatalf("geom should rank dual-view doc (%.3f) above single-view doc (%.3f)", gBoth, gOne)
	}
}

func TestRound2ConsensusQuery(t *testing.T) {
	q := "Accord commercial agricole — Quelles réactions des syndicats européens ?"
	cfg := configuration{Round2ConsensusTopN: 10, Round2ConsensusDocs: 2, Round2MaxTerms: 3, Round2QueryBase: "core"}
	c1 := quivrplugin.Candidate{RecordID: "r1", PartKey: "body", Start: 0, Text: "BRUXELLES - L'accord commercial Mercosur suscite la colère des syndicats agricoles européens et de la Commission européenne."}
	c2 := quivrplugin.Candidate{RecordID: "r2", PartKey: "body", Start: 0, Text: "PARIS - Face à l'accord commercial Mercosur, les syndicats agricoles dénoncent la position de la Commission européenne."}
	r2Q, terms, ok := buildRound2ConsensusQuery(q, cfg, []quivrplugin.Candidate{c1, c2}, []quivrplugin.Candidate{c2, c1})
	if !ok || len(terms) == 0 {
		t.Fatalf("expected consensus expansion terms, got (%q, %v, %v)", r2Q, terms, ok)
	}
	if !strings.Contains(strings.ToLower(r2Q), "mercosur") {
		t.Fatalf("expected shared entity Mercosur in Round-2 query %q (terms=%v)", r2Q, terms)
	}
	// Single consensus doc should NOT trigger Round-2 expansion.
	_, _, okSingle := buildRound2ConsensusQuery(q, cfg, []quivrplugin.Candidate{c1}, []quivrplugin.Candidate{c1})
	if okSingle {
		t.Fatalf("expected Round-2 expansion to skip when fewer than 2 consensus docs exist")
	}
}

func TestTemporalAlignmentAndDatelineEntity(t *testing.T) {
	qYears, qNums := extractYearsAndNumbers("Sommet climatique COP29 en 2024 à Bakou")
	if len(qYears) != 1 || qYears[0] != "2024" {
		t.Fatalf("expected year 2024, got %v", qYears)
	}
	if len(qNums) == 0 {
		t.Fatalf("expected numeric token cop29, got %v", qNums)
	}
	segMatch := quivrplugin.Candidate{SegmentID: "s1", RecordID: "r1", PartKey: "body", Start: 0, Text: "BAKOU (Wire) - La conférence COP29 s'est ouverte en novembre 2024 sur le financement climatique."}
	segOld := quivrplugin.Candidate{SegmentID: "s2", RecordID: "r2", PartKey: "body", Start: 0, Text: "DUBAÏ (Wire) - La conférence climatique de décembre 2023 s'était conclue sur un texte de transition."}
	bMatch := &recordBundle{RecordID: "r1", BestSeg: segMatch, HasBestSeg: true, LeadSeg: &segMatch}
	bOld := &recordBundle{RecordID: "r2", BestSeg: segOld, HasBestSeg: true, LeadSeg: &segOld}

	a1, c1 := scoreTemporalAlignment(qYears, qNums, bMatch, 300)
	a2, c2 := scoreTemporalAlignment(qYears, qNums, bOld, 300)
	if a1 <= a2 || c1 != 0 || c2 <= 0 {
		t.Fatalf("expected matching doc align=%.2f>%.2f and old doc conflict=%.2f>0 (match conflict=%.2f)", a1, a2, c2, c1)
	}

	qEnts := extractQueryEntityStems("Sommet climatique COP29 en 2024 à Bakou")
	d1 := scoreDatelineEntityBoost(qEnts, bMatch)
	d2 := scoreDatelineEntityBoost(qEnts, bOld)
	if d1 <= d2 {
		t.Fatalf("expected dateline entity boost (%.2f) > non-matching dateline (%.2f)", d1, d2)
	}
}

func TestRound1MultiViewDenseFanOut(t *testing.T) {
	r := New()
	req := &quivrplugin.SearchRequest{
		Profile:       "default",
		Round:         1,
		Limit:         10,
		Configuration: json.RawMessage(`{"dense_core_view":true,"dense_core_mode":"subject","dense_core_weight":0.5,"dense_entity_view":true,"dense_entity_weight":0.4,"entity_leg":true,"passage_ratio_weight":0.15}`),
		Spaces:        []quivrplugin.SearchSpace{{ID: "sp1", Role: "served"}},
	}
	req.Query.Text = "Budget 2025 — Quelles annonces du ministre Eric Lombard pour la France ?"
	req.Query.Mode = "hybrid"

	ans, err := r.Search(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Requests) != 6 {
		t.Fatalf("expected 6 Round-1 requests (3 dense views + 2 bm25 + 1 ungrouped dense), got %d", len(ans.Requests))
	}
}


