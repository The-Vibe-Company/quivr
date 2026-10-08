// Package workqueue owns deployment workload classes and queue observations.
package workqueue

import (
	"context"
	"fmt"
	"slices"
)

const Live = "live"
const Bulk = "bulk"

type Slots struct {
	Live int `json:"live"`
	Bulk int `json:"bulk"`
}
type Config struct {
	Queues []string `json:"queues"`
	Slots  Slots    `json:"slots"`
}

func (c Config) Resolve() (Config, error) {
	if c.Queues == nil {
		c.Queues = []string{Live, Bulk}
	}
	if len(c.Queues) == 0 {
		return c, fmt.Errorf("worker.queues must select live, bulk, or both")
	}
	seen := map[string]bool{}
	for _, q := range c.Queues {
		if !Valid(q) || seen[q] {
			return c, fmt.Errorf("worker.queues contains unknown or duplicate queue %q", q)
		}
		seen[q] = true
	}
	for _, n := range []int{c.Slots.Live, c.Slots.Bulk} {
		if n < 0 || n > 1024 {
			return c, fmt.Errorf("worker.slots must be between 1 and 1024 (zero selects four)")
		}
	}
	if c.Slots.Live == 0 {
		c.Slots.Live = 4
	}
	if c.Slots.Bulk == 0 {
		c.Slots.Bulk = 4
	}
	return c, nil
}
func Valid(q string) bool             { return q == Live || q == Bulk }
func (c Config) Serves(q string) bool { return slices.Contains(c.Queues, q) }
func (c Config) Capacity(q string) int {
	if q == Bulk {
		return c.Slots.Bulk
	}
	return c.Slots.Live
}
func TaskQueue(q string) string { return "quivr-" + q + "-v1" }

type classKey struct{}

func WithClass(ctx context.Context, q string) context.Context {
	return context.WithValue(ctx, classKey{}, q)
}
func Class(ctx context.Context) string {
	if q, _ := ctx.Value(classKey{}).(string); Valid(q) {
		return q
	}
	return Live
}

// Status observes document work in a class. Document age uses durable admission
// time; estimated operation age uses operation creation time.
// Rebuild/backfill waiting terms estimate remaining scope from operation counters.
type Status struct {
	Queue   string `json:"-"`
	Waiting int64  `json:"waiting"`
	// IngestionWaiting excludes operations and post-baseline work. Nil means
	// the snapshot writer has not published this split yet.
	IngestionWaiting *int64  `json:"-"`
	InProgress       int64   `json:"in_progress"`
	OldestAgeSeconds float64 `json:"oldest_waiting_age_seconds"`
}
type Reader interface {
	QueueBacklog(context.Context) ([]Status, error)
}

// Selected distinguishes unclassified legacy callers from explicit queue claims.
func Selected(ctx context.Context) (string, bool) {
	q, _ := ctx.Value(classKey{}).(string)
	return q, Valid(q)
}
