package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/backfill"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/quarantine"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/testutil/apicontract"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

const internalErrorMarker = "internal-dependency-detail-must-stay-private"

func assertPublicMessage(t *testing.T, rec *httptest.ResponseRecorder, code string, messages ...string) {
	t.Helper()
	if strings.Contains(rec.Body.String(), internalErrorMarker) {
		t.Errorf("internal error marker leaked in response: %s", rec.Body.String())
	}
	var body struct{ Code, Message string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode failure body %q: %v", rec.Body.String(), err)
	}
	message := strings.ReplaceAll(code, "_", " ")
	if len(messages) > 0 && messages[0] != "" {
		message = messages[0]
	}
	if body.Code != code || body.Message != message {
		t.Errorf("response %s, want code %q message %q", rec.Body.String(), code, message)
	}
}

type refusedBackfillPlan struct{ err error }

func (p refusedBackfillPlan) ActiveIngestion(context.Context) (backfill.Ingestion, error) {
	return backfill.Ingestion{}, p.err
}

type refusedQuarantine struct {
	quarantine.Store
	err error
}

func (s refusedQuarantine) Quarantined(context.Context, string, []string, quarantine.Filter, string, int) ([]quarantine.Entry, error) {
	return nil, s.err
}

func (s refusedQuarantine) ReprocessSize(context.Context, string, quarantine.Filter) (operations.ReprocessEstimate, error) {
	return operations.ReprocessEstimate{}, s.err
}

// These routes map errors inline, so exercise their real HTTP handlers with
// dependency failures rather than introducing mapping helpers just for tests.
func TestBackfillFailureMessagesIgnoreDetail(t *testing.T) {
	for _, route := range []struct {
		path, body, code, message string
		status                    int
		sentinel                  error
	}{
		{backfillsPath, `{"idempotency_key":"k","corpus_id":"corpus_a","dry_run":true}`, "invalid_backfill", "", 422, backfill.ErrInvalid},
		{backfillsPath, `{"idempotency_key":"k","corpus_id":"corpus_a","dry_run":true}`, "storage_unavailable", "", 503, registry.ErrNoPlan},
	} {
		for style, err := range detailed(route.sentinel) {
			t.Run(route.code+"/"+style, func(t *testing.T) {
				checkRefusedRoute(t, "POST", route.path, route.body, route.status, route.code,
					WithBackfills(backfill.Service{Plans: refusedBackfillPlan{err}}, backfill.Promotions{}), route.message)
			})
		}
	}
}

func TestQuarantineFailureMessagesIgnoreDetail(t *testing.T) {
	for _, route := range []struct {
		method, path, body, code string
	}{
		{"GET", quarantinePath, "", "invalid_query"},
		{"POST", reprocessPath, `{"idempotency_key":"k","corpus_id":"corpus_a","dry_run":true}`, "invalid_reprocess"},
	} {
		for style, err := range detailed(quarantine.ErrInvalid) {
			t.Run(route.code+"/"+style, func(t *testing.T) {
				checkRefusedRoute(t, route.method, route.path, route.body, 422, route.code,
					WithQuarantine(quarantine.Service{Store: refusedQuarantine{err: err}}))
			})
		}
	}
}

func checkRefusedRoute(t *testing.T, method, path, body string, status int, code string, option Option, message ...string) {
	t.Helper()
	const token = "operator-token-0123456789abcdef0123456789"
	keys := map[string]corpus.Scope{token: {Organization: "org_a", Corpora: []string{"*"}, Actions: []string{operations.BackfillPermission}}}
	handler, err := New(nil, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, []byte("cursor-key-0123456789abcdef0123456789"), option)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	apicontract.Handler(t, handler).ServeHTTP(rec, req)
	if rec.Code != status {
		t.Fatalf("%s %s: %d %s, want status %d", method, path, rec.Code, rec.Body.String(), status)
	}
	assertPublicMessage(t, rec, code, message...)
}

func TestPluginFailurePreservesTypedDiagnostics(t *testing.T) {
	// Use the conflict producer, so dropping its typed plugin identity breaks
	// the operator response even if the transport still formats injected data.
	pins := []*plugins.Pin{
		{Manifest: plugins.Manifest{ID: "example.first", Version: "1.2.3", Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{}}}},
		{Manifest: plugins.Manifest{ID: "example.first", Version: "2.0.0", Contributions: plugins.Contributions{Retrieval: &plugins.Retrieval{}}}},
	}
	_, conflict := plugins.NewPinSet(pins)
	var pinError *plugins.PinError
	if !errors.As(conflict, &pinError) {
		t.Fatalf("duplicate plugin ID: %v, want a typed conflict", conflict)
	}
	for _, tc := range []struct {
		code, message string
		status        int
		err           error
	}{
		{"invalid_plugin", "invalid plugin; schema_violation /manifest/dimensions", 422,
			&registry.IssueError{Kind: registry.ErrInvalid, Issues: []plugins.Issue{{Code: plugins.CodeSchema, Path: "/manifest/dimensions", Message: internalErrorMarker}}}},
		{"plugin_conflict", "plugin conflict; plugin_conflict /plugins/1/manifest plugin=example.first@1.2.3", 409,
			&registry.IssueError{Kind: registry.ErrConflict, Issues: pinError.Issues}},
		{"plugin_conflict", "plugin conflict; space_owner_conflict vector space example.space@1", 409,
			&content.SpaceError{Kind: content.ErrSpaceOwner, Space: "example.space@1", Detail: internalErrorMarker}},
		{"plugin_conflict", "plugin conflict; owner=example.source space=example.source.small@1 missing_documents=3 missing_generations=0; backfill the returning owner with POST /v0/admin/backfills, then retry", 409,
			&registry.CoverageError{Gaps: []registry.CoverageGap{{Owner: "example.source", Space: "example.source.small@1", MissingVersions: 3}}}},
		{"plugin_unreachable", "plugin unreachable; plugin_unreachable /registrations/old plugin=example.source@1.0.0 cause=network_error; restore the exact build at its endpoint, or activate the current registration of the previous owner listed by GET /v0/admin/plugins", 409,
			&registry.IssueError{Kind: registry.ErrUnreachable, Issues: []plugins.Issue{{Code: registry.CodeUnreachable, Path: "/registrations/old", PluginID: "example.source", PluginVersion: "1.0.0", Cause: plugins.CauseNetwork, Message: internalErrorMarker}}}},
	} {
		for style, err := range detailed(tc.err) {
			t.Run(tc.code+"/"+style, func(t *testing.T) {
				rec := httptest.NewRecorder()
				writeError(rec, err, publicerr.StorageUnavailable)
				if rec.Code != tc.status {
					t.Fatalf("status %d, want %d", rec.Code, tc.status)
				}
				assertPublicMessage(t, rec, tc.code, tc.message)
			})
		}
	}
	// Both the number and size of typed details can grow independently. The
	// response must stay bounded, valid JSON and free of internal issue text.
	for _, tc := range []struct {
		name, id, version string
		count             int
		kind              error
		coverage          bool
	}{
		{"many long Unicode values", strings.Repeat("é", 4000), strings.Repeat("1", 4000), 100, registry.ErrConflict, false},
		{"invalid UTF-8 expands during JSON encoding", strings.Repeat("\xff", 128), strings.Repeat("\xff", 64), 5, registry.ErrConflict, false},
		{"unreachable guidance stays bounded", strings.Repeat("é", 4000), strings.Repeat("1", 4000), 100, registry.ErrUnreachable, false},
		{"coverage hint stays bounded", strings.Repeat("é", 4000), "unused", 100, registry.ErrConflict, true},
		{"coverage invalid UTF-8", strings.Repeat("\xff", 4000), "unused", 100, registry.ErrConflict, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := make([]plugins.Issue, tc.count)
			for i := range issues {
				issues[i] = plugins.Issue{Code: plugins.CodeKindConflict, Path: "/manifest", PluginID: tc.id, PluginVersion: tc.version, Cause: plugins.IssueCause(internalErrorMarker), Message: internalErrorMarker}
			}
			rec := httptest.NewRecorder()
			var refusal error = &registry.IssueError{Kind: tc.kind, Issues: issues}
			want := "kind_conflict /manifest"
			if tc.coverage {
				gaps := make([]registry.CoverageGap, tc.count)
				for i := range gaps {
					gaps[i] = registry.CoverageGap{Owner: tc.id, Space: tc.id, MissingVersions: 3}
				}
				refusal = &registry.CoverageError{Gaps: gaps}
				want = "POST /v0/admin/backfills, then retry"
			}
			writeError(rec, refusal, publicerr.StorageUnavailable)
			var body struct{ Message string }
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Message) > 2048 || !strings.Contains(body.Message, want) || strings.Contains(rec.Body.String(), internalErrorMarker) {
				t.Fatalf("bounded public diagnostics: %d bytes, %s", len(body.Message), rec.Body.String())
			}
		})
	}
}

type rollbackDiagnosticStore struct {
	registry.Store
	active, target registry.Plan
	members        map[string]registry.Registration
}

func (s rollbackDiagnosticStore) Rollback(_ context.Context, _ registry.RollbackRequest, compute func(registry.Plan, registry.Plan, map[string]registry.Registration) (registry.Activation, error)) (registry.Plan, error) {
	_, err := compute(s.active, s.target, s.members)
	return registry.Plan{}, err
}

func TestRollbackFailureReportsTypedCause(t *testing.T) {
	manifest, err := os.ReadFile("../../../sdks/go/examples/static-source/quivr-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	old := registry.Registration{ID: "old", PluginID: "example.static-source", Version: "0.1.0", State: registry.StateDraining, Manifest: manifest, Endpoint: "http://127.0.0.1:9900"}
	current := old
	current.ID, current.Version, current.State = "current", "0.2.0", registry.StateActive
	current.Manifest = []byte(strings.Replace(string(manifest), "version: 0.1.0", "version: 0.2.0", 1))
	plan := func(r registry.Registration) registry.Plan {
		return registry.Plan{Roles: []registry.Assignment{{Role: "connector:static", RegistrationID: r.ID, PluginID: r.PluginID, Version: r.Version}}}
	}
	store := rollbackDiagnosticStore{active: plan(current), target: plan(old), members: map[string]registry.Registration{old.ID: old, current.ID: current}}
	for _, tc := range []struct {
		cause string
		err   error
	}{
		{"network_error", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New(internalErrorMarker)}},
		{"deadline_exceeded", fmt.Errorf("%s: %w", internalErrorMarker, context.DeadlineExceeded)},
		{"deadline_exceeded", &net.DNSError{Err: internalErrorMarker, IsTimeout: true}},
		{"canceled", fmt.Errorf("%s: %w", internalErrorMarker, context.Canceled)},
		{"discovery_invalid", &registry.IssueError{Kind: registry.ErrUnreachable, Issues: []plugins.Issue{{Code: "discovery_mismatch", Message: internalErrorMarker}}}},
		{"unavailable", errors.New(internalErrorMarker)},
	} {
		t.Run(tc.cause, func(t *testing.T) {
			service := registry.Service{Store: store, Reach: func(context.Context, registry.Registration) error { return tc.err }}
			_, err := service.Rollback(context.Background(), corpus.Scope{Actions: []string{registry.Action}}, registry.RollbackRequest{})
			if !errors.Is(err, registry.ErrUnreachable) {
				t.Fatalf("rollback: %v, want unreachable", err)
			}
			rec := httptest.NewRecorder()
			writeError(rec, fmt.Errorf("%s: %w", internalErrorMarker, err), publicerr.StorageUnavailable)
			assertPublicMessage(t, rec, "plugin_unreachable", "plugin unreachable; plugin_unreachable /registrations/old plugin=example.static-source@0.1.0 cause="+tc.cause+"; restore the exact build at its endpoint, or activate the current registration of the previous owner listed by GET /v0/admin/plugins")
		})
	}
}
