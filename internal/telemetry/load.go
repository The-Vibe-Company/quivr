package telemetry

import (
	"context"
	"fmt"
	"io"
	"slices"
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

// SearchAdmissionStats is a point-in-time snapshot of process-local search
// admission state.
type SearchAdmissionStats struct {
	Limit     int
	InUse     int
	Available int
	Queued    int
}

// LoadMetrics records process-local HTTP load and search admission pressure.
// Register routes and bind admission before serving. Only those templates and
// fixed methods/status classes can become labels, even for refused requests.
type LoadMetrics struct {
	requests, durations *Family
	refusals            *Family
	degradations        *Family
	mu                  sync.RWMutex
	routes              map[string]struct{}
	series              map[[2]string]*requestSeries
	searchStats         func() SearchAdmissionStats
}

func NewLoadMetrics() *LoadMetrics {
	m := &LoadMetrics{
		requests:     newFamily("quivr_http_requests_total", "Completed HTTP requests by registered route, method and status class.", []string{"route", "method", "status_class"}, nil, 0, "{request}"),
		durations:    newFamily("quivr_http_request_duration_seconds", "HTTP request lifetime, including streaming responses.", []string{"route", "method"}, []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}, 0, "s"),
		refusals:     newFamily("quivr_search_admission_refused_total", "Searches shed because search admission was saturated.", nil, nil, 1, "{request}"),
		degradations: newFamily("quivr_search_degradations_total", "Completed searches that returned degraded results, by fixed reason.", []string{"reason"}, nil, 8, "{request}"),
		routes:       map[string]struct{}{}, series: map[[2]string]*requestSeries{},
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
	method = HTTPMethod(method)
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

// HTTPMethods returns the standard methods that can have distinct labels.
// OTHER is reserved for normalization, rather than route matching.
func HTTPMethods() []string { return slices.Clone(httpMethods[:len(httpMethods)-1]) }

// HTTPMethod normalizes arbitrary request methods to the shared label set.
func HTTPMethod(method string) string {
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

// BindSearch observes a fixed-capacity admission channel directly so gauges
// cannot drift from it. Only the API binds it; worker processes omit these gauges.
func (m *LoadMetrics) BindSearch(slots <-chan struct{}) {
	if m == nil || slots == nil {
		return
	}
	m.BindSearchAdmission(func() SearchAdmissionStats {
		used, capacity := len(slots), cap(slots)
		return SearchAdmissionStats{Limit: capacity, InUse: used, Available: max(0, capacity-used)}
	})
}

// BindSearchAdmission observes the live adaptive search admission state.
// Only the API binds it; worker processes omit these gauges.
func (m *LoadMetrics) BindSearchAdmission(sample func() SearchAdmissionStats) {
	if m == nil || sample == nil {
		return
	}
	m.mu.Lock()
	m.searchStats = sample
	m.mu.Unlock()
	RegisterGauges(searchGauges, func(context.Context) ([]float64, error) { return m.searchValues(), nil })
}

var searchGauges = []GaugeDefinition{
	{Name: "quivr_search_admission_capacity", Help: "Current adaptive concurrency limit for simultaneous admitted searches in this API process.", Unit: "{request}"},
	{Name: "quivr_search_admission_in_use", Help: "Search admission slots currently occupied.", Unit: "{request}"},
	{Name: "quivr_search_admission_available", Help: "Search admission slots currently available.", Unit: "{request}"},
	{Name: "quivr_search_admission_queued", Help: "Searches currently waiting in the bounded admission queue.", Unit: "{request}"},
}

func (m *LoadMetrics) searchValues() []float64 {
	m.mu.RLock()
	sample := m.searchStats
	m.mu.RUnlock()
	if sample == nil {
		return []float64{0, 0, 0, 0}
	}
	s := sample()
	return []float64{float64(s.Limit), float64(s.InUse), float64(s.Available), float64(s.Queued)}
}

func (m *LoadMetrics) SearchRefused() {
	if m != nil {
		m.refusals.Add(1)
	}
}

// SearchDegraded counts one search degradation under a fixed, low-cardinality
// reason. Unknown values normalize to "other" so dynamic data never enters.
func (m *LoadMetrics) SearchDegraded(reason string) {
	if m == nil {
		return
	}
	switch reason {
	case "vectors_unavailable", "reranker_budget_exceeded", "reranker_unavailable":
	default:
		reason = "other"
	}
	m.degradations.Add(1, reason)
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
	bound := m.searchStats != nil
	m.mu.RUnlock()
	if bound {
		values := m.searchValues()
		for i, g := range searchGauges {
			Gauge(w, g.Name, g.Help, values[i])
		}
		m.refusals.Write(w)
		m.degradations.Write(w)
	}
}
