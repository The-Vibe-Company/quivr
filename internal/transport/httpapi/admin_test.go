package httpapi_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

// memoryActivity holds documents newest first, as the store returns them.
type memoryActivity struct{ items []content.Activity }

func (m memoryActivity) LatestActivity(_ context.Context, org string, after *content.ActivityCursor, limit int) ([]content.Activity, error) {
	out := []content.Activity{}
	for _, a := range m.items {
		if after != nil && !a.Steps.Accepted.Before(after.AcceptedAt) {
			continue
		}
		if len(out) < limit {
			out = append(out, a)
		}
	}
	return out, nil
}

func (m memoryActivity) VersionActivity(_ context.Context, org, id string) (content.Activity, error) {
	for _, a := range m.items {
		if a.VersionID == id {
			return a, nil
		}
	}
	return content.Activity{}, corpus.ErrNotFound
}

const (
	observer       = "observer-token-0123456789abcdef0123456789ab"
	fencedObserver = "fenced-observer-token-0123456789abcdef0123"
)

// TestAdminDocumentsNeedObservabilityRead owns the authorization, paging and
// response mapping of the admin document reads.
func TestAdminDocumentsNeedObservabilityRead(t *testing.T) {
	at := func(s int) *time.Time { v := time.Date(2026, 9, 30, 10, 0, s, 0, time.UTC); return &v }
	newer := content.Activity{VersionID: "version_2", RecordID: "record_2", Source: content.Source{CorpusID: "corpus_b", Namespace: "news", RecordKey: "b"}, State: "received", Steps: content.Steps{Accepted: at(5)}}
	older := content.Activity{VersionID: "version_1", RecordID: "record_1", Source: content.Source{CorpusID: "corpus_a", Namespace: "news", RecordKey: "a"}, Title: "Harbour reopens", State: "retrieval_ready", Current: true,
		Normalizer: &content.PluginRef{ID: "pdf-text", Version: "0.1.0"}, Steps: content.Steps{Accepted: at(0), Materialized: at(1), Segmented: at(2), RetrievalReady: at(3)}}
	keys := map[string]corpus.Scope{
		organizationAdmin: {Organization: "org_a", Actions: []string{"corpora:read", "content:read", "changes:read", "monitoring:read", "operations:read"}, Corpora: []string{"*"}},
		observer:          {Organization: "org_a", Actions: []string{content.ObservabilityRead}, Corpora: []string{"*"}},
		fencedObserver:    {Organization: "org_a", Actions: []string{content.ObservabilityRead}, Corpora: []string{"corpus_a"}},
		// The same reader with one more action: another scope for cursors.
		observer + "-renamed": {Organization: "org_a", Actions: []string{content.ObservabilityRead, "content:read"}, Corpora: []string{"*"}},
	}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"), httpapi.WithActivity(content.Activities{Store: memoryActivity{items: []content.Activity{newer, older}}}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	for _, path := range []string{"/v0/admin/documents", "/v0/admin/documents/version_1/timeline"} {
		if e := getJSON(t, server, path, organizationAdmin, 403); e["code"] != "forbidden" {
			t.Fatalf("GET %s without observability:read: %v", path, e)
		}
	}

	first := getJSON(t, server, "/v0/admin/documents?limit=1", observer, 200)
	items, _ := first["items"].([]any)
	cursor, _ := first["next_page_cursor"].(string)
	if len(items) != 1 || items[0].(map[string]any)["version_id"] != "version_2" || cursor == "" {
		t.Fatalf("first page: %v", first)
	}
	second := getJSON(t, server, "/v0/admin/documents?limit=1&page_cursor="+cursor, observer, 200)
	items, _ = second["items"].([]any)
	if len(items) != 1 || second["next_page_cursor"] != nil {
		t.Fatalf("second page: %v", second)
	}
	doc := items[0].(map[string]any)
	steps, _ := doc["steps"].(map[string]any)
	if doc["version_id"] != "version_1" || doc["corpus_id"] != "corpus_a" || doc["source_namespace"] != "news" || doc["record_key"] != "a" || doc["title"] != "Harbour reopens" ||
		doc["state"] != "retrieval_ready" || doc["is_current"] != true || steps["retrieval_ready_at"] != "2026-09-30T10:00:03Z" || steps["enriched_at"] != nil {
		t.Fatalf("document: %v", doc)
	}
	// A cursor is bound to the scope that listed it.
	observerAgain := observer + "-renamed"
	if e := getJSON(t, server, "/v0/admin/documents?page_cursor="+cursor, observerAgain, 409); e["code"] != "cursor_scope_changed" {
		t.Fatalf("cursor under another scope: %v", e)
	}
	// The list spans the Organization: a key limited to some Corpora cannot page it.
	if e := getJSON(t, server, "/v0/admin/documents", fencedObserver, 403); e["code"] != "forbidden" {
		t.Fatalf("list under a key limited to some Corpora: %v", e)
	}
	if e := getJSON(t, server, "/v0/admin/documents?limit=101", observer, 422); e["code"] != "invalid_limit" {
		t.Fatalf("limit over 100: %v", e)
	}

	timeline := getJSON(t, server, "/v0/admin/documents/version_1/timeline", fencedObserver, 200)
	entries, _ := timeline["steps"].([]any)
	if timeline["document"].(map[string]any)["version_id"] != "version_1" || len(entries) != 4 {
		t.Fatalf("timeline: %v", timeline)
	}
	materialized := entries[1].(map[string]any)
	if materialized["step"] != "materialized" || materialized["since"] != "accepted" || materialized["duration_ms"] != float64(1000) || materialized["plugin_id"] != "pdf-text" || materialized["plugin_version"] != "0.1.0" {
		t.Fatalf("materialized step: %v", materialized)
	}
	if accepted := entries[0].(map[string]any); accepted["step"] != "accepted" || accepted["since"] != nil || accepted["duration_ms"] != nil {
		t.Fatalf("accepted step: %v", accepted)
	}
	// A Version outside the key's Corpora is as absent as an unknown one.
	for _, id := range []string{"version_2", "version_unknown"} {
		if e := getJSON(t, server, "/v0/admin/documents/"+id+"/timeline", fencedObserver, 404); e["code"] != "not_found" {
			t.Fatalf("timeline of %s under a fenced key: %v", id, e)
		}
	}
}
