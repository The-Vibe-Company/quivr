package app

import (
	"encoding/json"
	"testing"
)

// Deployment keys and omitted defaults are an external configuration contract.
func TestWorkerQueueConfiguration(t *testing.T) {
	for _, tc := range []struct {
		raw        string
		queues     []string
		live, bulk int
		valid      bool
	}{
		{`{}`, []string{"live", "bulk"}, 4, 4, true},
		{`{"worker":{"queues":["live"],"slots":{"live":2,"bulk":7}}}`, []string{"live"}, 2, 7, true},
		{`{"worker":{"queues":["bulk"],"slots":{"bulk":1}}}`, []string{"bulk"}, 4, 1, true},
		{`{"worker":{"queues":[]}}`, nil, 0, 0, false},
		{`{"worker":{"queues":["unknown"]}}`, nil, 0, 0, false},
		{`{"worker":{"queues":["live","live"]}}`, nil, 0, 0, false},
		{`{"worker":{"slots":{"live":-1}}}`, nil, 0, 0, false},
		{`{"worker":{"slots":{"bulk":1025}}}`, nil, 0, 0, false},
	} {
		var cfg Config
		if err := json.Unmarshal([]byte(tc.raw), &cfg); err != nil {
			t.Fatal(err)
		}
		got, err := cfg.Worker.Resolve()
		if (err == nil) != tc.valid {
			t.Fatalf("%s: got %v", tc.raw, err)
		}
		if !tc.valid {
			continue
		}
		if len(got.Queues) != len(tc.queues) || got.Slots.Live != tc.live || got.Slots.Bulk != tc.bulk {
			t.Fatalf("%s: got %+v", tc.raw, got)
		}
		for i, q := range tc.queues {
			if got.Queues[i] != q {
				t.Fatalf("%s: queues %v", tc.raw, got.Queues)
			}
		}
	}
}
