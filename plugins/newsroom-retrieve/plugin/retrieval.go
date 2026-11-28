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
	LexicalK                int     `json:"lexical_k"`
	DenseK                  int     `json:"dense_k"`
	DenseGrouped            bool    `json:"dense_grouped"`
	CleanLexicalQuery       bool    `json:"clean_lexical_query"`
	Fusion                  string  `json:"fusion"`
	RRFK                    float64 `json:"rrf_k"`
	DenseWeight             float64 `json:"dense_weight"`
	QueryAdaptiveAlpha      bool    `json:"query_adaptive_alpha"`
	LexicalWeight           float64 `json:"lexical_weight"`
	EntityLeg               bool               `json:"entity_leg"`
	EntityWeight            float64            `json:"entity_weight"`
	EntityMaxTerms          int                `json:"entity_max_terms"`
	FieldLexicalLeg         bool               `json:"field_lexical_leg"`
	FieldLexicalWeight      float64            `json:"field_lexical_weight"`
	FieldLexicalK           int                `json:"field_lexical_k"`
	PartKeyBoosts           map[string]float64 `json:"part_key_boosts"`
	CombMNZBonus            float64            `json:"combmnz_bonus"`
	TitleLeadWeight         float64 `json:"title_lead_weight"`
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
	RerankInstruction       string  `json:"rerank_instruction"`
	RerankTopK              int     `json:"rerank_top_k"`
	RerankRanking           string  `json:"rerank_ranking"`
	RerankWeight            float64 `json:"rerank_weight"`
	RerankMaxDocChars       int     `json:"rerank_max_doc_chars"`
	RerankTimeoutMS         int     `json:"rerank_timeout_ms"`
	FallbackOnError         *bool   `json:"fallback_on_error"`
	SurrogateMode           string  `json:"surrogate_mode"`
	AdaptiveCascade         bool    `json:"adaptive_cascade"`
	CascadeJaccardThreshold float64 `json:"cascade_jaccard_threshold"`
	CascadeMarginThreshold  float64 `json:"cascade_margin_threshold"`
	CascadeDGapThreshold    float64 `json:"cascade_dgap_threshold"`
	LogDiagnostics          bool    `json:"log_diagnostics"`
}

func (c configuration) fallbackOnError() bool {
	if c.FallbackOnError == nil {
		return true
	}
	return *c.FallbackOnError
}

func readConfig(raw json.RawMessage) (configuration, error) {
	cfg := configuration{
		CandidateCount:          100,
		LexicalK:                60,
		DenseK:                  100,
		DenseGrouped:            false,
		CleanLexicalQuery:       true,
		Fusion:                  "convex",
		RRFK:                    60,
		DenseWeight:             0.65,
		LexicalWeight:           0.35,
		EntityLeg:               false,
		EntityWeight:            0.10,
		EntityMaxTerms:          6,
		FieldLexicalLeg:         false,
		FieldLexicalWeight:      0.15,
		FieldLexicalK:           60,
		CombMNZBonus:            0,
		TitleLeadWeight:         0.12,
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
		LeadChars:               250,
		DeepChunkPenalty:        0,
		PassageRatioWeight:      0.10,
		PassageSupportWeight:    0.12,
		RerankProtocol:          "tei",
		RerankTopK:              30,
		RerankRanking:           "linear",
		RerankWeight:            0.90,
		RerankMaxDocChars:       2000,
		RerankTimeoutMS:         12000,
		SurrogateMode:           "inverted_pyramid",
		AdaptiveCascade:         false,
		CascadeJaccardThreshold: 0.50,
		CascadeMarginThreshold:  0.15,
		CascadeDGapThreshold:    2.25,
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

	// Parse served candidate legs from Round 1.
	var denseGrouped, lexicalFull, lexicalEntity, fieldLexical, denseUngrouped []quivrplugin.Candidate
	for _, s := range req.Served {
		if s.Round != 1 {
			continue
		}
		switch s.Request.Primitive {
		case quivrplugin.PrimitiveNearVector:
			if s.Request.GroupBy == "record" {
				denseGrouped = append(denseGrouped, s.Candidates...)
			} else {
				denseUngrouped = append(denseUngrouped, s.Candidates...)
			}
		case quivrplugin.PrimitiveBM25:
			if s.Request.Field == quivrplugin.FieldLexical {
				fieldLexical = append(fieldLexical, s.Candidates...)
				continue
			}
			expectedLex := strings.TrimSpace(req.Query.Text)
			if cfg.CleanLexicalQuery {
				expectedLex = strings.TrimSpace(cleanLexicalQuery(req.Query.Text))
			}
			if len(lexicalFull) == 0 && strings.TrimSpace(s.Request.QueryText) == expectedLex {
				lexicalFull = s.Candidates
			} else if len(lexicalFull) == 0 && !cfg.EntityLeg {
				lexicalFull = s.Candidates
			} else {
				lexicalEntity = s.Candidates
			}
		}
	}

	bundles, denseForQPP := fuseAndScoreWithQPP(req, cfg, denseGrouped, lexicalFull, lexicalEntity, fieldLexical, denseUngrouped)
	if len(bundles) == 0 {
		return quivrplugin.Rank(), nil
	}

	conf := computeStage1Confidence(denseForQPP, lexicalFull, bundles)
	if cfg.LogDiagnostics {
		entityQ, _ := extractEntityQuery(req.Query.Text, cfg.EntityMaxTerms)
		fmt.Fprintf(os.Stderr,
			"[newsroom-diag] profile=%s mode=%s pool=%d dense=%d lex=%d ent=%d ungrp=%d jaccard10=%.3f margin=%.3f dgap=%.2f lgap=%.2f lnqc=%.2f top5lead=%.2f entity_q=%q\n",
			req.Profile, req.Query.Mode, len(bundles), len(denseGrouped), len(lexicalFull), len(lexicalEntity), len(denseUngrouped),
			conf.Jaccard10, conf.ScoreMargin, conf.DenseGap, conf.LexGap, conf.LexNQC, conf.Top5LeadFrac, entityQ,
		)
	}

	// Deep profile: optional adaptive confidence cascade + inverted-pyramid surrogate reranking.
	if req.Profile == "deep" && cfg.RerankEndpoint != "" {
		skipRerank := cfg.AdaptiveCascade &&
			conf.Jaccard10 >= cfg.CascadeJaccardThreshold &&
			conf.ScoreMargin >= cfg.CascadeMarginThreshold &&
			conf.DenseGap >= cfg.CascadeDGapThreshold
		if !skipRerank {
			rerankedBundles, err := r.applyDeepRerank(ctx, req, cfg, started, bundles)
			if err != nil {
				return nil, err
			}
			bundles = rerankedBundles
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

func clampK(k, reqLimit int) int {
	if k <= 0 {
		k = 100
	}
	if k < reqLimit {
		k = reqLimit
	}
	if k > 100 {
		k = 100
	}
	return k
}

func buildRound1Requests(req *quivrplugin.SearchRequest, cfg configuration) ([]quivrplugin.CandidateRequest, error) {
	k := clampK(cfg.CandidateCount, req.Limit)
	lexK := k
	if cfg.LexicalK > 0 {
		lexK = clampK(cfg.LexicalK, req.Limit)
	}
	denseK := k
	if cfg.DenseK > 0 {
		denseK = clampK(cfg.DenseK, req.Limit)
	}

	var reqs []quivrplugin.CandidateRequest
	space := req.ServedSpace()

	if req.Query.Mode == "semantic" && space == nil {
		return nil, quivrplugin.TerminalSearchError("no_served_space", "the searched Corpora serve no vector space; search by keywords, or rebuild them")
	}

	// Leg A: Full-query dense near_vector.
	// When dense_grouped=false (default), a single ungrouped near_vector call provides
	// MaxP, FirstP/MaxP ratio, multi-passage corroboration, and surrogate passages at half the latency.
	if req.Query.Mode != "lexical" && space != nil {
		if cfg.DenseGrouped {
			reqs = append(reqs, quivrplugin.CandidateRequest{
				Primitive: quivrplugin.PrimitiveNearVector,
				QueryText: req.Query.Text,
				Space:     space.ID,
				K:         k,
				GroupBy:   "record",
			})
		} else {
			reqs = append(reqs, quivrplugin.CandidateRequest{
				Primitive: quivrplugin.PrimitiveNearVector,
				QueryText: req.Query.Text,
				Space:     space.ID,
				K:         denseK,
				GroupBy:   "",
			})
		}
	}

	// Leg B: Full-query lexical BM25 (grouped by record)
	if req.Query.Mode != "semantic" {
		lexQuery := req.Query.Text
		if cfg.CleanLexicalQuery {
			lexQuery = cleanLexicalQuery(req.Query.Text)
		}
		reqs = append(reqs, quivrplugin.CandidateRequest{
			Primitive: quivrplugin.PrimitiveBM25,
			QueryText: lexQuery,
			Field:     quivrplugin.FieldSource,
			K:         lexK,
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
				K:         lexK,
				GroupBy:   "record",
			})
		}
	}

	// Leg D: Segment.LexicalText BM25 over French-normalized entity/theme cards (FieldLexical)
	if cfg.FieldLexicalLeg && req.Query.Mode != "semantic" {
		fieldLexK := lexK
		if cfg.FieldLexicalK > 0 {
			fieldLexK = clampK(cfg.FieldLexicalK, req.Limit)
		}
		toks := tokenizeContent(req.Query.Text)
		fieldLexQ := strings.Join(toks, " ")
		if fieldLexQ == "" {
			fieldLexQ = cleanLexicalQuery(req.Query.Text)
		}
		reqs = append(reqs, quivrplugin.CandidateRequest{
			Primitive: quivrplugin.PrimitiveBM25,
			QueryText: fieldLexQ,
			Field:     quivrplugin.FieldLexical,
			K:         fieldLexK,
			GroupBy:   "record",
		})
	}

	// Extra ungrouped dense request only when dense_grouped=true AND passage/surrogate signals need it.
	if cfg.DenseGrouped {
		needUngrouped := cfg.PassageRatioWeight > 0 || cfg.PassageSupportWeight > 0 ||
			(req.Profile == "deep" && cfg.RerankEndpoint != "" && cfg.SurrogateMode == "inverted_pyramid")
		if needUngrouped && req.Query.Mode != "lexical" && space != nil {
			reqs = append(reqs, quivrplugin.CandidateRequest{
				Primitive: quivrplugin.PrimitiveNearVector,
				QueryText: req.Query.Text,
				Space:     space.ID,
				K:         denseK,
				GroupBy:   "",
			})
		}
	}

	return reqs, nil
}

func fuseAndScore(
	req *quivrplugin.SearchRequest,
	cfg configuration,
	denseGrouped, lexicalFull, lexicalEntity, denseUngrouped []quivrplugin.Candidate,
) []*recordBundle {
	bundles, _ := fuseAndScoreWithQPP(req, cfg, denseGrouped, lexicalFull, lexicalEntity, nil, denseUngrouped)
	return bundles
}

func partKeyBoostedScore(c quivrplugin.Candidate, boosts map[string]float64) float64 {
	if len(boosts) == 0 {
		return c.Score
	}
	if mult, ok := boosts[c.PartKey]; ok && mult > 0 {
		return c.Score * mult
	}
	return c.Score
}

func fuseAndScoreWithQPP(
	req *quivrplugin.SearchRequest,
	cfg configuration,
	denseGrouped, lexicalFull, lexicalEntity, fieldLexical, denseUngrouped []quivrplugin.Candidate,
) ([]*recordBundle, []quivrplugin.Candidate) {
	byRecord := map[string]*recordBundle{}
	addBodySeg := func(b *recordBundle, c quivrplugin.Candidate, fromDense bool) {
		if !isBodyPart(c) {
			if b.CardSeg == nil || c.Score > b.CardSeg.Score {
				cp := c
				b.CardSeg = &cp
			}
			return
		}
		if isLeadSegment(c) {
			if b.BodyLeadSeg == nil || c.Score > b.BodyLeadSeg.Score || (c.Score == b.BodyLeadSeg.Score && c.SegmentID < b.BodyLeadSeg.SegmentID) {
				cp := c
				b.BodyLeadSeg = &cp
			}
		} else {
			if b.BodyDeepBestSeg == nil || c.Score > b.BodyDeepBestSeg.Score || (c.Score == b.BodyDeepBestSeg.Score && c.SegmentID < b.BodyDeepBestSeg.SegmentID) {
				cp := c
				b.BodyDeepBestSeg = &cp
			}
		}
		if fromDense {
			if b.BodyBestSeg == nil || !b.HasBodyDense || c.Score > b.BodyBestSeg.Score || (c.Score == b.BodyBestSeg.Score && c.SegmentID < b.BodyBestSeg.SegmentID) {
				if b.BodyBestSeg != nil && b.HasBodyDense && b.BodyBestSeg.SegmentID != c.SegmentID {
					if b.BodySecondSeg == nil || b.BodyBestSeg.Score > b.BodySecondSeg.Score {
						prev := *b.BodyBestSeg
						b.BodySecondSeg = &prev
					}
				}
				cp := c
				b.BodyBestSeg = &cp
				b.HasBodyDense = true
			} else if b.BodyBestSeg.SegmentID != c.SegmentID && (b.BodySecondSeg == nil || c.Score > b.BodySecondSeg.Score) {
				cp := c
				b.BodySecondSeg = &cp
			}
		} else if !b.HasBodyDense {
			if b.BodyBestSeg == nil || c.Score > b.BodyBestSeg.Score {
				cp := c
				b.BodyBestSeg = &cp
			}
		}
	}

	getBundle := func(c quivrplugin.Candidate) *recordBundle {
		key := recordKey(c)
		b, ok := byRecord[key]
		if !ok {
			b = &recordBundle{RecordID: key}
			byRecord[key] = b
		}
		if isLeadSegment(c) {
			if b.LeadSeg == nil || c.Score > b.LeadSeg.Score || (c.Score == b.LeadSeg.Score && c.SegmentID < b.LeadSeg.SegmentID) {
				cp := c
				b.LeadSeg = &cp
			}
		} else {
			if b.DeepBestSeg == nil || c.Score > b.DeepBestSeg.Score || (c.Score == b.DeepBestSeg.Score && c.SegmentID < b.DeepBestSeg.SegmentID) {
				cp := c
				b.DeepBestSeg = &cp
			}
		}
		return b
	}

	// Ingest Leg A: grouped dense (if requested)
	for i, c := range denseGrouped {
		sc := partKeyBoostedScore(c, cfg.PartKeyBoosts)
		c.Score = sc
		b := getBundle(c)
		addBodySeg(b, c, true)
		if !b.HasDense || sc > b.DenseScore || (sc == b.DenseScore && c.SegmentID < b.BestSeg.SegmentID) {
			b.DenseScore = sc
			b.DenseRank = i + 1
			b.HasDense = true
			b.BestSeg = c
			b.HasBestSeg = true
		}
	}

	// Ingest ungrouped dense passages for FirstP/MaxP ratio, multi-passage corroboration,
	// surrogates, and (when denseGrouped is absent) primary dense scoring!
	ungrpFloor := 0.0
	if len(denseUngrouped) > 0 {
		ungrpFloor = denseUngrouped[len(denseUngrouped)-1].Score
		seenSeg := map[string]bool{}
		for _, c := range denseUngrouped {
			if seenSeg[c.SegmentID] {
				continue
			}
			seenSeg[c.SegmentID] = true
			sc := partKeyBoostedScore(c, cfg.PartKeyBoosts)
			c.Score = sc
			b := getBundle(c)
			addBodySeg(b, c, true)
			b.PassageCount++
			if !b.HasUngrouped || sc > b.MaxP || (sc == b.MaxP && c.SegmentID < b.BestSeg.SegmentID) {
				if b.HasUngrouped && b.HasBestSeg && b.BestSeg.SegmentID != c.SegmentID {
					if b.SecondSeg == nil || b.BestSeg.Score > b.SecondSeg.Score || (b.BestSeg.Score == b.SecondSeg.Score && b.BestSeg.SegmentID < b.SecondSeg.SegmentID) {
						prev := b.BestSeg
						b.SecondSeg = &prev
					}
				}
				b.MaxP = sc
				b.HasUngrouped = true
				if len(denseGrouped) == 0 {
					b.DenseScore = sc
					b.HasDense = true
					b.BestSeg = c
					b.HasBestSeg = true
				}
			} else if b.SecondSeg == nil || sc > b.SecondSeg.Score || (sc == b.SecondSeg.Score && c.SegmentID < b.SecondSeg.SegmentID) {
				cp := c
				b.SecondSeg = &cp
			}
			if isLeadSegment(c) {
				if !b.HasFirstP || sc > b.FirstP {
					b.FirstP = sc
					b.HasFirstP = true
				}
			}
			if !b.HasBestSeg {
				b.BestSeg = c
				b.HasBestSeg = true
			}
		}
	}

	// Build primary dense candidate list (deduplicated per record) for leg stats & QPP.
	densePrimary := denseGrouped
	if len(densePrimary) == 0 && len(denseUngrouped) > 0 {
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

	// Ingest Leg B: full-query BM25
	lexStats := computeLegStats(lexicalFull)
	for i, c := range lexicalFull {
		b := getBundle(c)
		addBodySeg(b, c, false)
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

	// Ingest Leg D: FieldLexical BM25 (Segment.LexicalText)
	fieldLexStats := computeLegStats(fieldLexical)
	for i, c := range fieldLexical {
		b := getBundle(c)
		addBodySeg(b, c, false)
		if !b.HasFieldLexical || c.Score > b.FieldLexicalScore {
			b.FieldLexicalScore = c.Score
			b.FieldLexicalRank = i + 1
			b.HasFieldLexical = true
		}
		if !b.HasBestSeg {
			b.BestSeg = c
			b.HasBestSeg = true
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

	// Stage 1: Multi-Leg Calibrated Fusion (DBSF / Convex / RRF + optional Query-Adaptive Alpha + CombMNZ)
	effDenseW := cfg.DenseWeight
	effLexW := cfg.LexicalWeight
	effEntW := cfg.EntityWeight
	effFieldLexW := cfg.FieldLexicalWeight
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
	if len(densePrimary) > 0 {
		activeWeightSum += effDenseW
	}
	if len(lexicalFull) > 0 {
		activeWeightSum += effLexW
	}
	if len(lexicalEntity) > 0 {
		activeWeightSum += effEntW
	}
	if len(fieldLexical) > 0 && effFieldLexW > 0 {
		activeWeightSum += effFieldLexW
	}
	if activeWeightSum <= 0 {
		activeWeightSum = 1.0
	}

	for _, b := range bundles {
		fused := 0.0
		legs := 0
		if b.HasDense {
			fused += effDenseW * normalizeLegScore(b.DenseScore, b.DenseRank, denseStats, cfg.Fusion, cfg.RRFK)
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
		if b.HasFieldLexical && effFieldLexW > 0 {
			fused += effFieldLexW * normalizeLegScore(b.FieldLexicalScore, b.FieldLexicalRank, fieldLexStats, cfg.Fusion, cfg.RRFK)
			legs++
		}
		// Normalize fused score to comparable [0, ~1] scale across 2-leg vs 3-leg configs.
		if cfg.Fusion != "rrf" {
			fused /= activeWeightSum
		}
		// Cross-modal CombMNZ: only reward consensus when dense AND lexical agree
		// (prevents correlated BM25+entity legs from double-boosting lexical-only hits).
		if cfg.CombMNZBonus > 0 && b.HasDense && (b.HasLexical || b.HasEntity) {
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

	// Stage 2: Algorithmic $0 Candidate-Pool Signals (Title+Lead IDF, Aspect Coverage, Min-Span/SDM Proximity, Inverted-Pyramid Salience)
	needTextSignals := cfg.TitleLeadWeight > 0 || cfg.AspectCoverageWeight > 0 || cfg.MissingAspectPenalty > 0 ||
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
				titleLeadSet             map[string]bool
			}
			docs := make([]docTextData, len(bundles))
			tokenSets := make([]map[string]bool, len(bundles))
			titleLeadSets := make([]map[string]bool, len(bundles))
			for i, b := range bundles {
				t, l, e, d, full := extractZoneTexts(b, cfg.LeadChars)
				toks := tokenizeContent(full)
				set := make(map[string]bool, len(toks))
				for _, tok := range toks {
					set[tok] = true
				}
				var tlSet map[string]bool
				if cfg.TitleLeadWeight > 0 {
					tlCombined := t
					if l != "" {
						if tlCombined != "" {
							tlCombined += " " + l
						} else {
							tlCombined = l
						}
					}
					tlSet = tokenSetOf(tlCombined)
				}
				docs[i] = docTextData{title: t, lead: l, early: e, deep: d, tokens: toks, tokenSet: set, titleLeadSet: tlSet}
				tokenSets[i] = set
				titleLeadSets[i] = tlSet
			}
			idfs := computePoolIDF(aspects, tokenSets)
			var titleLeadIDFs []float64
			if cfg.TitleLeadWeight > 0 {
				titleLeadIDFs = computePoolIDF(aspects, titleLeadSets)
			}

			for i, b := range bundles {
				if cfg.TitleLeadWeight > 0 {
					tlCov := scoreAspectCoverage(aspects, titleLeadIDFs, docs[i].titleLeadSet)
					b.FinalScore += cfg.TitleLeadWeight * tlCov
				}
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

	docs := make([]string, len(head))
	for i, b := range head {
		docs[i] = buildSurrogate(b, cfg.SurrogateMode, cfg.RerankMaxDocChars)
	}
	scores, err := r.callReranker(callCtx, cfg, req.Query.Text, docs)
	if err != nil {
		if cfg.LogDiagnostics {
			fmt.Fprintf(os.Stderr, "[newsroom-diag] deep_rerank_error=%v fallback=%v\n", err, cfg.fallbackOnError())
		}
		if cfg.fallbackOnError() {
			return bundles, nil
		}
		return nil, &quivrplugin.SearchError{Code: "reranker_unavailable", Message: err.Error(), Retryable: true}
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
	return append(reranked, tail...), nil
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
