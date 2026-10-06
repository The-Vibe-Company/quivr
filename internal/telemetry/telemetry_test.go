package telemetry_test

import (
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

func render(w func(*strings.Builder)) string {
	var b strings.Builder
	w(&b)
	return b.String()
}

func TestCountersKeepOnlyDeclaredLabelValues(t *testing.T) {
	c := telemetry.NewCommands()
	c.Accepted(telemetry.CommandRecord, 1)
	c.Accepted(telemetry.CommandBatchEntry, 3)
	c.Accepted("receipt_0123456789abcdef", 1) // an identifier never becomes a label
	c.Accepted(telemetry.CommandWithdrawal, 0)
	var zero telemetry.Commands
	zero.Accepted(telemetry.CommandRecord, 1) // the zero value ignores observations
	text := render(func(b *strings.Builder) { c.Write(b) })
	for _, want := range []string{
		"# TYPE quivr_commands_accepted_total counter",
		`quivr_commands_accepted_total{command="record"} 1`,
		`quivr_commands_accepted_total{command="batch_entry"} 3`,
		`quivr_commands_accepted_total{command="withdrawal"} 0`,
		`quivr_commands_accepted_total{command="upload_confirm"} 0`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, "receipt_") {
		t.Fatal("unbounded label value exposed", text)
	}
}

func TestProcessingOutcomesAndSearchableHistogram(t *testing.T) {
	p := telemetry.NewProcessing()
	p.Outcome(telemetry.StageBaseline, telemetry.OutcomeSucceeded)
	p.Outcome(telemetry.StageEnrichment, telemetry.OutcomeRetrying)
	p.Outcome(telemetry.StageEnrichment, telemetry.OutcomeRetrying)
	p.Outcome("secret-stage", telemetry.OutcomeSucceeded)
	p.Searchable(300 * time.Millisecond)
	p.Searchable(4 * time.Second)
	p.Searchable(-time.Second) // ignored
	var nilProcessing *telemetry.Processing
	nilProcessing.Outcome(telemetry.StageBaseline, telemetry.OutcomeBlocked)
	nilProcessing.Searchable(time.Second)
	text := render(func(b *strings.Builder) { p.Write(b) })
	for _, want := range []string{
		`quivr_processing_outcomes_total{stage="baseline",outcome="succeeded"} 1`,
		`quivr_processing_outcomes_total{stage="enrichment",outcome="retrying"} 2`,
		`quivr_processing_outcomes_total{stage="baseline",outcome="blocked"} 0`,
		"# TYPE quivr_acceptance_to_searchable_seconds histogram",
		`quivr_acceptance_to_searchable_seconds_bucket{le="0.25"} 0`,
		`quivr_acceptance_to_searchable_seconds_bucket{le="0.5"} 1`,
		`quivr_acceptance_to_searchable_seconds_bucket{le="5"} 2`,
		`quivr_acceptance_to_searchable_seconds_bucket{le="+Inf"} 2`,
		"quivr_acceptance_to_searchable_seconds_sum 4.3",
		"quivr_acceptance_to_searchable_seconds_count 2",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, "secret-stage") {
		t.Fatal("undeclared stage exposed", text)
	}
}

func TestGauge(t *testing.T) {
	text := render(func(b *strings.Builder) { telemetry.Gauge(b, "quivr_x", "An example.", 1.5) })
	if text != "# HELP quivr_x An example.\n# TYPE quivr_x gauge\nquivr_x 1.5\n" {
		t.Fatalf("%q", text)
	}
}

func TestHistogramWithoutBoundariesKeepsHistogramExposition(t *testing.T) {
	h := telemetry.NewHistogram("quivr_empty_bounds", "No finite buckets.")
	h.ObserveValue(2)
	text := render(func(b *strings.Builder) { h.Write(b) })
	for _, want := range []string{"# TYPE quivr_empty_bounds histogram", "quivr_empty_bounds_bucket{le=\"+Inf\"} 1", "quivr_empty_bounds_sum 2", "quivr_empty_bounds_count 1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}
