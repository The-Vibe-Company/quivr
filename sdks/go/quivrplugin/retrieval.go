package quivrplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// Retriever answers searches in rounds (the retrieval Contribution, Plugin
// API 0.7). Each round it either asks the core for candidates or returns the
// final ranking, chosen among the candidates the core served in this search.
// It must be deterministic: the same request yields the same answer.
type Retriever interface {
	Search(ctx context.Context, req *SearchRequest) (*SearchAnswer, error)
}

// Candidate primitives and fields.
const (
	PrimitiveBM25       = "bm25"
	PrimitiveNearVector = "near_vector"
	PrimitiveHybrid     = "hybrid"
	PrimitiveProfile    = "profile"
	FieldSource         = "source"
	FieldLexical        = "lexical"
)

// CandidateRequest asks the core for candidates.
type CandidateRequest struct {
	Primitive string             `json:"primitive"`
	QueryText string             `json:"query_text,omitempty"`
	Vector    []float32          `json:"vector,omitempty"`
	Space     string             `json:"space,omitempty"`
	Field     string             `json:"field,omitempty"`
	Alpha     *float64           `json:"alpha,omitempty"`
	Fusion    string             `json:"fusion,omitempty"`
	K         int                `json:"k,omitempty"`
	Profile   *ProfileCandidates `json:"profile,omitempty"`
	Filter    *CandidateFilter   `json:"filter,omitempty"`
	GroupBy   string             `json:"group_by,omitempty"`
}

// ProfileCandidates asks for a full profile declared in the manifest's requires.
type ProfileCandidates struct {
	Name  string `json:"name"`
	Query string `json:"query,omitempty"`
	Mode  string `json:"mode,omitempty"`
	Limit int    `json:"limit"`
}

// SearchBudget covers this profile and its dependencies. Report this plugin's own usage.
type SearchBudget struct {
	RemainingTimeMS    int     `json:"remaining_time_ms"`
	RemainingCostCents float64 `json:"remaining_cost_cents"`
}

// CandidateFilter narrows a request within the search's scope.
type CandidateFilter struct {
	SourceNamespaces []string `json:"source_namespaces,omitempty"`
}

// Candidate is a segment the core served: authorized, current, hydrated.
type Candidate struct {
	SegmentID       string        `json:"segment_id"`
	RecordID        string        `json:"record_id"`
	VersionID       string        `json:"version_id"`
	PartKey         string        `json:"part_key"`
	Text            string        `json:"text"`
	Start           int           `json:"start"`
	End             int           `json:"end"`
	PassageText     string        `json:"passage_text,omitempty"`
	SourceRanges    []SourceRange `json:"source_ranges,omitempty"`
	SourceSeparator string        `json:"source_separator,omitempty"`
	Score           float64       `json:"score"`
	Explanation     string        `json:"explanation,omitempty"`
}

// ServedRequest is a request of an earlier round and what the core served.
type ServedRequest struct {
	Round        int              `json:"round"`
	RequestIndex int              `json:"request_index"`
	Request      CandidateRequest `json:"request"`
	Candidates   []Candidate      `json:"candidates"`
}

// SearchSpace is a vector space the search may name.
type SearchSpace struct {
	ID    string `json:"id"`
	Owner struct {
		Kind          string `json:"kind"`
		PluginID      string `json:"plugin_id,omitempty"`
		PluginVersion string `json:"plugin_version,omitempty"`
	} `json:"owner"`
	Model           string   `json:"model"`
	Dimensions      int      `json:"dimensions"`
	Metric          string   `json:"metric"`
	Indexes         []string `json:"indexes"`
	QueryModalities []string `json:"query_modalities"`
	Role            string   `json:"role"`
	Coverage        struct {
		Segments int64  `json:"segments"`
		Total    int64  `json:"total"`
		Unknown  bool   `json:"unknown,omitempty"`
		AgeMS    *int64 `json:"age_ms,omitempty"`
	} `json:"coverage"`
}

// SearchRequest is a validated round of a search.
type SearchRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Profile        string          `json:"profile"`
	Round          int             `json:"round"`
	Query          struct {
		Text string `json:"text"`
		Mode string `json:"mode"`
	} `json:"query"`
	Limit int `json:"limit"`
	Scope struct {
		CorpusIDs        []string `json:"corpus_ids"`
		SourceNamespaces []string `json:"source_namespaces,omitempty"`
	} `json:"scope"`
	// Spaces are the vector spaces near_vector and hybrid may name, the
	// served one first.
	Spaces []SearchSpace `json:"spaces"`
	// Served holds every request of earlier rounds with its candidates.
	Served []ServedRequest `json:"served"`
	Budget *SearchBudget   `json:"budget,omitempty"`
	logger *slog.Logger
}

// Logger returns the request logger.
func (r *SearchRequest) Logger() *slog.Logger { return r.logger }

// ServedSpace is the served space, or nil when the search offers none.
func (r *SearchRequest) ServedSpace() *SearchSpace {
	for i := range r.Spaces {
		if r.Spaces[i].Role == "served" {
			return &r.Spaces[i]
		}
	}
	return nil
}

// RankedHit is one hit of the final ranking.
type RankedHit struct {
	SegmentID   string  `json:"segment_id"`
	Score       float64 `json:"score"`
	Explanation string  `json:"explanation,omitempty"`
}

// Usage is what a round spent on paid calls.
type Usage struct {
	PaidCalls int     `json:"paid_calls,omitempty"`
	CostCents float64 `json:"cost_cents,omitempty"`
}

// SearchAnswer is either candidate requests or the final ranking.
type SearchAnswer struct {
	Requests []CandidateRequest
	// Ranking is the final answer, best first; it is used when Requests is
	// empty, and may itself be empty.
	Ranking []RankedHit
	Usage   *Usage
}

// Ask answers a round with candidate requests.
func Ask(requests ...CandidateRequest) *SearchAnswer { return &SearchAnswer{Requests: requests} }

// Rank answers a round with the final ranking.
func Rank(hits ...RankedHit) *SearchAnswer {
	if hits == nil {
		hits = []RankedHit{}
	}
	return &SearchAnswer{Ranking: hits}
}

// SearchError is a failure of a Retriever. A retryable one (a backend
// outage) makes the search unavailable; a terminal one refuses the query.
type SearchError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *SearchError) Error() string { return e.Code + ": " + e.Message }

// TerminalSearchError reports a query the plugin can never answer.
func TerminalSearchError(code, message string) *SearchError {
	return &SearchError{Code: code, Message: message}
}

// Retrieval registers the implementation of the declared retrieval
// Contribution.
func (p *Plugin) Retrieval(impl Retriever) error {
	if p.m.Retrieval == nil {
		return fmt.Errorf("the manifest declares no retrieval Contribution")
	}
	p.retriever = impl
	return nil
}

func (p *Plugin) serveSearch(w http.ResponseWriter, r *http.Request) {
	var req SearchRequest
	if !p.readIngestion(w, r, "plugins/v0/retrieval-search-request.schema.json", &req) {
		return
	}
	_, ok := p.m.Retrieval.Profiles[req.Profile]
	if !ok {
		refuse(w, 400, "unknown_profile", fmt.Sprintf("profile %q is not declared by %s", req.Profile, p.m.ID), Credential{})
		return
	}
	req.logger = p.requestLogger(r.Context(), Credential{}, req.InvocationID).With("round", req.Round)
	defer p.ingestPanic(w, req.logger)
	// max_latency_ms is the profile's latency objective, not a deadline: the
	// engine ends the request, and so this context, at its hard bound.
	answer, err := p.retriever.Search(r.Context(), &req)
	if err != nil {
		var e *SearchError
		if errors.As(err, &e) {
			status := 422
			if e.Retryable {
				status = 503
			}
			writeJSON(w, status, envelope{Code: e.Code, Message: truncate(e.Message), Retryable: e.Retryable})
			return
		}
		req.logger.Error("search failed with an unclassified error", "error", err.Error())
		writeJSON(w, 503, envelope{Code: "unexpected_error", Message: "the plugin failed with an unclassified error; see the plugin log", Retryable: true})
		return
	}
	body, problem := p.encodeSearch(&req, answer)
	if problem != "" {
		req.logger.Error("the retriever returned an answer the core would refuse", "problem", problem)
		writeJSON(w, 500, envelope{Code: "invalid_response", Message: truncate(problem), Retryable: false})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(body)
}

// encodeSearch encodes an answer and checks it the way the core will: no
// requests in the last round, request and k limits, and a ranking of served
// candidates only, each once, at most limit.
func (p *Plugin) encodeSearch(req *SearchRequest, a *SearchAnswer) ([]byte, string) {
	rc := p.m.Retrieval
	if a == nil {
		return nil, "Search returned no answer; return Rank() for an empty ranking"
	}
	doc := map[string]any{}
	if len(a.Requests) > 0 {
		if req.Round >= min(rc.Limits.MaxRounds, 3) {
			return nil, fmt.Sprintf("round %d is the last one; answer a ranking", req.Round)
		}
		if len(a.Requests) > rc.Limits.MaxRequests {
			return nil, fmt.Sprintf("%d requests exceed max_requests %d", len(a.Requests), rc.Limits.MaxRequests)
		}
		for i, c := range a.Requests {
			if c.Primitive == PrimitiveProfile {
				declared := false
				if c.Profile != nil {
					for _, r := range p.m.Requires {
						for _, profile := range r.Profiles {
							declared = declared || c.Profile.Name == r.Plugin+"/"+profile
						}
					}
				}
				if !resolveAPIFeatures(p.m.pluginAPI).speaks("profile_candidates") || !declared {
					return nil, fmt.Sprintf("request %d: profile must be declared in requires and supported by this Plugin API", i)
				}
			}
			limit := c.K
			if c.Profile != nil {
				limit = c.Profile.Limit
			}
			if limit < 1 || limit > rc.Limits.MaxCandidates {
				return nil, fmt.Sprintf("request %d: k %d is outside 1..max_candidates %d", i, c.K, rc.Limits.MaxCandidates)
			}
		}
		doc["requests"] = a.Requests
	} else {
		served := map[string]bool{}
		for _, s := range req.Served {
			for _, c := range s.Candidates {
				served[c.SegmentID] = true
			}
		}
		if len(a.Ranking) > req.Limit {
			return nil, fmt.Sprintf("%d hits exceed the search's limit %d", len(a.Ranking), req.Limit)
		}
		seen := map[string]bool{}
		for i, h := range a.Ranking {
			if !served[h.SegmentID] || seen[h.SegmentID] {
				return nil, fmt.Sprintf("hit %d: segment %q was not served in this search, or is ranked twice", i, h.SegmentID)
			}
			seen[h.SegmentID] = true
		}
		hits := a.Ranking
		if hits == nil {
			hits = []RankedHit{}
		}
		doc["ranking"] = map[string]any{"hits": hits}
	}
	if a.Usage != nil {
		doc["usage"] = a.Usage
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, "the answer is not JSON-encodable: " + err.Error()
	}
	body := buf.Bytes()
	if len(body) > rc.Limits.MaxResponseBytes {
		return nil, fmt.Sprintf("the response is %d bytes; max_response_bytes is %d", len(body), rc.Limits.MaxResponseBytes)
	}
	if err := validate("plugins/v0/retrieval-search-response.schema.json", body); err != nil {
		return nil, "the answer does not match the response schema: " + err.Error()
	}
	return body, ""
}
