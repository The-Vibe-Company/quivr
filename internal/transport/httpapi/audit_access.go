package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/audit"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
)

const auditExportPath = "/v0/admin/audit/export"

type auditEventKey struct{}

func (a *API) auditKey(r *http.Request) (corpus.Scope, string) {
	if token, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); bearer {
		if scope, known := a.Keys[token]; known {
			return scope, apiKeyID(token)
		}
	}
	return corpus.Scope{}, ""
}

func requestAuditEvent(r *http.Request, scope corpus.Scope, keyID string) audit.Event {
	actorID := scope.ActorID
	if !audit.ValidActorID(actorID) {
		actorID = keyID
	}
	return audit.Event{
		Actor: keyID, ActorID: actorID, Organization: scope.Organization,
		RequestID: logging.RequestID(r.Context()), TargetType: "http_route",
		Detail: audit.Detail{Route: r.Pattern},
	}
}

func (a *API) serveAudited(w *responseWriter, r *http.Request, scope corpus.Scope, keyID string) {
	refused := false
	delegated := keyID != "" && scope.VisibilityMode == visibility.Enforce
	if delegated && r.Pattern == visibilityGrantPath && (r.Method == http.MethodPut || r.Method == http.MethodDelete) {
		// Policy administration is always performed by a management key.
		refused = true
	}
	if delegated && readsData(r) {
		actor := r.Header.Values("X-Quivr-Actor")
		if len(actor) == 1 && audit.ValidActorID(actor[0]) {
			scope.ActorID = actor[0]
		}
		principals, valid := callerPrincipals(r.Header.Values("X-Quivr-Principals"))
		refused = len(actor) != 1 || !audit.ValidActorID(actor[0]) || !valid || requiresNonDelegatedKey(r.Pattern)
		if !refused {
			r = r.WithContext(visibility.WithPrincipals(r.Context(), principals))
		}
	}
	if a.Audit != nil {
		event := requestAuditEvent(r, scope, keyID)
		if route, sensitive := auditedRoutes[r.Method+" "+r.Pattern]; sensitive && keyID != "" && !refused {
			a.serveSensitive(w, r, route, event)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), auditEventKey{}, event))
		// Commit before writing the status or releasing any body bytes, including
		// the first flush of a stream. Only registered templates enter the event.
		w.beforeFinal = func(status int, code string) error {
			event.Action = a.accessAuditAction(r, status, code)
			if event.Action == "" {
				return nil
			}
			return a.appendAccessAudit(r.Context(), &event, status, code)
		}
	}
	if refused {
		writeError(w, publicerr.Forbidden, nil)
		return
	}
	a.servePushAudited(w, r)
}

func refusalAuditAction(status int, code string, matched bool) string {
	if code == "cursor_scope_changed" {
		return "permission.refused"
	}
	switch status {
	case http.StatusUnauthorized:
		return "auth.refused"
	case http.StatusForbidden:
		return "permission.refused"
	case http.StatusNotFound:
		if matched {
			// Scoped lookups deliberately return the same 404 for concealed
			// and missing resources, including from inside storage queries.
			return "resource.unavailable"
		}
	}
	return ""
}

func (a *API) accessAuditAction(r *http.Request, status int, code string) string {
	if action := refusalAuditAction(status, code, r.Pattern != "unmatched"); action != "" {
		return action
	}
	if r.Method == http.MethodGet && r.Pattern == auditExportPath {
		return "audit.export"
	}
	if !a.auditReads || r.Pattern == "unmatched" || status == http.StatusMethodNotAllowed {
		return ""
	}
	switch {
	case r.Method == http.MethodGet && !isWebhookRoute(r.URL.Path) && !isConnectorAPIPath(r.URL.Path):
		return "http.read"
	case r.Method == http.MethodPost && (r.Pattern == "/v0/search" || r.Pattern == "/v0/facets" || r.Pattern == "/v0/subscription-previews"):
		return "http.search"
	}
	return ""
}

// Batch items and open streams can report refusals inside a successful
// response. They retain the original request identity and correlation ID.
func (a *API) auditEmbeddedRefusal(ctx context.Context, status int, code string) error {
	event, ok := ctx.Value(auditEventKey{}).(audit.Event)
	if !ok {
		return nil
	}
	event.Action = refusalAuditAction(status, code, true)
	if event.Action == "" {
		return nil
	}
	return a.appendAccessAudit(ctx, &event, status, code)
}

func (a *API) appendAccessAudit(ctx context.Context, event *audit.Event, status int, code string) error {
	event.Detail.Status = status
	event.Detail.ErrorCode = boundedAuditID(code)
	event.Outcome = "refused"
	if status >= 200 && status < 300 {
		event.Outcome = "accepted"
	}
	work, cancel := lifecycle.CleanupContext(ctx, 2*time.Second)
	defer cancel()
	if err := a.Audit.Append(work, event); err != nil {
		return err
	}
	audit.Log(work, *event)
	return nil
}
