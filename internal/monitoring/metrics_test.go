package monitoring_test

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
)

func TestDeliveryMetricsExposeOutcomesAndBacklog(t *testing.T) {
	m := &monitoring.DeliveryMetrics{}
	for _, o := range []string{monitoring.AttemptAcknowledged, monitoring.AttemptRetryableError, monitoring.AttemptRetryableError, monitoring.AttemptUnknown, "not-an-outcome"} {
		m.Observe(o)
	}
	m.ObserveDuration(120 * time.Millisecond)
	m.Extra = func(w io.Writer) { io.WriteString(w, "quivr_extra 1\n") }
	var nilMetrics *monitoring.DeliveryMetrics
	nilMetrics.Observe(monitoring.AttemptAcknowledged) // nil-safe
	nilMetrics.ObserveDuration(time.Second)
	h := m.Handler(func(context.Context) (monitoring.DeliveryBacklog, error) {
		return monitoring.DeliveryBacklog{Pending: 3, OldestAge: 90 * time.Second}, nil
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	text := string(body)
	for _, want := range []string{
		`quivr_delivery_attempts_total{outcome="acknowledged"} 1`,
		`quivr_delivery_attempts_total{outcome="retryable_error"} 2`,
		`quivr_delivery_attempts_total{outcome="permanent_error"} 0`,
		"quivr_delivery_pending 3",
		"quivr_delivery_oldest_pending_age_seconds 90",
		"# TYPE quivr_delivery_attempts_total counter",
		"# TYPE quivr_delivery_pending gauge",
		`quivr_delivery_request_duration_seconds_bucket{le="0.1"} 0`,
		`quivr_delivery_request_duration_seconds_bucket{le="0.25"} 1`,
		"quivr_delivery_request_duration_seconds_count 1",
		"quivr_extra 1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "not-an-outcome") || strings.Contains(text, `outcome="unknown"`) || rec.Header().Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("unbounded label or content type: %s %q", text, rec.Header().Get("Content-Type"))
	}
	// A backlog failure still serves counters and omits the gauges.
	rec = httptest.NewRecorder()
	m.Handler(func(context.Context) (monitoring.DeliveryBacklog, error) {
		return monitoring.DeliveryBacklog{}, errors.New("db down")
	}).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if b := rec.Body.String(); !strings.Contains(b, "quivr_delivery_attempts_total") || strings.Contains(b, "quivr_delivery_pending ") || strings.Contains(b, "db down") {
		t.Fatalf("backlog failure: %s", b)
	}
}
