package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

type registryRows struct {
	registry.Store
	registrations     []registry.Registration
	registrationError error
	plan              *registry.Plan
	registration      registry.Registration
	key               string
	readID            string
	activatedID       string
	rollback          registry.RollbackRequest
	limit             int
}

func (m *registryRows) PluginRegistrations(context.Context) ([]registry.Registration, error) {
	return m.registrations, nil
}
func (m *registryRows) PluginRegistration(_ context.Context, id string) (registry.Registration, error) {
	m.readID = id
	if m.registrationError != nil {
		return registry.Registration{}, m.registrationError
	}
	return m.registrations[0], nil
}
func (m *registryRows) ActivePlan(context.Context) (registry.Plan, error) {
	if m.plan == nil {
		return registry.Plan{}, registry.ErrNoPlan
	}
	return *m.plan, nil
}
func (m *registryRows) PipelinePlan(_ context.Context, id string) (registry.Plan, error) {
	m.readID = id
	return *m.plan, nil
}
func (m *registryRows) RegisterPlugin(_ context.Context, registration registry.Registration, key string) (registry.Registration, bool, error) {
	m.registration, m.key = registration, key
	return m.registrations[0], true, nil
}
func (m *registryRows) Activate(_ context.Context, id string, _ func(registry.Plan, map[string]registry.Registration, registry.Registration) (registry.Activation, error)) (registry.Plan, error) {
	m.activatedID = id
	return *m.plan, nil
}
func (m *registryRows) PipelinePlans(_ context.Context, limit int) ([]registry.Plan, error) {
	m.limit = limit
	return []registry.Plan{*m.plan}, nil
}
func (m *registryRows) Rollback(_ context.Context, request registry.RollbackRequest, _ func(registry.Plan, registry.Plan, map[string]registry.Registration) (registry.Activation, error)) (registry.Plan, error) {
	m.rollback = request
	return *m.plan, nil
}

const (
	pluginOperator = "plugin-operator-token-0123456789abcdef012345"
	// Every other action of an Organization key, never plugins:admin.
	organizationAdmin    = "organization-admin-token-0123456789abcdef01"
	pluginObserver       = "observer-token-0123456789abcdef0123456789ab"
	fencedPluginObserver = "fenced-observer-token-0123456789abcdef01234"
)

func pluginServer(t *testing.T, store registry.Store) *httptest.Server {
	t.Helper()
	keys := map[string]corpus.Scope{
		pluginOperator:       {Organization: "org_ops", Actions: []string{registry.Action}, Corpora: []string{"*"}},
		organizationAdmin:    {Organization: "org_a", Actions: []string{"corpora:read", "corpora:write", "content:read", "content:write", "search:query", "changes:read", "monitoring:read", "monitoring:write", "connectors:read", "connectors:write", "projections:rebuild", "operations:read", "operations:write"}, Corpora: []string{"*"}},
		pluginObserver:       {Organization: "org_o", Actions: []string{content.ObservabilityRead}, Corpora: []string{"*"}},
		fencedPluginObserver: {Organization: "org_o", Actions: []string{content.ObservabilityRead}, Corpora: []string{"corpus_1"}},
	}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"), httpapi.WithPlugins(registry.Service{Store: store}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func TestPluginRegistryTransportGuards(t *testing.T) {
	server := pluginServer(t, nil)
	for _, route := range []struct{ method, path string }{
		{"GET", "/v0/admin/plugins"},
		{"POST", "/v0/admin/plugins"},
		{"GET", "/v0/admin/plugins/plan"},
		{"GET", "/v0/admin/plugins/plans"},
		{"GET", "/v0/admin/plugins/plans/plan_1"},
		{"GET", "/v0/admin/plugins/plugin_registration_x"},
		{"POST", "/v0/admin/plugins/plugin_registration_x/activate"},
		{"POST", "/v0/admin/plugins/plan/rollback"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			for _, access := range []struct {
				key, code string
				status    int
			}{
				{organizationAdmin, "forbidden", 403},
				{pluginOperator, "not_found", 404},
			} {
				res, body := operationCall(t, server, route.method, route.path, access.key, "application/json", "{}")
				if res.StatusCode != access.status || body["code"] != access.code {
					t.Fatalf("disabled registry: %d %v, want %d %s", res.StatusCode, body, access.status, access.code)
				}
			}
			wrongMethod := "DELETE"
			if route.method == "POST" && route.path != "/v0/admin/plugins" {
				wrongMethod = "GET"
			}
			if res, body := operationCall(t, server, wrongMethod, route.path, pluginOperator, "", ""); res.StatusCode != 405 || body["code"] != "method_not_allowed" {
				t.Fatalf("%s: %d %v, want 405 method_not_allowed", wrongMethod, res.StatusCode, body)
			}
		})
	}
}

func TestPluginRegistryReadResponses(t *testing.T) {
	at := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	pdf := registry.Registration{ID: "plugin_registration_pdf", PluginID: "pdf-text", Version: "0.1.0", Endpoint: "http://127.0.0.1:9900", ManifestDigest: "sha256:pdf", Contributions: []string{"normalizer"}, Roles: []string{"normalizer:application/pdf"}, State: registry.StateActive, CreatedAt: at, UpdatedAt: at}
	pdf.Check = &registry.CheckReport{Certified: true, CheckedAt: at, Passed: 1}
	plan := registry.Plan{ID: "plan_1", CreatedAt: at, ActivatedAt: at, Roles: []registry.Assignment{{Role: "normalizer:application/pdf", RegistrationID: pdf.ID, PluginID: "pdf-text", Version: "0.1.0"}}}
	store := &registryRows{registrations: []registry.Registration{pdf}, plan: &plan}
	server := pluginServer(t, store)

	res, list := operationCall(t, server, "GET", "/v0/admin/plugins", pluginOperator, "", "")
	items, _ := list["items"].([]any)
	if res.StatusCode != 200 || len(items) != 1 {
		t.Fatalf("list: %d %v", res.StatusCode, list)
	}
	item := items[0].(map[string]any)
	if item["registration_id"] != pdf.ID || item["plugin_id"] != "pdf-text" || item["state"] != "active" || item["endpoint"] != pdf.Endpoint || item["created_at"] != "2026-09-30T10:00:00Z" {
		t.Fatalf("registration %v", item)
	}
	if _, reported := item["artifact_digest"]; reported {
		t.Fatalf("registration %v reports an artifact digest the plugin never gave", item)
	}
	if _, reported := item["check"]; reported {
		t.Fatalf("the list includes the detail-only check report: %v", item)
	}
	res, detail := operationCall(t, server, "GET", "/v0/admin/plugins/"+pdf.ID, pluginOperator, "", "")
	check, err := json.Marshal(detail["check"])
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || store.readID != pdf.ID || detail["registration_id"] != pdf.ID || string(check) != `{"certified":true,"checked_at":"2026-09-30T10:00:00Z","checks":[],"failed":0,"passed":1,"skipped":0}` {
		t.Fatalf("registration detail: %d %v (requested %q), want the registration and its check report", res.StatusCode, detail, store.readID)
	}

	res, body := operationCall(t, server, "GET", "/v0/admin/plugins/plan", pluginOperator, "", "")
	roles, _ := body["roles"].([]any)
	if res.StatusCode != 200 || body["plan_id"] != "plan_1" || len(roles) != 1 || roles[0].(map[string]any)["registration_id"] != pdf.ID {
		t.Fatalf("plan: %d %v", res.StatusCode, body)
	}

	unseeded := pluginServer(t, &registryRows{})
	if res, list := operationCall(t, unseeded, "GET", "/v0/admin/plugins", pluginOperator, "", ""); res.StatusCode != 200 || !reflect.DeepEqual(list["items"], []any{}) {
		t.Fatalf("list of an unseeded registry: %d %v, want 200 with empty items", res.StatusCode, list)
	}
	for path, rows := range map[string]*registryRows{
		"/v0/admin/plugins/plan":    {},
		"/v0/admin/plugins/unknown": {registrationError: registry.ErrNotFound},
	} {
		t.Run(path, func(t *testing.T) {
			res, body := operationCall(t, pluginServer(t, rows), "GET", path, pluginOperator, "", "")
			if res.StatusCode != 404 || body["code"] != "not_found" {
				t.Fatalf("store refusal: %d %v, want 404 not_found", res.StatusCode, body)
			}
		})
	}
}

func TestActivePluginsNeedOnlyObservabilityRead(t *testing.T) {
	at := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	plan := registry.Plan{ID: "plan_1", CreatedAt: at, ActivatedAt: at, Roles: []registry.Assignment{
		{Role: "connector:rss", RegistrationID: "plugin_registration_rss", PluginID: "rss", Version: "1.0.0"},
		{Role: "ingestion", RegistrationID: "plugin_registration_core", PluginID: "core-ingest", Version: "0.2.0"},
		{Role: "normalizer:application/pdf", RegistrationID: "plugin_registration_pdf", PluginID: "pdf-text", Version: "0.1.0"},
		{Role: "normalizer:text/html", RegistrationID: "plugin_registration_pdf", PluginID: "pdf-text", Version: "0.1.0"},
		{Role: "normalizer:text/plain", RegistrationID: "plugin_registration_pdf_old", PluginID: "pdf-text", Version: "0.0.9"},
	}}
	disabled := pluginServer(t, nil)
	for _, key := range []string{organizationAdmin, fencedPluginObserver} {
		if res, body := operationCall(t, disabled, "GET", "/v0/admin/active-plugins", key, "", ""); res.StatusCode != 403 || body["code"] != "forbidden" {
			t.Fatalf("without observability:read on every Corpus: %d %v, want 403 forbidden", res.StatusCode, body)
		}
	}
	if res, body := operationCall(t, disabled, "GET", "/v0/admin/active-plugins", pluginObserver, "", ""); res.StatusCode != 404 || body["code"] != "not_found" {
		t.Fatalf("disabled registry: %d %v, want 404 not_found", res.StatusCode, body)
	}
	if res, body := operationCall(t, disabled, "DELETE", "/v0/admin/active-plugins", pluginObserver, "", ""); res.StatusCode != 405 || body["code"] != "method_not_allowed" {
		t.Fatalf("DELETE active plugins: %d %v, want 405 method_not_allowed", res.StatusCode, body)
	}
	server := pluginServer(t, &registryRows{plan: &plan})
	res, body := operationCall(t, server, "GET", "/v0/admin/active-plugins", pluginObserver, "", "")
	got, _ := json.Marshal(body)
	want := `{"items":[{"plugin_id":"core-ingest","roles":["ingestion"],"version":"0.2.0"},{"plugin_id":"pdf-text","roles":["normalizer:text/plain"],"version":"0.0.9"},{"plugin_id":"pdf-text","roles":["normalizer:application/pdf","normalizer:text/html"],"version":"0.1.0"},{"plugin_id":"rss","roles":["connector:rss"],"version":"1.0.0"}],"plan_activated_at":"2026-09-30T10:00:00Z"}`
	if res.StatusCode != 200 || string(got) != want {
		t.Fatalf("active plugins: %d\n got %s\nwant %s", res.StatusCode, got, want)
	}
	if res, body := operationCall(t, pluginServer(t, &registryRows{}), "GET", "/v0/admin/active-plugins", pluginObserver, "", ""); res.StatusCode != 200 || !reflect.DeepEqual(body, map[string]any{"items": []any{}}) {
		t.Fatalf("without an active plan: %d %v, want 200 and no item", res.StatusCode, body)
	}
}

func TestPluginRegistrationAndActivationRoutes(t *testing.T) {
	at := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	plan := registry.Plan{ID: "plan_1", CreatedAt: at, ActivatedAt: at, Source: registry.SourceActivation, PreviousPlanID: "plan_0"}
	stored := registry.Registration{ID: "plugin_registration_stored", PluginID: "example.hash_embedder", State: registry.StateRegistered}
	store := &registryRows{plan: &plan, registrations: []registry.Registration{stored}}
	server := pluginServer(t, store)
	manifest, err := os.ReadFile("../../../sdks/go/examples/hash-embedder/quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	body := func(key, manifest string) string {
		b, err := json.Marshal(map[string]any{"idempotency_key": key, "endpoint": "http://127.0.0.1:9961", "manifest": manifest, "configuration": map[string]any{"batch_size": 3}, "fixtures": map[string][]byte{"inputs/article.txt": {0xff, 0x00}}, "spaces": map[string]string{"example.hash_embedder.small": "served", "example.hash_embedder.large": "evaluation"}})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if res, body := operationCall(t, server, "POST", "/v0/admin/plugins", pluginOperator, "application/json", `{"idempotency_key":"k"}`); res.StatusCode != 422 || body["code"] != "invalid_schema" {
		t.Fatalf("register without manifest: %d %v", res.StatusCode, body)
	}
	if res, body := operationCall(t, server, "POST", "/v0/admin/plugins", pluginOperator, "application/json", body("k", strings.Replace(string(manifest), "dimensions: 16", "dimensions: sixteen", 1))); res.StatusCode != 422 || body["code"] != "invalid_plugin" || !strings.Contains(body["message"].(string), "dimensions") {
		t.Fatalf("register a manifest the engine refuses: %d %v", res.StatusCode, body)
	}
	res, registered := operationCall(t, server, "POST", "/v0/admin/plugins", pluginOperator, "application/json", body("k", string(manifest)))
	if res.StatusCode != 202 || registered["registration_id"] != stored.ID || registered["plugin_id"] != "example.hash_embedder" || registered["state"] != "registered" || res.Header.Get("Location") != "/v0/admin/plugins/"+stored.ID {
		t.Fatalf("register: %d %v (Location %q)", res.StatusCode, registered, res.Header.Get("Location"))
	}
	if store.key != "k" || store.registration.Endpoint != "http://127.0.0.1:9961" || string(store.registration.Manifest) != string(manifest) || string(store.registration.Settings.Configuration) != `{"batch_size":3}` || !reflect.DeepEqual(store.registration.Fixtures, map[string][]byte{"inputs/article.txt": {0xff, 0x00}}) || !reflect.DeepEqual(store.registration.Settings.Spaces, map[string]string{"example.hash_embedder.small": "served", "example.hash_embedder.large": "evaluation"}) {
		t.Fatalf("registration inputs: key %q, registration %+v", store.key, store.registration)
	}
	if res, body := operationCall(t, server, "GET", "/v0/admin/plugins/plans/plan_1", pluginOperator, "", ""); res.StatusCode != 200 || body["source"] != "activation" || body["plan_id"] != "plan_1" || store.readID != "plan_1" {
		t.Fatalf("read a plan: %d %v", res.StatusCode, body)
	}
	if res, body := operationCall(t, server, "POST", "/v0/admin/plugins/plugin_registration_x/activate", pluginOperator, "application/json", "{}"); res.StatusCode != 200 || body["plan_id"] != "plan_1" || store.activatedID != "plugin_registration_x" {
		t.Fatalf("activate: %d %v", res.StatusCode, body)
	}

	rollback := "/v0/admin/plugins/plan/rollback"
	for _, invalid := range []string{`{}`, `{"idempotency_key":"r","pinned_work":"cancel"}`} {
		if res, body := operationCall(t, server, "POST", rollback, pluginOperator, "application/json", invalid); res.StatusCode != 422 || body["code"] != "invalid_schema" {
			t.Fatalf("rollback %s: %d %v, want 422 invalid_schema", invalid, res.StatusCode, body)
		}
	}
	if res, body := operationCall(t, server, "POST", rollback, pluginOperator, "application/json", `{"idempotency_key":"r","plan_id":"plan_0","pinned_work":"stop"}`); res.StatusCode != 200 || body["plan_id"] != "plan_1" || body["previous_plan_id"] != "plan_0" {
		t.Fatalf("rollback: %d %v, want the plan naming the one it replaced", res.StatusCode, body)
	}
	if store.rollback != (registry.RollbackRequest{Key: "r", Plan: "plan_0", PinnedWork: "stop"}) {
		t.Fatalf("rollback inputs: %+v", store.rollback)
	}
	for _, query := range []struct {
		suffix string
		limit  int
	}{{"", 20}, {"?limit=2", 2}} {
		res, list := operationCall(t, server, "GET", "/v0/admin/plugins/plans"+query.suffix, pluginOperator, "", "")
		if items, _ := list["items"].([]any); res.StatusCode != 200 || len(items) != 1 || items[0].(map[string]any)["plan_id"] != "plan_1" || store.limit != query.limit {
			t.Fatalf("plan history: %d %v (limit %d, want %d)", res.StatusCode, list, store.limit, query.limit)
		}
	}
	for _, limit := range []string{"101", ""} {
		if res, body := operationCall(t, server, "GET", "/v0/admin/plugins/plans?limit="+limit, pluginOperator, "", ""); res.StatusCode != 422 || body["code"] != "invalid_limit" {
			t.Fatalf("plan history with limit %q: %d %v, want 422 invalid_limit", limit, res.StatusCode, body)
		}
	}
}
