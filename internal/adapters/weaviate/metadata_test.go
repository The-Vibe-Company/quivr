package weaviate_test

import (
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"strings"
	"testing"
)

// Owns engine filtering before ranking: more nonmatching anchors than the
// candidate window cannot hide a matching low-ranked document. Enriched
// anchors must retain all typed fields, and quoted/multiword values stay exact.
func TestMetadataFilterBeforeRankingInEveryMode(t *testing.T) {
	f := newAttachFixture(t)
	g := f.gen
	g.MetadataProjected = true
	g.Fields = []corpus.Field{{Name: "urgency", SourcePointer: "/provenance/urgency", Type: "number", Roles: []string{"filter"}}, {Name: "labels", SourcePointer: "/provenance/labels", Type: "string_array", Roles: []string{"filter"}}, {Name: "urgent", SourcePointer: "/provenance/urgent", Type: "boolean", Roles: []string{"filter"}}}
	empty, err := f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: g}}, corpus.Scope{Organization: f.org, Corpora: []string{"*"}}, retrieval.Request{Query: "harbour", Mode: "lexical", Metadata: []corpus.MetadataFilter{{Field: "metadata.language", AnyOf: []any{"en"}}, {Field: "labels", AnyOf: []any{"label-59"}}}})
	if err != nil || len(empty) != 0 {
		t.Fatalf("filtered empty generation = %v %v", empty, err)
	}
	publish := func(id, language, text string) {
		seg := f.segmentation(id, text)
		labels := make([]any, 60)
		for i := range labels {
			labels[i] = fmt.Sprintf("label-%d", i)
		}
		labels[0] = ""
		labels[1] = strings.Repeat("x", 201)
		v := content.Version{ID: seg.VersionID, Provenance: map[string]any{"urgency": 2.0, "urgent": true, "labels": labels}, Extensions: content.Extensions{"quivr.metadata": {SchemaVersion: "1", Data: map[string]any{"language": language, "tags": []any{"sea \"weather\"", "science"}, "published_at": "2026-10-01T12:30:00Z"}}}}
		if err := f.store.Publish(f.ctx, g, f.org, f.corpusID, "source", v, seg); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i <= retrieval.CandidateLimit; i++ {
		publish(fmt.Sprintf("loud-%03d", i), "fr", "harbour harbour harbour")
	}
	publish("quiet", "en", "a harbour report")
	query := unitVector(1)
	if err := f.store.PublishEmbeddings(f.ctx, g, f.org, []content.EmbeddingData{f.embedding(g, "loud-000", query), f.embedding(g, "quiet", unitVector(2))}); err != nil {
		t.Fatal(err)
	}
	routes := []retrieval.Route{{CorpusID: f.corpusID, Generation: g}}
	scope := corpus.Scope{Organization: f.org, Corpora: []string{"*"}}
	unfiltered, err := f.store.Search(f.ctx, routes, scope, retrieval.Request{Query: "harbour", Mode: "lexical"})
	if err != nil {
		t.Fatal(err)
	}
	if len(unfiltered) != retrieval.CandidateLimit {
		t.Fatalf("unfiltered candidate window = %d", len(unfiltered))
	}
	for _, hit := range unfiltered {
		if hit.SegmentID == "quiet" {
			t.Fatal("quiet already in unfiltered window")
		}
	}
	filters := []corpus.MetadataFilter{{Field: "metadata.language", AnyOf: []any{"en"}}, {Field: "metadata.tags", AnyOf: []any{"absent", "sea \"weather\""}}, {Field: "metadata.published_at", Gte: "2026-10-01T12:30:00Z", Lte: "2026-10-01T12:30:00Z"}, {Field: "urgency", AnyOf: []any{2.0}}, {Field: "urgent", AnyOf: []any{true}}, {Field: "labels", AnyOf: []any{"label-59"}}}
	for _, mode := range []string{"lexical", "semantic", "hybrid"} {
		candidates, err := f.store.Search(f.ctx, routes, scope, retrieval.Request{Query: "harbour", Vector: query, Mode: mode, Metadata: filters})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		seen := map[string]bool{}
		for _, c := range candidates {
			seen[c.SegmentID] = true
		}
		if len(seen) != 1 || !seen["quiet"] {
			t.Fatalf("%s matched %v, want quiet only", mode, seen)
		}
	}
	// Equality is byte-exact rather than a token or whitespace comparison.
	filters[1].AnyOf = []any{" sea \"weather\""}
	got, err := f.store.Search(f.ctx, routes, scope, retrieval.Request{Query: "harbour", Mode: "lexical", Metadata: filters})
	if err != nil || len(got) != 0 {
		t.Fatalf("exact tag negative control = %v, %v", got, err)
	}
}
