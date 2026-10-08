package retrieval

import (
	"context"

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

// PurgeItem is one claimed purge. Collections name the physical collections
// its objects may live in.
type PurgeItem struct {
	Organization string
	Kind         string
	CorpusID     string
	GenerationID string
	VersionID    string
	Collections  []string
}

// PurgeResult is the outcome of one bounded delete-by-filter. Complete means
// nothing matching the filter remains.
type PurgeResult struct {
	Deleted  int
	Complete bool
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
	RecordPurge(ctx context.Context, item PurgeItem, deleted int, complete bool, limit int) (bool, error)
}

// PurgeProjection deletes projection objects by filter, never by object ID.
type PurgeProjection interface {
	PurgeGeneration(ctx context.Context, collection, org, corpusID, generationID string) (PurgeResult, error)
	PurgeVersion(ctx context.Context, collection, org, versionID string) (PurgeResult, error)
}

// PurgeMetrics counts completed purges and deleted objects per kind.
type PurgeMetrics struct {
	Purges, Objects map[string]*atomic.Int64
}

// NewPurgeMetrics returns zeroed counters for both purge kinds.
func NewPurgeMetrics() *PurgeMetrics {
	m := &PurgeMetrics{Purges: map[string]*atomic.Int64{}, Objects: map[string]*atomic.Int64{}}
	for _, kind := range []string{PurgeGeneration, PurgeVersion} {
		m.Purges[kind], m.Objects[kind] = &atomic.Int64{}, &atomic.Int64{}
	}
	return m
}

// Write renders the purge counters in the Prometheus text format.
func (m *PurgeMetrics) Write(w io.Writer) {
	for _, family := range []struct {
		name, help string
		counts     map[string]*atomic.Int64
	}{
		{"quivr_projection_purges_total", "Completed projection purges by kind (abandoned generation or dead Version).", m.Purges},
		{"quivr_projection_purged_objects_total", "Projection objects physically deleted by purges, by kind.", m.Objects},
	} {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", family.name, family.help, family.name)
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
			slog.Warn("projection purge sweep failed; retrying next interval", "error", err)
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
	completed := 0
	var failure error
	for _, item := range items {
		deleted, complete := 0, true
		for _, collection := range item.Collections {
			var r PurgeResult
			if item.Kind == PurgeGeneration {
				r, err = p.Projection.PurgeGeneration(ctx, collection, item.Organization, item.CorpusID, item.GenerationID)
			} else {
				r, err = p.Projection.PurgeVersion(ctx, collection, item.Organization, item.VersionID)
			}
			deleted += r.Deleted
			if err != nil {
				break
			}
			complete = complete && r.Complete
		}
		if err != nil {
			// The item stays unrecorded; its lease expires and a later run
			// repeats the idempotent delete. Other items still proceed.
			if p.Metrics != nil {
				p.Metrics.Objects[item.Kind].Add(int64(deleted))
			}
			if failure == nil {
				failure = err
			}
			if ctx.Err() != nil {
				return completed, failure
			}
			continue
		}
		// An incomplete item (more objects than one delete may match) is
		// released and continues next run.
		if complete, err = p.Store.RecordPurge(ctx, item, deleted, complete, batch); err != nil {
			return completed, err
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
	}
	return completed, failure
}
