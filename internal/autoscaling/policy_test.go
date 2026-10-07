package autoscaling_test

import (
	"math"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/autoscaling"
)

// This is the owner of backlog rounding/clamps and downscale stabilization.
// Literal outcomes come from the deployment policy, not the implementation.
func TestPolicy(t *testing.T) {
	t.Run("validate configuration", func(t *testing.T) {
		for _, c := range []autoscaling.Config{
			{Min: 0, Max: 8, DocumentsPerReplica: 20000},
			{Min: 2, Max: 1, DocumentsPerReplica: 20000},
			{Min: 1, Max: 8, DocumentsPerReplica: 0},
			{Min: 1, Max: 8, DocumentsPerReplica: 20000, DownscaleWindow: -time.Second},
		} {
			if _, err := autoscaling.NewPolicy(c); err == nil {
				t.Fatalf("accepted invalid config %+v", c)
			}
		}
	})
	t.Run("custom bounds and zero capacity recovery", func(t *testing.T) {
		for _, tc := range []struct {
			waiting       int64
			current, want int
		}{{0, 0, 2}, {1, 2, 2}, {90000, 2, 3}} {
			p, err := autoscaling.NewPolicy(autoscaling.Config{Min: 2, Max: 3, DocumentsPerReplica: 20000})
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.Decide(tc.waiting, tc.current, time.Unix(1000, 0))
			if err != nil || got != tc.want {
				t.Fatalf("waiting %d/current %d: want %d, got %d/error %v", tc.waiting, tc.current, tc.want, got, err)
			}
		}
	})
	for _, tc := range []struct {
		waiting int64
		want    int
	}{
		{0, 1}, {1, 1}, {20000, 1}, {20001, 2}, {40000, 2}, {140001, 8}, {1000000, 8}, {math.MaxInt64, 8},
	} {
		p, err := autoscaling.NewPolicy(autoscaling.Config{Min: 1, Max: 8, DocumentsPerReplica: 20000})
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.Decide(tc.waiting, 1, time.Unix(1000, 0))
		if err != nil || got != tc.want {
			t.Fatalf("waiting %d: want %d, got %d, error %v", tc.waiting, tc.want, got, err)
		}
	}
	t.Run("stabilize highest recent recommendation", func(t *testing.T) {
		p, err := autoscaling.NewPolicy(autoscaling.Config{Min: 1, Max: 8, DocumentsPerReplica: 20000, DownscaleWindow: 5 * time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		start := time.Unix(1000, 0)
		for _, tc := range []struct {
			seconds       int
			waiting       int64
			current, want int
		}{
			{0, 0, 4, 4}, {299, 0, 4, 4}, {300, 40001, 4, 3}, {301, 0, 3, 3},
			{400, 140001, 3, 8}, {401, 0, 8, 8}, {700, 0, 8, 1},
		} {
			got, err := p.Decide(tc.waiting, tc.current, start.Add(time.Duration(tc.seconds)*time.Second))
			if err != nil || got != tc.want {
				t.Fatalf("second %d: want %d, got %d, error %v", tc.seconds, tc.want, got, err)
			}
		}
	})
}
