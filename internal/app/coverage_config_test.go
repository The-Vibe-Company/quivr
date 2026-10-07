package app

import (
	"encoding/json"
	"testing"
	"time"
)

// Literal deployment keys and omission own refresh pacing and startup bounds.
func TestCoverageSnapshotConfiguration(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  time.Duration
		valid bool
	}{
		{`{}`, 10 * time.Second, true},
		{`{"retrieval":{"coverage_refresh":"100ms"}}`, 100 * time.Millisecond, true},
		{`{"retrieval":{"coverage_refresh":"0s"}}`, 0, false},
		{`{"retrieval":{"coverage_refresh":"-1s"}}`, 0, false},
		{`{"retrieval":{"coverage_refresh":"invalid"}}`, 0, false},
	} {
		var cfg Config
		if err := json.Unmarshal([]byte(tc.input), &cfg); err != nil {
			t.Fatal(err)
		}
		got, err := cfg.Retrieval.coverageRefresh()
		if (err == nil) != tc.valid || (tc.valid && got != tc.want) {
			t.Fatalf("config %s: refresh=%s error=%v, want=%s valid=%v", tc.input, got, err, tc.want, tc.valid)
		}
	}
}
