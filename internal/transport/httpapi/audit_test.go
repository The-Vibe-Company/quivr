package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/audit"
	"github.com/The-Vibe-Company/quivr/internal/backfill"
	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost/fakeplugin"
	"github.com/The-Vibe-Company/quivr/internal/quarantine"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

type auditSink struct {
	events []audit.Event
	fail   bool
	filter audit.Filter
	org    string
	page   []audit.Event
}

func (s *auditSink) Record(ctx context.Context, e *audit.Event, command func(context.Context) error) error {
	if err := command(ctx); err != nil {
		if errors.Is(err, audit.ErrReadOnly) {
			return nil
		}
		return err
	}
	if s.fail {
		return errors.New("audit unavailable")
	}
	e.ID = int64(len(s.events) + 1)
	e.Time = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	s.events = append(s.events, *e)
	return nil
}
func (s *auditSink) List(_ context.Context, org string, f audit.Filter) ([]audit.Event, error) {
	s.org = org
	s.filter = f
	return s.page, nil
}

type auditTokens struct{ connectors.TokenStore }

func (auditTokens) CreateInstanceToken(_ context.Context, _, _ string, d connectors.TokenDeposit) (connectors.TokenInfo, error) {
	return connectors.TokenInfo{ID: d.ID, Prefix: d.Prefix, CreatedAt: time.Now()}, nil
}

type auditCorpora struct{ knownCorpora }

type auditWithdrawals struct{ content.SubmissionStore }

func (auditWithdrawals) Withdraw(_ context.Context, _ corpus.Scope, w content.Withdrawal) (content.Receipt, error) {
	return content.Receipt{ID: "receipt_withdraw", State: "accepted", RecordID: "record_withdrawn", VersionID: "version_private", Source: w.Source}, nil
}

func (auditCorpora) Create(context.Context, string, corpus.CreateInput) (corpus.Corpus, bool, error) {
	return corpus.Corpus{ID: "corpus_created", Name: "Audit", Retrieval: map[string]any{}}, false, nil
}

// Owns transport enrollment and privacy. The sink observes produced entries;
// atomicity and SQL filtering are independently owned by the PostgreSQL test.
func TestAuditSensitiveRoutesOutcomesAndPrivacy(t *testing.T) {
	sink := &auditSink{}
	var logs bytes.Buffer
	previous := slog.Default()
	logger, err := logging.New(&logs, logging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	slog.SetDefault(logger)
	defer slog.SetDefault(previous)
	token := "fixture-audit-bearer-secret"
	keys := map[string]corpus.Scope{token: {Organization: "org_a", Actions: []string{"corpora:write", "corpora:read"}, Corpora: []string{"*"}}}
	estimateStore := backfillStore{estimates: map[string][]byte{}}
	connectorRegistry, _ := connectors.NewRegistry(fakeplugin.FixtureConnector{})
	sealer, _ := connectors.NewSealer("fixture-audit-credential-key-0123456789")
	connectorStore := &memoryConnectors{items: map[string]connectors.Instance{}}
	handler, err := httpapi.New(auditCorpora{}, content.Service{Submissions: auditWithdrawals{}}, retrieval.Service{}, uploads.Service{}, keys, catalogCursorKey, httpapi.WithAudit(sink), httpapi.WithConnectors(connectors.Service{Store: connectorStore, Registry: connectorRegistry, Sealer: sealer, Tokens: auditTokens{}}), httpapi.WithBackfills(backfill.Service{Store: estimateStore, Registry: estimateStore, Plans: backfillPlans{}}, backfill.Promotions{}), httpapi.WithQuarantine(quarantine.Service{Store: quarantineStore{estimates: map[string][]byte{}}}))
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request("POST", "/v0/corpora", token, `{"idempotency_key":"create","name":"secret-name-should-not-be-audited"}`)
	if w.Code != 201 || len(sink.events) != 1 {
		t.Fatalf("create status=%d events=%+v body=%s", w.Code, sink.events, w.Body.String())
	}
	e := sink.events[0]
	if e.Outcome != "accepted" || e.Actor == "" || e.Actor == token || e.Organization != "org_a" || e.TargetID != "corpus_created" || e.RequestID != w.Header().Get("X-Request-ID") {
		t.Fatalf("accepted event %+v", e)
	}
	// These are actual contract addresses, including aliases and parameter IDs.
	for _, tc := range []struct{ method, path, action, target, id string }{
		{"PUT", "/v0/corpora/corpus_a/retrieval", "corpus.configure", "corpus", "corpus_a"},
		{"POST", "/v0/corpora/corpus_a/rebuilds", "operation.rebuild", "corpus", "corpus_a"},
		{"POST", "/v0/admin/plugins/", "plugin.register", "plugin_registration", ""},
		{"POST", "/v0/admin/plugins/registration_a/activate", "plugin.activate", "plugin_registration", "registration_a"},
		{"POST", "/v0/admin/plugins/plan/rollback", "plugin.rollback", "pipeline_plan", ""},
		{"POST", "/v0/connectors", "connector.create", "connector", ""},
		{"POST", "/v0/connectors/connector_a/disable", "connector.disable", "connector", "connector_a"},
		{"PUT", "/v0/connectors/connector_a/credential", "credential.replace", "connector", "connector_a"},
		{"PUT", "/v0/connectors/connector_a/schedule", "connector.update", "connector", "connector_a"},
		{"POST", "/v0/connectors/connector_a/runs", "connector.run", "connector", "connector_a"},
		{"POST", "/v0/connectors/connector_a/tokens", "token.create", "connector", "connector_a"},
		{"POST", "/v0/connectors/connector_a/tokens/token_a/rotate", "token.rotate", "connector_token", "token_a"},
		{"DELETE", "/v0/connectors/connector_a/tokens/token_a", "token.revoke", "connector_token", "token_a"},
		{"POST", "/v0/records/withdrawals", "record.withdraw", "record", ""},
		{"POST", "/v0/saved-queries/query_a/delete", "saved_query.delete", "saved_query", "query_a"},
		{"POST", "/v0/subscriptions/subscription_a/delete", "subscription.delete", "subscription", "subscription_a"},
		{"POST", "/v0/operations/operation_a/cancel", "operation.cancel", "operation", "operation_a"},
		{"POST", "/v0/operations/operation_a/rerun", "operation.rerun", "operation", "operation_a"},
		{"POST", "/v0/operations/operation_a/pause", "operation.pause", "operation", "operation_a"},
		{"POST", "/v0/operations/operation_a/resume", "operation.resume", "operation", "operation_a"},
		{"POST", "/v0/admin/backfills", "operation.backfill", "operation", ""},
		{"POST", "/v0/admin/quarantine/reprocess", "operation.reprocess", "operation", ""},
		{"POST", "/v0/admin/spaces/space_a/promote", "vector_space.promote", "vector_space", "space_a"},
	} {
		before := len(sink.events)
		w := request(tc.method, tc.path, token, `{"secret":"fixture-credential-private","password":"fixture-password-private"}`)
		if len(sink.events) != before+1 {
			t.Fatalf("%s %s status=%d not audited", tc.method, tc.path, w.Code)
		}
		e := sink.events[before]
		if w.Code < 400 || e.Action != tc.action || e.TargetType != tc.target || e.TargetID != tc.id || e.Outcome != "refused" || e.Detail.Status != w.Code {
			t.Fatalf("%s %s: status=%d event=%+v", tc.method, tc.path, w.Code, e)
		}
	}
	before := len(sink.events)
	request("GET", "/v0/corpora", token, "")
	request("POST", "/v0/search", token, `{}`)
	request("GET", "/v0/admin/audit", token, "")
	if len(sink.events) != before {
		t.Fatal("read or search audited")
	}
	w = request("POST", "/v0/corpora", "unknown-key-secret", `{}`)
	anonymous := sink.events[len(sink.events)-1]
	if w.Code != 401 || anonymous.Actor != "" || anonymous.Organization != "" || anonymous.Detail.ErrorCode != "invalid_api_key" {
		t.Fatalf("unauthenticated event %+v status=%d", anonymous, w.Code)
	}
	// Successful estimates do not enter the audit, but the subsequent
	// confirmation remains usable and produces the operation target.
	keys[token] = corpus.Scope{Organization: "org_a", Actions: []string{"corpora:read", "corpora:write", "plugins:admin"}, Corpora: []string{"*"}}
	for _, path := range []string{"/v0/admin/backfills", "/v0/admin/quarantine/reprocess"} {
		before := len(sink.events)
		w := request("POST", path, token, `{"idempotency_key":"dry","corpus_id":"corpus_a","dry_run":true}`)
		if w.Code != 200 || len(sink.events) != before {
			t.Fatalf("estimate %s status=%d audited=%v body=%s", path, w.Code, len(sink.events) != before, w.Body.String())
		}
		body := `{"idempotency_key":"dry","corpus_id":"corpus_a","dry_run":false}`
		if path == "/v0/admin/backfills" {
			body = `{"idempotency_key":"dry","corpus_id":"corpus_a","dry_run":false,"confirm_cost":true}`
		}
		w = request("POST", path, token, body)
		if w.Code != 202 || len(sink.events) != before+1 || sink.events[before].TargetID == "" {
			t.Fatalf("confirmation %s status=%d events=%+v body=%s", path, w.Code, sink.events, w.Body.String())
		}
	}
	// Credential deposits and generated token secrets pass through production
	// services; only their public metadata may reach the audit sink or logger.
	keys[token] = corpus.Scope{Organization: "org_a", Actions: []string{"corpora:write", "corpora:read", "connectors:write", "connectors:admin"}, Corpora: []string{"*"}}
	logs.Reset()
	w = request("POST", "/v0/connectors", token, `{"idempotency_key":"connector","corpus_id":"corpus_a","source_namespace":"wire","kind":"fixture","config":{"script":[]},"credential":{"secret":{"token":"fixture-audit-deposited-secret"}}}`)
	if w.Code != 201 {
		t.Fatalf("credential create status=%d body=%s", w.Code, w.Body.String())
	}
	var connector struct {
		ID string `json:"connector_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &connector)
	deposited := sink.events[len(sink.events)-1]
	if deposited.TargetID != connector.ID || deposited.Detail.CredentialVersion != 1 {
		t.Fatalf("credential metadata %+v", deposited)
	}
	w = request("POST", "/v0/connectors/"+connector.ID+"/tokens", token, "")
	var issued struct {
		Secret string `json:"secret"`
		Token  struct {
			ID string `json:"token_id"`
		} `json:"token"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &issued)
	if w.Code != 201 || issued.Secret == "" {
		t.Fatalf("token create status=%d", w.Code)
	}
	issuance := sink.events[len(sink.events)-1]
	if issuance.Action != "token.create" || issuance.TargetType != "connector_token" || issuance.TargetID != issued.Token.ID {
		t.Fatalf("token target %+v", issuance)
	}
	privateData, _ := json.Marshal(sink.events)
	for _, secret := range []string{issued.Secret, "fixture-audit-deposited-secret"} {
		if bytes.Contains(privateData, []byte(secret)) || strings.Contains(logs.String(), secret) {
			t.Fatal("credential or generated token leaked to audit")
		}
	}
	// A write-only key keeps public record/version IDs private while the
	// operator audit retains the command's internally resolved record target.
	keys[token] = corpus.Scope{Organization: "org_a", Actions: []string{"content:write"}, Corpora: []string{"*"}}
	w = request("POST", "/v0/records/withdrawals", token, `{"idempotency_key":"withdraw","source":{"corpus_id":"corpus_a","namespace":"audit","record_key":"source-a"}}`)
	withdrawn := sink.events[len(sink.events)-1]
	if w.Code != 202 || withdrawn.Outcome != "accepted" || withdrawn.TargetType != "record" || withdrawn.TargetID != "record_withdrawn" {
		t.Fatalf("write-only withdrawal status=%d event=%+v body=%s", w.Code, withdrawn, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "record_withdrawn") || strings.Contains(w.Body.String(), "version_private") {
		t.Fatal("write-only withdrawal response reveals private IDs")
	}
	keys[token] = corpus.Scope{Organization: "org_a", Actions: []string{"corpora:write", "corpora:read"}, Corpora: []string{"*"}}
	// A storage failure never releases the buffered successful response or logs
	// an audit entry that did not commit.
	logs.Reset()
	sink.fail = true
	before = len(sink.events)
	w = request("POST", "/v0/corpora", token, `{"idempotency_key":"again","name":"Again"}`)
	if w.Code != 503 || len(sink.events) != before || strings.Contains(logs.String(), `"event":"quivr.audit"`) {
		t.Fatalf("failed audit status=%d events=%d log=%s", w.Code, len(sink.events), logs.String())
	}
	sink.fail = false
	logs.Reset()
	request("POST", "/v0/corpora", token, `{"idempotency_key":"third","name":"secret-name-should-not-be-audited"}`)
	data, _ := json.Marshal(sink.events)
	for _, secret := range []string{token, "unknown-key-secret", "secret-name-should-not-be-audited", "fixture-credential-private", "fixture-password-private"} {
		if bytes.Contains(data, []byte(secret)) || strings.Contains(logs.String(), secret) {
			t.Fatalf("secret leaked: %s", secret)
		}
	}
	if !strings.Contains(logs.String(), `"event":"quivr.audit"`) {
		t.Fatal("committed audit log absent")
	}
}

func TestAuditReadAuthorizationAndFilterBoundPaging(t *testing.T) {
	sink := &auditSink{page: []audit.Event{{ID: 9007199254740993, Organization: "org_a", Action: "plugin.activate", Time: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}, {ID: 2}}}
	keys := map[string]corpus.Scope{
		"reader": {Organization: "org_a", Actions: []string{"audit:read"}, Corpora: []string{"*"}},
		"other":  {Organization: "org_b", Actions: []string{"audit:read"}, Corpora: []string{"*"}},
		"fenced": {Organization: "org_a", Actions: []string{"audit:read"}, Corpora: []string{"corpus_a"}},
		"admin":  {Organization: "org_a", Actions: []string{"plugins:admin", "observability:read"}, Corpora: []string{"*"}},
	}
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, keys, catalogCursorKey, httpapi.WithAudit(sink))
	if err != nil {
		t.Fatal(err)
	}
	get := func(key, query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/v0/admin/audit"+query, nil)
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, key := range []string{"fenced", "admin"} {
		if w := get(key, ""); w.Code != 403 {
			t.Fatalf("%s status=%d", key, w.Code)
		}
	}
	if w := get("unknown", ""); w.Code != 401 {
		t.Fatalf("unknown status=%d", w.Code)
	}
	query := "?limit=1&actor=actor-a&action=plugin.activate&target_type=plugin_registration&target_id=reg-a&since=2026-01-01T00:00:00Z&until=2026-02-01T00:00:00Z"
	w := get("reader", query)
	if w.Code != 200 || sink.org != "org_a" || sink.filter.Actor != "actor-a" || sink.filter.Action != "plugin.activate" || sink.filter.TargetType != "plugin_registration" || sink.filter.TargetID != "reg-a" || sink.filter.Since.IsZero() || sink.filter.Until.IsZero() || sink.filter.Limit != 2 {
		t.Fatalf("filter transport status=%d org=%s filter=%+v body=%s", w.Code, sink.org, sink.filter, w.Body.String())
	}
	var page struct {
		Items []struct {
			ID string `json:"id"`
		}
		Cursor string `json:"next_page_cursor"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].ID != "9007199254740993" || page.Cursor == "" {
		t.Fatalf("page %+v %v", page, err)
	}
	if w := get("reader", query+"&page_cursor="+page.Cursor); w.Code != 200 || sink.filter.After != 9007199254740993 {
		t.Fatalf("resume status=%d after=%d", w.Code, sink.filter.After)
	}
	for _, tc := range []struct{ key, query string }{{"other", query + "&page_cursor=" + page.Cursor}, {"reader", "?page_cursor=" + page.Cursor}, {"reader", query + "&page_cursor=tampered"}, {"reader", "?limit=0"}, {"reader", "?since=bad"}, {"reader", "?since=2026-02-01T00:00:00Z&until=2026-01-01T00:00:00Z"}} {
		if w := get(tc.key, tc.query); w.Code != 422 {
			t.Fatalf("invalid filter/cursor %s %s status=%d body=%s", tc.key, tc.query, w.Code, w.Body.String())
		}
	}
	if len(sink.events) != 0 {
		t.Fatal("audit reads were audited")
	}
}
