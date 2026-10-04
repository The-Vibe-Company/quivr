package content_test

import (
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

func mappedVersion() (content.Version, content.Segmentation) {
	v := content.Version{ID: "v1", Manifest: content.Manifest{Kind: "manifest", Parts: []content.Part{
		{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: "Plain title"}},
		{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "First. Second."}},
	}}, Extensions: content.Extensions{"example.editorial": {SchemaVersion: "1", Data: map[string]any{
		"headline": "Structured headline",
		"summary":  "Structured summary",
		"subjects": []any{"alpha", "beta"},
		"score":    3.5,
	}}}, Provenance: map[string]any{"a/b": "escaped", "list": []any{"one", 2.0}}}
	seg := content.Segmentation{ID: "s1", VersionID: "v1", Segments: []content.Segment{
		{ID: "p0", PartKey: "body", Text: "First.", Title: "Plain title", Derivation: content.SegmentDerivation{Ordinal: 0}},
		{ID: "p1", PartKey: "body", Text: "Second.", Title: "Plain title", Derivation: content.SegmentDerivation{Ordinal: 1}},
	}}
	return v, seg
}

func search(name, pointer, typ string) corpus.Field {
	return corpus.Field{Name: name, SourcePointer: pointer, Type: typ, Roles: []string{"search"}}
}

func TestProjectionTextWithoutMappingIsCanonical(t *testing.T) {
	v, seg := mappedVersion()
	texts, skipped := content.ProjectionText(v, seg, nil)
	if skipped != 0 || len(texts) != 2 || texts[0] != (content.SegmentText{Title: "Plain title", Body: "First."}) || texts[1] != (content.SegmentText{Title: "Plain title", Body: "Second."}) {
		t.Fatalf("unmapped projection changed: %+v %d", texts, skipped)
	}
}

func TestProjectionTextAppliesMappingOncePerVersion(t *testing.T) {
	v, seg := mappedVersion()
	fields := []corpus.Field{
		search("title", "/extensions/example.editorial/data/headline", "string"),
		search("summary", "/extensions/example.editorial/data/summary", "string"),
		search("subjects", "/extensions/example.editorial/data/subjects", "string_array"),
		{Name: "score", SourcePointer: "/extensions/example.editorial/data/score", Type: "number", Roles: []string{"filter"}},
		search("escaped", "/provenance/a~1b", "string"),
	}
	texts, skipped := content.ProjectionText(v, seg, fields)
	if skipped != 0 {
		t.Fatalf("skipped %d", skipped)
	}
	// Mapped title replaces the projected title on every segment.
	for _, s := range texts {
		if s.Title != "Structured headline" {
			t.Fatalf("title not mapped: %+v", texts)
		}
	}
	// Other search text is placed once, on the first (title-bearing) segment,
	// after the canonical segment text; later segments stay canonical.
	if texts[0].Body != "First.\n\nStructured summary\n\nalpha\nbeta\n\nescaped" || texts[1].Body != "Second." {
		t.Fatalf("body placement: %+v", texts)
	}
}

func TestProjectionTextSkipsMissingAndMistypedValues(t *testing.T) {
	v, seg := mappedVersion()
	fields := []corpus.Field{
		search("title", "/extensions/example.editorial/data/score", "string"),
		search("missing", "/provenance/absent", "string"),
		search("mixed", "/provenance/list", "string_array"),
		search("outside", "/manifest/parts/9/content/text", "string"),
		search("part", "/manifest/parts/0/content/text", "string"),
	}
	texts, skipped := content.ProjectionText(v, seg, fields)
	if skipped != 4 {
		t.Fatalf("skipped %d want 4", skipped)
	}
	if texts[0].Title != "Plain title" || texts[0].Body != "First.\n\nPlain title" {
		t.Fatalf("fallback projection: %+v", texts)
	}
}
