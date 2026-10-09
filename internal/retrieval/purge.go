package retrieval

import (
	"context"
	"errors"

	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"io"
	"log/slog"
	"sync/atomic"
	"time"
)

// Purge kinds: an abandoned (Organization, Corpus, generation) triple, or a
// dead Record Version whose objects no routed generation can serve again.
const (
	PurgeGeneration = "generation"
	PurgeVersion    = "version"
)

// DefaultPurgeGrace is how long a dead item stays in the projection store
// before its objects are deleted, so searches and guarded writes still aimed
// at it when it died finish first.
const DefaultPurgeGrace = time.Hour

// Purge requests and the complete sweep stay below the five-minute item lease.
const (
	DefaultPurgeTimeout = time.Minute
	MaximumPurgeTimeout = 2 * time.Minute
)

// PurgeItem is one claimed purge. Collections name the physical collections
// its objects may live in.
type PurgeItem struct {
	Organization string
	Kind         string
	CorpusID     string
	GenerationID string
	VersionID    string
	Collections  []string
	NoticedAt    time.Time
}

// PurgeResult is the outcome of one bounded delete-by-filter. Complete means
// nothing matching the filter remains.
type PurgeResult struct {
	Deleted  int
	Complete bool
	// RemainingAtLeast is a lower bound from a bounded provider observation.
	RemainingAtLeast int
}

// PurgeStore selects, leases and records purges in PostgreSQL. Selection only
// ever returns items that can never be routed, or current and eligible, again.
type PurgeStore interface {
	// NoticePurges records up to limit newly dead items and reports how many.
	NoticePurges(ctx context.Context, limit int) (int, error)
	// ClaimPurges leases up to limit unpurged items noticed before now-grace.
	ClaimPurges(ctx context.Context, grace, lease time.Duration, limit int) ([]PurgeItem, error)
	// RecordPurge adds deleted objects to the item's record and releases its
	// lease; complete marks it purged so it is never claimed again.
	RecordPurge(ctx context.Context, item PurgeItem, deleted int, complete bool) error
}

// PurgeProjection deletes scoped projection objects in bounded batches.
type PurgeProjection interface {
	PurgeGeneration(ctx context.Context, collection, org, corpusID, generationID string) (PurgeResult, error)
	PurgeVersion(ctx context.Context, collection, org, versionID string) (PurgeResult, error)
}

// PurgeMetrics counts completed purges and deleted objects per kind.
type PurgeMetrics struct {
	Purges, Objects, Failures, Oldest, Remaining map[string]*atomic.Int64
}

// NewPurgeMetrics returns zeroed counters for both purge kinds.
func NewPurgeMetrics() *PurgeMetrics {
	m := &PurgeMetrics{Purges: map[string]*atomic.Int64{}, Objects: map[string]*atomic.Int64{}, Failures: map[string]*atomic.Int64{}, Oldest: map[string]*atomic.Int64{}, Remaining: map[string]*atomic.Int64{}}
	for _, kind := range []string{PurgeGeneration, PurgeVersion} {
		m.Purges[kind], m.Objects[kind] = &atomic.Int64{}, &atomic.Int64{}
		m.Failures[kind], m.Oldest[kind], m.Remaining[kind] = &atomic.Int64{}, &atomic.Int64{}, &atomic.Int64{}
	}
	return m
}

// Write renders the purge counters in the Prometheus text format.
func (m *PurgeMetrics) Write(w io.Writer) {
	for _, family := range []struct {
		name, help, metricType string
		counts                 map[string]*atomic.Int64
	}{
		{"quivr_projection_purges_total", "Completed projection purges by kind (abandoned generation or dead Version).", "counter", m.Purges},
		{"quivr_projection_purged_objects_total", "Projection objects physically deleted by purges, by kind.", "counter", m.Objects},
		{"quivr_projection_purge_failures_total", "Failed projection purge batches by kind.", "counter", m.Failures},
		{"quivr_projection_purge_oldest_observed_age_seconds", "Oldest unfinished purge age among items observed in the last nonempty sweep.", "gauge", m.Oldest},
		{"quivr_projection_purge_remaining_objects_lower_bound", "Remaining object lower bound among unfinished items observed in the last nonempty sweep; not a backlog total.", "gauge", m.Remaining},
	} {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", family.name, family.help, family.name, family.metricType)
		for _, kind := range []string{PurgeGeneration, PurgeVersion} {
			fmt.Fprintf(w, "%s{kind=%q} %d\n", family.name, kind, family.counts[kind].Load())
		}
	}
}

// Purger physically deletes the projection objects of abandoned generations
// and dead Versions. Each run is bounded; every step is idempotent, so an
// interrupted or concurrent run only repeats deletes that match nothing.
type Purger struct {
	Store      PurgeStore
	Projection PurgeProjection
	Grace      time.Duration
	Interval   time.Duration
	Batch      int
	Metrics    *PurgeMetrics
	// RequestTimeout reserves enough sweep time for selection and one delete.
	RequestTimeout time.Duration
}

// Run sweeps until ctx ends.
func (p Purger) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	for ctx.Err() == nil {
		work, admitted := lifecycle.Admit(ctx)
		if !admitted {
			return
		}
		if _, err := p.Sweep(work); err != nil && ctx.Err() == nil {
			slog.Warn("projection purge sweep failed; retrying next interval", "reason", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// Sweep notices newly dead items, then purges at most Batch items whose grace
// period elapsed. It reports how many items it completed.
func (p Purger) Sweep(ctx context.Context) (int, error) {
	requestTimeout := p.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = DefaultPurgeTimeout
	}
	requestTimeout = min(requestTimeout, MaximumPurgeTimeout)
	// One shared deadline covers notice, all leased items/collections and requests.
	// At the maximum: 255s work + one 5s cleanup < the 300s item lease.
	ctx, cancel := context.WithTimeout(ctx, 2*requestTimeout+15*time.Second)
	defer cancel()
	batch, grace := p.Batch, p.Grace
	if batch <= 0 {
		batch = 100
	}
	if grace <= 0 {
		grace = DefaultPurgeGrace
	}
	if _, err := p.Store.NoticePurges(ctx, batch); err != nil {
		return 0, err
	}
	items, err := p.Store.ClaimPurges(ctx, grace, 5*time.Minute, batch)
	if err != nil {
		return 0, err
	}
	if p.Metrics != nil && len(items) > 0 {
		for _, kind := range []string{PurgeGeneration, PurgeVersion} {
			p.Metrics.Oldest[kind].Store(0)
			p.Metrics.Remaining[kind].Store(0)
		}
	}
	completed := 0
	var failure error
	for _, item := range items {
		if ctx.Err() != nil {
			return completed, errors.Join(failure, ctx.Err())
		}
		deleted, complete, remaining := 0, true, 0
		for _, collection := range item.Collections {
			if err = ctx.Err(); err != nil {
				break
			}
			var r PurgeResult
			if item.Kind == PurgeGeneration {
				r, err = p.Projection.PurgeGeneration(ctx, collection, item.Organization, item.CorpusID, item.GenerationID)
			} else {
				r, err = p.Projection.PurgeVersion(ctx, collection, item.Organization, item.VersionID)
			}
			deleted += r.Deleted
			remaining += r.RemainingAtLeast
			if err != nil {
				break
			}
			complete = complete && r.Complete
		}
		if err != nil {
			complete = false
			if p.Metrics != nil {
				p.Metrics.Failures[item.Kind].Add(1)
			}
			failure = errors.Join(failure, err)
			slog.Warn("projection purge batch failed", "kind", item.Kind, "reason", err.Error(), "objects_deleted", deleted)
		}
		// Save confirmed progress even on partial failure/cancellation. The bounded
		// cleanup context retains the managed process shutdown budget.
		checkpoint, finish := lifecycle.CleanupContext(ctx, 5*time.Second)
		recordErr := p.Store.RecordPurge(checkpoint, item, deleted, complete)
		finish()
		if recordErr != nil {
			return completed, errors.Join(failure, recordErr)
		}
		if p.Metrics != nil && !complete {
			if !item.NoticedAt.IsZero() {
				age := max(int64(0), int64(time.Since(item.NoticedAt).Seconds()))
				if age > p.Metrics.Oldest[item.Kind].Load() {
					p.Metrics.Oldest[item.Kind].Store(age)
				}
			}
			p.Metrics.Remaining[item.Kind].Add(int64(remaining))
		}
		if p.Metrics != nil {
			p.Metrics.Objects[item.Kind].Add(int64(deleted))
			if complete {
				p.Metrics.Purges[item.Kind].Add(1)
			}
		}
		if complete {
			completed++
		}
		// One final checkpoint may outlive the attempt deadline; do not renew
		// cleanup budgets or contact further collections/items after expiry.
		if ctx.Err() != nil {
			return completed, errors.Join(failure, ctx.Err())
		}
	}
	return completed, failure
}
