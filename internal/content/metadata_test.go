package content_test

import (
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"reflect"
	"testing"
)

// Owns typed JSON pointer extraction; a filter-only mapping or a normalized
// date can regress without changing any projected text.
func TestProjectionMetadataUsesTypedPointersAndOmitsMistypedValues(t *testing.T) {
	v := content.Version{Extensions: content.Extensions{"quivr.metadata": {SchemaVersion: "1", Data: map[string]any{"language": "en", "tags": []any{"weather", "science"}, "published_at": "2026-10-01T14:30:00.123456+02:00"}}}, Provenance: map[string]any{"urgency": 2.0, "flag": true, "bad": "3"}}
	fields := []corpus.Field{{Name: "urgency", Type: "number", SourcePointer: "/provenance/urgency", Roles: []string{"filter"}}, {Name: "flag", Type: "boolean", SourcePointer: "/provenance/flag", Roles: []string{"filter"}}, {Name: "bad", Type: "number", SourcePointer: "/provenance/bad", Roles: []string{"filter"}}}
	got := content.ProjectionMetadata(v, fields)
	want := map[string]any{"metadata.language": "en", "metadata.tags": []string{"weather", "science"}, "metadata.published_at": "2026-10-01T12:30:00.123Z", "urgency": 2.0, "flag": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projected metadata = %#v; want %#v", got, want)
	}
}
