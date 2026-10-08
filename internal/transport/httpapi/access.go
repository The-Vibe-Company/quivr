package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func (a *API) serveAccess(w http.ResponseWriter, r *http.Request) {
	if a.processWork != nil {
		r = r.WithContext(lifecycle.WithWorkContext(r.Context(), a.processWork))
	}
	keyID := ""
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		if _, valid := a.Keys[token]; valid {
			keyID = apiKeyID(token)
		}
	}
	r.Pattern = a.router.Pattern(r)
	logRequest(w, r, keyID, a.pushClientIP(r).String(), a.loadMetrics, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.draining != nil && a.draining() {
			writeError(w, publicerr.ContentUnavailable, nil)
			return
		}
		a.serveSensitive(w, r)
	}))
}

// WithDraining stops public request admissions during process shutdown.
func WithDraining(draining func() bool) Option { return func(a *API) { a.draining = draining } }

// WithLifecycle also binds durable request cleanup to the process grace budget.
func WithLifecycle(group *lifecycle.Group) Option {
	return func(a *API) {
		a.draining = group.Draining
		a.processWork = lifecycle.WorkContext(group.Context())
	}
}

// AccessLog adds the same event envelope to private probe HTTP requests.
// The handler must use registered route patterns rather than raw request paths.
func AccessLog(next http.Handler, metrics ...*telemetry.LoadMetrics) http.Handler {
	var load *telemetry.LoadMetrics
	if len(metrics) > 0 {
		load = metrics[0]
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mux, ok := next.(*http.ServeMux); ok {
			_, pattern := mux.Handler(r)
			if pattern == "" {
				// A method rejection still belongs to its registered path. Ask
				// the mux using fixed methods without dispatching a handler.
				probe := *r
				for _, method := range telemetry.HTTPMethods() {
					probe.Method = method
					_, pattern = mux.Handler(&probe)
					if pattern != "" {
						break
					}
				}
			}
			if _, path, found := strings.Cut(pattern, " "); found {
				pattern = path
			}
			r.Pattern = pattern
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		logRequest(w, r, "", ip, load, next)
	})
}

func logRequest(w http.ResponseWriter, r *http.Request, keyID, ip string, metrics *telemetry.LoadMetrics, next http.Handler) {
	start := time.Now()
	var requestID [16]byte
	_, _ = rand.Read(requestID[:])
	id := r.Header.Get("X-Request-ID")
	if len(r.Header.Values("X-Request-ID")) != 1 || !validRequestID(id) {
		id = hex.EncodeToString(requestID[:])
	}
	w.Header().Set("X-Request-ID", id)
	ctx := telemetry.Extract(logging.WithRequestID(r.Context(), id), r.Header)
	route := r.Pattern
	if route == "" {
		route = "unmatched"
	}
	finish := metrics.Begin(route, telemetry.HTTPMethod(r.Method))
	observed := &responseWriter{ResponseWriter: w}
	metricStatus := http.StatusInternalServerError
	defer func() {
		if observed.status != 0 {
			metricStatus = observed.status
		}
		finish(metricStatus, time.Since(start))
	}()
	ctx, span := telemetry.Start(ctx, telemetry.HTTPMethod(r.Method)+" "+route, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attribute.String("http.route", route), attribute.String("http.request.method", telemetry.HTTPMethod(r.Method))))
	defer span.End()
	sc := span.SpanContext()
	if sc.IsValid() {
		w.Header().Set("X-Trace-ID", sc.TraceID().String())
		w.Header().Set("X-Span-ID", sc.SpanID().String())
	}
	r = r.WithContext(ctx)
	defer func() {
		method := telemetry.HTTPMethod(r.Method)
		status := observed.status
		if status == 0 {
			status = http.StatusOK
		}
		span.SetName(method + " " + route)
		span.SetAttributes(attribute.String("http.route", route), attribute.Int("http.response.status_code", status))
		if status >= 500 {
			span.SetStatus(codes.Error, "request failed")
		}
		slog.InfoContext(r.Context(), "http request", "request_id", id, "route", route, "method", method,
			"status", status, "duration_ms", float64(time.Since(start).Microseconds())/1000,
			"response_size", observed.size, "client_ip", ip, "api_key_id", keyID, "error_code", observed.errorCode)
	}()
	next.ServeHTTP(observed, r)
	metricStatus = http.StatusOK
}

type responseWriter struct {
	http.ResponseWriter
	status, size int
	errorCode    string
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseWriter) SetErrorCode(code string)    { w.errorCode = code }
func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.size += n
	return n, err
}
func (w *responseWriter) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// Caller request IDs are bounded opaque correlation values, never free text.
func validRequestID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
