package app

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/observability"
	"github.com/The-Vibe-Company/quivr-v2/internal/telemetry"
)

// processingObserver feeds the worker's processing metrics (THE-662) and
// the step rollups (THE-795).
type processingObserver struct {
	metrics *telemetry.Processing
	store   interface {
		ReceiptAge(context.Context, string, string) (time.Duration, error)
	}
	steps *observability.Recorder
}

// StepSearchable is the step from a Receipt's acceptance to its Version being
// searchable by keyword.
const StepSearchable = "accepted_to_searchable"

// Outcome counts the stage outcome and records the stage as a step; any
// outcome but succeeded is an error carrying its code.
func (o processingObserver) Outcome(org, stage, outcome, code string, d time.Duration) {
	o.metrics.Outcome(stage, outcome)
	if outcome == telemetry.OutcomeSucceeded {
		code = ""
	} else if code == "" {
		code = outcome
	}
	o.steps.Step(org, stage, d, code)
}

// Searchable measures acceptance to searchable from the Receipt's durable
// acceptance time; a failed read skips the observation rather than guessing.
func (o processingObserver) Searchable(ctx context.Context, org, receiptID string) {
	read, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if age, err := o.store.ReceiptAge(read, org, receiptID); err == nil {
		o.metrics.Searchable(age)
		o.steps.Step(org, StepSearchable, age, "")
	}
}

// apiMetrics serves accepted-command counters and the ingestion backlog gauges.
// A backlog read failure omits the gauges rather than reporting zero.
func apiMetrics(commands telemetry.Commands, backlog func(context.Context) (int64, time.Duration, error), extra func(io.Writer)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		commands.Write(w)
		extra(w)
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
