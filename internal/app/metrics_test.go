package app

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/telemetry"
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

type fakeAges struct{ err error }

func (f fakeAges) ReceiptAge(context.Context, string, string) (time.Duration, error) {
	return 2 * time.Second, f.err
}

func TestProcessingObserverMeasuresFromAcceptance(t *testing.T) {
	m := telemetry.NewProcessing()
	o := processingObserver{metrics: m, store: fakeAges{}}
	o.Outcome("org", telemetry.StageBaseline, telemetry.OutcomeSucceeded, "", time.Second)
	o.Searchable(context.Background(), "org", "receipt")
	processingObserver{metrics: m, store: fakeAges{err: errors.New("gone")}}.Searchable(context.Background(), "org", "receipt")
	var b strings.Builder
	m.Write(&b)
	if !strings.Contains(b.String(), "quivr_acceptance_to_searchable_seconds_count 1") || !strings.Contains(b.String(), `quivr_processing_outcomes_total{stage="baseline",outcome="succeeded"} 1`) {
		t.Fatal(b.String())
	}
}
