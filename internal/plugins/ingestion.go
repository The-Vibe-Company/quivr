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
	CodeOffsetOutOfRange       = "offset_out_of_range"
	CodeEmptySegment           = "empty_segment"
	CodeDuplicateSegment       = "duplicate_segment"
	CodeDuplicateSourceRange   = "duplicate_source_range"
	CodeSourceRangeOrder       = "source_range_order"
	CodeSourceRangeAnchor      = "source_range_anchor_mismatch"
	CodeSourceRangeEmpty       = "empty_source_range"
	CodeSourceRangeTooLarge    = "source_range_too_large"
	CodePackedTextTooLarge     = "packed_text_too_large"
	CodeTooManySourceRanges    = "too_many_source_ranges"
	CodeInvalidSourceSeparator = "invalid_source_separator"
	CodeMultiPartUnsupported   = "multi_part_segments_unsupported"
	CodeTooManySegments        = "too_many_segments"
	CodeMissingVector          = "missing_vector"
	CodeUnrequestedSpace       = "unrequested_space"
	CodeDimensionMismatch      = "dimension_mismatch"
	CodeInvalidVector          = "invalid_vector"
	CodeInvalidLexicalText     = "invalid_lexical_text"
	CodeLexicalTextTooLarge    = "lexical_text_too_large"
	CodeProvenanceTooLarge     = "provenance_too_large"
	CodeInvalidProvenance      = "invalid_provenance"
	CodeInvalidRegion          = "invalid_region"
	CodeMultimodalUnsupported  = "multimodal_ingestion_unsupported"
)

// Bounds of one segment of a segment_and_embed answer.
const (
	MaxLexicalTextRunes     = 16384
	MaxProvenanceBytes      = 4 << 10
	MaxSourceRanges         = 256
	MaxSourceRangeRunes     = 4096
	MaxPackedTextRunes      = 16384
	MaxSourceSeparatorRunes = 16
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

// IngestionBlobPart is one verified Blob Part passed by reference to a
// multimodal ingestion plugin (Plugin API 0.20).
type IngestionBlobPart struct {
	Key       string              `json:"key"`
	Role      string              `json:"role"`
	BlobID    string              `json:"blob_id"`
	MediaType string              `json:"media_type"`
	SizeBytes int64               `json:"size_bytes"`
	SHA256    string              `json:"sha256"`
	Reference NormalizerReference `json:"reference"`
}

// ImageRegion is normalized bounding-box provenance for an image segment.
type ImageRegion struct {
	CoordinateSystem string  `json:"coordinate_system"`
	X                float64 `json:"x"`
	Y                float64 `json:"y"`
	Width            float64 `json:"width"`
	Height           float64 `json:"height"`
	ImageWidth       int     `json:"image_width,omitempty"`
	ImageHeight      int     `json:"image_height,omitempty"`
	Label            string  `json:"label,omitempty"`
}

// SourceRange identifies one non-empty Unicode code point slice of a request
// Part. Ranges in one segment are listed in the request's reading order.
type SourceRange struct {
	PartKey string `json:"part_key"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
}

// IngestionRequestView is what output validation needs from a
// segment_and_embed request: the Parts and the requested spaces.
type IngestionRequestView struct {
	Parts     []IngestionPart       `json:"parts"`
	BlobParts []IngestionBlobPart   `json:"blob_parts,omitempty"`
	Spaces    []string              `json:"spaces"`
	Page      *IngestionPageRequest `json:"page,omitempty"`
}

// IngestionPageRequest bounds one call without bounding the item's coverage.
type IngestionPageRequest struct {
	Start       int `json:"start"`
	MaxSegments int `json:"max_segments"`
}

// ViewIngestionRequest reads a segment_and_embed request body.
func ViewIngestionRequest(request []byte) (IngestionRequestView, error) {
	var view IngestionRequestView
	err := json.Unmarshal(request, &view)
	return view, err
}

// IngestionSegment is one decoded segment of a valid answer. Offsets are
// Unicode code points in the Part text, or [0, 0) with Region for a Blob Part.
type IngestionSegment struct {
	PartKey         string               `json:"part_key"`
	Start           int                  `json:"start"`
	End             int                  `json:"end"`
	SourceRanges    []SourceRange        `json:"source_ranges,omitempty"`
	SourceSeparator string               `json:"source_separator,omitempty"`
	Vectors         map[string][]float64 `json:"vectors"`
	LexicalText     string               `json:"lexical_text,omitempty"`
	Region          *ImageRegion         `json:"region,omitempty"`
	Provenance      json.RawMessage      `json:"provenance,omitempty"`
}

// IngestionAnswer is a decoded segment_and_embed answer.
type IngestionAnswer struct {
	Segments  []IngestionSegment `json:"segments"`
	NextStart *int               `json:"next_start,omitempty"`
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
	var wire struct {
		Segments []map[string]json.RawMessage `json:"segments"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return []Issue{{Code: CodeSchema, Path: "/segments", Message: err.Error()}}
	}
	var issues []Issue
	partOrder := map[string]int{}
	if limit := IngestionMaxSegments(m); len(answer.Segments) > limit {
		issues = append(issues, Issue{Code: CodeTooManySegments, Path: "/segments",
			Message: fmt.Sprintf("%d segments; the manifest declares at most %d (limits.max_segments)", len(answer.Segments), limit)})
	}
	parts := map[string]int{}
	blobParts := map[string]bool{}
	hasTitle := false
	keys := make([]string, 0, len(request.Parts)+len(request.BlobParts))
	for i, p := range request.Parts {
		parts[p.Key] = utf8.RuneCountInString(p.Text)
		partOrder[p.Key] = i
		keys = append(keys, p.Key)
		if p.Role == "title" {
			hasTitle = true
		}
	}
	for _, bp := range request.BlobParts {
		blobParts[bp.Key] = true
		keys = append(keys, bp.Key)
	}
	seen := map[string]int{}
	for i, s := range answer.Segments {
		path := fmt.Sprintf("/segments/%d", i)
		rangesPresent, separatorPresent, regionPresent := sourceFieldsPresent(wire.Segments, i)
		if (rangesPresent || separatorPresent) && !manifestSpeaks(m, FeatureMultiPartSegments) {
			field := "source_ranges"
			if !rangesPresent {
				field = "source_separator"
			}
			issues = append(issues, Issue{Code: CodeMultiPartUnsupported, Path: path + "/" + field,
				Message: fmt.Sprintf("%s requires Plugin API %s or later", field, FeatureSince(FeatureMultiPartSegments))})
		}
		if regionPresent && !manifestSpeaks(m, FeatureMultimodalIngestion) {
			issues = append(issues, Issue{Code: CodeMultimodalUnsupported, Path: path + "/region",
				Message: fmt.Sprintf("region requires Plugin API %s or later", FeatureSince(FeatureMultimodalIngestion))})
		}
		length, known := parts[s.PartKey]
		isBlob := blobParts[s.PartKey]
		switch {
		case !known && !isBlob:
			issues = append(issues, Issue{Code: CodeUnknownPartKey, Path: path + "/part_key",
				Message: fmt.Sprintf("Part key %q is not a Part of the request (Parts: %v)", s.PartKey, keys)})
		case isBlob:
			if !manifestSpeaks(m, FeatureMultimodalIngestion) {
				issues = append(issues, Issue{Code: CodeMultimodalUnsupported, Path: path + "/part_key",
					Message: fmt.Sprintf("Blob Part segments require Plugin API %s or later", FeatureSince(FeatureMultimodalIngestion))})
			}
			if s.Start != 0 || s.End != 0 {
				issues = append(issues, Issue{Code: CodeOffsetOutOfRange, Path: path,
					Message: fmt.Sprintf("Blob Part %q segment offsets must be [0, 0), got [%d, %d)", s.PartKey, s.Start, s.End)})
			}
			if s.Region == nil {
				issues = append(issues, Issue{Code: CodeInvalidRegion, Path: path + "/region",
					Message: fmt.Sprintf("Blob Part %q segment requires region provenance", s.PartKey)})
			}
			if len(s.SourceRanges) > 0 || s.SourceSeparator != "" {
				issues = append(issues, Issue{Code: CodeInvalidRegion, Path: path + "/source_ranges",
					Message: "Blob Part segments may not carry text source_ranges or source_separator"})
			}
		case s.Start > s.End || s.End > length:
			issues = append(issues, Issue{Code: CodeOffsetOutOfRange, Path: path,
				Message: fmt.Sprintf("offsets [%d, %d) are outside Part %q, which has %d code points; start <= end <= length", s.Start, s.End, s.PartKey, length)})
		case s.Start == s.End && !hasTitle:
			issues = append(issues, Issue{Code: CodeEmptySegment, Path: path,
				Message: fmt.Sprintf("segment [%d, %d) of Part %q is empty; an empty segment stands for the title alone, so it needs a Part with the role title", s.Start, s.End, s.PartKey)})
		}
		if known && s.Region != nil {
			issues = append(issues, Issue{Code: CodeInvalidRegion, Path: path + "/region",
				Message: fmt.Sprintf("region applies only to Blob Parts, not text Part %q", s.PartKey)})
		}
		if s.Region != nil {
			issues = append(issues, regionIssues(path+"/region", s.Region)...)
		}
		if len(s.SourceRanges) > MaxSourceRanges {
			issues = append(issues, Issue{Code: CodeTooManySourceRanges, Path: path + "/source_ranges",
				Message: fmt.Sprintf("%d source ranges; at most %d are allowed", len(s.SourceRanges), MaxSourceRanges)})
		}
		if strings.ContainsRune(s.SourceSeparator, 0) || !utf8.ValidString(s.SourceSeparator) {
			issues = append(issues, Issue{Code: CodeInvalidSourceSeparator, Path: path + "/source_separator",
				Message: "the source separator contains a NUL character or invalid UTF-8"})
		} else if n := utf8.RuneCountInString(s.SourceSeparator); n > MaxSourceSeparatorRunes {
			issues = append(issues, Issue{Code: CodeInvalidSourceSeparator, Path: path + "/source_separator",
				Message: fmt.Sprintf("the source separator has %d code points; at most %d are allowed", n, MaxSourceSeparatorRunes)})
		}
		if len(s.SourceRanges) > 0 && !isBlob {
			issues = append(issues, sourceRangeIssues(path+"/source_ranges", s, parts, partOrder)...)
		}
		identity := sourceSegmentIdentity(s)
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
	issues = append(issues, ingestionPageIssues(answer, request, m)...)
	return issues
}

func ingestionPageIssues(answer IngestionAnswer, request IngestionRequestView, m *Manifest) []Issue {
	bad := func(message string) []Issue { return []Issue{{Code: "invalid_ingestion_page", Message: message}} }
	if request.Page == nil {
		if answer.NextStart != nil {
			return bad("next_start requires a paged request")
		}
		return nil
	}
	if !manifestSpeaks(m, FeatureIngestionPages) {
		return bad("ingestion pages require Plugin API 0.18.0")
	}
	if len(request.Parts) != 1 || request.Page.Start < 0 || request.Page.MaxSegments < 1 || len(answer.Segments) > request.Page.MaxSegments {
		return bad("a page needs one Part and must respect its work bound")
	}
	part := request.Parts[0]
	end := request.Page.Start
	for _, segment := range answer.Segments {
		ranges := segment.SourceRanges
		if len(ranges) == 0 {
			ranges = []SourceRange{{PartKey: segment.PartKey, Start: segment.Start, End: segment.End}}
		}
		for _, r := range ranges {
			if r.PartKey != part.Key || r.Start != end || r.End <= r.Start {
				return bad("paged source ranges must cover every code point once in reading order")
			}
			end = r.End
		}
	}
	length := utf8.RuneCountInString(part.Text)
	if answer.NextStart != nil {
		if *answer.NextStart != end || end <= request.Page.Start || end >= length {
			return bad("next_start must advance to the first uncovered code point")
		}
	} else if end != length {
		return bad("a final page must cover the entire remaining source")
	}
	return nil
}

// sourceFieldsPresent reports whether an answer explicitly carries additive
// multi-Part or multimodal region fields. The decoded string cannot distinguish
// an omitted separator from an explicitly empty separator, so inspect the wire
// object for API admission checks.
func sourceFieldsPresent(segments []map[string]json.RawMessage, index int) (rangesPresent, separatorPresent, regionPresent bool) {
	if index < 0 || index >= len(segments) {
		return false, false, false
	}
	_, rangesPresent = segments[index]["source_ranges"]
	_, separatorPresent = segments[index]["source_separator"]
	_, regionPresent = segments[index]["region"]
	return rangesPresent, separatorPresent, regionPresent
}

const regionBoundEpsilon = 1e-6

func regionIssues(path string, r *ImageRegion) []Issue {
	var issues []Issue
	if r.CoordinateSystem != "normalized_bbox" {
		issues = append(issues, Issue{Code: CodeInvalidRegion, Path: path + "/coordinate_system",
			Message: fmt.Sprintf("unsupported region coordinate_system %q; expected normalized_bbox", r.CoordinateSystem)})
	}
	for _, f := range []struct {
		name string
		val  float64
	}{{"x", r.X}, {"y", r.Y}, {"width", r.Width}, {"height", r.Height}} {
		if math.IsNaN(f.val) || math.IsInf(f.val, 0) {
			issues = append(issues, Issue{Code: CodeInvalidRegion, Path: path + "/" + f.name,
				Message: fmt.Sprintf("region %s must be finite", f.name)})
		}
	}
	if r.X < 0 || r.X > 1 || r.Y < 0 || r.Y > 1 || r.Width <= 0 || r.Width > 1 || r.Height <= 0 || r.Height > 1 {
		issues = append(issues, Issue{Code: CodeInvalidRegion, Path: path,
			Message: "region coordinates must satisfy 0 <= x,y <= 1 and 0 < width,height <= 1"})
	} else if r.X+r.Width > 1+regionBoundEpsilon || r.Y+r.Height > 1+regionBoundEpsilon {
		issues = append(issues, Issue{Code: CodeInvalidRegion, Path: path,
			Message: fmt.Sprintf("region extends outside the normalized [0, 1] image frame (x+width=%g, y+height=%g)", r.X+r.Width, r.Y+r.Height)})
	}
	if r.Label != "" && (strings.ContainsRune(r.Label, 0) || !utf8.ValidString(r.Label) || utf8.RuneCountInString(r.Label) > 128) {
		issues = append(issues, Issue{Code: CodeInvalidRegion, Path: path + "/label",
			Message: "region label must be valid UTF-8 without NUL and at most 128 code points"})
	}
	return issues
}

func manifestSpeaks(m *Manifest, feature Feature) bool {
	if m == nil || m.Compatibility.PluginAPI == "" {
		return true
	}
	r, err := ParseRange(m.Compatibility.PluginAPI)
	if err != nil {
		return true
	}
	_, admitted := admitsFeature(r, feature)
	return admitted
}

func sourceSegmentIdentity(s IngestionSegment) string {
	if s.Region != nil {
		return fmt.Sprintf("%s\x00region\x00%s\x00%.6f\x00%.6f\x00%.6f\x00%.6f\x00%s",
			s.PartKey, s.Region.CoordinateSystem, s.Region.X, s.Region.Y, s.Region.Width, s.Region.Height, s.Region.Label)
	}
	if len(s.SourceRanges) == 0 {
		return fmt.Sprintf("%s\x00%d\x00%d", s.PartKey, s.Start, s.End)
	}
	var b strings.Builder
	for _, r := range s.SourceRanges {
		fmt.Fprintf(&b, "%s\x00%d\x00%d\x00", r.PartKey, r.Start, r.End)
	}
	return b.String()
}

func sourceRangeIssues(path string, segment IngestionSegment, parts map[string]int, partOrder map[string]int) []Issue {
	var issues []Issue
	if segment.SourceRanges[0].PartKey != segment.PartKey || segment.SourceRanges[0].Start != segment.Start || segment.SourceRanges[0].End != segment.End {
		issues = append(issues, Issue{Code: CodeSourceRangeAnchor, Path: path + "/0",
			Message: fmt.Sprintf("the legacy anchor [%d, %d) of Part %q must equal the first source range", segment.Start, segment.End, segment.PartKey)})
	}
	seen := map[string]int{}
	lastPart := -1
	lastEnd := map[string]int{}
	packedRunes := 0
	validRanges := 0
	for i, r := range segment.SourceRanges {
		rangePath := fmt.Sprintf("%s/%d", path, i)
		length, known := parts[r.PartKey]
		if !known {
			issues = append(issues, Issue{Code: CodeUnknownPartKey, Path: rangePath + "/part_key",
				Message: fmt.Sprintf("Part key %q is not a Part of the request", r.PartKey)})
			continue
		}
		if r.Start >= r.End {
			issues = append(issues, Issue{Code: CodeSourceRangeEmpty, Path: rangePath,
				Message: fmt.Sprintf("source range [%d, %d) of Part %q must be non-empty", r.Start, r.End, r.PartKey)})
		} else if r.Start < 0 || r.End > length {
			issues = append(issues, Issue{Code: CodeOffsetOutOfRange, Path: rangePath,
				Message: fmt.Sprintf("source range [%d, %d) is outside Part %q, which has %d code points", r.Start, r.End, r.PartKey, length)})
		} else if r.End-r.Start > MaxSourceRangeRunes {
			issues = append(issues, Issue{Code: CodeSourceRangeTooLarge, Path: rangePath,
				Message: fmt.Sprintf("source range spans %d code points; at most %d are allowed", r.End-r.Start, MaxSourceRangeRunes)})
		} else {
			if validRanges > 0 {
				packedRunes += utf8.RuneCountInString(segment.SourceSeparator)
			}
			packedRunes += r.End - r.Start
			validRanges++
		}
		identity := fmt.Sprintf("%s\x00%d\x00%d", r.PartKey, r.Start, r.End)
		if first, duplicate := seen[identity]; duplicate {
			issues = append(issues, Issue{Code: CodeDuplicateSourceRange, Path: rangePath,
				Message: fmt.Sprintf("source range repeats /%s/%d", path, first)})
			continue
		} else {
			seen[identity] = i
		}
		order := partOrder[r.PartKey]
		if order < lastPart || (order == lastPart && r.Start < lastEnd[r.PartKey]) {
			issues = append(issues, Issue{Code: CodeSourceRangeOrder, Path: rangePath,
				Message: "source ranges must follow request Part order and be increasing without overlap within a Part"})
		}
		if order > lastPart {
			lastPart = order
		}
		if r.End > lastEnd[r.PartKey] {
			lastEnd[r.PartKey] = r.End
		}
	}
	if packedRunes > MaxPackedTextRunes {
		issues = append(issues, Issue{Code: CodePackedTextTooLarge, Path: path,
			Message: fmt.Sprintf("joined source ranges span %d code points including separators; at most %d are allowed", packedRunes, MaxPackedTextRunes)})
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
