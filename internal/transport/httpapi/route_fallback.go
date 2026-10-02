package httpapi

import (
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/routing"
)

// routeFallback preserves the established errors for unsupported methods and
// malformed addresses. Successful engine operations are registered only by
// the generated contract. Dynamic plugin methods still belong to the plugin.
func (a *API) routeFallback(w http.ResponseWriter, r *http.Request, match routing.PathMatch) {
	path := r.URL.Path
	scope := requestScope(r.Context())
	if isWebhookRoute(path) || isConnectorAPIPath(path) {
		if match.Handler != nil {
			match.Handler(w, r)
		} else {
			writeError(w, publicerr.NotFound, nil)
		}
		return
	}
	if strings.HasPrefix(path, "/v0/connectors/") {
		parts := strings.Split(strings.TrimPrefix(path, "/v0/connectors/"), "/")
		if len(parts) >= 2 && parts[1] == "tokens" {
			shape := ""
			switch {
			case len(parts) == 2:
				shape = "collection"
			case len(parts) == 3 && parts[2] != "":
				shape = "token"
			case len(parts) == 4 && parts[2] != "" && parts[3] == "rotate":
				shape = "rotate"
			}
			a.connectorTokenFallback(w, r, scope, parts[0], shape)
			return
		}
		if a.Connectors.Store == nil {
			writeError(w, publicerr.NotFound, nil)
		} else if len(parts) <= 2 {
			writeError(w, publicerr.MethodNotAllowed, nil)
		} else {
			writeError(w, publicerr.NotFound, nil)
		}
		return
	}
	if path == evaluationBacklogPath || path == evaluationRetirementsPath || strings.HasPrefix(path, evaluationRetirementsPath+"/") {
		if r.Method == "GET" && strings.HasPrefix(path, evaluationRetirementsPath+"/") {
			a.handleGetEvaluationRetirement(w, r, scope, strings.TrimPrefix(path, evaluationRetirementsPath+"/"))
		} else {
			a.evaluationAdministrationFallback(w, r, scope)
		}
		return
	}
	if path == "/v0/connectors" {
		if a.Connectors.Store == nil {
			writeError(w, publicerr.NotFound, nil)
		} else {
			writeError(w, publicerr.MethodNotAllowed, nil)
		}
		return
	}
	if match.Pattern == "/v0/connector-kinds" && match.Handler != nil {
		match.Handler(w, r)
		return
	}
	// These families rejected empty path identifiers before testing methods.
	invalidPath := strings.Contains(match.Pattern, "{saved_query_id}") && r.PathValue("saved_query_id") == "" ||
		strings.Contains(match.Pattern, "{subscription_id}") && r.PathValue("subscription_id") == "" ||
		strings.Contains(match.Pattern, "{match_id}") && r.PathValue("match_id") == "" ||
		strings.Contains(match.Pattern, "{delivery_id}") && r.PathValue("delivery_id") == "" ||
		strings.Contains(match.Pattern, "{plan_id}") && r.PathValue("plan_id") == ""
	if invalidPath {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	if match.Handler == nil {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	// These original route families only recognized their declared methods;
	// other families recognized the path first and answered method_not_allowed.
	switch {
	case strings.HasPrefix(match.Pattern, "/v0/records"), strings.HasPrefix(match.Pattern, "/v0/uploads"), strings.HasPrefix(match.Pattern, "/v0/blobs/"), strings.HasPrefix(match.Pattern, "/v0/ingestion-receipts/"), strings.HasPrefix(match.Pattern, "/v0/search"), strings.HasPrefix(match.Pattern, "/v0/changes"):
		writeError(w, publicerr.NotFound, nil)
	case match.Pattern == "/v0/corpora/{corpus_id}" || match.Pattern == "/v0/corpora/{corpus_id}/vector-spaces":
		writeError(w, publicerr.NotFound, nil)
	case strings.HasPrefix(match.Pattern, "/v0/saved-queries") || strings.HasPrefix(match.Pattern, "/v0/subscriptions") || match.Pattern == "/v0/subscription-previews":
		if a.Monitoring.Store == nil {
			writeError(w, publicerr.NotFound, nil)
		} else {
			writeError(w, publicerr.MethodNotAllowed, nil)
		}
	case strings.HasPrefix(match.Pattern, "/v0/matches") || strings.HasPrefix(match.Pattern, "/v0/deliveries"):
		if a.Monitoring.MatchStore == nil {
			writeError(w, publicerr.NotFound, nil)
		} else {
			writeError(w, publicerr.MethodNotAllowed, nil)
		}
	default:
		writeError(w, publicerr.MethodNotAllowed, nil)
	}
}
