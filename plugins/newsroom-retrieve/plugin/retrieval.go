package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

const responseMargin = 50 * time.Millisecond

const (
	legDense     = "dense"
	legLexical   = "lex"
	legDenseView = "dense_view"
	legLexView   = "lex_view"
)

type namedRequest struct {
	Name string
	Req  quivrplugin.CandidateRequest
}

type extraLeg struct {
	Name   string
	Weight float64
	Cands  []quivrplugin.Candidate
}

type configuration struct {
	DenseK                  int     `json:"dense_k"`
	LexicalK                int     `json:"lexical_k"`
	CleanLexicalQuery       bool    `json:"clean_lexical_query"`
	Fusion                  string  `json:"fusion"`
	RRFK                    float64 `json:"rrf_k"`
	DenseWeight             float64 `json:"dense_weight"`
	LexicalWeight           float64 `json:"lexical_weight"`
	DenseViewMode           string  `json:"dense_view_mode"`
	DenseViewWeight         float64 `json:"dense_view_weight"`
	DenseViewGrouped        bool    `json:"dense_view_grouped"`
	LexicalViewMode         string  `json:"lexical_view_mode"`
	LexicalViewWeight       float64 `json:"lexical_view_weight"`
	TitleLeadWeight         float64 `json:"title_lead_weight"`
	LeadChars               int     `json:"lead_chars"`
	PassageRatioWeight      float64 `json:"passage_ratio_weight"`
	PassageSupportWeight    float64 `json:"passage_support_weight"`
	RerankEndpoint          string  `json:"rerank_endpoint"`
	RerankProtocol          string  `json:"rerank_protocol"`
	RerankModel             string  `json:"rerank_model"`
	RerankInstruction       string  `json:"rerank_instruction"`
	RerankTopK              int     `json:"rerank_top_k"`
	RerankScoreSpace        string  `json:"rerank_score_space"`
	RerankRanking           string  `json:"rerank_ranking"`
	RerankWeight            float64 `json:"rerank_weight"`
	RerankMaxDocChars       int     `json:"rerank_max_doc_chars"`
	RerankTimeoutMS         int     `json:"rerank_timeout_ms"`
	RerankParallelRequests  int     `json:"rerank_parallel_requests"`
	RerankBandStart         int     `json:"rerank_band_start"`
	RerankBandEnd           int     `json:"rerank_band_end"`
	RerankBandMaxChars      int     `json:"rerank_band_max_chars"`
	RerankBandWeight        float64 `json:"rerank_band_weight"`
	FallbackOnError         *bool   `json:"fallback_on_error"`
	AdaptiveCascade         bool    `json:"adaptive_cascade"`
	CascadeJaccardThreshold float64 `json:"cascade_jaccard_threshold"`
	CascadeMarginThreshold  float64 `json:"cascade_margin_threshold"`
	CascadeDGapThreshold    float64 `json:"cascade_dgap_threshold"`
}

func (c configuration) fallbackOnError() bool {
	if c.FallbackOnError == nil {
		return true
	}
	return *c.FallbackOnError
}

// readConfig returns the high-performance Grand Champion defaults out of the
// box; callers override individual fields to opt out for lighter cost/latency.
func readConfig(raw json.RawMessage) (configuration, error) {
	cfg := configuration{
		DenseK:                  100,
		LexicalK:                60,
		CleanLexicalQuery:       true,
		Fusion:                  "convex",
		RRFK:                    60,
		DenseWeight:             0.65,
		LexicalWeight:           0.35,
		DenseViewMode:           "question",
		DenseViewWeight:         0.16,
		DenseViewGrouped:        true,
		LexicalViewMode:         "question",
		LexicalViewWeight:       0.10,
		TitleLeadWeight:         0.12,
		LeadChars:               250,
		PassageRatioWeight:      0.10,
		PassageSupportWeight:    0.12,
		RerankProtocol:          "tei",
		RerankTopK:              50,
		RerankScoreSpace:        "logit",
		RerankRanking:           "zscore",
		RerankWeight:            0.90,
		RerankMaxDocChars:       2000,
		RerankTimeoutMS:         12000,
		RerankParallelRequests:  2,
		RerankBandStart:         30,
		RerankBandEnd:           80,
		RerankBandMaxChars:      900,
		RerankBandWeight:        0.85,
		AdaptiveCascade:         false,
		CascadeJaccardThreshold: 0.50,
		CascadeMarginThreshold:  0.15,
		CascadeDGapThreshold:    2.25,
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, quivrplugin.TerminalSearchError("invalid_configuration", err.Error())
		}
	}
	if cfg.RerankEndpoint != "" {
		u, err := url.Parse(cfg.RerankEndpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return cfg, quivrplugin.TerminalSearchError("invalid_configuration", "rerank_endpoint must be an absolute HTTP(S) URL without embedded credentials or fragment")
		}
	}
	return cfg, nil
}

type retriever struct {
	client *http.Client
	apiKey string
}

// New creates a newsroom retriever for the Quivr plugin SDK.
func New() quivrplugin.Retriever {
	return retriever{
		client: newRerankHTTPClient(),
		apiKey: os.Getenv("RERANKER_API_KEY"),
	}
}

func (r retriever) Search(ctx context.Context, req *quivrplugin.SearchRequest) (*quivrplugin.SearchAnswer, error) {
	started := time.Now()
	cfg, err := readConfig(req.Configuration)
	if err != nil {
		return nil, err
	}

	if req.Round == 1 {
		reqs, err := buildRound1Requests(req, cfg)
		if err != nil {
			return nil, err
		}
		return quivrplugin.Ask(reqs...), nil
	}

	specs, _ := round1Specs(req, cfg)
	var denseUngrouped, lexicalFull []quivrplugin.Candidate
	extraByName := map[string]*extraLeg{}
	addExtra := func(name string, weight float64, cands []quivrplugin.Candidate) {
		if weight <= 0 || len(cands) == 0 {
			return
		}
		cp := make([]quivrplugin.Candidate, len(cands))
		copy(cp, cands)
		if el, ok := extraByName[name]; ok {
			el.Cands = append(el.Cands, cp...)
			return
		}
		extraByName[name] = &extraLeg{Name: name, Weight: weight, Cands: cp}
	}
	for _, s := range req.Served {
		if s.Round != 1 {
			continue
		}
		name := ""
		if s.RequestIndex >= 0 && s.RequestIndex < len(specs) && specs[s.RequestIndex].Req.Primitive == s.Request.Primitive &&
			strings.TrimSpace(specs[s.RequestIndex].Req.QueryText) == strings.TrimSpace(s.Request.QueryText) {
			name = specs[s.RequestIndex].Name
		}
		switch name {
		case legDense:
			denseUngrouped = append(denseUngrouped, s.Candidates...)
		case legLexical:
			lexicalFull = s.Candidates
		case legDenseView:
			addExtra(legDenseView, cfg.DenseViewWeight, s.Candidates)
		case legLexView:
			addExtra(legLexView, cfg.LexicalViewWeight, s.Candidates)
		default:
			switch s.Request.Primitive {
			case quivrplugin.PrimitiveNearVector:
				denseUngrouped = append(denseUngrouped, s.Candidates...)
			case quivrplugin.PrimitiveBM25:
				if len(lexicalFull) == 0 {
					lexicalFull = s.Candidates
				}
			}
		}
	}

	extras := make([]extraLeg, 0, len(extraByName))
	for _, name := range []string{legDenseView, legLexView} {
		if el, ok := extraByName[name]; ok {
			extras = append(extras, *el)
		}
	}

	bundles, denseForQPP := fuseAndScore(req, cfg, denseUngrouped, lexicalFull, extras)
	if len(bundles) == 0 {
		return quivrplugin.Rank(), nil
	}

	if req.Profile == "deep" && cfg.RerankEndpoint != "" && req.Query.Mode != "lexical" {
		skipRerank := false
		if cfg.AdaptiveCascade {
			conf := computeStage1Confidence(denseForQPP, lexicalFull, bundles)
			skipRerank = conf.Jaccard10 >= cfg.CascadeJaccardThreshold &&
				conf.ScoreMargin >= cfg.CascadeMarginThreshold &&
				conf.DenseGap >= cfg.CascadeDGapThreshold
		}
		if !skipRerank {
			reranked, rerr := r.applyDeepRerank(ctx, req, cfg, started, bundles)
			if rerr != nil {
				return nil, rerr
			}
			bundles = reranked
		}
	}

	limit := req.Limit
	if limit <= 0 || limit > len(bundles) {
		limit = len(bundles)
	}
	ranked := make([]quivrplugin.RankedHit, limit)
	for i := 0; i < limit; i++ {
		b := bundles[i]
		ranked[i] = quivrplugin.RankedHit{
			SegmentID:   b.BestSeg.SegmentID,
			Score:       b.FinalScore,
			Explanation: b.Explanation,
		}
	}
	return quivrplugin.Rank(ranked...), nil
}

func round1Specs(req *quivrplugin.SearchRequest, cfg configuration) ([]namedRequest, error) {
	mode := req.Query.Mode
	if mode == "" {
		mode = "hybrid"
	}
	space := req.ServedSpace()
	if mode == "semantic" && space == nil {
		return nil, quivrplugin.TerminalSearchError("missing_served_space", "semantic search requires a served vector space")
	}

	lexK := clampK(cfg.LexicalK, req.Limit)
	denseK := clampK(cfg.DenseK, req.Limit)

	var specs []namedRequest
	if (mode == "semantic" || mode == "hybrid") && space != nil {
		specs = append(specs, namedRequest{
			Name: legDense,
			Req: quivrplugin.CandidateRequest{
				Primitive: quivrplugin.PrimitiveNearVector,
				QueryText: req.Query.Text,
				Space:     space.ID,
				K:         denseK,
			},
		})
		if viewQ, ok := denseViewQuery(req.Query.Text, cfg); ok {
			gb := ""
			if cfg.DenseViewGrouped {
				gb = "record"
			}
			specs = append(specs, namedRequest{
				Name: legDenseView,
				Req: quivrplugin.CandidateRequest{
					Primitive: quivrplugin.PrimitiveNearVector,
					QueryText: viewQ,
					Space:     space.ID,
					K:         denseK,
					GroupBy:   gb,
				},
			})
		}
	}
	if mode == "lexical" || mode == "hybrid" {
		lexQuery := req.Query.Text
		if cfg.CleanLexicalQuery {
			lexQuery = cleanLexicalQuery(req.Query.Text)
		}
		specs = append(specs, namedRequest{
			Name: legLexical,
			Req: quivrplugin.CandidateRequest{
				Primitive: quivrplugin.PrimitiveBM25,
				QueryText: lexQuery,
				Field:     quivrplugin.FieldSource,
				K:         lexK,
				GroupBy:   "record",
			},
		})
		if viewLex, ok := lexicalViewQuery(req.Query.Text, cfg); ok {
			specs = append(specs, namedRequest{
				Name: legLexView,
				Req: quivrplugin.CandidateRequest{
					Primitive: quivrplugin.PrimitiveBM25,
					QueryText: viewLex,
					Field:     quivrplugin.FieldSource,
					K:         lexK,
					GroupBy:   "record",
				},
			})
		}
	}
	return specs, nil
}

func buildRound1Requests(req *quivrplugin.SearchRequest, cfg configuration) ([]quivrplugin.CandidateRequest, error) {
	specs, err := round1Specs(req, cfg)
	if err != nil {
		return nil, err
	}
	reqs := make([]quivrplugin.CandidateRequest, len(specs))
	for i, s := range specs {
		reqs[i] = s.Req
	}
	return reqs, nil
}

func clampK(configured, limit int) int {
	k := configured
	if k <= 0 {
		k = 50
	}
	if limit > k {
		k = limit
	}
	if k > 100 {
		k = 100
	}
	return k
}

func fuseAndScore(
	req *quivrplugin.SearchRequest,
	cfg configuration,
	denseUngrouped, lexicalFull []quivrplugin.Candidate,
	extras []extraLeg,
) ([]*recordBundle, []quivrplugin.Candidate) {
	byRecord := map[string]*recordBundle{}
	// Modality-safe bundle lookup: dense passages always take precedence over
	// lexical passages for LeadSeg/DeepBestSeg so raw BM25 scores (>1) never
	// overwrite dense cosine scores ([0,1]).
	getBundle := func(c quivrplugin.Candidate, fromDense bool) *recordBundle {
		key := recordKey(c)
		b, ok := byRecord[key]
		if !ok {
			b = &recordBundle{RecordID: key}
			byRecord[key] = b
		}
		if isLeadSegment(c) {
			replace := b.LeadSeg == nil ||
				(fromDense && !b.LeadFromDense) ||
				(fromDense == b.LeadFromDense && (c.Score > b.LeadSeg.Score || (c.Score == b.LeadSeg.Score && c.SegmentID < b.LeadSeg.SegmentID)))
			if replace {
				cp := c
				b.LeadSeg = &cp
				b.LeadFromDense = fromDense
			}
		} else {
			replace := b.DeepBestSeg == nil ||
				(fromDense && !b.DeepFromDense) ||
				(fromDense == b.DeepFromDense && (c.Score > b.DeepBestSeg.Score || (c.Score == b.DeepBestSeg.Score && c.SegmentID < b.DeepBestSeg.SegmentID)))
			if replace {
				cp := c
				b.DeepBestSeg = &cp
				b.DeepFromDense = fromDense
			}
		}
		return b
	}

	// Primary ungrouped dense leg: tracks FirstP/MaxP, PassageCount, BestSeg, SecondSeg, and floor.
	ungrpFloor := 0.0
	if len(denseUngrouped) > 0 {
		ungrpFloor = denseUngrouped[0].Score
		seenSeg := make(map[string]bool, len(denseUngrouped))
		for _, c := range denseUngrouped {
			if c.Score < ungrpFloor {
				ungrpFloor = c.Score
			}
			if seenSeg[c.SegmentID] {
				continue
			}
			seenSeg[c.SegmentID] = true
			b := getBundle(c, true)
			b.PassageCount++
			if !b.HasUngrouped || c.Score > b.MaxP || (c.Score == b.MaxP && c.SegmentID < b.BestSeg.SegmentID) {
				if b.HasUngrouped && b.HasBestSeg && b.BestSeg.SegmentID != c.SegmentID {
					if b.SecondSeg == nil || b.BestSeg.Score > b.SecondSeg.Score || (b.BestSeg.Score == b.SecondSeg.Score && b.BestSeg.SegmentID < b.SecondSeg.SegmentID) {
						prev := b.BestSeg
						b.SecondSeg = &prev
					}
				}
				b.MaxP = c.Score
				b.HasUngrouped = true
				b.DenseScore = c.Score
				b.HasDense = true
				b.BestSeg = c
				b.HasBestSeg = true
			} else if b.SecondSeg == nil || c.Score > b.SecondSeg.Score || (c.Score == b.SecondSeg.Score && c.SegmentID < b.SecondSeg.SegmentID) {
				cp := c
				b.SecondSeg = &cp
			}
			if isLeadSegment(c) && (!b.HasFirstP || c.Score > b.FirstP) {
				b.FirstP = c.Score
				b.HasFirstP = true
			}
		}
	}

	// Build deduplicated primary dense ranking for leg stats & QPP.
	var densePrimary []quivrplugin.Candidate
	if len(denseUngrouped) > 0 {
		dedup := make([]*recordBundle, 0, len(byRecord))
		for _, b := range byRecord {
			if b.HasDense {
				dedup = append(dedup, b)
			}
		}
		sort.Slice(dedup, func(i, j int) bool {
			if dedup[i].DenseScore != dedup[j].DenseScore {
				return dedup[i].DenseScore > dedup[j].DenseScore
			}
			return dedup[i].RecordID < dedup[j].RecordID
		})
		densePrimary = make([]quivrplugin.Candidate, len(dedup))
		for i, b := range dedup {
			b.DenseRank = i + 1
			c := b.BestSeg
			c.Score = b.DenseScore
			densePrimary[i] = c
		}
	}
	denseStats := computeLegStats(densePrimary)

	// Primary BM25 leg.
	lexStats := computeLegStats(lexicalFull)
	for i, c := range lexicalFull {
		b := getBundle(c, false)
		if !b.HasLexical || c.Score > b.LexicalScore {
			b.LexicalScore = c.Score
			b.LexicalRank = i + 1
			b.HasLexical = true
		}
		if !b.HasBestSeg {
			b.BestSeg = c
			b.HasBestSeg = true
		}
	}

	// Secondary query-view legs (question-stripped dense & BM25).
	extraStats := make([]legStats, len(extras))
	for li, el := range extras {
		fromDense := el.Name == legDenseView
		records := dedupByRecord(el.Cands)
		extraStats[li] = computeLegStats(records)
		for i, c := range records {
			b := getBundle(c, fromDense)
			if b.Extra == nil {
				b.Extra = map[string]legHit{}
			}
			if h, ok := b.Extra[el.Name]; !ok || c.Score > h.Score {
				b.Extra[el.Name] = legHit{Score: c.Score, Rank: i + 1}
			}
			if !b.HasBestSeg {
				b.BestSeg = c
				b.HasBestSeg = true
			}
		}
	}

	bundles := make([]*recordBundle, 0, len(byRecord))
	for _, b := range byRecord {
		if b.HasBestSeg {
			bundles = append(bundles, b)
		}
	}
	sort.Slice(bundles, func(i, j int) bool {
		return bundles[i].RecordID < bundles[j].RecordID
	})

	// Stage 1: Calibrated multi-leg fusion.
	activeWeightSum := 0.0
	if len(densePrimary) > 0 {
		activeWeightSum += cfg.DenseWeight
	}
	if len(lexicalFull) > 0 {
		activeWeightSum += cfg.LexicalWeight
	}
	for _, el := range extras {
		if len(el.Cands) > 0 && el.Weight > 0 {
			activeWeightSum += el.Weight
		}
	}
	if activeWeightSum <= 0 {
		activeWeightSum = 1.0
	}

	for _, b := range bundles {
		fused := 0.0
		legs := 0
		if b.HasDense {
			fused += cfg.DenseWeight * normalizeLegScore(b.DenseScore, b.DenseRank, denseStats, cfg.Fusion, cfg.RRFK)
			legs++
		}
		if b.HasLexical {
			fused += cfg.LexicalWeight * normalizeLegScore(b.LexicalScore, b.LexicalRank, lexStats, cfg.Fusion, cfg.RRFK)
			legs++
		}
		for li, el := range extras {
			if h, ok := b.Extra[el.Name]; ok && el.Weight > 0 {
				fused += el.Weight * normalizeLegScore(h.Score, h.Rank, extraStats[li], cfg.Fusion, cfg.RRFK)
				legs++
			}
		}
		if cfg.Fusion != "rrf" {
			fused /= activeWeightSum
		}
		b.LegsMatched = legs
		b.Stage1Score = fused
		b.FinalScore = fused
	}

	// Stage 2: Title + Lead pool-IDF aspect coverage (single-pass tokenization).
	if cfg.TitleLeadWeight > 0 {
		aspects := queryAspects(req.Query.Text)
		if len(aspects) > 0 {
			titleLeadSets := make([]map[string]bool, len(bundles))
			for i, b := range bundles {
				t, l := extractTitleAndLead(b, cfg.LeadChars)
				if t != "" && l != "" {
					titleLeadSets[i] = tokenSetOf(t + " " + l)
				} else if t != "" {
					titleLeadSets[i] = tokenSetOf(t)
				} else if l != "" {
					titleLeadSets[i] = tokenSetOf(l)
				}
			}
			idfs := computePoolIDF(aspects, titleLeadSets)
			for i, b := range bundles {
				b.FinalScore += cfg.TitleLeadWeight * scoreAspectCoverage(aspects, idfs, titleLeadSets[i])
			}
		}
	}

	// Stage 3: Inverted-pyramid FirstP/MaxP ratio & multi-passage corroboration.
	if cfg.PassageRatioWeight > 0 || cfg.PassageSupportWeight > 0 {
		for _, b := range bundles {
			if cfg.PassageRatioWeight > 0 {
				var ratio float64
				switch {
				case b.HasUngrouped && b.HasFirstP && b.MaxP > 0:
					ratio = clamp01(b.FirstP / b.MaxP)
				case b.HasUngrouped && !b.HasFirstP && b.MaxP > 0:
					ratio = clamp01((ungrpFloor * 0.95) / b.MaxP)
				default:
					if isLeadSegment(b.BestSeg) || b.LeadSeg != nil {
						ratio = 0.85
					} else {
						ratio = 0.35
					}
				}
				b.FinalScore += cfg.PassageRatioWeight * ratio
			}
			if cfg.PassageSupportWeight > 0 && b.PassageCount >= 2 {
				support := clamp01(math.Log2(float64(b.PassageCount)) / 2.0)
				b.FinalScore += cfg.PassageSupportWeight * support
			}
		}
	}

	for _, b := range bundles {
		b.Explanation = fmt.Sprintf("newsroom(%s,legs=%d)", cfg.Fusion, b.LegsMatched)
	}
	sortBundlesDeterministic(bundles)
	return bundles, densePrimary
}

func (r retriever) applyDeepRerank(
	ctx context.Context,
	req *quivrplugin.SearchRequest,
	cfg configuration,
	started time.Time,
	bundles []*recordBundle,
) ([]*recordBundle, error) {
	topK := min(cfg.RerankTopK, len(bundles))
	if topK <= 0 {
		return bundles, nil
	}
	head := bundles[:topK]
	tail := bundles[topK:]

	deadline := time.Now().Add(time.Duration(cfg.RerankTimeoutMS) * time.Millisecond)
	if req.Budget != nil {
		budgetDeadline := started.Add(time.Duration(req.Budget.RemainingTimeMS)*time.Millisecond - responseMargin)
		if budgetDeadline.Before(deadline) {
			deadline = budgetDeadline
		}
	}
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Add(-responseMargin).Before(deadline) {
		deadline = parentDeadline.Add(-responseMargin)
	}
	if time.Until(deadline) <= 0 {
		if cfg.fallbackOnError() {
			return bundles, nil
		}
		return nil, &quivrplugin.SearchError{Code: "reranker_unavailable", Message: "reranker budget exhausted", Retryable: true}
	}
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	preRerank := make(map[*recordBundle]float64, len(bundles))
	for _, b := range bundles {
		preRerank[b] = b.FinalScore
	}

	docs := make([]string, len(head))
	for i, b := range head {
		docs[i] = buildSurrogate(b, cfg.RerankMaxDocChars)
	}
	bandN := 0
	if cfg.RerankBandEnd > topK {
		bandN = min(cfg.RerankBandEnd-topK, len(tail))
	}
	bandStart := len(docs)
	for i := 0; i < bandN; i++ {
		docs = append(docs, buildLeadView(tail[i], cfg.RerankBandMaxChars))
	}

	rs, err := r.callRerankerSharded(callCtx, cfg, req.Query.Text, docs, cfg.RerankParallelRequests)
	if err != nil {
		if cfg.fallbackOnError() {
			return bundles, nil
		}
		return nil, &quivrplugin.SearchError{Code: "reranker_unavailable", Message: err.Error(), Retryable: true}
	}
	all := rs.Prob
	if cfg.RerankScoreSpace == "logit" {
		all = rs.logitSpace()
	}
	scores := all[:len(head)]

	reranked := make([]*recordBundle, len(head))
	copy(reranked, head)

	switch cfg.RerankRanking {
	case "rrf":
		order := make([]int, len(scores))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(i, j int) bool { return scores[order[i]] > scores[order[j]] })
		rrank := make([]int, len(scores))
		for pos, idx := range order {
			rrank[idx] = pos + 1
		}
		for i, b := range reranked {
			b.FinalScore = 1.0/float64(61+i) + 1.0/float64(60+rrank[i])
			b.Explanation = "newsroom+deep(rrf)"
		}
	case "zscore":
		baseScores := make([]float64, len(reranked))
		for i, b := range reranked {
			baseScores[i] = b.FinalScore
		}
		zb := zScores(baseScores)
		zr := zScores(scores)
		for i, b := range reranked {
			b.FinalScore = cfg.RerankWeight*zr[i] + (1.0-cfg.RerankWeight)*zb[i]
			b.Explanation = "newsroom+deep(zscore)"
		}
	case "linear":
		baseScores := make([]float64, len(reranked))
		for i, b := range reranked {
			baseScores[i] = b.FinalScore
		}
		nb := minMaxNormalize(baseScores)
		nr := minMaxNormalize(scores)
		for i, b := range reranked {
			b.FinalScore = cfg.RerankWeight*nr[i] + (1.0-cfg.RerankWeight)*nb[i]
			b.Explanation = "newsroom+deep(linear)"
		}
	default: // rerank_only
		for i, b := range reranked {
			b.FinalScore = scores[i]
			b.Explanation = "newsroom+deep(rerank_only)"
		}
	}
	sortBundlesDeterministic(reranked)

	// Shift head strictly above untouched Stage-1 tail so Recall@50 is never truncated.
	if len(tail) > 0 && len(reranked) > 0 {
		minHead := reranked[len(reranked)-1].FinalScore
		maxTail := tail[0].FinalScore
		if minHead <= maxTail {
			shift := (maxTail - minHead) + 1.0
			for _, b := range reranked {
				b.FinalScore += shift
			}
		}
	}
	out := append(reranked, tail...)

	// Recall Band: re-sort [band_start, topK+bandN) straddling the Recall@50 cut
	// in the configured score space while keeping the top head untouched.
	if bandN > 0 {
		bs := cfg.RerankBandStart
		if bs <= 0 || bs > topK {
			bs = topK
		}
		be := topK + bandN
		scoreOf := make(map[*recordBundle]float64, len(head)+bandN)
		for i, b := range head {
			scoreOf[b] = all[i]
		}
		for i := 0; i < bandN; i++ {
			scoreOf[tail[i]] = all[bandStart+i]
		}
		region := out[bs:be]
		origScores := make([]float64, len(region))
		rv := make([]float64, len(region))
		sv := make([]float64, len(region))
		for i, b := range region {
			origScores[i] = b.FinalScore
			rv[i] = scoreOf[b]
			sv[i] = preRerank[b]
		}
		zr, zs := zScores(rv), zScores(sv)
		key := make(map[*recordBundle]float64, len(region))
		for i, b := range region {
			key[b] = cfg.RerankBandWeight*zr[i] + (1-cfg.RerankBandWeight)*zs[i]
		}
		sort.SliceStable(region, func(i, j int) bool {
			if key[region[i]] != key[region[j]] {
				return key[region[i]] > key[region[j]]
			}
			return region[i].RecordID < region[j].RecordID
		})
		for i, b := range region {
			b.FinalScore = origScores[i]
			if !strings.HasPrefix(b.Explanation, "newsroom+deep") {
				b.Explanation = "newsroom+deep(band)"
			}
		}
	}
	return out, nil
}
