package weaviate_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/weaviate"
)

// A delete is complete only when the engine proves nothing is left: fewer
// matches than its per-call limit, or no match at all. A capped or
// limit-less response keeps the purge item for the next sweep.
func TestPurgeCompletenessFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		results  map[string]any
		deleted  int
		complete bool
		err      bool
	}{
		{"under limit", map[string]any{"matches": 3, "limit": 10000, "successful": 3}, 3, true, false},
		{"capped at limit", map[string]any{"matches": 10000, "limit": 10000, "successful": 10000}, 10000, false, false},
		{"limit missing", map[string]any{"matches": 3, "successful": 3}, 3, false, false},
		{"nothing left", map[string]any{"matches": 0}, 0, true, false},
		{"failed objects", map[string]any{"matches": 3, "limit": 10000, "successful": 2, "failed": 1}, 2, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var where any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "DELETE" || r.URL.Path != "/v1/batch/objects" {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				where = body["match"].(map[string]any)["where"]
				_ = json.NewEncoder(w).Encode(map[string]any{"results": tc.results})
			}))
			defer server.Close()
			r, err := weaviate.New(server.URL).PurgeVersion(context.Background(), "QuivrTextV4", "org", "version")
			if (err != nil) != tc.err || r.Deleted != tc.deleted || r.Complete != tc.complete {
				t.Fatalf("result %+v %v", r, err)
			}
			if b, _ := json.Marshal(where); string(b) != `{"operands":[{"operator":"Equal","path":["organization"],"valueText":"org"},{"operator":"Equal","path":["versionId"],"valueText":"version"}],"operator":"And"}` {
				t.Fatalf("filter %s", b)
			}
		})
	}
}
