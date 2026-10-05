package monitoring

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

// DeliveryBacklog is the admissible scheduled delivery work: Deliveries that
// are pending or delivering with work due or waiting for a retry. Work parked
// because admission refused it (disabled, withdrawn, destination unavailable)
// is not stuck work and is excluded.
type DeliveryBacklog struct {
	Pending   int64
	OldestAge time.Duration
}

// attemptOutcomes is the bounded label set of the attempt counter: the
// outcomes a request can have. An unknown outcome is inferred from a lost
// lease, never observed by a request, and is read from attempt history.
var attemptOutcomes = []string{AttemptAcknowledged, AttemptRetryableError, AttemptPermanentError}

// DeliveryMetrics counts the outcomes of webhook requests this process sent.
// Its zero value is ready; a nil receiver ignores observations.
type DeliveryMetrics struct {
	counts *telemetry.Counter
	// Extra renders further process metrics on the same endpoint (processing, THE-662).
	Extra    func(io.Writer)
	once     sync.Once
	duration *telemetry.Histogram
}

func (m *DeliveryMetrics) histogram() *telemetry.Histogram {
	m.once.Do(func() {
		m.counts = telemetry.NewCounter("quivr_delivery_attempts_total", "Outcomes of webhook delivery requests sent by this process.", []string{"outcome"}, []string{AttemptAcknowledged}, []string{AttemptRetryableError}, []string{AttemptPermanentError})
		m.duration = telemetry.NewHistogram("quivr_delivery_request_duration_seconds", "Duration of webhook delivery requests with a known outcome.", 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30)
	})
	return m.duration
}

// ObserveDuration records one request's duration; a nil receiver ignores it.
func (m *DeliveryMetrics) ObserveDuration(d time.Duration) {
	if m != nil {
		m.histogram().Observe(d)
	}
}

// Observe counts one request outcome; other labels are dropped.
func (m *DeliveryMetrics) Observe(outcome string) {
	if m == nil {
		return
	}
	m.histogram()
	m.counts.Inc(outcome)
}

// Handler serves the counters and backlog gauges in the Prometheus text
// format. A backlog read failure omits the gauges rather than reporting zero.
func (m *DeliveryMetrics) Handler(backlog func(context.Context) (DeliveryBacklog, error)) http.Handler {
	gauges := []telemetry.GaugeDefinition{{Name: "quivr_delivery_pending", Help: "Admissible Deliveries awaiting an attempt or a retry."}, {Name: "quivr_delivery_oldest_pending_age_seconds", Help: "Age of the oldest admissible pending Delivery."}}
	telemetry.RegisterGauges(gauges, func(ctx context.Context) ([]float64, error) {
		b, err := backlog(ctx)
		return []float64{float64(b.Pending), b.OldestAge.Seconds()}, err
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		m.histogram()
		m.counts.Write(w)
		m.histogram().Write(w)
		if m.Extra != nil {
			m.Extra(w)
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		b, err := backlog(ctx)
		if err != nil {
			return
		}
		telemetry.Gauge(w, gauges[0].Name, gauges[0].Help, float64(b.Pending))
		telemetry.Gauge(w, gauges[1].Name, gauges[1].Help, b.OldestAge.Seconds())
	})
}

// EvaluationMetrics observes how evaluation reaches evaluators: calls per
// evaluated Record Version (per step group), and distinct evaluations and
// Subscriptions per call, which shows batching and deduplication. A nil
// receiver ignores observations.
type EvaluationMetrics struct {
	once                                             sync.Once
	callsPerVersion, expressionsPerCall, subsPerCall *telemetry.Histogram
}

func (m *EvaluationMetrics) init() {
	m.once.Do(func() {
		counts := []float64{0, 1, 2, 4, 8, 16, 32, 64, 128, 256}
		m.callsPerVersion = telemetry.NewHistogram("quivr_evaluation_calls_per_record_version", "Evaluator calls made to decide the due evaluations of one Record Version and evaluator.", counts...)
		m.expressionsPerCall = telemetry.NewHistogram("quivr_evaluation_expressions_per_call", "Distinct evaluations (expression and configuration) sent in one evaluator call.", counts[1:]...)
		m.subsPerCall = telemetry.NewHistogram("quivr_evaluation_subscriptions_per_call", "Subscription Versions decided by one evaluator call after deduplication.", counts[1:]...)
	})
}

func (m *EvaluationMetrics) observeRecordVersion(calls int) {
	if m != nil {
		m.init()
		m.callsPerVersion.ObserveValue(float64(calls))
	}
}

func (m *EvaluationMetrics) observeCall(expressions, subscriptions int) {
	if m != nil {
		m.init()
		m.expressionsPerCall.ObserveValue(float64(expressions))
		m.subsPerCall.ObserveValue(float64(subscriptions))
	}
}

// Write renders the histograms in the Prometheus text format.
func (m *EvaluationMetrics) Write(w io.Writer) {
	if m == nil {
		return
	}
	m.init()
	m.callsPerVersion.Write(w)
	m.expressionsPerCall.Write(w)
	m.subsPerCall.Write(w)
}
