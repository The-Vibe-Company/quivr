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

func TestHighPerfDefaultsEnabledOutOfTheBox(t *testing.T) {
	cfg, err := readConfig(nil)
	if err != nil {
		t.Fatal(err)
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
}

func TestRound1AsksMultiViewForStructuredQuestionsAndTwoLegsForShortQueries(t *testing.T) {
	r := New()
	// Short keyword query has no boilerplate -> stays at 2 clean legs.
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

	// Structured natural-language question automatically triggers question views.
	richReq := &quivrplugin.SearchRequest{
		Profile: "default",
		Round:   1,
		Limit:   10,
		Spaces:  []quivrplugin.SearchSpace{{ID: "sp1", Role: "served"}},
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
	if ans2.Requests[1].GroupBy != "record" {
		t.Fatalf("expected dense_view grouped by record, got %q", ans2.Requests[1].GroupBy)
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
	if len(bundles) < 2 || bundles[0].RecordID != "rec_lead" {
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
	for _, bad := range []string{"https://user:pass@example.org/rerank", "https://example.org/rerank#frag", "ftp://example.org/rerank"} {
		if _, err := readConfig(json.RawMessage(`{"rerank_endpoint":"` + bad + `"}`)); err == nil {
			t.Fatalf("expected invalid rerank_endpoint %q to be rejected", bad)
		}
	}
}
