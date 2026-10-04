package weaviate_test

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// A generation's pinned retrieval fields shape its projected text: the mapped
// title reaches every segment, other mapped text only the first segment, and
// an unmapped generation of the same Version stays canonical.
func TestPinnedRetrievalFieldsShapeProjectedText(t *testing.T) {
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("make verify real adapters")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		WeaviateURL string `json:"weaviate_url"`
	}
	if err = json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	store := weaviate.New(cfg.WeaviateURL)
	collection := "QuivrMapping" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err = store.Bootstrap(ctx, collection); err != nil {
		t.Fatal(err)
	}
	fields := []corpus.Field{
		{Name: "title", SourcePointer: "/extensions/example.editorial/data/headline", Type: "string", Roles: []string{"search"}},
		{Name: "summary", SourcePointer: "/extensions/example.editorial/data/summary", Type: "string", Roles: []string{"search"}},
	}
	plain := content.Generation{ID: "generation-plain", Collection: collection, ProfileVersion: retrieval.ProfileVersion}
	mapped := content.Generation{ID: "generation-mapped", Collection: collection, ProfileVersion: retrieval.ProfileVersion, Fields: fields}
	v := content.Version{ID: "version-1", Extensions: content.Extensions{"example.editorial": {SchemaVersion: "1", Data: map[string]any{"headline": "zircone", "summary": "basalte"}}}}
	seg := content.Segmentation{ID: "seg-1", VersionID: v.ID, Segments: []content.Segment{
		{ID: "first", PartKey: "body", Text: "harbour traffic report", Derivation: content.SegmentDerivation{Ordinal: 0}},
		{ID: "second", PartKey: "body", Text: "annual tonnage figures", Derivation: content.SegmentDerivation{Ordinal: 1}},
	}}
	org := "adapter-mapping"
	for _, g := range []content.Generation{plain, mapped} {
		// Publishing twice is idempotent and verified after write.
		for i := 0; i < 2; i++ {
			if err = store.Publish(ctx, g, org, "corpus-"+g.ID, "example-feed", v, seg); err != nil {
				t.Fatal(err)
			}
		}
	}
	scope := corpus.Scope{Organization: org, Corpora: []string{"*"}}
	routes := []retrieval.Route{{CorpusID: "corpus-" + plain.ID, Generation: plain}, {CorpusID: "corpus-" + mapped.ID, Generation: mapped}}
	found := func(query string) []string {
		t.Helper()
		candidates, err := store.Search(ctx, routes, scope, retrieval.Request{Query: query, Mode: "lexical", Profile: "default", Limit: 10, CorpusIDs: []string{"corpus-" + plain.ID, "corpus-" + mapped.ID}})
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, c := range candidates {
			out = append(out, c.SegmentID+"@"+c.GenerationID)
		}
		sort.Strings(out)
		return out
	}
	if got, want := found("zircone"), []string{"first@generation-mapped", "second@generation-mapped"}; !equalStrings(got, want) {
		t.Fatalf("mapped title hits %v, want %v", got, want)
	}
	if got, want := found("basalte"), []string{"first@generation-mapped"}; !equalStrings(got, want) {
		t.Fatalf("mapped summary hits %v, want %v", got, want)
	}
	if got, want := found("tonnage"), []string{"second@generation-mapped", "second@generation-plain"}; !equalStrings(got, want) {
		t.Fatalf("canonical hits %v, want %v", got, want)
	}
}
