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
	"unicode/utf8"

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
	EntityLeg               bool    `json:"entity_leg"`
	EntityWeight            float64 `json:"entity_weight"`
	EntityMaxTerms          int     `json:"entity_max_terms"`
	CombMNZBonus            float64 `json:"combmnz_bonus"`
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

	// Wide-funnel candidate generation (Wave 2C-A).
	DenseViewMode         string  `json:"dense_view_mode"`
	DenseViewWeight       float64 `json:"dense_view_weight"`
	DenseViewGrouped      bool    `json:"dense_view_grouped"`
	DenseViewK            int     `json:"dense_view_k"`
	DenseRecallWeight     float64 `json:"dense_recall_weight"`
	DenseRecallK          int     `json:"dense_recall_k"`
	LexicalViewMode       string  `json:"lexical_view_mode"`
	LexicalViewWeight     float64 `json:"lexical_view_weight"`
	LexicalViewK          int     `json:"lexical_view_k"`
	LexicalNormDepth      int     `json:"lexical_norm_depth"`
	DenseNormDepth        int     `json:"dense_norm_depth"`
	Round2ConsensusWeight float64 `json:"round2_consensus_weight"`
	Round2ConsensusTopN   int     `json:"round2_consensus_top_n"`
	Round2ConsensusDocs   int     `json:"round2_consensus_docs"`
	Round2MaxTerms        int     `json:"round2_max_terms"`
	Round2QueryBase       string  `json:"round2_query_base"`
	Round2DenseK          int     `json:"round2_dense_k"`
	Round2LexicalWeight   float64 `json:"round2_lexical_weight"`
	Round2LexicalK        int     `json:"round2_lexical_k"`
	DenseEntityMaxTerms   int     `json:"dense_entity_max_terms"`
	DumpPool              bool    `json:"dump_pool"`
	TailFusion            string  `json:"tail_fusion"`
	TailStart             int     `json:"tail_start"`
	TailRRFK              float64 `json:"tail_rrf_k"`
	TailConvexWeight      float64 `json:"tail_convex_weight"`
	TailExtraWeight       float64 `json:"tail_extra_weight"`

	// Deep-tier calibration and multi-passage scoring (Wave 2C-A).
	RerankScoreSpace       string  `json:"rerank_score_space"`
	RerankMultiPassageTopN int     `json:"rerank_multi_passage_top_n"`
	RerankMultiPassageMode string  `json:"rerank_multi_passage_mode"`
	RerankBodyWeight       float64 `json:"rerank_body_weight"`
	RerankLeadChars        int     `json:"rerank_lead_chars"`
	RerankBandEnd          int     `json:"rerank_band_end"`
	RerankBandStart        int     `json:"rerank_band_start"`
	RerankBandMaxChars     int     `json:"rerank_band_max_chars"`
	RerankBandWeight       float64 `json:"rerank_band_weight"`
	RerankParallelRequests int     `json:"rerank_parallel_requests"`
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
		DenseViewMode:           "none",
		DenseViewWeight:         0,
		DenseViewK:              100,
		DenseRecallK:            100,
		LexicalViewMode:         "none",
		LexicalViewK:            60,
		Round2ConsensusTopN:     10,
		Round2ConsensusDocs:     2,
		Round2MaxTerms:          3,
		Round2QueryBase:         "core",
		Round2DenseK:            100,
		Round2LexicalK:          60,
		DenseEntityMaxTerms:     8,
		TailFusion:              "none",
		TailStart:               10,
		TailRRFK:                60,
		TailConvexWeight:        1.0,
		TailExtraWeight:         1.0,
		RerankScoreSpace:        "prob",
		RerankMultiPassageMode:  "blend",
		RerankBodyWeight:        0.35,
		RerankLeadChars:         1200,
		RerankBandEnd:           0,
		RerankBandMaxChars:      900,
		RerankBandWeight:        0.85,
		RerankParallelRequests:  1,
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

	// Attribute served Round-1 candidates to legs by request_index (the Round-1
	// request list is a pure function of the query and configuration).
	specs, _ := round1Specs(req, cfg)
	var denseGrouped, lexicalFull, lexicalEntity, denseUngrouped []quivrplugin.Candidate
	extraByName := map[string]*extraLeg{}
	addExtra := func(name string, weight float64, depth int, cands []quivrplugin.Candidate) {
		if weight <= 0 || len(cands) == 0 {
			return
		}
		if el, ok := extraByName[name]; ok {
			el.Cands = append(el.Cands, cands...)
			return
		}
		extraByName[name] = &extraLeg{Name: name, Weight: weight, NormDepth: depth, Cands: cands}
	}
	for _, s := range req.Served {
		if s.Round == 2 {
			switch s.Request.Primitive {
			case quivrplugin.PrimitiveNearVector:
				addExtra(legR2Dense, cfg.Round2ConsensusWeight, 0, s.Candidates)
			case quivrplugin.PrimitiveBM25:
				addExtra(legR2Lex, cfg.Round2LexicalWeight, cfg.LexicalNormDepth, s.Candidates)
			}
			continue
		}
		if s.Round != 1 {
			continue
		}
		name := ""
		if s.RequestIndex >= 0 && s.RequestIndex < len(specs) && specs[s.RequestIndex].Req.Primitive == s.Request.Primitive &&
			strings.TrimSpace(specs[s.RequestIndex].Req.QueryText) == strings.TrimSpace(s.Request.QueryText) {
			name = specs[s.RequestIndex].Name
		}
		switch name {
		case legDense, legDenseAux:
			denseUngrouped = append(denseUngrouped, s.Candidates...)
		case legDenseGroup:
			denseGrouped = append(denseGrouped, s.Candidates...)
		case legLexical:
			lexicalFull = s.Candidates
		case legEntity:
			lexicalEntity = s.Candidates
		case legDenseView:
			addExtra(legDenseView, cfg.DenseViewWeight, 0, s.Candidates)
		case legDenseRecall:
			addExtra(legDenseRecall, cfg.DenseRecallWeight, 0, s.Candidates)
		case legLexView:
			addExtra(legLexView, cfg.LexicalViewWeight, cfg.LexicalNormDepth, s.Candidates)
		default:
			// Unattributed request (e.g. a host that reorders served lists): fall back to primitive heuristics.
			switch s.Request.Primitive {
			case quivrplugin.PrimitiveNearVector:
				if s.Request.GroupBy == "record" {
					denseGrouped = append(denseGrouped, s.Candidates...)
				} else {
					denseUngrouped = append(denseUngrouped, s.Candidates...)
				}
			case quivrplugin.PrimitiveBM25:
				if len(lexicalFull) == 0 {
					lexicalFull = s.Candidates
				} else {
					lexicalEntity = s.Candidates
				}
			}
		}
	}

	if cfg.DumpPool && req.Round == 2 {
		for _, s := range req.Served {
			if len(s.Candidates) > 0 {
				c := s.Candidates[0]
				fmt.Fprintf(os.Stderr, "[newsroom-shape] prim=%s group=%s part=%q start=%d end=%d ranges=%+v sep=%q text=%d passage=%d head=%q\n",
					s.Request.Primitive, s.Request.GroupBy, c.PartKey, c.Start, c.End, c.SourceRanges, c.SourceSeparator,
					len(c.Text), len(c.PassageText), truncateRunes(candidateBodyText(c), 160))
			}
		}
	}

	// Optional zero-LLM Round-2 consensus PRF (dense and/or lexical).
	if req.Round == 2 {
		denseRecords := denseGrouped
		if len(denseRecords) == 0 {
			denseRecords = dedupByRecord(denseUngrouped)
		}
		if r2, terms := buildRound2Requests(req, cfg, denseRecords, lexicalFull); len(r2) > 0 {
			if cfg.LogDiagnostics {
				fmt.Fprintf(os.Stderr, "[newsroom-diag] round2_consensus terms=%q requests=%d\n", terms, len(r2))
			}
			return quivrplugin.Ask(r2...), nil
		}
	}
	extras := make([]extraLeg, 0, len(extraByName))
	for _, name := range []string{legDenseView, legDenseRecall, legLexView, legR2Dense, legR2Lex} {
		if el, ok := extraByName[name]; ok {
			extras = append(extras, *el)
		}
	}

	bundles, denseForQPP := fuseAndScoreExt(req, cfg, denseGrouped, lexicalFull, lexicalEntity, denseUngrouped, extras)
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

	if cfg.DumpPool {
		dumpPool(req.Query.Text, req.Round, bundles)
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
	specs, err := round1Specs(req, cfg)
	if err != nil {
		return nil, err
	}
	reqs := make([]quivrplugin.CandidateRequest, len(specs))
	for i, sp := range specs {
		reqs[i] = sp.Req
	}
	return reqs, nil
}

// round1Specs deterministically builds the named Round-1 candidate requests.
func round1Specs(req *quivrplugin.SearchRequest, cfg configuration) ([]namedRequest, error) {
	k := clampK(cfg.CandidateCount, req.Limit)
	lexK := k
	if cfg.LexicalK > 0 {
		lexK = clampK(cfg.LexicalK, req.Limit)
	}
	denseK := k
	if cfg.DenseK > 0 {
		denseK = clampK(cfg.DenseK, req.Limit)
	}

	var specs []namedRequest
	space := req.ServedSpace()

	if req.Query.Mode == "semantic" && space == nil {
		return nil, quivrplugin.TerminalSearchError("no_served_space", "the searched Corpora serve no vector space; search by keywords, or rebuild them")
	}
	useDense := req.Query.Mode != "lexical" && space != nil
	useLex := req.Query.Mode != "semantic"

	// Leg A: Full-query dense near_vector.
	// When dense_grouped=false (default), a single ungrouped near_vector call provides
	// MaxP, FirstP/MaxP ratio, multi-passage corroboration, and surrogate passages at half the latency.
	if useDense {
		if cfg.DenseGrouped {
			specs = append(specs, namedRequest{legDenseGroup, quivrplugin.CandidateRequest{
				Primitive: quivrplugin.PrimitiveNearVector,
				QueryText: req.Query.Text,
				Space:     space.ID,
				K:         k,
				GroupBy:   "record",
			}})
		} else {
			specs = append(specs, namedRequest{legDense, quivrplugin.CandidateRequest{
				Primitive: quivrplugin.PrimitiveNearVector,
				QueryText: req.Query.Text,
				Space:     space.ID,
				K:         denseK,
				GroupBy:   "",
			}})
		}
	}

	// Leg B: Full-query lexical BM25 (grouped by record)
	if useLex {
		lexQuery := req.Query.Text
		if cfg.CleanLexicalQuery {
			lexQuery = cleanLexicalQuery(req.Query.Text)
		}
		specs = append(specs, namedRequest{legLexical, quivrplugin.CandidateRequest{
			Primitive: quivrplugin.PrimitiveBM25,
			QueryText: lexQuery,
			Field:     quivrplugin.FieldSource,
			K:         lexK,
			GroupBy:   "record",
		}})
	}

	// Leg C: Focused Entity / High-Specificity Sub-Query BM25
	if cfg.EntityLeg && useLex {
		if subQ, ok := extractEntityQuery(req.Query.Text, cfg.EntityMaxTerms); ok && subQ != "" {
			specs = append(specs, namedRequest{legEntity, quivrplugin.CandidateRequest{
				Primitive: quivrplugin.PrimitiveBM25,
				QueryText: subQ,
				Field:     quivrplugin.FieldSource,
				K:         lexK,
				GroupBy:   "record",
			}})
		}
	}

	// Extra ungrouped dense request only when dense_grouped=true AND passage/surrogate signals need it.
	if cfg.DenseGrouped && useDense {
		needUngrouped := cfg.PassageRatioWeight > 0 || cfg.PassageSupportWeight > 0 ||
			(req.Profile == "deep" && cfg.RerankEndpoint != "" && cfg.SurrogateMode != "raw")
		if needUngrouped {
			specs = append(specs, namedRequest{legDenseAux, quivrplugin.CandidateRequest{
				Primitive: quivrplugin.PrimitiveNearVector,
				QueryText: req.Query.Text,
				Space:     space.ID,
				K:         denseK,
				GroupBy:   "",
			}})
		}
	}

	// Wide-funnel leg D: boilerplate-stripped dense query view.
	if useDense && cfg.DenseViewWeight > 0 {
		if vq, ok := denseViewQuery(req.Query.Text, cfg); ok {
			group := ""
			if cfg.DenseViewGrouped {
				group = "record"
			}
			specs = append(specs, namedRequest{legDenseView, quivrplugin.CandidateRequest{
				Primitive: quivrplugin.PrimitiveNearVector,
				QueryText: vq,
				Space:     space.ID,
				K:         clampK(cfg.DenseViewK, req.Limit),
				GroupBy:   group,
			}})
		}
	}

	// Wide-funnel leg E: record-grouped full-query dense recall leg (100 distinct records).
	if useDense && cfg.DenseRecallWeight > 0 && !cfg.DenseGrouped {
		specs = append(specs, namedRequest{legDenseRecall, quivrplugin.CandidateRequest{
			Primitive: quivrplugin.PrimitiveNearVector,
			QueryText: req.Query.Text,
			Space:     space.ID,
			K:         clampK(cfg.DenseRecallK, req.Limit),
			GroupBy:   "record",
		}})
	}

	// Wide-funnel leg F: boilerplate-stripped lexical query view.
	if useLex && cfg.LexicalViewWeight > 0 {
		if lq, ok := lexicalViewQuery(req.Query.Text, cfg); ok {
			specs = append(specs, namedRequest{legLexView, quivrplugin.CandidateRequest{
				Primitive: quivrplugin.PrimitiveBM25,
				QueryText: lq,
				Field:     quivrplugin.FieldSource,
				K:         clampK(cfg.LexicalViewK, req.Limit),
				GroupBy:   "record",
			}})
		}
	}

	return specs, nil
}

func fuseAndScore(
	req *quivrplugin.SearchRequest,
	cfg configuration,
	denseGrouped, lexicalFull, lexicalEntity, denseUngrouped []quivrplugin.Candidate,
) []*recordBundle {
	bundles, _ := fuseAndScoreWithQPP(req, cfg, denseGrouped, lexicalFull, lexicalEntity, denseUngrouped)
	return bundles
}

func fuseAndScoreWithQPP(
	req *quivrplugin.SearchRequest,
	cfg configuration,
	denseGrouped, lexicalFull, lexicalEntity, denseUngrouped []quivrplugin.Candidate,
) ([]*recordBundle, []quivrplugin.Candidate) {
	return fuseAndScoreExt(req, cfg, denseGrouped, lexicalFull, lexicalEntity, denseUngrouped, nil)
}

func fuseAndScoreExt(
	req *quivrplugin.SearchRequest,
	cfg configuration,
	denseGrouped, lexicalFull, lexicalEntity, denseUngrouped []quivrplugin.Candidate,
	extras []extraLeg,
) ([]*recordBundle, []quivrplugin.Candidate) {
	byRecord := map[string]*recordBundle{}
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
		b := getBundle(c)
		if !b.HasDense || c.Score > b.DenseScore || (c.Score == b.DenseScore && c.SegmentID < b.BestSeg.SegmentID) {
			b.DenseScore = c.Score
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
			b := getBundle(c)
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
				if len(denseGrouped) == 0 {
					b.DenseScore = c.Score
					b.HasDense = true
					b.BestSeg = c
					b.HasBestSeg = true
				}
			} else if b.SecondSeg == nil || c.Score > b.SecondSeg.Score || (c.Score == b.SecondSeg.Score && c.SegmentID < b.SecondSeg.SegmentID) {
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
	denseStats := statsAtDepth(densePrimary, cfg.DenseNormDepth)

	// Ingest Leg B: full-query BM25
	lexStats := statsAtDepth(lexicalFull, cfg.LexicalNormDepth)
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

	// Ingest wide-funnel extra legs (record-level ranks, calibrated per leg).
	extraStats := make([]legStats, len(extras))
	for li, el := range extras {
		records := dedupByRecord(el.Cands)
		extraStats[li] = statsAtDepth(records, el.NormDepth)
		for i, c := range records {
			b := getBundle(c)
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
		for li, el := range extras {
			if h, ok := b.Extra[el.Name]; ok && el.Weight > 0 {
				fused += el.Weight * normalizeLegScore(h.Score, h.Rank, extraStats[li], cfg.Fusion, cfg.RRFK)
				legs++
			}
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
	applyTailFusion(bundles, cfg)
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

	// Primary view per head record; optional multi-passage lead/body views.
	nMP := min(cfg.RerankMultiPassageTopN, len(head))
	docs := make([]string, len(head))
	for i, b := range head {
		if i < nMP {
			docs[i] = buildLeadView(b, cfg.RerankMaxDocChars)
		} else {
			docs[i] = surrogateFor(b, cfg)
		}
	}
	bodyOf := map[int]int{} // head index -> doc index of its body view
	for i := 0; i < nMP; i++ {
		if bv, ok := buildBodyView(head[i], cfg.RerankMaxDocChars); ok {
			bodyOf[i] = len(docs)
			docs = append(docs, bv)
		}
	}
	// Optional recall band: compact lead views for Stage-1 ranks topK+1..band_end so the
	// cross-encoder can promote relevant records across the Recall@50 cut.
	bandN := 0
	if cfg.RerankBandEnd > topK {
		bandN = min(cfg.RerankBandEnd-topK, len(tail))
	}
	bandStart := len(docs)
	for i := 0; i < bandN; i++ {
		docs = append(docs, buildLeadView(tail[i], cfg.RerankBandMaxChars))
	}
	if cfg.LogDiagnostics {
		total, short := 0, 0
		for _, d := range docs {
			n := utf8.RuneCountInString(d)
			total += n
			if n < 400 {
				short++
			}
		}
		fmt.Fprintf(os.Stderr, "[newsroom-diag] rerank_docs=%d chars=%d short=%d sample=%q\n", len(docs), total, short, truncateRunes(docs[0], 240))
	}
	rs, err := r.callRerankerSharded(callCtx, cfg, req.Query.Text, docs, cfg.RerankParallelRequests)
	if err != nil {
		if cfg.LogDiagnostics {
			fmt.Fprintf(os.Stderr, "[newsroom-diag] deep_rerank_error=%v fallback=%v\n", err, cfg.fallbackOnError())
		}
		if cfg.fallbackOnError() {
			return bundles, nil
		}
		return nil, &quivrplugin.SearchError{Code: "reranker_unavailable", Message: err.Error(), Retryable: true}
	}
	all := rs.Prob
	if cfg.RerankScoreSpace == "logit" {
		all = rs.logitSpace()
	}
	scores := make([]float64, len(head))
	for i := range head {
		scores[i] = all[i]
		if j, ok := bodyOf[i]; ok {
			scores[i] = combineViews(all[i], all[j], cfg.RerankMultiPassageMode, cfg.RerankBodyWeight)
		}
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
	case "zscore":
		baseScores := make([]float64, len(reranked))
		for i, b := range reranked {
			baseScores[i] = b.FinalScore
		}
		zb := zScores(baseScores)
		zr := zScores(scores)
		for i, b := range reranked {
			b.FinalScore = cfg.RerankWeight*zr[i] + (1.0-cfg.RerankWeight)*zb[i]
			b.Explanation = "newsroom+deep(zscore," + cfg.SurrogateMode + ")"
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
	out := append(reranked, tail...)
	if bandN > 0 {
		// Re-sort the region [band_start, topK+bandN) that straddles the Recall@50 cut:
		// head records keep their full-view margin, band records use their compact-view
		// margin; both are standardized together with the pre-rerank Stage-1 score.
		bs := cfg.RerankBandStart
		if bs <= 0 || bs > topK {
			bs = topK
		}
		be := topK + bandN
		logits := rs.logitSpace()
		headLogit := make(map[*recordBundle]float64, len(head))
		for i, b := range head {
			v := logits[i]
			if j, ok := bodyOf[i]; ok {
				v = combineViews(logits[i], logits[j], cfg.RerankMultiPassageMode, cfg.RerankBodyWeight)
			}
			headLogit[b] = v
		}
		for i := 0; i < bandN; i++ {
			headLogit[tail[i]] = logits[bandStart+i]
		}
		region := out[bs:be]
		origScores := make([]float64, len(region))
		rv := make([]float64, len(region))
		sv := make([]float64, len(region))
		for i, b := range region {
			origScores[i] = b.FinalScore
			rv[i] = headLogit[b]
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
		}
	}
	return out, nil
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
