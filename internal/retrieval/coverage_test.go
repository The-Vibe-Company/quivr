package retrieval

import (
	"context"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// countingRegistry routes the Corpus to generation and counts its reads.
type countingRegistry struct {
	generation, space string
	reads             int
	routing           *content.IngestionRouting
}

func (r *countingRegistry) VectorSpaces(context.Context, string, string) (content.Generation, []content.SpaceCoverage, int64, error) {
	r.reads++
	return content.Generation{ID: r.generation, SpaceID: r.space, IngestionRouting: r.routing}, []content.SpaceCoverage{{GenerationRole: content.SpaceServed, Segments: int64(r.reads)}}, 10, nil
}

// Coverage is counted once per TTL and routed generation and space, not per
// search.
func TestCoverageIsReadOncePerTTLAndGeneration(t *testing.T) {
	now := time.Unix(0, 0)
	cache := &CoverageCache{TTL: 10 * time.Second, now: func() time.Time { return now }}
	registry := &countingRegistry{generation: "gen-a", space: "small@1"}
	route := Route{CorpusID: "corpus", Generation: content.Generation{ID: "gen-a", SpaceID: "small@1"}}
	read := func() int64 {
		t.Helper()
		spaces, _, err := cache.spaces(context.Background(), registry, "org", route)
		if err != nil {
			t.Fatal(err)
		}
		return spaces[0].Segments
	}
	read()
	now = now.Add(9 * time.Second)
	if read(); registry.reads != 1 {
		t.Fatalf("%d registry reads within the TTL, want 1", registry.reads)
	}
	now = now.Add(2 * time.Second)
	if read(); registry.reads != 2 {
		t.Fatalf("%d registry reads after the TTL, want 2", registry.reads)
	}
	registry.generation, route.Generation.ID = "gen-b", "gen-b"
	if got := read(); registry.reads != 3 || got != 3 {
		t.Fatalf("after a rebuild: %d reads, coverage %d; want the new generation read at once", registry.reads, got)
	}
	registry.space, route.Generation.SpaceID = "large@1", "large@1"
	if got := read(); registry.reads != 4 || got != 4 {
		t.Fatalf("after a space promotion: %d reads, coverage %d; want the promoted space read at once", registry.reads, got)
	}
	registry.routing = &content.IngestionRouting{Default: "example.first", Routes: map[string]string{"text/plain": "example.second"}}
	route.Generation.IngestionRouting = registry.routing
	if got := read(); registry.reads != 5 || got != 5 {
		t.Fatalf("after a source-route promotion: %d reads, coverage %d; want new serving-owner coverage at once", registry.reads, got)
	}

}
