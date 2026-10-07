//go:build measurement

package retrieval_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// Explicit measurement only: go test -tags measurement ./internal/retrieval
// -run TestRebuildMeasurement -v -timeout 25m. Never part of check or verify.
// A provider call waits 500ms; the Step loop waits the workflow's 200ms pace.
type latencyRebuildDeriver struct{ fakeDeriver }

func (d *latencyRebuildDeriver) Derive(ctx context.Context, org, corpusID string, v content.Version, g content.Generation) (content.Segmentation, []content.EmbeddingData, error) {
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return content.Segmentation{}, nil, ctx.Err()
	}
	return d.fakeDeriver.Derive(ctx, org, corpusID, v, g)
}

func TestRebuildMeasurement(t *testing.T) {
	for _, concurrency := range []int{1, 0} {
		t.Run(fmt.Sprintf("concurrency=%d", concurrency), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := &fakeRebuildStore{covered: map[string][]content.Embedding{}}
			for i := range 2000 {
				store.candidates = append(store.candidates, retrieval.RebuildCandidate{RecordID: fmt.Sprint(i), VersionID: fmt.Sprint(i), VectorsRequired: true})
			}
			d := &latencyRebuildDeriver{}
			r := rebuilder(store, &fakeRebuildContent{}, &fakeRebuildProjection{})
			r.Concurrency, r.Plugin = concurrency, d
			started := time.Now()
			for {
				done, err := r.Step(ctx, "org", "op")
				if err != nil {
					t.Fatal(err)
				}
				if done {
					break
				}
				time.Sleep(200 * time.Millisecond) // Measurement includes production pacing.
			}
			elapsed := time.Since(started)
			if len(store.covered) != 2000 || d.derived != 2000 || !store.activated {
				t.Fatalf("coverage=%d calls=%d activated=%v", len(store.covered), d.derived, store.activated)
			}
			effective := concurrency
			if effective == 0 {
				effective = retrieval.DefaultRebuildConcurrency
			}
			t.Logf("versions=2000 configured_concurrency=%d effective_concurrency=%d elapsed_seconds=%.3f versions_per_second=%.3f calls=%d", concurrency, effective, elapsed.Seconds(), 2000/elapsed.Seconds(), d.derived)
		})
	}
}
