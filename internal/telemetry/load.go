package telemetry

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var httpMethods = [...]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE", "OTHER"}

type requestSeries struct {
	route, method string
	active        atomic.Int64
	attrs         attribute.Set
}

// LoadMetrics records process-local HTTP load and search admission pressure.
// Register routes and bind admission before serving. Only those templates and
// fixed methods/status classes can become labels, even for refused requests.
type LoadMetrics struct {
	requests, durations *Family
	refusals            *Family
	mu                  sync.RWMutex
	routes              map[string]struct{}
	series              map[[2]string]*requestSeries
	searchSlots         <-chan struct{}
}

func NewLoadMetrics() *LoadMetrics {
	m := &LoadMetrics{
		requests:  newFamily("quivr_http_requests_total", "Completed HTTP requests by registered route, method and status class.", []string{"route", "method", "status_class"}, nil, 0, "{request}"),
		durations: newFamily("quivr_http_request_duration_seconds", "HTTP request lifetime, including streaming responses.", []string{"route", "method"}, []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}, 0, "s"),
		refusals:  newFamily("quivr_search_admission_refused_total", "Searches refused because every admission slot was occupied.", nil, nil, 1, "{request}"),
		routes:    map[string]struct{}{}, series: map[[2]string]*requestSeries{},
	}
	m.RegisterRoutes("unmatched")
	gauge, err := otel.Meter("quivr").Int64ObservableGauge("quivr_http_requests_in_flight", metric.WithDescription("HTTP requests still executing, including streams."), metric.WithUnit("{request}"))
	if err != nil {
		otel.Handle(err)
		return m
	}
	_, err = otel.Meter("quivr").RegisterCallback(func(_ context.Context, observer metric.Observer) error {
		for _, s := range m.snapshot() {
			observer.ObserveInt64(gauge, s.active.Load(), metric.WithAttributeSet(s.attrs))
		}
		return nil
	}, gauge)
	if err != nil {
		otel.Handle(err)
	}
	return m
}

// RegisterRoutes permits templates from a router's startup registrations.
// Request paths, tenant identifiers and arbitrary plugin names never belong here.
func (m *LoadMetrics) RegisterRoutes(routes ...string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, route := range routes {
		m.routes[route] = struct{}{}
	}
	// Include every fixed method/status combination, independent of the SDK's
	// default cardinality cap. Series are created only when actually observed.
	m.requests.mu.Lock()
	m.requests.limit = len(m.routes) * len(httpMethods) * 5
	m.requests.mu.Unlock()
	m.durations.mu.Lock()
	m.durations.limit = len(m.routes) * len(httpMethods)
	m.durations.mu.Unlock()
}

// Begin returns a completion function that records the final response status
// and releases the active request, including on cancellation/panic unwind.
func (m *LoadMetrics) Begin(route, method string) func(int, time.Duration) {
	if m == nil {
		return func(int, time.Duration) {}
	}
	method = boundedHTTPMethod(method)
	m.mu.Lock()
	if _, ok := m.routes[route]; !ok {
		route = "unmatched"
	}
	key := [2]string{route, method}
	s := m.series[key]
	if s == nil {
		s = &requestSeries{route: route, method: method, attrs: attribute.NewSet(attribute.String("route", route), attribute.String("method", method))}
		m.series[key] = s
	}
	s.active.Add(1)
	m.mu.Unlock()
	return func(status int, duration time.Duration) {
		s.active.Add(-1)
		class := status / 100
		if class < 1 || class > 5 {
			class = 5
		}
		m.requests.Add(1, route, method, strconv.Itoa(class)+"xx")
		m.durations.Observe(duration.Seconds(), route, method)
	}
}

func boundedHTTPMethod(method string) string {
	for _, known := range httpMethods {
		if method == known {
			return method
		}
	}
	return "OTHER"
}

func (m *LoadMetrics) snapshot() []*requestSeries {
	m.mu.RLock()
	series := make([]*requestSeries, 0, len(m.series))
	for _, s := range m.series {
		series = append(series, s)
	}
	m.mu.RUnlock()
	sort.Slice(series, func(i, j int) bool {
		if series[i].route != series[j].route {
			return series[i].route < series[j].route
		}
		return series[i].method < series[j].method
	})
	return series
}

// BindSearch observes the actual admission channel rather than a second count
// that can drift from it. Only the API binds it; worker processes omit these gauges.
func (m *LoadMetrics) BindSearch(slots <-chan struct{}) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.searchSlots = slots
	m.mu.Unlock()
	RegisterGauges(searchGauges, func(context.Context) ([]float64, error) { return m.searchValues(), nil })
}

var searchGauges = []GaugeDefinition{
	{Name: "quivr_search_admission_capacity", Help: "Maximum simultaneous admitted searches in this API process.", Unit: "{request}"},
	{Name: "quivr_search_admission_in_use", Help: "Search admission slots currently occupied.", Unit: "{request}"},
	{Name: "quivr_search_admission_available", Help: "Search admission slots currently available.", Unit: "{request}"},
}

func (m *LoadMetrics) searchValues() []float64 {
	m.mu.RLock()
	slots := m.searchSlots
	m.mu.RUnlock()
	used, capacity := len(slots), cap(slots)
	return []float64{float64(capacity), float64(used), float64(capacity - used)}
}
func (m *LoadMetrics) SearchRefused() {
	if m != nil {
		m.refusals.Add(1)
	}
}

func (m *LoadMetrics) Write(w io.Writer) {
	if m == nil {
		return
	}
	m.requests.Write(w)
	m.durations.Write(w)
	fmt.Fprintln(w, "# HELP quivr_http_requests_in_flight HTTP requests still executing, including streams.\n# TYPE quivr_http_requests_in_flight gauge")
	for _, s := range m.snapshot() {
		fmt.Fprintf(w, "quivr_http_requests_in_flight{route=%q,method=%q} %d\n", s.route, s.method, s.active.Load())
	}
	m.mu.RLock()
	bound := m.searchSlots != nil
	m.mu.RUnlock()
	if bound {
		values := m.searchValues()
		for i, g := range searchGauges {
			Gauge(w, g.Name, g.Help, values[i])
		}
		m.refusals.Write(w)
	}
}
