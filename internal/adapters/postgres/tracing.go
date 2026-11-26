package postgres

import (
	"context"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type querySpanKey struct{}

// AttachDependencyMetrics installs the PostgreSQL query/batch counter (and OTel
// tracer when telemetry export is enabled) onto a pool configuration.
func AttachDependencyMetrics(cfg *pgxpool.Config, metrics *telemetry.DependencyMetrics) {
	if cfg == nil {
		return
	}
	if metrics == nil && !telemetry.Enabled() {
		return
	}
	cfg.ConnConfig.Tracer = queryTracer{metrics: metrics}
}

// queryTracer deliberately never records SQL, parameters, DSNs or error text.
// This covers queries issued on a pool or its transactions, including batches.
type queryTracer struct {
	metrics *telemetry.DependencyMetrics
}

func (t queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	t.metrics.Record(telemetry.DependencyPostgres, telemetry.ProtocolWire, telemetry.OpQuery)
	if !telemetry.Enabled() {
		return ctx
	}
	ctx, span := telemetry.Start(ctx, "postgres.query", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("db.system.name", "postgresql")))
	return context.WithValue(ctx, querySpanKey{}, span)
}

func (queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if span, ok := ctx.Value(querySpanKey{}).(trace.Span); ok {
		telemetry.Fail(span, data.Err)
		span.End()
	}
}

func (t queryTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	t.metrics.Record(telemetry.DependencyPostgres, telemetry.ProtocolWire, telemetry.OpBatch)
	if !telemetry.Enabled() {
		return ctx
	}
	ctx, span := telemetry.Start(ctx, "postgres.batch", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("db.system.name", "postgresql")))
	return context.WithValue(ctx, querySpanKey{}, span)
}

func (t queryTracer) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {
	t.metrics.Record(telemetry.DependencyPostgres, telemetry.ProtocolWire, telemetry.OpBatchQuery)
}

func (queryTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	if span, ok := ctx.Value(querySpanKey{}).(trace.Span); ok {
		telemetry.Fail(span, data.Err)
		span.End()
	}
}
