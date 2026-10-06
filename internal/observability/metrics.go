package observability

import (
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"io"
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

type family struct {
	instrument *telemetry.Family
	histogram  bool
}

func newFamily(name, help string, histogram bool, labels ...string) *family {
	var bounds []float64
	if histogram {
		bounds = make([]float64, len(BoundsMS))
		for i, b := range BoundsMS {
			bounds[i] = b / 1000
		}
	}
	return &family{telemetry.NewFamily(name, help, labels, bounds, maxSeries), histogram}
}
func (f *family) observe(ms float64, values ...string) {
	if f.histogram {
		f.instrument.Observe(ms/1000, values...)
	} else {
		f.instrument.Add(1, values...)
	}
}
func (f *family) write(w io.Writer) { f.instrument.Write(w) }

// metrics are the Prometheus metrics a Recorder keeps beside its rollups.
// They are per process and reset at restart, like every /metrics value.
type metrics struct {
	pluginCalls, pluginDurations, searches, searchDurations, matches *family
}

func newMetrics() *metrics {
	return &metrics{
		pluginCalls:     newFamily("quivr_plugin_calls_total", "Plugin Contribution invocations by plugin, operation and outcome.", false, "plugin", "operation", "outcome"),
		pluginDurations: newFamily("quivr_plugin_call_duration_seconds", "Duration of plugin Contribution invocations.", true, "plugin", "operation"),
		searches:        newFamily("quivr_searches_total", "Public searches by mode, profile and outcome.", false, "mode", "profile", "outcome"),
		searchDurations: newFamily("quivr_search_duration_seconds", "Duration of public searches.", true, "mode", "profile"),
		matches:         newFamily("quivr_matches_created_total", "Matches committed by alerts, by evaluator plugin.", false, "evaluator"),
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

func (m *metrics) match(evaluator string) {
	m.matches.observe(0, evaluator)
}

func (m *metrics) write(w io.Writer) {
	m.pluginCalls.write(w)
	m.pluginDurations.write(w)
	m.searches.write(w)
	m.searchDurations.write(w)
	m.matches.write(w)
}
