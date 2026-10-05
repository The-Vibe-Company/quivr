package observability

import (
	"cmp"
	"context"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
)

// Store persists rollup rows.
type Store interface {
	// UpsertRollups adds each row to the stored row of the same bucket,
	// creating it when missing.
	UpsertRollups(ctx context.Context, rows []Row) error
	// PruneRollups deletes the rows of one resolution that start before
	// before, and returns how many it deleted.
	PruneRollups(ctx context.Context, resolution time.Duration, before time.Time) (int64, error)
	// ReadRollups lists the rows of one Organization, series and resolution
	// that start at or after from, ordered by key then start; only the rows
	// of keys when any are given.
	ReadRollups(ctx context.Context, org, series string, resolution time.Duration, from time.Time, keys ...string) ([]Row, error)
	// TopKeys sums the counts of one series per key over the rows that start
	// at or after from: the limit largest, and the total over every key.
	TopKeys(ctx context.Context, org, series string, resolution time.Duration, from time.Time, limit int) (Ranking, error)
}

// KeyCount is a key and its summed count.
type KeyCount struct {
	Key   string
	Count int64
}

// Ranking is the largest keys of a series over a window, largest first,
// with the count and number of every key, listed or not.
type Ranking struct {
	Keys     []KeyCount
	Total    int64
	Distinct int
}

// Config is the observability part of the engine configuration.
type Config struct {
	// RecordQueryText counts searches by their normalized query text so the
	// most frequent queries can be listed. Off by default: queries can hold
	// personal or confidential text, and when on it is stored for 7 days.
	RecordQueryText bool `json:"record_query_text"`
	// FlushInterval is how often a process writes its counts (Go duration,
	// default 5s). A process that crashes loses at most this much.
	FlushInterval string `json:"flush_interval"`
}

// DefaultFlushInterval is how often a process writes its counts by default.
const DefaultFlushInterval = 5 * time.Second

// Interval parses FlushInterval.
func (c Config) Interval() (time.Duration, bool) {
	if c.FlushInterval == "" {
		return DefaultFlushInterval, true
	}
	d, err := time.ParseDuration(c.FlushInterval)
	return d, err == nil && d > 0
}

// Bounds of the in-memory buffer: past them new events are dropped and
// counted, so an unreachable database never grows a process without limit.
// Query text and source namespaces come from clients, so each of those
// series also takes at most maxPendingKeys new keys between two flushes.
const (
	maxPendingRows = 50000
	maxPendingKeys = 1000
)

// clientKeyed reports a series whose keys come from clients.
func clientKeyed(series string) bool {
	return series == SeriesSearchQuery || series == SeriesReceived
}

// PluginCall is one Contribution invocation.
type PluginCall struct {
	Organization, Plugin, Version, Operation string
	Duration                                 time.Duration
	// ErrorCode is empty when the call succeeded.
	ErrorCode string
}

// Search is one public search.
type Search struct {
	Organization, Mode, Profile string
	// Query is recorded only when the deployment enables record_query_text.
	Query    string
	Results  int
	Duration time.Duration
	// ErrorCode is empty when the search succeeded.
	ErrorCode string
	// OverObjective reports a search that took longer than its profile's
	// latency objective, max_latency_ms.
	OverObjective bool
}

// Recorder aggregates events in memory and flushes them to its Store. The
// hot path only takes a mutex; PostgreSQL is written by Run. A nil Recorder
// ignores every event.
type Recorder struct {
	store           Store
	interval        time.Duration
	recordQueryText bool
	// prune also deletes expired rows; only the worker prunes.
	prune bool

	mu      sync.Mutex
	pending map[ID]*Row
	// keys holds the keys of client-keyed series buffered since the last
	// flush, per Organization and series, so each takes at most
	// maxPendingKeys and one Organization never uses up another's.
	keys map[[2]string]map[string]struct{}

	metrics  *metrics
	dropped  atomic.Int64
	failures atomic.Int64
}

// NewRecorder builds a process's recorder. prune is true in the one process
// that deletes expired rows.
func NewRecorder(store Store, cfg Config, prune bool) *Recorder {
	interval, ok := cfg.Interval()
	if !ok {
		interval = DefaultFlushInterval
	}
	return &Recorder{store: store, interval: interval, recordQueryText: cfg.RecordQueryText, prune: prune, pending: map[ID]*Row{}, keys: map[[2]string]map[string]struct{}{}, metrics: newMetrics()}
}

// RecordsQueryText reports whether query text is recorded.
func (r *Recorder) RecordsQueryText() bool { return r != nil && r.recordQueryText }

// PluginCall records one Contribution invocation.
func (r *Recorder) PluginCall(c PluginCall) {
	if r == nil || c.Organization == "" {
		return
	}
	r.metrics.pluginCall(c)
	r.add(c.Organization, SeriesPluginCall, Key(c.Plugin, c.Version, c.Operation), Tiers, c.Duration, 0, 0, c.ErrorCode)
}

// Search records one public search and, when enabled, its query text.
func (r *Recorder) Search(s Search) {
	if r == nil || s.Organization == "" {
		return
	}
	r.metrics.search(s)
	var over int64
	if s.OverObjective {
		over = 1
	}
	r.add(s.Organization, SeriesSearch, Key(s.Mode, s.Profile), Tiers, s.Duration, int64(max(s.Results, 0)), over, s.ErrorCode)
	if q := NormalizeQuery(s.Query); r.recordQueryText && q != "" {
		r.add(s.Organization, SeriesSearchQuery, q, []Tier{QueryTier}, s.Duration, 0, 0, s.ErrorCode)
	}
}

// Received records one document received from a source namespace: a
// command that reserved a new revision of a Record.
func (r *Recorder) Received(org, sourceNamespace string) {
	if r == nil || org == "" || sourceNamespace == "" {
		return
	}
	r.add(org, SeriesReceived, sourceNamespace, Tiers, 0, 0, 0, "")
}

// Matched records one Match committed by a Subscription whose evaluator is
// the plugin evaluator.
func (r *Recorder) Matched(org, evaluator string) {
	if r == nil || org == "" || evaluator == "" {
		return
	}
	r.metrics.match(evaluator)
	r.add(org, SeriesMatch, evaluator, Tiers, 0, 0, 0, "")
}

// Step records one processing step of one Organization.
func (r *Recorder) Step(org, step string, d time.Duration, errorCode string) {
	if r == nil || org == "" {
		return
	}
	r.add(org, SeriesStep, step, Tiers, d, 0, 0, errorCode)
}

func (r *Recorder) add(org, series, key string, tiers []Tier, d time.Duration, items, overObjective int64, errorCode string) {
	now := time.Now().UTC()
	ms := durationMS(d)
	event := Row{Organization: org, Series: series, Key: key, Count: 1, Items: items, OverObjective: overObjective, DurationSumMS: ms}
	event.Buckets[bucketOf(ms)] = 1
	if errorCode != "" {
		event.Errors, event.LastErrorCode, event.LastErrorAt = 1, errorCode, now
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if clientKeyed(series) {
		// A key already buffered keeps counting, across bucket boundaries too.
		scope := [2]string{org, series}
		seen := r.keys[scope]
		if _, ok := seen[key]; !ok {
			if len(seen) >= maxPendingKeys {
				r.dropped.Add(1)
				return
			}
			if seen == nil {
				seen = map[string]struct{}{}
				r.keys[scope] = seen
			}
			seen[key] = struct{}{}
		}
	}
	for _, tier := range tiers {
		event.Resolution, event.Start = tier.Resolution, now.Truncate(tier.Resolution)
		id := event.ID()
		if row, ok := r.pending[id]; ok {
			row.merge(event)
			continue
		}
		if len(r.pending) >= maxPendingRows {
			r.dropped.Add(1)
			continue
		}
		row := event
		r.pending[id] = &row
	}
}

// Flush writes the buffered rows in one batch. Rows a failed write could not
// store are merged back and retried at the next flush.
func (r *Recorder) Flush(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	pending := r.pending
	r.pending, r.keys = map[ID]*Row{}, map[[2]string]map[string]struct{}{}
	r.mu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	rows := make([]Row, 0, len(pending))
	for _, row := range pending {
		rows = append(rows, *row)
	}
	// One lock order for every process: concurrent flushes never deadlock.
	slices.SortFunc(rows, func(a, b Row) int {
		return cmp.Or(cmp.Compare(a.Organization, b.Organization), cmp.Compare(a.Series, b.Series), cmp.Compare(a.Resolution, b.Resolution),
			a.Start.Compare(b.Start), cmp.Compare(a.Key, b.Key))
	})
	err := r.store.UpsertRollups(ctx, rows)
	if err != nil {
		r.failures.Add(1)
		r.mu.Lock()
		for _, row := range rows {
			if existing, ok := r.pending[row.ID()]; ok {
				existing.merge(row)
			} else if len(r.pending) < maxPendingRows {
				copied := row
				r.pending[row.ID()] = &copied
			} else {
				r.dropped.Add(row.Count)
			}
		}
		r.mu.Unlock()
	}
	return err
}

// Prune deletes the rows of every tier older than its retention.
func (r *Recorder) Prune(ctx context.Context) (int64, error) {
	var total int64
	now := time.Now().UTC()
	for _, tier := range append(slices.Clone(Tiers), QueryTier) {
		n, err := r.store.PruneRollups(ctx, tier.Resolution, now.Add(-tier.Retention))
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// Run flushes every interval until ctx ends, then flushes once more. The
// pruning recorder also deletes expired rows once a minute.
func (r *Recorder) Run(ctx context.Context) {
	if r == nil {
		return
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	var pruned time.Time
	for {
		select {
		case <-ctx.Done():
			final, cancel := lifecycle.CleanupContext(ctx, 2*time.Second)
			if err := r.Flush(final); err != nil {
				slog.Warn("observability flush failed at shutdown", "component", "observability", "error", err.Error())
			}
			cancel()
			return
		case <-ticker.C:
			if err := r.Flush(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("observability flush failed; retrying at the next flush", "component", "observability", "error", err.Error())
			}
			if r.prune && time.Since(pruned) >= time.Minute {
				pruned = time.Now()
				if n, err := r.Prune(ctx); err != nil && ctx.Err() == nil {
					slog.Warn("observability prune failed", "component", "observability", "error", err.Error())
				} else if n > 0 {
					slog.Info("observability rollups pruned", "component", "observability", "rows", n)
				}
			}
		}
	}
}

// WriteMetrics renders the plugin call and search metrics in the Prometheus
// text format.
func (r *Recorder) WriteMetrics(w io.Writer) {
	if r == nil {
		return
	}
	r.metrics.write(w)
	writeCounter(w, "quivr_observability_flush_failures_total", "Rollup flushes that failed and were retried.", r.failures.Load())
	writeCounter(w, "quivr_observability_dropped_events_total", "Events dropped because the rollup buffer was full.", r.dropped.Load())
}
