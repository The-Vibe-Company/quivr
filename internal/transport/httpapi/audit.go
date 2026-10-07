package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/internal/audit"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

func WithAudit(store audit.Store) Option { return func(a *API) { a.Audit = store } }
func apiKeyID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:16])
}

type auditedRoute struct{ action, target, parameter string }

// Only commands are enrolled; reads, searches, push delivery and ingestion do
// not create audit events. Add new sensitive routes here when introduced.
var auditedRoutes = map[string]auditedRoute{
	"PATCH /v0/corpora/{corpus_id}":                               {"corpus.rename", "corpus", "corpus_id"},
	"POST /v0/corpora/{corpus_id}/archive":                        {"corpus.archive", "corpus", "corpus_id"},
	"POST /v0/corpora/{corpus_id}/unarchive":                      {"corpus.unarchive", "corpus", "corpus_id"},
	"POST /v0/corpora":                                            {"corpus.create", "corpus", ""},
	"PUT /v0/corpora/{corpus_id}/retrieval":                       {"corpus.configure", "corpus", "corpus_id"},
	"POST /v0/corpora/{corpus_id}/rebuilds":                       {"operation.rebuild", "corpus", "corpus_id"},
	"POST /v0/admin/plugins":                                      {"plugin.register", "plugin_registration", ""},
	"POST /v0/admin/plugins/{registration_id}/activate":           {"plugin.activate", "plugin_registration", "registration_id"},
	"POST /v0/admin/plugins/plan/rollback":                        {"plugin.rollback", "pipeline_plan", ""},
	"POST /v0/connectors":                                         {"connector.create", "connector", ""},
	"POST /v0/connectors/{connector_id}/disable":                  {"connector.disable", "connector", "connector_id"},
	"PUT /v0/connectors/{connector_id}/credential":                {"credential.replace", "connector", "connector_id"},
	"PUT /v0/connectors/{connector_id}/schedule":                  {"connector.update", "connector", "connector_id"},
	"POST /v0/connectors/{connector_id}/runs":                     {"connector.run", "connector", "connector_id"},
	"POST /v0/connectors/{connector_id}/tokens":                   {"token.create", "connector", "connector_id"},
	"POST /v0/connectors/{connector_id}/tokens/{token_id}/rotate": {"token.rotate", "connector_token", "token_id"},
	"DELETE /v0/connectors/{connector_id}/tokens/{token_id}":      {"token.revoke", "connector_token", "token_id"},
	"POST /v0/records/withdrawals":                                {"record.withdraw", "record", ""},
	"POST /v0/saved-queries/{saved_query_id}/delete":              {"saved_query.delete", "saved_query", "saved_query_id"},
	"POST /v0/subscriptions/{subscription_id}/delete":             {"subscription.delete", "subscription", "subscription_id"},
	"POST /v0/operations/{operation_id}/cancel":                   {"operation.cancel", "operation", "operation_id"},
	"POST /v0/operations/{operation_id}/rerun":                    {"operation.rerun", "operation", "operation_id"},
	"POST /v0/operations/{operation_id}/pause":                    {"operation.pause", "operation", "operation_id"},
	"POST /v0/operations/{operation_id}/resume":                   {"operation.resume", "operation", "operation_id"},
	"POST /v0/admin/backfills":                                    {"operation.backfill", "operation", ""},
	"POST /v0/admin/quarantine/reprocess":                         {"operation.reprocess", "operation", ""},
	"POST /v0/admin/subscriptions/evaluation-retirements":         {"evaluation.retire", "evaluation_retirement", ""},
	"POST /v0/admin/spaces/{vector_space_id}/promote":             {"vector_space.promote", "vector_space", "vector_space_id"},
}

func (a *API) serveSensitive(w http.ResponseWriter, r *http.Request) {
	route, sensitive := auditedRoutes[r.Method+" "+r.Pattern]
	if !sensitive || a.Audit == nil {
		a.servePushAudited(w, r)
		return
	}
	// Unknown keys have no authenticated actor or organization. Refuse them
	// before database admission; the access log preserves the bounded refusal.
	token, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	scope, known := a.Keys[token]
	if !bearer || !known {
		a.servePushAudited(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	event := audit.Event{Action: route.action, TargetType: route.target, Actor: apiKeyID(token), Organization: scope.Organization, RequestID: logging.RequestID(ctx)}
	pattern, parts := strings.Split(r.Pattern, "/"), strings.Split(r.URL.Path, "/")
	for i, p := range pattern {
		if p == "{"+route.parameter+"}" && i < len(parts) {
			event.TargetID = boundedAuditID(parts[i])
		}
	}
	buffered := &auditResponse{header: make(http.Header)}
	buffered.header.Set("X-Request-ID", event.RequestID)
	for _, name := range []string{"X-Trace-ID", "X-Span-ID"} {
		if id := w.Header().Get(name); id != "" {
			buffered.header.Set(name, id)
		}
	}
	initialHeaders := buffered.header.Clone()
	body := &auditBodyReplay{source: r.Body}
	if body.source == nil {
		body.source = http.NoBody
	}
	err := a.Audit.Record(ctx, &event, func(work context.Context) error {
		// The store may replay a deadlocked parent transaction. Only the last
		// attempt's response escapes, with the same bounded request bytes.
		buffered = &auditResponse{header: initialHeaders.Clone()}
		if body.overflow {
			return errors.New("audited request exceeds replay bound")
		}
		work = audit.WithTargetRecorder(work, func(targetType, targetID string) {
			event.TargetType = boundedAuditID(targetType)
			event.TargetID = boundedAuditID(targetID)
		})
		request := r.Clone(work)
		request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body.cached.Bytes()), io.TeeReader(body.source, body)))
		a.servePushAudited(buffered, request)
		if buffered.err != nil {
			return buffered.err
		}
		if buffered.status == 0 {
			buffered.status = 200
		}
		event.Detail.Status = buffered.status
		event.Outcome = "refused"
		if buffered.status >= 200 && buffered.status < 300 {
			event.Outcome = "accepted"
			// Decode only explicitly named identifier fields. Credential values,
			// configuration, error messages and arbitrary payloads never enter audit.
			var result struct {
				CorpusID       string `json:"corpus_id"`
				ConnectorID    string `json:"connector_id"`
				RegistrationID string `json:"registration_id"`
				RecordID       string `json:"record_id"`
				OperationID    string `json:"operation_id"`
				PlanID         string `json:"plan_id"`
				RetirementID   string `json:"retirement_id"`
				DryRun         bool   `json:"dry_run"`
				Credential     *struct {
					Version int `json:"version"`
				} `json:"credential"`
				Token *struct {
					ID string `json:"token_id"`
				} `json:"token"`
			}
			_ = json.Unmarshal(buffered.body.Bytes(), &result)
			event.Detail.PlanID = boundedAuditID(result.PlanID)
			if result.Credential != nil {
				event.Detail.CredentialVersion = result.Credential.Version
			}
			if result.Token != nil {
				event.TargetType = "connector_token"
				event.TargetID = boundedAuditID(result.Token.ID)
			}
			if buffered.status == 200 && (route.action == "operation.backfill" || route.action == "operation.reprocess" || (route.action == "evaluation.retire" && result.DryRun)) {
				return audit.ErrReadOnly
			}
			if event.TargetID == "" {
				ids := map[string]string{"corpus": result.CorpusID, "connector": result.ConnectorID, "plugin_registration": result.RegistrationID, "record": result.RecordID, "operation": result.OperationID, "pipeline_plan": result.PlanID, "evaluation_retirement": result.RetirementID}
				event.TargetID = boundedAuditID(ids[event.TargetType])
			}
		} else {
			event.Detail.ErrorCode = boundedAuditID(buffered.errorCode)
		}
		return nil
	})
	if err != nil {
		writeError(w, publicerr.StorageUnavailable, nil)
		return
	}
	if event.ID != 0 {
		audit.Log(ctx, event)
	}
	for k, vs := range buffered.header {
		w.Header()[k] = vs
	}
	if coded, ok := w.(interface{ SetErrorCode(string) }); ok {
		coded.SetErrorCode(buffered.errorCode)
	}
	w.WriteHeader(buffered.status)
	_, _ = w.Write(buffered.body.Bytes())
}

// Capture only bytes a handler reads. Its decoder still owns media-type and
// size errors; a retry replays the captured prefix followed by any unread tail.
type auditBodyReplay struct {
	source   io.Reader
	cached   bytes.Buffer
	overflow bool
}

func (b *auditBodyReplay) Write(p []byte) (int, error) {
	remaining := maxPluginRegistrationBytes + 1 - b.cached.Len()
	n := min(len(p), remaining)
	b.cached.Write(p[:n])
	b.overflow = b.overflow || n < len(p)
	return len(p), nil
}
func boundedAuditID(s string) string {
	if len(s) > 512 {
		s = s[:512]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

type auditResponse struct {
	header    http.Header
	status    int
	body      bytes.Buffer
	errorCode string
	err       error
}

func (w *auditResponse) Header() http.Header      { return w.header }
func (w *auditResponse) SetErrorCode(code string) { w.errorCode = code }
func (w *auditResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *auditResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.body.Len()+len(b) > maxPluginRegistrationBytes {
		w.err = errors.New("audited response exceeds bound")
		return 0, w.err
	}
	return w.body.Write(b)
}

type auditCursor struct {
	After  int64  `json:"after"`
	Scope  string `json:"scope"`
	Filter string `json:"filter"`
}

func (a *API) ListAuditEvents(ctx context.Context, in transport.ListAuditEventsRequestObject) (transport.ListAuditEventsResponseObject, error) {
	return transport.ListAuditEventsResponseFunc(func(w http.ResponseWriter) { a.listAudit(w, in.HTTPRequest, requestScope(ctx)) }), nil
}
func (a *API) listAudit(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	if err := scope.Require(corpus.ActionAuditRead); err != nil {
		writeError(w, err, nil)
		return
	}
	if a.Audit == nil {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	q := r.URL.Query()
	limit, ok := pageLimit(w, q, 50, 200)
	if !ok {
		return
	}
	f := audit.Filter{Actor: q.Get("actor"), Action: q.Get("action"), TargetType: q.Get("target_type"), TargetID: q.Get("target_id"), Limit: limit + 1}
	for name, dest := range map[string]*time.Time{"since": &f.Since, "until": &f.Until} {
		if q.Has(name) {
			t, err := time.Parse(time.RFC3339Nano, q.Get(name))
			if err != nil {
				writeError(w, publicerr.InvalidSchema, nil)
				return
			}
			*dest = t.UTC()
		}
	}
	if !f.Since.IsZero() && !f.Until.IsZero() && !f.Since.Before(f.Until) {
		writeError(w, publicerr.InvalidSchema, nil)
		return
	}
	filterJSON, _ := json.Marshal(f)
	digest := sha256.Sum256(filterJSON)
	filterID := hex.EncodeToString(digest[:])
	scopeID := scopeDigest(scope)
	if q.Has("page_cursor") {
		var c auditCursor
		if a.decodePage("audit-page", q.Get("page_cursor"), &c) != nil || c.After <= 0 || c.Scope != scopeID || c.Filter != filterID {
			writeError(w, publicerr.InvalidCursor, nil)
			return
		}
		f.After = c.After
	}
	items, err := a.Audit.List(r.Context(), scope.Organization, f)
	if err != nil {
		writeError(w, publicerr.StorageUnavailable, nil)
		return
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = a.encodePage("audit-page", auditCursor{items[len(items)-1].ID, scopeID, filterID})
	}
	if items == nil {
		items = []audit.Event{}
	}
	// IDs are decimal strings so JavaScript clients preserve bigint precision.
	out := transport.AuditEventPage{Items: make([]transport.AuditEvent, 0, len(items)), NextPageCursor: optionalString(next)}
	for _, e := range items {
		row := transport.AuditEvent{Id: strconv.FormatInt(e.ID, 10), Time: e.Time, Actor: e.Actor, Action: e.Action, TargetType: e.TargetType, TargetId: e.TargetID, Organization: e.Organization, Outcome: transport.AuditEventOutcome(e.Outcome), RequestId: e.RequestID}
		row.Detail.Status = e.Detail.Status
		if e.Detail.CredentialVersion != 0 {
			version := e.Detail.CredentialVersion
			row.Detail.CredentialVersion = &version
		}
		row.Detail.ErrorCode = optionalString(e.Detail.ErrorCode)
		row.Detail.PlanId = optionalString(e.Detail.PlanID)
		out.Items = append(out.Items, row)
	}
	send(w, 200, out)
}
