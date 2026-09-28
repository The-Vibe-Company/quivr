package content

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// SegmentText is the searchable text a projection indexes for one segment.
type SegmentText struct{ Title, Body string }

// ProjectionText derives each segment's projected text from the Version's
// canonical source view and a generation's pinned retrieval fields. Without
// search mappings it is exactly the segment title and canonical segment text.
//
// A search field named corpus.TitleField replaces the projected title. Every
// other search field is placed once per Version, after the canonical text of
// the first (title-bearing) segment, so a long Record does not multiply its
// term frequency. Excerpts and vectors stay canonical: a hit may match on
// mapped text that its canonical excerpt does not contain. Missing or
// mistyped values contribute nothing and are counted as skipped.
func ProjectionText(v Version, seg Segmentation, fields []corpus.Field) ([]SegmentText, int) {
	out := make([]SegmentText, len(seg.Segments))
	for i, p := range seg.Segments {
		out[i] = SegmentText{Title: p.Title, Body: p.Text}
	}
	if len(fields) == 0 || len(out) == 0 {
		return out, 0
	}
	view := sourceView(v)
	skipped, title, extra := 0, "", []string{}
	for _, f := range fields {
		if !f.Searchable() {
			continue
		}
		text, ok := fieldText(view, f)
		if !ok {
			skipped++
			continue
		}
		if f.Name == corpus.TitleField {
			title = text
		} else {
			extra = append(extra, text)
		}
	}
	if title != "" {
		for i := range out {
			out[i].Title = title
		}
	}
	if len(extra) > 0 {
		first := 0
		for i, p := range seg.Segments {
			if p.Derivation.Ordinal < seg.Segments[first].Derivation.Ordinal {
				first = i
			}
		}
		out[first].Body = strings.Join(append([]string{out[first].Body}, extra...), "\n\n")
	}
	return out, skipped
}

// sourceView is the canonical source representation mapping pointers address.
func sourceView(v Version) any {
	b, _ := json.Marshal(struct {
		Manifest   Manifest       `json:"manifest"`
		Extensions Extensions     `json:"extensions"`
		Provenance map[string]any `json:"provenance"`
	}{v.Manifest, v.Extensions, v.Provenance})
	var view any
	_ = json.Unmarshal(b, &view)
	return view
}

func fieldText(view any, f corpus.Field) (string, bool) {
	tokens, ok := corpus.PointerTokens(f.SourcePointer)
	if !ok {
		return "", false
	}
	node := view
	for _, token := range tokens {
		switch current := node.(type) {
		case map[string]any:
			node, ok = current[token]
		case []any:
			i, err := strconv.Atoi(token)
			ok = err == nil && i >= 0 && i < len(current) && strconv.Itoa(i) == token
			if ok {
				node = current[i]
			}
		default:
			ok = false
		}
		if !ok {
			return "", false
		}
	}
	switch f.Type {
	case "string":
		s, ok := node.(string)
		return s, ok && s != ""
	case "string_array":
		items, ok := node.([]any)
		if !ok || len(items) == 0 {
			return "", false
		}
		values := make([]string, len(items))
		for i, item := range items {
			s, ok := item.(string)
			if !ok {
				return "", false
			}
			values[i] = s
		}
		return strings.Join(values, "\n"), true
	}
	return "", false
}
