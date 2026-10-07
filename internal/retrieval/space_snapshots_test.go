package retrieval

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

type snapshotRegistry struct {
	generation string
	reads      int
	complete   chan struct{}
	fail       bool
}

func (r *snapshotRegistry) DescribeVectorSpaces(context.Context, string, string) (content.Generation, []content.SpaceCoverage, error) {
	g := content.Generation{ID: r.generation, SpaceID: "space"}
	sp := content.SpaceCoverage{CoverageUnknown: true, GenerationRole: content.SpaceServed}
	sp.ID = "space"
	return g, []content.SpaceCoverage{sp}, nil
}

func (r *snapshotRegistry) VectorSpaces(ctx context.Context, org, id string) (content.Generation, []content.SpaceCoverage, int64, error) {
	g, spaces, _ := r.DescribeVectorSpaces(ctx, org, id)
	r.reads++
	select {
	case <-r.complete:
	case <-ctx.Done():
		return g, nil, 0, ctx.Err()
	}
	if r.fail {
		return g, nil, 0, errors.New("registry unavailable")
	}
	spaces[0].Segments = 7
	return g, spaces, 10, nil
}

// No wall-clock waits: virtual time verifies cold, stale and failed refreshes
// while the real counting dependency is deliberately blocked (THE-1231).
func TestSpaceSnapshotsNeverWaitForCoverage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		registry := &snapshotRegistry{generation: "a", complete: make(chan struct{})}
		cache := NewSpaceSnapshots(ctx, registry, 10*time.Second)
		read := func(org string) content.SpaceCoverage {
			t.Helper()
			_, spaces, total, err := cache.VectorSpaces(context.Background(), org, "corpus")
			if err != nil || len(spaces) != 1 {
				t.Fatalf("spaces %+v, error %v", spaces, err)
			}
			if spaces[0].CoverageUnknown && (total != 0 || spaces[0].CoverageAgeMS != nil) {
				t.Fatalf("unknown coverage must not invent an observation: total %d, %+v", total, spaces[0])
			}
			return spaces[0]
		}
		if !read("org").CoverageUnknown {
			t.Fatal("cold snapshot must be unknown")
		}
		synctest.Wait()
		for range 5 {
			if !read("org").CoverageUnknown {
				t.Fatal("blocked count must remain unknown")
			}
		}
		if registry.reads != 1 {
			t.Fatalf("%d simultaneous full counts, want one", registry.reads)
		}
		close(registry.complete)
		synctest.Wait()
		if sp := read("org"); sp.CoverageUnknown || sp.Segments != 7 || sp.CoverageAgeMS == nil || *sp.CoverageAgeMS != 0 {
			t.Fatalf("completed snapshot %+v", sp)
		}
		registry.complete = make(chan struct{})
		time.Sleep(11 * time.Second) // advances only the synctest clock
		if sp := read("org"); sp.CoverageUnknown || sp.Segments != 7 || *sp.CoverageAgeMS != 11000 {
			t.Fatalf("stale snapshot must stay available during refresh: %+v", sp)
		}
		synctest.Wait()
		registry.fail = true
		close(registry.complete)
		synctest.Wait()
		if sp := read("org"); sp.CoverageUnknown || sp.Segments != 7 || *sp.CoverageAgeMS != 11000 {
			t.Fatalf("failed refresh must preserve observation and age: %+v", sp)
		}
		if !read("other-org").CoverageUnknown {
			t.Fatal("snapshot crossed organization boundary")
		}
		synctest.Wait()
		registry.generation = "b"
		registry.complete = make(chan struct{})
		if !read("org").CoverageUnknown {
			t.Fatal("new generation inherited old counts")
		}
		synctest.Wait()
		// The query was already reading generation b when routing moved to c.
		registry.generation = "c"
		registry.fail = false
		close(registry.complete)
		synctest.Wait()
		if !read("org").CoverageUnknown {
			t.Fatal("concurrent routing change inherited another generation")
		}
		cancel()
		synctest.Wait()
	})
}
