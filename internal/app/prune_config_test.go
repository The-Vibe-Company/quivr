package app

import (
	"slices"
	"testing"
	"time"
)

func TestChangePruneConfigSharesTheAPIRetentionUnlessExplicitlyConfined(t *testing.T) {
	week := 7 * 24 * time.Hour
	p, err := ChangePruneConfig{}.parse(week)
	if err != nil || p.Interval != time.Minute || p.Retention != week || len(p.Organizations) != 0 {
		t.Fatalf("defaults %+v %v", p, err)
	}
	// A longer prune retention only keeps events longer than the API promises.
	p, err = ChangePruneConfig{Interval: "5m", Retention: "240h"}.parse(week)
	if err != nil || p.Interval != 5*time.Minute || p.Retention != 240*time.Hour {
		t.Fatalf("longer retention %+v %v", p, err)
	}
	// The harness confines a short retention to named Organizations, explicitly.
	p, err = ChangePruneConfig{Interval: "1s", Retention: "2s", Organizations: []string{"org_r"}, AllowShortRetention: true}.parse(week)
	if err != nil || p.Retention != 2*time.Second || !slices.Equal(p.Organizations, []string{"org_r"}) {
		t.Fatalf("confined short retention %+v %v", p, err)
	}
	for name, bad := range map[string]ChangePruneConfig{
		"shorter than the API retention":         {Retention: "1h"},
		"short retention for every Organization": {Retention: "1h", AllowShortRetention: true},
		"short retention without the override":   {Retention: "1h", Organizations: []string{"org_r"}},
		"zero interval":                          {Interval: "0s"},
		"bad interval":                           {Interval: "soon"},
		"bad retention":                          {Retention: "-1h"},
		"empty Organization":                     {Organizations: []string{""}},
	} {
		if _, err = bad.parse(week); err == nil {
			t.Fatalf("accepted %s: %+v", name, bad)
		}
	}
}
