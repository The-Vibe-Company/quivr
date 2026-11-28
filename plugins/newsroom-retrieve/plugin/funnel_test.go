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

func wideFunnelRequest(round int, cfgJSON string) *quivrplugin.SearchRequest {
	req := &quivrplugin.SearchRequest{
		Profile:       "default",
		Round:         round,
		Limit:         10,
		Configuration: json.RawMessage(cfgJSON),
		Spaces:        []quivrplugin.SearchSpace{{ID: "sp1", Role: "served"}},
	}
	req.Query.Text = "Grève portuaire\nQuelles conséquences de la grève des dockers sur le trafic maritime ?"
	req.Query.Mode = "hybrid"
	return req
}

func TestWideFunnelRound1AddsNamedViewLegs(t *testing.T) {
	cfg := `{"dense_view_mode":"question","dense_view_weight":0.16,"dense_view_grouped":true,"dense_recall_weight":0.15,"lexical_view_mode":"question","lexical_view_weight":0.1}`
	req := wideFunnelRequest(1, cfg)
	ans, err := New().Search(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := readConfig(req.Configuration)
	specs, _ := round1Specs(req, c)
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	want := []string{legDense, legLexical, legDenseView, legDenseRecall, legLexView}
	if strings.Join(names, ",") != strings.Join(want, ",") || len(ans.Requests) != len(want) {
		t.Fatalf("unexpected Round-1 legs %v (%d requests)", names, len(ans.Requests))
	}
	view := ans.Requests[2]
	if view.GroupBy != "record" || strings.Contains(strings.ToLower(view.QueryText), "quelles") || view.QueryText == req.Query.Text {
		t.Fatalf("dense view should be a grouped boilerplate-stripped question: %+v", view)
	}
	if ans.Requests[3].GroupBy != "record" || ans.Requests[3].QueryText != req.Query.Text {
		t.Fatalf("dense recall leg should be grouped full query: %+v", ans.Requests[3])
	}
	for _, r := range ans.Requests {
		if r.K > 100 {
			t.Fatalf("contract caps k at 100: %+v", r)
		}
	}

	// Defaults keep the 2-leg champion request list.
	ans, err = New().Search(context.Background(), wideFunnelRequest(1, `{}`))
	if err != nil || len(ans.Requests) != 2 {
		t.Fatalf("defaults must keep 2 Round-1 requests, got %v err=%v", len(ans.Requests), err)
	}
}

func TestExtraLegAdmitsAndRanksRecordsByRequestIndex(t *testing.T) {
	cfg := `{"dense_recall_weight":0.5}`
	req := wideFunnelRequest(2, cfg)
	c, _ := readConfig(req.Configuration)
	specs, _ := round1Specs(req, c)
	if len(specs) != 3 || specs[2].Name != legDenseRecall {
		t.Fatalf("unexpected specs %+v", specs)
	}
	seg := func(id, rec string, start int, score float64) quivrplugin.Candidate {
		return quivrplugin.Candidate{SegmentID: id, RecordID: rec, PartKey: "body", Start: start, End: start + 100, Text: "grève dockers trafic maritime " + id, Score: score}
	}
	req.Served = []quivrplugin.ServedRequest{
		{Round: 1, RequestIndex: 0, Request: specs[0].Req, Candidates: []quivrplugin.Candidate{seg("a0", "A", 0, 0.80), seg("b0", "B", 0, 0.70)}},
		{Round: 1, RequestIndex: 1, Request: specs[1].Req, Candidates: []quivrplugin.Candidate{seg("a0", "A", 0, 9.0)}},
		// Record C is only retrieved by the grouped recall leg.
		{Round: 1, RequestIndex: 2, Request: specs[2].Req, Candidates: []quivrplugin.Candidate{seg("a0", "A", 0, 0.80), seg("c0", "C", 0, 0.75), seg("b0", "B", 0, 0.70)}},
	}
	ans, err := New().Search(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, h := range ans.Ranking {
		got[h.SegmentID] = true
	}
	if !got["c0"] {
		t.Fatalf("record only in the recall leg must enter the pool: %+v", ans.Ranking)
	}
	for i := 1; i < len(ans.Ranking); i++ {
		if ans.Ranking[i].Score > ans.Ranking[i-1].Score {
			t.Fatalf("hits must be monotonic: %+v", ans.Ranking)
		}
	}
}

func TestStatsAtDepthKeepsHeadFloor(t *testing.T) {
	cands := []quivrplugin.Candidate{{Score: 10}, {Score: 8}, {Score: 6}, {Score: 1}}
	st := statsAtDepth(cands, 3)
	if st.min != 6 || st.max != 10 {
		t.Fatalf("depth-3 stats should use the first 3 hits: %+v", st)
	}
	if v := normalizeLegScore(1, 4, st, "convex", 60); v != 0 {
		t.Fatalf("hits below the calibration depth clamp to 0, got %v", v)
	}
}

func TestTailFusionKeepsHeadAndMonotonicScores(t *testing.T) {
	var bundles []*recordBundle
	for i := 0; i < 6; i++ {
		b := &recordBundle{RecordID: string(rune('A' + i)), FinalScore: float64(10 - i), HasBestSeg: true}
		b.BestSeg.SegmentID = b.RecordID
		bundles = append(bundles, b)
	}
	// F (last) is top-1 in a leg; it should move up inside the tail only.
	bundles[5].HasLexical, bundles[5].LexicalRank = true, 1
	cfg, _ := readConfig(json.RawMessage(`{"tail_fusion":"rrf","tail_start":2}`))
	applyTailFusion(bundles, cfg)
	if bundles[0].RecordID != "A" || bundles[1].RecordID != "B" {
		t.Fatalf("head must be untouched: %v %v", bundles[0].RecordID, bundles[1].RecordID)
	}
	if bundles[2].RecordID != "F" {
		t.Fatalf("strong single-leg record should lead the tail, got %s", bundles[2].RecordID)
	}
	for i := 1; i < len(bundles); i++ {
		if bundles[i].FinalScore > bundles[i-1].FinalScore {
			t.Fatalf("scores must stay monotonic")
		}
	}
}

func TestDecodeRawScoresAndLogitSpace(t *testing.T) {
	raw := []byte(`[{"index":1,"score":0.9999,"raw_score":9.5},{"index":0,"score":0.999,"raw_score":7.0}]`)
	s, err := decodeRerankScoresFull(raw, "tei", 2)
	if err != nil || s.Raw == nil {
		t.Fatalf("expected raw margins: %+v %v", s, err)
	}
	l := s.logitSpace()
	if l[0] != 7.0 || l[1] != 9.5 {
		t.Fatalf("logit space should use raw margins, got %v", l)
	}
	// Without raw margins, log-odds are derived from (clamped) probabilities.
	s, _ = decodeRerankScoresFull([]byte(`[{"index":0,"score":0.5},{"index":1,"score":1.0}]`), "tei", 2)
	l = s.logitSpace()
	if s.Raw != nil || math.Abs(l[0]) > 1e-9 || math.IsInf(l[1], 0) || l[1] < 10 {
		t.Fatalf("unexpected derived log-odds %v", l)
	}
}

func TestRichSurrogateAndMultiPassageViews(t *testing.T) {
	lead := quivrplugin.Candidate{SegmentID: "l", RecordID: "R", PartKey: "body", Start: 0, End: 300, Text: "Les dockers ont entamé une grève illimitée lundi, bloquant le port.", Score: 0.7}
	deep := quivrplugin.Candidate{SegmentID: "d", RecordID: "R", PartKey: "body", Start: 2000, End: 2600, Text: "Le trafic maritime a chuté de moitié selon l'autorité portuaire.", Score: 0.8}
	b := &recordBundle{RecordID: "R", BestSeg: deep, HasBestSeg: true, LeadSeg: &lead}
	s := buildRichSurrogate(b, 2200, 1200)
	if !strings.Contains(s, "[LEAD]: Les dockers") || !strings.Contains(s, "[BEST BODY PASSAGE]: Le trafic") {
		t.Fatalf("rich surrogate must carry lead and best body: %q", s)
	}
	if lv := buildLeadView(b, 2000); strings.Contains(lv, "trafic") {
		t.Fatalf("lead view must exclude the body passage: %q", lv)
	}
	if bv, ok := buildBodyView(b, 2000); !ok || !strings.Contains(bv, "trafic") || strings.Contains(bv, "dockers") {
		t.Fatalf("unexpected body view %q", bv)
	}
	if v := combineViews(4, 8, "lead_gated", 0.35); v != 4+0.35*4 {
		t.Fatalf("lead_gated combine: %v", v)
	}
	if v := combineViews(8, 2, "lead_gated", 0.35); v != 8 {
		t.Fatalf("body view must never sink a strong lead: %v", v)
	}
	if v := combineViews(2, 8, "maxmin", 0.35); math.Abs(v-(0.65*8+0.35*2)) > 1e-12 {
		t.Fatalf("maxmin combine: %v", v)
	}
}

func TestDeepZScoreLogitMultiPassageEndToEnd(t *testing.T) {
	var gotTexts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Texts []string `json:"texts"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotTexts = len(body.Texts)
		out := make([]map[string]any, len(body.Texts))
		for i, txt := range body.Texts {
			margin := -2.0
			if strings.Contains(txt, "chute") {
				margin = 6.0
			}
			out[i] = map[string]any{"index": i, "score": 1 / (1 + math.Exp(-margin)), "raw_score": margin}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	ret := retriever{client: srv.Client()}
	req := &quivrplugin.SearchRequest{Profile: "deep", Round: 2, Limit: 10}
	req.Query.Text = "trafic maritime"
	req.Query.Mode = "hybrid"
	cfg, _ := readConfig(json.RawMessage(`{"rerank_endpoint":"` + srv.URL + `","rerank_top_k":2,"rerank_ranking":"zscore","rerank_score_space":"logit","rerank_multi_passage_top_n":2,"rerank_multi_passage_mode":"lead_gated"}`))
	mk := func(rec string, score float64, body string) *recordBundle {
		l := quivrplugin.Candidate{SegmentID: rec + "l", RecordID: rec, PartKey: "body", Start: 0, End: 100, Text: "Ouverture " + rec, Score: score}
		d := quivrplugin.Candidate{SegmentID: rec + "d", RecordID: rec, PartKey: "body", Start: 900, End: 1000, Text: body, Score: score}
		return &recordBundle{RecordID: rec, BestSeg: d, HasBestSeg: true, LeadSeg: &l, FinalScore: score}
	}
	bundles := []*recordBundle{mk("X", 0.9, "rien"), mk("Y", 0.8, "chute du trafic"), mk("Z", 0.1, "tail")}
	out, err := ret.applyDeepRerank(context.Background(), req, cfg, ttime(), bundles)
	if err != nil {
		t.Fatal(err)
	}
	if gotTexts != 4 {
		t.Fatalf("expected 2 lead views + 2 body views, got %d texts", gotTexts)
	}
	if out[0].RecordID != "Y" || out[2].RecordID != "Z" {
		t.Fatalf("body evidence should lift Y above X while the tail stays put: %s %s %s", out[0].RecordID, out[1].RecordID, out[2].RecordID)
	}
	if !(out[1].FinalScore > out[2].FinalScore) {
		t.Fatalf("head must stay strictly above tail")
	}
}

func ttime() time.Time { return time.Now() }

func TestShardedRerankMergesInDocumentOrder(t *testing.T) {
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
		t.Fatalf("expected 3 sharded reranker HTTP calls, got %d", got)
	}
	for i := range docs {
		if s.Raw[i] != float64(i) {
			t.Fatalf("shard merge out of order: %v", s.Raw)
		}
	}
}

func TestRecallBandStraddlesCut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Texts []string `json:"texts"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out := make([]map[string]any, len(body.Texts))
		for i, txt := range body.Texts {
			margin := -3.0
			if strings.Contains(txt, "pertinent") {
				margin = 5.0
			}
			out[i] = map[string]any{"index": i, "score": 1 / (1 + math.Exp(-margin)), "raw_score": margin}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	ret := retriever{client: srv.Client()}
	req := &quivrplugin.SearchRequest{Profile: "deep", Round: 2, Limit: 10}
	req.Query.Text = "q"
	req.Query.Mode = "hybrid"
	cfg, _ := readConfig(json.RawMessage(`{"rerank_endpoint":"` + srv.URL + `","rerank_top_k":2,"rerank_ranking":"zscore","rerank_score_space":"logit","rerank_band_start":1,"rerank_band_end":4,"rerank_band_weight":0.9}`))
	mk := func(rec string, score float64, text string) *recordBundle {
		l := quivrplugin.Candidate{SegmentID: rec, RecordID: rec, PartKey: "body", Start: 0, End: 100, Text: text, Score: score}
		return &recordBundle{RecordID: rec, BestSeg: l, HasBestSeg: true, LeadSeg: &l, FinalScore: score}
	}
	bundles := []*recordBundle{mk("A", 0.9, "pertinent A"), mk("B", 0.8, "hors sujet"), mk("C", 0.7, "hors sujet"), mk("D", 0.6, "pertinent D")}
	out, err := ret.applyDeepRerank(context.Background(), req, cfg, ttime(), bundles)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].RecordID != "A" || out[1].RecordID != "D" {
		t.Fatalf("band record D should cross into position 2: %s %s %s %s", out[0].RecordID, out[1].RecordID, out[2].RecordID, out[3].RecordID)
	}
	for i := 1; i < len(out); i++ {
		if out[i].FinalScore > out[i-1].FinalScore {
			t.Fatalf("scores must stay monotonic")
		}
	}
}

func TestSurrogateBudgetWithoutTitle(t *testing.T) {
	lead := quivrplugin.Candidate{SegmentID: "l", RecordID: "R", PartKey: "body", Start: 0, End: 1900, Text: strings.Repeat("a", 1900), Score: 9}
	deep := quivrplugin.Candidate{SegmentID: "d", RecordID: "R", PartKey: "body", Start: 1228, End: 2019, Text: strings.Repeat("b", 800), Score: 0.7}
	b := &recordBundle{RecordID: "R", BestSeg: deep, HasBestSeg: true, LeadSeg: &lead}
	if n := len([]rune(buildRichSurrogate(b, 2200, 1200))); n < 1900 || n > 2300 {
		t.Fatalf("rich surrogate must use its full budget without a title, got %d runes", n)
	}
	if n := len([]rune(buildLeadView(b, 2000))); n < 1800 {
		t.Fatalf("lead view too short: %d", n)
	}
	if bv, _ := buildBodyView(b, 2000); len([]rune(bv)) < 790 {
		t.Fatalf("body view too short: %d", len([]rune(bv)))
	}
}
