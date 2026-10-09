package weaviate_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
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
		reason   string
	}{
		{"under limit", map[string]any{"matches": 3, "limit": 10000, "successful": 3, "failed": 0}, 3, true, false, ""},
		{"capped at limit", map[string]any{"matches": 10000, "limit": 10000, "successful": 10000, "failed": 0}, 10000, false, false, ""},
		{"limit missing", map[string]any{"matches": 3, "successful": 3, "failed": 0}, 3, false, false, ""},
		{"nothing left", map[string]any{"matches": 0, "successful": 0, "failed": 0}, 0, true, false, ""},
		{"failed objects", map[string]any{"matches": 3, "limit": 10000, "successful": 2, "failed": 1, "objects": []any{map[string]any{"errors": map[string]any{"error": []any{map[string]any{"message": "context deadline exceeded"}}}}}}, 2, false, true, "context deadline exceeded"},
		{"unaccounted objects", map[string]any{"matches": 3, "limit": 10000, "successful": 2, "failed": 0}, 2, false, false, ""},
		{"missing failed count", map[string]any{"matches": 3, "limit": 10000, "successful": 3}, 0, false, true, "result missing"},
		{"missing result", nil, 0, false, true, "result missing"},
		{"missing counts", map[string]any{}, 0, false, true, "result missing"},
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
			if tc.reason != "" && (err == nil || !strings.Contains(err.Error(), tc.reason)) {
				t.Fatalf("provider reason lost: %v", err)
			}
			if b, _ := json.Marshal(where); string(b) != `{"operands":[{"operator":"Equal","path":["organization"],"valueText":"org"},{"operator":"Equal","path":["versionId"],"valueText":"version"}],"operator":"And"}` {
				t.Fatalf("filter %s", b)
			}
		})
	}
}

// Only an explicit empty list proves no matching generation objects remain.
// Null/missing data and provider errors must leave the durable purge unfinished.
func TestGenerationPurgeListingFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		complete   bool
	}{
		{"empty", `{"data":{"Get":{"QuivrTextV4":[]}}}`, true},
		{"null", `{"data":{"Get":{"QuivrTextV4":null}}}`, false},
		{"missing", `{"data":{"Get":{}}}`, false},
		{"provider failure", `{"data":{"Get":{"QuivrTextV4":[]}},"errors":[{"message":"query deadline exceeded"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v1/graphql" {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			result, err := weaviate.New(server.URL).PurgeGeneration(context.Background(), "QuivrTextV4", "org", "corpus", "generation")
			if result.Complete != tc.complete || (err == nil) != tc.complete || result.Deleted != 0 {
				t.Fatalf("listing result %+v %v, complete want %v", result, err, tc.complete)
			}
		})
	}
}
