package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/audit"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

type spyAuditStore struct {
	events []audit.Event
}

func (s *spyAuditStore) Record(ctx context.Context, event *audit.Event, work func(context.Context) error) error {
	if err := work(ctx); err != nil {
		return err
	}
	event.ID = int64(len(s.events) + 1)
	s.events = append(s.events, *event)
	return nil
}

func (s *spyAuditStore) List(context.Context, string, audit.Filter) ([]audit.Event, error) {
	return s.events, nil
}

// Owns strict functional/admin route isolation, dedicated admin credentials,
// peer-IP logging, draining and audit continuity when split across listeners.
func TestSplitListenersIsolateAdminSurfaceAndCredentials(t *testing.T) {
	const (
		functionalKey = "functional-key-0123456789abcdef0123456789"
		adminKey      = "dedicated-admin-key-0123456789abcdef01234"
		queueOnlyKey  = "dedicated-queue-key-0123456789abcdef01234"
	)
	functionalKeys := map[string]corpus.Scope{
		// Even a functional key carrying admin actions must never reach /v0/admin/* when split.
		functionalKey: {Organization: "org_a", Actions: []string{"corpora:read", "plugins:admin", "observability:read", "queues:read", "audit:read"}, Corpora: []string{"*"}},
	}
	adminKeys := map[string]corpus.Scope{
		adminKey:     {Organization: "org_a", Actions: []string{"plugins:admin", "observability:read", "queues:read", "audit:read"}, Corpora: []string{"*"}},
		queueOnlyKey: {Organization: "org_a", Actions: []string{"queues:read"}, Corpora: []string{"*"}},
	}

	if _, _, err := httpapi.NewSplit(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, functionalKeys, nil, catalogCursorKey); err == nil {
		t.Fatal("NewSplit accepted empty admin keys")
	}
	if _, _, err := httpapi.NewSplit(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, functionalKeys, map[string]corpus.Scope{
		functionalKey: {Organization: "org_a", Actions: []string{"queues:read"}, Corpora: []string{"*"}},
	}, catalogCursorKey); err == nil {
		t.Fatal("NewSplit accepted overlapping functional and admin key")
	}

	var logs bytes.Buffer
	previous := slog.Default()
	logger, err := logging.New(&logs, logging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })

	draining := false
	queueCalls := 0
	audits := &spyAuditStore{}
	metrics := telemetry.NewLoadMetrics()
	publicHandler, adminHandler, err := httpapi.NewSplit(
		knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{},
		functionalKeys, adminKeys, catalogCursorKey,
		httpapi.WithActivity(content.Activities{Store: memoryActivity{}}),
		httpapi.WithQueues(queueRows{&queueCalls, false}),
		httpapi.WithAudit(audits),
		httpapi.WithPlugins(registry.Service{}),
		httpapi.WithLoadMetrics(metrics),
		httpapi.WithTrustedPushProxies([]string{"192.0.2.0/24"}),
		httpapi.WithDraining(func() bool { return draining }),
	)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Functional listener refuses every /v0/admin/* path before auth and audit.
	for _, path := range []string{
		"/v0/admin",
		"/v0/admin/plugins",
		"/v0/admin/plugins/",
		"/v0/admin/queues",
		"/v0/admin/documents",
		"/v0/admin/audit",
		"/v0/admin/subscriptions/evaluation-backlog",
	} {
		for _, token := range []string{"", functionalKey, adminKey} {
			logs.Reset()
			req := httptest.NewRequest("GET", path, nil)
			req.RemoteAddr = "192.0.2.7:4321"
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			rec := httptest.NewRecorder()
			publicHandler.ServeHTTP(rec, req)
			if rec.Code != 404 {
				t.Fatalf("public GET %s (token=%t): want 404, got %d %s", path, token != "", rec.Code, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["code"] != "not_found" {
				t.Fatalf("public GET %s body: %s", path, rec.Body.String())
			}
			var event map[string]any
			if err := json.NewDecoder(&logs).Decode(&event); err != nil {
				t.Fatal(err)
			}
			if event["route"] != "unmatched" || event["status"] != float64(404) || event["error_code"] != "not_found" || event["api_key_id"] != "" {
				t.Fatalf("public refused admin access log for %s: %+v", path, event)
			}
		}
	}
	if queueCalls != 0 || len(audits.events) != 0 {
		t.Fatalf("refused public admin requests touched backend: queueCalls=%d audits=%d", queueCalls, len(audits.events))
	}

	// 2. Functional listener still serves functional routes with functional keys and rejects admin keys.
	for _, tc := range []struct {
		name, token string
		want        int
	}{
		{"functional key", functionalKey, 200},
		{"admin key rejected on functional port", adminKey, 401},
	} {
		req := httptest.NewRequest("GET", "/v0/version", nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		rec := httptest.NewRecorder()
		publicHandler.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("public GET /v0/version (%s): want %d got %d", tc.name, tc.want, rec.Code)
		}
	}

	// 3. Admin listener serves /v0/admin/* only with dedicated admin keys and enforces action scopes.
	for _, tc := range []struct {
		name, method, path, token string
		want                      int
	}{
		{"unauthenticated admin rejected", "GET", "/v0/admin/queues", "", 401},
		{"functional key rejected on admin port", "GET", "/v0/admin/queues", functionalKey, 401},
		{"dedicated queue key allowed on queues", "GET", "/v0/admin/queues", queueOnlyKey, 200},
		{"dedicated queue key forbidden on documents", "GET", "/v0/admin/documents", queueOnlyKey, 403},
		{"dedicated admin key allowed on documents", "GET", "/v0/admin/documents", adminKey, 200},
		{"dedicated admin key allowed on audit", "GET", "/v0/admin/audit", adminKey, 200},
		{"non-admin functional route refused on admin port", "GET", "/v0/corpora", adminKey, 404},
		{"version route refused on admin port", "GET", "/v0/version", adminKey, 404},
		{"webhook route refused on admin port", "POST", "/v0/connector-webhooks/c1", "", 404},
		{"connector api route refused on admin port", "POST", "/v0/connectors/c1/api/events", "", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.RemoteAddr = "192.0.2.9:5555"
			req.Header.Set("X-Forwarded-For", "198.51.100.77")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			adminHandler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("admin %s %s: want %d got %d %s", tc.method, tc.path, tc.want, rec.Code, rec.Body.String())
			}
			var event map[string]any
			if err := json.NewDecoder(&logs).Decode(&event); err != nil {
				t.Fatal(err)
			}
			// Admin listener must ignore X-Forwarded-For even when push proxies are configured.
			if event["client_ip"] != "192.0.2.9" {
				t.Fatalf("admin listener trusted forwarded IP: %+v", event)
			}
			if tc.token == adminKey && tc.want == 200 && (event["api_key_id"] == nil || event["api_key_id"] == "") {
				t.Fatalf("admin access log missing key id: %+v", event)
			}
		})
	}

	// 4. Sensitive admin commands on the admin listener remain audited.
	req := httptest.NewRequest("POST", "/v0/admin/backfills", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	adminHandler.ServeHTTP(rec, req)
	if len(audits.events) != 1 || audits.events[0].Action != "operation.backfill" {
		t.Fatalf("expected audited admin command, got %+v", audits.events)
	}

	// 5. Draining stops new admin requests just like functional requests.
	draining = true
	req = httptest.NewRequest("GET", "/v0/admin/queues", nil)
	req.Header.Set("Authorization", "Bearer "+queueOnlyKey)
	rec = httptest.NewRecorder()
	adminHandler.ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatalf("draining admin request: want 503 got %d", rec.Code)
	}
}

// Owns the shared probe+admin listener contract: probes stay unauthenticated
// while /v0/admin/* enforces dedicated admin authentication and functional routes 404.
func TestCombineProbeAndAdminSeparatesProbesFromAuthenticatedAdmin(t *testing.T) {
	const (
		functionalKey = "functional-key-0123456789abcdef0123456789"
		adminKey      = "dedicated-admin-key-0123456789abcdef01234"
	)
	calls := 0
	_, adminHandler, err := httpapi.NewSplit(
		knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{},
		map[string]corpus.Scope{functionalKey: {Organization: "org_a", Actions: []string{"queues:read"}, Corpora: []string{"*"}}},
		map[string]corpus.Scope{adminKey: {Organization: "org_a", Actions: []string{"queues:read"}, Corpora: []string{"*"}}},
		catalogCursorKey,
		httpapi.WithQueues(queueRows{&calls, false}),
	)
	if err != nil {
		t.Fatal(err)
	}
	probes := http.NewServeMux()
	probes.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	probes.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	combined := httpapi.CombineProbeAndAdmin(httpapi.AccessLog(probes), adminHandler)

	for _, tc := range []struct {
		path, token string
		want        int
	}{
		{"/healthz", "", 204},
		{"/readyz", "", 204},
		{"/v0/admin/queues", "", 401},
		{"/v0/admin/queues", functionalKey, 401},
		{"/v0/admin/queues", adminKey, 200},
		{"/v0/corpora", adminKey, 404},
	} {
		req := httptest.NewRequest("GET", tc.path, nil)
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		rec := httptest.NewRecorder()
		combined.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			body, _ := io.ReadAll(rec.Body)
			t.Fatalf("combined GET %s (token=%t): want %d got %d %s", tc.path, tc.token != "", tc.want, rec.Code, string(body))
		}
	}
}
