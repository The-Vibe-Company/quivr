package quivrplugin

import (
	"context"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"net/http"
)

type requestIDKey struct{}

func continueTrace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		id := r.Header.Get("X-Request-ID")
		if len(id) > 128 {
			id = ""
		}
		for _, c := range id {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				id = ""
				break
			}
		}
		ctx = context.WithValue(ctx, requestIDKey{}, id)
		ctx, span := otel.Tracer("quivr-plugin").Start(ctx, "plugin.request", trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// InjectTrace continues the current invocation on a provider request. Configure
// an OpenTelemetry SDK provider in the plugin process to export its own spans.
// Only W3C TraceContext and the bounded request ID travel; baggage is excluded.
func InjectTrace(ctx context.Context, headers http.Header) {
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(headers))
	if id, _ := ctx.Value(requestIDKey{}).(string); id != "" {
		headers.Set("X-Request-ID", id)
	}
}
