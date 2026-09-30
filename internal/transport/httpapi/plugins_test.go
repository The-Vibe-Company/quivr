package httpapi_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

type memoryRegistry struct {
	registrations []registry.Registration
	plan          *registry.Plan
}

func (m memoryRegistry) SeedPlugins(context.Context, registry.Seed) (bool, error) { return false, nil }
func (m memoryRegistry) PluginRegistrations(context.Context) ([]registry.Registration, error) {
	return m.registrations, nil
}
func (m memoryRegistry) ActivePlan(context.Context) (registry.Plan, error) {
	if m.plan == nil {
		return registry.Plan{}, registry.ErrNoPlan
	}
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
		if res, _ := operationCall(t, server, "POST", path, pluginOperator, "application/json", "{}"); res.StatusCode != 405 {
			t.Fatalf("POST %s: %d, want 405", path, res.StatusCode)
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
