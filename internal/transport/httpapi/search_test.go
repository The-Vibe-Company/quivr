package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

// A search's usage reports each phase under its own name, in whole
// milliseconds rounded down (THE-873).
func TestSearchUsageNamesEachPhase(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n)*time.Millisecond + 900*time.Microsecond }
	u := retrieval.Usage{Profiles: []retrieval.ProfileUsage{{Profile: "example.rerank/default", PluginVersion: "1.0.0", Rounds: 2, PaidCalls: 1, CostCents: 0.25}, {Profile: "core.retrieve/default", PluginVersion: "2.0.0", Rounds: 2, PaidCalls: 2, CostCents: 0.125}}, Rounds: 2, Elapsed: ms(90), Phases: retrieval.Phases{Routing: ms(1), Coverage: ms(2), PluginRounds: ms(3), QueryEncoding: ms(4), IndexQuery: ms(5), Hydration: ms(6)}}
	b, err := json.Marshal(usageToTransport(u))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ElapsedMS int            `json:"elapsed_ms"`
		Phases    map[string]int `json:"phases"`
		Profiles  []struct {
			Profile       string  `json:"profile"`
			PluginVersion string  `json:"plugin_version"`
			PaidCalls     int     `json:"paid_calls"`
			CostCents     float64 `json:"cost_cents"`
			Rounds        int     `json:"rounds"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"routing_ms": 1, "coverage_ms": 2, "plugin_rounds_ms": 3, "query_encoding_ms": 4, "index_query_ms": 5, "hydration_ms": 6}
	if got.ElapsedMS != 90 || len(got.Phases) != len(want) {
		t.Fatalf("usage %s; want elapsed_ms 90 and phases %v", b, want)
	}
	for name, v := range want {
		if got.Phases[name] != v {
			t.Fatalf("usage %s; want phases %v", b, want)
		}
	}
	if len(got.Profiles) != 2 || got.Profiles[0].Profile != "example.rerank/default" || got.Profiles[1].PluginVersion != "2.0.0" || got.Profiles[0].CostCents != 0.25 || got.Profiles[1].PaidCalls != 2 || got.Profiles[1].Rounds != 2 {
		t.Fatalf("per-profile usage %s", b)
	}

}
