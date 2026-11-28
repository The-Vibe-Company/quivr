package weaviate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
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
	// An English Corpus with the language-neutral analyzer folds accents but
	// never stems: French stemming would match "organisation" to "organ" and
	// highlight the earlier passage.
	english := f.gen
	english.ID = "english-generation"
	english.ItemKeywordsProjected = true
	english.Fields = []corpus.Field{{Name: "body", PartRole: "body", Type: "string", Roles: []string{"search"}, Analyzer: "folded"}}
	publishEnglish := func(gen content.Generation, id, title string, passages ...string) {
		seg := f.segmentation(id, "")
		seg.Segments = nil
		for i, text := range passages {
			seg.Segments = append(seg.Segments, content.Segment{ID: id + string(rune('a'+i)), PartKey: "body", Text: text})
		}
		parts := []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: strings.Join(passages, "\n")}}}
		if title != "" {
			parts = append(parts, content.Part{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: title}})
		}
		v := content.Version{ID: seg.VersionID, RecordID: "record-" + id, Manifest: content.Manifest{Parts: parts}}
		if err := f.store.Publish(f.ctx, gen, f.org, gen.ID, "feed", v, seg); err != nil {
			t.Fatal(err)
		}
	}
	publishEnglish(english, "organ", "", "The organisation met", "An organ donor")
	publishEnglish(english, "society", "", "The organisation met")
	publishEnglish(english, "cafe", "", "Café opening")
	// With two analyzers, each copy is queried only with its own analysis:
	// French "nations" stems to the title's "nation", folded keeps the plural.
	mixed := english
	mixed.ID = "mixed-generation"
	mixed.Fields = append(english.Fields, corpus.Field{Name: "title", PartRole: "title", Type: "string", Roles: []string{"search"}, Analyzer: "french_light"})
	publishEnglish(mixed, "nation", "", "Nation building")
	publishEnglish(mixed, "report", "Nation report", "Annual figures")
	for _, tc := range []struct {
		gen         content.Generation
		query, want string
	}{{english, "organ", "organb"}, {english, "cafe", "cafea"}, {mixed, "nations", "reporta"}} {
		hits, err = f.store.Search(f.ctx, []retrieval.Route{{CorpusID: tc.gen.ID, Generation: tc.gen}}, corpus.Scope{Organization: f.org}, retrieval.Request{Query: tc.query, Mode: "lexical", GroupBy: "record", K: 10})
		if err != nil || len(hits) != 1 || hits[0].SegmentID != tc.want {
			t.Fatalf("%s %q: %+v %v, want only %s", tc.gen.ID, tc.query, hits, err, tc.want)
		}
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
	seg.Segments = append(seg.Segments, content.Segment{ID: "zz-coverage", PartKey: "body", Text: "ferries boat water"})
	v := content.Version{ID: seg.VersionID, RecordID: "record", Manifest: content.Manifest{Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: strings.Repeat("harbour\n", 32) + "harbour ferries\nferries boat water"}}}}}
	if err := f.store.Publish(f.ctx, g, f.org, f.corpusID, "feed", v, seg); err != nil {
		t.Fatal(err)
	}
	query := unitVector(1)
	if err := f.store.PublishEmbeddings(f.ctx, g, f.org, []content.EmbeddingData{f.embedding(g, "low", unitVector(2)), f.embedding(g, "zz-best", query), f.embedding(g, "other", unitVector(3))}); err != nil {
		t.Fatal(err)
	}
	// The lexical scan budget counts canonical passages even after vectors
	// are attached: enriched physical copies must not occupy those slots.
	queryJSON, _ := json.Marshal(map[string]string{"query": fmt.Sprintf(`{Get{%s(where:{operator:And,operands:[{path:["organization"],operator:Equal,valueText:%q},{path:["generationId"],operator:Equal,valueText:%q},{path:["itemKind"],operator:Equal,valueText:"segment"}]},limit:100){segmentId}}}`, g.Collection, f.org, g.ID)})
	req, err := http.NewRequestWithContext(f.ctx, http.MethodPost, f.url+"/v1/graphql", strings.NewReader(string(queryJSON)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := f.store.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var scan struct {
		Data struct {
			Get map[string][]struct {
				SegmentID string `json:"segmentId"`
			} `json:"Get"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	err = json.NewDecoder(res.Body).Decode(&scan)
	res.Body.Close()
	if err != nil || len(scan.Errors) != 0 || len(scan.Data.Get[g.Collection]) != len(seg.Segments) {
		t.Fatalf("lexical passage population after enrichment: %+v %v", scan, err)
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
	// A partial match must still choose the best highlight within a bounded
	// dependency budget. Exhausting that budget fails immediately, without
	// sleeping or asserting machine-dependent latency.
	f.store.Client.Transport = &searchRequestBudget{next: f.store.Client.Transport, remaining: 4}
	hits, err = f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: g}}, corpus.Scope{Organization: f.org}, retrieval.Request{Query: "harbour harbour harbour ferries boat water", Mode: "lexical", GroupBy: "record", K: 1})
	if err != nil || len(hits) != 1 || hits[0].SegmentID != "zz-coverage" {
		t.Fatalf("distinct query-term highlight %+v %v", hits, err)
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
	res, err := f.store.Client.Get(f.url + "/v1/schema/" + g.Collection)
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
	// Shared keyword anchors stay ownerless. Higher-scoring anchors from
	// another owner must not consume an evaluation owner's candidate window.
	for _, owner := range []string{"served.owner", "trial.owner"} {
		body, title := "other", "ownerterm"
		if owner == "trial.owner" {
			body, title = "ownerterm", "other"
		}
		seg := f.segmentation(owner, body)
		seg.Recipe = "plugin:" + owner + "@1.0.0"
		v := content.Version{ID: seg.VersionID, RecordID: "record-" + owner, Manifest: content.Manifest{Parts: []content.Part{{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: title}}, {Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: body}}}}}
		if err := f.store.Publish(f.ctx, g, f.org, f.corpusID, "owner-feed", v, seg); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"lexical", "hybrid"} {
		q := retrieval.Request{Query: "ownerterm", Mode: mode, Vector: vector, K: 1, GroupBy: "record", SourceNamespaces: []string{"owner-feed"}, EvaluationPlugin: "trial.owner", Hybrid: &retrieval.HybridOptions{Alpha: 0, Fusion: retrieval.FusionRelativeScore}}
		hits, err := f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: g}}, corpus.Scope{Organization: f.org}, q)
		if err != nil || len(hits) != 1 || hits[0].SegmentID != "trial.owner" {
			t.Fatalf("%s owner-eligible item refill: %+v %v", mode, hits, err)
		}
	}
}

// Enforce the search's dependency budget while leaving real Weaviate ranking,
// filtering and passage selection in the owner test.
type searchRequestBudget struct {
	next      http.RoundTripper
	remaining int
}

func (b *searchRequestBudget) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/v1/graphql" {
		b.remaining--
		if b.remaining < 0 {
			return nil, errors.New("search dependency request budget exhausted")
		}
	}
	return b.next.RoundTrip(r)
}

// Owns bounded recovery when individually valid passages exceed the text
// response budget. Every passage belongs to the same item, so text is needed.
func TestItemPassageResponseBudget(t *testing.T) {
	text := strings.Repeat("harbour ", 1<<17)
	rows := func(count int, withText bool) []itemSearchRow {
		out := make([]itemSearchRow, count)
		for i := range out {
			out[i] = itemSearchRow{Segment: fmt.Sprintf("passage-%03d", i), Version: "version"}
			if withText {
				out[i].Text = text
				if i == 0 {
					out[i].Text = strings.Repeat("other ", 1<<17)
				}
			}
		}
		return out
	}
	textReads := 0
	transport := itemSearchTransport(func(_ context.Context, query string) ([]byte, error) {
		switch {
		case strings.Contains(query, "bm25:"):
			return itemSearchResponse([]itemSearchRow{{Version: "version", Score: "1"}}), nil
		case !strings.Contains(query, "passageText"):
			return itemSearchResponse(rows(17, false)), nil
		default:
			textReads++
			if textReads > 1 {
				return nil, errors.New("recovery needs only the metadata already read")
			}
			return itemSearchResponse(rows(17, true)), nil
		}
	})
	hits, err := runItemSearch(t.Context(), transport, retrieval.Request{Query: "harbour", Mode: "lexical", GroupBy: "record", K: 10})
	if err != nil || len(hits) != 1 || hits[0].SegmentID != "passage-000" || textReads != 1 {
		t.Fatalf("large-passage search: %+v, %v, %d text reads", hits, err, textReads)
	}
}

func TestItemSearchOverlapsQueriesWithinRequestDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
		defer cancel()
		var mu sync.Mutex
		started := map[string]bool{}
		allStarted := make(chan struct{})
		transport := itemSearchTransport(func(ctx context.Context, query string) ([]byte, error) {
			var leg string
			switch {
			case strings.Contains(query, "nearVector:"):
				leg = "dense"
			case strings.Contains(query, "_folded^"):
				leg = "analyzed"
			case strings.Contains(query, "bm25:"):
				leg = "raw"
			default:
				return itemSearchResponse(nil), nil
			}
			mu.Lock()
			started[leg] = true
			if len(started) == 3 {
				close(allStarted)
			}
			mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-allStarted:
			}
			// Advance the virtual clock past the shared client's 4 s timeout.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
			}
			if leg == "dense" {
				distance := 0.1
				return itemSearchResponse([]itemSearchRow{{Segment: "best", Version: "version", Distance: &distance}}), nil
			}
			return itemSearchResponse(nil), nil
		})
		store := weaviate.New("http://index.invalid")
		store.Client.Transport = transport
		g := content.Generation{
			ID: "generation", Collection: "ItemSearch", ItemKeywordsProjected: true,
			Fields: []corpus.Field{
				{Name: "title", PartRole: "title", Type: "string", Roles: []string{"search"}},
				{Name: "body", PartRole: "body", Type: "string", Roles: []string{"search"}, Analyzer: "folded"},
			},
		}
		hits, err := store.Search(ctx, []retrieval.Route{{CorpusID: "corpus", Generation: g}}, corpus.Scope{Organization: "organization"}, retrieval.Request{Query: "harbour", Mode: "hybrid", Vector: []float32{1}, GroupBy: "record", K: 3})
		if err != nil || len(hits) != 1 || hits[0].SegmentID != "best" {
			t.Fatalf("hybrid search with a slow index: %+v, %v (started %v)", hits, err, started)
		}
	})
}

func matchesFieldFilter(query, field, value string) bool {
	quoted := strconv.Quote(value)
	if strings.Contains(query, `{path:[`+strconv.Quote(field)+`],operator:Equal,valueText:`+quoted) {
		return true
	}
	prefix := `{path:[` + strconv.Quote(field) + `],operator:ContainsAny,valueText:[`
	for rest := query; ; {
		idx := strings.Index(rest, prefix)
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(prefix):]
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return false
		}
		if strings.Contains(rest[:end], quoted) {
			return true
		}
		rest = rest[end+1:]
	}
}

func TestItemSearchFetchesTextOnlyForPassageSelection(t *testing.T) {
	for _, mode := range []struct {
		group, fusion string
		wantText      []string
		wantHits      map[string]bool
	}{
		{"", retrieval.FusionRelativeScore, nil, map[string]bool{"a-dense": true, "b-dense": true, "a-sparse": true, "b-sparse": true, "single": true}},
		{"record", retrieval.FusionRelativeScore, []string{"a-sparse", "b-sparse"}, map[string]bool{"b-dense": true, "b-sparse": true, "single": true}},
		{"record", retrieval.FusionRanked, []string{"a-dense", "b-dense", "a-sparse", "b-sparse"}, map[string]bool{"b-dense": true, "b-sparse": true, "single": true}},
	} {
		t.Run(mode.group+"/"+mode.fusion, func(t *testing.T) {
			textReads := 0
			passages := []itemSearchRow{
				{Segment: "a-dense", Version: "dense", Text: "harbour boat"},
				{Segment: "a-sparse", Version: "sparse", Text: "harbour"},
				{Segment: "b-dense", Version: "dense", Text: "harbour"},
				{Segment: "b-sparse", Version: "sparse", Text: "harbour boat"},
				{Segment: "single", Version: "single", Text: "other"},
			}
			transport := itemSearchTransport(func(_ context.Context, query string) ([]byte, error) {
				switch {
				case strings.Contains(query, "nearVector:"):
					distance := 0.1
					return itemSearchResponse([]itemSearchRow{{Segment: "b-dense", Version: "dense", Distance: &distance}}), nil
				case strings.Contains(query, "bm25:"):
					return itemSearchResponse([]itemSearchRow{{Version: "dense", Score: "2"}, {Version: "sparse", Score: "2"}, {Version: "single", Score: "1"}}), nil
				case strings.Contains(query, "passageText"):
					textReads++
					var rows []itemSearchRow
					for _, row := range passages {
						if matchesFieldFilter(query, "segmentId", row.Segment) {
							rows = append(rows, row)
						}
					}
					if len(rows) != len(mode.wantText) {
						return nil, fmt.Errorf("projected text for %v, want %v", rows, mode.wantText)
					}
					for _, id := range mode.wantText {
						if !matchesFieldFilter(query, "segmentId", id) {
							return nil, fmt.Errorf("missing text for %s", id)
						}
					}
					return itemSearchResponse(rows), nil
				default:
					rows := append([]itemSearchRow(nil), passages...)
					for i := range rows {
						rows[i].Text = ""
					}
					return itemSearchResponse(rows), nil
				}
			})
			hits, err := runItemSearch(t.Context(), transport, retrieval.Request{
				Query: "harbour boat", Mode: "hybrid", Vector: []float32{1}, GroupBy: mode.group, K: 10,
				Hybrid: &retrieval.HybridOptions{Alpha: 0.5, Fusion: mode.fusion},
			})
			wantReads := 0
			if len(mode.wantText) > 0 {
				wantReads = 1
			}
			if err != nil || len(hits) != len(mode.wantHits) || textReads != wantReads {
				t.Fatalf("item search: %+v, %v, %d text reads (want %d)", hits, err, textReads, wantReads)
			}
			for _, hit := range hits {
				if !mode.wantHits[hit.SegmentID] {
					t.Fatalf("unexpected passage %s in %+v", hit.SegmentID, hits)
				}
			}
		})
	}
}

func TestItemPassageSelectionPrefersSubstantiveAndBM25(t *testing.T) {
	richHarbour := "Maritime authorities published a detailed schedule for the harbour ferries operating across the northern bay throughout the autumn season."
	richHarbourDense := "Harbour planners confirmed that harbour ferries will double harbour departures because ferries now carry record commuter traffic daily."
	richCommon := "The quarterly report summarizes municipal spending across regional schools, transit corridors, libraries, and water treatment plants."
	richRare := "Engineers deployed new photovoltaic arrays across coastal stations to stabilize afternoon electricity supply during peak summer demand."
	richLead := "Delegates met in Geneva on Monday to negotiate binding targets for grid modernization, industrial efficiency, and cross-border energy storage."
	richFrench := "Les élections municipales ont mobilisé une forte participation dans les communes côtières autour des transports publics et du logement."
	richFrenchStop := "Le conseil municipal a présenté le plan annuel des investissements pour le réseau fluvial et les équipements scolaires régionaux."

	for _, tc := range []struct {
		name     string
		analyzer string
		query    string
		passages []itemSearchRow
		want     string
	}{
		{
			name:     "rich paragraph beats 8-char matching boilerplate",
			analyzer: "folded",
			query:    "harbour",
			passages: []itemSearchRow{
				{Segment: "aaa-boilerplate", Version: "v1", Text: "Harbour."},
				{Segment: "zzz-rich", Version: "v1", Text: richHarbour},
			},
			want: "zzz-rich",
		},
		{
			name:     "rich paragraph beats short multi-term dateline boilerplate",
			analyzer: "folded",
			query:    "harbour 2026",
			passages: []itemSearchRow{
				{Segment: "aaa-dateline", Version: "v1", Text: "Harbour 2026"},
				{Segment: "zzz-rich", Version: "v1", Text: richHarbour},
			},
			want: "zzz-rich",
		},
		{
			name:     "title match with zero body hits prefers rich paragraph over 8-char footer",
			analyzer: "folded",
			query:    "unmatched-title-token",
			passages: []itemSearchRow{
				{Segment: "aaa-footer", Version: "v1", Text: "Page 1/4"},
				{Segment: "bbb-notice", Version: "v1", Text: "All rights reserved by the publisher for this edition."},
				{Segment: "zzz-lead", Version: "v1", Text: richLead},
			},
			want: "zzz-lead",
		},
		{
			name:     "bm25 breaks equal coverage ties by term frequency",
			analyzer: "folded",
			query:    "harbour ferries",
			passages: []itemSearchRow{
				{Segment: "aaa-single", Version: "v1", Text: richHarbour},
				{Segment: "zzz-multi", Version: "v1", Text: richHarbourDense},
			},
			want: "zzz-multi",
		},
		{
			name:     "bm25 breaks equal coverage ties by rare query term idf",
			analyzer: "folded",
			query:    "photovoltaic report",
			passages: []itemSearchRow{
				{Segment: "aaa-common-1", Version: "v1", Text: richCommon},
				{Segment: "bbb-common-2", Version: "v1", Text: richCommon + " Additional budget appendices follow."},
				{Segment: "ccc-common-3", Version: "v1", Text: richCommon + " Committee minutes are archived online."},
				{Segment: "zzz-rare", Version: "v1", Text: richRare},
			},
			want: "zzz-rare",
		},
		{
			name:     "french analyzer prefers rich paragraph over 8-char boilerplate",
			analyzer: "french_light",
			query:    "election",
			passages: []itemSearchRow{
				{Segment: "aaa-short", Version: "v1", Text: "Élection"},
				{Segment: "zzz-rich", Version: "v1", Text: richFrench},
			},
			want: "zzz-rich",
		},
		{
			name:     "stopword-only query under french analyzer falls back to folded scoring",
			analyzer: "french_light",
			query:    "le",
			passages: []itemSearchRow{
				{Segment: "aaa-short", Version: "v1", Text: "Le 2026."},
				{Segment: "zzz-rich", Version: "v1", Text: richFrenchStop},
			},
			want: "zzz-rich",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := itemSearchTransport(func(_ context.Context, query string) ([]byte, error) {
				switch {
				case strings.Contains(query, "bm25:"):
					return itemSearchResponse([]itemSearchRow{{Version: "v1", Score: "4.2"}}), nil
				case strings.Contains(query, "passageText"):
					return itemSearchResponse(tc.passages), nil
				default:
					meta := append([]itemSearchRow(nil), tc.passages...)
					for i := range meta {
						meta[i].Text = ""
					}
					return itemSearchResponse(meta), nil
				}
			})
			store := weaviate.New("http://index.invalid")
			store.Client.Transport = transport
			g := content.Generation{
				ID: "generation", Collection: "ItemSearch", ItemKeywordsProjected: true,
				Fields: []corpus.Field{{Name: "body", PartRole: "body", Type: "string", Roles: []string{"search"}, Analyzer: tc.analyzer}},
			}
			hits, err := store.Search(t.Context(), []retrieval.Route{{CorpusID: "corpus", Generation: g}}, corpus.Scope{Organization: "organization"}, retrieval.Request{
				Query: tc.query, Mode: "lexical", GroupBy: "record", K: 10,
			})
			if err != nil || len(hits) != 1 || hits[0].SegmentID != tc.want {
				t.Fatalf("got %+v, err=%v; want segment %s", hits, err, tc.want)
			}
		})
	}
}

func TestItemSearchBoundsPassageScansToTopK(t *testing.T) {
	const totalItems = 60
	rich := "Maritime authorities published a detailed schedule for the harbour ferries operating across the northern bay throughout the autumn season."
	bm25Rows := make([]itemSearchRow, totalItems)
	byVersion := make(map[string][]itemSearchRow, totalItems)
	for i := 0; i < totalItems; i++ {
		ver := fmt.Sprintf("v-%03d", i)
		bm25Rows[i] = itemSearchRow{Version: ver, Score: fmt.Sprintf("%.2f", float64(totalItems-i))}
		byVersion[ver] = []itemSearchRow{
			{Segment: fmt.Sprintf("aaa-%03d", i), Version: ver, Text: "Harbour."},
			{Segment: fmt.Sprintf("zzz-%03d", i), Version: ver, Text: rich},
		}
	}

	parseLimit := func(query string) int {
		idx := strings.LastIndex(query, "limit:")
		if idx < 0 {
			return -1
		}
		rest := query[idx+len("limit:"):]
		end := strings.IndexAny(rest, "), }")
		if end > 0 {
			rest = rest[:end]
		}
		n, _ := strconv.Atoi(rest)
		return n
	}

	filterPassages := func(query string, withText bool) []itemSearchRow {
		var out []itemSearchRow
		for i := 0; i < totalItems; i++ {
			ver := fmt.Sprintf("v-%03d", i)
			for _, row := range byVersion[ver] {
				if matchesFieldFilter(query, "segmentId", row.Segment) ||
					matchesFieldFilter(query, "versionId", ver) {
					if !withText {
						row.Text = ""
					}
					out = append(out, row)
				}
			}
		}
		return out
	}

	t.Run("hybrid bounds scan limits, skips tail text, and reuses dense tail passages", func(t *testing.T) {
		var mu sync.Mutex
		var textVersions []string
		var tailQueriedDense bool
		denseTailVer := "v-040"
		transport := itemSearchTransport(func(_ context.Context, query string) ([]byte, error) {
			lim := parseLimit(query)
			if lim >= 2400 {
				return nil, fmt.Errorf("unexpected 2400-passage scan limit %d in %s", lim, query)
			}
			switch {
			case strings.Contains(query, "nearVector:"):
				if lim > 600 {
					return nil, fmt.Errorf("dense scan limit %d exceeds 600", lim)
				}
				d := 0.1
				return itemSearchResponse([]itemSearchRow{{Segment: "dense-40", Version: denseTailVer, Distance: &d}}), nil
			case strings.Contains(query, "bm25:"):
				return itemSearchResponse(bm25Rows), nil
			case strings.Contains(query, "passageText"):
				rows := filterPassages(query, true)
				mu.Lock()
				for _, r := range rows {
					textVersions = append(textVersions, r.Version)
				}
				mu.Unlock()
				return itemSearchResponse(rows), nil
			default:
				if lim > 512 {
					return nil, fmt.Errorf("passage metadata scan limit %d exceeds 512", lim)
				}
				if strings.Contains(query, strconv.Quote("v-039")) && strings.Contains(query, strconv.Quote(denseTailVer)) {
					mu.Lock()
					tailQueriedDense = true
					mu.Unlock()
				}
				return itemSearchResponse(filterPassages(query, false)), nil
			}
		})
		hits, err := runItemSearch(t.Context(), transport, retrieval.Request{
			Query: "harbour", Mode: "hybrid", Vector: []float32{1}, GroupBy: "record", Limit: 5, K: retrieval.CandidateLimit,
			Hybrid: &retrieval.HybridOptions{Alpha: 0.5, Fusion: retrieval.FusionRelativeScore},
		})
		if err != nil {
			t.Fatalf("hybrid search failed: %v", err)
		}
		if len(hits) != totalItems {
			t.Fatalf("got %d hits, want all %d items preserved for fusion/fallback", len(hits), totalItems)
		}
		if tailQueriedDense {
			t.Fatal("tail passage query should reuse dense passage instead of re-querying Weaviate")
		}
		// Limit 5 gives selectionK = 10 (5 + min(5, 10)), so only v-000..v-009 get text scoring.
		seenText := map[string]bool{}
		for _, v := range textVersions {
			seenText[v] = true
			if v >= "v-010" {
				t.Fatalf("fetched passageText for tail version %s outside top-K", v)
			}
		}
		if len(seenText) != 10 {
			t.Fatalf("fetched passageText for %d versions, want 10 top-K versions", len(seenText))
		}
		byVer := map[string]string{}
		for _, h := range hits {
			byVer[h.VersionID] = h.SegmentID
		}
		for i := 0; i < 10; i++ {
			ver := fmt.Sprintf("v-%03d", i)
			if want := fmt.Sprintf("zzz-%03d", i); byVer[ver] != want {
				t.Fatalf("top-K version %s picked %s, want rich passage %s", ver, byVer[ver], want)
			}
		}
		if byVer[denseTailVer] != "dense-40" {
			t.Fatalf("dense tail version %s picked %s, want dense-40", denseTailVer, byVer[denseTailVer])
		}
	})

	t.Run("owner-filtered top items promote eligible tail items into text selection", func(t *testing.T) {
		var textVersions []string
		transport := itemSearchTransport(func(_ context.Context, query string) ([]byte, error) {
			switch {
			case strings.Contains(query, "bm25:"):
				return itemSearchResponse(bm25Rows[:15]), nil
			case strings.Contains(query, "passageText"):
				rows := filterPassages(query, true)
				for _, r := range rows {
					textVersions = append(textVersions, r.Version)
				}
				return itemSearchResponse(rows), nil
			default:
				// Only v-010..v-014 belong to the requested owner; v-000..v-009 return empty.
				var out []itemSearchRow
				for _, r := range filterPassages(query, false) {
					if r.Version >= "v-010" {
						out = append(out, r)
					}
				}
				return itemSearchResponse(out), nil
			}
		})
		hits, err := runItemSearch(t.Context(), transport, retrieval.Request{
			Query: "harbour", Mode: "lexical", GroupBy: "record", Limit: 2, K: 15, EvaluationPlugin: "trial.owner",
		})
		if err != nil {
			t.Fatalf("owner-filtered lexical search failed: %v", err)
		}
		if len(hits) != 5 {
			t.Fatalf("got %d hits, want 5 owner-eligible items", len(hits))
		}
		// Limit 2 gives selectionK = 4; top 4 eligible promoted items (v-010..v-013) pick rich passages.
		byVer := map[string]string{}
		for _, h := range hits {
			byVer[h.VersionID] = h.SegmentID
		}
		for i := 10; i < 14; i++ {
			ver := fmt.Sprintf("v-%03d", i)
			if want := fmt.Sprintf("zzz-%03d", i); byVer[ver] != want {
				t.Fatalf("promoted version %s picked %s, want rich passage %s", ver, byVer[ver], want)
			}
		}
		for _, v := range textVersions {
			if v == "v-014" {
				t.Fatal("should not fetch passageText beyond promoted selectionK")
			}
		}
	})

	t.Run("max limit retains hydration headroom for text-based passage selection", func(t *testing.T) {
		const maxLimitItems = 70
		rows := make([]itemSearchRow, maxLimitItems)
		passages := make(map[string][]itemSearchRow, maxLimitItems)
		for i := 0; i < maxLimitItems; i++ {
			ver := fmt.Sprintf("m-%03d", i)
			rows[i] = itemSearchRow{Version: ver, Score: fmt.Sprintf("%.2f", float64(maxLimitItems-i))}
			passages[ver] = []itemSearchRow{
				{Segment: fmt.Sprintf("aaa-%03d", i), Version: ver, Text: "Harbour."},
				{Segment: fmt.Sprintf("zzz-%03d", i), Version: ver, Text: rich},
			}
		}
		var mu sync.Mutex
		textVers := map[string]bool{}
		transport := itemSearchTransport(func(_ context.Context, query string) ([]byte, error) {
			lim := parseLimit(query)
			if lim >= 2400 {
				return nil, fmt.Errorf("unexpected 2400-passage scan limit %d", lim)
			}
			switch {
			case strings.Contains(query, "bm25:"):
				return itemSearchResponse(rows), nil
			case strings.Contains(query, "passageText"):
				var out []itemSearchRow
				for i := 0; i < maxLimitItems; i++ {
					ver := fmt.Sprintf("m-%03d", i)
					for _, row := range passages[ver] {
						if matchesFieldFilter(query, "segmentId", row.Segment) {
							out = append(out, row)
							mu.Lock()
							textVers[ver] = true
							mu.Unlock()
						}
					}
				}
				return itemSearchResponse(out), nil
			default:
				var out []itemSearchRow
				for i := 0; i < maxLimitItems; i++ {
					ver := fmt.Sprintf("m-%03d", i)
					if !matchesFieldFilter(query, "versionId", ver) {
						continue
					}
					for _, row := range passages[ver] {
						row.Text = ""
						out = append(out, row)
					}
				}
				if lim > 0 && len(out) > lim {
					out = out[:lim]
				}
				return itemSearchResponse(out), nil
			}
		})
		hits, err := runItemSearch(t.Context(), transport, retrieval.Request{
			Query: "harbour", Mode: "lexical", GroupBy: "record", Limit: retrieval.MaxLimit, K: retrieval.CandidateLimit,
		})
		if err != nil || len(hits) != maxLimitItems {
			t.Fatalf("max-limit search: len=%d, err=%v", len(hits), err)
		}
		wantSelected := retrieval.MaxLimit + 10
		if len(textVers) != wantSelected {
			t.Fatalf("fetched passageText for %d versions, want %d (MaxLimit + 10 headroom)", len(textVers), wantSelected)
		}
		byVer := map[string]string{}
		for _, h := range hits {
			byVer[h.VersionID] = h.SegmentID
		}
		for i := 0; i < wantSelected; i++ {
			ver := fmt.Sprintf("m-%03d", i)
			if want := fmt.Sprintf("zzz-%03d", i); byVer[ver] != want {
				t.Fatalf("version %s picked %s, want rich passage %s", ver, byVer[ver], want)
			}
		}
		for i := wantSelected; i < maxLimitItems; i++ {
			ver := fmt.Sprintf("m-%03d", i)
			if textVers[ver] {
				t.Fatalf("unexpected passageText fetch for tail version %s", ver)
			}
		}
	})

	t.Run("high-passage item saturating top or tail scan cap cannot starve other ranked versions", func(t *testing.T) {
		var allPassages []itemSearchRow
		// v-huge has 600 early-sorting passages ("aaa-0000".."aaa-0599"), enough
		// to fill the entire 512-row shared scan window before later segmentIds appear.
		for i := 0; i < 600; i++ {
			allPassages = append(allPassages, itemSearchRow{Segment: fmt.Sprintf("aaa-%04d", i), Version: "v-huge", Text: "Harbour."})
		}
		allPassages = append(allPassages,
			itemSearchRow{Segment: "zzz-top-short", Version: "v-top-starved", Text: "Harbour."},
			itemSearchRow{Segment: "zzz-top-rich", Version: "v-top-starved", Text: rich},
		)
		for i := 0; i < 600; i++ {
			allPassages = append(allPassages, itemSearchRow{Segment: fmt.Sprintf("aab-%04d", i), Version: "v-tail-huge", Text: "Harbour."})
		}
		allPassages = append(allPassages, itemSearchRow{Segment: "zzz-tail-only", Version: "v-tail-starved", Text: rich})

		// Sort all passages by segmentId asc, matching Weaviate's passageOrder.
		bm25 := []itemSearchRow{
			{Version: "v-top-starved", Score: "10.0"},
			{Version: "v-huge", Score: "9.0"},
			{Version: "v-tail-huge", Score: "5.0"},
			{Version: "v-tail-starved", Score: "4.0"},
		}
		var mu sync.Mutex
		textVers := map[string]bool{}
		transport := itemSearchTransport(func(_ context.Context, query string) ([]byte, error) {
			lim := parseLimit(query)
			if lim >= 2400 {
				return nil, fmt.Errorf("unexpected 2400-passage scan limit %d", lim)
			}
			switch {
			case strings.Contains(query, "bm25:"):
				return itemSearchResponse(bm25), nil
			case strings.Contains(query, "passageText"):
				var out []itemSearchRow
				for _, row := range allPassages {
					if matchesFieldFilter(query, "segmentId", row.Segment) {
						out = append(out, row)
						mu.Lock()
						textVers[row.Version] = true
						mu.Unlock()
					}
				}
				return itemSearchResponse(out), nil
			default:
				var out []itemSearchRow
				for _, row := range allPassages {
					if matchesFieldFilter(query, "versionId", row.Version) {
						row.Text = ""
						out = append(out, row)
					}
				}
				if lim > 0 && len(out) > lim {
					out = out[:lim]
				}
				return itemSearchResponse(out), nil
			}
		})
		// Limit 1 gives selectionK = 2, so v-top-starved and v-huge are in top,
		// while v-tail-huge and v-tail-starved are in tail.
		hits, err := runItemSearch(t.Context(), transport, retrieval.Request{
			Query: "harbour", Mode: "lexical", GroupBy: "record", Limit: 1, K: retrieval.CandidateLimit,
		})
		if err != nil {
			t.Fatalf("starvation test failed: %v", err)
		}
		if len(hits) != 4 {
			t.Fatalf("got %d hits (%+v), want all 4 versions preserved without starvation", len(hits), hits)
		}
		if hits[0].VersionID != "v-top-starved" || hits[0].SegmentID != "zzz-top-rich" {
			t.Fatalf("top-ranked starved version got %+v, want v-top-starved/zzz-top-rich", hits[0])
		}
		if hits[3].VersionID != "v-tail-starved" || hits[3].SegmentID != "zzz-tail-only" {
			t.Fatalf("tail starved version got %+v, want v-tail-starved/zzz-tail-only", hits[3])
		}
		if textVers["v-tail-starved"] || textVers["v-tail-huge"] {
			t.Fatalf("tail versions must not fetch passageText: %v", textVers)
		}

		// Ungrouped mode (GroupBy == "") must also recover starved top-ranked versions
		// when a high-passage item fills the 512-row shared scan window.
		ungroupedHits, err := runItemSearch(t.Context(), transport, retrieval.Request{
			Query: "harbour", Mode: "lexical", GroupBy: "", Limit: 2, K: retrieval.CandidateLimit,
		})
		if err != nil {
			t.Fatalf("ungrouped starvation test failed: %v", err)
		}
		foundStarved := false
		for _, h := range ungroupedHits {
			if h.VersionID == "v-top-starved" {
				foundStarved = true
				break
			}
		}
		if !foundStarved {
			t.Fatalf("ungrouped search starved v-top-starved; got %d hits", len(ungroupedHits))
		}
	})

	t.Run("dense grouped refill caps scan depth below 2400", func(t *testing.T) {
		var maxSeen int
		transport := itemSearchTransport(func(_ context.Context, query string) ([]byte, error) {
			lim := parseLimit(query)
			if lim > maxSeen {
				maxSeen = lim
			}
			// Always return `lim` passages belonging to a single version to force refill.
			d := 0.2
			rows := make([]itemSearchRow, lim)
			for i := range rows {
				rows[i] = itemSearchRow{Segment: fmt.Sprintf("seg-%04d", i), Version: "only-version", Distance: &d}
			}
			return itemSearchResponse(rows), nil
		})
		hits, err := runItemSearch(t.Context(), transport, retrieval.Request{
			Query: "harbour", Mode: "semantic", Vector: []float32{1}, GroupBy: "record", K: retrieval.CandidateLimit,
		})
		if err != nil || len(hits) != 1 {
			t.Fatalf("dense refill: %+v, %v", hits, err)
		}
		if maxSeen != 600 {
			t.Fatalf("dense max scan depth = %d, want 600", maxSeen)
		}
	})
}

type itemSearchRow struct {
	Segment  string
	Version  string
	Text     string
	Score    string
	Distance *float64
}

func itemSearchResponse(rows []itemSearchRow) []byte {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		value := map[string]any{"segmentId": row.Segment, "generationId": "generation", "versionId": row.Version}
		if row.Text != "" {
			value["passageText"] = row.Text
		}
		if row.Score != "" || row.Distance != nil {
			value["_additional"] = map[string]any{"score": row.Score, "distance": row.Distance}
		}
		out = append(out, value)
	}
	body, _ := json.Marshal(map[string]any{"data": map[string]any{"Get": map[string]any{"ItemSearch": out}}})
	return body
}

type itemSearchTransport func(context.Context, string) ([]byte, error)

func (h itemSearchTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := []byte(`{}`)
	if r.URL.Path == "/v1/graphql" {
		var req struct{ Query string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, err
		}
		var err error
		body, err = h(r.Context(), req.Query)
		if err != nil {
			return nil, err
		}
	} else if !strings.HasPrefix(r.URL.Path, "/v1/schema/") {
		return nil, errors.New("unexpected projection endpoint")
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
}

func runItemSearch(ctx context.Context, transport http.RoundTripper, q retrieval.Request) ([]content.Candidate, error) {
	store := weaviate.New("http://index.invalid")
	store.Client.Transport = transport
	g := content.Generation{
		ID: "generation", Collection: "ItemSearch", ItemKeywordsProjected: true,
		Fields: []corpus.Field{{Name: "body", PartRole: "body", Type: "string", Roles: []string{"search"}, Analyzer: "folded"}},
	}
	return store.Search(ctx, []retrieval.Route{{CorpusID: "corpus", Generation: g}}, corpus.Scope{Organization: "organization"}, q)
}

func TestItemSearchAndReplayWithLegacyAndUnifiedSegments(t *testing.T) {
	f := newAttachFixture(t)
	g := f.gen
	g.ItemKeywordsProjected, g.SpacesProjected = true, true
	f.store.LegacySpace = g.SpaceID
	routes := []retrieval.Route{{CorpusID: f.corpusID, Generation: g}}
	scope := corpus.Scope{Organization: f.org}
	vector := unitVector(1)
	for _, name := range []string{"legacy", "unified"} {
		seg := f.segmentation(name, "harbour ferry")
		seg.Segments[0].Derivation.LexicalText = name + " harbour ferry"
		v := content.Version{ID: seg.VersionID, RecordID: name, Manifest: content.Manifest{
			Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "harbour ferry"}}},
		}}
		publish := func() error { return f.store.Publish(f.ctx, g, f.org, f.corpusID, "feed", v, seg) }
		if err := publish(); err != nil {
			t.Fatal(err)
		}
		anchor := lexicalObject(f.objects(g, name))
		if name == "legacy" {
			// Restore the 2.x seed layout before attachment. It must keep
			// the same UUID and its separate enriched object on replay.
			path := f.url + "/v1/objects/" + g.Collection + "/" + anchor.ID
			req, err := http.NewRequestWithContext(f.ctx, http.MethodPatch, path,
				strings.NewReader(`{"properties":{"itemKind":"passage"}}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			res, err := f.store.Client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != http.StatusNoContent {
				t.Fatalf("restore legacy passage: %s", res.Status)
			}
		}
		transport := f.store.Client.Transport
		var checkedSwap bool
		if name == "unified" {
			f.store.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodDelete && req.URL.Path == "/v1/batch/objects" {
					// Just before the seed is retired, both immutable
					// snapshots must be visible to plugin lexical search.
					objects := f.objects(g, name)
					if plain, enriched := shape(objects); plain != 1 || enriched != 1 {
						t.Errorf("segment swap visibility: %+v", objects)
					}
					hits, err := f.store.Search(f.ctx, routes, scope, retrieval.Request{
						Query: name, Mode: "lexical", Field: retrieval.FieldLexical, RecordIDs: []string{name}, K: 3,
					})
					if err != nil || len(hits) == 0 || hits[0].SegmentID != name {
						t.Errorf("plugin lexical during segment swap: %+v, %v", hits, err)
					}
					checkedSwap = true
				}
				return transport.RoundTrip(req)
			})
		}
		data := []content.EmbeddingData{f.embedding(g, name, vector)}
		for _, write := range []func() error{
			func() error { return f.store.PublishEmbeddings(f.ctx, g, f.org, data) },
			publish,
			func() error { return f.store.PublishEmbeddings(f.ctx, g, f.org, data) },
		} {
			if err := write(); err != nil {
				t.Fatal(err)
			}
		}
		f.store.Client.Transport = transport
		objects := f.objects(g, name)
		if name == "legacy" {
			if lexical, enriched := shape(objects); lexical != 1 || enriched != 1 || lexicalObject(objects).ID != anchor.ID {
				t.Fatalf("legacy segment after replay: %+v", objects)
			}
		} else if !checkedSwap || len(objects) != 1 || objects[0].ID == anchor.ID || !objects[0].Vector {
			t.Fatalf("unified segment after replay: %+v", objects)
		}
	}
	for _, mode := range []string{"lexical", "semantic", "hybrid"} {
		hits, err := f.store.Search(f.ctx, routes, scope, retrieval.Request{
			Query: "harbour", Vector: vector, Mode: mode, GroupBy: "record", K: 2,
		})
		if err != nil || len(hits) != 2 || hits[0].SegmentID == hits[1].SegmentID {
			t.Fatalf("mixed %s search: %+v, %v", mode, hits, err)
		}
	}
}

// Regression test for BM25F stopword masking: analyzed fields (e.g. french_light)
// must not also run on the unfiltered raw branch where stopwords inflate raw BM25
// and mask stemmed matches; an all-stopword query falls back to raw fields.
func TestItemBM25AnalyzedFieldsSkipRawStopwordMasking(t *testing.T) {
	t.Run("french_light fields query only analyzed stems and never raw stopwords", func(t *testing.T) {
		var mu sync.Mutex
		var bm25Queries []string
		transport := itemSearchTransport(func(_ context.Context, query string) ([]byte, error) {
			if strings.Contains(query, "bm25:") {
				mu.Lock()
				bm25Queries = append(bm25Queries, query)
				mu.Unlock()
				if strings.Contains(query, "_fr^") {
					return itemSearchResponse([]itemSearchRow{
						{Version: "stemmed-match", Score: "3.0"},
						{Version: "stopword-heavy", Score: "1.0"},
					}), nil
				}
				// Simulate raw stopword masking: if a raw branch were issued,
				// stopword-heavy would outscore the stemmed match.
				return itemSearchResponse([]itemSearchRow{
					{Version: "stopword-heavy", Score: "9.5"},
				}), nil
			}
			return itemSearchResponse([]itemSearchRow{
				{Segment: "seg-stemmed", Version: "stemmed-match"},
				{Segment: "seg-stopword", Version: "stopword-heavy"},
			}), nil
		})
		store := weaviate.New("http://index.invalid")
		store.Client.Transport = transport
		six, two := 6, 2
		g := content.Generation{
			ID: "generation", Collection: "ItemSearch", ItemKeywordsProjected: true,
			Fields: []corpus.Field{
				{Name: "title", PartRole: "title", Type: "string", Roles: []string{"search"}, Boost: &six, Analyzer: "french_light"},
				{Name: "body", PartRole: "body", Type: "string", Roles: []string{"search"}, Boost: &two, Analyzer: "french_light"},
				{Name: "caption", PartRole: "caption", Type: "string", Roles: []string{"search"}, Analyzer: "french_light"},
				{Name: "transcript", PartRole: "transcript", Type: "string", Roles: []string{"search"}, Analyzer: "french_light"},
			},
		}
		hits, err := store.Search(t.Context(), []retrieval.Route{{CorpusID: "corpus", Generation: g}}, corpus.Scope{Organization: "organization"}, retrieval.Request{
			Query: "les élections de la ville", Mode: "lexical", GroupBy: "record", K: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(bm25Queries) != 1 {
			t.Fatalf("issued %d BM25 branches (%v), want exactly 1 analyzed french_light branch", len(bm25Queries), bm25Queries)
		}
		if !strings.Contains(bm25Queries[0], `_fr^6`) || !strings.Contains(bm25Queries[0], `_fr^2`) || strings.Contains(bm25Queries[0], `"les `) {
			t.Fatalf("unexpected BM25 query: %s", bm25Queries[0])
		}
		if len(hits) != 2 || hits[0].SegmentID != "seg-stemmed" {
			t.Fatalf("hits = %+v, want stemmed-match ranked first without raw stopword masking", hits)
		}

		// All-stopword query falls back to raw fields so BM25 still answers.
		bm25Queries = nil
		_, err = store.Search(t.Context(), []retrieval.Route{{CorpusID: "corpus", Generation: g}}, corpus.Scope{Organization: "organization"}, retrieval.Request{
			Query: "le de la", Mode: "lexical", GroupBy: "record", K: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(bm25Queries) != 1 || strings.Contains(bm25Queries[0], "_fr^") || !strings.Contains(bm25Queries[0], `query:"le de la"`) || !strings.Contains(bm25Queries[0], "^6") {
			t.Fatalf("all-stopword fallback BM25 queries = %v, want 1 raw fallback branch including analyzed fields", bm25Queries)
		}
	})

	t.Run("mixed schema keeps raw branch restricted to non-analyzed fields", func(t *testing.T) {
		var mu sync.Mutex
		var bm25Queries []string
		transport := itemSearchTransport(func(_ context.Context, query string) ([]byte, error) {
			if strings.Contains(query, "bm25:") {
				mu.Lock()
				bm25Queries = append(bm25Queries, query)
				mu.Unlock()
			}
			return itemSearchResponse(nil), nil
		})
		store := weaviate.New("http://index.invalid")
		store.Client.Transport = transport
		six, two := 6, 2
		g := content.Generation{
			ID: "generation", Collection: "ItemSearch", ItemKeywordsProjected: true,
			Fields: []corpus.Field{
				{Name: "title", PartRole: "title", Type: "string", Roles: []string{"search"}, Boost: &six},
				{Name: "body", PartRole: "body", Type: "string", Roles: []string{"search"}, Boost: &two, Analyzer: "french_light"},
			},
		}
		if _, err := store.Search(t.Context(), []retrieval.Route{{CorpusID: "corpus", Generation: g}}, corpus.Scope{Organization: "organization"}, retrieval.Request{
			Query: "les élections de la ville", Mode: "lexical", GroupBy: "record", K: 10,
		}); err != nil {
			t.Fatal(err)
		}
		if len(bm25Queries) != 2 {
			t.Fatalf("issued %d BM25 branches, want 2 (1 raw for unanalyzed fields, 1 french_light for body)", len(bm25Queries))
		}
		var sawRawWithoutBody, sawAnalyzedBodyOnly bool
		for _, q := range bm25Queries {
			if strings.Contains(q, "_fr^2") && !strings.Contains(q, "^6") {
				sawAnalyzedBodyOnly = true
			}
			if strings.Contains(q, "^6") && !strings.Contains(q, "^2") {
				sawRawWithoutBody = true
			}
		}
		if !sawRawWithoutBody || !sawAnalyzedBodyOnly {
			t.Fatalf("mixed schema BM25 branches = %v, want raw without body (^6, no ^2) and analyzed body-only (_fr^2)", bm25Queries)
		}

		// All-stopword query also falls back the analyzed body field onto the raw branch.
		bm25Queries = nil
		if _, err := store.Search(t.Context(), []retrieval.Route{{CorpusID: "corpus", Generation: g}}, corpus.Scope{Organization: "organization"}, retrieval.Request{
			Query: "le de la", Mode: "lexical", GroupBy: "record", K: 10,
		}); err != nil {
			t.Fatal(err)
		}
		if len(bm25Queries) != 1 || !strings.Contains(bm25Queries[0], "^6") || !strings.Contains(bm25Queries[0], "^2") {
			t.Fatalf("mixed schema all-stopword BM25 branches = %v, want 1 raw branch including both title (^6) and body (^2)", bm25Queries)
		}
	})
}

func TestContainsAnyFiltersAndLegacyCallerDeadline(t *testing.T) {
	t.Run("version, passage, and source namespace filters use ContainsAny", func(t *testing.T) {
		var mu sync.Mutex
		var queries []string
		transport := itemSearchTransport(func(_ context.Context, query string) ([]byte, error) {
			mu.Lock()
			queries = append(queries, query)
			mu.Unlock()
			switch {
			case strings.Contains(query, "bm25:"):
				return itemSearchResponse([]itemSearchRow{
					{Version: "v1", Score: "2"},
					{Version: "v2", Score: "1"},
				}), nil
			case strings.Contains(query, "passageText"):
				return itemSearchResponse([]itemSearchRow{
					{Segment: "s1", Version: "v1", Text: "harbour ferries"},
					{Segment: "s2", Version: "v1", Text: "harbour"},
				}), nil
			default:
				return itemSearchResponse([]itemSearchRow{
					{Segment: "s1", Version: "v1"},
					{Segment: "s2", Version: "v1"},
					{Segment: "s3", Version: "v2"},
				}), nil
			}
		})
		hits, err := runItemSearch(t.Context(), transport, retrieval.Request{
			Query:            "harbour",
			Mode:             "lexical",
			GroupBy:          "record",
			K:                10,
			SourceNamespaces: []string{"ns-a", "ns-b"},
		})
		if err != nil || len(hits) != 2 {
			t.Fatalf("item search = %+v, %v", hits, err)
		}
		var sawSourceContainsAny, sawVersionContainsAny, sawSegmentContainsAny bool
		for _, q := range queries {
			if strings.Contains(q, `{path:["sourceNamespace"],operator:ContainsAny,valueText:["ns-a","ns-b"]}`) {
				sawSourceContainsAny = true
			}
			if strings.Contains(q, `{path:["versionId"],operator:ContainsAny,valueText:[`) {
				sawVersionContainsAny = true
			}
			if strings.Contains(q, `{path:["segmentId"],operator:ContainsAny,valueText:["s1","s2"]}`) {
				sawSegmentContainsAny = true
			}
		}
		if !sawSourceContainsAny || !sawVersionContainsAny || !sawSegmentContainsAny {
			t.Fatalf("expected ContainsAny for sourceNamespace=%t versionId=%t segmentId=%t; queries=%v",
				sawSourceContainsAny, sawVersionContainsAny, sawSegmentContainsAny, queries)
		}
	})

	t.Run("legacy search propagates caller deadline past default client timeout", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			var seenQuery string
			transport := itemSearchTransport(func(ctx context.Context, query string) ([]byte, error) {
				seenQuery = query
				// Advance the virtual clock past the shared client's 4 s timeout.
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(5 * time.Second):
				}
				return itemSearchResponse([]itemSearchRow{{Segment: "legacy-seg", Score: "1.5"}}), nil
			})
			store := weaviate.New("http://index.invalid")
			store.Client.Transport = transport
			g := content.Generation{ID: "generation", Collection: "ItemSearch"}
			hits, err := store.Search(ctx, []retrieval.Route{{CorpusID: "corpus", Generation: g}}, corpus.Scope{Organization: "organization"}, retrieval.Request{
				Query: "harbour", Mode: "lexical", K: 5, SourceNamespaces: []string{"ns-a", "ns-b"},
			})
			if err != nil || len(hits) != 1 || hits[0].SegmentID != "legacy-seg" {
				t.Fatalf("legacy search with caller deadline: %+v, %v", hits, err)
			}
			if !strings.Contains(seenQuery, `{path:["sourceNamespace"],operator:ContainsAny,valueText:["ns-a","ns-b"]}`) {
				t.Fatalf("legacy search did not use ContainsAny for sourceNamespace: %s", seenQuery)
			}
		})
	})
}
