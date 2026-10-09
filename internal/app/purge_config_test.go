package app

import (
	"encoding/json"
	"testing"
	"time"
)

// Literal deployment keys, omission and startup bounds own this configuration.
func TestProjectionPurgeTimeoutConfiguration(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		want  time.Duration
		valid bool
	}{
		{`{}`, time.Minute, true},
		{`{"projection_purge_timeout":"75s"}`, 75 * time.Second, true},
		{`{"projection_purge_timeout":"120s"}`, 2 * time.Minute, true},
		{`{"projection_purge_timeout":"0s"}`, 0, false},
		{`{"projection_purge_timeout":"-1s"}`, 0, false},
		{`{"projection_purge_timeout":"121s"}`, 0, false},
		{`{"projection_purge_timeout":"invalid"}`, 0, false},
	} {
		var cfg Config
		if err := json.Unmarshal([]byte(tc.raw), &cfg); err != nil {
			t.Fatal(err)
		}
		got, err := cfg.purgeTimeout()
		if (err == nil) != tc.valid || (tc.valid && got != tc.want) {
			t.Fatalf("config %s: timeout=%s err=%v, want %s valid=%v", tc.raw, got, err, tc.want, tc.valid)
		}
	}
}
