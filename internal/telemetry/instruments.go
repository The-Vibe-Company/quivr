package telemetry

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Family records a bounded metric through the OTel meter. Its private SDK
// reader preserves component-local exposition (including zero series); the
// process meter exports the same observations through OTLP when configured.
// Aggregation, bucket ownership and concurrency belong entirely to the SDK.
type Family struct {
	name, help                 string
	labels                     []string
	bounds                     []float64
	reader                     *sdkmetric.ManualReader
	counter, exportCounter     metric.Int64Counter
	histogram, exportHistogram metric.Float64Histogram
	mu                         sync.Mutex
	series                     map[string][]string
	limit                      int
}

func NewFamily(name, help string, labels []string, bounds []float64, limit int) *Family {
	f := &Family{name: name, help: help, labels: append([]string(nil), labels...), bounds: append([]float64(nil), bounds...), series: map[string][]string{}, limit: limit, reader: sdkmetric.NewManualReader()}
	if bounds != nil {
		f.bounds = append([]float64{}, bounds...)
	}
	sort.Float64s(f.bounds)
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(f.reader)).Meter("quivr")
	exported := otel.Meter("quivr")
	if bounds == nil {
		f.counter, _ = meter.Int64Counter(name, metric.WithDescription(help))
		f.exportCounter, _ = exported.Int64Counter(name, metric.WithDescription(help))
	} else {
		options := []metric.Float64HistogramOption{metric.WithDescription(help), metric.WithExplicitBucketBoundaries(f.bounds...)}
		f.histogram, _ = meter.Float64Histogram(name, options...)
		f.exportHistogram, _ = exported.Float64Histogram(name, options...)
	}
	return f
}
func (f *Family) attributes(values []string) (attribute.Set, bool) {
	if len(values) != len(f.labels) {
		return attribute.Set{}, false
	}
	key := strings.Join(values, "\x00")
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.series[key]; !ok {
		if len(f.series) >= f.limit {
			return attribute.Set{}, false
		}
		f.series[key] = append([]string(nil), values...)
	}
	attrs := make([]attribute.KeyValue, len(values))
	for i, v := range values {
		attrs[i] = attribute.String(f.labels[i], v)
	}
	return attribute.NewSet(attrs...), true
}
func (f *Family) Add(n int64, values ...string) {
	if f == nil || n < 0 {
		return
	}
	attrs, ok := f.attributes(values)
	if !ok {
		return
	}
	ctx := context.Background()
	option := metric.WithAttributeSet(attrs)
	f.counter.Add(ctx, n, option)
	f.exportCounter.Add(ctx, n, option)
}
func (f *Family) Observe(value float64, values ...string) {
	if f == nil || value < 0 {
		return
	}
	attrs, ok := f.attributes(values)
	if !ok {
		return
	}
	ctx := context.Background()
	option := metric.WithAttributeSet(attrs)
	f.histogram.Record(ctx, value, option)
	f.exportHistogram.Record(ctx, value, option)
}
func (f *Family) Write(w io.Writer) {
	if f == nil {
		return
	}
	var data metricdata.ResourceMetrics
	if err := f.reader.Collect(context.Background(), &data); err != nil {
		return
	}
	kind := "counter"
	if f.bounds != nil {
		kind = "histogram"
	}
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, kind)
	f.mu.Lock()
	series := make([][]string, 0, len(f.series))
	for _, v := range f.series {
		series = append(series, v)
	}
	f.mu.Unlock()
	sort.Slice(series, func(i, j int) bool { return strings.Join(series[i], "\x00") < strings.Join(series[j], "\x00") })
	// Histograms with no observations still expose their established zero series.
	if len(series) == 0 && len(f.labels) == 0 {
		series = append(series, nil)
	}
	for _, values := range series {
		attrs := make([]attribute.KeyValue, len(values))
		pairs := make([]string, len(values))
		for i, v := range values {
			attrs[i] = attribute.String(f.labels[i], v)
			pairs[i] = fmt.Sprintf("%s=%q", f.labels[i], v)
		}
		set := attribute.NewSet(attrs...)
		labels := strings.Join(pairs, ",")
		suffix := ""
		if len(f.labels) > 0 {
			suffix = "{" + labels + "}"
		}
		if f.bounds == nil {
			var value int64
			for _, scope := range data.ScopeMetrics {
				for _, m := range scope.Metrics {
					if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
						for _, point := range sum.DataPoints {
							if point.Attributes.Equals(&set) {
								value = point.Value
							}
						}
					}
				}
			}
			fmt.Fprintf(w, "%s%s %d\n", f.name, suffix, value)
		} else {
			point := metricdata.HistogramDataPoint[float64]{}
			for _, scope := range data.ScopeMetrics {
				for _, m := range scope.Metrics {
					if hist, ok := m.Data.(metricdata.Histogram[float64]); ok {
						for _, p := range hist.DataPoints {
							if p.Attributes.Equals(&set) {
								point = p
							}
						}
					}
				}
			}
			prefix := labels
			if prefix != "" {
				prefix += ","
			}
			var cumulative uint64
			for i, b := range f.bounds {
				if i < len(point.BucketCounts) {
					cumulative += point.BucketCounts[i]
				}
				fmt.Fprintf(w, "%s_bucket{%sle=%q} %d\n", f.name, prefix, formatBound(b), cumulative)
			}
			fmt.Fprintf(w, "%s_bucket{%sle=\"+Inf\"} %d\n%s_sum%s %s\n%s_count%s %d\n", f.name, prefix, point.Count, f.name, suffix, strconv.FormatFloat(point.Sum, 'g', -1, 64), f.name, suffix, point.Count)
		}
	}
}
