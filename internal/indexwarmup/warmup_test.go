package indexwarmup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// This owns the deployment's read-only warming contract. A fixed store response
// supplies data; only the warmer chooses the active tenant, probes, and retries.
func TestWarmingDiscoversImportedSpacesAndRetriesWithoutActivatingTenants(t *testing.T) {
	imported := false
	var queries []string
	var probeMu sync.Mutex
	var cutProbe context.CancelFunc
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-test-key" {
			t.Error("warm-up did not authenticate")
		}
		if r.Method != http.MethodGet && r.URL.Path != "/v1/graphql" {
			t.Errorf("unexpected write: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/schema":
			if !imported {
				w.Write([]byte(`{"classes":[]}`))
				return
			}
			w.Write([]byte(`{"classes":[{"class":"Articles","multiTenancyConfig":{"enabled":true},"vectorConfig":{"primary":{"vectorIndexType":"hnsw"},"secondary":{"vectorIndexType":"hnsw"}},"properties":[{"name":"text","dataType":["text"],"indexSearchable":true}]}]}`))
		case "/v1/schema/Articles/tenants":
			w.Write([]byte(`[{"name":"active","activityStatus":"HOT"},{"name":"retired","activityStatus":"COLD"}]`))
		case "/v1/objects":
			if r.URL.Query().Get("tenant") != "active" || r.URL.Query().Get("include") != "vector" {
				t.Errorf("wrong sample scope: %s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"objects":[{"id":"00000000-0000-0000-0000-000000000001","properties":{"text":"Harbour ferry opens"},"vectors":{"primary":[1,0],"secondary":[0,1]}}]}`))
		case "/v1/graphql":
			var body struct {
				Query string `json:"query"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			probeMu.Lock()
			queries = append(queries, body.Query)
			count, cut := len(queries), cutProbe
			probeMu.Unlock()
			if cut != nil {
				cut() // Force the pass boundary without waiting for a timer.
				return
			}
			// Cold/compacting stores can return a GraphQL error with HTTP 200.
			if count == 1 {
				w.Write([]byte(`{"errors":[{"message":"index busy"}]}`))
				return
			}
			w.Write([]byte(`{"data":{"Get":{"Articles":[{"_additional":{"id":"00000000-0000-0000-0000-000000000001"}}]}}}`))
		default:
			t.Errorf("unexpected discovery: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	warmer, err := New(Config{URL: server.URL, APIKey: "private-test-key", RequestTimeout: time.Second, PassTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if report := warmer.Pass(context.Background()); report.Errors != 0 || report.Scopes != 0 {
		t.Fatalf("empty index: %+v", report)
	}
	imported = true // The next periodic pass discovers data without restarting.
	if report := warmer.Pass(context.Background()); report.Errors != 1 || report.NearVector != 1 || report.BM25 != 1 || report.Scopes != 1 {
		t.Fatalf("one failed cold probe must not prevent the other space or BM25: %+v", report)
	}
	if report := warmer.Pass(context.Background()); report.Errors != 0 || report.NearVector != 2 || report.BM25 != 1 {
		t.Fatalf("next pass must retry the failed space: %+v", report)
	}
	if len(queries) != 6 {
		t.Fatalf("want two named-vector probes and one BM25 per pass, got %d", len(queries))
	}
	for _, query := range queries {
		if !strings.Contains(query, `tenant:"active"`) || !strings.Contains(query, "limit:1") || strings.Contains(query, "retired") {
			t.Errorf("wrong probe scope/bound: %s", query)
		}
	}
	if !strings.Contains(queries[0], `targetVectors:["primary"]`) || !strings.Contains(queries[1], `targetVectors:["secondary"]`) || !strings.Contains(queries[2], `bm25:{query:"Harbour",properties:["text"]}`) {
		t.Fatalf("wrong warming queries: %v", queries[:3])
	}
	// A slow target that consumes a pass must not starve another target or BM25.
	// Keep the store unchanged and cut each pass at its first probe boundary.
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		probeMu.Lock()
		cutProbe = cancel
		probeMu.Unlock()
		warmer.Pass(ctx)
		cancel()
	}
	probeMu.Lock()
	cutProbe = nil
	progress := append([]string(nil), queries[6:]...)
	probeMu.Unlock()
	if len(progress) != 3 || progress[0] != queries[0] || progress[1] != queries[1] || progress[2] != queries[2] {
		t.Fatalf("interrupted passes must visit both targets and BM25, got %v", progress)
	}
	// The deployment's first pass runs immediately, even with a long cadence.
	// Cancellation in its callback makes this a lifecycle check without a sleep.
	ctx, stop := context.WithCancel(context.Background())
	passes := 0
	warmer.Run(ctx, time.Hour, func(report Report) {
		passes++
		stop()
	})
	if passes != 1 {
		t.Fatalf("want one immediate pass before canceled wait, got %d", passes)
	}
}
