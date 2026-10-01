package httpapi_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/changes"
	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

// memoryCatalog answers keyset reads over Records put in key order. Key order,
// Corpus and organization isolation are the store's rules, owned by postgres
// TestRecordCatalogKeysetTraversal.
type memoryCatalog struct {
	records []content.Record
}

func (c *memoryCatalog) put(r content.Record) { c.records = append(c.records, r) }

func (c *memoryCatalog) Records(_ context.Context, _, _, after string, limit int) ([]content.Record, error) {
	var page []content.Record
	for _, r := range c.records {
		if r.ID > after && len(page) < limit {
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
		catalogReader: {Organization: "org_a", Actions: []string{"content:read", "changes:read", "corpora:read", "connectors:read"}, Corpora: []string{"*"}},
		catalogScoped: {Organization: "org_a", Actions: []string{"content:read", "changes:read"}, Corpora: []string{"corpus_a"}},
		catalogDenied: {Organization: "org_a", Actions: []string{"changes:read"}, Corpora: []string{"*"}},
	}
	key := catalogCursorKey
	journal := &memoryJournal{}
	feed := changes.Service{Journal: journal, Key: key, Retention: time.Second}
	handler, err := httpapi.New(knownCorpora{}, content.Service{Catalog: catalog}, retrieval.Service{}, uploads.Service{}, keys, key, httpapi.WithChanges(feed, 0), httpapi.WithConnectors(catalogConnectors(t)))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	changeCursor := getJSON(t, server, "/v0/changes?corpus_id=corpus_a", catalogReader, 200)["next_cursor"].(string)
	return server, changeCursor
}

// catalogConnectors holds two Connector instances so /v0/connectors issues a page cursor.
func catalogConnectors(t *testing.T) connectors.Service {
	t.Helper()
	registry, err := connectors.NewRegistry(connectors.Fixture{})
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryConnectors{items: map[string]connectors.Instance{}}
	at := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	for _, id := range []string{"connector_a", "connector_b"} {
		store.items[id] = connectors.Instance{Organization: "org_a", ID: id, CorpusID: "corpus_a", Namespace: id, Kind: "fixture", Config: []byte(`{}`), Enabled: true, CreatedAt: at, Health: connectors.Health{State: connectors.HealthActive, EvaluatedAt: at}}
	}
	return connectors.Service{Store: store, Registry: registry}
}

var catalogCursorKey = []byte("cursor-key-0123456789abcdef0123456789")

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

// A page cursor is issued only when more Records remain, and resumes after the
// last Record returned.
func TestCatalogPagesResumeAfterTheLastRecordReturned(t *testing.T) {
	catalog := &memoryCatalog{}
	for _, id := range []string{"record_a", "record_b", "record_c"} {
		catalog.put(content.Record{ID: id, Source: content.Source{CorpusID: "corpus_a", Namespace: "feed", RecordKey: id}, Withdrawn: id == "record_b", CurrentVersionID: "version_" + id})
	}
	server, _ := catalogServer(t, catalog)

	first := getJSON(t, server, recordsPath("corpus_a", "", 2), catalogReader, 200)
	items := first["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["record_id"] != "record_a" || items[1].(map[string]any)["withdrawn"] != true || first["next_page_cursor"] == nil {
		t.Fatal("first page", first)
	}
	second := getJSON(t, server, recordsPath("corpus_a", first["next_page_cursor"].(string), 2), catalogReader, 200)
	items = second["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["record_id"] != "record_c" || second["next_page_cursor"] != nil {
		t.Fatal("last page", second)
	}
	record := items[0].(map[string]any)
	if record["current_version_id"] != "version_record_c" || record["source"].(map[string]any)["corpus_id"] != "corpus_a" {
		t.Fatal("record shape", record)
	}
}

func TestCatalogAuthorizationAndValidation(t *testing.T) {
	server, _ := catalogServer(t, &memoryCatalog{})
	for _, tc := range []struct {
		path, token string
		status      int
		code        string
	}{
		// Authorization is judged before the cursor is decoded: an out-of-scope
		// caller learns nothing about a Corpus from how its cursor is refused.
		{recordsPath("corpus_a", "garbage", 0), catalogDenied, 403, "forbidden"},
		{recordsPath("corpus_b", "garbage", 0), catalogScoped, 404, "not_found"},
		{recordsPath("missing", "", 0), catalogReader, 404, "not_found"},
		{"/v0/records", catalogReader, 422, "invalid_query"},
		{"/v0/records?corpus_id=corpus_a&limit=0", catalogReader, 422, "invalid_limit"},
		{"/v0/records?corpus_id=corpus_a&limit=101", catalogReader, 422, "invalid_limit"},
		{"/v0/records?corpus_id=corpus_a&page_cursor=", catalogReader, 422, "invalid_cursor"},
		{"/v0/records?corpus_id=corpus_a&q=filter", catalogReader, 422, "invalid_query"},
		{"/v0/records?corpus_id=corpus_a&corpus_id=corpus_b", catalogReader, 422, "invalid_query"},
	} {
		if e := getJSON(t, server, tc.path, tc.token, tc.status); e["code"] != tc.code {
			t.Errorf("GET %s: code %v, want %s", tc.path, e["code"], tc.code)
		}
	}
}

func TestCatalogPageCursorsAreDistinctAndBound(t *testing.T) {
	catalog := &memoryCatalog{}
	for _, id := range []string{"record_a", "record_b"} {
		catalog.put(content.Record{ID: id, Source: content.Source{CorpusID: "corpus_a", Namespace: "feed", RecordKey: id}})
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
	corpusCursor, ok := corporaPage["next_page_cursor"].(string)
	if !ok {
		t.Fatal("corpora page issued no cursor", corporaPage)
	}
	connectorPage := getJSON(t, server, "/v0/connectors?limit=1", catalogReader, 200)
	connectorCursor, ok := connectorPage["next_page_cursor"].(string)
	if !ok {
		t.Fatal("connectors page issued no cursor", connectorPage)
	}
	// No cursor kind is accepted in place of another. Change and record page
	// cursors carry the same fields, so the two checks above are the ones only
	// signing domains pass; the table below also rejects on payload shape.
	for name, path := range map[string]string{
		"corpus cursor as record page cursor":    recordsPath("corpus_a", corpusCursor, 0),
		"corpus cursor as change cursor":         "/v0/changes?corpus_id=corpus_a&cursor=" + url.QueryEscape(corpusCursor),
		"corpus cursor as connector cursor":      "/v0/connectors?page_cursor=" + url.QueryEscape(corpusCursor),
		"change cursor as corpus cursor":         "/v0/corpora?page_cursor=" + url.QueryEscape(changeCursor),
		"change cursor as connector cursor":      "/v0/connectors?page_cursor=" + url.QueryEscape(changeCursor),
		"record page cursor as corpus cursor":    "/v0/corpora?page_cursor=" + url.QueryEscape(page),
		"record page cursor as connector cursor": "/v0/connectors?page_cursor=" + url.QueryEscape(page),
		"connector cursor as corpus cursor":      "/v0/corpora?page_cursor=" + url.QueryEscape(connectorCursor),
		"connector cursor as record page cursor": recordsPath("corpus_a", connectorCursor, 0),
		"connector cursor as change cursor":      "/v0/changes?corpus_id=corpus_a&cursor=" + url.QueryEscape(connectorCursor),
	} {
		if e := getJSON(t, server, path, catalogReader, 422); e["code"] != "invalid_cursor" {
			t.Fatal(name, e)
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
}

func TestListPageCursorsPaginateAndRejectUndomainedSignatures(t *testing.T) {
	server, _ := catalogServer(t, &memoryCatalog{})
	for _, c := range []struct{ path, idField, lastID string }{
		{"/v0/corpora", "corpus_id", "corpus_b"},
		{"/v0/connectors", "connector_id", "connector_b"},
	} {
		first := getJSON(t, server, c.path+"?limit=1", catalogReader, 200)
		next, ok := first["next_page_cursor"].(string)
		if !ok {
			t.Fatal(c.path, "issued no cursor", first)
		}
		second := getJSON(t, server, c.path+"?limit=1&page_cursor="+url.QueryEscape(next), catalogReader, 200)
		if items := second["items"].([]any); len(items) != 1 || items[0].(map[string]any)[c.idField] != c.lastID || second["next_page_cursor"] != nil {
			t.Fatal(c.path, "second page", second)
		}
		// Cursors issued before signing domains were an HMAC of the bare payload.
		payload := strings.SplitN(next, ".", 2)[0]
		raw, err := base64.RawURLEncoding.DecodeString(payload)
		if err != nil {
			t.Fatal(err)
		}
		h := hmac.New(sha256.New, catalogCursorKey)
		h.Write(raw)
		legacy := payload + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
		if e := getJSON(t, server, c.path+"?page_cursor="+url.QueryEscape(legacy), catalogReader, 422); e["code"] != "invalid_cursor" {
			t.Fatal(c.path, "legacy cursor", e)
		}
	}
}
