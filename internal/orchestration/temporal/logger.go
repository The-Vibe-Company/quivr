package temporal

import (
	"context"
	"log/slog"

	"github.com/The-Vibe-Company/quivr/internal/logging"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/log"
)

// SDK loggers have no context parameter. Translate their typed tracing fields
// into the same context used by the engine logger, keeping its authoritative
// envelope and redaction policy rather than serializing SDK values as Any.
type temporalLogger struct {
	logger *slog.Logger
	ctx    context.Context
}

func newTemporalLogger(logger *slog.Logger) log.Logger {
	return temporalLogger{logger, context.Background()}
}
func (l temporalLogger) Debug(msg string, args ...interface{}) {
	l.logger.DebugContext(l.ctx, msg, args...)
}
func (l temporalLogger) Info(msg string, args ...interface{}) {
	l.logger.InfoContext(l.ctx, msg, args...)
}
func (l temporalLogger) Warn(msg string, args ...interface{}) {
	l.logger.WarnContext(l.ctx, msg, args...)
}
func (l temporalLogger) Error(msg string, args ...interface{}) {
	l.logger.ErrorContext(l.ctx, msg, args...)
}
func (l temporalLogger) With(args ...interface{}) log.Logger {
	sc := trace.SpanContextFromContext(l.ctx)
	tid, sid := sc.TraceID(), sc.SpanID()
	var attrs []any
	for i := 0; i+1 < len(args); i += 2 {
		key, _ := args[i].(string)
		switch key {
		case "TraceID":
			if id, ok := args[i+1].(trace.TraceID); ok {
				tid = id
			}
		case "SpanID":
			if id, ok := args[i+1].(trace.SpanID); ok {
				sid = id
			}
		case "request_id":
			if id, ok := args[i+1].(string); ok {
				l.ctx = logging.WithRequestID(l.ctx, id)
			}
		default:
			attrs = append(attrs, args[i], args[i+1])
		}
	}
	l.ctx = trace.ContextWithSpanContext(l.ctx, trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid}))
	l.logger = l.logger.With(attrs...)
	return l
}
