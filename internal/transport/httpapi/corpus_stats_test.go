package httpapi_test

import (
	"context"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

type statsReader struct{ failure error }

func (s *statsReader) CorpusStats(_ context.Context, _, id string, q content.CorpusStatsQuery) (content.CorpusStats, error) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	out := content.CorpusStats{CorpusID: id, Total: 4, CatalogTotal: 6, CatalogUndatedTotal: 2, ObservedAt: at, Complete: true}
	if q.Histogram {
		next := at.Add(24 * time.Hour)
		out.Histogram = &content.StatsHistogram{From: at, To: next, ResolutionSeconds: 86400, Items: []content.StatsBucket{{Start: at, Count: 4, CatalogCount: 4}}, Next: &next}
	}
	if q.Sources {
		out.Sources = &content.StatsSources{Items: []content.StatsSource{{Namespace: "source", ConnectorID: "unknown", Count: 4, CatalogCount: 6}}, Next: &content.SourceStatsKey{Namespace: "source", ConnectorID: "unknown"}}
	}
	return out, s.failure
}
func (s *statsReader) RecordCount(context.Context, string, string, content.RecordQuery) (content.RecordCountResult, error) {
	return content.RecordCountResult{Count: 6, ObservedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Approximate: true, Complete: false}, s.failure
}

// Owns HTTP shape, scoped cursors and refusal behavior. Storage membership and
// work bounds have real PostgreSQL owners and are not replayed with this fake.
func TestCorpusStatsHTTPContract(t *testing.T) {
	reader := &statsReader{}
	keys := map[string]corpus.Scope{
		catalogReader: {Organization: "org_a", Actions: []string{"content:read"}, Corpora: []string{"*"}},
		catalogScoped: {Organization: "org_a", Actions: []string{"content:read"}, Corpora: []string{"corpus_b"}},
		catalogDenied: {Organization: "org_a", Actions: []string{"corpora:read"}, Corpora: []string{"*"}},
	}
	handler, err := httpapi.New(knownCorpora{}, content.Service{Stats: reader}, retrieval.Service{}, uploads.Service{}, keys, catalogCursorKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(checkedAPI(t, handler))
	defer server.Close()
	root := "/v0/corpora/corpus_a/stats"
	got := getJSON(t, server, root, catalogReader, 200)
	if got["total"] != float64(4) || got["catalog_total"] != float64(6) || got["complete"] != true || got["approximate"] != false || got["histogram"] != nil || got["sources"] != nil {
		t.Fatalf("total response: %v", got)
	}
	got = getJSON(t, server, root+"?include=histogram,sources", catalogReader, 200)
	h := got["histogram"].(map[string]any)
	sources := got["sources"].(map[string]any)
	if h["next_page_cursor"] == "" || sources["next_page_cursor"] == "" || sources["items"].([]any)[0].(map[string]any)["connector_id"] != "unknown" {
		t.Fatalf("series response: %v", got)
	}
	cursor := url.QueryEscape(h["next_page_cursor"].(string))
	getJSON(t, server, root+"?include=histogram,sources&histogram_cursor="+cursor, catalogReader, 200)
	for _, tc := range []struct {
		path, token string
		status      int
		code        string
	}{
		{root, catalogDenied, 403, "forbidden"}, {root, catalogScoped, 404, "not_found"},
		{"/v0/corpora/missing/stats", catalogReader, 404, "not_found"},
		{root + "?include=other", catalogReader, 422, "invalid_query"},
		{root + "?include=histogram&include=sources", catalogReader, 422, "invalid_query"},
		{root + "?resolution=minute", catalogReader, 422, "invalid_query"},
		{root + "?include=histogram&accepted_after=2026-10-01T00:30:00Z", catalogReader, 422, "invalid_query"},
		{root + "?include=histogram&histogram_cursor=broken", catalogReader, 422, "invalid_cursor"},
		{root + "?include=histogram,sources&resolution=hour&histogram_cursor=" + cursor, catalogReader, 409, "cursor_scope_changed"},
		{root + "?include=sources&source_limit=1001", catalogReader, 422, "invalid_query"},
	} {
		if body := getJSON(t, server, tc.path, tc.token, tc.status); body["code"] != tc.code {
			t.Fatalf("%s: %v", tc.path, body)
		}
	}
	count := getJSON(t, server, "/v0/records/count?corpus_id=corpus_a", catalogReader, 200)
	if count["count"] != float64(6) || count["approximate"] != true || count["complete"] != false || count["observed_at"] == nil {
		t.Fatalf("count response: %v", count)
	}
	reader.failure = content.ErrCountTooBroad
	body := getJSON(t, server, "/v0/records/count?corpus_id=corpus_a&accepted_after=2026-10-01T00:00:00Z", catalogReader, 422)
	if body["code"] != "record_count_too_broad" || body["retryable"] != false {
		t.Fatalf("broad count response: %v", body)
	}
	reader.failure = errors.New("storage unavailable")
	body = getJSON(t, server, root+"?include=histogram,sources", catalogReader, 503)
	if body["code"] != "content_unavailable" || body["retryable"] != true {
		t.Fatalf("stats storage failure: %v", body)
	}

}
