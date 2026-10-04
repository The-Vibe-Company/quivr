package changes

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

type pruneCall struct {
	retention      time.Duration
	organizations  []string
	batch, batches int
}

type fakePruneStore struct {
	mu      sync.Mutex
	calls   []pruneCall
	results []error
}

func (f *fakePruneStore) PruneChanges(_ context.Context, retention time.Duration, organizations []string, batch, batches int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, pruneCall{retention, organizations, batch, batches})
	if i := len(f.calls) - 1; i < len(f.results) && f.results[i] != nil {
		return 2, f.results[i]
	}
	return 3, nil
}

func (f *fakePruneStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// TestPrunerRunsBoundedPassesAndCountsThem runs the loop immediately and on
// each interval with the bounded batch policy, counts pruned events (also
// from a failed pass) and failures, and stops with its context.
func TestPrunerRunsBoundedPassesAndCountsThem(t *testing.T) {
	store := &fakePruneStore{results: []error{nil, errors.New("database unavailable")}}
	metrics := &telemetry.ChangePrune{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Pruner{Store: store, Retention: time.Hour, Interval: 10 * time.Millisecond, Organizations: []string{"org_r"}, Metrics: metrics}.Run(ctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for store.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pruner did not stop with its context")
	}
	if store.count() < 3 {
		t.Fatal("pruner did not run on its interval", store.count())
	}
	first := store.calls[0]
	if first.retention != time.Hour || !slices.Equal(first.organizations, []string{"org_r"}) || first.batch != PruneBatch || first.batches != PruneBatches {
		t.Fatalf("unbounded or misconfigured pass %+v", first)
	}
	var out bytes.Buffer
	metrics.Write(&out)
	for _, want := range []string{"# TYPE quivr_change_events_pruned_total counter", "# TYPE quivr_change_prune_failures_total counter", "quivr_change_prune_failures_total 1\n"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("metrics missing %q:\n%s", want, out.String())
		}
	}
	if !strings.Contains(out.String(), "quivr_change_events_pruned_total ") || strings.Contains(out.String(), "quivr_change_events_pruned_total 0\n") {
		t.Fatalf("pruned events not counted:\n%s", out.String())
	}
}
