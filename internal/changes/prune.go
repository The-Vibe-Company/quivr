package changes

import (
	"context"

	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"log/slog"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

// Each prune pass deletes at most PruneBatches transactions of PruneBatch
// positions per Organization, so one pass stays short and its locks brief.
const (
	PruneBatch   = 1000
	PruneBatches = 10
)

// PruneStore physically deletes aged journal events that every internal
// consumer has passed, and records the pruned watermark.
type PruneStore interface {
	PruneChanges(ctx context.Context, retention time.Duration, organizations []string, batch, batches int) (int, error)
}

// Pruner is the worker loop that keeps the journal bounded by retention.
// Organizations restricts it (all when empty).
type Pruner struct {
	Store         PruneStore
	Retention     time.Duration
	Interval      time.Duration
	Organizations []string
	Metrics       *telemetry.ChangePrune
}

// Run prunes immediately, then on every interval until ctx ends.
func (p Pruner) Run(ctx context.Context) {
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		n, err := p.Store.PruneChanges(lifecycle.WorkContext(ctx), p.Retention, p.Organizations, PruneBatch, PruneBatches)
		p.Metrics.Pruned(n)
		if err != nil && ctx.Err() == nil {
			p.Metrics.Failed()
			slog.Warn("change journal prune failed; retrying next interval", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
