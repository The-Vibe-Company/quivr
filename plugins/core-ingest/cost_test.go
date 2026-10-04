package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

func quantiles(samples []time.Duration) (time.Duration, time.Duration) {
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2], sorted[(len(sorted)*95+99)/100-1]
}

// TestTokenizerQueryCost records what the pinned tokenizer costs a search now
// that it lives in this plugin, loaded once (THE-675, THE-777): its start, the
// token check of one query, and one embed_query through the SDK up to the
// point where TEI is called. It only logs: numbers are evidence, not a
// threshold, so it runs in the measurement lane
// (.github/workflows/measure-adapters.yml), never in make verify.
func TestTokenizerQueryCost(t *testing.T) {
	path := os.Getenv("QUIVR_CORE_INGEST_CONFIG")
	if os.Getenv("QUIVR_MEASURE") == "" || path == "" {
		t.Skip("measurement: set QUIVR_MEASURE=1 and QUIVR_CORE_INGEST_CONFIG (measure-adapters workflow)")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c configuration
	if err = json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	server := &Server{Config: c.Tokenizer}
	defer server.Close()
	windows := TokenWindows{Tokenizer: server}
	ctx := context.Background()
	start := time.Now()
	if _, err := windows.NormalizeQuery(ctx, "inflation zone euro"); err != nil {
		t.Fatal(err)
	}
	t.Logf("THE-777 tokenizer start in the plugin: %s", time.Since(start).Round(time.Millisecond))
	samples := make([]time.Duration, 0, 200)
	for range 200 {
		start := time.Now()
		if _, err := windows.NormalizeQuery(ctx, "inflation zone euro"); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, time.Since(start))
	}
	p50, p95 := quantiles(samples)
	t.Logf("THE-777 query token check: n=%d p50=%s p95=%s", len(samples), p50.Round(time.Microsecond), p95.Round(time.Microsecond))
	// One embed_query through the SDK with TEI unreachable: the request, the
	// checks and the token count, without the model.
	plugin, err := quivrplugin.New("quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	impl := &ingester{backends: map[string]*backend{}}
	_ = plugin.Ingestion(impl)
	handler, _ := plugin.Handler()
	c.TEIURL = "http://127.0.0.1:9"
	body, _ := json.Marshal(map[string]any{"invocation_id": "cost", "contribution": "ingestion", "organization_id": "org", "configuration": c,
		"space": space, "query": map[string]string{"modality": "text", "text": "inflation zone euro"}})
	samples = samples[:0]
	for range 50 {
		start := time.Now()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v0/contributions/ingestion/embed_query", bytes.NewReader(body)))
		samples = append(samples, time.Since(start))
	}
	p50, p95 = quantiles(samples)
	t.Logf("THE-777 embed_query without TEI: n=%d p50=%s p95=%s", len(samples), p50.Round(time.Microsecond), p95.Round(time.Microsecond))
}
