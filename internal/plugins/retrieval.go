package plugins

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"
)

// SearchRoute is the route of the retrieval Contribution.
const SearchRoute = "/v0/contributions/retrieval/search"

// DefaultProfile is the profile every retrieval plugin declares; it answers a
// search that names no profile.
const DefaultProfile = "default"

// Retrieval defaults and engine caps.
const (
	// MaxRetrievalRounds caps the rounds of one search, whatever the manifest
	// declares: the last round must answer a ranking.
	MaxRetrievalRounds               = 3
	DefaultMaxRetrievalRequests      = 4
	DefaultMaxRetrievalCandidates    = 50
	DefaultRetrievalMaxResponseBytes = 1 << 20
	EngineRetrievalMaxResponseBytes  = 4 << 20
)

// Candidate primitives, fields and fusions a retrieval plugin may request.
const (
	PrimitiveBM25       = "bm25"
	PrimitiveNearVector = "near_vector"
	PrimitiveHybrid     = "hybrid"
	FieldSource         = "source"
	FieldLexical        = "lexical"
	FusionRelativeScore = "relative_score"
	FusionRanked        = "ranked"
	GroupByRecord       = "record"
)

// Issue codes for retrieval answers.
const (
	CodeUnservedCandidate  = "unserved_candidate"
	CodeDuplicateHit       = "duplicate_hit"
	CodeTooManyHits        = "too_many_hits"
	CodeTooManyRounds      = "too_many_rounds"
	CodeTooManyRequests    = "too_many_requests"
	CodeCandidateLimit     = "candidate_limit"
	CodeUnknownSpace       = "unknown_space"
	CodeFilterOutsideScope = "filter_outside_scope"
	CodeOverBudget         = "over_budget"
)

// Retrieval answers searches in rounds: each round asks the core for
// candidates or returns the final ranking among served candidates.
type Retrieval struct {
	Profiles map[string]RetrievalProfile `json:"profiles"`
	Limits   RetrievalLimits             `json:"limits"`
}

// RetrievalProfile is one declared search profile and its budgets.
type RetrievalProfile struct {
	Description  string  `json:"description,omitempty"`
	MaxLatencyMS int     `json:"max_latency_ms"`
	MaxCostCents float64 `json:"max_cost_cents"`
}

// Objective is the profile's latency objective, max_latency_ms: a search
// that takes longer still answers, and is reported as over its objective.
func (p RetrievalProfile) Objective() time.Duration {
	return time.Duration(p.MaxLatencyMS) * time.Millisecond
}

// retrievalDeadlineCap keeps every search bound under the api's 10 s HTTP
// write timeout, so the error that ends a search always reaches the client.
const retrievalDeadlineCap = 9 * time.Second

// Deadline is the hard bound of one search under the profile: four times its
// objective, at least 2 s, at most 9 s (THE-813). The objective is a p95
// target, so a deadline equal to it would fail a slice of healthy searches
// under load; the bound only stops a search whose plugin or dependency is
// stuck.
func (p RetrievalProfile) Deadline() time.Duration {
	return min(max(4*p.Objective(), 2*time.Second), retrievalDeadlineCap)
}

// RetrievalLimits bound one search.
type RetrievalLimits struct {
	MaxRounds        int `json:"max_rounds"`
	MaxRequests      int `json:"max_requests"`
	MaxCandidates    int `json:"max_candidates"`
	MaxResponseBytes int `json:"max_response_bytes"`
}

// ProfileNames lists the declared profiles, default first, then by name.
func (r *Retrieval) ProfileNames() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.Profiles))
	for name := range r.Profiles {
		if name != DefaultProfile {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if _, ok := r.Profiles[DefaultProfile]; ok {
		names = append([]string{DefaultProfile}, names...)
	}
	return names
}

func retrievalOf(m *Manifest) *Retrieval {
	if m == nil {
		return nil
	}
	return m.Contributions.Retrieval
}

// RetrievalMaxResponseBytes is the declared max_response_bytes of the
// retrieval Contribution, capped by EngineRetrievalMaxResponseBytes.
func RetrievalMaxResponseBytes(m *Manifest) int {
	limit := DefaultRetrievalMaxResponseBytes
	if r := retrievalOf(m); r != nil && r.Limits.MaxResponseBytes > 0 {
		limit = r.Limits.MaxResponseBytes
	}
	return min(limit, EngineRetrievalMaxResponseBytes)
}

// RetrievalMaxRounds is the declared max_rounds, capped by MaxRetrievalRounds.
func RetrievalMaxRounds(m *Manifest) int {
	rounds := MaxRetrievalRounds
	if r := retrievalOf(m); r != nil && r.Limits.MaxRounds > 0 {
		rounds = r.Limits.MaxRounds
	}
	return min(rounds, MaxRetrievalRounds)
}

func retrievalMaxRequests(m *Manifest) int {
	if r := retrievalOf(m); r != nil && r.Limits.MaxRequests > 0 {
		return r.Limits.MaxRequests
	}
	return DefaultMaxRetrievalRequests
}

func retrievalMaxCandidates(m *Manifest) int {
	if r := retrievalOf(m); r != nil && r.Limits.MaxCandidates > 0 {
		return r.Limits.MaxCandidates
	}
	return DefaultMaxRetrievalCandidates
}

// CandidateRequest is one request for candidates.
type CandidateRequest struct {
	Primitive string           `json:"primitive"`
	QueryText string           `json:"query_text,omitempty"`
	Vector    []float64        `json:"vector,omitempty"`
	Space     string           `json:"space,omitempty"`
	Field     string           `json:"field,omitempty"`
	Alpha     *float64         `json:"alpha,omitempty"`
	Fusion    string           `json:"fusion,omitempty"`
	K         int              `json:"k"`
	Filter    *CandidateFilter `json:"filter,omitempty"`
	GroupBy   string           `json:"group_by,omitempty"`
}

// CandidateFilter narrows a request within the search's scope.
type CandidateFilter struct {
	SourceNamespaces []string `json:"source_namespaces,omitempty"`
}

// EffectiveField is the request's field, source by default.
func (c CandidateRequest) EffectiveField() string {
	if c.Field == "" {
		return FieldSource
	}
	return c.Field
}

// EffectiveAlpha is the hybrid weight of the vector side, 0.5 by default.
func (c CandidateRequest) EffectiveAlpha() float64 {
	if c.Alpha == nil {
		return 0.5
	}
	return *c.Alpha
}

// EffectiveFusion is the hybrid fusion, relative_score by default.
func (c CandidateRequest) EffectiveFusion() string {
	if c.Fusion == "" {
		return FusionRelativeScore
	}
	return c.Fusion
}

// Candidate is one served candidate: an authorized, current segment.
type Candidate struct {
	SegmentID string  `json:"segment_id"`
	RecordID  string  `json:"record_id"`
	VersionID string  `json:"version_id"`
	PartKey   string  `json:"part_key"`
	Text      string  `json:"text"`
	Start     int     `json:"start"`
	End       int     `json:"end"`
	Score     float64 `json:"score"`
}

// ServedRequest is a candidate request of an earlier round with what the core
// served for it.
type ServedRequest struct {
	Round        int              `json:"round"`
	RequestIndex int              `json:"request_index"`
	Request      CandidateRequest `json:"request"`
	Candidates   []Candidate      `json:"candidates"`
}

// SpaceOwner is the owner of a search space: the engine or a plugin.
type SpaceOwner struct {
	Kind          string `json:"kind"`
	PluginID      string `json:"plugin_id,omitempty"`
	PluginVersion string `json:"plugin_version,omitempty"`
}

// SpaceCoverage counts current segments with a vector in a space.
type SpaceCoverage struct {
	Segments int64 `json:"segments"`
	Total    int64 `json:"total"`
}

// SearchSpace is a vector space a search may name.
type SearchSpace struct {
	ID              string        `json:"id"`
	Owner           SpaceOwner    `json:"owner"`
	Model           string        `json:"model"`
	Dimensions      int           `json:"dimensions"`
	Metric          string        `json:"metric"`
	Indexes         []string      `json:"indexes"`
	QueryModalities []string      `json:"query_modalities"`
	Role            string        `json:"role"`
	Coverage        SpaceCoverage `json:"coverage"`
}

// SearchQuery is the client's query and mode.
type SearchQuery struct {
	Text string `json:"text"`
	Mode string `json:"mode"`
}

// SearchScope is what every candidate stays within.
type SearchScope struct {
	CorpusIDs        []string `json:"corpus_ids"`
	SourceNamespaces []string `json:"source_namespaces,omitempty"`
}

// SearchRequest is one round of one search.
type SearchRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Profile        string          `json:"profile"`
	Round          int             `json:"round"`
	Query          SearchQuery     `json:"query"`
	Limit          int             `json:"limit"`
	Scope          SearchScope     `json:"scope"`
	Spaces         []SearchSpace   `json:"spaces"`
	Served         []ServedRequest `json:"served"`
}

// RankedHit is one hit of a ranking.
type RankedHit struct {
	SegmentID   string  `json:"segment_id"`
	Score       float64 `json:"score"`
	Explanation string  `json:"explanation,omitempty"`
}

// SearchUsage is what a round, or a whole search, reports it spent.
type SearchUsage struct {
	PaidCalls int     `json:"paid_calls,omitempty"`
	CostCents float64 `json:"cost_cents,omitempty"`
}

// SearchRanking is the final answer of a search.
type SearchRanking struct {
	Hits []RankedHit `json:"hits"`
}

// SearchAnswer is a decoded answer: requests or a ranking.
type SearchAnswer struct {
	Requests []CandidateRequest `json:"requests,omitempty"`
	Ranking  *SearchRanking     `json:"ranking,omitempty"`
	Usage    *SearchUsage       `json:"usage,omitempty"`
}

// CheckSearchOutput judges a 200 search answer exactly as the engine does:
// the response bound, the schema, then the rules of the round. Requests: not
// in the last round, at most max_requests, k at most max_candidates, spaces
// the request offered, vectors with the space's dimensions, filters inside
// the scope. A ranking: at most limit hits, each a candidate served in an
// earlier round of this search, each once.
func CheckSearchOutput(raw []byte, request SearchRequest, m *Manifest) []Issue {
	if limit := RetrievalMaxResponseBytes(m); len(raw) > limit {
		return []Issue{{Code: CodeResponseTooLarge, Message: fmt.Sprintf("the response is %d bytes; the limit is %d (declared max_response_bytes, capped by the engine at %d)", len(raw), limit, EngineRetrievalMaxResponseBytes)}}
	}
	if issues := ValidateDocument("retrieval-search-response.schema.json", raw); len(issues) > 0 {
		return issues
	}
	answer, err := DecodeSearch(raw)
	if err != nil {
		return []Issue{{Code: CodeSchema, Message: err.Error()}}
	}
	if answer.Ranking != nil {
		return rankingIssues(answer.Ranking.Hits, request)
	}
	return requestIssues(answer.Requests, request, m)
}

func requestIssues(requests []CandidateRequest, request SearchRequest, m *Manifest) []Issue {
	var issues []Issue
	if rounds := RetrievalMaxRounds(m); request.Round >= rounds {
		issues = append(issues, Issue{Code: CodeTooManyRounds, Path: "/requests",
			Message: fmt.Sprintf("round %d of at most %d asks for more candidates; the last round must answer a ranking", request.Round, rounds)})
	}
	if limit := retrievalMaxRequests(m); len(requests) > limit {
		issues = append(issues, Issue{Code: CodeTooManyRequests, Path: "/requests",
			Message: fmt.Sprintf("%d candidate requests; the manifest declares at most %d per round (limits.max_requests)", len(requests), limit)})
	}
	spaces := map[string]SearchSpace{}
	ids := []string{}
	for _, s := range request.Spaces {
		spaces[s.ID] = s
		ids = append(ids, s.ID)
	}
	maxK := retrievalMaxCandidates(m)
	for i, c := range requests {
		path := fmt.Sprintf("/requests/%d", i)
		if c.K > maxK {
			issues = append(issues, Issue{Code: CodeCandidateLimit, Path: path + "/k",
				Message: fmt.Sprintf("k is %d; the manifest declares at most %d candidates per request (limits.max_candidates)", c.K, maxK)})
		}
		if c.Primitive != PrimitiveBM25 {
			space, known := spaces[c.Space]
			switch {
			case !known:
				issues = append(issues, Issue{Code: CodeUnknownSpace, Path: path + "/space",
					Message: fmt.Sprintf("space %q is not one of the spaces this search offers (%v)", c.Space, ids)})
			case c.Vector != nil && len(c.Vector) != space.Dimensions:
				issues = append(issues, Issue{Code: CodeDimensionMismatch, Path: path + "/vector",
					Message: fmt.Sprintf("%d numbers; space %q has %d dimensions", len(c.Vector), c.Space, space.Dimensions)})
			case c.Vector == nil && !slices.Contains(space.QueryModalities, "text"):
				issues = append(issues, Issue{Code: CodeUnknownSpace, Path: path + "/space",
					Message: fmt.Sprintf("space %q encodes no text query (query modalities %v)", c.Space, space.QueryModalities)})
			}
			for j, x := range c.Vector {
				if math.IsInf(float64(float32(x)), 0) {
					issues = append(issues, Issue{Code: CodeInvalidVector, Path: fmt.Sprintf("%s/vector/%d", path, j), Message: fmt.Sprintf("%v does not fit a finite 32-bit float", x)})
					break
				}
			}
		}
		if c.Filter != nil && len(request.Scope.SourceNamespaces) > 0 {
			for j, ns := range c.Filter.SourceNamespaces {
				if !slices.Contains(request.Scope.SourceNamespaces, ns) {
					issues = append(issues, Issue{Code: CodeFilterOutsideScope, Path: fmt.Sprintf("%s/filter/source_namespaces/%d", path, j),
						Message: fmt.Sprintf("Source Namespace %q is outside the search's scope %v; a filter narrows the scope, never widens it", ns, request.Scope.SourceNamespaces)})
				}
			}
		}
	}
	return issues
}

func rankingIssues(hits []RankedHit, request SearchRequest) []Issue {
	var issues []Issue
	if len(hits) > request.Limit {
		issues = append(issues, Issue{Code: CodeTooManyHits, Path: "/ranking/hits",
			Message: fmt.Sprintf("%d hits; the search asks for at most %d (limit)", len(hits), request.Limit)})
	}
	served := map[string]bool{}
	for _, s := range request.Served {
		for _, c := range s.Candidates {
			served[c.SegmentID] = true
		}
	}
	seen := map[string]int{}
	for i, h := range hits {
		path := fmt.Sprintf("/ranking/hits/%d/segment_id", i)
		if first, dup := seen[h.SegmentID]; dup {
			issues = append(issues, Issue{Code: CodeDuplicateHit, Path: path,
				Message: fmt.Sprintf("segment %q repeats /ranking/hits/%d; rank each segment once", h.SegmentID, first)})
			continue
		}
		seen[h.SegmentID] = i
		if !served[h.SegmentID] {
			issues = append(issues, Issue{Code: CodeUnservedCandidate, Path: path,
				Message: fmt.Sprintf("segment %q was never served in this search; a ranking holds only candidates the core served, which it has authorized", h.SegmentID)})
		}
	}
	return issues
}

// DecodeSearch decodes a search answer.
func DecodeSearch(raw []byte) (SearchAnswer, error) {
	var answer SearchAnswer
	err := json.Unmarshal(raw, &answer)
	return answer, err
}

// RetrievalSession carries one search across its rounds for the engine and
// the Contract Runner: the request of the current round, every candidate
// served so far, and the usage the plugin reported.
type RetrievalSession struct {
	m       *Manifest
	request SearchRequest
	usage   SearchUsage
}

// NewRetrievalSession starts a search at round 1 from its first request.
func NewRetrievalSession(m *Manifest, first SearchRequest) *RetrievalSession {
	first.Contribution, first.Round = "retrieval", 1
	if first.Served == nil {
		first.Served = []ServedRequest{}
	}
	if first.Spaces == nil {
		first.Spaces = []SearchSpace{}
	}
	if len(first.Configuration) == 0 {
		first.Configuration = json.RawMessage(`{}`)
	}
	return &RetrievalSession{m: m, request: first}
}

// Request is the request of the current round.
func (s *RetrievalSession) Request() SearchRequest { return s.request }

// Judge checks the current round's 200 answer and records its usage.
func (s *RetrievalSession) Judge(raw []byte) (SearchAnswer, []Issue) {
	if issues := CheckSearchOutput(raw, s.request, s.m); len(issues) > 0 {
		return SearchAnswer{}, issues
	}
	answer, err := DecodeSearch(raw)
	if err != nil {
		return SearchAnswer{}, []Issue{{Code: CodeSchema, Message: err.Error()}}
	}
	if answer.Usage != nil {
		s.usage.PaidCalls += answer.Usage.PaidCalls
		s.usage.CostCents += answer.Usage.CostCents
	}
	return answer, nil
}

// Serve records what the core served for the current round's requests, in
// order, and moves to the next round under a new invocation id.
func (s *RetrievalSession) Serve(invocationID string, requests []CandidateRequest, candidates [][]Candidate) {
	served := slices.Clone(s.request.Served)
	for i, r := range requests {
		c := candidates[i]
		if c == nil {
			c = []Candidate{}
		}
		served = append(served, ServedRequest{Round: s.request.Round, RequestIndex: i, Request: r, Candidates: c})
	}
	s.request.Served = served
	s.request.Round++
	s.request.InvocationID = invocationID
}

// Round is the number of the current round.
func (s *RetrievalSession) Round() int { return s.request.Round }

// Usage is what the plugin reported spending over every round so far.
func (s *RetrievalSession) Usage() SearchUsage { return s.usage }

// Served returns a served candidate by segment id.
func (s *RetrievalSession) Served(segmentID string) (Candidate, bool) {
	for _, r := range s.request.Served {
		for _, c := range r.Candidates {
			if c.SegmentID == segmentID {
				return c, true
			}
		}
	}
	return Candidate{}, false
}

// Budget reports a declared spend over the profile's max_cost_cents.
func (s *RetrievalSession) Budget() *Issue {
	r := retrievalOf(s.m)
	if r == nil {
		return nil
	}
	profile, ok := r.Profiles[s.request.Profile]
	if !ok || s.usage.CostCents <= profile.MaxCostCents {
		return nil
	}
	return &Issue{Code: CodeOverBudget, Path: "/usage/cost_cents",
		Message: fmt.Sprintf("the search reported %.4g cents; profile %q allows at most %.4g (max_cost_cents)", s.usage.CostCents, s.request.Profile, profile.MaxCostCents)}
}
