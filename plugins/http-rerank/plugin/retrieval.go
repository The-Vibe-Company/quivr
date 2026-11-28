package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
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

type retriever struct {
	client *http.Client
	apiKey string
}

// New creates a retriever for the plugin SDK. It reads its optional secret once at startup.
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
	if time.Until(deadline) <= 0 {
		err = errors.New("insufficient remaining search time for reranking")
	} else {
		callCtx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		docs := make([]string, len(candidates))
		for i, candidate := range candidates {
			docs[i] = document(candidate, cfg)
		}
		scores, err = r.rerank(callCtx, cfg, req.Query.Text, docs)
	}
	if err != nil {
		if !cfg.FallbackOnError {
			return nil, &quivrplugin.SearchError{Code: "reranker_unavailable", Message: err.Error(), Retryable: true}
		}
		hits := make([]quivrplugin.RankedHit, min(len(servedCandidates), req.Limit))
		for i, c := range servedCandidates[:len(hits)] {
			hits[i] = quivrplugin.RankedHit{SegmentID: c.SegmentID, Score: c.Score, Explanation: "base profile fallback: " + err.Error()}
		}
		return quivrplugin.Rank(hits...), nil
	}
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
	text := c.PassageText
	if text == "" {
		text = c.Text
	} else if cfg.IncludeTitle && c.PartKey == "title" && c.Text != "" && !strings.HasPrefix(text, c.Text) {
		// A title anchor can add context. Preserve the canonical passage:
		// the SDK does not label the role of Parts already in that passage.
		text = c.Text + "\n\n" + text
	}
	count := 0
	for offset := range text {
		if count == cfg.MaxDocChars {
			return text[:offset]
		}
		count++
	}
	return text
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
