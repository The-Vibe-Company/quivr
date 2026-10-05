package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Config enables OTLP export only when Endpoint is set. ResourceAttributes are
// deployment identity, never request data. SamplingRatio defaults to one.
type Config struct {
	Endpoint           string            `json:"endpoint"`
	Protocol           string            `json:"protocol"`
	Headers            map[string]string `json:"headers"`
	SamplingRatio      *float64          `json:"sampling_ratio"`
	ResourceAttributes map[string]string `json:"resource_attributes"`
}

var enabled atomic.Bool
var traceContext = propagation.TraceContext{}

// Enabled reports whether dependency tracing should be installed at startup.
func Enabled() bool { return enabled.Load() }

// Runtime owns exporter lifetime. Shutdown must run after in-flight work drains.
type Runtime struct {
	traces  *sdktrace.TracerProvider
	metrics *sdkmetric.MeterProvider
}

func Init(ctx context.Context, cfg Config) (*Runtime, error) {
	ratio := 1.0
	if cfg.SamplingRatio != nil {
		ratio = *cfg.SamplingRatio
	}
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 || ratio > 1 {
		return nil, errors.New("telemetry sampling_ratio must be between zero and one")
	}
	if cfg.Protocol == "" {
		cfg.Protocol = "http/protobuf"
	}
	if cfg.Protocol != "http/protobuf" && cfg.Protocol != "grpc" {
		return nil, errors.New("telemetry protocol must be http/protobuf or grpc")
	}
	r := &Runtime{}
	if cfg.Endpoint == "" {
		enabled.Store(false)
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
		return r, nil
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("telemetry endpoint must be an HTTP(S) collector URL without credentials or query")
	}
	if cfg.Protocol == "grpc" && u.Path != "" && u.Path != "/" {
		return nil, errors.New("telemetry gRPC endpoint must not have a path")
	}
	attrs := []attribute.KeyValue{attribute.String("service.name", "quivr")}
	for k, v := range cfg.ResourceAttributes {
		attrs = append(attrs, attribute.String(k, v))
	}
	res := resource.NewSchemaless(attrs...)
	var exp sdktrace.SpanExporter
	var metrics sdkmetric.Exporter
	if cfg.Protocol == "grpc" {
		to := []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(cfg.Endpoint), otlptracegrpc.WithHeaders(cfg.Headers), otlptracegrpc.WithTimeout(5 * time.Second)}
		mo := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpointURL(cfg.Endpoint), otlpmetricgrpc.WithHeaders(cfg.Headers), otlpmetricgrpc.WithTimeout(5 * time.Second)}
		exp, err = otlptracegrpc.New(ctx, to...)
		if err == nil {
			metrics, err = otlpmetricgrpc.New(ctx, mo...)
		}
	} else {
		exp, err = otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(strings.TrimRight(cfg.Endpoint, "/")+"/v1/traces"), otlptracehttp.WithHeaders(cfg.Headers), otlptracehttp.WithTimeout(5*time.Second))
		if err == nil {
			metrics, err = otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(strings.TrimRight(cfg.Endpoint, "/")+"/v1/metrics"), otlpmetrichttp.WithHeaders(cfg.Headers), otlpmetrichttp.WithTimeout(5*time.Second))
		}
	}
	if err != nil {
		if exp != nil {
			_ = exp.Shutdown(ctx)
		}
		return nil, errors.New("telemetry exporter initialization failed")
	}
	r.traces = sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))), sdktrace.WithBatcher(exp))
	r.metrics = sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metrics)))
	otel.SetTracerProvider(r.traces)
	otel.SetMeterProvider(r.metrics)
	// SDK errors can contain collector URLs and authorization headers. Preserve
	// a stable diagnostic, without dependency-owned error text.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) { slog.Warn("telemetry export failed", "event", "quivr.telemetry.export_failed") }))
	enabled.Store(true)
	return r, nil
}
func (r *Runtime) Shutdown(ctx context.Context) error {
	var errs []error
	if r.traces != nil {
		errs = append(errs, r.traces.Shutdown(ctx))
	}
	if r.metrics != nil {
		errs = append(errs, r.metrics.Shutdown(ctx))
	}
	return errors.Join(errs...)
}

// Carrier is the bounded W3C context persisted across durable queues. Baggage
// is deliberately excluded: its arbitrary values may contain customer data.
type Carrier struct {
	Traceparent string `json:"traceparent,omitempty"`
	Tracestate  string `json:"tracestate,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
}

func Capture(ctx context.Context) Carrier {
	h := http.Header{}
	Inject(ctx, h)
	return Carrier{h.Get("traceparent"), h.Get("tracestate"), logging.RequestID(ctx)}
}
func (c Carrier) Restore(ctx context.Context) context.Context {
	h := http.Header{}
	h.Set("traceparent", c.Traceparent)
	h.Set("tracestate", c.Tracestate)
	ctx = Extract(ctx, h)
	if c.RequestID != "" {
		ctx = logging.WithRequestID(ctx, c.RequestID)
	}
	return ctx
}
func Extract(ctx context.Context, h http.Header) context.Context {
	return traceContext.Extract(ctx, propagation.HeaderCarrier(h))
}
func Inject(ctx context.Context, h http.Header) {
	traceContext.Inject(ctx, propagation.HeaderCarrier(h))
	if id := logging.RequestID(ctx); id != "" {
		h.Set("X-Request-ID", id)
	}
}
func Start(ctx context.Context, name string, options ...trace.SpanStartOption) (context.Context, trace.Span) {
	return otel.Tracer("quivr").Start(ctx, name, options...)
}

// Fail reports only a safe, fixed failure classification, never an error string.
func Fail(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, "operation failed")
	}
}

// Transport propagates W3C context with fixed operation names. Unlike generic
// HTTP instrumentation it never exports URLs, object keys, headers or bodies.
func Transport(base http.RoundTripper, name string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if !Enabled() {
		return base
	}
	return tracedTransport{base, name}
}

type tracedTransport struct {
	base http.RoundTripper
	name string
}

func (t tracedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, span := Start(r.Context(), t.name, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("http.request.method", r.Method)))
	defer span.End()
	clone := r.Clone(ctx)
	Inject(ctx, clone.Header)
	response, err := t.base.RoundTrip(clone)
	Fail(span, err)
	if response != nil {
		span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
		if response.StatusCode >= 500 {
			span.SetStatus(codes.Error, "upstream failed")
		}
	}
	return response, err
}

// Encode captures only validated W3C fields and the engine's request ID.
func Encode(ctx context.Context) string {
	c := Capture(ctx)
	if c.Traceparent == "" && c.RequestID == "" {
		return ""
	}
	b, _ := json.Marshal(c)
	return string(b)
}
func Restore(ctx context.Context, encoded string) context.Context {
	if encoded == "" {
		return ctx
	}
	var c Carrier
	if len(encoded) > 4096 || json.Unmarshal([]byte(encoded), &c) != nil {
		return ctx
	}
	return c.Restore(ctx)
}
