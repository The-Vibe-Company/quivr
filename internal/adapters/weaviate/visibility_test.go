package weaviate

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
	"google.golang.org/protobuf/proto"
)

type testSourcePartitions map[string]visibility.Sources

func (p testSourcePartitions) Sources(_ context.Context, _, id string, _ []string) (visibility.Sources, error) {
	return p[id], nil
}

func (p testSourcePartitions) Current(context.Context, string, map[string]int64) (bool, error) {
	return true, nil
}

func (p testSourcePartitions) SourceVersions(_ context.Context, _, corpusID string, namespaces []string, _ int) ([]string, error) {
	if slices.Equal(namespaces, []string{" permitted "}) {
		return []string{corpusID + "-padded"}, nil
	}
	return nil, fmt.Errorf("unexpected exact namespace request %v", namespaces)
}

func TestVisibilityPrefilterOnSharedAndTenantCollections(t *testing.T) {
	httpStore, grpcStore := realTenantStores(t)
	if grpcStore.grpc == nil {
		t.Fatal("gRPC integration requires weaviate_grpc_address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	org := fmt.Sprintf("visibility-%d", time.Now().UnixNano())
	legacy := "QuivrTextV4Visibility"
	parts := testSourcePartitions{}
	httpStore.Visibility, grpcStore.Visibility = parts, parts
	scope := corpus.Scope{Organization: org, Corpora: []string{"*"}}
	g := content.Generation{ID: "visibility", Collection: InitialCollection, SpaceID: "fixture", ItemKeywordsProjected: true, SourceNamespaceProjected: true}
	native := retrieval.Route{CorpusID: "native", Generation: g}
	shared := retrieval.Route{CorpusID: "shared", Generation: g}
	shared.Generation.Collection = legacy
	shared.Generation.ItemKeywordsProjected = false
	for _, route := range []retrieval.Route{native, shared} {
		if err := httpStore.Bootstrap(ctx, route.Generation.Collection); err != nil {
			t.Fatal(err)
		}
		if err := httpStore.EnsureGeneration(ctx, route.Generation.Collection, org, route.CorpusID, route.Generation.ID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := httpStore.PurgeGeneration(ctx, route.Generation.Collection, org, route.CorpusID, route.Generation.ID); err != nil {
				t.Error(err)
			}
		})
		for i, doc := range []struct {
			id, namespace string
			acl           []string
		}{
			{"visible", "permitted", nil},
			{"hidden-source", "blocked", nil},
			{"hidden-acl", "permitted", []string{"user:other"}},
			{"override", "blocked", []string{"group:reader"}},
			{"padded", " permitted ", nil},
		} {
			id := route.CorpusID + "-" + doc.id
			text := "harbour report"
			if i == 1 || i == 2 {
				text = "harbour harbour harbour"
			}
			v := content.Version{ID: id, RecordID: id, RecordACL: doc.acl, Manifest: content.Manifest{Parts: []content.Part{
				{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: text}},
			}}}
			seg := content.Segmentation{ID: id, VersionID: id, Segments: []content.Segment{{ID: id, PartKey: "body", Text: text}}}
			gen := route.Generation
			if err := httpStore.Publish(ctx, gen, org, route.CorpusID, doc.namespace, v, seg); err != nil {
				t.Fatalf("publish %s: %v", id, err)
			}
			vec := []float32{0, 1, 0, 0}
			if i == 1 || i == 2 {
				vec = []float32{1, 0, 0, 0}
			}
			raw, _ := content.VectorBytes(vec)
			artifact := content.Embedding{Organization: org, CorpusID: route.CorpusID, SegmentID: id, SpaceID: gen.SpaceID, Payload: content.Blob{SHA256: content.Hash(raw)}}
			if err := grpcStore.PublishEmbeddings(ctx, gen, org, []content.EmbeddingData{{Artifact: artifact, Vector: vec}}); err != nil {
				t.Fatalf("embed %s: %v", id, err)
			}
		}
	}
	caller := visibility.WithPrincipals(ctx, []string{"group:reader"})
	for _, scenario := range []struct {
		name            string
		allowed, denied []string
		override        bool
		want            []string
	}{
		{"all", []string{"permitted", "blocked", " permitted "}, nil, true, []string{"hidden-source", "override", "padded", "visible"}},
		{"exclude", append(sourceNamespaceList("permitted", 599), " permitted "), sourceNamespaceList("blocked", 400), true, []string{"override", "padded", "visible"}},
		{"include", append(sourceNamespaceList("permitted", 499), " permitted "), sourceNamespaceList("blocked", 500), true, []string{"override", "padded", "visible"}},
		{"exact namespace", []string{" permitted "}, []string{"permitted", "blocked"}, true, []string{"override", "padded"}},
		{"override only", nil, []string{"permitted", "blocked"}, true, []string{"override"}},
		{"short circuit", nil, []string{"permitted", "blocked"}, false, nil},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			parts["native"] = visibility.Sources{Allowed: scenario.allowed, Denied: scenario.denied, RecordOverride: scenario.override}
			parts["shared"] = parts["native"]
			for _, client := range []*Store{httpStore, grpcStore} {
				for _, mode := range []string{"lexical", "semantic", "hybrid"} {
					var want []string
					for _, route := range []retrieval.Route{native, shared} {
						for _, suffix := range scenario.want {
							want = append(want, route.CorpusID+"-"+suffix)
						}
					}
					q := retrieval.Request{Mode: mode, Query: "harbour", Vector: []float32{1, 0, 0, 0}, K: 10}
					got, err := client.Search(caller, []retrieval.Route{native, shared}, scope, q)
					var ids []string
					for _, c := range got {
						ids = append(ids, c.SegmentID)
					}
					slices.Sort(ids)
					slices.Sort(want)
					if err != nil || !slices.Equal(ids, want) {
						t.Fatalf("mode=%s grpc=%v: got %v (%v), want %v", mode, client.grpc != nil, ids, err, want)
					}
				}
			}
		})
	}
	changed := &changingSourcePartitions{}
	httpStore.Visibility = changed
	got, err := httpStore.Search(caller, []retrieval.Route{native}, scope, retrieval.Request{Mode: "lexical", Query: "harbour", K: 1})
	if err != nil || len(got) != 0 || changed.reads != 2 {
		t.Fatalf("stale route ranking was returned: got=%v reads=%d err=%v", got, changed.reads, err)
	}

}

// Simulate a second API instance committing revocation while Weaviate ranks
// an already-resolved partition. Only the source resolver is substituted.
type changingSourcePartitions struct {
	testSourcePartitions
	reads int
}

func (p *changingSourcePartitions) Sources(context.Context, string, string, []string) (visibility.Sources, error) {
	p.reads++
	if p.reads == 1 {
		return visibility.Sources{Allowed: []string{"permitted", "blocked", " permitted "}, Revision: 1}, nil
	}
	return visibility.Sources{Revision: 2}, nil
}

func (p *changingSourcePartitions) Current(_ context.Context, _ string, revisions map[string]int64) (bool, error) {
	return revisions["native"] == 2, nil
}

func sourceNamespaceList(prefix string, count int) []string {
	values := []string{prefix}
	for i := 1; i < count; i++ {
		values = append(values, fmt.Sprintf("%s-%04d", prefix, i))
	}
	return values
}

func BenchmarkVisibilityFilter(b *testing.B) {
	for _, n := range []int{500, 1000} {
		sources := visibility.Sources{Allowed: sourceNamespaceList("allowed", n/2), Denied: sourceNamespaceList("denied", n/2)}
		for _, transport := range []string{"http", "grpc"} {
			b.Run(fmt.Sprintf("%s/%d", transport, n), func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					f, _ := compileVisibility(sources, []string{"group:reader"})
					if transport == "http" {
						_ = f.graphql()
					} else {
						if _, err := proto.Marshal(f.proto()); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}
