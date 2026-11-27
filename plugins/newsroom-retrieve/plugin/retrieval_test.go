package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
	cleaned := cleanLexicalQuery(q)
	if strings.Contains(strings.ToLower(cleaned), "quelles") || strings.Contains(strings.ToLower(cleaned), " pour ") {
		t.Errorf("expected stopwords stripped from cleaned lexical query %q", cleaned)
	}
	if !strings.Contains(cleaned, "Lombard") || !strings.Contains(cleaned, "2025") {
		t.Errorf("expected content terms preserved in cleaned lexical query %q", cleaned)
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

func TestRound1AsksTwoLegsByDefaultAndFourWithGroupedEntity(t *testing.T) {
	r := New()
	req := &quivrplugin.SearchRequest{
		Profile:       "default",
		Round:         1,
		Limit:         10,
		Configuration: json.RawMessage(`{"lexical_k":60,"dense_k":100}`),
		Spaces:        []quivrplugin.SearchSpace{{ID: "sp1", Role: "served"}},
	}
	req.Query.Text = "Budget 2025\nQuelles annonces du ministre Eric Lombard ?"
	req.Query.Mode = "hybrid"

	ans, err := r.Search(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Requests) != 2 {
		t.Fatalf("expected 2 Round-1 requests (ungrouped dense + bm25), got %d", len(ans.Requests))
	}
	if ans.Requests[0].Primitive != quivrplugin.PrimitiveNearVector || ans.Requests[0].GroupBy != "" || ans.Requests[0].K != 100 {
		t.Fatalf("unexpected leg 0: %+v", ans.Requests[0])
	}
	if ans.Requests[1].Primitive != quivrplugin.PrimitiveBM25 || ans.Requests[1].GroupBy != "record" || ans.Requests[1].K != 60 {
		t.Fatalf("unexpected leg 1: %+v", ans.Requests[1])
	}

	req.Configuration = json.RawMessage(`{"dense_grouped":true,"entity_leg":true,"passage_ratio_weight":0.2}`)
	ans, err = r.Search(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Requests) != 4 {
		t.Fatalf("expected 4 Round-1 requests (dense grouped, bm25 full, bm25 entity, dense ungrouped), got %d", len(ans.Requests))
	}
}

func TestTwoLegUngroupedPrimaryAndTitleLeadIDF(t *testing.T) {
	req := &quivrplugin.SearchRequest{
		Profile: "default",
		Round:   2,
		Limit:   5,
	}
	req.Query.Text = "grève portuaire Marseille"
	req.Query.Mode = "hybrid"

	cfg, err := readConfig(json.RawMessage(`{"fusion":"convex","dense_weight":0.65,"lexical_weight":0.35,"title_lead_weight":0.15,"passage_support_weight":0.10}`))
	if err != nil {
		t.Fatal(err)
	}

	denseUngrouped := []quivrplugin.Candidate{
		{SegmentID: "deep_s1", RecordID: "rec_deep", PartKey: "body", Start: 2500, End: 4000, Text: "Mention rapide d'une grève portuaire à Marseille.", Score: 0.83},
		{SegmentID: "lead_s1", RecordID: "rec_lead", PartKey: "body", Start: 0, End: 800, Text: "Une grève portuaire paralyse le port de Marseille dès l'aube.", Score: 0.81},
		{SegmentID: "lead_s2", RecordID: "rec_lead", PartKey: "body", Start: 800, End: 1600, Text: "Les dockers de Marseille poursuivent la grève portuaire.", Score: 0.78},
		{SegmentID: "other_s1", RecordID: "rec_other", PartKey: "body", Start: 0, End: 400, Text: "Météo ensoleillée sur la côte.", Score: 0.30},
	}
	lexical := []quivrplugin.Candidate{
		{SegmentID: "deep_s1", RecordID: "rec_deep", PartKey: "body", Start: 2500, End: 4000, Text: "Mention rapide d'une grève portuaire à Marseille.", Score: 4.0},
		{SegmentID: "lead_s1", RecordID: "rec_lead", PartKey: "body", Start: 0, End: 800, Text: "Une grève portuaire paralyse le port de Marseille dès l'aube.", Score: 4.0},
		{SegmentID: "other_s1", RecordID: "rec_other", PartKey: "body", Start: 0, End: 400, Text: "Météo ensoleillée sur la côte.", Score: 1.0},
	}

	bundles, qppDense := fuseAndScoreWithQPP(req, cfg, nil, lexical, nil, denseUngrouped)
	if len(qppDense) != 3 {
		t.Fatalf("expected 3 deduplicated records for QPP, got %d", len(qppDense))
	}
	if bundles[0].RecordID != "rec_lead" {
		t.Fatalf("expected rec_lead to win via title+lead IDF + multi-passage support, got %s", bundles[0].RecordID)
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

func TestDeepRerankInstructionAndFallbackOnTimeout(t *testing.T) {
	var mu sync.Mutex
	var gotInstruction string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query       string   `json:"query"`
			Texts       []string `json:"texts"`
			Instruction string   `json:"instruction"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		gotInstruction = body.Instruction
		mu.Unlock()
		if strings.Contains(body.Query, "TIMEOUT") {
			select {
			case <-time.After(250 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"index":0,"score":0.1},{"index":1,"score":0.95}]`))
	}))
	defer srv.Close()

	ret := retriever{client: srv.Client()}
	mkReq := func(query string, cfgJSON string) *quivrplugin.SearchRequest {
		req := &quivrplugin.SearchRequest{
			Profile:       "deep",
			Round:         2,
			Limit:         2,
			Configuration: json.RawMessage(cfgJSON),
			Served: []quivrplugin.ServedRequest{
				{
					Round: 1,
					Request: quivrplugin.CandidateRequest{
						Primitive: quivrplugin.PrimitiveNearVector,
						GroupBy:   "",
					},
					Candidates: []quivrplugin.Candidate{
						{SegmentID: "s1", RecordID: "r1", PartKey: "body", Start: 0, Text: "Article A", Score: 0.9},
						{SegmentID: "s2", RecordID: "r2", PartKey: "body", Start: 0, Text: "Article B", Score: 0.7},
					},
				},
			},
		}
		req.Query.Text = query
		req.Query.Mode = "hybrid"
		return req
	}

	// 1. Custom rubric/instruction forwarded and rerank flips r2 above r1.
	wantInst := "Judge strict newsroom centrality."
	cfgOK, _ := json.Marshal(map[string]any{
		"rerank_endpoint":    srv.URL,
		"rerank_protocol":    "tei",
		"rerank_instruction": wantInst,
		"rerank_weight":      0.9,
		"rerank_timeout_ms":  2000,
		"fallback_on_error":  true,
	})
	ans, err := ret.Search(context.Background(), mkReq("budget", string(cfgOK)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	inst := gotInstruction
	mu.Unlock()
	if inst != wantInst {
		t.Fatalf("expected instruction %q, got %q", wantInst, inst)
	}
	if len(ans.Ranking) != 2 || ans.Ranking[0].SegmentID != "s2" {
		t.Fatalf("expected rerank to promote s2, got %+v", ans.Ranking)
	}

	// 2. Timeout with fallback_on_error=true gracefully returns Stage-1 default ordering (s1 first).
	cfgTimeoutFallback, _ := json.Marshal(map[string]any{
		"rerank_endpoint":   srv.URL,
		"rerank_protocol":   "tei",
		"rerank_timeout_ms": 100,
		"fallback_on_error": true,
	})
	ans, err = ret.Search(context.Background(), mkReq("TIMEOUT", string(cfgTimeoutFallback)))
	if err != nil {
		t.Fatalf("expected graceful fallback on timeout, got err: %v", err)
	}
	if len(ans.Ranking) != 2 || ans.Ranking[0].SegmentID != "s1" {
		t.Fatalf("expected fallback to Stage-1 s1 first, got %+v", ans.Ranking)
	}

	// 3. Timeout with fallback_on_error=false returns retryable SearchError.
	cfgTimeoutStrict, _ := json.Marshal(map[string]any{
		"rerank_endpoint":   srv.URL,
		"rerank_protocol":   "tei",
		"rerank_timeout_ms": 100,
		"fallback_on_error": false,
	})
	_, err = ret.Search(context.Background(), mkReq("TIMEOUT", string(cfgTimeoutStrict)))
	if err == nil {
		t.Fatalf("expected error when fallback_on_error=false on timeout")
	}
}

func TestDefaultConfigMatchesUnifiedChampion(t *testing.T) {
	cfg, err := readConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DenseGrouped || !cfg.CleanLexicalQuery || cfg.LexicalK != 60 || cfg.DenseK != 100 {
		t.Fatalf("unexpected Stage-1 leg defaults: %+v", cfg)
	}
	if cfg.Fusion != "convex" || cfg.DenseWeight != 0.65 || cfg.LexicalWeight != 0.35 {
		t.Fatalf("unexpected fusion defaults: %+v", cfg)
	}
	if cfg.TitleLeadWeight != 0.12 || cfg.PassageRatioWeight != 0.10 || cfg.PassageSupportWeight != 0.12 {
		t.Fatalf("unexpected aboutness signal defaults: %+v", cfg)
	}
	if cfg.RerankTopK != 30 || cfg.RerankWeight != 0.90 || cfg.RerankMaxDocChars != 2000 || !cfg.fallbackOnError() {
		t.Fatalf("unexpected deep rerank defaults: %+v", cfg)
	}
}
