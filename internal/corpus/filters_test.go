package corpus_test

import (
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"testing"
)

// Owns predicate validation/type compatibility. No existing source-only filter
// test sees typed values, bounds or missing-field exclusion.
func TestMetadataPredicatesResolveAgainstFilterableFields(t *testing.T) {
	fields := []corpus.Field{{Name: "urgency", Type: "number", Roles: []string{"filter"}}, {Name: "headline", Type: "string", Roles: []string{"search"}}}
	filters := []corpus.MetadataFilter{{Field: "metadata.language", AnyOf: []any{"en", "fr"}}, {Field: "urgency", AnyOf: []any{2.0}}}
	got, missing, err := corpus.ResolveFilters(filters, fields)
	if err != nil || len(missing) != 0 || len(got) != 2 || got[0].Type != "string" || got[1].Type != "number" {
		t.Fatalf("resolve = %+v missing %v err %v", got, missing, err)
	}
	_, missing, err = corpus.ResolveFilters([]corpus.MetadataFilter{{Field: "headline", AnyOf: []any{"title"}}, {Field: "absent", AnyOf: []any{true}}}, fields)
	if err != nil || len(missing) != 2 || missing[0] != "headline" || missing[1] != "absent" {
		t.Fatalf("exclusion = %v %v", missing, err)
	}
	for _, f := range []corpus.MetadataFilter{
		{Field: "urgency", AnyOf: []any{"2"}}, {Field: "metadata.language"}, {Field: "metadata.language", AnyOf: []any{map[string]any{"raw": "x"}}},
		{Field: "metadata.published_at", Gte: "2026-02-30T00:00:00Z"}, {Field: "metadata.language", Gte: "2026-01-01T00:00:00Z"},
		{Field: "metadata.published_at", Gte: "2026-02-01T00:00:00Z", Lte: "2026-01-01T00:00:00Z"},
	} {
		if _, _, err := corpus.ResolveFilters([]corpus.MetadataFilter{f}, fields); !errors.Is(err, corpus.ErrInvalidFilter) {
			t.Errorf("%+v error %v", f, err)
		}
	}
}
