package monitoring_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type observingEvaluation struct {
	*fakeEvaluation
	commits []trace.SpanContext
	retries []trace.SpanContext
}

func (s *observingEvaluation) CommitMatch(ctx context.Context, in monitoring.Intent, evidence monitoring.MatchEvidence) (string, error) {
	s.commits = append(s.commits, trace.SpanContextFromContext(ctx))
	return s.fakeEvaluation.CommitMatch(ctx, in, evidence)
}
func (s *observingEvaluation) CommitNoMatch(ctx context.Context, in monitoring.Intent) (string, error) {
	s.commits = append(s.commits, trace.SpanContextFromContext(ctx))
	return s.fakeEvaluation.CommitNoMatch(ctx, in)
}

func (s *observingEvaluation) Retry(ctx context.Context, in monitoring.Intent, code string, delay time.Duration) error {
	s.retries = append(s.retries, trace.SpanContextFromContext(ctx))
	return s.fakeEvaluation.Retry(ctx, in, code, delay)
}

type failingBatchEvaluation struct{ *observingEvaluation }

func (s *failingBatchEvaluation) CommitMatches(ctx context.Context, group []monitoring.MatchCommit) ([]string, error) {
	for range group {
		s.commits = append(s.commits, trace.SpanContextFromContext(ctx))
	}
	return nil, errors.New("storage unavailable")
}

// Owns operation context at the engine/store boundary: the store only observes
// contexts, while the real SDK exports spans produced by the engine. SQL span
// instrumentation and durable storage are owned by their adapter tests.
func TestEvaluationTraceOwnsCommitsAndFailureStatus(t *testing.T) {
	for _, scenario := range []string{"shared-parent", "different-parent", "evaluation-error", "batch-storage-error"} {
		t.Run(scenario, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			old := otel.GetTracerProvider()
			otel.SetTracerProvider(provider)
			t.Cleanup(func() { otel.SetTracerProvider(old); _ = provider.Shutdown(context.Background()) })
			evaluator := &phraseEvaluator{max: 32}
			if scenario == "evaluation-error" {
				evaluator.err = monitoring.ErrEvaluation
			}
			texts := []string{"strike", "election"}
			if scenario == "batch-storage-error" {
				texts[1] = "strike"
			}
			store, engine, _ := batchScenario(evaluator, texts...)
			encode := func(id string) string {
				return telemetry.Encode(telemetry.Extract(context.Background(), http.Header{"Traceparent": {"00-" + id + "-2222222222222222-01"}}))
			}
			firstID, secondID := "11111111111111111111111111111111", "11111111111111111111111111111111"
			if scenario == "different-parent" || scenario == "batch-storage-error" {
				secondID = "33333333333333333333333333333333"
			}
			store.intents[0].TraceContext = encode(firstID)
			store.related[0].TraceContext = encode(secondID)
			observer := &observingEvaluation{fakeEvaluation: store}
			engine.Store = observer
			if scenario == "batch-storage-error" {
				engine.Store = &failingBatchEvaluation{observer}
			}
			if worked, err := engine.Step(context.Background()); !worked || err != nil {
				t.Fatalf("step: worked=%v error=%v", worked, err)
			}
			spans := exporter.GetSpans()
			var evaluation *tracetest.SpanStub
			for i := range spans {
				if spans[i].Name == "monitoring.evaluate" {
					evaluation = &spans[i]
				}
			}
			if evaluation == nil {
				t.Fatal("evaluation span missing")
			}
			if scenario == "evaluation-error" {
				if evaluation.Status.Code != codes.Error {
					t.Fatal("retried evaluation failure has no error status")
				}
				return
			}
			if len(observer.commits) != 2 {
				t.Fatalf("got %d commits", len(observer.commits))
			}
			for i, id := range []string{firstID, secondID} {
				got := observer.commits[i]
				if got.TraceID().String() != id || got.SpanID().String() == "2222222222222222" {
					t.Fatal("commit lost its operation span or request parent")
				}
				if id == firstID && got.SpanID() != evaluation.SpanContext.SpanID() {
					t.Fatal("shared-parent commit detached from evaluation span")
				}
			}
			if scenario == "batch-storage-error" {
				if len(observer.retries) != 2 {
					t.Fatalf("storage retries=%d, want2", len(observer.retries))
				}
				for i, got := range observer.retries {
					if !got.Equal(observer.commits[i]) {
						t.Fatalf("retry%d detached from its commit parent: got%v want%v", i, got, observer.commits[i])
					}
				}
			}
			if scenario == "different-parent" || scenario == "batch-storage-error" {
				if len(spans) != 2 || spans[0].Name != "monitoring.commit" || spans[0].Parent.TraceID().String() != secondID || len(spans[0].Links) != 1 || spans[0].Links[0].SpanContext.SpanID() != evaluation.SpanContext.SpanID() {
					t.Fatal("different-parent commit missing its parent or shared-work link")
				}
			}
		})
	}
}
