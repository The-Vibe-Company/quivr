package postgres

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// queryTracer deliberately never records SQL, parameters, DSNs or error text.
// This covers queries issued on a pool or its transactions, including batches.
type queryTracer struct{}

func (queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	ctx, _ = telemetry.Start(ctx, "postgres.query", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("db.system.name", "postgresql")))
	return ctx
}
func (queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span := trace.SpanFromContext(ctx)
	telemetry.Fail(span, data.Err)
	span.End()
}
func (queryTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	ctx, _ = telemetry.Start(ctx, "postgres.batch", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("db.system.name", "postgresql")))
	return ctx
}
func (queryTracer) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}
func (queryTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	span := trace.SpanFromContext(ctx)
	telemetry.Fail(span, data.Err)
	span.End()
}
