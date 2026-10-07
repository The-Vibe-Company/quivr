package weaviate_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// Real BM25 owns headline weighting, item frequency and query normalization.
func TestItemKeywordRanking(t *testing.T) {
	f := newAttachFixture(t)
	g := f.gen
	g.ItemKeywordsProjected = true
	g.MetadataProjected = true
	six, two := 6, 2
	g.Fields = []corpus.Field{
		{Name: "title", PartRole: "title", Type: "string", Roles: []string{"search"}, Boost: &six},
		{Name: "body", PartRole: "body", Type: "string", Roles: []string{"search"}, Boost: &two, Analyzer: "french_light"},
	}
	publish := func(id, title, body string, passages int) {
		seg := f.segmentation(id, body)
		seg.Segments = nil
		for i := 0; i < passages; i++ {
			seg.Segments = append(seg.Segments, content.Segment{ID: id + string(rune('a'+i)), PartKey: "body", Text: body})
		}
		v := content.Version{ID: seg.VersionID, RecordID: "record-" + id, Manifest: content.Manifest{Parts: []content.Part{
			{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: title}},
			{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: body}},
		}}}
		if err := f.store.Publish(f.ctx, g, f.org, f.corpusID, "feed", v, seg); err != nil {
			t.Fatal(err)
		}
	}
	publish("headline", "election", "Une nouvelle du port", 3)
	publish("body", "Une nouvelle du port", "election", 1)
	publish("accent", "Une nouvelle du port", "Les élections françaises et le thé", 3)
	q := retrieval.Request{Query: "election", Mode: "lexical", GroupBy: "record", K: 10}
	hits, err := f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: g}}, corpus.Scope{Organization: f.org}, q)
	if err != nil || len(hits) != 3 || hits[0].SegmentID != "headlinea" {
		t.Fatalf("boosted item hits: %+v %v", hits, err)
	}
	seen := map[string]bool{}
	for _, h := range hits {
		if seen[h.VersionID] {
			t.Fatalf("duplicate item: %+v", hits)
		}
		seen[h.VersionID] = true
	}
	q.Query = "the"
	hits, err = f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: g}}, corpus.Scope{Organization: f.org}, q)
	if err != nil || len(hits) != 1 || hits[0].SegmentID != "accenta" {
		t.Fatalf("French tea must survive English stopwords: %+v %v", hits, err)
	}
	q.Query = "election"
	q.GroupBy = ""
	hits, err = f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: g}}, corpus.Scope{Organization: f.org}, q)
	if err != nil || len(hits) != 7 {
		t.Fatalf("ungrouped passages: %+v %v", hits, err)
	}
	// A second Corpus has its own keyword recipe. Partition-local ranking
	// must not erase the first Corpus's headline boost during global fusion.
	other := g
	other.ID = "other-generation"
	one := 1
	other.Fields = []corpus.Field{{Name: "body", PartRole: "body", Type: "string", Roles: []string{"search"}, Boost: &one}}
	seg := f.segmentation("cross-corpus", "election")
	v := content.Version{ID: seg.VersionID, RecordID: "other-record", Manifest: content.Manifest{Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "election"}}}}}
	if err := f.store.Publish(f.ctx, other, f.org, "other-corpus", "feed", v, seg); err != nil {
		t.Fatal(err)
	}
	q.GroupBy = "record"
	hits, err = f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: g}, {CorpusID: "other-corpus", Generation: other}}, corpus.Scope{Organization: f.org}, q)
	if err != nil || len(hits) != 4 || hits[0].SegmentID != "headlinea" {
		t.Fatalf("cross-corpus boosted hits: %+v %v", hits, err)
	}
	vector := unitVector(11)
	var embeddings []content.EmbeddingData
	for _, id := range []string{"headlinea", "headlineb", "headlinec", "bodya", "accenta", "accentb", "accentc"} {
		embeddings = append(embeddings, f.embedding(g, id, vector))
	}
	if err := f.store.PublishEmbeddings(f.ctx, g, f.org, embeddings); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PublishEmbeddings(f.ctx, other, f.org, []content.EmbeddingData{f.embedding(other, "cross-corpus", vector)}); err != nil {
		t.Fatal(err)
	}
	q.Mode, q.Vector = "hybrid", vector
	hits, err = f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: g}, {CorpusID: "other-corpus", Generation: other}}, corpus.Scope{Organization: f.org}, q)
	if err != nil || len(hits) != 4 || hits[0].SegmentID != "headlinea" {
		t.Fatalf("hybrid headline ranking after enrichment: %+v %v", hits, err)
	}
	// An upgrade can route both recipes. Their BM25 populations and hybrid
	// normalizations differ, so the page uses reciprocal ranks across recipes.
	legacy := f.gen
	legacy.ID = "legacy-generation"
	legacySeg := f.segmentation("legacy-hit", "election")
	legacyVersion := content.Version{ID: legacySeg.VersionID, Manifest: content.Manifest{Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "election"}}}}}
	if err := f.store.Publish(f.ctx, legacy, f.org, "legacy-corpus", "feed", legacyVersion, legacySeg); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PublishEmbeddings(f.ctx, legacy, f.org, []content.EmbeddingData{f.embedding(legacy, "legacy-hit", vector)}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"lexical", "hybrid"} {
		q.Mode, q.K = mode, 2
		hits, err = f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: g}, {CorpusID: "legacy-corpus", Generation: legacy}}, corpus.Scope{Organization: f.org}, q)
		if err != nil || len(hits) != 2 {
			t.Fatalf("mixed %s hits: %+v %v", mode, hits, err)
		}
		seen := map[string]bool{}
		for _, hit := range hits {
			seen[hit.GenerationID] = true
			if hit.Score != 1.0/61 {
				t.Fatalf("mixed %s score scale: %+v", mode, hits)
			}
		}
		if !seen[g.ID] || !seen[legacy.ID] {
			t.Fatalf("mixed %s omitted a recipe's first-ranked item: %+v", mode, hits)
		}
	}
}

// Owns dense max aggregation and choosing the highest-scoring canonical passage.
func TestItemHybridKeepsBestPassage(t *testing.T) {
	f := newAttachFixture(t)
	g := f.gen
	g.ItemKeywordsProjected = true
	seg := f.segmentation("item", "harbour")
	seg.Segments = []content.Segment{{ID: "low", PartKey: "body", Text: "harbour"}, {ID: "other", PartKey: "body", Text: "harbour"}}
	for i := 0; i < 30; i++ {
		seg.Segments = append(seg.Segments, content.Segment{ID: fmt.Sprintf("filler-%02d", i), PartKey: "body", Text: "harbour"})
	}
	seg.Segments = append(seg.Segments, content.Segment{ID: "zz-best", PartKey: "body", Text: "harbour ferries"})
	v := content.Version{ID: seg.VersionID, RecordID: "record", Manifest: content.Manifest{Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: strings.Repeat("harbour\n", 32) + "harbour ferries"}}}}}
	if err := f.store.Publish(f.ctx, g, f.org, f.corpusID, "feed", v, seg); err != nil {
		t.Fatal(err)
	}
	query := unitVector(1)
	if err := f.store.PublishEmbeddings(f.ctx, g, f.org, []content.EmbeddingData{f.embedding(g, "low", unitVector(2)), f.embedding(g, "zz-best", query), f.embedding(g, "other", unitVector(3))}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"semantic", "hybrid"} {
		hits, err := f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: g}}, corpus.Scope{Organization: f.org}, retrieval.Request{Query: "harbour", Mode: mode, Vector: query, GroupBy: "record", K: 10})
		if err != nil || len(hits) != 1 || hits[0].SegmentID != "zz-best" {
			t.Fatalf("%s best passage %+v %v", mode, hits, err)
		}
	}
	if err := f.store.Publish(f.ctx, g, f.org, f.corpusID, "feed", v, seg); err != nil {
		t.Fatalf("replay after enrichment: %v", err)
	}
	hits, err := f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: g}}, corpus.Scope{Organization: f.org}, retrieval.Request{Query: "ferries", Mode: "lexical", GroupBy: "record", K: 1})
	if err != nil || len(hits) != 1 || hits[0].SegmentID != "zz-best" {
		t.Fatalf("lexical highlight %+v %v", hits, err)
	}
}

func TestItemIndexedMetadataAndIdentityFilters(t *testing.T) {
	f := newAttachFixture(t)
	g := f.gen
	g.ItemKeywordsProjected = true
	g.MetadataProjected = true
	vector := unitVector(7)
	g.SpacesProjected = true
	g.Spaces = []content.GenerationSpace{{ID: g.SpaceID, Metric: "cosine"}}
	g.Fields = []corpus.Field{
		{Name: "heading", PartRole: "title", Type: "string", Roles: []string{"search", "filter"}},
		{Name: "categories", SourcePointer: "/provenance/categories", ValuePointer: "/label", Type: "string_array", Roles: []string{"filter"}},
	}
	for i := 0; i < 8; i++ {
		id := "outside" + string(rune('a'+i))
		date := "2026-01-01T00:00:00Z"
		namespace := "other"
		if i == 7 {
			id = "target"
			date = "2026-10-07T12:00:00Z"
			namespace = "feed"
		}
		seg := f.segmentation(id, "harbour")
		seg.Segments[0].Derivation.LexicalText = "harbour"
		v := content.Version{ID: seg.VersionID, RecordID: "record-" + id, Provenance: map[string]any{"categories": []any{map[string]any{"label": "science"}}}, Manifest: content.Manifest{Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "harbour"}}, {Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: "Harbour update"}}}}, Extensions: content.Extensions{"quivr.metadata": {SchemaVersion: "1", Data: map[string]any{"published_at": date, "language": "fr", "subjects": []any{"topic:science"}, "place": []any{"place:harbour"}}}}}
		if err := f.store.Publish(f.ctx, g, f.org, f.corpusID, namespace, v, seg); err != nil {
			t.Fatal(err)
		}
		if err := f.store.PublishEmbeddings(f.ctx, g, f.org, []content.EmbeddingData{f.embedding(g, id, vector)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"lexical", "semantic", "hybrid"} {
		q := retrieval.Request{Query: "harbour", Mode: mode, Vector: vector, GroupBy: "record", K: 2, RecordIDs: []string{"record-target"}, VersionIDs: []string{"version-target"}, SourceNamespaces: []string{"feed"}, Metadata: []corpus.MetadataFilter{{Field: "metadata.published_at", Gte: "2026-10-01T00:00:00Z", Lte: "2026-10-31T23:59:59Z"}, {Field: "metadata.language", AnyOf: []any{"fr"}}, {Field: "metadata.subjects", AnyOf: []any{"topic:science"}}, {Field: "metadata.place", AnyOf: []any{"place:harbour"}}}}
		q.Metadata = append(q.Metadata, corpus.MetadataFilter{Field: "heading", AnyOf: []any{"Harbour update"}}, corpus.MetadataFilter{Field: "categories", AnyOf: []any{"science"}})
		hits, err := f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: g}}, corpus.Scope{Organization: f.org}, q)
		if err != nil || len(hits) != 1 || hits[0].SegmentID != "target" {
			t.Fatalf("%s filters %+v %v", mode, hits, err)
		}
		q.RecordIDs = []string{"record-outsidea"}
		hits, err = f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: g}}, corpus.Scope{Organization: f.org}, q)
		if err != nil || len(hits) != 0 {
			t.Fatalf("%s mismatched identity %+v %v", mode, hits, err)
		}
		if mode == "lexical" {
			q.Field, q.RecordIDs = retrieval.FieldLexical, []string{"record-target"}
			hits, err = f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: g}}, corpus.Scope{Organization: f.org}, q)
			if err != nil || len(hits) == 0 {
				t.Fatalf("plugin lexical identity filter: %+v %v", hits, err)
			}
			for _, h := range hits {
				if h.SegmentID != "target" {
					t.Fatalf("plugin lexical identity leaked: %+v", hits)
				}
			}
		}
	}
	// Range indexing is a schema contract, not a timing assertion.
	res, err := http.Get(f.url + "/v1/schema/" + g.Collection)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var schema struct {
		Properties []struct {
			DataType   []string `json:"dataType"`
			Range      bool     `json:"indexRangeFilters"`
			Searchable bool     `json:"indexSearchable"`
			Filterable bool     `json:"indexFilterable"`
		} `json:"properties"`
	}
	if err := json.NewDecoder(res.Body).Decode(&schema); err != nil {
		t.Fatal(err)
	}
	dates := 0
	for _, p := range schema.Properties {
		if len(p.DataType) > 0 && p.DataType[0] == "date" {
			dates++
			if !p.Range || !p.Filterable || p.Searchable {
				t.Fatalf("date index flags: %+v", p)
			}
		}
	}
	if dates == 0 {
		t.Fatal("publication date index missing")
	}
}
