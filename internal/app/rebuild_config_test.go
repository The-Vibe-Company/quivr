package app

import (
	"encoding/json"
	"testing"
)

// Literal deployment keys and omission protect parsing and startup bounds.
func TestRebuildConcurrencyConfiguration(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int
		valid bool
	}{
		{`{}`, 8, true},
		{`{"rebuild":{"concurrency":0}}`, 8, true},
		{`{"rebuild":{"concurrency":1}}`, 1, true},
		{`{"rebuild":{"concurrency":256}}`, 256, true},
		{`{"rebuild":{"concurrency":-1}}`, 0, false},
		{`{"rebuild":{"concurrency":257}}`, 0, false},
	} {
		var cfg Config
		if err := json.Unmarshal([]byte(tc.input), &cfg); err != nil {
			t.Fatal(err)
		}
		got, err := cfg.Rebuild.concurrency()
		if (err == nil) != tc.valid || (tc.valid && got != tc.want) {
			t.Fatalf("config %s: concurrency=%d error=%v, want=%d valid=%v", tc.input, got, err, tc.want, tc.valid)
		}
	}
}
