package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

// Literal deployment keys, omission, and environment override own hydration
// blob cache sizing and startup bounds.
func TestHydrationBlobCacheConfiguration(t *testing.T) {
	t.Setenv(hydrationBlobCacheBytesEnv, "")
	for _, tc := range []struct {
		input string
		want  int64
		valid bool
	}{
		{`{}`, content.DefaultHydrationBlobCacheBytes, true},
		{`{"retrieval":{"hydration_blob_cache_bytes":0}}`, 0, true},
		{`{"retrieval":{"hydration_blob_cache_bytes":1048576}}`, 1048576, true},
		{`{"retrieval":{"hydration_blob_cache_bytes":1073741824}}`, content.MaxHydrationBlobCacheBytes, true},
		{`{"retrieval":{"hydration_blob_cache_bytes":-1}}`, 0, false},
		{`{"retrieval":{"hydration_blob_cache_bytes":1073741825}}`, 0, false},
	} {
		var cfg Config
		if err := json.Unmarshal([]byte(tc.input), &cfg); err != nil {
			t.Fatal(err)
		}
		got, err := cfg.Retrieval.hydrationBlobCacheBytes()
		if (err == nil) != tc.valid || (tc.valid && got != tc.want) {
			t.Fatalf("config %s: bytes=%d error=%v, want=%d valid=%v", tc.input, got, err, tc.want, tc.valid)
		}
	}
	t.Run("env_override", func(t *testing.T) {
		t.Setenv(hydrationBlobCacheBytesEnv, "268435456")
		var cfg Config
		got, err := cfg.Retrieval.hydrationBlobCacheBytes()
		if err != nil || got != 268435456 {
			t.Fatalf("env override: bytes=%d err=%v, want 268435456", got, err)
		}
		t.Setenv(hydrationBlobCacheBytesEnv, "invalid")
		if _, err := cfg.Retrieval.hydrationBlobCacheBytes(); err == nil {
			t.Fatal("invalid env override accepted")
		}
		t.Setenv(hydrationBlobCacheBytesEnv, "-5")
		if _, err := cfg.Retrieval.hydrationBlobCacheBytes(); err == nil {
			t.Fatal("negative env override accepted")
		}
	})
}

// API /metrics must expose hydration blob cache counters and gauges alongside
// command and backlog metrics.
func TestAPIMetricsExposeHydrationBlobCache(t *testing.T) {
	cache := content.NewHydrationBlobCache(4096)
	rec := httptest.NewRecorder()
	apiMetrics(telemetry.NewCommands(), func(context.Context) (int64, time.Duration, error) {
		return 0, 0, nil
	}, cache.WriteMetrics).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`quivr_hydration_blob_cache_lookups_total{result="hit"} 0`,
		`quivr_hydration_blob_cache_lookups_total{result="miss"} 0`,
		`quivr_hydration_blob_cache_evictions_total 0`,
		`quivr_hydration_blob_cache_bytes 0`,
		`quivr_hydration_blob_cache_max_bytes 4096`,
		`quivr_hydration_blob_cache_entries 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in\n%s", want, body)
		}
	}
}
