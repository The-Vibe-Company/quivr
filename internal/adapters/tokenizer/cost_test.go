package tokenizer_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sort"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tokenizer"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
)

// pinnedConfig loads the prepared tokenizer configuration used by make verify.
func pinnedConfig(t testing.TB) tokenizer.Config {
	t.Helper()
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("make verify pinned tokenizer")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Tokenizer tokenizer.Config `json:"tokenizer"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.Tokenizer
}

// queryBatch is exactly what NormalizeQuery sends for one public search.
var queryBatch = []processing.TokenInput{{Text: "inflation zone euro"}, {Text: "query: inflation zone euro", Special: true}}

func quantiles(samples []time.Duration) (time.Duration, time.Duration) {
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2], sorted[(len(sorted)*95+99)/100-1]
}

// TestTokenizerQueryCost records the per-search tokenizer cost on the host (THE-675).
// It only logs: numbers are evidence, not a pass/fail threshold. It is a
// measurement, so it runs in the measurement lane (.github/workflows/measure-adapters.yml),
// nightly and on changes to the tokenizer, never in make verify.
func TestTokenizerQueryCost(t *testing.T) {
	if os.Getenv("QUIVR_MEASURE") == "" {
		t.Skip("measurement: set QUIVR_MEASURE=1 (measure-adapters workflow)")
	}
	cfg := pinnedConfig(t)
	var encoder processing.Tokenizer = tokenizer.Encoder{Config: cfg}
	samples := make([]time.Duration, 0, 20)
	for range 20 {
		start := time.Now()
		if _, err := encoder.Encode(context.Background(), queryBatch); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, time.Since(start))
	}
	p50, p95 := quantiles(samples)
	t.Logf("THE-675 one-shot query encode: n=%d p50=%s p95=%s", len(samples), p50.Round(time.Millisecond), p95.Round(time.Millisecond))
	// Phase breakdown inside one pinned helper invocation, using the same interpreter and tokenizer.json.
	phases := `
import time
t0=time.perf_counter()
import hashlib,pathlib,sys,tokenizers
from tokenizers import Tokenizer
t1=time.perf_counter()
data=pathlib.Path(sys.argv[1]).read_bytes();hashlib.sha256(data).hexdigest()
t2=time.perf_counter()
tk=Tokenizer.from_str(data.decode('utf-8'));tk.no_truncation();tk.no_padding()
t3=time.perf_counter()
tk.encode('inflation zone euro',add_special_tokens=False);tk.encode('query: inflation zone euro',add_special_tokens=True)
t4=time.perf_counter()
print('import_ms=%.1f read_sha_ms=%.1f from_str_ms=%.1f encode_ms=%.3f tokenizer_bytes=%d'%((t1-t0)*1e3,(t2-t1)*1e3,(t3-t2)*1e3,(t4-t3)*1e3,len(data)))
`
	for i := range 3 {
		start := time.Now()
		out, err := exec.Command(cfg.Python, "-c", phases, cfg.Model).Output()
		if err != nil {
			t.Fatal("phase probe failed")
		}
		t.Logf("THE-675 phases run %d: wall_ms=%d %s", i, time.Since(start).Milliseconds(), out)
	}
	server := &tokenizer.Server{Config: cfg}
	defer server.Close()
	start := time.Now()
	if _, err := server.Encode(context.Background(), queryBatch); err != nil {
		t.Fatal(err)
	}
	t.Logf("THE-675 persistent tokenizer start: %s", time.Since(start).Round(time.Millisecond))
	samples = samples[:0]
	for range 200 {
		start := time.Now()
		if _, err := server.Encode(context.Background(), queryBatch); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, time.Since(start))
	}
	p50, p95 = quantiles(samples)
	t.Logf("THE-675 persistent query encode: n=%d p50=%s p95=%s", len(samples), p50.Round(time.Microsecond), p95.Round(time.Microsecond))
	start = time.Now()
	if err := exec.Command(cfg.Python, "-c", "pass").Run(); err != nil {
		t.Fatal(err)
	}
	t.Logf("THE-675 bare interpreter start: %s", time.Since(start).Round(time.Millisecond))
}

func BenchmarkTokenizerQueryEncode(b *testing.B) {
	var encoder processing.Tokenizer = tokenizer.Encoder{Config: pinnedConfig(b)}
	for b.Loop() {
		if _, err := encoder.Encode(context.Background(), queryBatch); err != nil {
			b.Fatal(err)
		}
	}
}
