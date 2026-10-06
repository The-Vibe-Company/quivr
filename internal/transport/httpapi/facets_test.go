package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

type facetRouting struct {
	generations map[string]content.Generation
	fail        error
	reads       atomic.Int64
}

func (r *facetRouting) Authorize(context.Context, corpus.Scope, []string) error { return nil }
func (r *facetRouting) Generation(_ context.Context, _, id string) (content.Generation, error) {
	r.reads.Add(1)
	return r.generations[id], r.fail
}

type facetStorage struct {
	query content.FacetQuery
	fail  error
	reads int
}

func (s *facetStorage) CountFacets(_ context.Context, _ string, q content.FacetQuery) ([]content.Facet, error) {
	s.reads++
	s.query = q
	out := []content.Facet{}
	for _, f := range q.Fields {
		out = append(out, content.Facet{Field: f.Field, Buckets: []content.FacetBucket{}})
	}
	return out, s.fail
}

// Owns the public wire and routing contract. Exact counts are owned by the
// real PostgreSQL test; this fake never implements aggregation or eligibility.
func TestFacetRequestAuthorizationAndRouting(t *testing.T) {
	fields := []corpus.Field{{Name: "rating", Type: "number", Roles: []string{"filter"}}}
	routing := &facetRouting{generations: map[string]content.Generation{"corpus_a": {ID: "g_a", MetadataProjected: true, Fields: fields}, "corpus_b": {ID: "g_b", MetadataProjected: true}}}
	storage := &facetStorage{}
	keys := map[string]corpus.Scope{catalogReader: {Organization: "org_a", Actions: []string{"content:read"}, Corpora: []string{"*"}}, catalogScoped: {Organization: "org_a", Actions: []string{"content:read"}, Corpora: []string{"corpus_a"}}, catalogDenied: {Organization: "org_a", Actions: []string{}, Corpora: []string{"*"}}}
	handler, err := httpapi.New(knownCorpora{}, content.Service{Facets: storage}, retrieval.Service{Routing: routing}, uploads.Service{}, keys, catalogCursorKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(checkedAPI(t, handler))
	defer server.Close()
	call := func(key, body string, status int) map[string]any {
		t.Helper()
		response, e := operationCall(t, server, "POST", "/v0/facets", key, "application/json", body)
		if response.StatusCode != status {
			t.Fatalf("%s status %d want %d: %v", body, response.StatusCode, status, e)
		}
		return e
	}
	common := `{"corpus_ids":["corpus_a","corpus_b"],"fields":[{"field":"metadata.language"}]}`
	call(catalogReader, common, 200)
	if len(storage.query.Records.FilterRoutes) != 2 || storage.query.Fields[0].Limit != 20 || storage.query.Fields[0].Type != "string" {
		t.Fatalf("resolved common request: %#v", storage.query)
	}
	custom := `{"corpus_ids":["corpus_a","corpus_b"],"fields":[{"field":"rating"}],"filter":{"metadata":[{"field":"metadata.language","any_of":["en"]}],"source_namespaces":["source"]}}`
	excluded := call(catalogReader, custom, 200)["excluded_corpora"].([]any)
	if len(excluded) != 1 || excluded[0].(map[string]any)["corpus_id"] != "corpus_b" || excluded[0].(map[string]any)["fields"].([]any)[0] != "rating" || len(storage.query.Records.FilterRoutes) != 1 || storage.query.SourceNamespaces[0] != "source" {
		t.Fatalf("custom routing: %v %#v", excluded, storage.query)
	}
	reads := routing.reads.Load()
	call(catalogScoped, custom, 404)
	call(catalogDenied, common, 403)
	if routing.reads.Load() != reads {
		t.Fatal("unauthorized Corpus reached mapping resolution")
	}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"corpus_ids":[],"fields":[{"field":"metadata.language"}]}`, 422},
		{`{"corpus_ids":["corpus_a","corpus_a"],"fields":[{"field":"metadata.language"}]}`, 422},
		{`{"corpus_ids":["corpus_a"],"fields":[]}`, 422},
		{`{"corpus_ids":["corpus_a"],"fields":[{"field":"metadata.language","limit":101}]}`, 422},
		{`{"corpus_ids":["corpus_a"],"fields":[{"field":"metadata.language","limit":0}]}`, 422},
		{`{"corpus_ids":["corpus_a"],"fields":[{"field":"metadata.language"},{"field":"metadata.language"}]}`, 422},
		{`{"corpus_ids":["corpus_a"],"fields":[{"field":"metadata.language","interval":"day"}]}`, 422},
		{`{"corpus_ids":["corpus_a"],"fields":[{"field":"metadata.published_at"}]}`, 422},
		{`{"corpus_ids":["corpus_a"],"fields":[{"field":"metadata.published_at","interval":"hour"}]}`, 422},
		{`{"corpus_ids":["corpus_a"],"fields":[{"field":"metadata.language"}],"accepted_after":"2026-10-02T00:00:00Z","accepted_before":"2026-10-01T00:00:00Z"}`, 422},
		{`{"corpus_ids":["corpus_a"],"fields":[{"field":"metadata.language"}],"filter":{"metadata":[{"field":"rating","any_of":["2"]}]}}`, 422},
		{`{"corpus_ids":["corpus_a"],"fields":[{"field":"metadata.language"}],"query":"harbour"}`, 422},
	} {
		call(catalogReader, tc.body, tc.status)
	}
	call(catalogReader, `{"corpus_ids":["corpus_a"],"fields":[{"field":"metadata.published_at","interval":"day"}]}`, 200)
	g := routing.generations["corpus_b"]
	g.Fields = []corpus.Field{{Name: "rating", Type: "string", Roles: []string{"filter"}}}
	routing.generations["corpus_b"] = g
	call(catalogReader, custom, 422)
	g.MetadataProjected = false
	routing.generations["corpus_b"] = g
	if got := call(catalogReader, common, 422); got["code"] != "metadata_filter_unavailable" {
		t.Fatal(got)
	}
	routing.fail = errors.New("offline")
	if got := call(catalogReader, common, 503); got["code"] != "content_unavailable" {
		t.Fatal(got)
	}
	routing.fail = nil
	g.MetadataProjected = true
	routing.generations["corpus_b"] = g
	storage.fail = errors.New("offline")
	if got := call(catalogReader, common, 503); got["code"] != "content_unavailable" {
		t.Fatal(got)
	}
}

// Owns execution bounds through the real handler with a blocked dependency.
// synctest advances fake time; this adds no wall-clock wait to the suite.
type blockedFacets struct{ entered chan struct{} }

func (s blockedFacets) CountFacets(ctx context.Context, _ string, _ content.FacetQuery) ([]content.Facet, error) {
	s.entered <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestFacetCapacityAndDeadline(t *testing.T) {
	routing := &facetRouting{generations: map[string]content.Generation{"corpus_a": {ID: "g_a", MetadataProjected: true}}}

	handler, err := httpapi.New(knownCorpora{}, content.Service{Facets: blockedFacets{entered: make(chan struct{}, 8)}}, retrieval.Service{Routing: routing}, uploads.Service{}, map[string]corpus.Scope{catalogReader: {Organization: "org_a", Actions: []string{"content:read"}, Corpora: []string{"*"}}}, catalogCursorKey)
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		done := make(chan *httptest.ResponseRecorder, 8)
		request := func() *httptest.ResponseRecorder {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v0/facets", strings.NewReader(`{"corpus_ids":["corpus_a"],"fields":[{"field":"metadata.language"}]}`))
			req.Header.Set("Authorization", "Bearer "+catalogReader)
			req.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(recorder, req)
			return recorder
		}
		for i := 0; i < 8; i++ {
			go func() { done <- request() }()
		}
		synctest.Wait()
		rejected := request()
		if rejected.Code != 503 || rejected.Header().Get("Retry-After") != "1" || time.Since(start) != 0 {
			t.Fatalf("capacity: status=%d retry=%q elapsed=%v", rejected.Code, rejected.Header().Get("Retry-After"), time.Since(start))
		}
		for i := 0; i < 8; i++ {
			response := <-done
			if response.Code != 503 {
				t.Fatalf("deadline status=%d: %s", response.Code, response.Body)
			}
		}
		if time.Since(start) != 25*time.Second {
			t.Fatalf("facet deadline %v, want 25s", time.Since(start))
		}
	})
}
