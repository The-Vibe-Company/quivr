package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Owns PostgreSQL query/batch dependency counting via pgx tracer hooks without
// requiring OTLP export or a live database.
func TestAttachDependencyMetricsCountsQueriesAndBatches(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://localhost/bench?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	metrics := telemetry.NewDependencyMetrics()
	AttachDependencyMetrics(cfg, metrics)

	qt, ok := cfg.ConnConfig.Tracer.(pgx.QueryTracer)
	if !ok {
		t.Fatalf("expected pgx.QueryTracer, got %T", cfg.ConnConfig.Tracer)
	}
	bt, ok := cfg.ConnConfig.Tracer.(pgx.BatchTracer)
	if !ok {
		t.Fatalf("expected pgx.BatchTracer, got %T", cfg.ConnConfig.Tracer)
	}

	ctx := context.Background()
	qCtx := qt.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	qt.TraceQueryEnd(qCtx, nil, pgx.TraceQueryEndData{})
	qCtx = qt.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 2"})
	qt.TraceQueryEnd(qCtx, nil, pgx.TraceQueryEndData{})

	bCtx := bt.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{})
	bt.TraceBatchQuery(bCtx, nil, pgx.TraceBatchQueryData{SQL: "SELECT 3"})
	bt.TraceBatchQuery(bCtx, nil, pgx.TraceBatchQueryData{SQL: "SELECT 4"})
	bt.TraceBatchEnd(bCtx, nil, pgx.TraceBatchEndData{})

	var b strings.Builder
	metrics.Write(&b)
	text := b.String()
	for _, want := range []string{
		`quivr_dependency_calls_total{dependency="postgres",protocol="wire",operation="query"} 2`,
		`quivr_dependency_calls_total{dependency="postgres",protocol="wire",operation="batch"} 1`,
		`quivr_dependency_calls_total{dependency="postgres",protocol="wire",operation="batch_query"} 2`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
}
