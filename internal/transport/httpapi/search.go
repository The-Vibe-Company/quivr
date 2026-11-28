package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

const (
	maxSearchCorpora         = 16
	maxSearchLimit           = 50
	maxSearchQueryCodepoints = retrieval.MaxQueryCodepoints
	maxSearchFilterStrings   = 50
	maxSearchFilterStringLen = 200
	maxSearchMetadataFilters = corpus.MaxMetadataFilters
	maxSearchMetadataValues  = corpus.MaxFilterValues
	// Cap whole-payload string aliasing so large whitespace-padded payloads
	// never stay pinned by decoded strings.
	searchDecodeAliasMaxBytes = 4096
)

func isSearchOverload(err error) bool {
	// A recovered dependency panic is a bug, not load: like a handler
	// panic, it must not steer admission.
	if err == nil || errors.Is(err, retrieval.ErrPanicked) {
		return false
	}
	return errors.Is(err, retrieval.ErrUnavailable) ||
		errors.Is(err, retrieval.ErrModelUnavailable) ||
		errors.Is(err, retrieval.ErrDeadline) ||
		errors.Is(err, context.DeadlineExceeded)
}

func (a *API) handleSearchPanic(w http.ResponseWriter, r *http.Request, rec any, wroteResponse bool) {
	if rec == http.ErrAbortHandler {
		panic(rec)
	}
	// Never log the panic value itself: it may quote query text, request bodies,
	// or credentials. Log only its concrete Go type and stack trace.
	slog.ErrorContext(r.Context(), "search handler panicked",
		"component", "search",
		"panic_type", fmt.Sprintf("%T", rec),
		"stack", string(debug.Stack()),
	)
	if wroteResponse {
		return
	}
	a.panicHeaders(w)
	writeError(w, publicerr.SearchUnavailable, nil)
}

// panicHeaders prepares a 503 for a search that panicked, in the handler or
// in a dependency retrieval recovered (retrieval.ErrPanicked).
func (a *API) panicHeaders(w http.ResponseWriter) {
	retryAfter := 1
	if a.searchAdmission != nil {
		retryAfter = a.searchAdmission.retryAfter()
	}
	w.Header().Del("X-Quivr-Degraded")
	w.Header().Del("X-Quivr-Degraded-Reason")
	w.Header().Del("Server-Timing")
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
}

func (a *API) search(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	wroteResponse := false
	defer func() {
		if rec := recover(); rec != nil {
			a.handleSearchPanic(w, r, rec, wroteResponse)
		}
	}()

	payload, ok := readPayload(w, r, maxRequestBytes)
	if !ok {
		wroteResponse = true
		return
	}
	q, err := decodeSearchRequest(payload)
	if err != nil {
		wroteResponse = true
		writeError(w, err, nil)
		return
	}
	started := time.Now()
	lease, retryAfter, admitted := a.searchAdmission.Acquire(r.Context())
	if !admitted {
		a.loadMetrics.SearchRefused()
		a.recordSearch(scope.Organization, q, retrieval.Result{}, started, publicerr.SearchUnavailable)
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		wroteResponse = true
		writeError(w, publicerr.SearchUnavailable, nil)
		return
	}
	// LIFO defer order guarantees the admission slot returns to 0 before the
	// outer panic recovery writes the 503 response.
	defer lease.Release(false, true)
	result, err := a.Retrieval.Search(r.Context(), scope, q)
	overload := isSearchOverload(err)
	skipObserve := errors.Is(r.Context().Err(), context.Canceled) ||
		(err != nil && !overload) ||
		(err == nil && (result.Usage == nil || result.Usage.Rounds == 0))
	var targetLatency time.Duration
	if result.Usage != nil && result.Usage.Objective > 0 && !result.Usage.DefaultProfile {
		targetLatency = result.Usage.Objective
	}
	lease.ReleaseWithTarget(overload, skipObserve, targetLatency)
	a.recordSearch(scope.Organization, q, result, started, err)
	if err != nil {
		if u := result.Usage; u != nil {
			w.Header().Set("Server-Timing", serverTiming(u.Phases))
		}
		if errors.Is(err, retrieval.ErrPanicked) {
			a.panicHeaders(w)
		}
		wroteResponse = true
		writeError(w, err, publicerr.SearchUnavailable)
		return
	}
	response := transport.SearchResponse{ExcludedCorpora: exclusionsToTransport(result.ExcludedCorpora), Items: []transport.SearchHit{}, RetrievalProfile: transport.SearchProfile{Name: result.Profile, Version: result.ProfileVersion}}
	if len(result.Degraded) > 0 {
		degraded := make([]transport.SearchDegradation, 0, len(result.Degraded))
		reasons := make([]string, 0, len(result.Degraded))
		for _, d := range result.Degraded {
			a.loadMetrics.SearchDegraded(d.Reason)
			degraded = append(degraded, transport.SearchDegradation{Reason: transport.SearchDegradationReason(d.Reason), CorpusIds: d.CorpusIDs})
			reasons = append(reasons, d.Reason)
		}
		response.RetrievalProfile.Degraded = &degraded
		w.Header().Set("X-Quivr-Degraded", "true")
		w.Header().Set("X-Quivr-Degraded-Reason", strings.Join(reasons, ","))
	}
	for i, h := range result.Hits {
		hit := transport.SearchHit{EmbeddingArtifactId: optionalString(h.EmbeddingID), VectorSpaceId: optionalString(h.SpaceID), RecordId: h.RecordID, VersionId: h.VersionID, PartKey: h.Segment.PartKey, SegmentId: h.Segment.ID, SegmentationId: h.SegmentationID, ProjectionGenerationId: h.GenerationID, Rank: i + 1, Excerpt: transport.SearchExcerpt{Text: h.Segment.AnchorText(), Start: h.Segment.Start, End: h.Segment.End, CoordinateSystem: "unicode_codepoint"}, Availability: availabilityToTransport(h.Availability), Explanation: optionalString(h.Explanation)}
		if h.Segment.Derivation.Region != nil {
			r := h.Segment.Derivation.Region
			region := transport.ImageRegion{
				CoordinateSystem: transport.ImageRegionCoordinateSystem(r.CoordinateSystem),
				X:                float32(r.X),
				Y:                float32(r.Y),
				Width:            float32(r.Width),
				Height:           float32(r.Height),
				Label:            optionalString(r.Label),
			}
			if r.ImageWidth > 0 {
				w := r.ImageWidth
				region.ImageWidth = &w
			}
			if r.ImageHeight > 0 {
				ht := r.ImageHeight
				region.ImageHeight = &ht
			}
			hit.Region = &region
		}
		if len(h.Segment.SourceExcerpts) > 0 {
			hit.PassageText = optionalString(h.Segment.Text)
			excerpts := []struct {
				CoordinateSystem transport.SearchHitSourceExcerptsCoordinateSystem `json:"coordinate_system"`
				End              int                                               `json:"end"`
				PartKey          string                                            `json:"part_key"`
				Start            int                                               `json:"start"`
				Text             string                                            `json:"text"`
			}{}
			for _, source := range h.Segment.SourceExcerpts {
				excerpts = append(excerpts, struct {
					CoordinateSystem transport.SearchHitSourceExcerptsCoordinateSystem `json:"coordinate_system"`
					End              int                                               `json:"end"`
					PartKey          string                                            `json:"part_key"`
					Start            int                                               `json:"start"`
					Text             string                                            `json:"text"`
				}{"unicode_codepoint", source.End, source.PartKey, source.Start, source.Text})
			}
			hit.SourceExcerpts = &excerpts
		}
		response.Items = append(response.Items, hit)
	}
	if u := result.Usage; u != nil {
		response.Usage = usageToTransport(*u)
		w.Header().Set("Server-Timing", serverTiming(u.Phases))
	}
	wroteResponse = true
	send(w, 200, response)
}

// searchDecoder performs single-pass RFC 8259 decoding and OpenAPI
// SearchRequest validation directly into retrieval.Request, avoiding the
// intermediate map[string]any -> json.Marshal -> transport.SearchRequest ->
// metadata json.Marshal/Unmarshal round-trips.
type searchDecoder struct {
	jsonScanner
	// rawStr aliases data as a single immutable string for bounded payloads so
	// unescaped decoded strings slice without per-string heap allocations.
	rawStr           string
	sawOverflowFloat bool
}

// decodeSearchRequest parses and validates one SearchRequest payload directly
// into retrieval.Request, preserving exact OpenAPI SearchRequest, SearchFilter,
// and MetadataFilter schema semantics and malformed_json-over-invalid_schema
// error precedence.
func decodeSearchRequest(raw []byte) (retrieval.Request, error) {
	d := searchDecoder{jsonScanner: jsonScanner{data: raw}}
	if len(raw) <= searchDecodeAliasMaxBytes {
		d.rawStr = string(raw)
	}
	q, err := d.parseSearchRequest()
	if err != nil {
		if errors.Is(err, errMalformedJSON) || !json.Valid(raw) {
			return retrieval.Request{}, publicerr.MalformedJson
		}
		return retrieval.Request{}, publicerr.InvalidSchema
	}
	d.skipWS()
	if d.pos != len(raw) {
		return retrieval.Request{}, publicerr.MalformedJson
	}
	if d.sawOverflowFloat {
		for _, m := range q.Metadata {
			for _, v := range m.AnyOf {
				if _, overflow := v.(json.Number); overflow {
					return retrieval.Request{}, publicerr.InvalidQuery
				}
			}
		}
	}
	return q, nil
}

func (d *searchDecoder) decodeOwnedString() (string, error) {
	quoteStart := d.pos
	raw, escaped, err := d.scanString()
	if err != nil {
		return "", err
	}
	if !escaped {
		if d.rawStr != "" {
			return d.rawStr[quoteStart+1 : d.pos-1], nil
		}
		return string(raw), nil
	}
	var out string
	if err := json.Unmarshal(d.data[quoteStart:d.pos], &out); err != nil {
		return "", errMalformedJSON
	}
	return out, nil
}

func (d *searchDecoder) decodeBoundedString(maxRunes int) (string, error) {
	if d.pos >= len(d.data) {
		return "", errMalformedJSON
	}
	if d.data[d.pos] != '"' {
		return "", publicerr.InvalidSchema
	}
	v, err := d.decodeOwnedString()
	if err != nil {
		return "", err
	}
	if len(v) == 0 {
		return "", publicerr.InvalidSchema
	}
	if maxRunes > 0 && len(v) > maxRunes && utf8.RuneCountInString(v) > maxRunes {
		return "", publicerr.InvalidSchema
	}
	return v, nil
}

func (d *searchDecoder) parseSearchRequest() (retrieval.Request, error) {
	d.skipWS()
	if d.pos >= len(d.data) {
		return retrieval.Request{}, errMalformedJSON
	}
	if d.data[d.pos] != '{' {
		return retrieval.Request{}, publicerr.InvalidSchema
	}
	d.pos++
	d.skipWS()
	if d.pos >= len(d.data) {
		return retrieval.Request{}, errMalformedJSON
	}
	if d.data[d.pos] == '}' {
		d.pos++
		return retrieval.Request{}, publicerr.InvalidSchema
	}

	const (
		reqQuery uint16 = 1 << iota
		reqCorpusIDs
		reqMode
		reqProfile
		reqLimit
		reqEvalPlugin
		reqEvalSpace
		reqFilter
	)
	q := retrieval.Request{Mode: "hybrid", Limit: 10}
	var (
		seen    uint16
		invalid uint16
	)

	for {
		key, err := d.scanKey()
		if err != nil {
			return retrieval.Request{}, err
		}
		if err := d.expectColon(); err != nil {
			return retrieval.Request{}, err
		}
		valStart := d.pos
		var bit uint16
		switch string(key) {
		case "query":
			bit = reqQuery
			var v string
			v, err = d.decodeBoundedString(maxSearchQueryCodepoints)
			if err == nil {
				q.Query = v
			}
		case "corpus_ids":
			bit = reqCorpusIDs
			var ids []string
			ids, err = d.parseUniqueStrings(maxSearchCorpora, 0)
			if err == nil {
				q.CorpusIDs = ids
			}
		case "mode":
			bit = reqMode
			var mode string
			mode, err = d.parseSearchMode()
			if err == nil {
				q.Mode = mode
			}
		case "profile":
			bit = reqProfile
			var v string
			v, err = d.decodeBoundedString(0)
			if err == nil {
				q.Profile = v
			}
		case "limit":
			bit = reqLimit
			var lim int
			lim, err = d.parseSearchLimit()
			if err == nil {
				q.Limit = lim
			}
		case "evaluation_plugin":
			bit = reqEvalPlugin
			var v string
			v, err = d.decodeBoundedString(0)
			if err == nil {
				q.EvaluationPlugin = v
			}
		case "evaluation_space":
			bit = reqEvalSpace
			var v string
			v, err = d.decodeBoundedString(0)
			if err == nil {
				q.Space = v
			}
		case "filter":
			bit = reqFilter
			var f parsedSearchFilter
			f, err = d.parseSearchFilter()
			if err == nil {
				q.SourceNamespaces = f.sourceNamespaces
				q.RecordIDs = f.recordIDs
				q.VersionIDs = f.versionIDs
				q.Metadata = f.metadata
			}
		default:
			return retrieval.Request{}, publicerr.InvalidSchema
		}
		if err != nil {
			if !errors.Is(err, publicerr.InvalidSchema) {
				return retrieval.Request{}, err
			}
			// Preserve last-value-wins semantics on duplicate keys while still
			// validating RFC 8259 JSON syntax of the skipped value.
			d.pos = valStart
			if skipErr := d.skipValue(); skipErr != nil {
				return retrieval.Request{}, skipErr
			}
			seen |= bit
			invalid |= bit
		} else {
			seen |= bit
			invalid &^= bit
		}

		d.skipWS()
		if d.pos >= len(d.data) {
			return retrieval.Request{}, errMalformedJSON
		}
		if d.data[d.pos] == ',' {
			d.pos++
			d.skipWS()
			continue
		}
		if d.data[d.pos] == '}' {
			d.pos++
			break
		}
		return retrieval.Request{}, errMalformedJSON
	}

	const required = reqQuery | reqCorpusIDs
	if invalid != 0 || seen&required != required {
		return retrieval.Request{}, publicerr.InvalidSchema
	}
	return q, nil
}

func (d *searchDecoder) parseSearchMode() (string, error) {
	if d.pos >= len(d.data) {
		return "", errMalformedJSON
	}
	if d.data[d.pos] != '"' {
		return "", publicerr.InvalidSchema
	}
	v, err := d.decodeOwnedString()
	if err != nil {
		return "", err
	}
	switch v {
	case "lexical":
		return "lexical", nil
	case "semantic":
		return "semantic", nil
	case "hybrid":
		return "hybrid", nil
	default:
		return "", publicerr.InvalidSchema
	}
}

func (d *searchDecoder) parseSearchLimit() (int, error) {
	if d.pos >= len(d.data) {
		return 0, errMalformedJSON
	}
	c := d.data[d.pos]
	if c != '-' && (c < '0' || c > '9') {
		return 0, publicerr.InvalidSchema
	}
	start := d.pos
	if err := d.skipNumber(); err != nil {
		return 0, err
	}
	raw := d.data[start:d.pos]
	// OpenAPI requires integer 1..50; transport.SearchRequest.Limit (*int)
	// also rejects fractional/exponential forms such as 10.0 or 1e1.
	if len(raw) == 0 || len(raw) > 2 || raw[0] < '1' || raw[0] > '9' {
		return 0, publicerr.InvalidSchema
	}
	v := int(raw[0] - '0')
	if len(raw) == 2 {
		if raw[1] < '0' || raw[1] > '9' {
			return 0, publicerr.InvalidSchema
		}
		v = v*10 + int(raw[1]-'0')
	}
	if v < 1 || v > maxSearchLimit {
		return 0, publicerr.InvalidSchema
	}
	return v, nil
}

func (d *searchDecoder) parseUniqueStrings(maxItems, maxRunes int) ([]string, error) {
	if d.pos >= len(d.data) {
		return nil, errMalformedJSON
	}
	if d.data[d.pos] != '[' {
		return nil, publicerr.InvalidSchema
	}
	d.pos++
	d.skipWS()
	if d.pos >= len(d.data) {
		return nil, errMalformedJSON
	}
	if d.data[d.pos] == ']' {
		d.pos++
		return nil, publicerr.InvalidSchema
	}

	initCap := min(maxItems, 16)
	out := make([]string, 0, initCap)
	var seen map[string]struct{}
	for {
		v, err := d.decodeBoundedString(maxRunes)
		if err != nil {
			return nil, err
		}
		if len(out) >= maxItems {
			return nil, publicerr.InvalidSchema
		}
		if seen != nil {
			if _, dup := seen[v]; dup {
				return nil, publicerr.InvalidSchema
			}
			seen[v] = struct{}{}
		} else if len(out) < 16 {
			if slices.Contains(out, v) {
				return nil, publicerr.InvalidSchema
			}
		} else {
			seen = make(map[string]struct{}, maxItems)
			for _, existing := range out {
				seen[existing] = struct{}{}
			}
			if _, dup := seen[v]; dup {
				return nil, publicerr.InvalidSchema
			}
			seen[v] = struct{}{}
		}
		out = append(out, v)

		d.skipWS()
		if d.pos >= len(d.data) {
			return nil, errMalformedJSON
		}
		if d.data[d.pos] == ',' {
			d.pos++
			d.skipWS()
			continue
		}
		if d.data[d.pos] == ']' {
			d.pos++
			return out, nil
		}
		return nil, errMalformedJSON
	}
}

type parsedSearchFilter struct {
	sourceNamespaces []string
	recordIDs        []string
	versionIDs       []string
	metadata         []corpus.MetadataFilter
}

func (d *searchDecoder) parseSearchFilter() (parsedSearchFilter, error) {
	if d.pos >= len(d.data) {
		return parsedSearchFilter{}, errMalformedJSON
	}
	if d.data[d.pos] != '{' {
		return parsedSearchFilter{}, publicerr.InvalidSchema
	}
	d.pos++
	d.skipWS()
	if d.pos >= len(d.data) {
		return parsedSearchFilter{}, errMalformedJSON
	}
	if d.data[d.pos] == '}' {
		d.pos++
		// SearchFilter requires minProperties: 1.
		return parsedSearchFilter{}, publicerr.InvalidSchema
	}

	const (
		fltSourceNamespaces uint8 = 1 << iota
		fltRecordIDs
		fltVersionIDs
		fltMetadata
	)
	var (
		out     parsedSearchFilter
		seen    uint8
		invalid uint8
	)
	for {
		key, err := d.scanKey()
		if err != nil {
			return parsedSearchFilter{}, err
		}
		if err := d.expectColon(); err != nil {
			return parsedSearchFilter{}, err
		}
		valStart := d.pos
		var bit uint8
		switch string(key) {
		case "source_namespaces":
			bit = fltSourceNamespaces
			var ns []string
			ns, err = d.parseUniqueStrings(maxSearchFilterStrings, maxSearchFilterStringLen)
			if err == nil {
				out.sourceNamespaces = ns
			}
		case "record_ids":
			bit = fltRecordIDs
			var ids []string
			ids, err = d.parseUniqueStrings(maxSearchFilterStrings, maxSearchFilterStringLen)
			if err == nil {
				out.recordIDs = ids
			}
		case "version_ids":
			bit = fltVersionIDs
			var ids []string
			ids, err = d.parseUniqueStrings(maxSearchFilterStrings, maxSearchFilterStringLen)
			if err == nil {
				out.versionIDs = ids
			}
		case "metadata":
			bit = fltMetadata
			var md []corpus.MetadataFilter
			md, err = d.parseMetadataFilters()
			if err == nil {
				out.metadata = md
			}
		default:
			return parsedSearchFilter{}, publicerr.InvalidSchema
		}
		if err != nil {
			if !errors.Is(err, publicerr.InvalidSchema) {
				return parsedSearchFilter{}, err
			}
			d.pos = valStart
			if skipErr := d.skipValue(); skipErr != nil {
				return parsedSearchFilter{}, skipErr
			}
			seen |= bit
			invalid |= bit
		} else {
			seen |= bit
			invalid &^= bit
		}

		d.skipWS()
		if d.pos >= len(d.data) {
			return parsedSearchFilter{}, errMalformedJSON
		}
		if d.data[d.pos] == ',' {
			d.pos++
			d.skipWS()
			continue
		}
		if d.data[d.pos] == '}' {
			d.pos++
			break
		}
		return parsedSearchFilter{}, errMalformedJSON
	}

	if invalid != 0 || seen == 0 {
		return parsedSearchFilter{}, publicerr.InvalidSchema
	}
	return out, nil
}

func (d *searchDecoder) parseMetadataFilters() ([]corpus.MetadataFilter, error) {
	if d.pos >= len(d.data) {
		return nil, errMalformedJSON
	}
	if d.data[d.pos] != '[' {
		return nil, publicerr.InvalidSchema
	}
	d.pos++
	d.skipWS()
	if d.pos >= len(d.data) {
		return nil, errMalformedJSON
	}
	if d.data[d.pos] == ']' {
		d.pos++
		return nil, publicerr.InvalidSchema
	}

	out := make([]corpus.MetadataFilter, 0, 4)
	for {
		f, err := d.parseMetadataFilter()
		if err != nil {
			return nil, err
		}
		if len(out) >= maxSearchMetadataFilters {
			return nil, publicerr.InvalidSchema
		}
		out = append(out, f)

		d.skipWS()
		if d.pos >= len(d.data) {
			return nil, errMalformedJSON
		}
		if d.data[d.pos] == ',' {
			d.pos++
			d.skipWS()
			continue
		}
		if d.data[d.pos] == ']' {
			d.pos++
			return out, nil
		}
		return nil, errMalformedJSON
	}
}

func (d *searchDecoder) parseMetadataFilter() (corpus.MetadataFilter, error) {
	if d.pos >= len(d.data) {
		return corpus.MetadataFilter{}, errMalformedJSON
	}
	if d.data[d.pos] != '{' {
		return corpus.MetadataFilter{}, publicerr.InvalidSchema
	}
	d.pos++
	d.skipWS()
	if d.pos >= len(d.data) {
		return corpus.MetadataFilter{}, errMalformedJSON
	}
	if d.data[d.pos] == '}' {
		d.pos++
		return corpus.MetadataFilter{}, publicerr.InvalidSchema
	}

	const (
		mdField uint8 = 1 << iota
		mdAnyOf
		mdGte
		mdLte
	)
	var (
		out     corpus.MetadataFilter
		seen    uint8
		invalid uint8
	)
	for {
		key, err := d.scanKey()
		if err != nil {
			return corpus.MetadataFilter{}, err
		}
		if err := d.expectColon(); err != nil {
			return corpus.MetadataFilter{}, err
		}
		valStart := d.pos
		var bit uint8
		switch string(key) {
		case "field":
			bit = mdField
			var v string
			v, err = d.decodeBoundedString(0)
			if err == nil && !isValidMetadataFieldName(v) {
				err = publicerr.InvalidSchema
			}
			if err == nil {
				out.Field = v
			}
		case "any_of":
			bit = mdAnyOf
			var vals []any
			vals, err = d.parseMetadataAnyOf()
			if err == nil {
				out.AnyOf = vals
			}
		case "gte":
			bit = mdGte
			var v string
			v, err = d.parseRFC3339String()
			if err == nil {
				out.Gte = v
			}
		case "lte":
			bit = mdLte
			var v string
			v, err = d.parseRFC3339String()
			if err == nil {
				out.Lte = v
			}
		default:
			return corpus.MetadataFilter{}, publicerr.InvalidSchema
		}
		if err != nil {
			if !errors.Is(err, publicerr.InvalidSchema) {
				return corpus.MetadataFilter{}, err
			}
			d.pos = valStart
			if skipErr := d.skipValue(); skipErr != nil {
				return corpus.MetadataFilter{}, skipErr
			}
			seen |= bit
			invalid |= bit
		} else {
			seen |= bit
			invalid &^= bit
		}

		d.skipWS()
		if d.pos >= len(d.data) {
			return corpus.MetadataFilter{}, errMalformedJSON
		}
		if d.data[d.pos] == ',' {
			d.pos++
			d.skipWS()
			continue
		}
		if d.data[d.pos] == '}' {
			d.pos++
			break
		}
		return corpus.MetadataFilter{}, errMalformedJSON
	}

	const conditionBits = mdAnyOf | mdGte | mdLte
	if invalid != 0 || seen&mdField == 0 || seen&conditionBits == 0 {
		return corpus.MetadataFilter{}, publicerr.InvalidSchema
	}
	return out, nil
}

func (d *searchDecoder) parseMetadataAnyOf() ([]any, error) {
	if d.pos >= len(d.data) {
		return nil, errMalformedJSON
	}
	if d.data[d.pos] != '[' {
		return nil, publicerr.InvalidSchema
	}
	d.pos++
	d.skipWS()
	if d.pos >= len(d.data) {
		return nil, errMalformedJSON
	}
	if d.data[d.pos] == ']' {
		d.pos++
		return nil, publicerr.InvalidSchema
	}

	out := make([]any, 0, 4)
	for {
		if d.pos >= len(d.data) {
			return nil, errMalformedJSON
		}
		if len(out) >= maxSearchMetadataValues {
			return nil, publicerr.InvalidSchema
		}
		var elem any
		switch d.data[d.pos] {
		case '"':
			v, err := d.decodeBoundedString(maxSearchFilterStringLen)
			if err != nil {
				return nil, err
			}
			elem = v
		case 't':
			if d.pos+4 <= len(d.data) && string(d.data[d.pos:d.pos+4]) == "true" {
				d.pos += 4
				elem = true
			} else {
				return nil, errMalformedJSON
			}
		case 'f':
			if d.pos+5 <= len(d.data) && string(d.data[d.pos:d.pos+5]) == "false" {
				d.pos += 5
				elem = false
			} else {
				return nil, errMalformedJSON
			}
		case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			numStart := d.pos
			if err := d.skipNumber(); err != nil {
				return nil, err
			}
			var numStr string
			if d.rawStr != "" {
				numStr = d.rawStr[numStart:d.pos]
			} else {
				numStr = string(d.data[numStart:d.pos])
			}
			f, err := strconv.ParseFloat(numStr, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				d.sawOverflowFloat = true
				elem = json.Number(numStr)
			} else {
				elem = f
			}
		case 'n', '{', '[':
			return nil, publicerr.InvalidSchema
		default:
			return nil, errMalformedJSON
		}
		out = append(out, elem)

		d.skipWS()
		if d.pos >= len(d.data) {
			return nil, errMalformedJSON
		}
		if d.data[d.pos] == ',' {
			d.pos++
			d.skipWS()
			continue
		}
		if d.data[d.pos] == ']' {
			d.pos++
			return out, nil
		}
		return nil, errMalformedJSON
	}
}

func (d *searchDecoder) parseRFC3339String() (string, error) {
	if d.pos >= len(d.data) {
		return "", errMalformedJSON
	}
	if d.data[d.pos] != '"' {
		return "", publicerr.InvalidSchema
	}
	v, err := d.decodeOwnedString()
	if err != nil {
		return "", err
	}
	normalized, ok := parseStrictRFC3339(v)
	if !ok {
		return "", publicerr.InvalidSchema
	}
	return normalized, nil
}

// parseStrictRFC3339 validates and canonicalizes an RFC 3339 timestamp with the
// exact same rules as time.Time.UnmarshalJSON + time.Time.MarshalJSON.
func parseStrictRFC3339(v string) (string, bool) {
	if len(v) < 20 || len(v) > 64 {
		return "", false
	}
	var buf [66]byte
	buf[0] = '"'
	n := copy(buf[1:], v)
	buf[n+1] = '"'
	var ts time.Time
	if err := ts.UnmarshalJSON(buf[:n+2]); err != nil {
		return "", false
	}
	if len(v) == 20 && v[19] == 'Z' {
		return v, true
	}
	return ts.Format(time.RFC3339Nano), true
}

// isValidMetadataFieldName matches OpenAPI's ^(metadata\.)?[a-z][a-z0-9_]{0,63}$.
func isValidMetadataFieldName(s string) bool {
	s = strings.TrimPrefix(s, "metadata.")
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// usageToTransport is what a search spent, with each phase in whole
// milliseconds, rounded down.
func usageToTransport(u retrieval.Usage) *transport.SearchUsage {
	// A search that ranked nothing spent no round, and the contract requires
	// at least one: its phases reach the Server-Timing header only.
	if u.Rounds == 0 {
		return nil
	}
	ph := u.Phases
	profiles := make([]transport.SearchProfileUsage, 0, len(u.Profiles))
	for _, p := range u.Profiles {
		profiles = append(profiles, transport.SearchProfileUsage{Profile: p.Profile, PluginVersion: p.PluginVersion, Rounds: p.Rounds, PaidCalls: p.PaidCalls, CostCents: float32(p.CostCents)})
	}
	out := &transport.SearchUsage{Rounds: u.Rounds, ElapsedMs: int(u.Elapsed.Milliseconds()), PaidCalls: u.PaidCalls, CostCents: float32(u.CostCents),
		Phases: &transport.SearchPhases{RoutingMs: int(ph.Routing.Milliseconds()), CoverageMs: int(ph.Coverage.Milliseconds()), PluginRoundsMs: int(ph.PluginRounds.Milliseconds()),
			QueryEncodingMs: int(ph.QueryEncoding.Milliseconds()), IndexQueryMs: int(ph.IndexQuery.Milliseconds()), HydrationMs: int(ph.Hydration.Milliseconds())}}
	if len(profiles) > 0 {
		out.Profiles = &profiles
	}
	return out
}

// serverTiming renders the phases as a Server-Timing header value, each metric
// in milliseconds with microsecond precision.
func serverTiming(p retrieval.Phases) string {
	metrics := []struct {
		name string
		d    time.Duration
	}{{"routing", p.Routing}, {"coverage", p.Coverage}, {"plugin", p.PluginRounds}, {"encode", p.QueryEncoding}, {"index", p.IndexQuery}, {"hydrate", p.Hydration}}
	parts := make([]string, len(metrics))
	for i, m := range metrics {
		parts[i] = m.name + ";dur=" + strconv.FormatFloat(float64(m.d.Microseconds())/1000, 'f', -1, 64)
	}
	return strings.Join(parts, ", ")
}

// searchProfiles lists the search profiles this deployment answers.
func (a *API) searchProfiles(w http.ResponseWriter, scope corpus.Scope) {
	profiles, err := a.Retrieval.ProfilesScoped(scope)
	if err != nil {
		writeError(w, publicerr.Forbidden, nil)
		return
	}
	out := transport.SearchProfileList{Items: []transport.SearchProfileDescription{}}
	for _, p := range profiles {
		latency, cost := p.MaxLatencyMS, float32(p.MaxCostCents)
		item := transport.SearchProfileDescription{Name: p.Name, FullName: p.FullName, Aliases: p.Aliases, Description: optionalString(p.Description), MaxLatencyMs: &latency, MaxCostCents: &cost}
		item.Provider.Kind = transport.SearchProfileDescriptionProviderKindPlugin
		item.Provider.PluginId, item.Provider.PluginVersion = optionalString(p.PluginID), optionalString(p.PluginVersion)
		out.Items = append(out.Items, item)
	}
	send(w, 200, out)
}
