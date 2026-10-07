package content

import (
	"encoding/json"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
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

func fieldValue(view any, f corpus.Field) (any, bool) {
	var node any
	var ok bool
	if f.PartRole != "" {
		parts, found := pointerValue(view, "/manifest/parts")
		if !found {
			return "", false
		}
		items, _ := parts.([]any)
		var values []any
		for _, item := range items {
			part, _ := item.(map[string]any)
			key, _ := part["key"].(string)
			if part["role"] == f.PartRole && strings.HasPrefix(key, f.PartKeyPrefix) {
				c, _ := part["content"].(map[string]any)
				if text, yes := c["text"].(string); yes && text != "" {
					values = append(values, text)
				}
			}
		}
		if f.Type == "string_array" {
			return values, len(values) > 0
		}
		texts := make([]string, len(values))
		for i, value := range values {
			texts[i] = value.(string)
		}
		return strings.Join(texts, "\n\n"), len(values) > 0
	}
	node, ok = pointerValue(view, f.SourcePointer)
	if !ok {
		return "", false
	}
	if f.ValuePointer != "" {
		items, yes := node.([]any)
		if !yes {
			return "", false
		}
		var values []any
		for _, item := range items {
			selected, found := pointerValue(item, f.ValuePointer)
			if !found {
				continue
			}
			switch value := selected.(type) {
			case string:
				values = append(values, value)
			case []any:
				values = append(values, value...)
			}
		}
		node = values
	}
	return node, true
}

func fieldText(view any, f corpus.Field) (string, bool) {
	node, ok := fieldValue(view, f)
	if !ok {
		return "", false
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

// ItemFields supplies default canonical text mappings unless a configured field
// replaces them. Text comes from canonical Parts, independently of chunking.
func ItemFields(fields []corpus.Field) []corpus.Field {
	two := 2
	out := []corpus.Field{{Name: "title", PartRole: "title", Type: "string", Roles: []string{"search"}, Boost: &two}}
	for _, role := range []string{"body", "caption", "transcript"} {
		out = append(out, corpus.Field{Name: role, PartRole: role, Type: "string", Roles: []string{"search"}})
	}
	for _, f := range fields {
		if !f.Searchable() {
			continue
		}
		replaced := false
		for i := range out {
			if out[i].Name == f.Name {
				out[i] = f
				replaced = true
			}
		}
		if !replaced {
			out = append(out, f)
		}
	}
	return out
}

// ItemText is each logical field's text, indexed once for a Version. Its values never come from embedding/model text.
func ItemText(v Version, fields []corpus.Field) map[string]string {
	out := map[string]string{}
	view := sourceView(v)
	for _, f := range ItemFields(fields) {
		if text, ok := fieldText(view, f); ok {
			out[f.Name] = text
		}
	}

	return out
}
