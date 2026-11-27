package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

const responseMargin = 50 * time.Millisecond

type configuration struct {
	CandidateCount          int     `json:"candidate_count"`
	Fusion                  string  `json:"fusion"`
	RRFK                    float64 `json:"rrf_k"`
	DenseWeight             float64 `json:"dense_weight"`
	DenseCoreView           bool    `json:"dense_core_view"`
	DenseCoreMode           string  `json:"dense_core_mode"`
	DenseCoreWeight         float64 `json:"dense_core_weight"`
	DenseClauseSplitView    bool    `json:"dense_clause_split_view"`
	DenseEntityView         bool    `json:"dense_entity_view"`
	DenseEntityWeight       float64 `json:"dense_entity_weight"`
	DenseEntityMaxTerms     int     `json:"dense_entity_max_terms"`
	DenseViewFusion         string  `json:"dense_view_fusion"`
	DenseSoftMinLambda      float64 `json:"dense_soft_min_lambda"`
	Round2ConsensusWeight   float64 `json:"round2_consensus_weight"`
	Round2ConsensusTopN     int     `json:"round2_consensus_top_n"`
	Round2ConsensusDocs     int     `json:"round2_consensus_docs"`
	Round2MaxTerms          int     `json:"round2_max_terms"`
	Round2QueryBase         string  `json:"round2_query_base"`
	TemporalAlignWeight     float64 `json:"temporal_align_weight"`
	TemporalConflictPenalty float64 `json:"temporal_conflict_penalty"`
	DatelineEntityWeight    float64 `json:"dateline_entity_weight"`
	QueryAdaptiveAlpha      bool    `json:"query_adaptive_alpha"`
	LexicalWeight           float64 `json:"lexical_weight"`
	EntityLeg               bool    `json:"entity_leg"`
	EntityWeight            float64 `json:"entity_weight"`
	EntityMaxTerms          int     `json:"entity_max_terms"`
	CombMNZBonus            float64 `json:"combmnz_bonus"`
	AspectCoverageWeight    float64 `json:"aspect_coverage_weight"`
	MissingAspectPenalty    float64 `json:"missing_aspect_penalty"`
	CoveragePenaltyGamma    float64 `json:"coverage_penalty_gamma"`
	CoveragePenaltyRho      float64 `json:"coverage_penalty_rho"`
	ProximityWeight         float64 `json:"proximity_weight"`
	ProximityMode           string  `json:"proximity_mode"`
	ProximityWindow         int     `json:"proximity_window"`
	SalienceWeight          float64 `json:"salience_weight"`
	ExpSalienceWeight       float64 `json:"exp_salience_weight"`
	TailOnlyPenalty         float64 `json:"tail_only_penalty"`
	LeadChars               int     `json:"lead_chars"`
	DeepChunkPenalty        float64 `json:"deep_chunk_penalty"`
	PassageRatioWeight      float64 `json:"passage_ratio_weight"`
	PassageSupportWeight    float64 `json:"passage_support_weight"`
	RerankEndpoint          string  `json:"rerank_endpoint"`
	RerankProtocol          string  `json:"rerank_protocol"`
	RerankModel             string  `json:"rerank_model"`
	RerankTopK              int     `json:"rerank_top_k"`
	RerankRanking           string  `json:"rerank_ranking"`
	RerankWeight            float64 `json:"rerank_weight"`
	RerankMaxDocChars       int     `json:"rerank_max_doc_chars"`
	RerankTimeoutMS         int     `json:"rerank_timeout_ms"`
	SurrogateMode           string  `json:"surrogate_mode"`
	AdaptiveCascade         bool    `json:"adaptive_cascade"`
	CascadeJaccardThreshold float64 `json:"cascade_jaccard_threshold"`
	CascadeMarginThreshold  float64 `json:"cascade_margin_threshold"`
	CascadeDGapThreshold    float64 `json:"cascade_dgap_threshold"`
	LogDiagnostics          bool    `json:"log_diagnostics"`
}

func readConfig(raw json.RawMessage) (configuration, error) {
	cfg := configuration{
		CandidateCount:          100,
		Fusion:                  "dbsf",
		RRFK:                    60,
		DenseWeight:             0.65,
		DenseCoreView:           false,
		DenseCoreMode:           "subject",
		DenseCoreWeight:         0,
		DenseClauseSplitView:    false,
		DenseEntityView:         false,
		DenseEntityWeight:       0,
		DenseEntityMaxTerms:     8,
		DenseViewFusion:         "weighted",
		DenseSoftMinLambda:      0.35,
		Round2ConsensusWeight:   0,
		Round2ConsensusTopN:     10,
		Round2ConsensusDocs:     2,
		Round2MaxTerms:          3,
		Round2QueryBase:         "core",
		TemporalAlignWeight:     0,
		TemporalConflictPenalty: 0,
		DatelineEntityWeight:    0,
		LexicalWeight:           0.35,
		EntityLeg:               false,
		EntityWeight:            0.25,
		EntityMaxTerms:          6,
		CombMNZBonus:            0,
		AspectCoverageWeight:    0,
		MissingAspectPenalty:    0,
		CoveragePenaltyGamma:    0,
		CoveragePenaltyRho:      1.75,
		ProximityWeight:         0,
		ProximityMode:           "pairwise",
		ProximityWindow:         25,
		SalienceWeight:          0,
		ExpSalienceWeight:       0,
		TailOnlyPenalty:         0,
		LeadChars:               300,
		DeepChunkPenalty:        0,
		PassageRatioWeight:      0,
		PassageSupportWeight:    0,
		RerankProtocol:          "tei",
		RerankTopK:              30,
		RerankRanking:           "linear",
		RerankWeight:            0.85,
		RerankMaxDocChars:       2500,
		RerankTimeoutMS:         8000,
		SurrogateMode:           "inverted_pyramid",
		AdaptiveCascade:         false,
		CascadeJaccardThreshold: 0.50,
		CascadeMarginThreshold:  0.15,
		LogDiagnostics:          false,
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, quivrplugin.TerminalSearchError("invalid_configuration", err.Error())
		}
	}
	if cfg.RerankEndpoint != "" {
		u, err := url.Parse(cfg.RerankEndpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return cfg, quivrplugin.TerminalSearchError("invalid_configuration", "rerank_endpoint must be an absolute HTTP(S) URL")
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
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		apiKey: os.Getenv("RERANKER_API_KEY"),
	}
}

type denseViewSpec struct {
	Name   string
	Query  string
	Weight float64
}

func buildDenseViewSpecs(query string, cfg configuration) []denseViewSpec {
	full := strings.TrimSpace(query)
	specs := []denseViewSpec{{Name: "full", Query: full, Weight: 1.0}}
	seen := map[string]bool{strings.ToLower(full): true}
	addView := func(name, q string, w float64) {
		q = strings.TrimSpace(q)
		if q == "" {
			return
		}
		key := strings.ToLower(q)
		if seen[key] {
			return
		}
		seen[key] = true
		specs = append(specs, denseViewSpec{Name: name, Query: q, Weight: w})
	}

	if cfg.DenseClauseSplitView {
		if a, b, ok := extractClausePair(full); ok {
			w := cfg.DenseCoreWeight
			if w <= 0 {
				w = 0.50
			}
			addView("clause_a", a, w)
			addView("clause_b", b, w)
		}
	} else if cfg.DenseCoreView {
		if coreQ, ok := extractCoreQuery(full, cfg.DenseCoreMode); ok {
			w := cfg.DenseCoreWeight
			if w <= 0 {
				w = 0.50
			}
			addView("core", coreQ, w)
		}
	}
	if cfg.DenseEntityView {
		if entQ, ok := extractDenseEntityQuery(full, cfg.DenseEntityMaxTerms); ok {
			w := cfg.DenseEntityWeight
			if w <= 0 {
				w = 0.45
			}
			addView("entity_dense", entQ, w)
		}
	}
	return specs
}

func effectiveCandidateK(req *quivrplugin.SearchRequest, cfg configuration) int {
	k := cfg.CandidateCount
	if k <= 0 {
		k = 100
	}
	if k < req.Limit {
		k = req.Limit
	}
	if k > 100 {
		k = 100
	}
	return k
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

	// Parse served candidate legs from Round 1 (and optional Round 2).
	viewSpecs := buildDenseViewSpecs(req.Query.Text, cfg)
	specByQuery := make(map[string]denseViewSpec, len(viewSpecs))
	for _, sp := range viewSpecs {
		specByQuery[strings.ToLower(strings.TrimSpace(sp.Query))] = sp
	}

	var denseViews []denseViewLeg
	var denseFull, denseRound2, lexicalFull, lexicalEntity, denseUngrouped []quivrplugin.Candidate
	for _, s := range req.Served {
		if s.Round == 2 && s.Request.Primitive == quivrplugin.PrimitiveNearVector {
			denseRound2 = append(denseRound2, s.Candidates...)
			continue
		}
		if s.Round != 1 {
			continue
		}
		switch s.Request.Primitive {
		case quivrplugin.PrimitiveNearVector:
			if s.Request.GroupBy == "record" {
				qKey := strings.ToLower(strings.TrimSpace(s.Request.QueryText))
				sp, ok := specByQuery[qKey]
				if !ok {
					sp = denseViewSpec{Name: fmt.Sprintf("view_%d", len(denseViews)), Query: s.Request.QueryText, Weight: 1.0}
				}
				denseViews = append(denseViews, denseViewLeg{Name: sp.Name, Weight: sp.Weight, Cands: s.Candidates})
				if sp.Name == "full" || len(denseFull) == 0 {
					denseFull = s.Candidates
				}
			} else {
				denseUngrouped = append(denseUngrouped, s.Candidates...)
			}
		case quivrplugin.PrimitiveBM25:
			if len(lexicalFull) == 0 && strings.TrimSpace(s.Request.QueryText) == strings.TrimSpace(req.Query.Text) {
				lexicalFull = s.Candidates
			} else {
				lexicalEntity = s.Candidates
			}
		}
	}

	// Optional Round-2 conservative consensus dense expansion (zero LLM, max_rounds=3).
	if req.Round == 2 && cfg.Round2ConsensusWeight > 0 && req.Query.Mode != "lexical" {
		if space := req.ServedSpace(); space != nil {
			if r2Q, r2Terms, ok := buildRound2ConsensusQuery(req.Query.Text, cfg, denseFull, lexicalFull); ok {
				if cfg.LogDiagnostics {
					fmt.Fprintf(os.Stderr, "[newsroom-diag] round2_consensus terms=%v query=%q\n", r2Terms, r2Q)
				}
				return quivrplugin.Ask(quivrplugin.CandidateRequest{
					Primitive: quivrplugin.PrimitiveNearVector,
					QueryText: r2Q,
					Space:     space.ID,
					K:         effectiveCandidateK(req, cfg),
					GroupBy:   "record",
				}), nil
			}
		}
	}

	bundles := fuseAndScore(req, cfg, denseViews, denseRound2, lexicalFull, lexicalEntity, denseUngrouped)
	if len(bundles) == 0 {
		return quivrplugin.Rank(), nil
	}

	conf := computeStage1Confidence(denseFull, lexicalFull, bundles)
	if cfg.LogDiagnostics {
		viewSummary := make([]string, len(viewSpecs))
		for i, sp := range viewSpecs {
			viewSummary[i] = fmt.Sprintf("%s=%q", sp.Name, sp.Query)
		}
		fmt.Fprintf(os.Stderr,
			"[newsroom-diag] profile=%s mode=%s round=%d pool=%d dviews=%d r2=%d lex=%d ent=%d ungrp=%d jaccard10=%.3f margin=%.3f dgap=%.2f views=[%s]\n",
			req.Profile, req.Query.Mode, req.Round, len(bundles), len(denseViews), len(denseRound2), len(lexicalFull), len(lexicalEntity), len(denseUngrouped),
			conf.Jaccard10, conf.ScoreMargin, conf.DenseGap, strings.Join(viewSummary, "; "),
		)
	}

	// Deep profile: optional adaptive confidence cascade + inverted-pyramid surrogate reranking.
	if req.Profile == "deep" && cfg.RerankEndpoint != "" {
		skipRerank := cfg.AdaptiveCascade &&
			conf.Jaccard10 >= cfg.CascadeJaccardThreshold &&
			conf.ScoreMargin >= cfg.CascadeMarginThreshold &&
			conf.DenseGap >= cfg.CascadeDGapThreshold
		if !skipRerank {
			bundles = r.applyDeepRerank(ctx, req, cfg, started, bundles)
		} else if cfg.LogDiagnostics {
			fmt.Fprintf(os.Stderr, "[newsroom-diag] adaptive_cascade=EARLY_EXIT jaccard10=%.3f margin=%.3f dgap=%.2f\n", conf.Jaccard10, conf.ScoreMargin, conf.DenseGap)
		}
	}

	limit := min(len(bundles), req.Limit)
	hits := make([]quivrplugin.RankedHit, limit)
	for i := 0; i < limit; i++ {
		hits[i] = quivrplugin.RankedHit{
			SegmentID:   bundles[i].BestSeg.SegmentID,
			Score:       bundles[i].FinalScore,
			Explanation: bundles[i].Explanation,
		}
	}
	return quivrplugin.Rank(hits...), nil
}

func buildRound1Requests(req *quivrplugin.SearchRequest, cfg configuration) ([]quivrplugin.CandidateRequest, error) {
	k := effectiveCandidateK(req, cfg)

	var reqs []quivrplugin.CandidateRequest
	space := req.ServedSpace()

	if req.Query.Mode == "semantic" && space == nil {
		return nil, quivrplugin.TerminalSearchError("no_served_space", "the searched Corpora serve no vector space; search by keywords, or rebuild them")
	}

	// Leg A (+ optional Multi-Query Dense Views 2 & 3): grouped dense near_vector legs
	if req.Query.Mode != "lexical" && space != nil {
		for _, sp := range buildDenseViewSpecs(req.Query.Text, cfg) {
			reqs = append(reqs, quivrplugin.CandidateRequest{
				Primitive: quivrplugin.PrimitiveNearVector,
				QueryText: sp.Query,
				Space:     space.ID,
				K:         k,
				GroupBy:   "record",
			})
		}
	}

	// Leg B: Full-query lexical BM25 (grouped by record)
	if req.Query.Mode != "semantic" {
		reqs = append(reqs, quivrplugin.CandidateRequest{
			Primitive: quivrplugin.PrimitiveBM25,
			QueryText: req.Query.Text,
			Field:     quivrplugin.FieldSource,
			K:         k,
			GroupBy:   "record",
		})
	}

	// Leg C: Focused Entity / High-Specificity Sub-Query BM25
	if cfg.EntityLeg && req.Query.Mode != "semantic" {
		if subQ, ok := extractEntityQuery(req.Query.Text, cfg.EntityMaxTerms); ok && subQ != "" {
			reqs = append(reqs, quivrplugin.CandidateRequest{
				Primitive: quivrplugin.PrimitiveBM25,
				QueryText: subQ,
				Field:     quivrplugin.FieldSource,
				K:         k,
				GroupBy:   "record",
			})
		}
	}

	// Optional ungrouped dense request for Algorithm 3 (FirstP/MaxP ratio, multi-passage support, or deep inverted-pyramid surrogates)
	needUngrouped := cfg.PassageRatioWeight > 0 || cfg.PassageSupportWeight > 0 ||
		(req.Profile == "deep" && cfg.RerankEndpoint != "" && cfg.SurrogateMode == "inverted_pyramid")
	if needUngrouped && req.Query.Mode != "lexical" && space != nil {
		reqs = append(reqs, quivrplugin.CandidateRequest{
			Primitive: quivrplugin.PrimitiveNearVector,
			QueryText: req.Query.Text,
			Space:     space.ID,
			K:         k,
			GroupBy:   "",
		})
	}

	return reqs, nil
}

func fuseAndScore(
	req *quivrplugin.SearchRequest,
	cfg configuration,
	denseViews []denseViewLeg,
	denseRound2, lexicalFull, lexicalEntity, denseUngrouped []quivrplugin.Candidate,
) []*recordBundle {
	numViews := len(denseViews)
	byRecord := map[string]*recordBundle{}
	getBundle := func(c quivrplugin.Candidate) *recordBundle {
		key := recordKey(c)
		b, ok := byRecord[key]
		if !ok {
			b = &recordBundle{
				RecordID:        key,
				DenseViewScores: make([]float64, numViews),
				DenseViewRanks:  make([]int, numViews),
				HasDenseView:    make([]bool, numViews),
			}
			byRecord[key] = b
		}
		if isLeadSegment(c) {
			if b.LeadSeg == nil || c.Score > b.LeadSeg.Score {
				cp := c
				b.LeadSeg = &cp
			}
		} else {
			if b.DeepBestSeg == nil || c.Score > b.DeepBestSeg.Score {
				cp := c
				b.DeepBestSeg = &cp
			}
		}
		return b
	}

	// Ingest Multi-View Dense Legs (View 1 = full, optional View 2 = core/clause, View 3 = entity)
	viewStats := make([]legStats, numViews)
	anyDense := false
	for vIdx, view := range denseViews {
		viewStats[vIdx] = computeLegStats(view.Cands)
		if len(view.Cands) > 0 {
			anyDense = true
		}
		for i, c := range view.Cands {
			b := getBundle(c)
			if !b.HasDenseView[vIdx] || c.Score > b.DenseViewScores[vIdx] {
				b.DenseViewScores[vIdx] = c.Score
				b.DenseViewRanks[vIdx] = i + 1
				b.HasDenseView[vIdx] = true
			}
			if vIdx == 0 {
				if !b.HasDense || c.Score > b.DenseScore {
					b.DenseScore = c.Score
					b.DenseRank = i + 1
					b.HasDense = true
					b.BestSeg = c
					b.HasBestSeg = true
				}
			} else if !b.HasBestSeg {
				b.BestSeg = c
				b.HasBestSeg = true
			}
		}
	}

	// Ingest optional Round-2 consensus dense leg
	r2Stats := computeLegStats(denseRound2)
	for i, c := range denseRound2 {
		b := getBundle(c)
		if !b.HasDenseR2 || c.Score > b.DenseR2Score {
			b.DenseR2Score = c.Score
			b.DenseR2Rank = i + 1
			b.HasDenseR2 = true
		}
		if !b.HasBestSeg {
			b.BestSeg = c
			b.HasBestSeg = true
		}
	}

	// Ingest Leg B: full-query BM25
	lexStats := computeLegStats(lexicalFull)
	for i, c := range lexicalFull {
		b := getBundle(c)
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

	// Ingest Leg C: focused entity BM25
	entStats := computeLegStats(lexicalEntity)
	for i, c := range lexicalEntity {
		b := getBundle(c)
		if !b.HasEntity || c.Score > b.EntityScore {
			b.EntityScore = c.Score
			b.EntityRank = i + 1
			b.HasEntity = true
		}
		if !b.HasBestSeg {
			b.BestSeg = c
			b.HasBestSeg = true
		}
	}

	// Ingest ungrouped dense passages for FirstP/MaxP ratio & multi-passage surrogates.
	ungrpFloor := 0.0
	if len(denseUngrouped) > 0 {
		ungrpFloor = denseUngrouped[len(denseUngrouped)-1].Score
		seenSeg := map[string]bool{}
		for _, c := range denseUngrouped {
			if seenSeg[c.SegmentID] {
				continue
			}
			seenSeg[c.SegmentID] = true
			b := getBundle(c)
			b.PassageCount++
			if !b.HasUngrouped || c.Score > b.MaxP {
				if b.HasUngrouped && (b.SecondSeg == nil || c.Score > b.SecondSeg.Score) {
					prev := b.BestSeg
					b.SecondSeg = &prev
				}
				b.MaxP = c.Score
				b.HasUngrouped = true
			} else if b.SecondSeg == nil || c.Score > b.SecondSeg.Score {
				cp := c
				b.SecondSeg = &cp
			}
			if isLeadSegment(c) {
				if !b.HasFirstP || c.Score > b.FirstP {
					b.FirstP = c.Score
					b.HasFirstP = true
				}
			}
			if !b.HasBestSeg {
				b.BestSeg = c
				b.HasBestSeg = true
			}
		}
	}

	// Collect bundles in deterministic order before computing pool-IDF.
	bundles := make([]*recordBundle, 0, len(byRecord))
	for _, b := range byRecord {
		if b.HasBestSeg {
			bundles = append(bundles, b)
		}
	}
	sort.Slice(bundles, func(i, j int) bool {
		return bundles[i].RecordID < bundles[j].RecordID
	})

	// Stage 1: Multi-Leg Calibrated Fusion (Multi-View Dense + Round-2 Consensus + Lexical + Entity)
	effDenseW := cfg.DenseWeight
	effLexW := cfg.LexicalWeight
	effEntW := cfg.EntityWeight
	effR2W := cfg.Round2ConsensusWeight
	if cfg.QueryAdaptiveAlpha {
		ar := anchorRatio(req.Query.Text)
		shift := 0.15 * ar
		effDenseW = max(0.35, cfg.DenseWeight-shift)
		if len(lexicalEntity) > 0 {
			effEntW += shift * 0.6
			effLexW += shift * 0.4
		} else {
			effLexW += shift
		}
	}

	activeWeightSum := 0.0
	if anyDense {
		activeWeightSum += effDenseW
	}
	if len(denseRound2) > 0 && effR2W > 0 {
		activeWeightSum += effR2W
	}
	if len(lexicalFull) > 0 {
		activeWeightSum += effLexW
	}
	if len(lexicalEntity) > 0 {
		activeWeightSum += effEntW
	}
	if activeWeightSum <= 0 {
		activeWeightSum = 1.0
	}

	for _, b := range bundles {
		fused := 0.0
		legs := 0
		denseCombined, viewsMatched := combineDenseViews(b, denseViews, viewStats, cfg)
		if viewsMatched > 0 {
			fused += effDenseW * denseCombined
			legs += viewsMatched
		}
		if b.HasDenseR2 && effR2W > 0 {
			fused += effR2W * normalizeLegScore(b.DenseR2Score, b.DenseR2Rank, r2Stats, cfg.Fusion, cfg.RRFK)
			legs++
		}
		if b.HasLexical {
			fused += effLexW * normalizeLegScore(b.LexicalScore, b.LexicalRank, lexStats, cfg.Fusion, cfg.RRFK)
			legs++
		}
		if b.HasEntity {
			fused += effEntW * normalizeLegScore(b.EntityScore, b.EntityRank, entStats, cfg.Fusion, cfg.RRFK)
			legs++
		}
		// Normalize fused score to comparable [0, ~1] scale across 2-leg vs 3-leg configs.
		if cfg.Fusion != "rrf" {
			fused /= activeWeightSum
		}
		// Cross-modal CombMNZ: only reward consensus when dense AND lexical agree.
		if cfg.CombMNZBonus > 0 && (viewsMatched > 0 || b.HasDenseR2) && (b.HasLexical || b.HasEntity) {
			if b.HasLexical && b.HasEntity {
				fused *= 1.0 + cfg.CombMNZBonus
			} else {
				fused *= 1.0 + 0.5*cfg.CombMNZBonus
			}
		}
		b.LegsMatched = legs
		b.Stage1Score = fused
		b.FinalScore = fused
	}

	// Stage 2: Algorithmic $0 Candidate-Pool Signals (Aspect Coverage, Min-Span/SDM Proximity, Inverted-Pyramid Salience)
	needTextSignals := cfg.AspectCoverageWeight > 0 || cfg.MissingAspectPenalty > 0 ||
		cfg.CoveragePenaltyGamma > 0 || cfg.ProximityWeight > 0 || cfg.SalienceWeight > 0 ||
		cfg.ExpSalienceWeight > 0 || cfg.TailOnlyPenalty > 0
	if needTextSignals {
		aspects := queryAspects(req.Query.Text)
		querySeq := tokenizeContent(req.Query.Text)
		if len(aspects) > 0 {
			type docTextData struct {
				title, lead, early, deep string
				tokens                   []string
				tokenSet                 map[string]bool
			}
			docs := make([]docTextData, len(bundles))
			tokenSets := make([]map[string]bool, len(bundles))
			for i, b := range bundles {
				t, l, e, d, full := extractZoneTexts(b, cfg.LeadChars)
				toks := tokenizeContent(full)
				set := make(map[string]bool, len(toks))
				for _, tok := range toks {
					set[tok] = true
				}
				docs[i] = docTextData{title: t, lead: l, early: e, deep: d, tokens: toks, tokenSet: set}
				tokenSets[i] = set
			}
			idfs := computePoolIDF(aspects, tokenSets)

			for i, b := range bundles {
				cov := scoreAspectCoverage(aspects, idfs, docs[i].tokenSet)
				if cfg.AspectCoverageWeight > 0 {
					b.FinalScore += cfg.AspectCoverageWeight * cov
				}
				if cfg.MissingAspectPenalty > 0 && cov < 0.5 {
					b.FinalScore -= cfg.MissingAspectPenalty * ((0.5 - cov) * 2.0)
				}
				if cfg.CoveragePenaltyGamma > 0 {
					rho := cfg.CoveragePenaltyRho
					if rho < 1.0 {
						rho = 1.75
					}
					b.FinalScore -= cfg.CoveragePenaltyGamma * math.Pow(1.0-cov, rho)
				}
				if cfg.ProximityWeight > 0 {
					var prox float64
					if cfg.ProximityMode == "sdm_span" {
						prox = scoreSDMSpanProximity(querySeq, aspects, docs[i].tokens)
					} else {
						prox = scoreProximity(aspects, idfs, docs[i].tokens, cfg.ProximityWindow)
					}
					b.FinalScore += cfg.ProximityWeight * prox
				}
				if cfg.SalienceWeight > 0 {
					sal := scoreSalience(aspects, idfs, docs[i].title, docs[i].lead, docs[i].early, docs[i].deep)
					b.FinalScore += cfg.SalienceWeight * sal
				}
				if cfg.ExpSalienceWeight > 0 || cfg.TailOnlyPenalty > 0 {
					expSal, tailOnly := scoreExpSalience(aspects, idfs, b)
					if cfg.ExpSalienceWeight > 0 {
						b.FinalScore += cfg.ExpSalienceWeight * expSal
					}
					if cfg.TailOnlyPenalty > 0 && tailOnly {
						b.FinalScore -= cfg.TailOnlyPenalty
					}
				}
			}
		}
	}

	// Stage 2b: Generic Inline Wire Metadata ($0 Go) — Temporal/Numeric Exact Alignment & Wire Dateline/Opening Entity Boost
	if cfg.TemporalAlignWeight > 0 || cfg.TemporalConflictPenalty > 0 {
		qYears, qNums := extractYearsAndNumbers(req.Query.Text)
		if len(qYears) > 0 || len(qNums) > 0 {
			for _, b := range bundles {
				align, conflict := scoreTemporalAlignment(qYears, qNums, b, cfg.LeadChars)
				if cfg.TemporalAlignWeight > 0 && align > 0 {
					b.FinalScore += cfg.TemporalAlignWeight * align
				}
				if cfg.TemporalConflictPenalty > 0 && conflict > 0 {
					b.FinalScore -= cfg.TemporalConflictPenalty * conflict
				}
			}
		}
	}
	if cfg.DatelineEntityWeight > 0 {
		qEntities := extractQueryEntityStems(req.Query.Text)
		if len(qEntities) > 0 {
			for _, b := range bundles {
				boost := scoreDatelineEntityBoost(qEntities, b)
				if boost > 0 {
					b.FinalScore += cfg.DatelineEntityWeight * boost
				}
			}
		}
	}

	// Explicit deep-chunk penalty when winning segment starts past offset 0 and no lead segment was matched.
	if cfg.DeepChunkPenalty > 0 {
		for _, b := range bundles {
			if !isLeadSegment(b.BestSeg) && b.LeadSeg == nil {
				b.FinalScore -= cfg.DeepChunkPenalty
			}
		}
	}

	// Stage 3: Inverted-Pyramid Lead-vs-Body Aboutness Ratio (FirstP / MaxP) & Multi-Passage Support
	if cfg.PassageRatioWeight > 0 || cfg.PassageSupportWeight > 0 {
		for _, b := range bundles {
			if cfg.PassageRatioWeight > 0 {
				var ratio float64
				switch {
				case b.HasUngrouped && b.HasFirstP && b.MaxP > 0:
					// Lead passage retrieved in ungrouped dense: 1.0 for single-passage docs and lead-best docs.
					ratio = clamp01(b.FirstP / b.MaxP)
				case b.HasUngrouped && !b.HasFirstP && b.MaxP > 0:
					// Only deep body passages appeared in top-100 dense; bound FirstP by list floor.
					ratio = clamp01((ungrpFloor * 0.95) / b.MaxP)
				default:
					// Lexical-only candidate not in ungrouped dense top-100.
					if isLeadSegment(b.BestSeg) || b.LeadSeg != nil {
						ratio = 0.85
					} else {
						ratio = 0.35
					}
				}
				b.FinalScore += cfg.PassageRatioWeight * ratio
			}
			if cfg.PassageSupportWeight > 0 && b.PassageCount >= 2 {
				// Corroboration bonus for multi-passage hits (never penalizes single-passage docs).
				support := clamp01(math.Log2(float64(b.PassageCount)) / 2.0)
				b.FinalScore += cfg.PassageSupportWeight * support
			}
		}
	}

	for _, b := range bundles {
		b.Explanation = fmt.Sprintf("newsroom(%s,legs=%d)", cfg.Fusion, b.LegsMatched)
	}
	sortBundlesDeterministic(bundles)
	return bundles
}

func (r retriever) applyDeepRerank(
	ctx context.Context,
	req *quivrplugin.SearchRequest,
	cfg configuration,
	started time.Time,
	bundles []*recordBundle,
) []*recordBundle {
	topK := min(cfg.RerankTopK, len(bundles))
	if topK <= 0 {
		return bundles
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
		return bundles
	}
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	docs := make([]string, len(head))
	for i, b := range head {
		docs[i] = buildSurrogate(b, cfg.SurrogateMode, cfg.RerankMaxDocChars)
	}
	scores, err := r.callReranker(callCtx, cfg, req.Query.Text, docs)
	if err != nil {
		return bundles
	}

	// Score head according to rerank_ranking mode, keeping strict monotonic separation above untouched tail.
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
			b.Explanation = "newsroom+deep(rrf," + cfg.SurrogateMode + ")"
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
			b.Explanation = "newsroom+deep(linear," + cfg.SurrogateMode + ")"
		}
	default: // rerank_only
		for i, b := range reranked {
			b.FinalScore = scores[i]
			b.Explanation = "newsroom+deep(rerank_only," + cfg.SurrogateMode + ")"
		}
	}
	sortBundlesDeterministic(reranked)

	// Shift head scores strictly above tail so the untouched Stage-1 tail preserves Recall@50!
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
	return append(reranked, tail...)
}

func minMaxNormalize(vals []float64) []float64 {
	out := make([]float64, len(vals))
	if len(vals) == 0 {
		return out
	}
	lo, hi := slices.Min(vals), slices.Max(vals)
	if lo == hi {
		for i := range out {
			out[i] = 0.5
		}
		return out
	}
	for i, v := range vals {
		out[i] = (v - lo) / (hi - lo)
	}
	return out
}
