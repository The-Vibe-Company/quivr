package weaviate_test

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

// Two logical generations share one physical collection; each Corpus is served
// only from the generation PostgreSQL routes it to.
func TestLogicalGenerationsCoexistInSharedCollection(t *testing.T) {
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
	collection := "QuivrGenerations" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err = store.Bootstrap(ctx, collection); err != nil {
		t.Fatal(err)
	}
	old := content.Generation{ID: "generation-old", Collection: collection, ProfileVersion: retrieval.ProfileVersion}
	next := content.Generation{ID: "generation-next", Collection: collection, ProfileVersion: retrieval.ProfileVersion}
	org := "adapter-generations"
	segment := func(id, text string) content.Segmentation {
		return content.Segmentation{ID: "seg-" + id, VersionID: "version-" + id, Segments: []content.Segment{{ID: id, PartKey: "body", Text: text}}}
	}
	a, bSeg := segment("segment-a", "alpha shared passage"), segment("segment-b", "alpha neighbour passage")
	for _, publish := range []struct {
		g      content.Generation
		corpus string
		seg    content.Segmentation
	}{{old, "corpus-a", a}, {next, "corpus-a", a}, {old, "corpus-b", bSeg}} {
		if err = store.Publish(ctx, publish.g, org, publish.corpus, content.Version{ID: publish.seg.VersionID}, publish.seg); err != nil {
			t.Fatal(err)
		}
	}
	scope := corpus.Scope{Organization: org, Corpora: []string{"*"}}
	query := retrieval.Request{Query: "alpha", Mode: "lexical", Profile: "balanced", Limit: 10, CorpusIDs: []string{"corpus-a", "corpus-b"}}
	found := func(routes []retrieval.Route) []string {
		t.Helper()
		candidates, err := store.Search(ctx, routes, scope, query)
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
	got := found([]retrieval.Route{{CorpusID: "corpus-a", Generation: next}, {CorpusID: "corpus-b", Generation: old}})
	if want := []string{"segment-a@generation-next", "segment-b@generation-old"}; !equalStrings(got, want) {
		t.Fatalf("routed search = %v, want %v", got, want)
	}
	got = found([]retrieval.Route{{CorpusID: "corpus-a", Generation: old}})
	if want := []string{"segment-a@generation-old"}; !equalStrings(got, want) {
		t.Fatalf("prior generation search = %v, want %v", got, want)
	}
	elsewhere := next
	elsewhere.Collection = "QuivrOtherCollection"
	if _, err = store.Search(ctx, []retrieval.Route{{CorpusID: "corpus-a", Generation: old}, {CorpusID: "corpus-b", Generation: elsewhere}}, scope, query); err == nil {
		t.Fatal("routes spanning physical collections must not be fused")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
