package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

// definitions is a minimal in-memory monitoring store for transport tests.
type definitions struct {
	queries map[string]monitoring.SavedQuery
	subs    map[string]monitoring.Subscription
}

func (d *definitions) CreateSavedQuery(_ context.Context, org string, in monitoring.SavedQueryInput) (monitoring.SavedQuery, error) {
	if q, ok := d.queries[in.Key]; ok {
		if q.Name != in.Name {
			return q, monitoring.ErrConflict
		}
		return q, nil
	}
	q := monitoring.SavedQuery{ID: "saved_query_" + in.Key, Name: in.Name, Current: monitoring.SavedQueryVersion{SavedQueryID: "saved_query_" + in.Key, VersionID: "saved_query_version_" + in.Key, Definition: in.Definition}}
	d.queries[in.Key] = q
	d.queries[q.ID] = q
	return q, nil
}
func (d *definitions) SavedQuery(_ context.Context, org, id string) (monitoring.SavedQuery, error) {
	q, ok := d.queries[id]
	if !ok || org != "org_a" {
		return q, monitoring.ErrNotFound
	}
	return q, nil
}
func (d *definitions) CreateSubscription(_ context.Context, org string, in monitoring.SubscriptionInput, query monitoring.SavedQueryVersion) (monitoring.Subscription, error) {
	s := monitoring.Subscription{ID: "subscription_" + in.Key, Name: in.Name, Enabled: true, Current: monitoring.SubscriptionVersion{SubscriptionID: "subscription_" + in.Key, VersionID: "subscription_version_" + in.Key, SavedQueryID: in.SavedQueryID, SavedQueryVersionID: in.SavedQueryVersionID, Evaluator: in.Evaluator, DestinationID: in.DestinationID, CorpusIDs: query.Definition.CorpusIDs}}
	d.subs[s.ID] = s
	return s, nil
}
func (d *definitions) Subscription(_ context.Context, org, id string) (monitoring.Subscription, error) {
	s, ok := d.subs[id]
	if !ok || org != "org_a" {
		return s, monitoring.ErrNotFound
	}
	return s, nil
}
func (d *definitions) DisableSubscription(_ context.Context, org, key, id string) (monitoring.Subscription, error) {
	s := d.subs[id]
	s.Enabled = false
	d.subs[id] = s
	return s, nil
}

type allCorpora struct{}

func (allCorpora) Authorize(context.Context, corpus.Scope, []string) error { return nil }

const (
	monitor       = "monitor-token-0123456789abcdef0123456789abcdef"
	monitorReader = "monitor-reader-0123456789abcdef0123456789abcdef"
	monitorNarrow = "monitor-narrow-0123456789abcdef0123456789abcdef"
	monitorOther  = "monitor-other-0123456789abcdef0123456789abcdef"
	noMonitoring  = "monitor-denied-0123456789abcdef0123456789abcdef"
)

func monitoringServer(t *testing.T) *httptest.Server {
	t.Helper()
	keys := map[string]corpus.Scope{
		monitor:       {Organization: "org_a", Actions: []string{"monitoring:read", "monitoring:write"}, Corpora: []string{"*"}},
		monitorReader: {Organization: "org_a", Actions: []string{"monitoring:read"}, Corpora: []string{"*"}},
		monitorNarrow: {Organization: "org_a", Actions: []string{"monitoring:read", "monitoring:write"}, Corpora: []string{"corpus_a"}},
		monitorOther:  {Organization: "org_b", Actions: []string{"monitoring:read", "monitoring:write"}, Corpora: []string{"*"}},
		noMonitoring:  {Organization: "org_a", Actions: []string{"content:read"}, Corpora: []string{"*"}},
	}
	service := monitoring.Service{
		Store:        &definitions{queries: map[string]monitoring.SavedQuery{}, subs: map[string]monitoring.Subscription{}},
		Corpora:      allCorpora{},
		Destinations: map[string]monitoring.Destination{"receiver_a": {Organization: "org_a", URL: "http://receiver.invalid/hook", Secret: "whsec_dGVzdC1zZWNyZXQtbmV2ZXItcmV0dXJuZWQ="}},
		MatchStore:   history{},
	}
	key := []byte("cursor-key-0123456789abcdef0123456789")
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, key, httpapi.WithMonitoring(service))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func call(t *testing.T, server *httptest.Server, method, path, token, body string, want int) (map[string]any, string) {
	t.Helper()
	req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var decoded map[string]any
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("%s %s: %v %s", method, path, err, raw)
	}
	if res.StatusCode != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, res.StatusCode, want, raw)
	}
	return decoded, string(raw)
}

const savedQueryBody = `{"idempotency_key":"q1","name":"Wire","definition":{"corpus_ids":["corpus_a"],"expression":{"fixture":{"decision":"match","threshold":0.50}},"retrieval_profile":"balanced","temporal_policy":"from_activation"}}`

func subscriptionBody(key, evaluator, destination string) string {
	return `{"idempotency_key":"` + key + `","name":"Alerts","saved_query_id":"saved_query_q1","saved_query_version_id":"saved_query_version_q1","evaluator":{"plugin_id":"` + evaluator + `","version":"1","configuration":{"decisions":{"default":"match"}}},"destination_id":"` + destination + `"}`
}

func TestMonitoringDefinitionsRoundTripPinnedConfiguration(t *testing.T) {
	server := monitoringServer(t)
	created, _ := call(t, server, "POST", "/v0/saved-queries", monitor, savedQueryBody, 201)
	version := created["current_version"].(map[string]any)
	if created["saved_query_id"] != "saved_query_q1" || version["version_id"] != "saved_query_version_q1" {
		t.Fatalf("saved query identity: %v", created)
	}
	_, pinned := call(t, server, "GET", "/v0/saved-queries/saved_query_q1/versions/saved_query_version_q1", monitorReader, "", 200)
	if !strings.Contains(pinned, `"threshold":0.50`) || !strings.Contains(pinned, `"temporal_policy":"from_activation"`) {
		t.Fatalf("pinned definition not preserved exactly: %s", pinned)
	}
	call(t, server, "GET", "/v0/saved-queries/saved_query_q1", monitorReader, "", 200)
	call(t, server, "GET", "/v0/saved-queries/saved_query_q1/versions/other", monitorReader, "", 404)

	sub, raw := call(t, server, "POST", "/v0/subscriptions", monitor, subscriptionBody("s1", "quivr.fixture", "receiver_a"), 201)
	if sub["enabled"] != true || strings.Contains(raw, "whsec_") || strings.Contains(raw, "receiver.invalid") {
		t.Fatalf("subscription exposes enabled state and never the destination secret or URL: %s", raw)
	}
	current := sub["current_version"].(map[string]any)
	if current["evaluator"].(map[string]any)["plugin_id"] != "quivr.fixture" || current["destination_id"] != "receiver_a" {
		t.Fatalf("pinned evaluator: %v", current)
	}
	versionPath := "/v0/subscriptions/subscription_s1/versions/" + current["version_id"].(string)
	immutable, _ := call(t, server, "GET", versionPath, monitorReader, "", 200)
	if _, leaks := immutable["enabled"]; leaks {
		t.Fatalf("immutable Version carries mutable enabled state: %v", immutable)
	}
	disabled, _ := call(t, server, "POST", "/v0/subscriptions/subscription_s1/disable", monitor, `{"idempotency_key":"d1"}`, 200)
	if disabled["enabled"] != false {
		t.Fatalf("disable: %v", disabled)
	}
	read, _ := call(t, server, "GET", "/v0/subscriptions/subscription_s1", monitorReader, "", 200)
	if read["enabled"] != false {
		t.Fatalf("disabled state not visible: %v", read)
	}
	call(t, server, "GET", versionPath, monitorReader, "", 200)
}

func TestMonitoringRejectsWithPublicErrors(t *testing.T) {
	server := monitoringServer(t)
	call(t, server, "POST", "/v0/saved-queries", monitor, savedQueryBody, 201)
	wide := strings.Replace(savedQueryBody, `["corpus_a"]`, `["corpus_a","corpus_b"]`, 1)
	cases := []struct {
		name, method, path, token, body string
		status                          int
		code                            string
	}{
		{"missing monitoring action", "POST", "/v0/saved-queries", noMonitoring, savedQueryBody, 403, "forbidden"},
		{"read-only create", "POST", "/v0/subscriptions", monitorReader, subscriptionBody("r", "quivr.fixture", "receiver_a"), 403, "forbidden"},
		{"ungranted Corpus", "POST", "/v0/saved-queries", monitorNarrow, strings.Replace(wide, `"q1"`, `"q2"`, 1), 403, "forbidden"},
		{"unknown field such as an inline secret", "POST", "/v0/subscriptions", monitor, strings.Replace(subscriptionBody("x", "quivr.fixture", "receiver_a"), `"destination_id"`, `"secret":"whsec_x","destination_id"`, 1), 422, "invalid_schema"},
		{"unsupported temporal policy", "POST", "/v0/saved-queries", monitor, strings.Replace(savedQueryBody, "from_activation", "backfill", 1), 422, "invalid_schema"},
		{"unimplemented profile", "POST", "/v0/saved-queries", monitor, strings.Replace(savedQueryBody, `"balanced"`, `"deep"`, 1), 422, "unsupported_profile"},
		{"other evaluator", "POST", "/v0/subscriptions", monitor, subscriptionBody("e", "vendor.semantic", "receiver_a"), 422, "unsupported_evaluator"},
		{"unconfigured destination", "POST", "/v0/subscriptions", monitor, subscriptionBody("d", "quivr.fixture", "receiver_z"), 422, "unknown_destination"},
		{"unknown query", "POST", "/v0/subscriptions", monitor, strings.Replace(subscriptionBody("u", "quivr.fixture", "receiver_a"), "saved_query_version_q1", "saved_query_version_zz", 1), 422, "unknown_saved_query"},
		{"conflicting replay", "POST", "/v0/saved-queries", monitor, strings.Replace(savedQueryBody, "Wire", "Renamed", 1), 409, "idempotency_conflict"},
		{"other Organization read", "GET", "/v0/saved-queries/saved_query_q1", monitorOther, "", 404, "not_found"},
		{"unknown Subscription disable", "POST", "/v0/subscriptions/missing/disable", monitor, `{"idempotency_key":"d"}`, 404, "not_found"},
		{"disable without key", "POST", "/v0/subscriptions/missing/disable", monitor, `{}`, 422, "invalid_schema"},
		{"unsupported method", "DELETE", "/v0/saved-queries/saved_query_q1", monitor, "", 405, "method_not_allowed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, _ := call(t, server, c.method, c.path, c.token, c.body, c.status)
			if body["code"] != c.code {
				t.Fatalf("code %v want %s", body["code"], c.code)
			}
		})
	}
}
