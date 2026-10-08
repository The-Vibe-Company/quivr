package app

import (
	"encoding/json"
	"testing"
	"time"
)

// Literal external keys and omission own installation refresh defaults/bounds.
func TestQueueObservationConfiguration(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  time.Duration
		valid bool
	}{
		{`{}`, 15 * time.Second, true},
		{`{"queue_observation":{"refresh_interval":"1s"}}`, time.Second, true},
		{`{"queue_observation":{"refresh_interval":"60s"}}`, time.Minute, true},
		{`{"queue_observation":{"refresh_interval":"20s"}}`, 20 * time.Second, true},
		{`{"queue_observation":{"refresh_interval":"999ms"}}`, 0, false},
		{`{"queue_observation":{"refresh_interval":"61s"}}`, 0, false},
		{`{"queue_observation":{"refresh_interval":"0s"}}`, 0, false},
		{`{"queue_observation":{"refresh_interval":"-1s"}}`, 0, false},
		{`{"queue_observation":{"refresh_interval":"invalid"}}`, 0, false},
	} {
		var cfg Config
		if err := json.Unmarshal([]byte(tc.input), &cfg); err != nil {
			t.Fatal(err)
		}
		got, err := cfg.QueueObservation.Resolve()
		if (err == nil) != tc.valid || (tc.valid && got != tc.want) {
			t.Fatalf("config %s: refresh=%s error=%v, want=%s valid=%v", tc.input, got, err, tc.want, tc.valid)
		}
	}
}
