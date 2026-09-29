// Package telemetry exposes a few process metrics in the Prometheus text
// format without a client library (THE-656, THE-662). Every label value comes
// from a fixed set declared at construction: an unknown value is dropped, so
// an identifier or a secret can never become a label.
package telemetry

import (
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Counter counts events by bounded labels: only declared label value tuples are counted.
type Counter struct {
	name, help string
	labels     []string
	series     [][]string
	counts     []atomic.Int64
}

// NewCounter declares a counter over labels whose value tuples are series.
func NewCounter(name, help string, labels []string, series ...[]string) *Counter {
	return &Counter{name: name, help: help, labels: labels, series: series, counts: make([]atomic.Int64, len(series))}
}

// Add counts n events for one declared tuple; anything else, or a nil counter, is ignored.
func (c *Counter) Add(n int, values ...string) {
	if c == nil || n <= 0 {
		return
	}
	for i, s := range c.series {
		if slices.Equal(s, values) {
			c.counts[i].Add(int64(n))
		}
	}
}

// Inc counts one event.
func (c *Counter) Inc(values ...string) { c.Add(1, values...) }

// Write renders the counter in the text exposition format.
func (c *Counter) Write(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", c.name, c.help, c.name)
	for i, s := range c.series {
		pairs := make([]string, len(s))
		for j, v := range s {
			pairs[j] = fmt.Sprintf("%s=%q", c.labels[j], v)
		}
		fmt.Fprintf(w, "%s{%s} %d\n", c.name, strings.Join(pairs, ","), c.counts[i].Load())
	}
}

// Histogram observes durations in fixed buckets, in seconds.
type Histogram struct {
	name, help string
	bounds     []float64
	mu         sync.Mutex
	counts     []uint64
	sum        float64
	total      uint64
}

// NewHistogram declares a histogram with ascending upper bounds in seconds.
func NewHistogram(name, help string, bounds ...float64) *Histogram {
	sort.Float64s(bounds)
	return &Histogram{name: name, help: help, bounds: bounds, counts: make([]uint64, len(bounds))}
}

// Observe records one duration; a nil histogram or a negative duration is ignored.
func (h *Histogram) Observe(d time.Duration) {
	if h == nil || d < 0 {
		return
	}
	s := d.Seconds()
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, b := range h.bounds {
		if s <= b {
			h.counts[i]++
		}
	}
	h.sum += s
	h.total++
}

// Write renders cumulative buckets, sum and count.
func (h *Histogram) Write(w io.Writer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", h.name, h.help, h.name)
	for i, b := range h.bounds {
		fmt.Fprintf(w, "%s_bucket{le=%q} %d\n", h.name, formatBound(b), h.counts[i])
	}
	fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n%s_sum %s\n%s_count %d\n", h.name, h.total, h.name, strconv.FormatFloat(h.sum, 'g', -1, 64), h.name, h.total)
}

func formatBound(b float64) string {
	if math.IsInf(b, 1) {
		return "+Inf"
	}
	return strconv.FormatFloat(b, 'g', -1, 64)
}

// Gauge writes one gauge value.
func Gauge(w io.Writer, name, help string, value float64) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", name, help, name, name, strconv.FormatFloat(value, 'g', -1, 64))
}

// Commands counts durable commands the API accepted, by command kind.
// Idempotent replays that return an existing Receipt count as accepted.
type Commands struct{ counter *Counter }

// Command kinds of the accepted-command counter.
const (
	CommandRecord        = "record"
	CommandBatchEntry    = "batch_entry"
	CommandWithdrawal    = "withdrawal"
	CommandUploadConfirm = "upload_confirm"
)

// NewCommands declares the accepted-command counter.
func NewCommands() Commands {
	kinds := [][]string{{CommandRecord}, {CommandBatchEntry}, {CommandWithdrawal}, {CommandUploadConfirm}}
	return Commands{NewCounter("quivr_commands_accepted_total", "Durable commands the API accepted (202, or accepted batch entries), replays included.", []string{"command"}, kinds...)}
}

// Accepted counts n accepted commands of one kind; the zero Commands ignores it.
func (c Commands) Accepted(kind string, n int) { c.counter.Add(n, kind) }

// Write renders the counter.
func (c Commands) Write(w io.Writer) { c.counter.Write(w) }

// Processing stages and outcomes of the processing outcome counter.
const (
	StageBaseline    = "baseline"
	StageEnrichment  = "enrichment"
	OutcomeSucceeded = "succeeded"
	OutcomeRetrying  = "retrying"
	OutcomeBlocked   = "blocked"
)

// Processing counts processing outcomes and measures acceptance to searchable.
type Processing struct {
	outcomes   *Counter
	searchable *Histogram
}

// NewProcessing declares the worker's processing metrics.
func NewProcessing() *Processing {
	var series [][]string
	for _, stage := range []string{StageBaseline, StageEnrichment} {
		for _, outcome := range []string{OutcomeSucceeded, OutcomeRetrying, OutcomeBlocked} {
			series = append(series, []string{stage, outcome})
		}
	}
	return &Processing{
		outcomes:   NewCounter("quivr_processing_outcomes_total", "Processing activity outcomes by stage and outcome.", []string{"stage", "outcome"}, series...),
		searchable: NewHistogram("quivr_acceptance_to_searchable_seconds", "Time from Receipt acceptance to the Version becoming lexically searchable.", 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 1800),
	}
}

// Outcome counts one processing outcome of a stage.
func (p *Processing) Outcome(stage, outcome string) {
	if p != nil {
		p.outcomes.Inc(stage, outcome)
	}
}

// Searchable records the acceptance-to-searchable duration of one Version.
func (p *Processing) Searchable(d time.Duration) {
	if p != nil {
		p.searchable.Observe(d)
	}
}

// Write renders the processing metrics.
func (p *Processing) Write(w io.Writer) {
	p.outcomes.Write(w)
	p.searchable.Write(w)
}

// ChangePrune counts change events the journal prune physically deleted and
// prune passes that failed (THE-697). The zero value is ready; nil ignores.
type ChangePrune struct{ pruned, failures atomic.Int64 }

// Pruned counts n deleted change events.
func (p *ChangePrune) Pruned(n int) {
	if p != nil && n > 0 {
		p.pruned.Add(int64(n))
	}
}

// Failed counts one failed prune pass.
func (p *ChangePrune) Failed() {
	if p != nil {
		p.failures.Add(1)
	}
}

// Write renders the prune counters.
func (p *ChangePrune) Write(w io.Writer) {
	fmt.Fprintf(w, "# HELP quivr_change_events_pruned_total Change events physically deleted after the retention window.\n# TYPE quivr_change_events_pruned_total counter\nquivr_change_events_pruned_total %d\n", p.pruned.Load())
	fmt.Fprintf(w, "# HELP quivr_change_prune_failures_total Change-journal prune passes that failed.\n# TYPE quivr_change_prune_failures_total counter\nquivr_change_prune_failures_total %d\n", p.failures.Load())
}
