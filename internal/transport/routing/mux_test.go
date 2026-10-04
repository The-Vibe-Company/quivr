package routing_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/transport/routing"
)

// The generated contract registrar uses this boundary; URL normalization and
// implicit HEAD handling would change answers independently of service tests.
func TestExactContractDispatch(t *testing.T) {
	mux := routing.New(func(w http.ResponseWriter, r *http.Request, match routing.PathMatch) {
		if match.Handler == nil {
			w.WriteHeader(404)
		} else {
			w.WriteHeader(405)
		}
	})
	mux.HandleFunc("GET /v0/items/{id}", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "item:"+r.PathValue("id")) })
	mux.HandleFunc("POST /v0/items/batch", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "batch") })
	mux.HandleFunc("GET /v0/items/profile", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "profile") })
	mux.HandleFunc("POST /v0/sources/{id}/api/{path}", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, r.PathValue("id")+":"+r.PathValue("path")) })
	if err := mux.HandleAlias("/legacy-profile", "/v0/items/profile"); err != nil {
		t.Fatal(err)
	}
	if err := mux.HandleAlias("/ghost", "/not-in-contract"); err == nil {
		t.Fatal("alias to a missing contract route accepted")
	}
	for _, tc := range []struct {
		method, path string
		status       int
		body         string
	}{
		{"GET", "/v0/items/one", 200, "item:one"},
		{"GET", "/v0/items/profile", 200, "profile"},
		{"GET", "/legacy-profile", 200, "profile"},
		{"HEAD", "/legacy-profile", 405, ""},
		{"POST", "/v0/items/batch", 200, "batch"},
		{"GET", "/v0/items/batch", 200, "item:batch"},
		{"GET", "/v0/items/", 200, "item:"},
		{"GET", "/v0/items/%61", 200, "item:a"},
		{"GET", "/v0/items/a%2Fb", 404, ""},
		{"HEAD", "/v0/items/one", 405, ""},
		{"PUT", "/v0/items/one", 405, ""},
		{"GET", "/v0//items/one", 404, ""},
		{"GET", "/v0/items/../one", 404, ""},
		{"POST", "/v0/sources/source_1/api/folder/receive", 200, "source_1:folder/receive"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.status || w.Body.String() != tc.body {
				t.Fatalf("got HTTP %d %q; want %d %q", w.Code, w.Body.String(), tc.status, tc.body)
			}
		})
	}
}
