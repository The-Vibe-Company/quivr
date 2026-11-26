package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

func TestVisibilityRefusalOnReadRoutes(t *testing.T) {
	const actor = "38e61a6d8a6f4849aca7b0f7dd7880d6"
	sink := &auditSink{}
	keys := map[string]corpus.Scope{
		"enforced": {Organization: "example", VisibilityMode: "enforce", Actions: []string{"content:read", "visibility:read", "visibility:write"}, Corpora: []string{"corpus"}},
		"existing": {Organization: "example"},
	}
	handler, err := httpapi.New(nil, content.Service{RecordStore: visibleRecordReader{}}, retrieval.Service{}, uploads.Service{}, keys, catalogCursorKey, httpapi.WithAudit(sink))
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ method, path string }{
		{"POST", "/v0/search"}, {"POST", "/v0/facets"},
		{"GET", "/v0/records"}, {"GET", "/v0/records/count"},
		{"GET", "/v0/records/record"}, {"GET", "/v0/records/record/versions/version"},
		{"GET", "/v0/ingestion-receipts/receipt"}, {"GET", "/v0/corpora/corpus/stats"},
		{"GET", "/v0/changes"}, {"GET", "/v0/changes/stream"},
		{"POST", "/v0/admin/backfills"}, {"POST", "/v0/admin/quarantine/reprocess"},
		{"POST", "/v0/admin/subscriptions/evaluator-migrations"}, {"POST", "/v0/admin/subscriptions/evaluation-retirements"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			for _, tc := range []struct {
				actors, principals []string
				auditActor         string
			}{
				{},
				{actors: []string{actor}, auditActor: actor},
				{actors: []string{actor}, principals: []string{"group:ok,invalid"}, auditActor: actor},
				{actors: []string{actor}, principals: []string{"group:one", "group:two"}, auditActor: actor},
				{actors: []string{actor, actor}, principals: []string{"group:one"}},
				{actors: []string{"invalid"}, principals: []string{"group:one"}},
			} {
				r := httptest.NewRequest(route.method, route.path, nil)
				r.Header.Set("Authorization", "Bearer enforced")
				r.Header["X-Quivr-Actor"] = tc.actors
				r.Header["X-Quivr-Principals"] = tc.principals
				w := httptest.NewRecorder()
				before := len(sink.events)
				handler.ServeHTTP(w, r)
				if w.Code != http.StatusForbidden || len(sink.events) != before+1 {
					t.Fatalf("status=%d count=%d body=%s", w.Code, len(sink.events)-before, w.Body)
				}
				e := sink.events[before]
				if e.Action != "permission.refused" || e.Outcome != "refused" || e.Detail.Status != 403 ||
					(tc.auditActor != "" && e.ActorID != tc.auditActor) ||
					(tc.auditActor == "" && e.ActorID != e.Actor) {
					t.Fatalf("refusal audit %+v", e)
				}
			}
		})
	}
	// A regular data read with valid context reaches canonical storage.
	r := httptest.NewRequest("GET", "/v0/records/record", nil)
	r.Header.Set("Authorization", "Bearer enforced")
	r.Header.Set("X-Quivr-Actor", actor)
	r.Header.Set("X-Quivr-Principals", "user:one, group:two")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("valid delegated record read: %d (%s)", w.Code, w.Body)
	}

	// Management routes require a separate key even if a delegated key
	// was configured with permission to manage grants.
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		r := httptest.NewRequest(method, "/v0/corpora/corpus/visibility-grants?source_namespace=feed", nil)
		r.Header.Set("Authorization", "Bearer enforced")
		r.Header.Set("X-Quivr-Actor", actor)
		r.Header.Set("X-Quivr-Principals", "group:reader")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("delegated %s grant: %d (%s)", method, w.Code, w.Body)
		}
	}

	// A legacy key ignores the visibility headers.
	r = httptest.NewRequest("GET", "/v0/version", nil)
	r.Header.Set("Authorization", "Bearer existing")
	r.Header.Set("X-Quivr-Actor", "invalid")
	r.Header.Set("X-Quivr-Principals", "invalid")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("legacy version read: %d (%s)", w.Code, w.Body)
	}
}

type visibleRecordReader struct{}

func (visibleRecordReader) Record(_ context.Context, _, id string) (content.Record, error) {
	return content.Record{ID: id, Source: content.Source{CorpusID: "corpus", Namespace: "feed", RecordKey: "key"}}, nil
}
