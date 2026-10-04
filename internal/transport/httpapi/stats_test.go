package httpapi_test

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/observability"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

// rollupRows serves fixed rollup rows; reads name the Organization they ask for.
type rollupRows struct {
	rows  []observability.Row
	asked *[]string
}

func (s rollupRows) UpsertRollups(context.Context, []observability.Row) error { return nil }
func (s rollupRows) PruneRollups(context.Context, time.Duration, time.Time) (int64, error) {
	return 0, nil
}
func (s rollupRows) ReadRollups(_ context.Context, org, series string, resolution time.Duration, _ time.Time, keys ...string) ([]observability.Row, error) {
	*s.asked = append(*s.asked, org)
	var out []observability.Row
	for _, r := range s.rows {
		if r.Organization == org && r.Series == series && r.Resolution == resolution && (len(keys) == 0 || slices.Contains(keys, r.Key)) {
			out = append(out, r)
		}
	}
	return out, nil
}

// TopKeys lists the stored keys of the series in order, one count each.
func (s rollupRows) TopKeys(_ context.Context, org, series string, resolution time.Duration, _ time.Time, limit int) (observability.Ranking, error) {
	out := observability.Ranking{Total: 7, Distinct: 3}
	rows, _ := s.ReadRollups(context.Background(), org, series, resolution, time.Time{})
	for _, r := range rows[:min(limit, len(rows))] {
		out.Keys = append(out.Keys, observability.KeyCount{Key: r.Key, Count: r.Count})
	}
	return out, nil
}

// TestStatsReadsAreScopedAndNeedObservabilityRead owns the authorization,
// the Organization scoping and the response mapping of the admin stats reads.
func TestStatsReadsAreScopedAndNeedObservabilityRead(t *testing.T) {
	const (
		observer = "stats-observer-token-0123456789abcdef0123456"
		reader   = "stats-reader-token-0123456789abcdef012345678"
		// Holds the action on one Corpus only: the rollups cover them all.
		fenced = "stats-fenced-token-0123456789abcdef012345678"
	)
	start := time.Now().UTC().Truncate(time.Minute)
	row := observability.Row{Organization: "org_a", Series: observability.SeriesPluginCall, Key: observability.Key("core.ingest", "1.0.0", "embed_query"),
		Resolution: time.Minute, Start: start, Count: 2, Errors: 1, DurationSumMS: 40, LastErrorCode: "plugin_unavailable", LastErrorAt: start}
	row.Buckets[3] = 2
	search := observability.Row{Organization: "org_a", Series: observability.SeriesSearch, Key: observability.Key("hybrid", "deep"), Resolution: time.Minute, Start: start, Count: 3, Items: 12, OverObjective: 2}
	search.Buckets[8] = 3
	received := observability.Row{Organization: "org_a", Series: observability.SeriesReceived, Key: "news-feed", Resolution: 15 * time.Minute, Start: start.Truncate(15 * time.Minute), Count: 4}
	query := observability.Row{Organization: "org_a", Series: observability.SeriesSearchQuery, Key: "must not be listed", Resolution: time.Hour, Start: start.Truncate(time.Hour), Count: 1}
	var asked []string
	keys := map[string]corpus.Scope{
		observer: {Organization: "org_a", Actions: []string{"observability:read"}, Corpora: []string{"*"}},
		reader:   {Organization: "org_a", Actions: []string{"corpora:read", "content:read", "search:query", "plugins:admin"}, Corpora: []string{"*"}},
		fenced:   {Organization: "org_a", Actions: []string{"observability:read"}, Corpora: []string{"corpus_1"}},
	}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"),
		httpapi.WithObservability(nil, observability.Reader{Store: rollupRows{rows: []observability.Row{row, search, received, query}, asked: &asked}}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(checkedAPI(t, handler))
	t.Cleanup(server.Close)

	for _, path := range []string{"/v0/admin/stats/plugins", "/v0/admin/stats/searches", "/v0/admin/stats/steps", "/v0/admin/stats/received", "/v0/admin/stats/matches", "/v0/admin/stats/top-queries", "/v0/admin/stats/connector-pushes"} {
		for _, token := range []string{reader, fenced} {
			if res, body := operationCall(t, server, "GET", path, token, "", ""); res.StatusCode != 403 || body["code"] != "forbidden" {
				t.Fatalf("GET %s without observability:read on every Corpus: %d %v, want 403 forbidden", path, res.StatusCode, body)
			}
		}
	}
	for path, code := range map[string]string{
		"/v0/admin/stats/plugins?window=30d": "invalid_window",
		"/v0/admin/stats/received?limit=101": "invalid_limit",
		"/v0/admin/stats/matches?limit=5":    "invalid_query",
		"/v0/admin/stats/steps?limit=5":      "invalid_query",
	} {
		if res, body := operationCall(t, server, "GET", path, observer, "", ""); res.StatusCode != 422 || body["code"] != code {
			t.Fatalf("GET %s: %d %v, want 422 %s", path, res.StatusCode, body, code)
		}
	}

	res, list := operationCall(t, server, "GET", "/v0/admin/stats/plugins?window=1h", observer, "", "")
	items, _ := list["items"].([]any)
	if res.StatusCode != 200 || len(items) != 1 || list["resolution_seconds"] != float64(60) {
		t.Fatalf("plugins: %d %v", res.StatusCode, list)
	}
	item := items[0].(map[string]any)
	summary := item["summary"].(map[string]any)
	if item["plugin_id"] != "core.ingest" || item["plugin_version"] != "1.0.0" || item["operation"] != "embed_query" ||
		summary["count"] != float64(2) || summary["errors"] != float64(1) || summary["last_error_code"] != "plugin_unavailable" || summary["p50_ms"] != float64(37.5) {
		t.Fatalf("plugin series %v", item)
	}
	// Searches per mode and profile, with those over the profile's latency
	// objective.
	res, list = operationCall(t, server, "GET", "/v0/admin/stats/searches", observer, "", "")
	items, _ = list["items"].([]any)
	if res.StatusCode != 200 || len(items) != 1 {
		t.Fatalf("searches: %d %v", res.StatusCode, list)
	}
	if item := items[0].(map[string]any); item["mode"] != "hybrid" || item["profile"] != "deep" || item["results"] != float64(12) || item["over_objective"] != float64(2) {
		t.Fatalf("search series %v; want hybrid deep with 12 results and 2 searches over the objective", item)
	}
	// Documents received: the largest source namespaces with their buckets,
	// and the total over every namespace.
	res, list = operationCall(t, server, "GET", "/v0/admin/stats/received?window=24h&limit=5", observer, "", "")
	items, _ = list["items"].([]any)
	if res.StatusCode != 200 || len(items) != 1 || list["resolution_seconds"] != float64(900) || list["total"] != float64(7) || list["sources"] != float64(3) {
		t.Fatalf("received: %d %v", res.StatusCode, list)
	}
	if item := items[0].(map[string]any); item["source_namespace"] != "news-feed" || item["count"] != float64(4) || len(item["points"].([]any)) != 1 {
		t.Fatalf("received source %v", item)
	}
	for _, org := range asked {
		if org != "org_a" {
			t.Fatalf("read Organizations %v; want only the key's org_a", asked)
		}
	}

	// Query text is not recorded by default: nothing is listed, whatever is stored.
	res, top := operationCall(t, server, "GET", "/v0/admin/stats/top-queries?window=7d", observer, "", "")
	if items, _ := top["items"].([]any); res.StatusCode != 200 || top["recording"] != false || len(items) != 0 {
		t.Fatalf("top queries with recording off: %d %v", res.StatusCode, top)
	}
}
