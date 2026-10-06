package httpapi_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

// Owns the version endpoint contract: any valid key can inspect the build,
// with no storage access, while missing or invalid credentials stay refused.
func TestVersionRequiresAValidKey(t *testing.T) {
	handler, err := httpapi.New(nil, content.Service{}, retrieval.Service{}, uploads.Service{},
		map[string]corpus.Scope{"reader": {Organization: "example"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler = checkedAPI(t, handler)
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {"unknown", 401}, {"reader", 200}} {
		r := httptest.NewRequest("GET", "/v0/version", nil)
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("key %q: status %d, body %s", tc.token, w.Code, w.Body)
		}
		if tc.status == 200 {
			var got map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{"version": "dev", "revision": "unknown", "api_version": "v0", "plugin_engine_version": "0.2.0"} {
				if got[key] != want {
					t.Fatalf("%s = %q; want %q", key, got[key], want)
				}
			}
		}
	}
}
