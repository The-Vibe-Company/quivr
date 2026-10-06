package corpus

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/internal/publicerr"
)

const MaxMetadataFilters = 16
const MaxFilterValues = 50
const MaxFilterString = 200

var ErrInvalidFilter = publicerr.InvalidQuery

// MetadataFilter ANDs an optional any-of equality set with inclusive datetime
// bounds. An array field matches when any of its elements equals a value.
type MetadataFilter struct {
	Field string `json:"field"`
	AnyOf []any  `json:"any_of,omitempty"`
	Gte   string `json:"gte,omitempty"`
	Lte   string `json:"lte,omitempty"`
}

type TypedFilter struct {
	MetadataFilter
	Type string
}

type CorpusExclusion struct {
	CorpusID string   `json:"corpus_id"`
	Fields   []string `json:"fields"`
}

// CommonFilterFields use a distinct logical prefix so existing Corpus mappings
// named language or tags retain their meaning.
func CommonFilterFields() []Field {
	out := []Field{}
	for _, name := range []string{"language", "published_at", "source_type", "source", "author", "subjects", "tags", "country", "place"} {
		typ := "string"
		switch name {
		case "published_at":
			typ = "datetime"
		case "author", "subjects", "tags", "country", "place":
			typ = "string_array"
		}
		out = append(out, Field{Name: "metadata." + name, SourcePointer: "/extensions/quivr.metadata/data/" + name, Type: typ, Roles: []string{"filter"}})
	}
	return out
}
func FilterFields(fields []Field) []Field {
	out := CommonFilterFields()
	for _, f := range fields {
		if f.Filterable() {
			out = append(out, f)
		}
	}
	return out
}
func (f Field) Filterable() bool {
	for _, r := range f.Roles {
		if r == "filter" {
			return true
		}
	}
	return false
}

var filterName = regexp.MustCompile(`^(metadata\.)?[a-z][a-z0-9_]{0,63}$`)
var filterDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]([01]\d|2[0-3]):[0-5]\d)$`)

// FilterDate normalizes dates to UTC milliseconds, the index's precision.
func FilterDate(s string) (string, bool) {
	if !filterDate.MatchString(s) {
		return "", false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return "", false
	}
	return t.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z"), true
}
func validScalar(v any) bool {
	switch v := v.(type) {
	case string:
		return utf8.ValidString(v) && !strings.ContainsRune(v, 0) && utf8.RuneCountInString(v) <= MaxFilterString && v != ""
	case bool:
		return true
	case float64:
		return !math.IsNaN(v) && !math.IsInf(v, 0)
	case json.Number:
		_, err := v.Float64()
		return err == nil
	default:
		return false
	}
}
func ValidateFilters(filters []MetadataFilter) error {
	if len(filters) > MaxMetadataFilters {
		return ErrInvalidFilter
	}
	seen := map[string]bool{}
	for _, f := range filters {
		if !filterName.MatchString(f.Field) || seen[f.Field] || (f.AnyOf != nil && len(f.AnyOf) == 0) || len(f.AnyOf) > MaxFilterValues || (len(f.AnyOf) == 0 && f.Gte == "" && f.Lte == "") {
			return ErrInvalidFilter
		}
		seen[f.Field] = true
		for _, v := range f.AnyOf {
			if !validScalar(v) {
				return ErrInvalidFilter
			}
		}
		lo, hi := "", ""
		if f.Gte != "" {
			var ok bool
			lo, ok = FilterDate(f.Gte)
			if !ok {
				return ErrInvalidFilter
			}
		}
		if f.Lte != "" {
			var ok bool
			hi, ok = FilterDate(f.Lte)
			if !ok {
				return ErrInvalidFilter
			}
		}
		if lo != "" && hi != "" && lo > hi {
			return ErrInvalidFilter
		}
	}
	return nil
}

// ResolveFilters excludes fields not declared with the filter role; values
// incompatible with a declared type are invalid rather than coerced.
func ResolveFilters(filters []MetadataFilter, fields []Field) ([]TypedFilter, []string, error) {
	if err := ValidateFilters(filters); err != nil {
		return nil, nil, err
	}
	declared := map[string]Field{}
	for _, f := range FilterFields(fields) {
		declared[f.Name] = f
	}
	out := []TypedFilter{}
	missing := []string{}
	for _, f := range filters {
		field, ok := declared[f.Field]
		if !ok {
			missing = append(missing, f.Field)
			continue
		}
		p := TypedFilter{MetadataFilter: f, Type: field.Type}
		p.AnyOf = append([]any(nil), f.AnyOf...)
		for i, v := range f.AnyOf {
			value, ok := FilterValue(v, field.Type, false)
			if !ok {
				return nil, nil, ErrInvalidFilter
			}
			p.AnyOf[i] = value
		}
		if f.Gte != "" || f.Lte != "" {
			if field.Type != "datetime" {
				return nil, nil, ErrInvalidFilter
			}
			if f.Gte != "" {
				p.Gte, _ = FilterDate(f.Gte)
			}
			if f.Lte != "" {
				p.Lte, _ = FilterDate(f.Lte)
			}
		}
		out = append(out, p)
	}
	return out, missing, nil
}

// FilterValue validates projection values as well as request values. Missing,
// mistyped or oversized source values are omitted from the projection.
func FilterValue(v any, typ string, array bool) (any, bool) {
	switch typ {
	case "string":
		s, ok := v.(string)
		return s, ok && validScalar(s)
	case "datetime":
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		return FilterDate(s)
	case "number":
		switch n := v.(type) {
		case float64:
			return n, validScalar(n)
		case json.Number:
			x, e := n.Float64()
			return x, e == nil && validScalar(x)
		}
	case "boolean":
		b, ok := v.(bool)
		return b, ok
	case "string_array":
		if !array {
			s, ok := v.(string)
			return s, ok && validScalar(s)
		}
		items, ok := v.([]any)
		// Source extension JSON is bounded at ingestion;
		// the any_of request limit must not discard larger declared arrays.
		if !ok || len(items) == 0 {
			return nil, false
		}
		values := make([]string, 0, len(items))
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			// Values outside the predicate domain cannot match, but they
			// must not hide other queryable members of a declared array.
			if validScalar(s) {
				values = append(values, s)
			}
		}
		return values, len(values) > 0
	}
	return nil, false
}
