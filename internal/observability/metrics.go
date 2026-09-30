package observability

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Outcome label values.
const (
	outcomeSucceeded = "succeeded"
	outcomeFailed    = "failed"
)

// maxSeries bounds the label tuples of one metric family. Label values come
// from deployment configuration (plugin ids, declared profiles) and fixed
// sets (operations, modes, outcomes), never from a request; the bound only
// guards against a misconfiguration.
const maxSeries = 256

// family is one Prometheus metric family with dynamic but bounded labels.
type family struct {
	name, help string
	labels     []string
	histogram  bool
	mu         sync.Mutex
	series     map[string]*sample
}

type sample struct {
	values  []string
	count   int64
	sumSec  float64
	buckets [Buckets]int64
}

func newFamily(name, help string, histogram bool, labels ...string) *family {
	return &family{name: name, help: help, labels: labels, histogram: histogram, series: map[string]*sample{}}
}

func (f *family) observe(ms float64, values ...string) {
	key := strings.Join(values, "\x00")
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.series[key]
	if !ok {
		if len(f.series) >= maxSeries {
			return
		}
		s = &sample{values: values}
		f.series[key] = s
	}
	s.count++
	if f.histogram {
		s.sumSec += ms / 1000
		s.buckets[bucketOf(ms)]++
	}
}

func (f *family) write(w io.Writer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kind := "counter"
	if f.histogram {
		kind = "histogram"
	}
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, kind)
	keys := make([]string, 0, len(f.series))
	for k := range f.series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := f.series[k]
		pairs := make([]string, len(s.values))
		for i, v := range s.values {
			pairs[i] = fmt.Sprintf("%s=%q", f.labels[i], v)
		}
		labels := strings.Join(pairs, ",")
		if !f.histogram {
			fmt.Fprintf(w, "%s{%s} %d\n", f.name, labels, s.count)
			continue
		}
		var cumulative int64
		for i, b := range BoundsMS {
			cumulative += s.buckets[i]
			fmt.Fprintf(w, "%s_bucket{%s,le=%q} %d\n", f.name, labels, strconv.FormatFloat(b/1000, 'g', -1, 64), cumulative)
		}
		fmt.Fprintf(w, "%s_bucket{%s,le=\"+Inf\"} %d\n%s_sum{%s} %s\n%s_count{%s} %d\n", f.name, labels, s.count,
			f.name, labels, strconv.FormatFloat(s.sumSec, 'g', -1, 64), f.name, labels, s.count)
	}
}

// metrics are the Prometheus metrics a Recorder keeps beside its rollups.
// They are per process and reset at restart, like every /metrics value.
type metrics struct {
	pluginCalls, pluginDurations, searches, searchDurations *family
}

func newMetrics() *metrics {
	return &metrics{
		pluginCalls:     newFamily("quivr_plugin_calls_total", "Plugin Contribution invocations by plugin, operation and outcome.", false, "plugin", "operation", "outcome"),
		pluginDurations: newFamily("quivr_plugin_call_duration_seconds", "Duration of plugin Contribution invocations.", true, "plugin", "operation"),
		searches:        newFamily("quivr_searches_total", "Public searches by mode, profile and outcome.", false, "mode", "profile", "outcome"),
		searchDurations: newFamily("quivr_search_duration_seconds", "Duration of public searches.", true, "mode", "profile"),
	}
}

func outcome(errorCode string) string {
	if errorCode == "" {
		return outcomeSucceeded
	}
	return outcomeFailed
}

func (m *metrics) pluginCall(c PluginCall) {
	ms := durationMS(c.Duration)
	m.pluginCalls.observe(ms, c.Plugin, c.Operation, outcome(c.ErrorCode))
	m.pluginDurations.observe(ms, c.Plugin, c.Operation)
}

func (m *metrics) search(s Search) {
	ms := durationMS(s.Duration)
	m.searches.observe(ms, s.Mode, s.Profile, outcome(s.ErrorCode))
	m.searchDurations.observe(ms, s.Mode, s.Profile)
}

func (m *metrics) write(w io.Writer) {
	m.pluginCalls.write(w)
	m.pluginDurations.write(w)
	m.searches.write(w)
	m.searchDurations.write(w)
}

func writeCounter(w io.Writer, name, help string, value int64) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, value)
}
