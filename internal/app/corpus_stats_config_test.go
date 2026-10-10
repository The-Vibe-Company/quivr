package app

import (
	"encoding/json"
	"testing"
)

// The operator gate is opt-in: upgrading a mixed writer fleet must not
// silently certify historical aggregates. Literal keys protect that contract.
func TestCorpusStatsInitializationConfiguration(t *testing.T) {
	for _, tc := range []struct {
		input   string
		enabled bool
	}{
		{`{}`, false},
		{`{"corpus_stats":{"bootstrap_enabled":false}}`, false},
		{`{"corpus_stats":{"bootstrap_enabled":true}}`, true},
	} {
		var cfg Config
		if err := json.Unmarshal([]byte(tc.input), &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.CorpusStats.BootstrapEnabled != tc.enabled {
			t.Fatalf("configuration %s: bootstrap_enabled=%t, want %t", tc.input, cfg.CorpusStats.BootstrapEnabled, tc.enabled)
		}
	}
}
