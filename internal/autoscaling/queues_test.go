package autoscaling_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/autoscaling"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestQueueSourceRequiresNumericWaiting(t *testing.T) {
	for _, tc := range []struct {
		name, queue, body string
		status            int
		want              int64
		bad               bool
	}{
		{"stable JSON", "bulk", `{"queues":{"live":{"waiting":999},"bulk":{"waiting":20001,"in_progress":5}}}`, 200, 20001, false},
		{"live", "live", `{"queues":{"live":{"waiting":7},"bulk":{"waiting":20001}}}`, 200, 7, false},
		{"zero", "bulk", `{"queues":{"bulk":{"waiting":0}}}`, 200, 0, false},
		{"missing", "bulk", `{"queues":{"live":{"waiting":3}}}`, 200, 0, true},
		{"missing waiting", "bulk", `{"queues":{"bulk":{}}}`, 200, 0, true},
		{"null", "bulk", `{"queues":{"bulk":{"waiting":null}}}`, 200, 0, true},
		{"negative", "bulk", `{"queues":{"bulk":{"waiting":-1}}}`, 200, 0, true},
		{"string", "bulk", `{"queues":{"bulk":{"waiting":"3"}}}`, 200, 0, true},
		{"fraction", "bulk", `{"queues":{"bulk":{"waiting":1.5}}}`, 200, 0, true},
		{"unavailable", "bulk", `sensitive upstream body`, 503, 0, true},
		{"unauthorized", "bulk", `sensitive upstream body`, 401, 0, true},
		{"invalid", "bulk", `sensitive upstream body`, 200, 0, true},
		{"trailing", "bulk", `{"queues":{"bulk":{"waiting":0}}} {}`, 200, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.String() != "http://api.example.org/v0/admin/queues" || r.Header.Get("Authorization") != "Bearer fixture-key" {
					t.Fatalf("unexpected queue request %s %s", r.Method, r.URL.Path)
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			source := autoscaling.QueueSource{URL: "http://api.example.org/v0/admin/queues", Key: " fixture-key ", Queue: tc.queue, Client: client}
			got, err := source.Waiting(context.Background())
			if (err != nil) != tc.bad || (!tc.bad && got != tc.want) {
				t.Fatalf("want count %d/error %v, got %d/%v", tc.want, tc.bad, got, err)
			}
			if err != nil && strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("response leaked: %v", err)
			}
		})
	}
	t.Run("network error redaction", func(t *testing.T) {
		source := autoscaling.QueueSource{URL: "http://api.example.org/v0/admin/queues", Key: "fixture-key", Queue: "bulk", Client: &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) { return nil, errors.New("sensitive network error") })}}
		_, err := source.Waiting(context.Background())
		if err == nil || strings.Contains(err.Error(), "sensitive") {
			t.Fatalf("unsafe error %v", err)
		}
	})
}
