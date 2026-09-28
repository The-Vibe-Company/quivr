package httpapi_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/changes"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

// memoryCatalog is an in-memory Record catalog in stable key order.
type memoryCatalog struct {
	mu      sync.Mutex
	records []content.Record
}

func (c *memoryCatalog) put(r content.Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r)
	sort.Slice(c.records, func(i, j int) bool { return c.records[i].ID < c.records[j].ID })
}

func (c *memoryCatalog) Records(_ context.Context, org, corpusID, after string, limit int) ([]content.Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var page []content.Record
	for _, r := range c.records {
		if org == "org_a" && r.Source.CorpusID == corpusID && r.ID > after && len(page) < limit {
			page = append(page, r)
		}
	}
	return page, nil
}

const (
	catalogReader = "catalog-reader-token-0123456789abcdef012345"
	catalogScoped = "catalog-scoped-token-0123456789abcdef012345"
	catalogDenied = "catalog-denied-token-0123456789abcdef012345"
)

func catalogServer(t *testing.T, catalog *memoryCatalog) (*httptest.Server, string) {
	t.Helper()
	keys := map[string]corpus.Scope{
		catalogReader: {Organization: "org_a", Actions: []string{"content:read", "changes:read", "corpora:read"}, Corpora: []string{"*"}},
		catalogScoped: {Organization: "org_a", Actions: []string{"content:read", "changes:read"}, Corpora: []string{"corpus_a"}},
		catalogDenied: {Organization: "org_a", Actions: []string{"changes:read"}, Corpora: []string{"*"}},
	}
	key := []byte("cursor-key-0123456789abcdef0123456789")
	journal := &memoryJournal{}
	feed := changes.Service{Journal: journal, Key: key, Retention: time.Second}
	handler, err := httpapi.New(knownCorpora{}, content.Service{Catalog: catalog}, retrieval.Service{}, uploads.Service{}, keys, key, httpapi.WithChanges(feed))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	changeCursor := getJSON(t, server, "/v0/changes?corpus_id=corpus_a", catalogReader, 200)["next_cursor"].(string)
	return server, changeCursor
}

func recordsPath(corpusID, pageCursor string, limit int) string {
	q := url.Values{"corpus_id": {corpusID}}
	if pageCursor != "" {
		q.Set("page_cursor", pageCursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return "/v0/records?" + q.Encode()
}

func TestCatalogPagesInStableKeyOrderIncludingWithdrawn(t *testing.T) {
	catalog := &memoryCatalog{}
	for _, id := range []string{"record_c", "record_a", "record_b"} {
		catalog.put(content.Record{ID: id, Source: content.Source{CorpusID: "corpus_a", Namespace: "feed", RecordKey: id}, Withdrawn: id == "record_b", CurrentVersionID: "version_" + id})
	}
	catalog.put(content.Record{ID: "record_0", Source: content.Source{CorpusID: "corpus_b", Namespace: "feed", RecordKey: "elsewhere"}})
	server, _ := catalogServer(t, catalog)

	first := getJSON(t, server, recordsPath("corpus_a", "", 2), catalogReader, 200)
	items := first["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["record_id"] != "record_a" || items[1].(map[string]any)["withdrawn"] != true || first["next_page_cursor"] == nil {
		t.Fatal("first page", first)
	}
	// A Record inserted behind the keyset position is not revisited; the journal covers it.
	catalog.put(content.Record{ID: "record_0a", Source: content.Source{CorpusID: "corpus_a", Namespace: "feed", RecordKey: "late"}})
	second := getJSON(t, server, recordsPath("corpus_a", first["next_page_cursor"].(string), 2), catalogReader, 200)
	items = second["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["record_id"] != "record_c" || second["next_page_cursor"] != nil {
		t.Fatal("last page", second)
	}
	record := items[0].(map[string]any)
	if record["current_version_id"] != "version_record_c" || record["source"].(map[string]any)["corpus_id"] != "corpus_a" {
		t.Fatal("record shape", record)
	}
	empty := getJSON(t, server, recordsPath("corpus_b", "", 0), catalogReader, 200)
	if items := empty["items"].([]any); len(items) != 1 || items[0].(map[string]any)["record_id"] != "record_0" {
		t.Fatal("corpus filter", empty)
	}
}

func TestCatalogAuthorizationAndValidation(t *testing.T) {
	server, _ := catalogServer(t, &memoryCatalog{})
	getJSON(t, server, recordsPath("corpus_a", "", 0), catalogDenied, 403)
	getJSON(t, server, recordsPath("corpus_b", "", 0), catalogScoped, 404)
	getJSON(t, server, recordsPath("missing", "", 0), catalogReader, 404)
	getJSON(t, server, "/v0/records", catalogReader, 422)
	getJSON(t, server, "/v0/records?corpus_id=corpus_a&limit=0", catalogReader, 422)
	getJSON(t, server, "/v0/records?corpus_id=corpus_a&limit=101", catalogReader, 422)
	getJSON(t, server, "/v0/records?corpus_id=corpus_a&page_cursor=", catalogReader, 422)
	getJSON(t, server, "/v0/records?corpus_id=corpus_a&q=filter", catalogReader, 422)
	getJSON(t, server, "/v0/records?corpus_id=corpus_a&corpus_id=corpus_b", catalogReader, 422)
}

func TestCatalogPageCursorsAreDistinctAndBound(t *testing.T) {
	catalog := &memoryCatalog{}
	for _, id := range []string{"record_a", "record_b"} {
		catalog.put(content.Record{ID: id, Source: content.Source{CorpusID: "corpus_a", Namespace: "feed", RecordKey: id}})
		catalog.put(content.Record{ID: id + "_b", Source: content.Source{CorpusID: "corpus_b", Namespace: "feed", RecordKey: id}})
	}
	server, changeCursor := catalogServer(t, catalog)
	page := getJSON(t, server, recordsPath("corpus_a", "", 1), catalogReader, 200)["next_page_cursor"].(string)
	corporaPage := getJSON(t, server, "/v0/corpora?limit=1", catalogReader, 200)

	// A Change Cursor is not a page cursor, and a page cursor is not a Change Cursor.
	if e := getJSON(t, server, recordsPath("corpus_a", changeCursor, 0), catalogReader, 422); e["code"] != "invalid_cursor" {
		t.Fatal(e)
	}
	if e := getJSON(t, server, "/v0/changes?corpus_id=corpus_a&cursor="+url.QueryEscape(page), catalogReader, 422); e["code"] != "invalid_cursor" {
		t.Fatal(e)
	}
	if c, ok := corporaPage["next_page_cursor"].(string); ok {
		if e := getJSON(t, server, recordsPath("corpus_a", c, 0), catalogReader, 422); e["code"] != "invalid_cursor" {
			t.Fatal(e)
		}
	}
	if e := getJSON(t, server, recordsPath("corpus_a", "x"+page, 0), catalogReader, 422); e["code"] != "invalid_cursor" {
		t.Fatal(e)
	}
	// A changed filter or authorization scope discards the traversal and names the restart.
	if e := getJSON(t, server, recordsPath("corpus_b", page, 0), catalogReader, 409); e["code"] != "cursor_scope_changed" || e["resync_url"] != "/v0/records?corpus_id=corpus_b" {
		t.Fatal(e)
	}
	if e := getJSON(t, server, recordsPath("corpus_a", page, 0), catalogScoped, 409); e["code"] != "cursor_scope_changed" || e["resync_url"] != "/v0/records?corpus_id=corpus_a" {
		t.Fatal(e)
	}
	if e := getJSON(t, server, "/v0/changes?corpus_id=corpus_a&cursor="+url.QueryEscape(changeCursor), catalogScoped, 409); e["resync_url"] != "/v0/records?corpus_id=corpus_a" {
		t.Fatal("change feed scope error lacks resync reference", e)
	}
}
