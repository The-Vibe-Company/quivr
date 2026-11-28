package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func TestCleanLexicalQueryStripsHyphenatedAndFrenchStopwords(t *testing.T) {
	q := "Crise budgétaire en France\nQuelles mesures fiscales le ministre Eric Lombard a-t-il annoncées pour le budget 2025 selon l'UE ?"
	cleaned := cleanLexicalQuery(q)
	lower := strings.ToLower(cleaned)
	for _, forbidden := range []string{"quelles", " pour ", "a-t-il", "selon"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("expected stopword %q stripped from cleaned query %q", forbidden, cleaned)
		}
	}
	for _, want := range []string{"Lombard", "2025", "UE"} {
		if !strings.Contains(cleaned, want) {
			t.Errorf("expected content term %q preserved in %q", want, cleaned)
		}
	}
}

func intPtr(v int) *int { return &v }

func TestHighPerfDefaultsEnabledOutOfTheBox(t *testing.T) {
	cfg, err := readConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DenseK != 100 || cfg.LexicalK != 60 || !cfg.includeLead() || !cfg.CleanLexicalQuery || cfg.Fusion != "convex" {
		t.Fatalf("unexpected Stage-1 retrieval defaults: %+v", cfg)
	}
	if cfg.DenseWeight != 0.65 || cfg.LexicalWeight != 0.35 || cfg.TitleLeadWeight != 0.12 || cfg.LeadChars != 250 || cfg.PassageRatioWeight != 0.10 || cfg.PassageSupportWeight != 0.12 {
		t.Fatalf("unexpected Stage-1 fusion/aboutness defaults: %+v", cfg)
	}
	if cfg.MetadataOverlapWeight != 0.08 || cfg.DateAlignmentWeight != 0.06 || cfg.RecencyWeight != 0 || cfg.RecencyHalfLifeDays != 30 {
		t.Fatalf("unexpected CommonMetadata defaults: %+v", cfg)
	}
	if cfg.DenseViewMode != "question" || cfg.LexicalViewMode != "question" {
		t.Fatalf("expected multi-view question defaults ON, got dense=%q lex=%q", cfg.DenseViewMode, cfg.LexicalViewMode)
	}
	if cfg.RerankTopK != 50 || cfg.RerankScoreSpace != "logit" || cfg.RerankRanking != "zscore" {
		t.Fatalf("expected Grand Champion deep defaults ON, got topK=%d space=%q ranking=%q", cfg.RerankTopK, cfg.RerankScoreSpace, cfg.RerankRanking)
	}
	if cfg.RerankBandStart != 30 || cfg.RerankBandEnd != 80 || cfg.RerankParallelRequests != 2 {
		t.Fatalf("expected Recall Band 30..80 and 2-shard defaults ON, got band=%d..%d shards=%d", cfg.RerankBandStart, cfg.RerankBandEnd, cfg.RerankParallelRequests)
	}
	if got := clampK(250, 10); got != 250 {
		t.Fatalf("expected Plugin API 0.21 deep pool k=250 preserved, got %d", got)
	}
	if got := clampK(500, 10); got != 400 {
		t.Fatalf("expected k clamped at max_candidates=400, got %d", got)
	}
}

func TestRound1AsksMultiViewForStructuredQuestionsAndTwoLegsForShortQueries(t *testing.T) {
	r := New()
	// Short keyword query has no boilerplate -> stays at 2 clean legs with include_lead=true.
	shortReq := &quivrplugin.SearchRequest{
		Profile: "default",
		Round:   1,
		Limit:   10,
		Spaces:  []quivrplugin.SearchSpace{{ID: "sp1", Role: "served"}},
	}
	shortReq.Query.Text = "grève portuaire"
	shortReq.Query.Mode = "hybrid"
	ans, err := r.Search(context.Background(), shortReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Requests) != 2 {
		t.Fatalf("expected 2 legs for short keyword query, got %d: %+v", len(ans.Requests), ans.Requests)
	}
	for i, req := range ans.Requests {
		if !req.IncludeLead {
			t.Fatalf("expected include_lead=true by default on leg %d: %+v", i, req)
		}
	}

	// Structured natural-language question automatically triggers question views + deep pool config + opt-out include_lead.
	richReq := &quivrplugin.SearchRequest{
		Profile:       "default",
		Round:         1,
		Limit:         10,
		Configuration: json.RawMessage(`{"dense_k":250,"lexical_k":180,"include_lead":false}`),
		Spaces:        []quivrplugin.SearchSpace{{ID: "sp1", Role: "served"}},
	}
	richReq.Query.Text = "Grève portuaire\nQuelles conséquences de la grève des dockers sur le trafic maritime ?"
	richReq.Query.Mode = "hybrid"
	ans2, err := r.Search(context.Background(), richReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(ans2.Requests) != 4 {
		t.Fatalf("expected 4 legs (dense + dense_view + bm25 + lex_view) by default, got %d: %+v", len(ans2.Requests), ans2.Requests)
	}
	if ans2.Requests[0].K != 250 || ans2.Requests[2].K != 180 {
		t.Fatalf("expected deep candidate pool k=(250,180), got (%d,%d)", ans2.Requests[0].K, ans2.Requests[2].K)
	}
	if ans2.Requests[1].GroupBy != "record" {
		t.Fatalf("expected dense_view grouped by record, got %q", ans2.Requests[1].GroupBy)
	}
	for i, req := range ans2.Requests {
		if req.IncludeLead {
			t.Fatalf("expected include_lead=false when opted out on leg %d: %+v", i, req)
		}
	}
}

func TestExtractZoneTextsHandlesMultiParagraphPackedTitleLead(t *testing.T) {
	title := "Accord commercial européen"
	body := "Premier paragraphe du chapeau.\n\nDeuxième paragraphe dans le même segment."
	passage := title + "\n\n" + body
	cand := quivrplugin.Candidate{
		SegmentID:       "seg_packed",
		RecordID:        "rec_1",
		PartKey:         "title",
		Start:           0,
		End:             len([]rune(title)),
		PassageText:     passage,
		SourceSeparator: "\n\n",
		SourceRanges: []quivrplugin.SourceRange{
			{PartKey: "title", Start: 0, End: len([]rune(title))},
			{PartKey: "body", Start: 0, End: len([]rune(body))},
		},
	}
	b := &recordBundle{RecordID: "rec_1", BestSeg: cand, HasBestSeg: true, LeadSeg: &cand}
	gotTitle, gotLead := extractTitleAndLead(b, 250)
	if gotTitle != title {
		t.Fatalf("expected title %q, got %q", title, gotTitle)
	}
	if !strings.HasPrefix(gotLead, "Premier paragraphe") {
		t.Fatalf("expected body lead separated from title, got %q", gotLead)
	}
	sur := buildSurrogate(b, 400)
	if !strings.Contains(sur, "[TITLE]: "+title) || !strings.Contains(sur, "[LEAD]: Premier paragraphe") {
		t.Fatalf("surrogate failed to separate packed title and lead: %q", sur)
	}
}

func TestModalitySafeLeadSegPreventsBM25OverwritingDense(t *testing.T) {
	req := &quivrplugin.SearchRequest{Profile: "default", Round: 2, Limit: 5}
	req.Query.Text = "grève portuaire"
	req.Query.Mode = "hybrid"
	cfg, _ := readConfig(nil)

	dense := []quivrplugin.Candidate{
		{SegmentID: "seg_dense_lead", RecordID: "rec_1", PartKey: "body", Start: 0, End: 100, Text: "Dense lead text", Score: 0.92},
	}
	lex := []quivrplugin.Candidate{
		{SegmentID: "seg_bm25_lead", RecordID: "rec_1", PartKey: "body", Start: 0, End: 100, Text: "BM25 text", Score: 18.5},
	}
	bundles, _ := fuseAndScore(req, cfg, dense, lex, nil)
	if len(bundles) != 1 || bundles[0].LeadSeg == nil {
		t.Fatalf("expected 1 bundle with LeadSeg")
	}
	if bundles[0].LeadSeg.SegmentID != "seg_dense_lead" {
		t.Fatalf("BM25 score (18.5) must not overwrite dense LeadSeg (0.92); got %s", bundles[0].LeadSeg.SegmentID)
	}
}

func TestFirstPMaxPRatioDoesNotPenalizeSinglePassage(t *testing.T) {
	req := &quivrplugin.SearchRequest{Profile: "default", Round: 2, Limit: 5}
	req.Query.Text = "grève portuaire"
	req.Query.Mode = "hybrid"
	cfg, _ := readConfig(json.RawMessage(`{"dense_weight":0.7,"lexical_weight":0.3,"passage_ratio_weight":0.3,"title_lead_weight":0}`))

	dense := []quivrplugin.Candidate{
		{SegmentID: "deep_seg", RecordID: "rec_deep", PartKey: "body", Start: 2500, End: 4000, Text: "Mention rapide d'une grève portuaire en fin d'article.", Score: 0.82},
		{SegmentID: "lead_seg", RecordID: "rec_lead", PartKey: "body", Start: 0, End: 800, Text: "Une grève portuaire paralyse le trafic maritime dès l'aube.", Score: 0.80},
		{SegmentID: "floor_seg", RecordID: "rec_other", PartKey: "body", Start: 0, End: 500, Text: "Autre dépêche.", Score: 0.40},
	}
	bundles, _ := fuseAndScore(req, cfg, dense, nil, nil)
	if len(bundles) < 2 {
		t.Fatalf("expected >= 2 bundles, got %d", len(bundles))
	}
	if bundles[0].RecordID != "rec_lead" {
		t.Fatalf("expected single-passage lead doc to outrank deep passing mention; got %+v", bundles[0].RecordID)
	}
}

func TestLogitSpacePreservesRawMarginsAndComputesLogOdds(t *testing.T) {
	sRaw := rerankScores{Prob: []float64{0.9999, 0.9999}, Raw: []float64{9.5, 4.1}}
	got := sRaw.logitSpace()
	if got[0] != 9.5 || got[1] != 4.1 {
		t.Fatalf("expected raw margins preserved, got %v", got)
	}

	// Probability log-odds fallback when endpoint does not expose raw_score.
	sProb := rerankScores{Prob: []float64{0.99, 0.5}}
	gotProb := sProb.logitSpace()
	if !(gotProb[0] > 4.0 && math.Abs(gotProb[1]) < 1e-9) {
		t.Fatalf("unexpected log-odds: %v", gotProb)
	}
}

func TestShardedRerankMergesInOrderAndReportsRootError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Texts []string `json:"texts"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out := make([]map[string]any, len(body.Texts))
		for i, txt := range body.Texts {
			var v float64
			_, _ = fmt.Sscanf(txt, "doc%f", &v)
			out[i] = map[string]any{"index": i, "score": v / 100, "raw_score": v}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	ret := retriever{client: srv.Client()}
	cfg, _ := readConfig(json.RawMessage(`{"rerank_endpoint":"` + srv.URL + `"}`))
	docs := make([]string, 9)
	for i := range docs {
		docs[i] = fmt.Sprintf("doc%d", i)
	}
	s, err := ret.callRerankerSharded(context.Background(), cfg, "q", docs, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 sharded calls, got %d", got)
	}
	for i := range docs {
		if s.Raw[i] != float64(i) {
			t.Fatalf("shard merge out of order: %v", s.Raw)
		}
	}

	// Verify HTTP 500 on one shard is never masked by context.Canceled on a slow sibling shard.
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Texts []string `json:"texts"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Texts) > 0 && body.Texts[0] == "doc1" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer failSrv.Close()
	cfgFail, _ := readConfig(json.RawMessage(`{"rerank_endpoint":"` + failSrv.URL + `"}`))
	_, err = ret.callRerankerSharded(context.Background(), cfgFail, "q", []string{"doc0", "doc1", "doc2", "doc3"}, 2)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("expected root HTTP 500 error, got %v", err)
	}
}

func TestRecallBandStraddlesCutAndPreservesHeadAndMonotonicity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Texts []string `json:"texts"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out := make([]map[string]any, len(body.Texts))
		for i, txt := range body.Texts {
			raw := 0.0
			switch {
			case strings.Contains(txt, "DOC_HEAD_TOP"):
				raw = 5.0
			case strings.Contains(txt, "DOC_HEAD_WEAK"):
				raw = -4.0
			case strings.Contains(txt, "DOC_BAND_GEM"):
				raw = 8.0
			}
			out[i] = map[string]any{"index": i, "score": 0.5, "raw_score": raw}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	ret := retriever{client: srv.Client()}
	cfg, _ := readConfig(json.RawMessage(`{
		"rerank_endpoint": "` + srv.URL + `",
		"rerank_top_k": 3,
		"rerank_score_space": "logit",
		"rerank_ranking": "zscore",
		"rerank_weight": 0.9,
		"rerank_band_start": 1,
		"rerank_band_end": 5,
		"rerank_band_weight": 0.95,
		"rerank_parallel_requests": 1
	}`))
	mk := func(id, txt string, s float64) *recordBundle {
		c := quivrplugin.Candidate{SegmentID: id, RecordID: id, PartKey: "body", Start: 0, Text: txt, Score: s}
		return &recordBundle{RecordID: id, BestSeg: c, HasBestSeg: true, LeadSeg: &c, FinalScore: s, Explanation: "newsroom(convex,legs=2)"}
	}
	bundles := []*recordBundle{
		mk("H0", "DOC_HEAD_TOP", 0.95),
		mk("H1", "DOC_HEAD_WEAK", 0.85),
		mk("H2", "DOC_HEAD_WEAK", 0.80),
		mk("B0", "DOC_BAND_GEM", 0.70),
		mk("B1", "other", 0.65),
		mk("T0", "untouched tail", 0.50),
	}
	req := &quivrplugin.SearchRequest{Profile: "deep", Round: 2, Limit: 6}
	req.Query.Text = "q"
	out, err := ret.applyDeepRerank(context.Background(), req, cfg, time.Now(), bundles)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].RecordID != "H0" || out[1].RecordID != "B0" || out[5].RecordID != "T0" {
		t.Fatalf("unexpected Recall Band order: %s %s %s %s %s %s",
			out[0].RecordID, out[1].RecordID, out[2].RecordID, out[3].RecordID, out[4].RecordID, out[5].RecordID)
	}
	if out[1].Explanation != "newsroom+deep(band)" {
		t.Fatalf("expected promoted band explanation updated, got %q", out[1].Explanation)
	}
	for i := 1; i < len(out); i++ {
		if out[i].FinalScore > out[i-1].FinalScore {
			t.Fatalf("non-monotonic scores at %d: %f > %f", i, out[i].FinalScore, out[i-1].FinalScore)
		}
	}
}

func TestDeepFallbackAndStrictErrorModes(t *testing.T) {
	mkReq := func(cfgJSON string) *quivrplugin.SearchRequest {
		req := &quivrplugin.SearchRequest{
			Profile:       "deep",
			Round:         2,
			Limit:         2,
			Configuration: json.RawMessage(cfgJSON),
			Spaces:        []quivrplugin.SearchSpace{{ID: "sp1", Role: "served"}},
			Served: []quivrplugin.ServedRequest{
				{
					Round:        1,
					RequestIndex: 0,
					Request:      quivrplugin.CandidateRequest{Primitive: quivrplugin.PrimitiveNearVector, QueryText: "harbour strike", Space: "sp1", K: 100},
					Candidates: []quivrplugin.Candidate{
						{SegmentID: "s1", RecordID: "r1", PartKey: "body", Start: 0, Text: "Harbour strike blocks ships", Score: 0.9},
						{SegmentID: "s2", RecordID: "r2", PartKey: "body", Start: 0, Text: "Other news", Score: 0.5},
					},
				},
			},
		}
		req.Query.Text = "harbour strike"
		req.Query.Mode = "semantic"
		return req
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer errSrv.Close()
	r := retriever{client: errSrv.Client()}

	// HTTP 503 with fallback_on_error: true returns Stage-1 ranking.
	ans, err := r.Search(context.Background(), mkReq(`{"rerank_endpoint":"`+errSrv.URL+`","rerank_parallel_requests":1,"fallback_on_error":true}`))
	if err != nil {
		t.Fatalf("expected graceful fallback on HTTP 503, got error: %v", err)
	}
	if len(ans.Ranking) != 2 || ans.Ranking[0].SegmentID != "s1" {
		t.Fatalf("unexpected fallback ranking: %+v", ans.Ranking)
	}

	// HTTP 503 with fallback_on_error: false returns retryable reranker_unavailable.
	if _, err := r.Search(context.Background(), mkReq(`{"rerank_endpoint":"`+errSrv.URL+`","rerank_parallel_requests":1,"fallback_on_error":false}`)); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("expected strict HTTP 503 error, got %v", err)
	}

	// Timeout with fallback_on_error: true vs false.
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slowSrv.Close()
	rSlow := retriever{client: slowSrv.Client()}

	ansSlow, err := rSlow.Search(context.Background(), mkReq(`{"rerank_endpoint":"`+slowSrv.URL+`","rerank_timeout_ms":100,"rerank_parallel_requests":1,"fallback_on_error":true}`))
	if err != nil || len(ansSlow.Ranking) != 2 || ansSlow.Ranking[0].SegmentID != "s1" {
		t.Fatalf("expected graceful timeout fallback, got ans=%+v err=%v", ansSlow, err)
	}
	if _, err := rSlow.Search(context.Background(), mkReq(`{"rerank_endpoint":"`+slowSrv.URL+`","rerank_timeout_ms":100,"rerank_parallel_requests":1,"fallback_on_error":false}`)); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected strict timeout error, got %v", err)
	}
}

func TestRerankEndpointRejectsUserinfoAndFragment(t *testing.T) {
	for _, bad := range []string{"https://user:pass@example.org/rerank", "https://example.org/rerank#frag", "ftp://example.org/rerank", "https:///rerank", "http://:8080/rerank"} {
		if _, err := readConfig(json.RawMessage(`{"rerank_endpoint":"` + bad + `"}`)); err == nil {
			t.Fatalf("expected invalid rerank_endpoint %q to be rejected", bad)
		}
	}
}

func TestExplicitTitleAndHydratedLeadEnrichAboutnessAndSurrogates(t *testing.T) {
	deepCand := quivrplugin.Candidate{
		SegmentID:      "seg_deep_2",
		RecordID:       "rec_deep",
		PartKey:        "body",
		Start:          1800,
		End:            2400,
		Title:          "Accord commercial européen",
		Text:           "Les ministres ont détaillé le calendrier d'application au paragraphe trois.",
		PassageOrdinal: intPtr(2),
		TotalPassages:  5,
		LeadPassage:    "Accord commercial européen\n\nBruxelles et ses partenaires ont signé un accord douanier majeur.",
		Score:          0.84,
	}
	b := &recordBundle{
		RecordID:      "rec_deep",
		Title:         deepCand.Title,
		TotalPassages: deepCand.TotalPassages,
		LeadPassage:   deepCand.LeadPassage,
		BestSeg:       deepCand,
		HasBestSeg:    true,
	}

	gotTitle, gotLead := extractTitleAndLead(b, 250)
	if gotTitle != "Accord commercial européen" {
		t.Fatalf("expected explicit Candidate.Title, got %q", gotTitle)
	}
	if gotLead != "Bruxelles et ses partenaires ont signé un accord douanier majeur." {
		t.Fatalf("expected hydrated LeadPassage with duplicated title line stripped, got %q", gotLead)
	}

	sur := buildSurrogate(b, 1000)
	for _, want := range []string{"[TITLE]: Accord commercial européen", "[LEAD]: Bruxelles et ses partenaires", "[MATCHED DEEP PASSAGE]: Les ministres ont détaillé"} {
		if !strings.Contains(sur, want) {
			t.Fatalf("expected deep surrogate to contain %q, got %q", want, sur)
		}
	}
	if strings.Contains(sur, "NOT IN ARTICLE LEAD") {
		t.Fatalf("expected hydrated LeadPassage to replace missing-lead banner, got %q", sur)
	}

	lv := buildLeadView(b, 600)
	if !strings.Contains(lv, "[TITLE]: Accord commercial européen") || !strings.Contains(lv, "[LEAD]: Bruxelles et ses partenaires") || strings.Contains(lv, "[MATCHED DEEP PASSAGE]") {
		t.Fatalf("expected Recall Band lead view built from explicit Title + LeadPassage, got %q", lv)
	}
}

func TestExactPassageOrdinalAndTotalPassagesReplaceOffsetHeuristics(t *testing.T) {
	// PassageOrdinal==0 with Start>0 (e.g. after dateline trim) is still the exact lead.
	leadAfterDateline := quivrplugin.Candidate{
		SegmentID:      "s_lead_offset",
		RecordID:       "r_lead",
		PartKey:        "body",
		Start:          120,
		End:            600,
		PassageOrdinal: intPtr(0),
		TotalPassages:  1,
		Text:           "Grève portuaire à Marseille.",
		Score:          0.80,
	}
	if !isLeadSegment(leadAfterDateline) {
		t.Fatal("expected PassageOrdinal==0 to be recognized as lead regardless of Start offset")
	}

	// PassageOrdinal>0 with Start==0 (e.g. a later part starting at offset 0) is NOT the lead.
	deepPartZero := quivrplugin.Candidate{
		SegmentID:      "s_deep_zero",
		RecordID:       "r_deep_early",
		PartKey:        "sidebar",
		Start:          0,
		End:            400,
		PassageOrdinal: intPtr(1),
		TotalPassages:  5,
		Text:           "Rappel sur la grève portuaire.",
		Score:          0.82,
	}
	if isLeadSegment(deepPartZero) {
		t.Fatal("expected PassageOrdinal>0 to be treated as deep passage even when Start==0")
	}

	deepTail := quivrplugin.Candidate{
		SegmentID:      "s_deep_tail",
		RecordID:       "r_deep_tail",
		PartKey:        "body",
		Start:          4000,
		End:            4500,
		PassageOrdinal: intPtr(4),
		TotalPassages:  5,
		Text:           "Rappel sur la grève portuaire.",
		Score:          0.82,
	}
	floor := quivrplugin.Candidate{
		SegmentID:      "s_floor",
		RecordID:       "r_floor",
		PartKey:        "body",
		Start:          0,
		End:            300,
		PassageOrdinal: intPtr(0),
		TotalPassages:  2,
		Text:           "Autre sujet.",
		Score:          0.40,
	}

	req := &quivrplugin.SearchRequest{Profile: "default", Round: 2, Limit: 5}
	req.Query.Text = "grève portuaire"
	req.Query.Mode = "semantic"
	cfg, _ := readConfig(json.RawMessage(`{"title_lead_weight":0,"metadata_overlap_weight":0,"date_alignment_weight":0,"passage_ratio_weight":0.30}`))
	bundles, _ := fuseAndScore(req, cfg, []quivrplugin.Candidate{deepTail, deepPartZero, leadAfterDateline, floor}, nil, nil)
	if len(bundles) != 4 {
		t.Fatalf("expected 4 bundles, got %d", len(bundles))
	}
	if bundles[0].RecordID != "r_lead" || bundles[1].RecordID != "r_deep_early" || bundles[2].RecordID != "r_deep_tail" {
		t.Fatalf("expected single-passage lead > early deep (ord 1/5) > buried tail (ord 4/5); got %s, %s, %s",
			bundles[0].RecordID, bundles[1].RecordID, bundles[2].RecordID)
	}
}

func TestCommonMetadataOverlapDateAlignmentAndPoolRecency(t *testing.T) {
	mkCand := func(id string, score float64, meta *quivrplugin.CommonMetadata) quivrplugin.Candidate {
		return quivrplugin.Candidate{
			SegmentID:      "seg_" + id,
			RecordID:       "rec_" + id,
			PartKey:        "body",
			Start:          0,
			End:            80,
			Text:           "Communiqué sur le budget public.",
			PassageOrdinal: intPtr(0),
			TotalPassages:  1,
			CommonMetadata: meta,
			Score:          score,
		}
	}

	// 1. Metadata facet overlap + date-mention alignment (EN/FR month+year & year) boost matching candidate.
	cMatch := mkCand("match", 0.78, &quivrplugin.CommonMetadata{
		PublishedAt: "2025-03-12T09:00:00Z",
		Subjects:    []string{"finances publiques"},
		Tags:        []string{"budget"},
		Place:       []string{"Paris"},
		Author:      []string{"Claire Moreau"},
	})
	cStale := mkCand("stale", 0.80, &quivrplugin.CommonMetadata{
		PublishedAt: "2021-07-04T09:00:00Z",
		Subjects:    []string{"sport"},
		Place:       []string{"Lyon"},
	})
	cFloor := mkCand("floor", 0.30, nil)

	req := &quivrplugin.SearchRequest{Profile: "default", Round: 2, Limit: 5}
	req.Query.Text = "budget Paris mars 2025 Claire Moreau"
	req.Query.Mode = "semantic"

	cfgOn, _ := readConfig(json.RawMessage(`{"title_lead_weight":0,"passage_ratio_weight":0,"passage_support_weight":0,"metadata_overlap_weight":0.10,"date_alignment_weight":0.08}`))
	onBundles, _ := fuseAndScore(req, cfgOn, []quivrplugin.Candidate{cStale, cMatch, cFloor}, nil, nil)
	if onBundles[0].RecordID != "rec_match" {
		t.Fatalf("expected metadata+date match to outrank stale candidate, got %s first", onBundles[0].RecordID)
	}

	// Opt-out with 0 weights restores pure Stage-1 dense order.
	cfgOff, _ := readConfig(json.RawMessage(`{"title_lead_weight":0,"passage_ratio_weight":0,"passage_support_weight":0,"metadata_overlap_weight":0,"date_alignment_weight":0}`))
	offBundles, _ := fuseAndScore(req, cfgOff, []quivrplugin.Candidate{cStale, cMatch, cFloor}, nil, nil)
	if offBundles[0].RecordID != "rec_stale" {
		t.Fatalf("expected 0 weights to disable metadata/date boosting, got %s first", offBundles[0].RecordID)
	}

	// 2. Opt-in deterministic pool-relative recency boost promotes fresher dispatch when enabled.
	reqNoDate := &quivrplugin.SearchRequest{Profile: "default", Round: 2, Limit: 5}
	reqNoDate.Query.Text = "budget public"
	reqNoDate.Query.Mode = "semantic"
	cFresh := mkCand("fresh", 0.78, &quivrplugin.CommonMetadata{PublishedAt: "2026-11-27T10:00:00Z"})
	cOld := mkCand("old", 0.80, &quivrplugin.CommonMetadata{PublishedAt: "2026-06-01T10:00:00Z"})

	cfgDefaultRecency, _ := readConfig(json.RawMessage(`{"title_lead_weight":0,"passage_ratio_weight":0,"passage_support_weight":0,"metadata_overlap_weight":0,"date_alignment_weight":0}`))
	bArchival, _ := fuseAndScore(reqNoDate, cfgDefaultRecency, []quivrplugin.Candidate{cOld, cFresh, cFloor}, nil, nil)
	if bArchival[0].RecordID != "rec_old" {
		t.Fatalf("expected recency_weight=0 default to preserve archival order, got %s first", bArchival[0].RecordID)
	}

	cfgRecencyOn, _ := readConfig(json.RawMessage(`{"title_lead_weight":0,"passage_ratio_weight":0,"passage_support_weight":0,"metadata_overlap_weight":0,"date_alignment_weight":0,"recency_weight":0.10,"recency_half_life_days":30}`))
	bLive1, _ := fuseAndScore(reqNoDate, cfgRecencyOn, []quivrplugin.Candidate{cOld, cFresh, cFloor}, nil, nil)
	bLive2, _ := fuseAndScore(reqNoDate, cfgRecencyOn, []quivrplugin.Candidate{cOld, cFresh, cFloor}, nil, nil)
	if bLive1[0].RecordID != "rec_fresh" {
		t.Fatalf("expected recency_weight>0 to boost fresher dispatch, got %s first", bLive1[0].RecordID)
	}
	if bLive1[0].FinalScore != bLive2[0].FinalScore {
		t.Fatalf("pool-relative recency must be strictly deterministic: %f vs %f", bLive1[0].FinalScore, bLive2[0].FinalScore)
	}

	// 3. Date parser handles EN month-day-year, FR day-month-year, ISO, and standalone years.
	dates := extractQueryDates("March 14, 2025 summit and 2024-11 review in 2023")
	if len(dates) != 3 || dates[0] != (queryDate{Year: 2024, Month: 11}) || dates[1] != (queryDate{Year: 2025, Month: 3}) || dates[2] != (queryDate{Year: 2023, Month: 0}) {
		t.Fatalf("unexpected extracted query dates: %+v", dates)
	}
}
