package telemetry

import (
	"context"
	"encoding/hex"
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// Owns configuration/export at the real OTLP boundary. Broken endpoint options,
// sampling, resource configuration or shutdown lose observations; local metric
// exposition and HTTP identity tests cannot detect that. Only the collector is
// fake; it decodes actual SDK exports instead of supplying expected parentage.
func TestOTLPConfigurationExportsAndFlushes(t *testing.T) {
	oldTrace, oldMeter, oldErrors := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetErrorHandler()
	t.Cleanup(func() {
		otel.SetTracerProvider(oldTrace)
		otel.SetMeterProvider(oldMeter)
		otel.SetErrorHandler(oldErrors)
	})
	for _, protocol := range []string{"http/protobuf", "grpc"} {
		t.Run(protocol, func(t *testing.T) {
			collector := &exportCollector{t: t}
			var endpoint string
			if protocol == "grpc" {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				server := grpc.NewServer()
				tracepb.RegisterTraceServiceServer(server, traceReceiver{collector: collector})
				metricspb.RegisterMetricsServiceServer(server, metricReceiver{collector: collector})
				go server.Serve(listener)
				defer server.Stop()
				endpoint = "http://" + listener.Addr().String()
			} else {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "collector-test" {
						t.Error("collector header missing")
					}
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					collector.mu.Lock()
					defer collector.mu.Unlock()
					switch r.URL.Path {
					case "/v1/traces":
						var data tracepb.ExportTraceServiceRequest
						if err := proto.Unmarshal(body, &data); err != nil {
							t.Error(err)
							return
						}
						collector.traces = append(collector.traces, &data)
					case "/v1/metrics":
						var data metricspb.ExportMetricsServiceRequest
						if err := proto.Unmarshal(body, &data); err != nil {
							t.Error(err)
							return
						}
						collector.metrics = append(collector.metrics, &data)
					default:
						t.Errorf("unexpected export endpoint %s", r.URL.Path)
					}
					w.Header().Set("Content-Type", "application/x-protobuf")
				}))
				defer server.Close()
				endpoint = server.URL
			}
			ratio := 0.0
			runtime, err := Init(context.Background(), Config{Endpoint: endpoint, Protocol: protocol, Headers: map[string]string{"Authorization": "collector-test"}, SamplingRatio: &ratio, ResourceAttributes: map[string]string{"service.name": "test-engine"}})
			if err != nil {
				t.Fatal(err)
			}
			_, root := Start(context.Background(), "root-not-sampled")
			if root.IsRecording() {
				t.Fatal("root sampling ratio zero ignored")
			}
			root.End()
			incoming := http.Header{"Traceparent": []string{"00-11111111111111111111111111111111-2222222222222222-01"}}
			ctx, child := Start(Extract(context.Background(), incoming), "test.operation")
			if !child.IsRecording() || Capture(ctx).Traceparent == "" {
				t.Fatal("sampled incoming parent lost")
			}
			child.End()
			incoming.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-00")
			_, unsampled := Start(Extract(context.Background(), incoming), "child-not-sampled")
			if unsampled.IsRecording() {
				t.Fatal("unsampled parent ignored")
			}
			unsampled.End()
			counter := NewCounter("quivr_test_requests_total", "Test requests.", nil, nil)
			counter.Add(7)
			load := NewLoadMetrics()
			load.RegisterRoutes("/items/{id}")
			load.Begin("/items/{id}", "GET")(503, 2*time.Second)
			finishActive := load.Begin("/items/{id}", "GET")
			slots := make(chan struct{}, 3)
			slots <- struct{}{}
			load.BindSearch(slots)
			load.SearchRefused()
			RegisterGauges([]GaugeDefinition{{Name: "quivr_test_backlog", Help: "Test backlog."}}, func(context.Context) ([]float64, error) { return []float64{40}, nil })
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := runtime.Shutdown(shutdown); err != nil {
				t.Fatal(err)
			}
			finishActive(200, time.Second)
			collector.mu.Lock()
			defer collector.mu.Unlock()
			count := 0
			for _, request := range collector.traces {
				for _, resource := range request.ResourceSpans {
					service := ""
					for _, attr := range resource.Resource.Attributes {
						if attr.Key == "service.name" {
							service = attr.Value.GetStringValue()
						}
					}
					if service != "test-engine" {
						t.Fatalf("resource override missing: %q", service)
					}
					for _, scope := range resource.ScopeSpans {
						for _, span := range scope.Spans {
							count++
							if span.Name != "test.operation" || hex.EncodeToString(span.TraceId) != "11111111111111111111111111111111" || hex.EncodeToString(span.ParentSpanId) != "2222222222222222" {
								t.Fatalf("wrong exported span: %v", span)
							}
						}
					}
				}
			}
			if count != 1 {
				t.Fatalf("exported %d spans, want sampled child only", count)
			}
			metricFound, gaugeFound := false, false
			loadCount, loadDuration, activeGauge, admissionGauge, refusalCount := false, false, false, false, false
			for _, request := range collector.metrics {
				for _, resource := range request.ResourceMetrics {
					for _, scope := range resource.ScopeMetrics {
						for _, m := range scope.Metrics {
							switch m.Name {
							case "quivr_http_requests_total":
								for _, p := range m.GetSum().DataPoints {
									loadCount = loadCount || p.GetAsInt() == 1 && m.Unit == "{request}"
								}
							case "quivr_http_request_duration_seconds":
								for _, p := range m.GetHistogram().DataPoints {
									loadDuration = loadDuration || p.Count == 1 && p.GetSum() == 2 && m.Unit == "s"
								}
							case "quivr_http_requests_in_flight":
								for _, p := range m.GetGauge().DataPoints {
									activeGauge = activeGauge || p.GetAsInt() == 1 && m.Unit == "{request}"
								}
							case "quivr_search_admission_refused_total":
								for _, p := range m.GetSum().DataPoints {
									refusalCount = refusalCount || p.GetAsInt() == 1 && m.Unit == "{request}"
								}
							case "quivr_search_admission_available":
								for _, p := range m.GetGauge().DataPoints {
									admissionGauge = admissionGauge || p.GetAsDouble() == 2 && m.Unit == "{request}"
								}
							}
							if m.Name == "quivr_test_backlog" {
								for _, p := range m.GetGauge().DataPoints {
									gaugeFound = gaugeFound || p.GetAsDouble() == 40
								}
							}
							if m.Name == "quivr_test_requests_total" {
								for _, p := range m.GetSum().DataPoints {
									metricFound = metricFound || p.GetAsInt() == 7
								}
							}
						}
					}
				}
			}
			if !loadCount || !loadDuration || !activeGauge || !admissionGauge || !refusalCount {
				t.Fatalf("OTLP load metrics: count=%t duration=%t active=%t admission=%t refusals=%t", loadCount, loadDuration, activeGauge, admissionGauge, refusalCount)
			}
			if !gaugeFound {
				t.Fatal("OTLP omitted gauge without Prometheus scrape")
			}
			if !metricFound {
				t.Fatal("OTLP shutdown omitted meter observation")
			}
		})
	}
	for _, cfg := range []Config{{Endpoint: "https://collector.invalid/v1/traces"}, {Endpoint: "https://collector.invalid/prefix/v1/metrics"}, {Endpoint: "secret-invalid"}, {Endpoint: "http://collector.invalid", Protocol: "bad"}, {Endpoint: "http://collector.invalid", SamplingRatio: func() *float64 { v := 1.1; return &v }()}} {
		if _, err := Init(context.Background(), cfg); err == nil {
			t.Fatal("invalid config accepted")
		} else if strings.Contains(err.Error(), "secret-invalid") {
			t.Fatal("config error leaked endpoint")
		}
	}
	disabled, err := Init(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer disabled.Shutdown(context.Background())
	_, off := Start(context.Background(), "off")
	if off.IsRecording() {
		t.Fatal("tracing enabled by default")
	}
	off.End()
}

type exportCollector struct {
	t       *testing.T
	mu      sync.Mutex
	traces  []*tracepb.ExportTraceServiceRequest
	metrics []*metricspb.ExportMetricsServiceRequest
}

func (c *exportCollector) checkHeader(ctx context.Context) {
	if values := metadata.ValueFromIncomingContext(ctx, "authorization"); len(values) != 1 || values[0] != "collector-test" {
		c.t.Error("gRPC collector header missing")
	}
}

type traceReceiver struct {
	tracepb.UnimplementedTraceServiceServer
	collector *exportCollector
}

func (r traceReceiver) Export(ctx context.Context, request *tracepb.ExportTraceServiceRequest) (*tracepb.ExportTraceServiceResponse, error) {
	r.collector.checkHeader(ctx)
	r.collector.mu.Lock()
	defer r.collector.mu.Unlock()
	r.collector.traces = append(r.collector.traces, request)
	return &tracepb.ExportTraceServiceResponse{}, nil
}

type metricReceiver struct {
	metricspb.UnimplementedMetricsServiceServer
	collector *exportCollector
}

func (r metricReceiver) Export(ctx context.Context, request *metricspb.ExportMetricsServiceRequest) (*metricspb.ExportMetricsServiceResponse, error) {
	r.collector.checkHeader(ctx)
	r.collector.mu.Lock()
	defer r.collector.mu.Unlock()
	r.collector.metrics = append(r.collector.metrics, request)
	return &metricspb.ExportMetricsServiceResponse{}, nil
}

func TestDurableContextBoundsVendorStateWithoutLosingParent(t *testing.T) {
	var state []string
	for i := 0; i < 32; i++ {
		state = append(state, fmt.Sprintf("v%d=%s", i, strings.Repeat("a", 256)))
	}
	ctx := Extract(logging.WithRequestID(context.Background(), "durable-request"), http.Header{"Traceparent": {"00-11111111111111111111111111111111-2222222222222222-01"}, "Tracestate": {strings.Join(state, ",")}})
	if len(Capture(ctx).Tracestate) < 4096 {
		t.Fatal("fixture did not supply maximal valid vendor state")
	}
	encoded := Encode(ctx)
	got := Capture(Restore(context.Background(), encoded))
	if len(encoded) > 4096 || got.Traceparent != Capture(ctx).Traceparent || got.RequestID != "durable-request" {
		t.Fatal("durable envelope lost the parent or caller identity")
	}
}
