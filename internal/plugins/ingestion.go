package plugins

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// Routes of the ingestion Contribution (Plugin API 0.6).
const (
	SegmentAndEmbedRoute = "/v0/contributions/ingestion/segment_and_embed"
	EmbedQueryRoute      = "/v0/contributions/ingestion/embed_query"
)

// Issue codes for ingestion answers.
const (
	CodeOffsetOutOfRange    = "offset_out_of_range"
	CodeEmptySegment        = "empty_segment"
	CodeDuplicateSegment    = "duplicate_segment"
	CodeTooManySegments     = "too_many_segments"
	CodeMissingVector       = "missing_vector"
	CodeUnrequestedSpace    = "unrequested_space"
	CodeDimensionMismatch   = "dimension_mismatch"
	CodeInvalidVector       = "invalid_vector"
	CodeInvalidLexicalText  = "invalid_lexical_text"
	CodeLexicalTextTooLarge = "lexical_text_too_large"
	CodeProvenanceTooLarge  = "provenance_too_large"
	CodeInvalidProvenance   = "invalid_provenance"
)

// Bounds of one segment of a segment_and_embed answer.
const (
	MaxLexicalTextRunes = 16384
	MaxProvenanceBytes  = 4 << 10
	// EmbedQueryMaxResponseBytes bounds an embed_query answer: one vector of
	// at most 4096 numbers.
	EmbedQueryMaxResponseBytes = 1 << 20
)

// SpaceKey is the identity of a declared vector space: its id and its
// version. Vectors of another version belong to another space.
func SpaceKey(id, version string) string { return id + "@" + version }

// SpaceManifest is the registry's description of a declared space: its
// identity, owner and properties as canonical JSON. It excludes the owner's
// version, which may change while the space stays the same.
func SpaceManifest(pluginID, id string, s VectorSpace) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"space": id, "version": s.Version, "owner": pluginID, "model": s.Model,
		"dimensions": s.Dimensions, "metric": s.Metric, "indexes": s.Indexes, "query_modalities": s.QueryModalities})
	return b
}

// IngestionMaxResponseBytes is the declared max_response_bytes of the
// ingestion Contribution, capped by EngineMaxResponseBytes.
func IngestionMaxResponseBytes(m *Manifest) int {
	limit := EngineMaxResponseBytes
	if m != nil && m.Contributions.Ingestion != nil && m.Contributions.Ingestion.Limits.MaxResponseBytes > 0 {
		limit = m.Contributions.Ingestion.Limits.MaxResponseBytes
	}
	return min(limit, EngineMaxResponseBytes)
}

// IngestionMaxSegments is the declared max_segments (or its default).
func IngestionMaxSegments(m *Manifest) int {
	if m != nil && m.Contributions.Ingestion != nil && m.Contributions.Ingestion.Limits.MaxSegments > 0 {
		return m.Contributions.Ingestion.Limits.MaxSegments
	}
	return DefaultMaxSegments
}

// IngestionPart is one text Part of a segment_and_embed request.
type IngestionPart struct {
	Key  string `json:"key"`
	Role string `json:"role"`
	Text string `json:"text"`
}

// IngestionRequestView is what output validation needs from a
// segment_and_embed request: the Parts and the requested spaces.
type IngestionRequestView struct {
	Parts  []IngestionPart `json:"parts"`
	Spaces []string        `json:"spaces"`
}

// ViewIngestionRequest reads a segment_and_embed request body.
func ViewIngestionRequest(request []byte) (IngestionRequestView, error) {
	var view IngestionRequestView
	err := json.Unmarshal(request, &view)
	return view, err
}

// IngestionSegment is one decoded segment of a valid answer. Offsets are
// Unicode code points in the Part text.
type IngestionSegment struct {
	PartKey     string               `json:"part_key"`
	Start       int                  `json:"start"`
	End         int                  `json:"end"`
	Vectors     map[string][]float64 `json:"vectors"`
	LexicalText string               `json:"lexical_text,omitempty"`
	Provenance  json.RawMessage      `json:"provenance,omitempty"`
}

// IngestionAnswer is a decoded segment_and_embed answer.
type IngestionAnswer struct {
	Segments []IngestionSegment `json:"segments"`
}

// CheckSegmentAndEmbedOutput judges a 200 segment_and_embed answer exactly as
// the engine does before it stores anything: the response bound, the
// response schema (unknown fields rejected), then the segment rules: a Part
// of the request, offsets inside its text, an empty segment only beside a
// title Part, no duplicate, at most max_segments, exactly one vector per
// requested space with the declared dimensions and finite values, and
// bounded lexical text and provenance.
func CheckSegmentAndEmbedOutput(raw []byte, request IngestionRequestView, m *Manifest) []Issue {
	if limit := IngestionMaxResponseBytes(m); len(raw) > limit {
		return []Issue{{Code: CodeResponseTooLarge, Message: fmt.Sprintf("the response is %d bytes; the limit is %d (declared max_response_bytes, capped by the engine at %d)", len(raw), limit, EngineMaxResponseBytes)}}
	}
	if issues := ValidateDocument("ingestion-segment-and-embed-response.schema.json", raw); len(issues) > 0 {
		return issues
	}
	var answer IngestionAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		return []Issue{{Code: CodeSchema, Path: "/segments", Message: err.Error()}}
	}
	var issues []Issue
	if limit := IngestionMaxSegments(m); len(answer.Segments) > limit {
		issues = append(issues, Issue{Code: CodeTooManySegments, Path: "/segments",
			Message: fmt.Sprintf("%d segments; the manifest declares at most %d (limits.max_segments)", len(answer.Segments), limit)})
	}
	parts := map[string]int{}
	hasTitle := false
	keys := make([]string, 0, len(request.Parts))
	for _, p := range request.Parts {
		parts[p.Key] = utf8.RuneCountInString(p.Text)
		keys = append(keys, p.Key)
		if p.Role == "title" {
			hasTitle = true
		}
	}
	seen := map[string]int{}
	for i, s := range answer.Segments {
		path := fmt.Sprintf("/segments/%d", i)
		length, known := parts[s.PartKey]
		switch {
		case !known:
			issues = append(issues, Issue{Code: CodeUnknownPartKey, Path: path + "/part_key",
				Message: fmt.Sprintf("Part key %q is not a Part of the request (Parts: %v)", s.PartKey, keys)})
		case s.Start > s.End || s.End > length:
			issues = append(issues, Issue{Code: CodeOffsetOutOfRange, Path: path,
				Message: fmt.Sprintf("offsets [%d, %d) are outside Part %q, which has %d code points; start <= end <= length", s.Start, s.End, s.PartKey, length)})
		case s.Start == s.End && !hasTitle:
			issues = append(issues, Issue{Code: CodeEmptySegment, Path: path,
				Message: fmt.Sprintf("segment [%d, %d) of Part %q is empty; an empty segment stands for the title alone, so it needs a Part with the role title", s.Start, s.End, s.PartKey)})
		}
		identity := fmt.Sprintf("%s\x00%d\x00%d", s.PartKey, s.Start, s.End)
		if first, dup := seen[identity]; dup {
			issues = append(issues, Issue{Code: CodeDuplicateSegment, Path: path,
				Message: fmt.Sprintf("segment [%d, %d) of Part %q repeats /segments/%d; return each segment once", s.Start, s.End, s.PartKey, first)})
		} else {
			seen[identity] = i
		}
		issues = append(issues, vectorIssues(path+"/vectors", s.Vectors, request.Spaces, m)...)
		if strings.ContainsRune(s.LexicalText, 0) || !utf8.ValidString(s.LexicalText) {
			issues = append(issues, Issue{Code: CodeInvalidLexicalText, Path: path + "/lexical_text", Message: "the lexical text contains a NUL character or invalid UTF-8, which the engine cannot index"})
		} else if n := utf8.RuneCountInString(s.LexicalText); n > MaxLexicalTextRunes {
			issues = append(issues, Issue{Code: CodeLexicalTextTooLarge, Path: path + "/lexical_text",
				Message: fmt.Sprintf("the lexical text has %d code points; the engine indexes at most %d", n, MaxLexicalTextRunes)})
		}
		if len(s.Provenance) > 0 {
			var provenance any
			_ = json.Unmarshal(s.Provenance, &provenance)
			compact, _ := json.Marshal(provenance)
			switch {
			case len(compact) > MaxProvenanceBytes:
				issues = append(issues, Issue{Code: CodeProvenanceTooLarge, Path: path + "/provenance",
					Message: fmt.Sprintf("the provenance serializes to %d bytes of JSON; the engine stores at most %d", len(compact), MaxProvenanceBytes)})
			case containsNUL(provenance):
				issues = append(issues, Issue{Code: CodeInvalidProvenance, Path: path + "/provenance", Message: "the provenance contains a NUL character, which the engine cannot store"})
			}
		}
	}
	return issues
}

// vectorIssues checks one vector per requested space, with the declared
// dimensions and values a 32-bit float index can hold.
func vectorIssues(path string, vectors map[string][]float64, requested []string, m *Manifest) []Issue {
	var issues []Issue
	wanted := map[string]bool{}
	for _, id := range requested {
		wanted[id] = true
		if _, ok := vectors[id]; !ok {
			issues = append(issues, Issue{Code: CodeMissingVector, Path: path,
				Message: fmt.Sprintf("no vector for space %q; return one vector for every requested space (%v)", id, requested)})
		}
	}
	ids := make([]string, 0, len(vectors))
	for id := range vectors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !wanted[id] {
			issues = append(issues, Issue{Code: CodeUnrequestedSpace, Path: path + "/" + pointerToken(id),
				Message: fmt.Sprintf("a vector for space %q, which the request does not ask for (requested: %v)", id, requested)})
			continue
		}
		if issue := checkVector(path+"/"+pointerToken(id), id, vectors[id], m); issue != nil {
			issues = append(issues, *issue)
		}
	}
	return issues
}

// checkVector judges one vector against the declared space.
func checkVector(path, space string, vector []float64, m *Manifest) *Issue {
	declared, ok := DeclaredSpace(m, space)
	if !ok {
		return &Issue{Code: CodeUnrequestedSpace, Path: path, Message: fmt.Sprintf("space %q is not declared by the manifest", space)}
	}
	if len(vector) != declared.Dimensions {
		return &Issue{Code: CodeDimensionMismatch, Path: path,
			Message: fmt.Sprintf("%d numbers; space %q declares %d dimensions", len(vector), space, declared.Dimensions)}
	}
	zero := true
	for i, x := range vector {
		if math.IsNaN(x) || math.IsInf(float64(float32(x)), 0) {
			return &Issue{Code: CodeInvalidVector, Path: fmt.Sprintf("%s/%d", path, i), Message: fmt.Sprintf("%v does not fit a finite 32-bit float", x)}
		}
		if x != 0 {
			zero = false
		}
	}
	if zero && declared.Metric == "cosine" {
		return &Issue{Code: CodeInvalidVector, Path: path, Message: fmt.Sprintf("an all-zero vector has no direction, so space %q (cosine) cannot rank it", space)}
	}
	return nil
}

// DeclaredSpace returns a space the manifest's ingestion Contribution declares.
func DeclaredSpace(m *Manifest, id string) (VectorSpace, bool) {
	if m == nil || m.Contributions.Ingestion == nil {
		return VectorSpace{}, false
	}
	space, ok := m.Contributions.Ingestion.Spaces[id]
	return space, ok
}

// DecodeSegmentAndEmbed decodes an answer CheckSegmentAndEmbedOutput accepted.
func DecodeSegmentAndEmbed(raw []byte) (IngestionAnswer, error) {
	var answer IngestionAnswer
	err := json.Unmarshal(raw, &answer)
	return answer, err
}

// CheckEmbedQueryOutput judges a 200 embed_query answer: the bound, the
// schema, then one vector with the space's declared dimensions.
func CheckEmbedQueryOutput(raw []byte, space string, m *Manifest) []Issue {
	if len(raw) > EmbedQueryMaxResponseBytes {
		return []Issue{{Code: CodeResponseTooLarge, Message: fmt.Sprintf("the response is %d bytes; an embed_query answer is at most %d", len(raw), EmbedQueryMaxResponseBytes)}}
	}
	if issues := ValidateDocument("ingestion-embed-query-response.schema.json", raw); len(issues) > 0 {
		return issues
	}
	var answer struct {
		Vector []float64 `json:"vector"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return []Issue{{Code: CodeSchema, Path: "/vector", Message: err.Error()}}
	}
	if issue := checkVector("/vector", space, answer.Vector, m); issue != nil {
		return []Issue{*issue}
	}
	return nil
}

// Float32s converts a checked vector to the 32-bit floats the index stores.
func Float32s(vector []float64) []float32 {
	out := make([]float32, len(vector))
	for i, x := range vector {
		out[i] = float32(x)
	}
	return out
}

// SegmentsOnly reports whether a manifest's plugin_api range admits
// SegmentOnlySince, so segment_and_embed may ask it for no space.
func SegmentsOnly(m *Manifest) bool {
	if m == nil {
		return false
	}
	r, err := ParseRange(m.Compatibility.PluginAPI)
	if err != nil {
		return false
	}
	_, ok := admits(r, SegmentOnlySince)
	return ok
}
