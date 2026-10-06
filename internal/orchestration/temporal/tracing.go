package temporal

import (
	"context"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/logging"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	temporalotel "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/workflow"
)

func tracingInterceptor() (interceptor.Interceptor, error) {
	return temporalotel.NewTracingInterceptor(temporalotel.TracerOptions{
		DisableBaggage: true, AllowInvalidParentSpans: true, TextMapPropagator: propagation.TraceContext{},
		SpanStarter: func(ctx context.Context, t trace.Tracer, name string, options ...trace.SpanStartOption) trace.Span {
			operation, _, _ := strings.Cut(name, ":")
			_, span := t.Start(ctx, "temporal."+operation, options...)
			return privateSpan{span}
		},
	})
}

// The SDK adds workflow/run IDs and raw errors after starting spans. Suppress
// those values at the adapter boundary; operation and parentage remain useful.
type privateSpan struct{ trace.Span }

func (privateSpan) SetAttributes(...attribute.KeyValue)     {}
func (privateSpan) RecordError(error, ...trace.EventOption) {}
func (s privateSpan) SetStatus(code codes.Code, _ string)   { s.Span.SetStatus(code, "") }
func (privateSpan) AddEvent(string, ...trace.EventOption)   {}

// requestIDPropagation complements the SDK's W3C interceptor across client,
// workflow and activity contexts. Only the bounded engine correlation ID travels.
type requestIDPropagation struct{}
type workflowRequestID struct{}

const requestIDHeader = "quivr-request-id"

func (requestIDPropagation) Inject(ctx context.Context, w workflow.HeaderWriter) error {
	if id := logging.RequestID(ctx); id != "" {
		p, err := converter.GetDefaultDataConverter().ToPayload(id)
		if err != nil {
			return err
		}
		w.Set(requestIDHeader, p)
	}
	return nil
}
func (requestIDPropagation) Extract(ctx context.Context, r workflow.HeaderReader) (context.Context, error) {
	if p, ok := r.Get(requestIDHeader); ok {
		var id string
		if err := converter.GetDefaultDataConverter().FromPayload(p, &id); err != nil {
			return ctx, err
		}
		ctx = logging.WithRequestID(ctx, id)
	}
	return ctx, nil
}
func (requestIDPropagation) InjectFromWorkflow(ctx workflow.Context, w workflow.HeaderWriter) error {
	id, _ := ctx.Value(workflowRequestID{}).(string)
	return (requestIDPropagation{}).Inject(logging.WithRequestID(context.Background(), id), w)
}
func (requestIDPropagation) ExtractToWorkflow(ctx workflow.Context, r workflow.HeaderReader) (workflow.Context, error) {
	c, err := (requestIDPropagation{}).Extract(context.Background(), r)
	if err != nil {
		return ctx, err
	}
	return workflow.WithValue(ctx, workflowRequestID{}, logging.RequestID(c)), nil
}

var _ workflow.ContextPropagator = requestIDPropagation{}
