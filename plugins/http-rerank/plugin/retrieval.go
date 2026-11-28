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
}

// New creates a retriever for the plugin SDK. It reads its optional secret once at startup.
func New() quivrplugin.Retriever {
	return retriever{
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		apiKey: os.Getenv("RERANKER_API_KEY"),
		stats:  &diagnostics{},
	}
}

func (r retriever) Diagnostics() DiagnosticsSnapshot {
	return r.stats.snapshot()
}

func requestLogger(req *quivrplugin.SearchRequest) *slog.Logger {
	if req != nil && req.Logger() != nil {
		return req.Logger()
	}
	return slog.Default()
}

func (r retriever) Search(ctx context.Context, req *quivrplugin.SearchRequest) (*quivrplugin.SearchAnswer, error) {
	started := time.Now()
	cfg, err := readConfig(req.Configuration)
	if err != nil {
		return nil, err
	}
	fetchDepth := min(max(cfg.CandidateCount, req.Limit), profileLimit)
	if req.Round == 1 {
		return quivrplugin.Ask(quivrplugin.CandidateRequest{
			Primitive: quivrplugin.PrimitiveProfile,
			Profile:   &quivrplugin.ProfileCandidates{Name: cfg.BaseProfile, Limit: fetchDepth},
		}), nil
	}
	var servedCandidates []quivrplugin.Candidate
	for _, served := range req.Served {
		p := served.Request.Profile
		if served.Round == 1 && served.Request.Primitive == quivrplugin.PrimitiveProfile && p != nil && p.Name == cfg.BaseProfile {
			servedCandidates = served.Candidates[:min(fetchDepth, len(served.Candidates))]
			break
		}
	}
	if len(servedCandidates) == 0 {
		return quivrplugin.Rank(), nil
	}
	rerankCount := min(max(cfg.CandidateCount, 0), len(servedCandidates))
	candidates := servedCandidates[:rerankCount]
	tail := servedCandidates[rerankCount:]
	if len(candidates) == 0 {
		hits := appendTail(nil, tail)
		return quivrplugin.Rank(hits[:min(len(hits), req.Limit)]...), nil
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
	log := requestLogger(req)
	if err != nil {
		r.stats.recordTimeout(timeoutPhase)
		if !cfg.FallbackOnError {
			if r.stats != nil {
				r.stats.errors.Add(1)
			}
			snap := r.stats.snapshot()
			log.WarnContext(ctx, "http reranker failed",
				"component", "http.rerank", "protocol", cfg.Protocol, "candidates", len(candidates),
				"title_anchors_formatted", titleFormatted, "timeout_phase", timeoutPhase,
				"degraded", false, "elapsed_ms", time.Since(started).Milliseconds(),
				"requests_total", snap.Requests, "errors_total", snap.Errors, "fallbacks_total", snap.Fallbacks,
				"title_anchors_formatted_total", snap.TitleAnchorsFormatted,
				"budget_timeouts_total", snap.BudgetTimeouts, "request_timeouts_total", snap.RequestTimeouts, "body_read_timeouts_total", snap.BodyReadTimeouts,
				"error", err.Error())
			return nil, &quivrplugin.SearchError{Code: "reranker_unavailable", Message: err.Error(), Retryable: true}
		}
		if r.stats != nil {
			r.stats.fallbacks.Add(1)
		}
		snap := r.stats.snapshot()
		log.WarnContext(ctx, "http reranker degraded to base profile",
			"component", "http.rerank", "protocol", cfg.Protocol, "candidates", len(candidates),
			"title_anchors_formatted", titleFormatted, "timeout_phase", timeoutPhase,
			"degraded", true, "fallback_count", snap.Fallbacks, "elapsed_ms", time.Since(started).Milliseconds(),
			"requests_total", snap.Requests, "errors_total", snap.Errors, "fallbacks_total", snap.Fallbacks,
			"title_anchors_formatted_total", snap.TitleAnchorsFormatted,
			"budget_timeouts_total", snap.BudgetTimeouts, "request_timeouts_total", snap.RequestTimeouts, "body_read_timeouts_total", snap.BodyReadTimeouts,
			"error", err.Error())
		hits := make([]quivrplugin.RankedHit, min(len(servedCandidates), req.Limit))
		for i, c := range servedCandidates[:len(hits)] {
			hits[i] = quivrplugin.RankedHit{SegmentID: c.SegmentID, Score: c.Score, Explanation: "base profile fallback: " + err.Error()}
		}
		return quivrplugin.Rank(hits...), nil
	}
	if r.stats != nil {
		r.stats.succeeded.Add(1)
	}
	snap := r.stats.snapshot()
	log.DebugContext(ctx, "http reranker completed",
		"component", "http.rerank", "protocol", cfg.Protocol, "candidates", len(candidates),
		"title_anchors_formatted", titleFormatted, "elapsed_ms", time.Since(started).Milliseconds(),
		"requests_total", snap.Requests, "succeeded_total", snap.Succeeded, "fallbacks_total", snap.Fallbacks,
		"errors_total", snap.Errors, "title_anchors_formatted_total", snap.TitleAnchorsFormatted)
	hits := appendTail(rank(cfg, candidates, scores), tail)
	return quivrplugin.Rank(hits[:min(len(hits), req.Limit)]...), nil
}

// appendTail appends untouched base-ranker tail candidates below the reranked
// head with strictly monotonic descending scores so Recall@K is never truncated
// when candidate_count < limit.
func appendTail(hits []quivrplugin.RankedHit, tail []quivrplugin.Candidate) []quivrplugin.RankedHit {
	if len(tail) == 0 {
		return hits
	}
	if len(hits) == 0 {
		out := make([]quivrplugin.RankedHit, 0, len(tail))
		for _, c := range tail {
			out = append(out, quivrplugin.RankedHit{SegmentID: c.SegmentID, Score: c.Score, Explanation: "base profile tail"})
		}
		return out
	}
	floor := hits[len(hits)-1].Score
	step := max(1e-6, math.Abs(floor)*1e-6)
	if floor > 0 {
		if scaled := floor / float64(len(tail)+1); scaled < step && scaled > 0 {
			step = scaled
		}
	}
	prev := floor
	for i, c := range tail {
		score := floor - float64(i+1)*step
		if !(score < prev) {
			score = math.Nextafter(prev, math.Inf(-1))
		}
		prev = score
		hits = append(hits, quivrplugin.RankedHit{
			SegmentID:   c.SegmentID,
			Score:       score,
			Explanation: "base profile tail",
		})
	}
	return hits
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
