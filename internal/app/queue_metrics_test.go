package app

import (
	"context"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type metricQueueRows struct{ fail bool }

func (s metricQueueRows) QueueBacklog(context.Context) ([]workqueue.Status, error) {
	if s.fail {
		return nil, errors.New("offline")
	}
	return []workqueue.Status{{Queue: "bulk", Waiting: 123, InProgress: 4, OldestAgeSeconds: 5.5}, {Queue: "live"}}, nil
}

// The exported series names/labels are the independent Prometheus scaler contract.
func TestQueueMetricsExposeNumericClassGaugesAndFailClosed(t *testing.T) {
	for _, fail := range []bool{false, true} {
		res := httptest.NewRecorder()
		queueMetrics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("quivr_buildinfo 1\n")) }), metricQueueRows{fail}).ServeHTTP(res, httptest.NewRequest("GET", "/metrics", nil))
		if fail {
			if res.Code != 200 || !strings.Contains(res.Body.String(), "quivr_buildinfo 1") || strings.Contains(res.Body.String(), "quivr_queue_waiting_documents{") {
				t.Fatalf("failed snapshot advertised zero: %d %s", res.Code, res.Body.String())
			}
			continue
		}
		for _, want := range []string{`quivr_queue_waiting_documents{queue="bulk"} 123`, `quivr_queue_in_progress_documents{queue="bulk"} 4`, `quivr_queue_oldest_waiting_age_seconds{queue="bulk"} 5.5`, `quivr_queue_waiting_documents{queue="live"} 0`} {
			if !strings.Contains(res.Body.String(), want) {
				t.Fatalf("missing %s in %s", want, res.Body.String())
			}
		}
	}
}
