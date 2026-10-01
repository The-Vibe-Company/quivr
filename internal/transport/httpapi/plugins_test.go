package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
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

// memoryRegistry echoes what it holds; the registry's rules are owned by
// the registry and adapter tests, and the error mapping by
// TestPluginFailureCodesIgnoreDetail.
type memoryRegistry struct {
	registrations []registry.Registration
	plan          *registry.Plan
}

func (m memoryRegistry) ApplyConfiguration(context.Context, registry.Seed) (registry.Applied, error) {
	return registry.Applied{}, nil
}
func (m memoryRegistry) PluginRegistrations(context.Context) ([]registry.Registration, error) {
	return m.registrations, nil
}
func (m memoryRegistry) PluginRegistration(_ context.Context, id string) (registry.Registration, error) {
	for _, r := range m.registrations {
		if r.ID == id {
			return r, nil
		}
	}
	return registry.Registration{}, registry.ErrNotFound
}
func (m memoryRegistry) ActivePlan(context.Context) (registry.Plan, error) {
	if m.plan == nil {
		return registry.Plan{}, registry.ErrNoPlan
	}
	return *m.plan, nil
}
func (m memoryRegistry) PipelinePlan(ctx context.Context, id string) (registry.Plan, error) {
	if m.plan == nil || m.plan.ID != id {
		return registry.Plan{}, registry.ErrNotFound
	}
	return *m.plan, nil
}
func (m memoryRegistry) ActivePlanID(context.Context) (string, error) { return "", nil }
func (m memoryRegistry) ActiveMembers(context.Context) (registry.Plan, map[string]registry.Registration, error) {
	return registry.Plan{}, nil, registry.ErrNoPlan
}
func (m memoryRegistry) PlanMembers(context.Context, string) (registry.Plan, map[string]registry.Registration, error) {
	return registry.Plan{}, nil, registry.ErrNotFound
}
func (m memoryRegistry) EvaluatorRegistrations(context.Context) ([]registry.Registration, error) {
	return nil, nil
}
func (m memoryRegistry) RegisterPlugin(_ context.Context, r registry.Registration, _ string) (registry.Registration, bool, error) {
	return r, true, nil
}
func (m memoryRegistry) ClaimCheck(context.Context, time.Duration) (registry.Registration, bool, error) {
	return registry.Registration{}, false, nil
}
func (m memoryRegistry) RecordCheck(context.Context, string, registry.CheckReport) error { return nil }
func (m memoryRegistry) Activate(context.Context, string, func(registry.Plan, map[string]registry.Registration, registry.Registration) (registry.Activation, error)) (registry.Plan, error) {
	return *m.plan, nil
}
func (m memoryRegistry) PipelinePlans(context.Context, int) ([]registry.Plan, error) {
	return []registry.Plan{*m.plan}, nil
}
func (m memoryRegistry) Rollback(context.Context, registry.RollbackRequest, func(registry.Plan, registry.Plan, map[string]registry.Registration) (registry.Activation, error)) (registry.Plan, error) {
	return *m.plan, nil
}

const (
	pluginOperator = "plugin-operator-token-0123456789abcdef012345"
	// Every other action of an Organization key, never plugins:admin.
	organizationAdmin = "organization-admin-token-0123456789abcdef01"
)

func pluginServer(t *testing.T, store memoryRegistry) *httptest.Server {
	t.Helper()
	keys := map[string]corpus.Scope{
		pluginOperator:    {Organization: "org_ops", Actions: []string{registry.Action}, Corpora: []string{"*"}},
		organizationAdmin: {Organization: "org_a", Actions: []string{"corpora:read", "corpora:write", "content:read", "content:write", "search:query", "changes:read", "monitoring:read", "monitoring:write", "connectors:read", "connectors:write", "projections:rebuild", "operations:read", "operations:write"}, Corpora: []string{"*"}},
	}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"), httpapi.WithPlugins(registry.Service{Store: store}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// TestPluginRegistryReadsNeedPluginsAdmin owns the authorization and the
// response mapping of the two operator reads.
func TestPluginRegistryReadsNeedPluginsAdmin(t *testing.T) {
	at := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	pdf := registry.Registration{ID: "plugin_registration_pdf", PluginID: "pdf-text", Version: "0.1.0", Endpoint: "http://127.0.0.1:9900", ManifestDigest: "sha256:pdf", Contributions: []string{"normalizer"}, Roles: []string{"normalizer:application/pdf"}, State: registry.StateActive, CreatedAt: at, UpdatedAt: at}
	plan := registry.Plan{ID: "plan_1", CreatedAt: at, ActivatedAt: at, Roles: []registry.Assignment{{Role: "normalizer:application/pdf", RegistrationID: pdf.ID, PluginID: "pdf-text", Version: "0.1.0"}}}
	server := pluginServer(t, memoryRegistry{registrations: []registry.Registration{pdf}, plan: &plan})

	for _, path := range []string{"/v0/admin/plugins", "/v0/admin/plugins/plan"} {
		if res, body := operationCall(t, server, "GET", path, organizationAdmin, "", ""); res.StatusCode != 403 || body["code"] != "forbidden" {
			t.Fatalf("GET %s with an Organization key: %d %v, want 403 forbidden", path, res.StatusCode, body)
		}
		if res, _ := operationCall(t, server, "DELETE", path, pluginOperator, "", ""); res.StatusCode != 405 {
			t.Fatalf("DELETE %s: %d, want 405", path, res.StatusCode)
		}
	}

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

	res, body := operationCall(t, server, "GET", "/v0/admin/plugins/plan", pluginOperator, "", "")
	roles, _ := body["roles"].([]any)
	if res.StatusCode != 200 || body["plan_id"] != "plan_1" || len(roles) != 1 || roles[0].(map[string]any)["registration_id"] != pdf.ID {
		t.Fatalf("plan: %d %v", res.StatusCode, body)
	}

	unseeded := pluginServer(t, memoryRegistry{})
	if res, body := operationCall(t, unseeded, "GET", "/v0/admin/plugins/plan", pluginOperator, "", ""); res.StatusCode != 404 || body["code"] != "not_found" {
		t.Fatalf("plan of an unseeded registry: %d %v, want 404 not_found", res.StatusCode, body)
	}
	if res, list := operationCall(t, unseeded, "GET", "/v0/admin/plugins", pluginOperator, "", ""); res.StatusCode != 200 || list["items"] == nil {
		t.Fatalf("list of an unseeded registry: %d %v, want 200 with empty items", res.StatusCode, list)
	}
}

// TestActivePluginsNeedOnlyObservabilityRead owns the operator views' plugin
// list: observability:read on every Corpus reads it without plugins:admin,
// it groups the plan's roles by plugin version, and it never names an
// address.
func TestActivePluginsNeedOnlyObservabilityRead(t *testing.T) {
	at := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	plan := registry.Plan{ID: "plan_1", CreatedAt: at, ActivatedAt: at, Roles: []registry.Assignment{
		{Role: "connector:rss", RegistrationID: "plugin_registration_rss", PluginID: "rss", Version: "1.0.0"},
		{Role: "ingestion", RegistrationID: "plugin_registration_core", PluginID: "core-ingest", Version: "0.2.0"},
		{Role: "normalizer:application/pdf", RegistrationID: "plugin_registration_pdf", PluginID: "pdf-text", Version: "0.1.0"},
		{Role: "normalizer:text/html", RegistrationID: "plugin_registration_pdf", PluginID: "pdf-text", Version: "0.1.0"},
	}}
	const observer = "observer-token-0123456789abcdef0123456789ab"
	const fenced = "fenced-observer-token-0123456789abcdef01234"
	keys := map[string]corpus.Scope{
		observer:          {Organization: "org_o", Actions: []string{content.ObservabilityRead}, Corpora: []string{"*"}},
		fenced:            {Organization: "org_o", Actions: []string{content.ObservabilityRead}, Corpora: []string{"corpus_1"}},
		organizationAdmin: {Organization: "org_a", Actions: []string{"corpora:read", "content:read", "search:query"}, Corpora: []string{"*"}},
	}
	serve := func(store memoryRegistry) *httptest.Server {
		handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"), httpapi.WithPlugins(registry.Service{Store: store}))
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		return server
	}
	server := serve(memoryRegistry{plan: &plan})
	// The plan is the deployment's: a key limited to some Corpora cannot read it.
	for _, key := range []string{organizationAdmin, fenced} {
		if res, body := operationCall(t, server, "GET", "/v0/admin/active-plugins", key, "", ""); res.StatusCode != 403 || body["code"] != "forbidden" {
			t.Fatalf("without observability:read on every Corpus: %d %v, want 403 forbidden", res.StatusCode, body)
		}
	}
	res, body := operationCall(t, server, "GET", "/v0/admin/active-plugins", observer, "", "")
	got, _ := json.Marshal(body)
	want := `{"items":[{"plugin_id":"core-ingest","roles":["ingestion"],"version":"0.2.0"},{"plugin_id":"pdf-text","roles":["normalizer:application/pdf","normalizer:text/html"],"version":"0.1.0"},{"plugin_id":"rss","roles":["connector:rss"],"version":"1.0.0"}],"plan_activated_at":"2026-09-30T10:00:00Z"}`
	if res.StatusCode != 200 || string(got) != want {
		t.Fatalf("active plugins: %d\n got %s\nwant %s", res.StatusCode, got, want)
	}
	if res, body := operationCall(t, serve(memoryRegistry{}), "GET", "/v0/admin/active-plugins", observer, "", ""); res.StatusCode != 200 || len(body["items"].([]any)) != 0 {
		t.Fatalf("without an active plan: %d %v, want 200 and no item", res.StatusCode, body)
	}
}

// TestPluginRegistrationAndActivationRoutes owns the operator commands'
// routing, authorization and request validation: an Organization key is
// refused, a manifest the engine refuses is 422 invalid_plugin naming the
// issue, a registration answers 202 with its read URL, and activation and
// plan reads answer the plan.
func TestPluginRegistrationAndActivationRoutes(t *testing.T) {
	at := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	plan := registry.Plan{ID: "plan_1", CreatedAt: at, ActivatedAt: at, Source: registry.SourceActivation, PreviousPlanID: "plan_0"}
	server := pluginServer(t, memoryRegistry{plan: &plan})
	manifest, err := os.ReadFile("../../../sdks/go/examples/hash-embedder/quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	body := func(key, manifest string) string {
		b, _ := json.Marshal(map[string]any{"idempotency_key": key, "endpoint": "http://127.0.0.1:9961", "manifest": manifest, "spaces": map[string]string{"example.hash_embedder.small": "served", "example.hash_embedder.large": "evaluation"}})
		return string(b)
	}
	if res, body := operationCall(t, server, "POST", "/v0/admin/plugins", organizationAdmin, "application/json", body("k", string(manifest))); res.StatusCode != 403 || body["code"] != "forbidden" {
		t.Fatalf("register with an Organization key: %d %v", res.StatusCode, body)
	}
	if res, body := operationCall(t, server, "POST", "/v0/admin/plugins", pluginOperator, "application/json", `{"idempotency_key":"k"}`); res.StatusCode != 422 || body["code"] != "invalid_schema" {
		t.Fatalf("register without manifest: %d %v", res.StatusCode, body)
	}
	if res, body := operationCall(t, server, "POST", "/v0/admin/plugins", pluginOperator, "application/json", body("k", strings.Replace(string(manifest), "dimensions: 16", "dimensions: sixteen", 1))); res.StatusCode != 422 || body["code"] != "invalid_plugin" || !strings.Contains(body["message"].(string), "dimensions") {
		t.Fatalf("register a manifest the engine refuses: %d %v", res.StatusCode, body)
	}
	res, registered := operationCall(t, server, "POST", "/v0/admin/plugins", pluginOperator, "application/json", body("k", string(manifest)))
	if res.StatusCode != 202 || registered["plugin_id"] != "example.hash_embedder" || registered["state"] != "registered" || res.Header.Get("Location") != "/v0/admin/plugins/"+registered["registration_id"].(string) {
		t.Fatalf("register: %d %v (Location %q)", res.StatusCode, registered, res.Header.Get("Location"))
	}
	if res, body := operationCall(t, server, "GET", "/v0/admin/plugins/plans/plan_1", pluginOperator, "", ""); res.StatusCode != 200 || body["source"] != "activation" {
		t.Fatalf("read a plan: %d %v", res.StatusCode, body)
	}
	if res, _ := operationCall(t, server, "GET", "/v0/admin/plugins/unknown", pluginOperator, "", ""); res.StatusCode != 404 {
		t.Fatalf("an unknown registration: %d", res.StatusCode)
	}
	if res, _ := operationCall(t, server, "GET", "/v0/admin/plugins/plugin_registration_x/activate", pluginOperator, "", ""); res.StatusCode != 405 {
		t.Fatalf("GET activate: %d, want 405", res.StatusCode)
	}
	if res, body := operationCall(t, server, "POST", "/v0/admin/plugins/plugin_registration_x/activate", pluginOperator, "application/json", "{}"); res.StatusCode != 200 || body["plan_id"] != "plan_1" {
		t.Fatalf("activate: %d %v", res.StatusCode, body)
	}

	rollback := "/v0/admin/plugins/plan/rollback"
	if res, body := operationCall(t, server, "POST", rollback, organizationAdmin, "application/json", `{"idempotency_key":"r"}`); res.StatusCode != 403 || body["code"] != "forbidden" {
		t.Fatalf("rollback with an Organization key: %d %v", res.StatusCode, body)
	}
	if res, _ := operationCall(t, server, "GET", rollback, pluginOperator, "", ""); res.StatusCode != 405 {
		t.Fatalf("GET rollback: %d, want 405", res.StatusCode)
	}
	for _, invalid := range []string{`{}`, `{"idempotency_key":"r","pinned_work":"cancel"}`} {
		if res, body := operationCall(t, server, "POST", rollback, pluginOperator, "application/json", invalid); res.StatusCode != 422 || body["code"] != "invalid_schema" {
			t.Fatalf("rollback %s: %d %v, want 422 invalid_schema", invalid, res.StatusCode, body)
		}
	}
	if res, body := operationCall(t, server, "POST", rollback, pluginOperator, "application/json", `{"idempotency_key":"r","plan_id":"plan_0","pinned_work":"stop"}`); res.StatusCode != 200 || body["plan_id"] != "plan_1" || body["previous_plan_id"] != "plan_0" {
		t.Fatalf("rollback: %d %v, want the plan naming the one it replaced", res.StatusCode, body)
	}
	res, list := operationCall(t, server, "GET", "/v0/admin/plugins/plans", pluginOperator, "", "")
	if items, _ := list["items"].([]any); res.StatusCode != 200 || len(items) != 1 || items[0].(map[string]any)["plan_id"] != "plan_1" {
		t.Fatalf("plan history: %d %v", res.StatusCode, list)
	}
	if res, body := operationCall(t, server, "GET", "/v0/admin/plugins/plans?limit=101", pluginOperator, "", ""); res.StatusCode != 422 || body["code"] != "invalid_limit" {
		t.Fatalf("plan history over the limit: %d %v", res.StatusCode, body)
	}
}
