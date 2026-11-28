package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// profileLimit is the Plugin API's maximum for PrimitiveProfile.
const profileLimit = 50

// The engine reports a time snapshot from before this round was sent.
// Leave time to return the base ranking before its deadline expires.
const responseMargin = 50 * time.Millisecond

type configuration struct {
	Endpoint        string  `json:"endpoint"`
	Protocol        string  `json:"protocol"`
	Model           string  `json:"model"`
	BaseProfile     string  `json:"base_profile"`
	CandidateCount  int     `json:"candidate_count"`
	Ranking         string  `json:"ranking"`
	RerankWeight    float64 `json:"rerank_weight"`
	MaxDocChars     int     `json:"max_doc_chars"`
	IncludeTitle    bool    `json:"include_title"`
	TimeoutMS       int     `json:"timeout_ms"`
	FallbackOnError bool    `json:"fallback_on_error"`
}

func readConfig(raw json.RawMessage) (configuration, error) {
	cfg := configuration{
		Protocol: "tei", BaseProfile: "core.retrieve/default", CandidateCount: 50,
		Ranking: "rerank_only", RerankWeight: 0.7, MaxDocChars: 4000, IncludeTitle: true,
		TimeoutMS: 2500, FallbackOnError: true,
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, quivrplugin.TerminalSearchError("invalid_configuration", "invalid reranker configuration")
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" ||
		strings.HasSuffix(endpoint.Host, ":") || strings.ContainsAny(endpoint.Host, " \t\r\n") {
		return cfg, quivrplugin.TerminalSearchError("invalid_configuration", "endpoint must be an absolute HTTP(S) URL without credentials or fragments")
	}
	if port := endpoint.Port(); port != "" {
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return cfg, quivrplugin.TerminalSearchError("invalid_configuration", "endpoint port must be at most 65535")
		}
	}
	if cfg.Protocol == "cohere" && strings.TrimSpace(cfg.Model) == "" {
		return cfg, quivrplugin.TerminalSearchError("invalid_configuration", "cohere protocol requires a model")
	}
	return cfg, nil
}

// DiagnosticsSnapshot holds a point-in-time view of reranker counters.
type DiagnosticsSnapshot struct {
	Requests              int64
	Succeeded             int64
	TitleAnchorsFormatted int64
	Fallbacks             int64
	Errors                int64
	BudgetTimeouts        int64
	RequestTimeouts       int64
	BodyReadTimeouts      int64
}

type diagnostics struct {
	requests              atomic.Int64
	succeeded             atomic.Int64
	titleAnchorsFormatted atomic.Int64
	fallbacks             atomic.Int64
	errors                atomic.Int64
	budgetTimeouts        atomic.Int64
	requestTimeouts       atomic.Int64
	bodyReadTimeouts      atomic.Int64
}

func (d *diagnostics) snapshot() DiagnosticsSnapshot {
	if d == nil {
		return DiagnosticsSnapshot{}
	}
	return DiagnosticsSnapshot{
		Requests:              d.requests.Load(),
		Succeeded:             d.succeeded.Load(),
		TitleAnchorsFormatted: d.titleAnchorsFormatted.Load(),
		Fallbacks:             d.fallbacks.Load(),
		Errors:                d.errors.Load(),
		BudgetTimeouts:        d.budgetTimeouts.Load(),
		RequestTimeouts:       d.requestTimeouts.Load(),
		BodyReadTimeouts:      d.bodyReadTimeouts.Load(),
	}
}

func (d *diagnostics) recordTimeout(phase string) {
	if d == nil {
		return
	}
	switch phase {
	case timeoutPhaseBudget:
		d.budgetTimeouts.Add(1)
	case timeoutPhaseHTTPRoundTrip:
		d.requestTimeouts.Add(1)
	case timeoutPhaseResponseRead:
		d.bodyReadTimeouts.Add(1)
	}
}

type retriever struct {
	client *http.Client
	apiKey string
	stats  *diagnostics
	log    *slog.Logger
}

// New creates a retriever for the plugin SDK. It reads its optional secret once at startup.
func New() quivrplugin.Retriever {
	return retriever{
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		apiKey: os.Getenv("RERANKER_API_KEY"),
		stats:  &diagnostics{},
		log:    slog.Default(),
	}
}

func (r retriever) Diagnostics() DiagnosticsSnapshot {
	return r.stats.snapshot()
}

func (r retriever) logger() *slog.Logger {
	if r.log != nil {
		return r.log
	}
	return slog.Default()
}

func (r retriever) Search(ctx context.Context, req *quivrplugin.SearchRequest) (*quivrplugin.SearchAnswer, error) {
	started := time.Now()
	cfg, err := readConfig(req.Configuration)
	if err != nil {
		return nil, err
	}
	depth := min(cfg.CandidateCount, profileLimit)
	if req.Round == 1 {
		return quivrplugin.Ask(quivrplugin.CandidateRequest{
			Primitive: quivrplugin.PrimitiveProfile,
			Profile:   &quivrplugin.ProfileCandidates{Name: cfg.BaseProfile, Limit: depth},
		}), nil
	}
	var candidates []quivrplugin.Candidate
	for _, served := range req.Served {
		p := served.Request.Profile
		if served.Round == 1 && served.Request.Primitive == quivrplugin.PrimitiveProfile && p != nil && p.Name == cfg.BaseProfile {
			candidates = served.Candidates[:min(depth, len(served.Candidates))]
			break
		}
	}
	if len(candidates) == 0 {
		return quivrplugin.Rank(), nil
	}
	if r.stats != nil {
		r.stats.requests.Add(1)
	}

	deadline := time.Now().Add(time.Duration(cfg.TimeoutMS) * time.Millisecond)
	if req.Budget != nil {
		budgetDeadline := started.Add(time.Duration(req.Budget.RemainingTimeMS)*time.Millisecond - responseMargin)
		if budgetDeadline.Before(deadline) {
			deadline = budgetDeadline
		}
	}
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Add(-responseMargin).Before(deadline) {
		deadline = parentDeadline.Add(-responseMargin)
	}
	var scores []float64
	timeoutPhase := timeoutPhaseNone
	var titleFormatted int
	if time.Until(deadline) <= 0 {
		timeoutPhase = timeoutPhaseBudget
		err = errors.New("insufficient remaining search time for reranking")
	} else {
		callCtx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		docs := make([]string, len(candidates))
		for i, candidate := range candidates {
			var formatted bool
			docs[i], formatted = formatDocument(candidate, cfg)
			if formatted {
				titleFormatted++
			}
		}
		if r.stats != nil && titleFormatted > 0 {
			r.stats.titleAnchorsFormatted.Add(int64(titleFormatted))
		}
		scores, timeoutPhase, err = r.rerank(callCtx, cfg, req.Query.Text, docs)
	}
	if err != nil {
		r.stats.recordTimeout(timeoutPhase)
		if !cfg.FallbackOnError {
			if r.stats != nil {
				r.stats.errors.Add(1)
			}
			r.logger().WarnContext(ctx, "http reranker failed",
				"component", "http.rerank", "protocol", cfg.Protocol, "candidates", len(candidates),
				"title_anchors_formatted", titleFormatted, "timeout_phase", timeoutPhase,
				"degraded", false, "elapsed_ms", time.Since(started).Milliseconds(), "error", err.Error())
			return nil, &quivrplugin.SearchError{Code: "reranker_unavailable", Message: err.Error(), Retryable: true}
		}
		var fallbackCount int64
		if r.stats != nil {
			fallbackCount = r.stats.fallbacks.Add(1)
		}
		r.logger().WarnContext(ctx, "http reranker degraded to base profile",
			"component", "http.rerank", "protocol", cfg.Protocol, "candidates", len(candidates),
			"title_anchors_formatted", titleFormatted, "timeout_phase", timeoutPhase,
			"degraded", true, "fallback_count", fallbackCount, "elapsed_ms", time.Since(started).Milliseconds(), "error", err.Error())
		hits := make([]quivrplugin.RankedHit, min(len(candidates), req.Limit))
		for i, c := range candidates[:len(hits)] {
			hits[i] = quivrplugin.RankedHit{SegmentID: c.SegmentID, Score: c.Score, Explanation: "base profile fallback: " + err.Error()}
		}
		return quivrplugin.Rank(hits...), nil
	}
	if r.stats != nil {
		r.stats.succeeded.Add(1)
	}
	hits := rank(cfg, candidates, scores)
	return quivrplugin.Rank(hits[:min(len(hits), req.Limit)]...), nil
}

func document(c quivrplugin.Candidate, cfg configuration) string {
	text, _ := formatDocument(c, cfg)
	return text
}

func formatDocument(c quivrplugin.Candidate, cfg configuration) (string, bool) {
	text := c.PassageText
	titleFormatted := false
	if text == "" {
		text = c.Text
	} else if cfg.IncludeTitle && c.PartKey == "title" && c.Text != "" && !strings.HasPrefix(text, c.Text) {
		// A title anchor can add context. Preserve the canonical passage:
		// the SDK does not label the role of Parts already in that passage.
		text = c.Text + "\n\n" + text
		titleFormatted = true
	}
	count := 0
	for offset := range text {
		if count == cfg.MaxDocChars {
			return text[:offset], titleFormatted
		}
		count++
	}
	return text, titleFormatted
}

func rank(cfg configuration, candidates []quivrplugin.Candidate, scores []float64) []quivrplugin.RankedHit {
	var ranks []int
	var normalizedRerank, normalizedBase []float64
	switch cfg.Ranking {
	case "rrf":
		order := make([]int, len(scores))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(i, j int) bool { return scores[order[i]] > scores[order[j]] })
		ranks = make([]int, len(scores))
		for i, index := range order {
			ranks[index] = i + 1
		}
	case "linear":
		base := make([]float64, len(scores))
		for i, c := range candidates {
			base[i] = c.Score
		}
		normalizedBase = normalize(base)
		normalizedRerank = normalize(scores)
	}
	hits := make([]quivrplugin.RankedHit, len(scores))
	for i, c := range candidates {
		score := scores[i]
		switch cfg.Ranking {
		case "rrf":
			score = 1/float64(61+i) + 1/float64(60+ranks[i])
		case "linear":
			score = cfg.RerankWeight*normalizedRerank[i] + (1-cfg.RerankWeight)*normalizedBase[i]
		}
		hits[i] = quivrplugin.RankedHit{SegmentID: c.SegmentID, Score: score, Explanation: "HTTP reranker: " + cfg.Ranking}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	return hits
}

func normalize(scores []float64) []float64 {
	low, high := slices.Min(scores), slices.Max(scores)
	values := make([]float64, len(scores))
	if low == high {
		return values
	}
	scale := 1.0
	if math.IsInf(high-low, 0) {
		scale = 0.5
	}
	for i, score := range scores {
		values[i] = (score*scale - low*scale) / (high*scale - low*scale)
	}
	return values
}
