package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"net/http/httptest"
	"testing"
)

type queueRows struct {
	calls *int
	fail  bool
}

func (s queueRows) QueueBacklog(context.Context) ([]workqueue.Status, error) {
	*s.calls++
	if s.fail {
		return nil, errors.New("database offline")
	}
	return []workqueue.Status{{Queue: "live", Waiting: 2, InProgress: 1, OldestAgeSeconds: 3.5}, {Queue: "bulk", Waiting: 123, InProgress: 4, OldestAgeSeconds: 10}}, nil
}

// Owns bearer permission enforcement and the stable numeric autoscaler paths.
func TestQueueBacklogHTTPContract(t *testing.T) {
	for _, tc := range []struct {
		token  string
		status int
		fail   bool
	}{{"", 401, false}, {"reader", 403, false}, {"scoped", 403, false}, {"operator", 200, false}, {"operator", 503, true}} {
		calls := 0
		keys := map[string]corpus.Scope{"reader": {Organization: "org_a", Actions: []string{"observability:read"}, Corpora: []string{"*"}}, "operator": {Organization: "org_a", Actions: []string{"queues:read"}, Corpora: []string{"*"}}, "scoped": {Organization: "org_a", Actions: []string{"queues:read"}, Corpora: []string{"one"}}}
		h, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"), httpapi.WithQueues(queueRows{&calls, tc.fail}))
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "/v0/admin/queues", nil)
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != tc.status {
			t.Fatalf("%s fail=%v: %d %s", tc.token, tc.fail, res.Code, res.Body.String())
		}
		if tc.status == 200 {
			var body struct {
				Queues map[string]map[string]float64 `json:"queues"`
			}
			if err = json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Queues["bulk"]["waiting"] != 123 || body.Queues["live"]["in_progress"] != 1 || body.Queues["live"]["oldest_waiting_age_seconds"] != 3.5 {
				t.Fatalf("autoscaler paths: %s", res.Body.String())
			}
		}
		if tc.status < 200 || tc.status == 403 {
			if calls != 0 {
				t.Fatalf("unauthorized database read: %d", calls)
			}
		}
	}
}
