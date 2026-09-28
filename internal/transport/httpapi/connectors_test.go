package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
)

func TestPublicIngestionRefusesTheReservedConnectorKeyFamily(t *testing.T) {
	port := &acceptancePort{}
	handler := newAPI(t, port)
	reserved := inline("connector:item_x", "a", "Alpha")
	if status, body := postJSON(t, handler, "/v0/records", adminKey, reserved); status != 422 || body["code"] != "reserved_idempotency_key" {
		t.Fatalf("single: %d %v", status, body)
	}
	status, body := postJSON(t, handler, "/v0/records/batch", adminKey, map[string]any{"items": []any{reserved, inline("ok", "b", "Beta")}})
	items := body["items"].([]any)
	if status != 200 || items[0].(map[string]any)["error"].(map[string]any)["code"] != "reserved_idempotency_key" || items[1].(map[string]any)["receipt"] == nil {
		t.Fatalf("batch: %d %v", status, body)
	}
	withdrawal := map[string]any{"idempotency_key": "connector:withdraw_x", "source": map[string]any{"corpus_id": "corpus_news", "namespace": "feed", "record_key": "a"}}
	if status, body := postJSON(t, handler, "/v0/records/withdrawals", adminKey, withdrawal); status != 422 || body["code"] != "reserved_idempotency_key" {
		t.Fatalf("withdrawal: %d %v", status, body)
	}
	if len(port.accepted) != 1 || port.accepted[0] != "ok" {
		t.Fatalf("accepted %v", port.accepted)
	}
}

// memoryConnectors is a minimal in-memory connectors.Store.
type memoryConnectors struct {
	items map[string]connectors.Instance
}

func (m *memoryConnectors) CreateConnector(_ context.Context, n connectors.NewInstance) (connectors.Instance, error) {
	in := n.Instance
	in.CreatedAt = time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	in.Health = connectors.Health{State: connectors.HealthActive, EvaluatedAt: in.CreatedAt}
	if n.Credential != nil {
		in.Credential = &connectors.CredentialInfo{Version: 1, DepositedAt: in.CreatedAt, ExpiresAt: n.Credential.ExpiresAt}
	}
	m.items[in.ID] = in
	return in, nil
}
func (m *memoryConnectors) ReadConnector(_ context.Context, org, id string) (connectors.Instance, error) {
	in, ok := m.items[id]
	if !ok || in.Organization != org {
		return in, corpus.ErrNotFound
	}
	return in, nil
}
func (m *memoryConnectors) ListConnectors(context.Context, corpus.Scope, string, string, int) ([]connectors.Instance, error) {
	return nil, nil
}
func (m *memoryConnectors) DisableConnector(ctx context.Context, org, id string) (connectors.Instance, error) {
	in, err := m.ReadConnector(ctx, org, id)
	in.Enabled = false
	in.Health.State = connectors.HealthDisabled
	m.items[id] = in
	return in, err
}
func (m *memoryConnectors) ReplaceCredential(ctx context.Context, org, id string, d connectors.CredentialDeposit) (connectors.Instance, error) {
	return m.ReadConnector(ctx, org, id)
}

const connectorKey = "connector-key-0123456789abcdef0123456"

func connectorAPI(t *testing.T) http.Handler {
	t.Helper()
	registry, err := connectors.NewRegistry(connectors.Fixture{})
	if err != nil {
		t.Fatal(err)
	}
	sealer, _ := connectors.NewSealer("handler-test-credential-key-0123456789")
	keys := map[string]corpus.Scope{
		connectorKey: {Organization: "org_a", Actions: []string{"connectors:read", "connectors:write"}, Corpora: []string{"corpus_news"}},
		readerKey:    {Organization: "org_a", Actions: []string{"connectors:read"}, Corpora: []string{"*"}},
	}
	handler, err := httpapi.New(nil, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"),
		httpapi.WithConnectors(connectors.Service{Store: &memoryConnectors{items: map[string]connectors.Instance{}}, Registry: registry, Sealer: sealer}))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestConnectorCreationValidatesAndNeverEchoesTheSecret(t *testing.T) {
	handler := connectorAPI(t)
	valid := func() map[string]any {
		return map[string]any{"idempotency_key": "c1", "corpus_id": "corpus_news", "source_namespace": "wire", "kind": "fixture", "config": map[string]any{"script": []any{}},
			"credential": map[string]any{"secret": map[string]any{"token": "fixture-test-secret-handler"}, "expires_at": "2027-01-01T00:00:00Z"}}
	}
	status, body := postJSON(t, handler, "/v0/connectors", connectorKey, valid())
	if status != 201 {
		t.Fatalf("create: %d %v", status, body)
	}
	conforms(t, "Connector", body)
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "fixture-test-secret") {
		t.Fatalf("secret echoed: %s", raw)
	}
	if body["schedule"].(map[string]any)["interval_seconds"].(float64) != 300 || body["credential"].(map[string]any)["version"].(float64) != 1 {
		t.Fatalf("defaults %v", body)
	}
	id := body["connector_id"].(string)
	cases := []struct {
		name   string
		key    string
		edit   func(map[string]any)
		status int
		code   string
	}{
		{"missing write action", readerKey, func(map[string]any) {}, 403, "forbidden"},
		{"Corpus outside scope", connectorKey, func(b map[string]any) { b["corpus_id"] = "corpus_other" }, 404, "not_found"},
		{"undelivered kind", connectorKey, func(b map[string]any) { b["kind"] = "rss" }, 422, "unsupported_connector_kind"},
		{"unknown kind", connectorKey, func(b map[string]any) { b["kind"] = "ftp" }, 422, "invalid_schema"},
		{"config fails the kind schema", connectorKey, func(b map[string]any) { b["config"] = map[string]any{"script": "x"} }, 422, "invalid_config"},
		{"secret fails the kind schema", connectorKey, func(b map[string]any) { b["credential"] = map[string]any{"secret": map[string]any{"password": "x"}} }, 422, "invalid_credential"},
		{"interval below the floor", connectorKey, func(b map[string]any) { b["schedule"] = map[string]any{"interval_seconds": 29} }, 422, "invalid_interval"},
	}
	for _, c := range cases {
		b := valid()
		c.edit(b)
		status, body := postJSON(t, handler, "/v0/connectors", c.key, b)
		if status != c.status || body["code"] != c.code {
			t.Errorf("%s: %d %v", c.name, status, body)
		}
	}
	if status, body := readJSON(t, handler, "/v0/connectors/"+id, readerKey); status != 200 || body["connector_id"] != id {
		t.Fatalf("read: %d %v", status, body)
	}
	if status, _ := readJSON(t, handler, "/v0/connectors/connector_missing", readerKey); status != 404 {
		t.Fatalf("missing: %d", status)
	}
	if status, body := postJSON(t, handler, "/v0/connectors/"+id+"/disable", connectorKey, map[string]any{"idempotency_key": "d1"}); status != 200 || body["enabled"] != false || body["health"].(map[string]any)["state"] != "disabled" {
		t.Fatalf("disable: %d %v", status, body)
	}
}

func readJSON(t *testing.T, handler http.Handler, path, key string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}
