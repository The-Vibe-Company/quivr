package plugin

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"os"
	"slices"
	"sort"
	"sync"
	"time"
)

func (cfg configuration) invalidAdditionalReranker() string {
	if cfg.RerankSecondaryEndpoint == "" {
		if cfg.RerankCascadeK != 0 || cfg.RerankEnsemble != "" || cfg.RerankMediumMargin != nil {
			return "cascade and ensemble modes require rerank_secondary_endpoint"
		}
		return ""
	}
	u, err := url.Parse(cfg.RerankSecondaryEndpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "rerank_secondary_endpoint must be an absolute HTTP(S) URL"
	}
	if cfg.RerankEndpoint == "" {
		return "rerank_secondary_endpoint requires rerank_endpoint"
	}
	switch cfg.RerankEnsemble {
	case "":
		if cfg.RerankCascadeK < cfg.RerankTopK || cfg.RerankCascadeK > 100 {
			return "rerank_cascade_k must be at least rerank_top_k and at most 100"
		}
		if cfg.RerankMediumMargin != nil && (*cfg.RerankMediumMargin < 0 || *cfg.RerankMediumMargin > 1) {
			return "rerank_medium_margin must be between 0 and 1"
		}
	case "logit", "rank_prob":
		if cfg.RerankCascadeK != 0 || cfg.RerankMediumMargin != nil {
			return "rerank_ensemble cannot be combined with cascade options"
		}
		primary, secondary, stage1 := cfg.RerankPrimaryWeight, cfg.RerankSecondaryWeight, cfg.RerankStage1Weight
		if primary <= 0 || secondary <= 0 || stage1 < 0 || math.Abs(primary+secondary+stage1-1) > 1e-9 {
			return "ensemble model weights must be positive, the Stage-1 weight nonnegative, and their sum 1"
		}
	default:
		return "rerank_ensemble must be logit or rank_prob"
	}
	return ""
}

func (r retriever) rerankBundles(ctx context.Context, cfg configuration, query string, bundles []*recordBundle, topK int) ([]*recordBundle, error) {
	pool := max(topK, min(cfg.RerankCascadeK, len(bundles)))
	docs := make([]string, pool)
	for i, b := range bundles[:pool] {
		docs[i] = buildSurrogate(b, cfg.SurrogateMode, cfg.RerankMaxDocChars)
	}

	head, tail := slices.Clone(bundles[:topK]), bundles[topK:]
	call := func(ctx context.Context, secondary bool, docs []string) (rerankScores, error) {
		endpoint := cfg
		stage := "primary"
		if secondary {
			stage = "secondary"
			endpoint.RerankEndpoint = cfg.RerankSecondaryEndpoint
			endpoint.RerankProtocol = "tei"
			endpoint.RerankInstruction = ""
		}
		start := time.Now()
		scores, err := r.callReranker(ctx, endpoint, query, docs)
		if cfg.LogDiagnostics {
			fmt.Fprintf(os.Stderr, "[newsroom-diag] rerank_stage=%s docs=%d elapsed_ms=%.2f success=%t\n",
				stage, len(docs), float64(time.Since(start))/float64(time.Millisecond), err == nil)
		}
		return scores, err
	}

	if cfg.RerankEnsemble != "" {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var secondaryScores rerankScores
		var secondaryErr error
		var wg sync.WaitGroup
		wg.Go(func() {
			secondaryScores, secondaryErr = call(ctx, true, docs)
			if secondaryErr != nil {
				cancel()
			}
		})
		primaryScores, primaryErr := call(ctx, false, docs)
		if primaryErr != nil {
			cancel()
		}
		wg.Wait()
		if primaryErr != nil && secondaryErr != nil {
			return nil, fmt.Errorf("primary: %v, secondary: %v", primaryErr, secondaryErr)
		}
		if primaryErr != nil {
			return nil, primaryErr
		}
		if secondaryErr != nil {
			return nil, secondaryErr
		}
		scoreEnsemble(cfg, head, primaryScores, secondaryScores)
		return stitchTail(head, tail), nil
	}

	if cfg.RerankSecondaryEndpoint != "" {
		secondaryScores, err := call(ctx, true, docs)
		if err != nil {
			return nil, err
		}
		order := descendingOrder(secondaryScores.logits)
		selected := make([]bool, pool)
		tail = make([]*recordBundle, 0, len(bundles)-topK)
		finalDocs := make([]string, topK)
		finalSecondary := rerankScores{probabilities: make([]float64, topK), logits: make([]float64, topK)}
		for i, idx := range order[:topK] {
			selected[idx] = true
			head[i], finalDocs[i] = bundles[idx], docs[idx]
			finalSecondary.probabilities[i] = secondaryScores.probabilities[idx]
			finalSecondary.logits[i] = secondaryScores.logits[idx]
		}
		// Keep Stage-1 order among all unselected documents, including those outside the pool.
		for i, b := range bundles {
			if i >= pool || !selected[i] {
				tail = append(tail, b)
			}
		}
		if cfg.RerankMediumMargin != nil &&
			secondaryScores.probabilities[order[0]]-secondaryScores.probabilities[order[min(9, len(order)-1)]] >= *cfg.RerankMediumMargin {
			scoreHead(cfg, head, finalSecondary)
			if cfg.LogDiagnostics {
				fmt.Fprintln(os.Stderr, "[newsroom-diag] adaptive_cascade=MEDIUM_EXIT")
			}
			return stitchTail(head, tail), nil
		}
		docs = finalDocs
	}

	scores, err := call(ctx, false, docs)
	if err != nil {
		return nil, err
	}
	scoreHead(cfg, head, scores)
	return stitchTail(head, tail), nil
}

func bundleScores(bundles []*recordBundle) []float64 {
	scores := make([]float64, len(bundles))
	for i, b := range bundles {
		scores[i] = b.FinalScore
	}
	return scores
}

func descendingOrder(scores []float64) []int {
	order := make([]int, len(scores))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return scores[order[i]] > scores[order[j]] })
	return order
}

func probabilityLogit(score float64) float64 {
	p := min(1-1e-12, max(1e-12, score))
	return math.Log(p) - math.Log1p(-p)
}

func standardize(scores []float64) []float64 {
	out := make([]float64, len(scores))
	var mean, variance float64
	for _, score := range scores {
		mean += score / float64(len(scores))
	}
	for _, score := range scores {
		delta := score - mean
		variance += delta * delta / float64(len(scores))
	}
	if deviation := math.Sqrt(variance); deviation > 1e-12 {
		for i, score := range scores {
			out[i] = (score - mean) / deviation
		}
	}
	return out
}

func rankProb(scores []float64) []float64 {
	out := make([]float64, len(scores))
	order := descendingOrder(scores)
	for start := 0; start < len(order); {
		end := start + 1
		for end < len(order) && scores[order[end]] == scores[order[start]] {
			end++
		}
		rank := 1 - float64(start+end-1)/(2*float64(max(1, len(scores)-1)))
		for _, idx := range order[start:end] {
			out[idx] = (rank + clamp01(scores[idx])) / 2
		}
		start = end
	}
	return out
}

func scoreEnsemble(cfg configuration, head []*recordBundle, primary, secondary rerankScores) {
	stage1 := bundleScores(head)
	primaryValues, secondaryValues := primary.probabilities, secondary.probabilities
	if cfg.RerankEnsemble == "logit" {
		primaryValues = standardize(primary.logits)
		secondaryValues = standardize(secondary.logits)
		stage1 = standardize(stage1)
	} else {
		primaryValues = rankProb(primary.probabilities)
		secondaryValues = rankProb(secondary.probabilities)
		stage1 = minMaxNormalize(stage1)
	}
	for i, b := range head {
		b.FinalScore = cfg.RerankPrimaryWeight*primaryValues[i] + cfg.RerankSecondaryWeight*secondaryValues[i] + cfg.RerankStage1Weight*stage1[i]
		b.Explanation = "newsroom+deep(" + cfg.RerankEnsemble + "," + cfg.SurrogateMode + ")"
	}
}
