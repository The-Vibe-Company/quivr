package app

import "testing"

// Deployment caps are validated before any worker or provider is contacted.
func TestBackfillConcurrencyConfiguration(t *testing.T) {
	for _, tc := range []struct {
		input, want int
		valid       bool
	}{{0, 4, true}, {1, 1, true}, {32, 32, true}, {-1, 0, false}, {33, 0, false}} {
		got, err := (BackfillConfig{Concurrency: tc.input}).settings()
		if (err == nil) != tc.valid || (tc.valid && got.Concurrency != tc.want) {
			t.Fatalf("concurrency %d: got %+v, error %v", tc.input, got, err)
		}
	}
}
