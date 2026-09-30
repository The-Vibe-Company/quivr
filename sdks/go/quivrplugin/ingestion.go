package quivrplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
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

// IngestPart is one text Part of a Record Version.
type IngestPart struct {
	Key  string `json:"key"`
	Role string `json:"role"`
	Text string `json:"text"`
}

// IngestRequest is a validated segment_and_embed request.
type IngestRequest struct {
	InvocationID   string          `json:"invocation_id"`
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
	Spaces []string `json:"spaces"`
	logger *slog.Logger
}

// Logger returns the request logger.
func (r *IngestRequest) Logger() *slog.Logger { return r.logger }

// Segment is one segment of a Part: Unicode code point offsets [Start, End)
// in the Part text, a vector per requested space, and optional lexical text
// and provenance.
type Segment struct {
	PartKey     string               `json:"part_key"`
	Start       int                  `json:"start"`
	End         int                  `json:"end"`
	Vectors     map[string][]float32 `json:"vectors"`
	LexicalText string               `json:"lexical_text,omitempty"`
	Provenance  map[string]any       `json:"provenance,omitempty"`
}

// QueryRequest is a validated embed_query request.
type QueryRequest struct {
	InvocationID   string          `json:"invocation_id"`
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
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
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
	req.logger = p.logger.With("invocation_id", req.InvocationID)
	defer p.ingestPanic(w, req.logger)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(p.m.Ingestion.TimeoutMS)*time.Millisecond)
	defer cancel()
	segments, err := p.ingester.SegmentAndEmbed(ctx, &req)
	if err != nil {
		p.ingestFail(w, req.logger, err)
		return
	}
	body, problem := p.encodeSegments(&req, segments)
	if problem != "" {
		req.logger.Error("the ingester returned invalid segments", "problem", problem)
		writeJSON(w, 500, envelope{Code: "invalid_response", Message: truncate(problem), Retryable: false})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(body)
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
	for _, part := range req.Parts {
		lengths[part.Key] = utf8.RuneCountInString(part.Text)
	}
	for i := range segments {
		if segments[i].Vectors == nil {
			segments[i].Vectors = map[string][]float32{}
		}
		s := segments[i]
		n, ok := lengths[s.PartKey]
		if !ok || s.Start < 0 || s.Start > s.End || s.End > n {
			return nil, fmt.Sprintf("segment %d: [%d, %d) is not inside Part %q", i, s.Start, s.End, s.PartKey)
		}
		if len(s.Vectors) != len(req.Spaces) {
			return nil, fmt.Sprintf("segment %d: %d vectors for %d requested spaces", i, len(s.Vectors), len(req.Spaces))
		}
		for _, id := range req.Spaces {
			v, ok := s.Vectors[id]
			if !ok || len(v) != in.Spaces[id].Dimensions {
				return nil, fmt.Sprintf("segment %d: space %s needs a vector of %d dimensions", i, id, in.Spaces[id].Dimensions)
			}
			for _, x := range v {
				if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
					return nil, fmt.Sprintf("segment %d: space %s has a non-finite value", i, id)
				}
			}
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

func (p *Plugin) serveEmbedQuery(w http.ResponseWriter, r *http.Request) {
	var req QueryRequest
	if !p.readIngestion(w, r, "plugins/v0/ingestion-embed-query-request.schema.json", &req) || p.undeclared(w, req.Space) {
		return
	}
	req.logger = p.logger.With("invocation_id", req.InvocationID)
	defer p.ingestPanic(w, req.logger)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(p.m.Ingestion.QueryTimeoutMS)*time.Millisecond)
	defer cancel()
	vector, err := p.ingester.EmbedQuery(ctx, &req)
	if err != nil {
		p.ingestFail(w, req.logger, err)
		return
	}
	if want := p.m.Ingestion.Spaces[req.Space].Dimensions; len(vector) != want {
		req.logger.Error("the ingester returned a query vector of the wrong size", "dimensions", len(vector), "declared", want)
		writeJSON(w, 500, envelope{Code: "invalid_response", Message: fmt.Sprintf("a vector of %d dimensions; space %s declares %d", len(vector), req.Space, want), Retryable: false})
		return
	}
	writeJSON(w, 200, map[string]any{"vector": vector})
}
