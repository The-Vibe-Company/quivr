package weaviate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
)

// The dependency times out large deletes; adaptation belongs to the real Store.
// No waits or production-only test hooks are needed at this HTTP boundary.
func TestGenerationPurgeAdaptsToSlowStore(t *testing.T) {
	provider := &slowPurgeStore{threshold: 64}
	for i := 1; i <= 700; i++ {
		provider.ids = append(provider.ids, fmt.Sprintf("00000000-0000-0000-0000-%012d", i))
	}
	store := weaviate.New("http://index.example")
	store.Client.Transport = provider
	deleted, complete := 0, false
	for attempt := 0; attempt < 80; attempt++ {
		result, err := store.PurgeGeneration(context.Background(), "QuivrTextV4", "org", "corpus", "generation")
		if err != nil && !strings.Contains(err.Error(), "context deadline exceeded") {
			t.Fatalf("unexpected purge failure: %v", err)
		}
		deleted += result.Deleted
		if deleted >= 128 {
			provider.threshold = 256
		}
		if result.Complete {
			complete = true
			break
		}
	}
	if !complete || deleted != 700 || len(provider.ids) != 0 {
		t.Fatalf("slow store did not drain: complete=%v confirmed=%d survivors=%d windows=%v", complete, deleted, len(provider.ids), provider.windows)
	}
	if len(provider.windows) < 4 || provider.windows[0] != 256 || provider.windows[1] != 128 || provider.windows[2] != 64 {
		t.Fatalf("timeout windows %v, want initial 256 then 128 then 64", provider.windows)
	}
	grew := false
	for _, n := range provider.windows[3:] {
		grew = grew || n > 64
		if n > 256 {
			t.Fatalf("unbounded delete window %d", n)
		}
	}
	for _, budget := range provider.budgets {
		if budget < 59*time.Second || budget > time.Minute {
			t.Fatalf("purge request budget %s, want 60s independently of ordinary 4s client", budget)
		}
	}
	if store.Client.Timeout != 4*time.Second {
		t.Fatal("purge changed ordinary request timeout")
	}
	if !grew {
		t.Fatalf("successful windows never recovered: %v", provider.windows)
	}
}

func TestGenerationPurgeTimeoutClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		floor   bool
	}{
		{"request timeout floor", context.DeadlineExceeded, true},
		{"other provider error", errors.New("index unavailable"), false},
		{"cancelled request", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &slowPurgeStore{threshold: 256, failure: tc.failure}
			for i := 1; i <= 300; i++ {
				provider.ids = append(provider.ids, fmt.Sprintf("00000000-0000-0000-0000-%012d", i))
			}
			store := weaviate.New("http://index.example")
			store.Client.Transport = provider
			store.PurgeTimeout = 75 * time.Second
			for i := 0; i < 7; i++ {
				result, err := store.PurgeGeneration(context.Background(), "QuivrTextV4", "org", "corpus", "generation")
				if err == nil || result.Complete || result.Deleted != 0 {
					t.Fatalf("failed window: %+v %v", result, err)
				}
			}
			want := 256
			if tc.floor {
				want = 16
			}
			if provider.windows[6] != want {
				t.Fatalf("failure %v: windows %v, want last window %d", tc.failure, provider.windows, want)
			}
			for _, budget := range provider.budgets {
				if budget < 74*time.Second || budget > 75*time.Second {
					t.Fatalf("configured purge deadline: %s", budget)
				}
			}
			provider.failure = nil
			for i := 0; i < 40; i++ {
				result, err := store.PurgeGeneration(context.Background(), "QuivrTextV4", "org", "corpus", "generation")
				if err != nil {
					t.Fatal(err)
				}
				if result.Complete {
					return
				}
			}
			t.Fatalf("did not recover after %s", tc.name)
		})
	}
}

func TestGenerationPurgeSelectionTimeoutRecovers(t *testing.T) {
	provider := &slowPurgeStore{threshold: 256, selectionFailure: context.DeadlineExceeded, ids: []string{"00000000-0000-0000-0000-000000000001"}}
	store := weaviate.New("http://index.example")
	store.Client.Transport = provider
	result, err := store.PurgeGeneration(context.Background(), "QuivrTextV4", "org", "corpus", "generation")
	if err == nil || result.Complete || result.Deleted != 0 || len(provider.windows) != 0 {
		t.Fatalf("selection timeout: %+v %v deletes=%v", result, err, provider.windows)
	}
	provider.selectionFailure = nil
	provider.probeFailure = errors.New("cap probe unavailable")
	result, err = store.PurgeGeneration(context.Background(), "QuivrTextV4", "org", "corpus", "generation")
	if err != nil || result.Deleted != 1 || result.Complete {
		t.Fatalf("selection recovery: %+v %v", result, err)
	}
	result, err = store.PurgeGeneration(context.Background(), "QuivrTextV4", "org", "corpus", "generation")
	if err != nil || !result.Complete {
		t.Fatalf("empty confirmation: %+v %v", result, err)
	}
}

type slowPurgeStore struct {
	ids              []string
	threshold        int
	windows          []int
	budgets          []time.Duration
	failure          error
	selectionFailure error
	probeFailure     error
}

func (s *slowPurgeStore) RoundTrip(req *http.Request) (*http.Response, error) {
	if deadline, ok := req.Context().Deadline(); ok {
		s.budgets = append(s.budgets, time.Until(deadline))
	} else {
		return nil, errors.New("purge request has no deadline")
	}
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, err
	}
	var out any
	if req.URL.Path == "/v1/graphql" {
		// Baseline path reaches the same oversized-delete timeout.
		rows := []any{}
		for _, id := range s.ids[:min(len(s.ids), 257)] {
			rows = append(rows, map[string]any{"_additional": map[string]any{"id": id}})
		}
		out = map[string]any{"data": map[string]any{"Get": map[string]any{"QuivrTextV4": rows}}}
	} else if req.URL.Path == "/v1/batch/objects" && req.Method == http.MethodDelete {
		where := body["match"].(map[string]any)["where"].(map[string]any)
		identities := purgeFilterIDs(where)
		selected := []string{}
		for _, id := range s.ids {
			if len(identities) == 0 || identities[id] {
				selected = append(selected, id)
			}
		}
		objects := []any{}
		if dry, _ := body["dryRun"].(bool); dry {
			if len(identities) > 0 && s.probeFailure != nil {
				return nil, s.probeFailure
			}
			if len(identities) == 0 && s.selectionFailure != nil {
				return nil, s.selectionFailure
			}
			for _, id := range selected {
				objects = append(objects, map[string]any{"id": id, "status": "DRYRUN"})
			}
			out = map[string]any{"dryRun": true, "results": map[string]any{"matches": len(selected), "limit": 10000, "successful": 0, "failed": 0, "objects": objects}}
		} else {
			s.windows = append(s.windows, len(selected))
			if s.failure != nil {
				return nil, s.failure
			}
			if len(selected) > s.threshold {
				return nil, context.DeadlineExceeded
			}
			remaining := []string{}
			for _, id := range s.ids {
				if !identities[id] {
					remaining = append(remaining, id)
				}
			}
			s.ids = remaining
			out = map[string]any{"results": map[string]any{"matches": len(selected), "limit": 10000, "successful": len(selected), "failed": 0}}
		}
	} else {
		return nil, fmt.Errorf("unexpected purge request %s %s", req.Method, req.URL.Path)
	}
	encoded, _ := json.Marshal(out)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(encoded))}, nil
}

func purgeFilterIDs(where map[string]any) map[string]bool {
	ids := map[string]bool{}
	var walk func(map[string]any)
	walk = func(filter map[string]any) {
		if path, ok := filter["path"].([]any); ok && len(path) == 1 && path[0] == "id" {
			if id, ok := filter["valueText"].(string); ok {
				ids[id] = true
			}
		}
		if operands, ok := filter["operands"].([]any); ok {
			for _, operand := range operands {
				walk(operand.(map[string]any))
			}
		}
	}
	walk(where)
	return ids
}

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

// Only a valid zero-match dry run proves completion. Invalid caps are checked
// with an ID fence before any generation-wide selection is admitted.
func TestGenerationPurgeListingFailsClosed(t *testing.T) {
	const empty = `{"dryRun":true,"results":{"matches":0,"limit":10000,"successful":0,"failed":0,"objects":[]}}`
	for _, tc := range []struct {
		name, body         string
		complete           bool
		preflight          bool
		deleted, remaining int
	}{
		{name: "empty", body: empty, complete: true},
		{name: "null", body: `{"dryRun":true,"results":null}`},
		{name: "missing", body: `{}`},
		{name: "missing dry run flag", body: `{"results":{"matches":0,"limit":10000,"successful":0,"failed":0}}`},
		{name: "not dry run", body: `{"dryRun":false,"results":{"matches":0,"limit":10000,"successful":0,"failed":0}}`},
		{name: "failed selection", body: `{"dryRun":true,"results":{"matches":1,"limit":10000,"successful":0,"failed":1}}`},
		{name: "missing identities", body: `{"dryRun":true,"results":{"matches":1,"limit":10000,"successful":0,"failed":0}}`},
		{name: "missing identity", body: `{"dryRun":true,"results":{"matches":1,"limit":10000,"successful":0,"failed":0,"objects":[{"status":"DRYRUN"}]}}`},
		{name: "failed identity", body: `{"dryRun":true,"results":{"matches":1,"limit":10000,"successful":0,"failed":0,"objects":[{"id":"00000000-0000-0000-0000-000000000001","status":"FAILED"}]}}`},
		{name: "duplicate identity", body: `{"dryRun":true,"results":{"matches":2,"limit":10000,"successful":0,"failed":0,"objects":[{"id":"00000000-0000-0000-0000-000000000001","status":"DRYRUN"},{"id":"00000000-0000-0000-0000-000000000001","status":"DRYRUN"}]}}`},
		{name: "uncapped preflight", preflight: true, body: `{"dryRun":true,"results":{"matches":0,"limit":0,"successful":0,"failed":0,"objects":[]}}`},
		{name: "excessive preflight cap", preflight: true, body: `{"dryRun":true,"results":{"matches":0,"limit":10001,"successful":0,"failed":0,"objects":[]}}`},
		{name: "cap plus one", body: `{"dryRun":true,"results":{"matches":3,"limit":2,"successful":0,"failed":0,"objects":[{"id":"00000000-0000-0000-0000-000000000001","status":"DRYRUN"},{"id":"00000000-0000-0000-0000-000000000002","status":"DRYRUN"}]}}`, deleted: 2, remaining: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "DELETE" || r.URL.Path != "/v1/batch/objects" {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				where := request["match"].(map[string]any)["where"].(map[string]any)
				ids := purgeFilterIDs(where)
				dry, _ := request["dryRun"].(bool)
				w.Header().Set("Content-Type", "application/json")
				if !dry {
					if tc.deleted == 0 {
						t.Error("invalid selection admitted a real delete")
					}
					io.WriteString(w, `{"results":{"matches":2,"limit":10000,"successful":2,"failed":0}}`)
				} else if len(ids) == 1 && !tc.preflight {
					io.WriteString(w, empty)
				} else {
					if tc.preflight && len(ids) != 1 {
						t.Error("unsafe cap allowed broad selection")
					}
					io.WriteString(w, tc.body)
				}
			}))
			defer server.Close()
			result, err := weaviate.New(server.URL).PurgeGeneration(context.Background(), "QuivrTextV4", "org", "corpus", "generation")
			success := tc.complete || tc.deleted > 0
			if result.Complete != tc.complete || (err == nil) != success || result.Deleted != tc.deleted || result.RemainingAtLeast != tc.remaining {
				t.Fatalf("selection result %+v %v, want complete=%v deleted=%d remaining=%d", result, err, tc.complete, tc.deleted, tc.remaining)
			}
		})
	}
}
