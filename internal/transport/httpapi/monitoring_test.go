package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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
	queries  map[string]monitoring.SavedQuery
	subs     map[string]monitoring.Subscription
	versions map[string]any // saved query or subscription ID + "/" + version ID
}

func (d *definitions) version(id, versionID string) (any, error) {
	v, ok := d.versions[id+"/"+versionID]
	if !ok {
		return nil, monitoring.ErrNotFound
	}
	return v, nil
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
	d.versions[q.ID+"/"+q.Current.VersionID] = q.Current
	return q, nil
}
func (d *definitions) SavedQueryVersion(_ context.Context, org, id, versionID string) (monitoring.SavedQueryVersion, error) {
	v, err := d.version(id, versionID)
	if err != nil {
		return monitoring.SavedQueryVersion{}, err
	}
	return v.(monitoring.SavedQueryVersion), nil
}
func (d *definitions) CreateSavedQueryVersion(_ context.Context, org, id string, in monitoring.SavedQueryVersionInput) (monitoring.SavedQueryVersion, error) {
	if v, err := d.version(id, "saved_query_version_"+in.Key); err == nil {
		return v.(monitoring.SavedQueryVersion), nil
	}
	q := d.queries[id]
	if q.Deleted {
		return monitoring.SavedQueryVersion{}, monitoring.ErrSavedQueryDeleted
	}
	q.Current = monitoring.SavedQueryVersion{SavedQueryID: id, VersionID: "saved_query_version_" + in.Key, Definition: in.Definition}
	d.queries[id] = q
	d.versions[id+"/"+q.Current.VersionID] = q.Current
	return q.Current, nil
}
func (d *definitions) DeleteSavedQuery(_ context.Context, org, key, id string) (monitoring.SavedQuery, error) {
	q := d.queries[id]
	for _, s := range d.subs {
		if s.Current.SavedQueryID == id && !s.Deleted && !q.Deleted {
			return q, monitoring.ErrSavedQueryInUse
		}
	}
	q.Deleted = true
	d.queries[id] = q
	return q, nil
}
func (d *definitions) RenameSavedQuery(_ context.Context, org, key, id, name string) (monitoring.SavedQuery, error) {
	q := d.queries[id]
	if q.Deleted {
		return q, monitoring.ErrSavedQueryDeleted
	}
	q.Name = name
	d.queries[id] = q
	return q, nil
}
func (d *definitions) RenameSubscription(_ context.Context, org, key, id, name string) (monitoring.Subscription, error) {
	s := d.subs[id]
	if s.Deleted {
		return s, monitoring.ErrSubscriptionDeleted
	}
	s.Name = name
	d.subs[id] = s
	return s, nil
}
func (d *definitions) SavedQuery(_ context.Context, org, id string) (monitoring.SavedQuery, error) {
	q, ok := d.queries[id]
	if !ok || org != "org_a" {
		return q, monitoring.ErrNotFound
	}
	return q, nil
}
func (d *definitions) CreateSubscription(_ context.Context, org string, in monitoring.SubscriptionInput, query monitoring.SavedQueryVersion) (monitoring.Subscription, error) {
	s := monitoring.Subscription{ID: "subscription_" + in.Key, Name: in.Name, Owner: in.Owner, Enabled: true, Current: monitoring.SubscriptionVersion{SubscriptionID: "subscription_" + in.Key, VersionID: "subscription_version_" + in.Key, Owner: in.Owner, SavedQueryID: in.SavedQueryID, SavedQueryVersionID: in.SavedQueryVersionID, Evaluator: in.Evaluator, DestinationID: in.DestinationID, CorpusIDs: query.Definition.CorpusIDs}}
	d.subs[s.ID] = s
	d.versions[s.ID+"/"+s.Current.VersionID] = s.Current
	return s, nil
}
func (d *definitions) SubscriptionVersion(_ context.Context, org, id, versionID string) (monitoring.SubscriptionVersion, error) {
	v, err := d.version(id, versionID)
	if err != nil {
		return monitoring.SubscriptionVersion{}, err
	}
	return v.(monitoring.SubscriptionVersion), nil
}
func (d *definitions) CreateSubscriptionVersion(_ context.Context, org, id string, in monitoring.SubscriptionVersionInput, query monitoring.SavedQueryVersion) (monitoring.SubscriptionVersion, error) {
	if v, err := d.version(id, "subscription_version_"+in.Key); err == nil {
		return v.(monitoring.SubscriptionVersion), nil
	}
	s := d.subs[id]
	if s.Deleted {
		return monitoring.SubscriptionVersion{}, monitoring.ErrSubscriptionDeleted
	}
	s.Current = monitoring.SubscriptionVersion{SubscriptionID: id, VersionID: "subscription_version_" + in.Key, SavedQueryID: query.SavedQueryID, SavedQueryVersionID: query.VersionID, Evaluator: in.Evaluator, DestinationID: in.DestinationID, CorpusIDs: query.Definition.CorpusIDs}
	d.subs[id] = s
	d.versions[id+"/"+s.Current.VersionID] = s.Current
	return s.Current, nil
}
func (d *definitions) DeleteSubscription(_ context.Context, org, key, id string) (monitoring.Subscription, error) {
	s := d.subs[id]
	s.Enabled, s.Deleted = false, true
	d.subs[id] = s
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

func (d *definitions) EnableSubscription(_ context.Context, org, key, id string) (monitoring.Subscription, error) {
	s := d.subs[id]
	if s.Deleted {
		return s, monitoring.ErrSubscriptionDeleted
	}
	s.Enabled = true
	d.subs[id] = s
	return s, nil
}

// Subscriptions pages active Subscriptions of an owner by ID, keeping those
// whose Corpora a non-nil corpora contains.
func (d *definitions) Subscriptions(_ context.Context, org string, owner monitoring.OwnerFilter, corpora []string, after string, limit int) ([]monitoring.Subscription, error) {
	ids := []string{}
	for id, s := range d.subs {
		visible := true
		for _, c := range s.Current.CorpusIDs {
			visible = visible && (corpora == nil || slices.Contains(corpora, c))
		}
		if org == "org_a" && id > after && s.Enabled && !s.Deleted && visible && s.Owner == owner.Owner && (s.Owner == "") == owner.Global {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	out := []monitoring.Subscription{}
	for _, id := range ids[:min(limit, len(ids))] {
		out = append(out, d.subs[id])
	}
	return out, nil
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
		Store:        &definitions{queries: map[string]monitoring.SavedQuery{}, subs: map[string]monitoring.Subscription{}, versions: map[string]any{}},
		Corpora:      allCorpora{},
		Destinations: map[string]monitoring.Destination{"receiver_a": {Organization: "org_a", URL: "http://receiver.invalid/hook", Secret: "whsec_dGVzdC1zZWNyZXQtbmV2ZXItcmV0dXJuZWQ="}},
		MatchStore:   history{},
		Evaluators:   monitoring.FixtureEvaluators(),
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

const savedQueryBody = `{"idempotency_key":"q1","name":"Wire","definition":{"corpus_ids":["corpus_a"],"expression":{"fixture":{"decision":"match","threshold":0.50}},"retrieval_profile":"default","temporal_policy":"from_activation"}}`

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
	enabled, _ := call(t, server, "POST", "/v0/subscriptions/subscription_s1/enable", monitor, `{"idempotency_key":"e1"}`, 200)
	if enabled["enabled"] != true || enabled["current_version"].(map[string]any)["version_id"] != current["version_id"] {
		t.Fatalf("enable keeps the same Version: %v", enabled)
	}
	call(t, server, "GET", "/v0/subscriptions/subscription_s1/enable", monitorReader, "", 405)
}

// TestMonitoringEditAndDelete drives the edit and delete routes: a new Saved
// Query Version does not move the Subscription, a new Subscription Version
// does, every Version stays readable, and deletion is visible, permanent and
// guarded by use.
func TestMonitoringEditAndDelete(t *testing.T) {
	server := monitoringServer(t)
	call(t, server, "POST", "/v0/saved-queries", monitor, savedQueryBody, 201)
	sub, _ := call(t, server, "POST", "/v0/subscriptions", monitor, subscriptionBody("s1", "quivr.fixture", "receiver_a"), 201)
	first := sub["current_version"].(map[string]any)["version_id"].(string)

	edit := `{"idempotency_key":"q-edit","definition":{"corpus_ids":["corpus_a"],"expression":{"fixture":{"decision":"no_match"}},"retrieval_profile":"default","temporal_policy":"from_activation"}}`
	v2, _ := call(t, server, "POST", "/v0/saved-queries/saved_query_q1/versions", monitor, edit, 201)
	if v2["version_id"] != "saved_query_version_q-edit" || v2["saved_query_id"] != "saved_query_q1" {
		t.Fatalf("new Saved Query Version: %v", v2)
	}
	query, _ := call(t, server, "GET", "/v0/saved-queries/saved_query_q1", monitorReader, "", 200)
	if query["current_version"].(map[string]any)["version_id"] != "saved_query_version_q-edit" || query["deleted"] != false {
		t.Fatalf("edited Saved Query: %v", query)
	}
	call(t, server, "GET", "/v0/saved-queries/saved_query_q1/versions/saved_query_version_q1", monitorReader, "", 200)
	still, _ := call(t, server, "GET", "/v0/subscriptions/subscription_s1", monitorReader, "", 200)
	if still["current_version"].(map[string]any)["saved_query_version_id"] != "saved_query_version_q1" {
		t.Fatalf("a Saved Query edit moved the Subscription: %v", still)
	}

	subEdit := `{"idempotency_key":"s-edit","saved_query_version_id":"saved_query_version_q-edit","evaluator":{"plugin_id":"quivr.fixture","version":"1","configuration":{}},"destination_id":"receiver_a"}`
	sv2, _ := call(t, server, "POST", "/v0/subscriptions/subscription_s1/versions", monitor, subEdit, 201)
	if sv2["saved_query_version_id"] != "saved_query_version_q-edit" || sv2["version_id"] == first {
		t.Fatalf("new Subscription Version: %v", sv2)
	}
	replayed, _ := call(t, server, "POST", "/v0/subscriptions/subscription_s1/versions", monitor, subEdit, 201)
	if replayed["version_id"] != sv2["version_id"] {
		t.Fatalf("replayed edit: %v", replayed)
	}
	call(t, server, "GET", "/v0/subscriptions/subscription_s1/versions/"+first, monitorReader, "", 200)
	call(t, server, "GET", "/v0/subscriptions/subscription_s1/versions", monitorReader, "", 405)

	// A rename answers the resource with its new name and keeps its Version.
	renamed, _ := call(t, server, "POST", "/v0/subscriptions/subscription_s1/rename", monitor, `{"idempotency_key":"sr","name":"Renamed"}`, 200)
	if renamed["name"] != "Renamed" || renamed["current_version"].(map[string]any)["version_id"] != sv2["version_id"] {
		t.Fatalf("renamed Subscription: %v", renamed)
	}
	if got, _ := call(t, server, "POST", "/v0/saved-queries/saved_query_q1/rename", monitor, `{"idempotency_key":"qr","name":"Renamed"}`, 200); got["name"] != "Renamed" {
		t.Fatalf("renamed Saved Query: %v", got)
	}
	call(t, server, "POST", "/v0/subscriptions/subscription_s1/rename", monitor, `{"idempotency_key":"sr","name":""}`, 422)
	call(t, server, "POST", "/v0/subscriptions/subscription_s1/rename", monitorReader, `{"idempotency_key":"sr","name":"x"}`, 403)

	inUse, _ := call(t, server, "POST", "/v0/saved-queries/saved_query_q1/delete", monitor, `{"idempotency_key":"qd"}`, 409)
	if inUse["code"] != "saved_query_in_use" {
		t.Fatalf("delete of a used Saved Query: %v", inUse)
	}
	deleted, _ := call(t, server, "POST", "/v0/subscriptions/subscription_s1/delete", monitor, `{"idempotency_key":"sd"}`, 200)
	if deleted["deleted"] != true || deleted["enabled"] != false {
		t.Fatalf("deleted Subscription: %v", deleted)
	}
	call(t, server, "GET", "/v0/subscriptions/subscription_s1", monitorReader, "", 200)
	for path, body := range map[string]string{
		"/v0/subscriptions/subscription_s1/enable":   `{"idempotency_key":"se"}`,
		"/v0/subscriptions/subscription_s1/rename":   `{"idempotency_key":"sr2","name":"Late"}`,
		"/v0/subscriptions/subscription_s1/versions": strings.Replace(subEdit, "s-edit", "s-edit-2", 1),
	} {
		if got, _ := call(t, server, "POST", path, monitor, body, 409); got["code"] != "subscription_deleted" {
			t.Fatalf("%s after delete: %v", path, got)
		}
	}
	query, _ = call(t, server, "POST", "/v0/saved-queries/saved_query_q1/delete", monitor, `{"idempotency_key":"qd"}`, 200)
	if query["deleted"] != true {
		t.Fatalf("deleted Saved Query: %v", query)
	}
	if got, _ := call(t, server, "POST", "/v0/saved-queries/saved_query_q1/versions", monitor, strings.Replace(edit, "q-edit", "q-edit-2", 1), 409); got["code"] != "saved_query_deleted" {
		t.Fatalf("edit after delete: %v", got)
	}
	call(t, server, "POST", "/v0/saved-queries/saved_query_q1/delete", monitorReader, `{"idempotency_key":"qd"}`, 403)
	call(t, server, "POST", "/v0/saved-queries/missing/delete", monitor, `{"idempotency_key":"qm"}`, 404)
	call(t, server, "POST", "/v0/saved-queries/saved_query_q1/disable", monitor, `{"idempotency_key":"x"}`, 404)
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
		{"unimplemented profile", "POST", "/v0/saved-queries", monitor, strings.Replace(savedQueryBody, `"default"`, `"deep"`, 1), 422, "unsupported_profile"},
		{"other evaluator", "POST", "/v0/subscriptions", monitor, subscriptionBody("e", "vendor.semantic", "receiver_a"), 422, "unsupported_evaluator"},
		{"unconfigured destination", "POST", "/v0/subscriptions", monitor, subscriptionBody("d", "quivr.fixture", "receiver_z"), 422, "unknown_destination"},
		{"unknown query", "POST", "/v0/subscriptions", monitor, strings.Replace(subscriptionBody("u", "quivr.fixture", "receiver_a"), "saved_query_version_q1", "saved_query_version_zz", 1), 422, "unknown_saved_query"},
		{"conflicting replay", "POST", "/v0/saved-queries", monitor, strings.Replace(savedQueryBody, "Wire", "Renamed", 1), 409, "idempotency_conflict"},
		{"other Organization read", "GET", "/v0/saved-queries/saved_query_q1", monitorOther, "", 404, "not_found"},
		{"unknown Subscription disable", "POST", "/v0/subscriptions/missing/disable", monitor, `{"idempotency_key":"d"}`, 404, "not_found"},
		{"disable without key", "POST", "/v0/subscriptions/missing/disable", monitor, `{}`, 422, "invalid_schema"},
		{"unknown Subscription enable", "POST", "/v0/subscriptions/missing/enable", monitor, `{"idempotency_key":"e"}`, 404, "not_found"},
		{"read-only enable", "POST", "/v0/subscriptions/missing/enable", monitorReader, `{"idempotency_key":"e"}`, 403, "forbidden"},
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

func ownedBody(key, owner string) string {
	body := subscriptionBody(key, "quivr.fixture", "receiver_a")
	return strings.Replace(body, `"name":"Alerts"`, `"name":"Alerts","owner":`+owner, 1)
}

// TestSubscriptionOwnerRoutes drives the Subscription Owner through the API:
// creation with or without an owner, its echo on reads, the listing by owner
// and of global Subscriptions with a signed page cursor bound to its filter,
// and the public refusals.
func TestSubscriptionOwnerRoutes(t *testing.T) {
	server := monitoringServer(t)
	call(t, server, "POST", "/v0/saved-queries", monitor, savedQueryBody, 201)
	for _, key := range []string{"s3", "s1", "s2"} {
		created, _ := call(t, server, "POST", "/v0/subscriptions", monitor, ownedBody(key, `"user-123"`), 201)
		if created["owner"] != "user-123" || created["current_version"].(map[string]any)["owner"] != "user-123" {
			t.Fatalf("owner on creation: %v", created)
		}
	}
	global, _ := call(t, server, "POST", "/v0/subscriptions", monitor, subscriptionBody("g1", "quivr.fixture", "receiver_a"), 201)
	if _, has := global["owner"]; has {
		t.Fatalf("a global Subscription has no owner: %v", global)
	}
	call(t, server, "POST", "/v0/subscriptions", monitor, ownedBody("other", `"user-456"`), 201)
	call(t, server, "POST", "/v0/subscriptions/subscription_s3/disable", monitor, `{"idempotency_key":"d3"}`, 200)
	if read, _ := call(t, server, "GET", "/v0/subscriptions/subscription_s1", monitorReader, "", 200); read["owner"] != "user-123" {
		t.Fatalf("owner on read: %v", read)
	}

	list := func(query string) (map[string]any, []string) {
		page, _ := call(t, server, "GET", "/v0/subscriptions?"+query, monitorReader, "", 200)
		ids := []string{}
		for _, item := range page["items"].([]any) {
			ids = append(ids, item.(map[string]any)["subscription_id"].(string))
		}
		return page, ids
	}
	first, ids := list("owner=user-123&limit=1")
	if !slices.Equal(ids, []string{"subscription_s1"}) || first["next_page_cursor"] == nil {
		t.Fatalf("first page: %v %v", ids, first)
	}
	cursor := url.QueryEscape(first["next_page_cursor"].(string))
	if last, ids := list("owner=user-123&limit=1&page_cursor=" + cursor); !slices.Equal(ids, []string{"subscription_s2"}) || last["next_page_cursor"] != nil {
		t.Fatalf("last page (the disabled one is not active): %v %v", ids, last)
	}
	if _, ids = list("owner=none"); !slices.Equal(ids, []string{"subscription_g1"}) {
		t.Fatalf("global listing: %v", ids)
	}
	if _, ids = list("owner=user-789"); len(ids) != 0 {
		t.Fatalf("unknown owner: %v", ids)
	}
	refusals := []struct {
		method, path, token, body string
		status                    int
		code                      string
	}{
		{"GET", "/v0/subscriptions?owner=none&page_cursor=" + cursor, monitorReader, "", 409, "cursor_scope_changed"},
		{"GET", "/v0/subscriptions?owner=user-123&page_cursor=" + cursor, monitorNarrow, "", 409, "cursor_scope_changed"},
		{"GET", "/v0/subscriptions?owner=user-123&page_cursor=forged", monitorReader, "", 422, "invalid_cursor"},
		{"GET", "/v0/subscriptions", monitorReader, "", 422, "invalid_query"},
		{"GET", "/v0/subscriptions?owner=", monitorReader, "", 422, "invalid_query"},
		{"GET", "/v0/subscriptions?owner=a&owner=b", monitorReader, "", 422, "invalid_query"},
		{"GET", "/v0/subscriptions?owner=user-123&corpus_id=c", monitorReader, "", 422, "invalid_query"},
		{"GET", "/v0/subscriptions?owner=user-123&limit=101", monitorReader, "", 422, "invalid_limit"},
		{"GET", "/v0/subscriptions?owner=" + strings.Repeat("u", 129), monitorReader, "", 422, "invalid_owner"},
		{"GET", "/v0/subscriptions?owner=user%0A1", monitorReader, "", 422, "invalid_owner"},
		{"GET", "/v0/subscriptions?owner=user-123", noMonitoring, "", 403, "forbidden"},
		{"POST", "/v0/subscriptions", monitor, ownedBody("bad-none", `"none"`), 422, "invalid_owner"},
		{"POST", "/v0/subscriptions", monitor, ownedBody("bad-control", `"user\u0007"`), 422, "invalid_owner"},
		{"POST", "/v0/subscriptions", monitor, ownedBody("bad-empty", `""`), 422, "invalid_schema"},
		{"POST", "/v0/subscriptions", monitor, ownedBody("bad-long", `"`+strings.Repeat("u", 129)+`"`), 422, "invalid_schema"},
		{"POST", "/v0/subscriptions", monitor, ownedBody("bad-type", `42`), 422, "invalid_schema"},
		{"POST", "/v0/subscriptions/subscription_s1/versions", monitor, `{"idempotency_key":"e","owner":"user-456","saved_query_version_id":"saved_query_version_q1","evaluator":{"plugin_id":"quivr.fixture","version":"1","configuration":{}},"destination_id":"receiver_a"}`, 422, "invalid_schema"},
	}
	for _, r := range refusals {
		if got, _ := call(t, server, r.method, r.path, r.token, r.body, r.status); got["code"] != r.code {
			t.Errorf("%s %s: %v, want %s", r.method, r.path, got, r.code)
		}
	}

	// A Match carries its Subscription's owner, a global one none.
	if m, _ := call(t, server, "GET", "/v0/matches/match_3", monitorReader, "", 200); m["owner"] != "user-123" {
		t.Fatalf("owned Match: %v", m)
	}
	if m, _ := call(t, server, "GET", "/v0/matches/match_1", monitorReader, "", 200); m["owner"] != nil {
		t.Fatalf("global Match: %v", m)
	}
}
