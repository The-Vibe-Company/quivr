package weaviate_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// A source filter is part of the candidate query: in every mode only the
// chosen Source Namespaces are ranked, so a source whose matches rank below
// the whole candidate window of other sources still returns them.
func TestSourceFilterAppliesBeforeRanking(t *testing.T) {
	f := newAttachFixture(t)
	publish := func(namespace string, seg content.Segmentation) {
		t.Helper()
		if err := f.store.Publish(f.ctx, f.gen, f.org, f.corpusID, namespace, content.Version{ID: seg.VersionID}, seg); err != nil {
			t.Fatal(err)
		}
	}
	// More strong matches than the candidate window, from one source.
	loud := map[string]bool{}
	for i := 0; i <= retrieval.CandidateLimit; i++ {
		id := fmt.Sprintf("loud-%03d", i)
		loud[id] = true
		publish("loud-feed", f.segmentation(id, "harbour harbour harbour"))
	}
	publish("quiet-feed", f.segmentation("quiet", "a long quay report where the harbour is mentioned once among many other words"))
	// Vectors for two segments, so semantic and hybrid have candidates in each source.
	query := unitVector(1)
	if err := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, []content.EmbeddingData{f.embedding(f.gen, "loud-000", query), f.embedding(f.gen, "quiet", unitVector(2))}); err != nil {
		t.Fatal(err)
	}
	scope := corpus.Scope{Organization: f.org, Corpora: []string{"*"}}
	routes := []retrieval.Route{{CorpusID: f.corpusID, Generation: f.gen}}
	search := func(mode string, namespaces ...string) []string {
		t.Helper()
		candidates, err := f.store.Search(f.ctx, routes, scope, retrieval.Request{Query: "harbour", Vector: query, Mode: mode, SourceNamespaces: namespaces})
		if err != nil {
			t.Fatalf("%s search filtered on %v: %v", mode, namespaces, err)
		}
		seen := map[string]bool{}
		for _, c := range candidates {
			seen[c.SegmentID] = true
		}
		out := []string{}
		for id := range seen {
			out = append(out, id)
		}
		sort.Strings(out)
		return out
	}
	if got := search("lexical"); contains(got, "quiet") {
		t.Fatalf("setup: the quiet match must rank beyond the unfiltered candidate window, got it among %d", len(got))
	}
	for _, mode := range []string{"lexical", "semantic", "hybrid"} {
		if got := search(mode, "quiet-feed"); !equalStrings(got, []string{"quiet"}) {
			t.Errorf("%s search filtered on quiet-feed = %v, want [quiet]", mode, got)
		}
		for _, id := range search(mode, "loud-feed") {
			if !loud[id] {
				t.Errorf("%s search filtered on loud-feed returned %s from another source", mode, id)
			}
		}
		if got := search(mode, "absent-feed"); len(got) != 0 {
			t.Errorf("%s search filtered on absent-feed = %v, want none", mode, got)
		}
		if got := search(mode, "absent-feed", "quiet-feed"); !equalStrings(got, []string{"quiet"}) {
			t.Errorf("%s search filtered on absent-feed or quiet-feed = %v, want [quiet]", mode, got)
		}
	}
}

// Generation routing selects the served projection before scoring. Evaluation
// queries select one owner explicitly and never admit legacy anchors without
// an owner property.
func TestProjectionRoutingFiltersOwnersBeforeScoring(t *testing.T) {
	f := newAttachFixture(t)
	g := f.gen
	g.IngestionRouting = &content.IngestionRouting{Default: "plugin.default", Routes: map[string]string{"application/pdf": "plugin.pdf"}}
	g.Spaces = []content.GenerationSpace{
		{ID: g.SpaceID, Role: content.SpaceServed, OwnerPluginID: "plugin.default"},
		{ID: "pdf@1", Role: content.SpaceServed, OwnerPluginID: "plugin.pdf"},
		{ID: "evaluation@1", Role: content.SpaceEvaluation, OwnerPluginID: "plugin.evaluation"},
	}
	publish := func(id, owner, mediaType string) {
		t.Helper()
		seg := f.segmentation(id, "harbour owner routing")
		seg.Recipe = "plugin:" + owner + "@1.0.0"
		if err := f.store.Publish(f.ctx, g, f.org, f.corpusID, "example-feed", content.Version{ID: seg.VersionID, SourceMediaType: mediaType}, seg); err != nil {
			t.Fatal(err)
		}
	}
	publish("default-text", "plugin.default", "text/plain")
	publish("default-pdf", "plugin.default", "application/pdf")
	publish("pdf-pdf", "plugin.pdf", "application/pdf")
	publish("pdf-text", "plugin.pdf", "text/plain")
	publish("evaluation-pdf", "plugin.evaluation", "application/pdf")
	f.publish(g, f.segmentation("legacy", "harbour legacy anchor"))

	search := func(q retrieval.Request, generation content.Generation) []string {
		t.Helper()
		q.Query, q.Mode, q.CorpusIDs = "harbour", "lexical", []string{f.corpusID}
		candidates, err := f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: generation}}, corpus.Scope{Organization: f.org, Corpora: []string{"*"}}, q)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			got = append(got, candidate.SegmentID)
		}
		sort.Strings(got)
		return got
	}
	if got := search(retrieval.Request{}, g); !equalStrings(got, []string{"default-text", "legacy", "pdf-pdf"}) {
		t.Fatalf("normal routed search = %v, want served routes plus legacy anchors", got)
	}
	if got := search(retrieval.Request{EvaluationPlugin: "plugin.evaluation"}, g); !equalStrings(got, []string{"evaluation-pdf"}) {
		t.Fatalf("evaluation-owner search = %v, want the selected owner only", got)
	}

	fallback := g
	fallback.ID = "generation-fallback"
	fallback.IngestionRouting = nil
	fallback.Spaces = []content.GenerationSpace{
		{ID: fallback.SpaceID, Role: content.SpaceServed, OwnerPluginID: "plugin.default"},
		{ID: "space-evaluation@1", Role: content.SpaceEvaluation, OwnerPluginID: "plugin.evaluation"},
	}
	fallback.SpacesProjected = true
	publishFallback := func(id, owner string) {
		t.Helper()
		seg := f.segmentation(id, "harbour fallback owner")
		seg.Recipe = "plugin:" + owner + "@1.0.0"
		if err := f.store.Publish(f.ctx, fallback, f.org, f.corpusID, "example-feed", content.Version{ID: seg.VersionID, SourceMediaType: "text/plain"}, seg); err != nil {
			t.Fatal(err)
		}
	}
	publishFallback("fallback-default", "plugin.default")
	publishFallback("fallback-evaluation", "plugin.evaluation")
	f.publish(fallback, f.segmentation("fallback-legacy", "harbour fallback legacy"))
	if got := search(retrieval.Request{}, fallback); !equalStrings(got, []string{"fallback-default", "fallback-legacy"}) {
		t.Fatalf("space-role fallback search = %v, want served owner plus legacy anchors", got)
	}
}

// A collection created before Source Namespace filtering gains the property at
// bootstrap, so publication keeps working with the engine's auto-schema off.
func TestBootstrapUpgradesCollectionWithoutSourceNamespace(t *testing.T) {
	f := newAttachFixture(t)
	legacy := f.gen
	legacy.Collection = f.gen.Collection + "Legacy"
	properties := []any{}
	for _, name := range []string{"organization", "corpusId", "generationId", "segmentId", "versionId", "segmentationId"} {
		properties = append(properties, map[string]any{"name": name, "dataType": []string{"text"}, "tokenization": "field", "indexFilterable": true, "indexSearchable": false})
	}
	for _, name := range []string{"title", "body"} {
		properties = append(properties, map[string]any{"name": name, "dataType": []string{"text"}, "tokenization": "word", "indexSearchable": true})
	}
	body, _ := json.Marshal(map[string]any{"class": legacy.Collection, "properties": properties, "vectorConfig": map[string]any{"semantic_text_v1": map[string]any{"vectorizer": map[string]any{"none": nil}, "vectorIndexType": "hnsw"}}})
	res, err := http.Post(f.url+"/v1/schema", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create legacy collection: HTTP %d", res.StatusCode)
	}
	for i := 0; i < 2; i++ { // Bootstrap stays idempotent once upgraded.
		if err = f.store.Bootstrap(f.ctx, legacy.Collection); err != nil {
			t.Fatalf("bootstrap %d of a legacy collection: %v", i+1, err)
		}
	}
	var schema struct {
		Properties []struct {
			Name string `json:"name"`
		} `json:"properties"`
	}
	res, err = http.Get(f.url + "/v1/schema/" + legacy.Collection)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.NewDecoder(res.Body).Decode(&schema); err != nil {
		res.Body.Close()
		t.Fatal(err)
	}
	res.Body.Close()
	propertyNames := map[string]bool{}
	for _, property := range schema.Properties {
		propertyNames[property.Name] = true
	}
	for _, name := range []string{"projectionPlugin", "sourceMediaType", "sourceNamespace", "lexicalText"} {
		if !propertyNames[name] {
			t.Fatalf("upgraded schema lacks %s: %+v", name, propertyNames)
		}
	}
	seg := f.segmentation("upgraded", "harbour notice")
	if err = f.store.Publish(f.ctx, legacy, f.org, f.corpusID, "example-feed", content.Version{ID: seg.VersionID}, seg); err != nil {
		t.Fatalf("publish into an upgraded collection: %v", err)
	}
	candidates, err := f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: legacy}}, corpus.Scope{Organization: f.org}, retrieval.Request{Query: "harbour", Mode: "lexical", SourceNamespaces: []string{"example-feed"}})
	if err != nil || len(candidates) != 1 || candidates[0].SegmentID != "upgraded" {
		t.Fatalf("filtered search in an upgraded collection = %v, %v; want the upgraded segment", candidates, err)
	}

	// An anchor written before the property existed is never rewritten by a
	// later publication of its segment: a rewrite re-indexes it.
	anchor := lexicalObject(f.objects(legacy, "upgraded"))
	old := map[string]any{"class": legacy.Collection, "id": anchor.ID, "properties": map[string]any{"organization": f.org, "corpusId": f.corpusID, "generationId": legacy.ID, "versionId": seg.VersionID, "segmentationId": seg.ID, "segmentId": "upgraded", "body": "harbour notice", "title": ""}}
	body, _ = json.Marshal(old)
	req, _ := http.NewRequest(http.MethodPut, f.url+"/v1/objects/"+legacy.Collection+"/"+anchor.ID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if res, err = http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("rewrite anchor as legacy: HTTP %d", res.StatusCode)
	}
	before := lexicalObject(f.objects(legacy, "upgraded"))
	if err = f.store.Publish(f.ctx, legacy, f.org, f.corpusID, "example-feed", content.Version{ID: seg.VersionID}, seg); err != nil {
		t.Fatalf("republish over a legacy anchor: %v", err)
	}
	if after := lexicalObject(f.objects(legacy, "upgraded")); after.Updated != before.Updated {
		t.Fatalf("legacy anchor rewritten: updated %s, then %s", before.Updated, after.Updated)
	}
}

// A plugin anchor written before ownership metadata was added remains
// immutable when publication learns the metadata later.
func TestPluginAnchorMissingRoutingMetadataRemainsImmutable(t *testing.T) {
	f := newAttachFixture(t)
	seg := f.segmentation("plugin-anchor", "harbour plugin metadata")
	seg.Recipe = "plugin:plugin.default@1.0.0"
	v := content.Version{ID: seg.VersionID, SourceMediaType: "application/pdf"}
	if err := f.store.Publish(f.ctx, f.gen, f.org, f.corpusID, "example-feed", v, seg); err != nil {
		t.Fatal(err)
	}
	anchor := lexicalObject(f.objects(f.gen, "plugin-anchor"))
	old := map[string]any{"class": f.gen.Collection, "id": anchor.ID, "properties": map[string]any{
		"organization": f.org, "corpusId": f.corpusID, "generationId": f.gen.ID, "versionId": seg.VersionID,
		"segmentationId": seg.ID, "segmentId": "plugin-anchor", "sourceNamespace": "example-feed", "body": "harbour plugin metadata", "title": "",
	}}
	body, _ := json.Marshal(old)
	req, err := http.NewRequest(http.MethodPut, f.url+"/v1/objects/"+f.gen.Collection+"/"+anchor.ID, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("rewrite plugin anchor as legacy: HTTP %d", res.StatusCode)
	}
	before := lexicalObject(f.objects(f.gen, "plugin-anchor"))
	if err = f.store.Publish(f.ctx, f.gen, f.org, f.corpusID, "example-feed", v, seg); err != nil {
		t.Fatal(err)
	}
	if after := lexicalObject(f.objects(f.gen, "plugin-anchor")); after.Updated != before.Updated {
		t.Fatalf("plugin anchor with missing routing metadata rewritten: updated %s, then %s", before.Updated, after.Updated)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
