package quivrplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Ingester segments and embeds Record Versions for the vector spaces the
// manifest declares (the ingestion Contribution, Plugin API 0.6). Both
// methods must be deterministic: the same request yields the same answer.
// Since Plugin API 0.8 a request may name no space: the core asks for the
// segments first and for the vectors later, and the segments must match.
type Ingester interface {
	// SegmentAndEmbed cuts the request's Parts into segments, in reading
	// order, each with one vector per requested space.
	SegmentAndEmbed(ctx context.Context, req *IngestRequest) ([]Segment, error)
	// EmbedQuery encodes one query into one declared space.
	EmbedQuery(ctx context.Context, req *QueryRequest) ([]float32, error)
}

// PagedIngester implements bounded full-text processing (Plugin API 0.18).
// The host durably records each page, including provider-negotiated cuts, before
// asking for the next. Every source code point from Page.Start must be covered.
type PagedIngester interface {
	SegmentAndEmbedPage(context.Context, *IngestRequest) (IngestPage, error)
}
type IngestPageRequest struct {
	Start       int `json:"start"`
	MaxSegments int `json:"max_segments"`
}
type IngestPage struct {
	Segments  []Segment `json:"segments"`
	NextStart *int      `json:"next_start,omitempty"`
}

// IngestPart is one text Part of a Record Version.
type IngestPart struct {
	Key  string `json:"key"`
	Role string `json:"role"`
	Text string `json:"text"`
}

// IngestRequest is a validated segment_and_embed request.
type IngestRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	IdempotencyKey string          `json:"idempotency_key"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Version        struct {
		CorpusID        string `json:"corpus_id"`
		RecordID        string `json:"record_id"`
		RecordVersionID string `json:"record_version_id"`
	} `json:"version"`
	// Language is a BCP 47 hint, empty when the core knows none.
	Language string       `json:"language,omitempty"`
	Parts    []IngestPart `json:"parts"`
	// Spaces are the declared spaces to embed: every segment carries one
	// vector for each. Empty (Plugin API 0.8) asks for the segments only.
	Spaces []string           `json:"spaces"`
	Page   *IngestPageRequest `json:"page,omitempty"`
	logger *slog.Logger
}

// Logger returns the request logger.
func (r *IngestRequest) Logger() *slog.Logger { return r.logger }

// SourceRange is one non-empty Unicode code point slice of a request Part.
// Ranges in one Segment are listed in the request's reading order.
type SourceRange struct {
	PartKey string `json:"part_key"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
}

const (
	// MaxSourceRangeRunes matches the host's maximum size for one source slice.
	MaxSourceRangeRunes = 4096
	// MaxPackedTextRunes matches the host's maximum joined passage size.
	MaxPackedTextRunes = 16384
)

// Segment is one segment of a Part: Unicode code point offsets [Start, End)
// in the Part text, a vector per requested space, and optional lexical text,
// provenance and multi-Part source ranges.
type Segment struct {
	PartKey         string               `json:"part_key"`
	Start           int                  `json:"start"`
	End             int                  `json:"end"`
	SourceRanges    []SourceRange        `json:"source_ranges,omitempty"`
	SourceSeparator string               `json:"source_separator,omitempty"`
	Vectors         map[string][]float32 `json:"vectors"`
	LexicalText     string               `json:"lexical_text,omitempty"`
	Provenance      map[string]any       `json:"provenance,omitempty"`
}

// QueryRequest is a validated embed_query request.
type QueryRequest struct {
	InvocationID   string          `json:"invocation_id"`
	Contribution   string          `json:"contribution"`
	OrganizationID string          `json:"organization_id"`
	Configuration  json.RawMessage `json:"configuration"`
	Space          string          `json:"space"`
	Query          struct {
		Modality string `json:"modality"`
		Text     string `json:"text"`
	} `json:"query"`
	logger *slog.Logger
}

// Logger returns the request logger.
func (r *QueryRequest) Logger() *slog.Logger { return r.logger }

// Ingestion registers the implementation of the declared ingestion
// Contribution.
func (p *Plugin) Ingestion(impl Ingester) error {
	if p.m.Ingestion == nil {
		return fmt.Errorf("the manifest declares no ingestion Contribution")
	}
	p.ingester = impl
	return nil
}

// IngestError is a failure of an Ingester. A retryable one (a backend
// outage) makes the core retry the Version later; a terminal one blocks it.
type IngestError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *IngestError) Error() string { return e.Code + ": " + e.Message }

// TerminalIngestError reports content the plugin can never segment or embed.
func TerminalIngestError(code, message string) *IngestError {
	return &IngestError{Code: code, Message: message}
}

// RetryableIngestError reports a failure worth retrying, such as a backend outage.
func RetryableIngestError(code, message string) *IngestError {
	return &IngestError{Code: code, Message: message, Retryable: true}
}

// readIngestion reads and validates a request against a protocol schema and
// the configuration schema; it answers the refusal itself and returns false.
func (p *Plugin) readIngestion(w http.ResponseWriter, r *http.Request, schema string, into any) bool {
	body, err := requestBody(r)
	if err != nil || len(body) > maxRequestBytes {
		refuse(w, 400, "invalid_request", "the request body is unreadable or larger than 16 MiB", Credential{})
		return false
	}
	if err := validate(schema, body); err != nil {
		refuse(w, 400, "invalid_request", err.Error(), Credential{})
		return false
	}
	var c struct {
		Configuration json.RawMessage `json:"configuration"`
	}
	_ = json.Unmarshal(body, &c)
	if p.configSch != nil {
		if err := validateWith(p.configSch, c.Configuration); err != nil {
			refuse(w, 400, "invalid_configuration", "configuration "+err.Error(), Credential{})
			return false
		}
	}
	if err := json.Unmarshal(body, into); err != nil {
		refuse(w, 400, "invalid_request", err.Error(), Credential{})
		return false
	}
	return true
}

func (p *Plugin) undeclared(w http.ResponseWriter, spaces ...string) bool {
	for _, id := range spaces {
		if _, ok := p.m.Ingestion.Spaces[id]; !ok {
			refuse(w, 400, "unknown_space", fmt.Sprintf("space %q is not declared by %s", id, p.m.ID), Credential{})
			return true
		}
	}
	return false
}

func (p *Plugin) ingestFail(w http.ResponseWriter, log *slog.Logger, err error) {
	var e *IngestError
	if errors.As(err, &e) {
		status := 422
		if e.Retryable {
			status = 503
		}
		writeJSON(w, status, envelope{Code: e.Code, Message: truncate(e.Message), Retryable: e.Retryable})
		return
	}
	log.Error("ingestion failed with an unclassified error", "error", err.Error())
	writeJSON(w, 503, envelope{Code: "unexpected_error", Message: "the plugin failed with an unclassified error; see the plugin log", Retryable: true})
}

func (p *Plugin) ingestPanic(w http.ResponseWriter, log *slog.Logger) {
	if v := recover(); v != nil {
		log.Error("ingestion panicked", "panic", fmt.Sprint(v))
		writeJSON(w, 500, envelope{Code: "internal_error", Message: "the plugin failed unexpectedly; see the plugin log", Retryable: false})
	}
}

func (p *Plugin) serveSegmentAndEmbed(w http.ResponseWriter, r *http.Request) {
	var req IngestRequest
	if !p.readIngestion(w, r, "plugins/v0/ingestion-segment-and-embed-request.schema.json", &req) || p.undeclared(w, req.Spaces...) {
		return
	}
	req.logger = p.requestLogger(r.Context(), Credential{}, req.InvocationID)
	defer p.ingestPanic(w, req.logger)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(p.m.Ingestion.TimeoutMS)*time.Millisecond)
	defer cancel()
	var segments []Segment
	var next *int
	var err error
	if req.Page != nil {
		paged, ok := p.ingester.(PagedIngester)
		if !ok || compareVersions(p.m.pluginAPI, "0.18.0") < 0 {
			refuse(w, 400, "invalid_request", "ingestion pages require a Plugin API 0.18 paged ingester", Credential{})
			return
		}
		page, cause := paged.SegmentAndEmbedPage(ctx, &req)
		segments, next, err = page.Segments, page.NextStart, cause
	} else {
		segments, err = p.ingester.SegmentAndEmbed(ctx, &req)
	}
	if err != nil {
		p.ingestFail(w, req.logger, err)
		return
	}
	body, problem := p.encodeSegments(&req, segments)
	if problem == "" && req.Page != nil {
		problem = ingestionPageProblem(&req, segments, next)
		if problem == "" {
			body, err = json.Marshal(IngestPage{Segments: segments, NextStart: next})
			if err != nil || len(body) > p.m.Ingestion.Limits.MaxResponseBytes {
				problem = "ingestion page exceeds max_response_bytes"
			}
		}
	}
	if problem != "" {
		req.logger.Error("the ingester returned invalid segments", "problem", problem)
		writeJSON(w, 500, envelope{Code: "invalid_response", Message: truncate(problem), Retryable: false})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(body)
}

func ingestionPageProblem(req *IngestRequest, segments []Segment, next *int) string {
	if len(req.Parts) != 1 || req.Page.Start < 0 || req.Page.MaxSegments < 1 || len(segments) > req.Page.MaxSegments {
		return "a page must respect its work bound and have one Part"
	}
	part := req.Parts[0]
	end := req.Page.Start
	for _, s := range segments {
		ranges := s.SourceRanges
		if len(ranges) == 0 {
			ranges = []SourceRange{{PartKey: s.PartKey, Start: s.Start, End: s.End}}
		}
		for _, r := range ranges {
			if r.PartKey != part.Key || r.Start != end || r.End <= r.Start {
				return "paged source ranges must cover every code point once"
			}
			end = r.End
		}
	}
	length := utf8.RuneCountInString(part.Text)
	if next != nil {
		if *next != end || end <= req.Page.Start || end >= length {
			return "next_start must advance to the first uncovered code point"
		}
	} else if end != length {
		return "a final page must cover the entire remaining source"
	}
	return ""
}

// encodeSegments encodes segments and checks them the way the engine will:
// Part keys and offsets, one vector per requested space with the declared
// dimensions, max_segments, the response size and schema.
func (p *Plugin) encodeSegments(req *IngestRequest, segments []Segment) ([]byte, string) {
	in := p.m.Ingestion
	if len(segments) == 0 {
		return nil, "SegmentAndEmbed returned no segment; return a terminal IngestError for content it cannot segment"
	}
	if len(segments) > in.Limits.MaxSegments {
		return nil, fmt.Sprintf("%d segments exceed max_segments %d", len(segments), in.Limits.MaxSegments)
	}
	lengths := map[string]int{}
	partOrder := map[string]int{}
	hasTitle := false
	for index, part := range req.Parts {
		lengths[part.Key] = utf8.RuneCountInString(part.Text)
		partOrder[part.Key] = index
		hasTitle = hasTitle || part.Role == "title"
	}
	seen := map[string]bool{}
	for i := range segments {
		if segments[i].Vectors == nil {
			segments[i].Vectors = map[string][]float32{}
		}
		s := segments[i]
		n, ok := lengths[s.PartKey]
		if !ok || s.Start < 0 || s.Start > s.End || s.End > n {
			return nil, fmt.Sprintf("segment %d: [%d, %d) is not inside Part %q", i, s.Start, s.End, s.PartKey)
		}
		if s.Start == s.End && !hasTitle {
			return nil, fmt.Sprintf("segment %d is empty without a title Part", i)
		}
		if len(s.SourceRanges) > 0 || s.SourceSeparator != "" {
			if compareVersions(p.m.pluginAPI, "0.17.0") < 0 {
				return nil, fmt.Sprintf("segment %d uses source ranges or a source separator, which requires Plugin API 0.17.0", i)
			}
		}
		if len(s.SourceRanges) > 256 {
			return nil, fmt.Sprintf("segment %d has %d source ranges; at most 256 are allowed", i, len(s.SourceRanges))
		}
		if !utf8.ValidString(s.SourceSeparator) || strings.ContainsRune(s.SourceSeparator, 0) {
			return nil, fmt.Sprintf("segment %d: source separator must be valid UTF-8 without NUL", i)
		}
		if utf8.RuneCountInString(s.SourceSeparator) > 16 {
			return nil, fmt.Sprintf("segment %d: source separator must be at most 16 code points", i)
		}
		if problem := sourceRangesProblem(s, lengths, partOrder); problem != "" {
			return nil, fmt.Sprintf("segment %d: %s", i, problem)
		}
		identity := sourceSegmentIdentity(s)
		if seen[identity] {
			return nil, fmt.Sprintf("segment %d repeats the same Part and offsets", i)
		}
		seen[identity] = true
		if len(s.Vectors) != len(req.Spaces) {
			return nil, fmt.Sprintf("segment %d: %d vectors for %d requested spaces", i, len(s.Vectors), len(req.Spaces))
		}
		for _, id := range req.Spaces {
			v, ok := s.Vectors[id]
			if !ok {
				return nil, fmt.Sprintf("segment %d: space %s needs a vector of %d dimensions", i, id, in.Spaces[id].Dimensions)
			}
			if problem := vectorProblem(v, in.Spaces[id]); problem != "" {
				return nil, fmt.Sprintf("segment %d: space %s: %s", i, id, problem)
			}
		}
		if !utf8.ValidString(s.LexicalText) || strings.ContainsRune(s.LexicalText, 0) || utf8.RuneCountInString(s.LexicalText) > 16384 {
			return nil, fmt.Sprintf("segment %d: lexical text must be valid UTF-8 without NUL and at most 16384 code points", i)
		}
		if problem := jsonValueProblem(s.Provenance, 4<<10); problem != "" {
			return nil, fmt.Sprintf("segment %d: provenance: %s", i, problem)
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]any{"segments": segments}); err != nil {
		return nil, "the segments are not JSON-encodable: " + err.Error()
	}
	body := buf.Bytes()
	if len(body) > in.Limits.MaxResponseBytes {
		return nil, fmt.Sprintf("the response is %d bytes; max_response_bytes is %d", len(body), in.Limits.MaxResponseBytes)
	}
	if err := validate("plugins/v0/ingestion-segment-and-embed-response.schema.json", body); err != nil {
		return nil, "the segments do not match the response schema: " + err.Error()
	}
	return body, ""
}

func sourceSegmentIdentity(s Segment) string {
	if len(s.SourceRanges) == 0 {
		return fmt.Sprintf("%s\x00%d\x00%d", s.PartKey, s.Start, s.End)
	}
	var b strings.Builder
	for _, r := range s.SourceRanges {
		fmt.Fprintf(&b, "%s\x00%d\x00%d\x00", r.PartKey, r.Start, r.End)
	}
	return b.String()
}

func sourceRangesProblem(s Segment, lengths, partOrder map[string]int) string {
	if len(s.SourceRanges) == 0 {
		return ""
	}
	first := s.SourceRanges[0]
	if first.PartKey != s.PartKey || first.Start != s.Start || first.End != s.End {
		return "the first source range must equal the legacy part_key/start/end anchor"
	}
	seen := map[string]bool{}
	lastPart := -1
	lastEnd := map[string]int{}
	packedRunes := 0
	for _, r := range s.SourceRanges {
		length, ok := lengths[r.PartKey]
		if !ok {
			return fmt.Sprintf("source range names unknown Part %q", r.PartKey)
		}
		if r.Start < 0 || r.Start >= r.End || r.End > length {
			return fmt.Sprintf("source range [%d, %d) is not a non-empty slice of Part %q", r.Start, r.End, r.PartKey)
		}
		if r.End-r.Start > MaxSourceRangeRunes {
			return fmt.Sprintf("source range [%d, %d) spans more than %d code points", r.Start, r.End, MaxSourceRangeRunes)
		}
		if packedRunes > 0 {
			packedRunes += utf8.RuneCountInString(s.SourceSeparator)
		}
		packedRunes += r.End - r.Start
		identity := fmt.Sprintf("%s\x00%d\x00%d", r.PartKey, r.Start, r.End)
		if seen[identity] {
			return fmt.Sprintf("source range [%d, %d) of Part %q is duplicated", r.Start, r.End, r.PartKey)
		}
		seen[identity] = true
		order := partOrder[r.PartKey]
		if order < lastPart || (order == lastPart && r.Start < lastEnd[r.PartKey]) {
			return "source ranges must follow request Part order and be increasing without overlap within a Part"
		}
		if order > lastPart {
			lastPart = order
		}
		if r.End > lastEnd[r.PartKey] {
			lastEnd[r.PartKey] = r.End
		}
	}
	if packedRunes > MaxPackedTextRunes {
		return fmt.Sprintf("joined source ranges span more than %d code points", MaxPackedTextRunes)
	}
	return ""
}

func (p *Plugin) serveEmbedQuery(w http.ResponseWriter, r *http.Request) {
	var req QueryRequest
	if !p.readIngestion(w, r, "plugins/v0/ingestion-embed-query-request.schema.json", &req) || p.undeclared(w, req.Space) {
		return
	}
	req.logger = p.requestLogger(r.Context(), Credential{}, req.InvocationID)
	defer p.ingestPanic(w, req.logger)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(p.m.Ingestion.QueryTimeoutMS)*time.Millisecond)
	defer cancel()
	vector, err := p.ingester.EmbedQuery(ctx, &req)
	if err != nil {
		p.ingestFail(w, req.logger, err)
		return
	}
	if problem := vectorProblem(vector, p.m.Ingestion.Spaces[req.Space]); problem != "" {
		req.logger.Error("the ingester returned an invalid query vector", "problem", problem)
		writeJSON(w, 500, envelope{Code: "invalid_response", Message: problem, Retryable: false})
		return
	}
	p.contributionResponse(w, map[string]any{"vector": vector}, "ingestion-embed-query-response.schema.json", 1<<20)
}

func vectorProblem(vector []float32, space Space) string {
	if len(vector) != space.Dimensions {
		return fmt.Sprintf("a vector of %d dimensions; the space declares %d", len(vector), space.Dimensions)
	}
	zero := true
	for _, x := range vector {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return "a vector contains a non-finite value"
		}
		zero = zero && x == 0
	}
	if zero && space.Metric == "cosine" {
		return "an all-zero vector has no direction in a cosine space"
	}
	return ""
}

// jsonValueProblem checks stored JSON using the engine's compact encoding.
// Decoding also covers nested typed maps/slices returned by Go implementations.
func jsonValueProblem(value any, maxBytes int) string {
	b, err := json.Marshal(value)
	if err != nil {
		return "the value is not JSON-encodable"
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return "the value is not JSON-decodable"
	}
	// The engine decodes JSON numbers as float64 before storing this value.
	// Rounding a Go integer can change the persisted length across a bound.
	compact, err := json.Marshal(doc)
	if err != nil {
		return "the stored value is not JSON-encodable"
	}
	if maxBytes > 0 && len(compact) > maxBytes {
		return fmt.Sprintf("JSON exceeds %d bytes", maxBytes)
	}
	if containsNUL(doc) {
		return "JSON contains NUL"
	}
	return ""
}

func containsNUL(value any) bool {
	switch v := value.(type) {
	case string:
		return strings.ContainsRune(v, 0)
	case map[string]any:
		for key, child := range v {
			if strings.ContainsRune(key, 0) || containsNUL(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if containsNUL(child) {
				return true
			}
		}
	}
	return false
}
