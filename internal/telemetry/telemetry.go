// Package telemetry records process metrics through OpenTelemetry meters,
// preserving the Prometheus text format. Every label value comes
// from a fixed set declared at construction: an unknown value is dropped, so
// an identifier or a secret can never become a label.
package telemetry

import (
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"
)

// Counter counts events by declared, bounded label tuples.
type Counter struct {
	family *Family
	series [][]string
}

func NewCounter(name, help string, labels []string, series ...[]string) *Counter {
	c := &Counter{family: NewFamily(name, help, labels, nil, len(series)), series: series}
	for _, values := range series {
		c.family.Add(0, values...)
	}
	return c
}
func (c *Counter) Add(n int, values ...string) {
	if c == nil || n <= 0 {
		return
	}
	for _, s := range c.series {
		if slices.Equal(s, values) {
			c.family.Add(int64(n), values...)
			return
		}
	}
}
func (c *Counter) Inc(values ...string) { c.Add(1, values...) }
func (c *Counter) Write(w io.Writer) {
	if c != nil {
		c.family.Write(w)
	}
}

// Histogram records non-negative values with SDK-owned explicit buckets.
type Histogram struct{ family *Family }

func NewHistogram(name, help string, bounds ...float64) *Histogram {
	return &Histogram{NewFamily(name, help, nil, bounds, 1)}
}
func (h *Histogram) Observe(d time.Duration) {
	if h != nil && d >= 0 {
		h.ObserveValue(d.Seconds())
	}
}
func (h *Histogram) ObserveValue(value float64) {
	if h != nil {
		h.family.Observe(value)
	}
}
func (h *Histogram) Write(w io.Writer) {
	if h != nil {
		h.family.Write(w)
	}
}
func formatBound(b float64) string {
	if math.IsInf(b, 1) {
		return "+Inf"
	}
	return strconv.FormatFloat(b, 'g', -1, 64)
}

// Gauge renders the established Prometheus representation. RegisterGauges
// supplies live observations to the OTLP reader independently of scrapes.
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
type ChangePrune struct {
	once             sync.Once
	pruned, failures *Counter
}

func (p *ChangePrune) init() {
	p.once.Do(func() {
		p.pruned = NewCounter("quivr_change_events_pruned_total", "Change events physically deleted after the retention window.", nil, nil)
		p.failures = NewCounter("quivr_change_prune_failures_total", "Change-journal prune passes that failed.", nil, nil)
	})
}
func (p *ChangePrune) Pruned(n int) {
	if p != nil {
		p.init()
		p.pruned.Add(n)
	}
}
func (p *ChangePrune) Failed() {
	if p != nil {
		p.init()
		p.failures.Inc()
	}
}
func (p *ChangePrune) Write(w io.Writer) {
	if p != nil {
		p.init()
		p.pruned.Write(w)
		p.failures.Write(w)
	}
}
