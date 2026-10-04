package app

import (
	"context"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/observability"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

// processingObserver feeds the worker's processing metrics (THE-662) and
// the step rollups (THE-795, THE-797).
type processingObserver struct {
	metrics *telemetry.Processing
	store   interface {
		ReceiptSteps(context.Context, string, string) (content.Steps, time.Duration, error)
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
// acceptance time, and records the pipeline steps the baseline finished:
// materialized, segmented and retrieval_ready. A failed read skips the
// observation rather than guessing.
func (o processingObserver) Searchable(ctx context.Context, org, receiptID string) {
	steps, age, ok := o.read(ctx, org, receiptID)
	if !ok {
		return
	}
	o.metrics.Searchable(age)
	o.steps.Step(org, StepSearchable, age, "")
	o.record(org, steps, content.StepMaterialized, content.StepSegmented, content.StepRetrievalReady)
}

// Enriched records the enriched step once the Version has its vectors, only
// when the stage that just ran for `ran` set it: a re-run on a Version
// enriched earlier would count the old duration again. Both ages are read
// on the database clock, so a skewed worker clock does not matter.
func (o processingObserver) Enriched(ctx context.Context, org, receiptID string, ran time.Duration) {
	steps, age, ok := o.read(ctx, org, receiptID)
	if !ok || steps.Enriched == nil || steps.Accepted == nil {
		return
	}
	if since := age - steps.Enriched.Sub(*steps.Accepted); since <= ran+time.Second {
		o.record(org, steps, content.StepEnriched)
	}
}

func (o processingObserver) read(ctx context.Context, org, receiptID string) (content.Steps, time.Duration, bool) {
	read, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	steps, age, err := o.store.ReceiptSteps(read, org, receiptID)
	return steps, age, err == nil
}

// record adds each named step to the step rollups, timed like the document
// timeline: from the step that causes it, so the time the Version waited for
// the step counts. A step missing either time is skipped.
func (o processingObserver) record(org string, steps content.Steps, names ...string) {
	for _, s := range content.Timeline(content.Activity{Steps: steps}) {
		if s.Since != "" && slices.Contains(names, s.Step) {
			o.steps.Step(org, s.Step, s.Duration, "")
		}
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
