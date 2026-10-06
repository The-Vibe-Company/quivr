package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost/fakeplugin"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
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

// memoryConnectors preserves the creation and scope rules of ConnectorStore.
type memoryConnectors struct {
	items     map[string]connectors.Instance
	creations map[[2]string]connectors.NewInstance
}

func (m *memoryConnectors) CreateConnector(_ context.Context, n connectors.NewInstance) (connectors.Instance, error) {
	key := [2]string{n.Organization, n.RequestKey}
	if existing, ok := m.creations[key]; ok {
		if !bytes.Equal(existing.RequestDigest, n.RequestDigest) {
			return connectors.Instance{}, connectors.ErrConflict
		}
		return m.items[existing.ID], nil
	}
	for _, existing := range m.items {
		if existing.Organization == n.Organization && existing.CorpusID == n.CorpusID && existing.Namespace == n.Namespace && existing.Enabled {
			return connectors.Instance{}, connectors.ErrNamespaceInUse
		}
	}
	if m.creations == nil {
		m.creations = map[[2]string]connectors.NewInstance{}
	}
	m.creations[key] = n
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
		if in.Organization == s.Organization && s.Contains(in.CorpusID) && (corpusID == "" || in.CorpusID == corpusID) && in.ID > after {
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
	if err != nil {
		return in, err
	}
	in.Enabled = false
	m.items[id] = in
	return in, err
}
func (m *memoryConnectors) ReplaceCredential(ctx context.Context, org, id string, d connectors.CredentialDeposit) (connectors.Instance, error) {
	return m.ReadConnector(ctx, org, id)
}
func (m *memoryConnectors) ChangeSchedule(ctx context.Context, org, id string, interval time.Duration) (connectors.Instance, error) {
	in, err := m.ReadConnector(ctx, org, id)
	if err != nil {
		return in, err
	}
	in.Interval = interval
	m.items[id] = in
	return in, nil
}
func (m *memoryConnectors) RequestRun(ctx context.Context, org, id string, _ time.Duration) (time.Time, error) {
	in, err := m.ReadConnector(ctx, org, id)
	if err == nil && !in.Enabled {
		err = connectors.ErrDisabled
	}
	return time.Date(2026, 9, 28, 10, 0, 30, 0, time.UTC), err
}

const (
	connectorKey = "connector-key-0123456789abcdef0123456"
	// otherCorpusKey may write connectors, but only in another Corpus.
	otherCorpusKey = "other-corpus-key-0123456789abcdef0123"
	contentKey     = "content-only-key-0123456789abcdef01234"
)

// bearerSource is a kind that needs a credential, as plugin kinds such as
// x_list do.
type bearerSource struct{ fakeplugin.FixtureConnector }

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
	registry, err := connectors.NewRegistry(fakeplugin.FixtureConnector{}, bearerSource{})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]corpus.Scope{
		connectorKey:   {Organization: "org_a", Actions: []string{"connectors:read", "connectors:write"}, Corpora: []string{"corpus_news"}},
		otherCorpusKey: {Organization: "org_a", Actions: []string{"connectors:read", "connectors:write"}, Corpora: []string{"corpus_other"}},
		readerKey:      {Organization: "org_a", Actions: []string{"connectors:read"}, Corpora: []string{"*"}},
		contentKey:     {Organization: "org_a", Actions: []string{"content:read"}, Corpora: []string{"*"}},
	}
	handler, err := httpapi.New(nil, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"),
		httpapi.WithConnectors(connectors.Service{Store: store, Registry: registry, Sealer: sealer}))
	if err != nil {
		t.Fatal(err)
	}
	return checkedAPI(t, handler)
}

func TestConnectorCreationValidatesAndNeverEchoesTheSecret(t *testing.T) {
	handler := connectorAPI(t)
	valid := func() map[string]any {
		return map[string]any{"idempotency_key": "c1", "corpus_id": "corpus_news", "source_namespace": "wire", "kind": "fixture", "config": map[string]any{"script": []any{}},
			"push_policy": map[string]any{"rate_per_second": 2.5, "burst": 10, "allowed_cidrs": []any{"192.0.2.0/24"}},
			"credential":  map[string]any{"secret": map[string]any{"token": "fixture-test-secret-handler"}, "expires_at": "2027-01-01T00:00:00Z"}}
	}
	status, body := postJSON(t, handler, "/v0/connectors", connectorKey, valid())
	if status != 201 {
		t.Fatalf("create: %d %v", status, body)
	}
	conforms(t, "Connector", body)
	policy := body["push_policy"].(map[string]any)
	if policy["rate_per_second"] != 2.5 || policy["burst"] != float64(10) || policy["allowed_cidrs"].([]any)[0] != "192.0.2.0/24" {
		t.Fatalf("push policy lost: %v", policy)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "fixture-test-secret") {
		t.Fatalf("secret echoed: %s", raw)
	}
	if body["schedule"].(map[string]any)["interval_seconds"].(float64) != 300 || body["credential"].(map[string]any)["version"].(float64) != 1 {
		t.Fatalf("defaults %v", body)
	}
	id := body["connector_id"].(string)
	if status, replay := postJSON(t, handler, "/v0/connectors", connectorKey, valid()); status != 201 || replay["connector_id"] != id {
		t.Fatalf("creation replay: %d %v", status, replay)
	}
	changed := valid()
	changed["source_namespace"] = "different"
	if status, conflict := postJSON(t, handler, "/v0/connectors", connectorKey, changed); status != 409 || conflict["code"] != "idempotency_conflict" {
		t.Fatalf("creation conflict: %d %v", status, conflict)
	}
	for _, key := range []string{connectorKey, otherCorpusKey} {
		status, page := readJSON(t, handler, "/v0/connectors", key)
		if status != 200 {
			t.Fatalf("list: %d %v", status, page)
		}
		conforms(t, "ConnectorPage", page)
		want := 1
		if key == otherCorpusKey {
			want = 0
		}
		if len(page["items"].([]any)) != want {
			t.Fatalf("scope %q listed %v", key, page)
		}
	}
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
		// A kind no provider serves; a plugin could provide it, so the contract does not enumerate kinds.
		{"unknown kind", connectorKey, func(b map[string]any) { b["kind"] = "ftp" }, 422, "unsupported_connector_kind"},
		{"malformed kind", connectorKey, func(b map[string]any) { b["kind"] = "FTP feed" }, 422, "invalid_schema"},
		{"config fails the kind schema", connectorKey, func(b map[string]any) { b["config"] = map[string]any{"script": "x"} }, 422, "invalid_config"},
		{"secret fails the kind schema", connectorKey, func(b map[string]any) { b["credential"] = map[string]any{"secret": map[string]any{"password": "x"}} }, 422, "invalid_credential"},
		{"namespace already in use", connectorKey, func(b map[string]any) { b["idempotency_key"] = "c2" }, 409, "source_namespace_in_use"},
		{"invalid command key", connectorKey, func(b map[string]any) { b["idempotency_key"] = "c\x00" }, 422, "invalid_input"},
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
	if status, body := postJSON(t, handler, "/v0/connectors/"+id+"/disable", connectorKey, map[string]any{"idempotency_key": "d1"}); status != 200 || body["connector_id"] != id || body["enabled"] != false {
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

// Health usage and diagnostics are stored by acquisition; the transport renders
// them, including the previous day's reads.
func TestConnectorHealthRendersUsageAndDiagnostics(t *testing.T) {
	store := &memoryConnectors{items: map[string]connectors.Instance{}}
	handler := connectorAPIWith(t, store)
	status, created := postJSON(t, handler, "/v0/connectors", connectorKey, map[string]any{"idempotency_key": "u1", "corpus_id": "corpus_news", "source_namespace": "wire", "kind": "fixture", "config": map[string]any{"script": []any{}}})
	if status != 201 {
		t.Fatalf("create: %d %v", status, created)
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

func putJSON(t *testing.T, handler http.Handler, path, key string, body any) (int, map[string]any) {
	t.Helper()
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", path, bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestConnectorKindsPublishSchemasAndCredentialDepositAvailability(t *testing.T) {
	status, body := readJSON(t, connectorAPI(t), "/v0/connector-kinds", readerKey)
	if status != 200 {
		t.Fatalf("kinds: %d %v", status, body)
	}
	conforms(t, "ConnectorKindCatalog", body)
	if body["credential_deposits"] != "available" || body["min_interval_seconds"].(float64) != 30 {
		t.Fatalf("catalog %v", body)
	}
	items := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("enabled kinds only: %v", items)
	}
	bearer, fixture := items[0].(map[string]any), items[1].(map[string]any)
	if fixture["kind"] != "fixture" || fixture["credential"] != "optional" || fixture["default_interval_seconds"].(float64) != 300 || fixture["title"] != "Test fixture" {
		t.Fatalf("fixture %v", fixture)
	}
	if bearer["kind"] != "bearer_source" || bearer["credential"] != "required" || bearer["credential_schema"].(map[string]any)["properties"].(map[string]any)["bearer_token"].(map[string]any)["writeOnly"] != true {
		t.Fatalf("bearer_source %v", bearer)
	}
	if fixture["config_schema"].(map[string]any)["required"].([]any)[0] != "script" {
		t.Fatalf("config schema %v", fixture["config_schema"])
	}
	keyless, _ := connectors.NewKeylessSealer("cursor-key-0123456789abcdef0123456789")
	if _, body := readJSON(t, connectorAPISealed(t, &memoryConnectors{items: map[string]connectors.Instance{}}, keyless), "/v0/connector-kinds", readerKey); body["credential_deposits"] != "unavailable" {
		t.Fatalf("keyless catalog %v", body)
	}
	if status, body := readJSON(t, connectorAPI(t), "/v0/connector-kinds", contentKey); status != 403 || body["code"] != "forbidden" {
		t.Fatalf("without connectors:read: %d %v", status, body)
	}
	if status, _ := postJSON(t, connectorAPI(t), "/v0/connector-kinds", connectorKey, map[string]any{}); status != 405 {
		t.Fatalf("POST kinds: %d", status)
	}
}

func TestConnectorScheduleChangesAreValidatedAndAuthorized(t *testing.T) {
	handler := connectorAPI(t)
	_, created := postJSON(t, handler, "/v0/connectors", connectorKey, map[string]any{"idempotency_key": "s1", "corpus_id": "corpus_news", "source_namespace": "wire", "kind": "fixture", "config": map[string]any{"script": []any{}}})
	path := "/v0/connectors/" + created["connector_id"].(string) + "/schedule"
	status, body := putJSON(t, handler, path, connectorKey, map[string]any{"interval_seconds": 120})
	if status != 200 || body["schedule"].(map[string]any)["interval_seconds"].(float64) != 120 {
		t.Fatalf("change: %d %v", status, body)
	}
	conforms(t, "Connector", body)
	for _, c := range []struct {
		name   string
		path   string
		key    string
		body   any
		status int
		code   string
		field  string
	}{
		{"below the floor", path, connectorKey, map[string]any{"interval_seconds": 29}, 422, "invalid_interval", "/interval_seconds"},
		{"above a day", path, connectorKey, map[string]any{"interval_seconds": 86401}, 422, "invalid_interval", "/interval_seconds"},
		{"zero", path, connectorKey, map[string]any{"interval_seconds": 0}, 422, "invalid_interval", "/interval_seconds"},
		{"negative", path, connectorKey, map[string]any{"interval_seconds": -1}, 422, "invalid_interval", "/interval_seconds"},
		{"wraps as a Duration", path, connectorKey, map[string]any{"interval_seconds": int64(1)<<55 + 64}, 422, "invalid_interval", "/interval_seconds"},
		{"not an integer", path, connectorKey, map[string]any{"interval_seconds": "60"}, 422, "invalid_schema", "/interval_seconds"},
		{"unexpected member", path, connectorKey, map[string]any{"interval_seconds": 60, "every": 1}, 422, "invalid_schema", "/every"},
		{"read-only key", path, readerKey, map[string]any{"interval_seconds": 60}, 403, "forbidden", ""},
		{"writer of another Corpus", path, otherCorpusKey, map[string]any{"interval_seconds": 60}, 404, "not_found", ""},
		{"unknown instance", "/v0/connectors/connector_missing/schedule", connectorKey, map[string]any{"interval_seconds": 60}, 404, "not_found", ""},
	} {
		status, body := putJSON(t, handler, c.path, c.key, c.body)
		field, _ := body["field"].(string)
		if status != c.status || body["code"] != c.code || field != c.field {
			t.Errorf("%s: %d %v", c.name, status, body)
		}
	}
	if status, _ := postJSON(t, handler, path, connectorKey, map[string]any{"interval_seconds": 60}); status != 405 {
		t.Fatalf("POST schedule: %d", status)
	}
}

func TestConnectorRunRequestsAreAuthorizedAndRefusedOnceDisabled(t *testing.T) {
	handler := connectorAPI(t)
	_, created := postJSON(t, handler, "/v0/connectors", connectorKey, map[string]any{"idempotency_key": "r1", "corpus_id": "corpus_news", "source_namespace": "wire", "kind": "fixture", "config": map[string]any{"script": []any{}}})
	id := created["connector_id"].(string)
	path := "/v0/connectors/" + id + "/runs"
	status, body := postJSON(t, handler, path, connectorKey, map[string]any{"idempotency_key": "now"})
	if status != 202 || body["connector_id"] != id || body["run_at"] != "2026-09-28T10:00:30Z" {
		t.Fatalf("request: %d %v", status, body)
	}
	conforms(t, "ConnectorRunRequest", body)
	for _, c := range []struct {
		name   string
		path   string
		key    string
		body   any
		status int
		code   string
	}{
		{"no idempotency key", path, connectorKey, map[string]any{}, 422, "invalid_schema"},
		{"read-only key", path, readerKey, map[string]any{"idempotency_key": "now"}, 403, "forbidden"},
		{"writer of another Corpus", path, otherCorpusKey, map[string]any{"idempotency_key": "now"}, 404, "not_found"},
		{"unknown instance", "/v0/connectors/connector_missing/runs", connectorKey, map[string]any{"idempotency_key": "now"}, 404, "not_found"},
	} {
		if status, body := postJSON(t, handler, c.path, c.key, c.body); status != c.status || body["code"] != c.code {
			t.Errorf("%s: %d %v", c.name, status, body)
		}
	}
	postJSON(t, handler, "/v0/connectors/"+id+"/disable", connectorKey, map[string]any{"idempotency_key": "off"})
	if status, body := postJSON(t, handler, path, connectorKey, map[string]any{"idempotency_key": "now"}); status != 409 || body["code"] != "connector_disabled" {
		t.Fatalf("disabled: %d %v", status, body)
	}
}

func TestConnectorValidationErrorsPointAtTheOffendingField(t *testing.T) {
	handler := connectorAPI(t)
	valid := func() map[string]any {
		return map[string]any{"idempotency_key": "f1", "corpus_id": "corpus_news", "source_namespace": "wire", "kind": "fixture", "config": map[string]any{"script": []any{}}}
	}
	for _, c := range []struct {
		name  string
		edit  func(map[string]any)
		code  string
		field string
	}{
		{"config member type", func(b map[string]any) { b["config"] = map[string]any{"script": "x"} }, "invalid_config", "/config/script"},
		{"missing config member", func(b map[string]any) { b["config"] = map[string]any{} }, "invalid_config", "/config/script"},
		{"secret member", func(b map[string]any) { b["credential"] = map[string]any{"secret": map[string]any{"token": ""}} }, "invalid_credential", "/credential/secret/token"},
		{"interval below the floor", func(b map[string]any) { b["schedule"] = map[string]any{"interval_seconds": 29} }, "invalid_interval", "/schedule/interval_seconds"},
		{"malformed kind", func(b map[string]any) { b["kind"] = "FTP feed" }, "invalid_schema", "/kind"},
	} {
		b := valid()
		c.edit(b)
		status, body := postJSON(t, handler, "/v0/connectors", connectorKey, b)
		if status != 422 || body["code"] != c.code || body["field"] != c.field {
			t.Errorf("%s: %d %v", c.name, status, body)
		}
		conforms(t, "Error", body)
	}
	_, created := postJSON(t, handler, "/v0/connectors", connectorKey, valid())
	status, body := putJSON(t, handler, "/v0/connectors/"+created["connector_id"].(string)+"/credential", connectorKey, map[string]any{"idempotency_key": "r", "secret": map[string]any{"token": ""}})
	if status != 422 || body["code"] != "invalid_credential" || body["field"] != "/secret/token" {
		t.Fatalf("rotation: %d %v", status, body)
	}
}

func (c bearerSource) Descriptor() connectors.Descriptor {
	d := c.FixtureConnector.Descriptor()
	d.Kind = "bearer_source"
	d.CredentialRequired = true
	d.CredentialSchema = []byte(`{"type":"object","required":["bearer_token"],"properties":{"bearer_token":{"type":"string","writeOnly":true}}}`)
	return d
}
