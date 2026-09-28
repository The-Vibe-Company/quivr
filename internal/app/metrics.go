package app

import (
	"context"
	"net/http"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/telemetry"
)

// processingObserver feeds the worker's processing metrics (THE-662).
type processingObserver struct {
	metrics *telemetry.Processing
	store   interface {
		ReceiptAge(context.Context, string, string) (time.Duration, error)
	}
}

func (o processingObserver) Outcome(stage, outcome string) { o.metrics.Outcome(stage, outcome) }

// Searchable measures acceptance to searchable from the Receipt's durable
// acceptance time; a failed read skips the observation rather than guessing.
func (o processingObserver) Searchable(ctx context.Context, org, receiptID string) {
	read, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if age, err := o.store.ReceiptAge(read, org, receiptID); err == nil {
		o.metrics.Searchable(age)
	}
}

// apiMetrics serves accepted-command counters and the ingestion backlog gauges.
// A backlog read failure omits the gauges rather than reporting zero.
func apiMetrics(commands telemetry.Commands, backlog func(context.Context) (int64, time.Duration, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		commands.Write(w)
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		pending, age, err := backlog(ctx)
		if err != nil {
			return
		}
		telemetry.Gauge(w, "quivr_ingestion_pending", "Receipts accepted but not yet materialized.", float64(pending))
		telemetry.Gauge(w, "quivr_ingestion_oldest_pending_age_seconds", "Age of the oldest Receipt accepted but not yet materialized.", age.Seconds())
	})
}
