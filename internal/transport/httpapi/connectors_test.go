package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
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
func (m *memoryConnectors) ListConnectors(_ context.Context, s corpus.Scope, corpusID, after string, limit int) ([]connectors.Instance, error) {
	out := []connectors.Instance{}
	for _, in := range m.items {
		if in.Organization == s.Organization && (corpusID == "" || in.CorpusID == corpusID) && in.ID > after {
			out = append(out, in)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
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
	return connectorAPIWith(t, &memoryConnectors{items: map[string]connectors.Instance{}})
}

func connectorAPIWith(t *testing.T, store *memoryConnectors) http.Handler {
	t.Helper()
	sealer, _ := connectors.NewSealer("handler-test-credential-key-0123456789")
	return connectorAPISealed(t, store, sealer)
}

func connectorAPISealed(t *testing.T, store *memoryConnectors, sealer connectors.Sealer) http.Handler {
	t.Helper()
	registry, err := connectors.NewRegistry(connectors.Fixture{}, connectors.XList{})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]corpus.Scope{
		connectorKey: {Organization: "org_a", Actions: []string{"connectors:read", "connectors:write"}, Corpora: []string{"corpus_news"}},
		readerKey:    {Organization: "org_a", Actions: []string{"connectors:read"}, Corpora: []string{"*"}},
	}
	handler, err := httpapi.New(nil, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"),
		httpapi.WithConnectors(connectors.Service{Store: store, Registry: registry, Sealer: sealer}))
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

func TestXListInstancesValidateTheirWindowAndExposeUsageAndDiagnostics(t *testing.T) {
	store := &memoryConnectors{items: map[string]connectors.Instance{}}
	handler := connectorAPIWith(t, store)
	body := func(config map[string]any) map[string]any {
		return map[string]any{"idempotency_key": fmt.Sprint(config), "corpus_id": "corpus_news", "source_namespace": fmt.Sprint(len(store.items)), "kind": "x_list", "config": config,
			"credential": map[string]any{"secret": map[string]any{"bearer_token": "x-handler-token-not-real", "consumer_secret": "x-handler-consumer-not-real"}}}
	}
	status, created := postJSON(t, handler, "/v0/connectors", connectorKey, body(map[string]any{"list_id": "1234567890123456789", "backfill_since": time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)}))
	if status != 201 || created["schedule"].(map[string]any)["interval_seconds"].(float64) != 120 {
		t.Fatalf("create: %d %v", status, created)
	}
	if raw, _ := json.Marshal(created); strings.Contains(string(raw), "x-handler") {
		t.Fatalf("secret echoed: %s", raw)
	}
	for _, config := range []map[string]any{
		{"list_id": "1", "backfill_since": time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339)},
		{"list_id": "not-a-list"},
		{"list_id": "1", "recheck_window_seconds": 10},
	} {
		if status, e := postJSON(t, handler, "/v0/connectors", connectorKey, body(config)); status != 422 || e["code"] != "invalid_config" {
			t.Errorf("%v: %d %v", config, status, e)
		}
	}
	id := created["connector_id"].(string)
	in := store.items[id]
	in.Health.Usage = &connectors.Usage{Day: time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC), ItemsRead: 412, PreviousDayItemsRead: 1830}
	in.Health.Diagnostics = json.RawMessage(`{"recheck_window_seconds":86400,"recheck_tracked_posts":3}`)
	store.items[id] = in
	status, read := readJSON(t, handler, "/v0/connectors/"+id, readerKey)
	if status != 200 {
		t.Fatalf("read %d %v", status, read)
	}
	conforms(t, "Connector", read)
	h := read["health"].(map[string]any)
	if u := h["usage"].(map[string]any); u["day"] != "2026-09-28" || u["items_read"].(float64) != 412 || u["previous_day_items_read"].(float64) != 1830 {
		t.Fatalf("usage %v", h)
	}
	if d := h["diagnostics"].(map[string]any); d["recheck_window_seconds"].(float64) != 86400 {
		t.Fatalf("diagnostics %v", h)
	}
}

func TestKeylessDeploymentRefusesCredentialDepositsWith503(t *testing.T) {
	sealer, err := connectors.NewKeylessSealer("cursor-key-0123456789abcdef0123456789")
	if err != nil {
		t.Fatal(err)
	}
	handler := connectorAPISealed(t, &memoryConnectors{items: map[string]connectors.Instance{}}, sealer)
	withSecret := map[string]any{"idempotency_key": "k1", "corpus_id": "corpus_news", "source_namespace": "wire", "kind": "fixture", "config": map[string]any{"script": []any{}},
		"credential": map[string]any{"secret": map[string]any{"token": "fixture-test-secret-keyless"}}}
	status, body := postJSON(t, handler, "/v0/connectors", connectorKey, withSecret)
	if status != 503 || body["code"] != "credentials_unavailable" || body["retryable"] != false {
		t.Fatalf("create with secret: %d %v", status, body)
	}
	conforms(t, "Error", body)
	free := map[string]any{"idempotency_key": "k2", "corpus_id": "corpus_news", "source_namespace": "wire", "kind": "fixture", "config": map[string]any{"script": []any{}}}
	status, created := postJSON(t, handler, "/v0/connectors", connectorKey, free)
	if status != 201 || created["credential"] != nil {
		t.Fatalf("secret-free create: %d %v", status, created)
	}
	payload, _ := json.Marshal(map[string]any{"idempotency_key": "r1", "secret": map[string]any{"token": "fixture-test-secret-keyless"}})
	req := httptest.NewRequest("PUT", "/v0/connectors/"+created["connector_id"].(string)+"/credential", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+connectorKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var rotated map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &rotated)
	if rec.Code != 503 || rotated["code"] != "credentials_unavailable" {
		t.Fatalf("rotation: %d %s", rec.Code, rec.Body.String())
	}
}
