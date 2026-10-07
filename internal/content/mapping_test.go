package content_test

import (
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
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

// Item fields use canonical Parts, not segmentation windows or model inputs.
func TestItemTextSelectors(t *testing.T) {
	v, _ := mappedVersion()
	v.Manifest.Parts = append(v.Manifest.Parts,
		content.Part{Key: "picture", Role: "caption", Content: content.Text{Kind: "text", Text: "Harbour photograph"}},
		content.Part{Key: "interview", Role: "transcript", Content: content.Text{Kind: "text", Text: "Recorded interview"}},
	)
	v.Provenance["subjects"] = []any{map[string]any{"names": []any{"science", "weather"}}, map[string]any{"names": []any{"culture"}}}
	fields := []corpus.Field{
		{Name: "headline", PartRole: "title", Type: "string", Roles: []string{"search"}},
		{Name: "body", PartRole: "body", Type: "string", Roles: []string{"search"}},
		{Name: "labels", SourcePointer: "/provenance/subjects", ValuePointer: "/names", Type: "string_array", Roles: []string{"search"}},
	}
	texts := content.ItemText(v, fields)
	if texts["headline"] != "Plain title" || texts["body"] != "First. Second." || texts["labels"] != "science\nweather\nculture" || texts["caption"] != "Harbour photograph" || texts["transcript"] != "Recorded interview" {
		t.Fatalf("item fields: %+v", texts)
	}
	fields[1].PartKeyPrefix = "missing"
	if content.ItemText(v, fields)["body"] != "" {
		t.Fatal("Part prefix ignored")
	}
}

func TestFrenchLightKeywordCopy(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Les élections françaises", "election francais"},
		{"chevaux cheval", "cheval cheval"},
		{"actrices acteurs", "acteu acteu"},
		{"LE port et la mer", "port mer"},
	} {
		if got := content.AnalyzeKeywords(tc.in, "french_light"); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := content.AnalyzeKeywords("Élections françaises", ""); got != "Élections françaises" {
		t.Fatalf("original text changed: %q", got)
	}
}
