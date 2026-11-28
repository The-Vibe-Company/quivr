package plugins

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

// SearchRoute is the route of the retrieval Contribution.
const SearchRoute = "/v0/contributions/retrieval/search"

// DefaultProfile is the deployment alias used by searches that name no profile.
// A single plugin with a local default profile can supply it without a map.
const DefaultProfile = "default"

// Retrieval defaults and engine caps.
const (
	// MaxRetrievalRounds caps the rounds of one search, whatever the manifest
	// declares: the last round must answer a ranking.
	MaxRetrievalRounds               = 3
	DefaultMaxRetrievalRequests      = 4
	DefaultMaxRetrievalCandidates    = 50
	LegacyMaxRetrievalCandidates     = 100
	MaxRetrievalCandidates           = 400
	DefaultRetrievalMaxResponseBytes = 1 << 20
	EngineRetrievalMaxResponseBytes  = 4 << 20
)

// Candidate primitives, fields and fusions a retrieval plugin may request.
const (
	PrimitiveBM25       = "bm25"
	PrimitiveNearVector = "near_vector"
	PrimitiveHybrid     = "hybrid"
	PrimitiveProfile    = "profile"
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

// RetrievalDeadlineCap keeps every search bound (including any profile fallback)
// under the api's 30 s HTTP write timeout, so the response always reaches the client.
const RetrievalDeadlineCap = 25 * time.Second

// Deadline is the hard bound of one search under the profile: four times its
// objective, at least 2 s, at most 25 s (THE-813). The objective is a p95
// target, so a deadline equal to it would fail a slice of healthy searches
// under load; the bound only stops a search whose plugin or dependency is
// stuck.
func (p RetrievalProfile) Deadline() time.Duration {
	return min(max(4*p.Objective(), 2*time.Second), RetrievalDeadlineCap)
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
	Primitive   string             `json:"primitive"`
	QueryText   string             `json:"query_text,omitempty"`
	Vector      []float64          `json:"vector,omitempty"`
	Space       string             `json:"space,omitempty"`
	Field       string             `json:"field,omitempty"`
	Alpha       *float64           `json:"alpha,omitempty"`
	Fusion      string             `json:"fusion,omitempty"`
	K           int                `json:"k,omitempty"`
	Profile     *ProfileCandidates `json:"profile,omitempty"`
	Filter      *CandidateFilter   `json:"filter,omitempty"`
	GroupBy     string             `json:"group_by,omitempty"`
	IncludeLead bool               `json:"include_lead,omitempty"`
}

// ProfileCandidates asks the engine to run a declared full profile name.
type ProfileCandidates struct {
	Name  string `json:"name"`
	Query string `json:"query,omitempty"`
	Mode  string `json:"mode,omitempty"`
	Limit int    `json:"limit"`
}

func (c CandidateRequest) Limit() int {
	if c.Profile != nil {
		return c.Profile.Limit
	}
	return c.K
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
	SegmentID       string          `json:"segment_id"`
	RecordID        string          `json:"record_id"`
	VersionID       string          `json:"version_id"`
	PartKey         string          `json:"part_key"`
	Text            string          `json:"text"`
	Start           int             `json:"start"`
	End             int             `json:"end"`
	PassageText     string          `json:"passage_text,omitempty"`
	SourceRanges    []SourceRange   `json:"source_ranges,omitempty"`
	SourceSeparator string          `json:"source_separator,omitempty"`
	Region          *ImageRegion    `json:"region,omitempty"`
	Score           float64         `json:"score"`
	Explanation     string          `json:"explanation,omitempty"`
	Title           string          `json:"title,omitempty"`
	CommonMetadata  json.RawMessage `json:"common_metadata,omitempty"`
	PassageOrdinal  *int            `json:"passage_ordinal,omitempty"`
	TotalPassages   int             `json:"total_passages,omitempty"`
	LeadPassage     string          `json:"lead_passage,omitempty"`
}

// servedCache memoizes the canonical json.Marshal bytes of an immutable
// ServedRequest produced by RetrievalSession.Serve so later rounds do not
// re-encode earlier rounds' candidate transcripts.
type servedCache struct {
	once sync.Once
	raw  []byte
	err  error
}

// ServedRequest is a candidate request of an earlier round with what the core
// served for it.
type ServedRequest struct {
	Round        int              `json:"round"`
	RequestIndex int              `json:"request_index"`
	Request      CandidateRequest `json:"request"`
	Candidates   []Candidate      `json:"candidates"`

	encoded *servedCache
}

// MarshalCachedJSON returns the canonical json.Marshal encoding of s. When s
// was produced by RetrievalSession.Serve, the serialized bytes are cached once
// and reused across subsequent rounds. Callers must treat the returned slice
// as read-only.
func (s ServedRequest) MarshalCachedJSON() ([]byte, error) {
	if s.encoded == nil {
		return marshalServedRequest(s)
	}
	s.encoded.once.Do(func() {
		s.encoded.raw, s.encoded.err = marshalServedRequest(s)
	})
	return s.encoded.raw, s.encoded.err
}

const jsonHex = "0123456789abcdef"

// marshalServedRequest encodes s into bytes identical to json.Marshal(s) using
// a single pre-sized buffer without per-candidate reflection overhead.
func marshalServedRequest(s ServedRequest) ([]byte, error) {
	reqBytes, err := json.Marshal(s.Request)
	if err != nil {
		return nil, err
	}
	est := 64 + len(reqBytes)
	for i := range s.Candidates {
		c := &s.Candidates[i]
		est += 224 + len(c.SegmentID) + len(c.RecordID) + len(c.VersionID) + len(c.PartKey) +
			len(c.Text) + len(c.PassageText) + len(c.Title) + len(c.Explanation) +
			len(c.CommonMetadata) + len(c.LeadPassage)
	}
	dst := make([]byte, 0, est)
	dst = append(dst, `{"round":`...)
	dst = strconv.AppendInt(dst, int64(s.Round), 10)
	dst = append(dst, `,"request_index":`...)
	dst = strconv.AppendInt(dst, int64(s.RequestIndex), 10)
	dst = append(dst, `,"request":`...)
	dst = append(dst, reqBytes...)
	dst = append(dst, `,"candidates":`...)
	if s.Candidates == nil {
		dst = append(dst, "null}"...)
		return dst, nil
	}
	dst = append(dst, '[')
	var compactBuf, htmlBuf bytes.Buffer
	for i := range s.Candidates {
		if i > 0 {
			dst = append(dst, ',')
		}
		var ok bool
		dst, ok = appendCandidateJSON(dst, &s.Candidates[i], &compactBuf, &htmlBuf)
		if !ok {
			return json.Marshal(s)
		}
	}
	dst = append(dst, "]}"...)
	return dst, nil
}

func appendCandidateJSON(dst []byte, c *Candidate, compactBuf, htmlBuf *bytes.Buffer) ([]byte, bool) {
	dst = append(dst, `{"segment_id":`...)
	dst = appendJSONString(dst, c.SegmentID)
	dst = append(dst, `,"record_id":`...)
	dst = appendJSONString(dst, c.RecordID)
	dst = append(dst, `,"version_id":`...)
	dst = appendJSONString(dst, c.VersionID)
	dst = append(dst, `,"part_key":`...)
	dst = appendJSONString(dst, c.PartKey)
	dst = append(dst, `,"text":`...)
	dst = appendJSONString(dst, c.Text)
	dst = append(dst, `,"start":`...)
	dst = strconv.AppendInt(dst, int64(c.Start), 10)
	dst = append(dst, `,"end":`...)
	dst = strconv.AppendInt(dst, int64(c.End), 10)
	if c.PassageText != "" {
		dst = append(dst, `,"passage_text":`...)
		dst = appendJSONString(dst, c.PassageText)
	}
	if len(c.SourceRanges) > 0 {
		dst = append(dst, `,"source_ranges":[`...)
		for i, sr := range c.SourceRanges {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = append(dst, `{"part_key":`...)
			dst = appendJSONString(dst, sr.PartKey)
			dst = append(dst, `,"start":`...)
			dst = strconv.AppendInt(dst, int64(sr.Start), 10)
			dst = append(dst, `,"end":`...)
			dst = strconv.AppendInt(dst, int64(sr.End), 10)
			dst = append(dst, '}')
		}
		dst = append(dst, ']')
	}
	if c.SourceSeparator != "" {
		dst = append(dst, `,"source_separator":`...)
		dst = appendJSONString(dst, c.SourceSeparator)
	}
	if c.Region != nil {
		raw, err := json.Marshal(c.Region)
		if err != nil {
			return nil, false
		}
		dst = append(dst, `,"region":`...)
		dst = append(dst, raw...)
	}
	dst = append(dst, `,"score":`...)
	var ok bool
	dst, ok = appendJSONFloat(dst, c.Score)
	if !ok {
		return nil, false
	}
	if c.Explanation != "" {
		dst = append(dst, `,"explanation":`...)
		dst = appendJSONString(dst, c.Explanation)
	}
	if c.Title != "" {
		dst = append(dst, `,"title":`...)
		dst = appendJSONString(dst, c.Title)
	}
	if len(c.CommonMetadata) > 0 {
		compactBuf.Reset()
		if err := json.Compact(compactBuf, c.CommonMetadata); err != nil {
			return nil, false
		}
		raw := compactBuf.Bytes()
		dst = append(dst, `,"common_metadata":`...)
		if needsJSONHTMLEscape(raw) {
			htmlBuf.Reset()
			json.HTMLEscape(htmlBuf, raw)
			dst = append(dst, htmlBuf.Bytes()...)
		} else {
			dst = append(dst, raw...)
		}
	}
	if c.PassageOrdinal != nil {
		dst = append(dst, `,"passage_ordinal":`...)
		dst = strconv.AppendInt(dst, int64(*c.PassageOrdinal), 10)
	}
	if c.TotalPassages != 0 {
		dst = append(dst, `,"total_passages":`...)
		dst = strconv.AppendInt(dst, int64(c.TotalPassages), 10)
	}
	if c.LeadPassage != "" {
		dst = append(dst, `,"lead_passage":`...)
		dst = appendJSONString(dst, c.LeadPassage)
	}
	dst = append(dst, '}')
	return dst, true
}

var jsonSafeSet = func() [256]bool {
	var t [256]bool
	for i := 0x20; i < utf8.RuneSelf; i++ {
		if i != '\\' && i != '"' && i != '<' && i != '>' && i != '&' {
			t[i] = true
		}
	}
	return t
}()

func needsJSONHTMLEscape(b []byte) bool {
	for _, c := range b {
		if c == '<' || c == '>' || c == '&' || c == 0xe2 {
			return true
		}
	}
	return false
}

func appendJSONFloat(dst []byte, f float64) ([]byte, bool) {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, false
	}
	abs := math.Abs(f)
	fmtByte := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		fmtByte = 'e'
	}
	start := len(dst)
	dst = strconv.AppendFloat(dst, f, fmtByte, -1, 64)
	if fmtByte == 'e' {
		n := len(dst)
		if n-start >= 4 && dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
			dst[n-2] = dst[n-1]
			dst = dst[:n-1]
		}
	}
	return dst, true
}

func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		// Fast path: scan safe ASCII bytes with a single table lookup per byte.
		for i < len(s) && jsonSafeSet[s[i]] {
			i++
		}
		if i >= len(s) {
			break
		}
		b := s[i]
		if b < utf8.RuneSelf {
			if start < i {
				dst = append(dst, s[start:i]...)
			}
			switch b {
			case '\\', '"':
				dst = append(dst, '\\', b)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', jsonHex[b>>4], jsonHex[b&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			if start < i {
				dst = append(dst, s[start:i]...)
			}
			dst = utf8.AppendRune(dst, utf8.RuneError)
			i++
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			if start < i {
				dst = append(dst, s[start:i]...)
			}
			dst = append(dst, `\u202`...)
			dst = append(dst, jsonHex[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	if start < len(s) {
		dst = append(dst, s[start:]...)
	}
	return append(dst, '"')
}

// SpaceOwner is the owner of a search space: the engine or a plugin.
type SpaceOwner struct {
	Kind          string `json:"kind"`
	PluginID      string `json:"plugin_id,omitempty"`
	PluginVersion string `json:"plugin_version,omitempty"`
}

// SpaceCoverage counts current segments with a vector in a space.
type SpaceCoverage struct {
	Segments int64  `json:"segments"`
	Total    int64  `json:"total"`
	Unknown  bool   `json:"unknown,omitempty"`
	AgeMS    *int64 `json:"age_ms,omitempty"`
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
	Budget         *SearchBudget   `json:"budget,omitempty"`
}

// SearchBudget bounds the remaining work of this profile and its dependencies.
type SearchBudget struct {
	RemainingTimeMS    int     `json:"remaining_time_ms"`
	RemainingCostCents float64 `json:"remaining_cost_cents"`
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
		if c.Limit() > maxK {
			issues = append(issues, Issue{Code: CodeCandidateLimit, Path: path + "/k",
				Message: fmt.Sprintf("limit is %d; the manifest declares at most %d candidates per request (limits.max_candidates)", c.Limit(), maxK)})
		} else if c.K > LegacyMaxRetrievalCandidates {
			if !m.SupportsDeepCandidatePools() {
				issues = append(issues, Issue{Code: CodeCandidateLimit, Path: path + "/k",
					Message: fmt.Sprintf("k above %d requires a plugin_api range admitting only Plugin API %s or later", LegacyMaxRetrievalCandidates, FeatureSince(FeatureDeepCandidatePools))})
			}
		}
		if c.IncludeLead && !m.SupportsRichRetrievalCandidates() {
			issues = append(issues, Issue{Code: CodeIncompatiblePluginAPI, Path: path + "/include_lead",
				Message: fmt.Sprintf("include_lead requires a plugin_api range admitting only Plugin API %s or later", FeatureSince(FeatureRichRetrievalCandidates))})
		}
		if c.Primitive == PrimitiveProfile {
			r, _ := ParseRange(m.Compatibility.PluginAPI)
			_, admitted := admitsFeature(r, FeatureProfileCandidates)
			if !admitted || c.Profile == nil || !m.RequiresProfile(c.Profile.Name) {
				issues = append(issues, Issue{Code: CodePluginDependency, Path: path + "/profile", Message: "profile candidates require the profile-candidates feature and a full profile name declared in requires"})
			}
			if c.Profile != nil && c.Profile.Mode != "" {
				if _, admitted := admitsFeature(r, FeatureProfileCandidateMode); !admitted {
					issues = append(issues, Issue{Code: CodePluginDependency, Path: path + "/profile/mode", Message: "profile mode requires the profile-candidate-mode feature"})
				}
			}
		}
		if c.Primitive != PrimitiveBM25 && c.Primitive != PrimitiveProfile {
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
	cost    SearchCost
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
		s.cost.Add(answer.Usage.CostCents)
		s.usage.CostCents = s.cost.Cents()
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
		served = append(served, ServedRequest{
			Round:        s.request.Round,
			RequestIndex: i,
			Request:      r,
			Candidates:   c,
			encoded:      &servedCache{},
		})
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
	if !ok || !s.cost.Exceeds(profile.MaxCostCents) {
		return nil
	}
	return &Issue{Code: CodeOverBudget, Path: "/usage/cost_cents",
		Message: fmt.Sprintf("the search reported %.4g cents; profile %q allows at most %.4g (max_cost_cents)", s.usage.CostCents, s.request.Profile, profile.MaxCostCents)}
}
