package httpapi_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/observability"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
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
func (s rollupRows) ReadRollups(_ context.Context, org, series string, resolution time.Duration, _ time.Time) ([]observability.Row, error) {
	*s.asked = append(*s.asked, org)
	var out []observability.Row
	for _, r := range s.rows {
		if r.Organization == org && r.Series == series && r.Resolution == resolution {
			out = append(out, r)
		}
	}
	return out, nil
}
func (s rollupRows) TopKeys(context.Context, string, string, time.Duration, time.Time, int) ([]observability.KeyCount, error) {
	return []observability.KeyCount{{Key: "must not be listed", Count: 1}}, nil
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
	var asked []string
	keys := map[string]corpus.Scope{
		observer: {Organization: "org_a", Actions: []string{"observability:read"}, Corpora: []string{"*"}},
		reader:   {Organization: "org_a", Actions: []string{"corpora:read", "content:read", "search:query", "plugins:admin"}, Corpora: []string{"*"}},
		fenced:   {Organization: "org_a", Actions: []string{"observability:read"}, Corpora: []string{"corpus_1"}},
	}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"),
		httpapi.WithObservability(nil, observability.Reader{Store: rollupRows{rows: []observability.Row{row}, asked: &asked}}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	for _, path := range []string{"/v0/admin/stats/plugins", "/v0/admin/stats/searches", "/v0/admin/stats/steps", "/v0/admin/stats/top-queries"} {
		for _, token := range []string{reader, fenced} {
			if res, body := operationCall(t, server, "GET", path, token, "", ""); res.StatusCode != 403 || body["code"] != "forbidden" {
				t.Fatalf("GET %s without observability:read on every Corpus: %d %v, want 403 forbidden", path, res.StatusCode, body)
			}
		}
	}
	if res, body := operationCall(t, server, "GET", "/v0/admin/stats/plugins?window=30d", observer, "", ""); res.StatusCode != 422 || body["code"] != "invalid_window" {
		t.Fatalf("window=30d: %d %v, want 422 invalid_window", res.StatusCode, body)
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
	if len(asked) != 1 || asked[0] != "org_a" {
		t.Fatalf("read Organizations %v; want only the key's org_a", asked)
	}

	// Query text is not recorded by default: nothing is listed, whatever is stored.
	res, top := operationCall(t, server, "GET", "/v0/admin/stats/top-queries?window=7d", observer, "", "")
	if items, _ := top["items"].([]any); res.StatusCode != 200 || top["recording"] != false || len(items) != 0 {
		t.Fatalf("top queries with recording off: %d %v", res.StatusCode, top)
	}
}
