package app

import (
	"cmp"
	"context"
	"errors"
	"io"
	"maps"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/observability"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

func TestAPIMetricsExposeCommandsAndIngestionBacklog(t *testing.T) {
	commands := telemetry.NewCommands()
	commands.Accepted(telemetry.CommandRecord, 2)
	serve := func(backlog func(context.Context) (int64, time.Duration, error)) string {
		rec := httptest.NewRecorder()
		apiMetrics(commands, backlog, func(io.Writer) {}).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
		if rec.Header().Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
			t.Fatal(rec.Header())
		}
		return rec.Body.String()
	}
	text := serve(func(context.Context) (int64, time.Duration, error) { return 3, 90 * time.Second, nil })
	for _, want := range []string{`quivr_commands_accepted_total{command="record"} 2`, "quivr_ingestion_pending 3", "quivr_ingestion_oldest_pending_age_seconds 90"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
	// A backlog failure keeps the counters, omits the gauges and never leaks the error.
	text = serve(func(context.Context) (int64, time.Duration, error) { return 0, 0, errors.New("password=hunter2") })
	if !strings.Contains(text, "quivr_commands_accepted_total") || strings.Contains(text, "quivr_ingestion_pending ") || strings.Contains(text, "hunter2") {
		t.Fatal(text)
	}
}

type fakeSteps struct {
	steps content.Steps
	// age is the time since acceptance at the read; 2 s by default.
	age time.Duration
	err error
}

func (f fakeSteps) ReceiptSteps(context.Context, string, string) (content.Steps, time.Duration, error) {
	return f.steps, cmp.Or(f.age, 2*time.Second), f.err
}

type rollups struct{ rows []observability.Row }

func (r *rollups) UpsertRollups(_ context.Context, rows []observability.Row) error {
	r.rows = append(r.rows, rows...)
	return nil
}
func (*rollups) PruneRollups(context.Context, time.Duration, time.Time) (int64, error) { return 0, nil }
func (*rollups) ReadRollups(context.Context, string, string, time.Duration, time.Time, ...string) ([]observability.Row, error) {
	return nil, nil
}
func (*rollups) TopKeys(context.Context, string, string, time.Duration, time.Time, int) (observability.Ranking, error) {
	return observability.Ranking{}, nil
}

// The worker records each pipeline step once, when the stage that finishes
// it succeeds, timed from the step that causes it, so the wait counts.
func TestProcessingObserverRecordsPipelineSteps(t *testing.T) {
	at := func(s int) *time.Time { v := time.Date(2026, 9, 30, 12, 0, s, 0, time.UTC); return &v }
	// Received at 0 s, materialized at 3 s, cut at 4 s, searchable at 6 s,
	// vectors at 16 s; the enrichment is not finished at the baseline.
	baseline := content.Steps{Accepted: at(0), Materialized: at(3), Segmented: at(4), RetrievalReady: at(6)}
	enriched := baseline
	enriched.Enriched = at(16)
	m, store := telemetry.NewProcessing(), &rollups{}
	recorder := observability.NewRecorder(store, observability.Config{}, false)
	o := processingObserver{metrics: m, store: fakeSteps{steps: baseline}, steps: recorder}
	o.Outcome("org", telemetry.StageBaseline, telemetry.OutcomeSucceeded, "", time.Second)
	o.Searchable(context.Background(), "org", "receipt")
	// Read 1 s after the vectors, by a stage that ran 2 s.
	processingObserver{metrics: m, store: fakeSteps{steps: enriched, age: 17 * time.Second}, steps: recorder}.Enriched(context.Background(), "org", "receipt", 2*time.Second)
	// A re-run on a Version enriched long before its 2 s run counts nothing.
	processingObserver{metrics: m, store: fakeSteps{steps: enriched, age: time.Minute}, steps: recorder}.Enriched(context.Background(), "org", "receipt", 2*time.Second)
	// A failed read records nothing rather than guessing.
	failed := processingObserver{metrics: m, store: fakeSteps{err: errors.New("gone")}, steps: recorder}
	failed.Searchable(context.Background(), "org", "receipt")
	failed.Enriched(context.Background(), "org", "receipt", time.Minute)
	if err := recorder.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, row := range store.rows {
		if row.Series == observability.SeriesStep && row.Resolution == observability.Tiers[0].Resolution {
			got[row.Key] += row.DurationSumMS
		}
	}
	want := map[string]float64{content.StepMaterialized: 3000, content.StepSegmented: 1000, content.StepRetrievalReady: 2000, content.StepEnriched: 10000, StepSearchable: 2000, telemetry.StageBaseline: 1000}
	if !maps.Equal(got, want) {
		t.Fatalf("step durations (ms) = %v, want %v", got, want)
	}
	var b strings.Builder
	m.Write(&b)
	if !strings.Contains(b.String(), "quivr_acceptance_to_searchable_seconds_count 1") || !strings.Contains(b.String(), `quivr_processing_outcomes_total{stage="baseline",outcome="succeeded"} 1`) {
		t.Fatal(b.String())
	}
}
