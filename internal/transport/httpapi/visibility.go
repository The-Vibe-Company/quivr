package httpapi

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/visibility"
)

func callerPrincipals(values []string) ([]string, bool) {
	if len(values) != 1 || len(values[0]) > visibility.MaxHeaderBytes {
		return nil, false
	}
	if values[0] == "" {
		return []string{}, true
	}
	tokens := strings.Split(values[0], ",")
	for i := range tokens {
		tokens[i] = strings.Trim(tokens[i], " \t")
	}
	return visibility.Normalize(tokens)
}

func readsData(r *http.Request) bool {
	if r.Pattern == "" || r.Pattern == "unmatched" {
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return true
	case http.MethodPost:
		switch r.Pattern {
		case "/v0/search", "/v0/facets", "/v0/subscription-previews",
			"/v0/admin/backfills", "/v0/admin/quarantine/reprocess", evaluatorMigrationsPath, evaluationRetirementsPath:
			return true
		}
	}
	return false
}

// These operator and upload surfaces have no record scope to intersect with
// the caller's principals. Require a separately scoped, non-delegated key.
func requiresNonDelegatedKey(pattern string) bool {
	switch pattern {
	case "/v0/uploads/{upload_id}", "/v0/blobs/{blob_id}", "/v0/operations/{operation_id}",
		"/v0/admin/audit", auditExportPath, "/v0/admin/queues",
		evaluationBacklogPath, evaluationRetirementsPath, evaluatorMigrationsPath, "/v0/admin/backfills",
		"/v0/admin/quarantine/reprocess", "/v0/admin/subscriptions/evaluation-retirements/{retirement_id}":
		return true
	}
	return strings.HasPrefix(pattern, "/v0/admin/stats/")
}

func WithVisibility(store visibility.Store) Option {
	return func(a *API) { a.Visibility = store }
}

func (a *API) visibilityGrant(w http.ResponseWriter, r *http.Request, scope corpus.Scope, id string) {
	if scope.VisibilityMode == visibility.Enforce {
		writeError(w, publicerr.Forbidden, nil)
		return
	}
	permission := "visibility:write"
	if r.Method == http.MethodGet {
		permission = "visibility:read"
	}
	if !scope.Allows(permission) {
		writeError(w, publicerr.Forbidden, nil)
		return
	}
	if a.Visibility == nil || !scope.Contains(id) {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	values := r.URL.Query()
	namespace := values.Get("source_namespace")
	if len(values) != 1 || len(values["source_namespace"]) != 1 || namespace == "" ||
		!content.ValidText(namespace) {
		writeError(w, publicerr.InvalidQuery, nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		grant, err := a.Visibility.Grant(r.Context(), scope.Organization, id, namespace)
		if err != nil {
			writeError(w, err, publicerr.StorageUnavailable)
			return
		}
		send(w, http.StatusOK, transport.VisibilityGrant{
			Visibility:        transport.VisibilityGrantVisibility(grant.Visibility),
			AllowedPrincipals: grant.AllowedPrincipals,
		})
	case http.MethodPut:
		raw, ok := decodeRequest(w, r, a.schemas["VisibilityGrant"])
		if !ok {
			return
		}
		values := raw.(map[string]any)
		entry := visibility.Grant{Visibility: values["visibility"].(string)}
		for _, value := range values["allowed_principals"].([]any) {
			entry.AllowedPrincipals = append(entry.AllowedPrincipals, value.(string))
		}
		entry.AllowedPrincipals, ok = visibility.Normalize(entry.AllowedPrincipals)
		if !ok || entry.Visibility == visibility.Public && len(entry.AllowedPrincipals) != 0 {
			writeError(w, publicerr.InvalidSchema, nil)
			return
		}
		if err := a.Visibility.SetGrant(r.Context(), scope.Organization, id, namespace, entry); err != nil {
			writeError(w, err, publicerr.StorageUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := a.Visibility.DeleteGrant(r.Context(), scope.Organization, id, namespace); err != nil {
			writeError(w, err, publicerr.StorageUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *API) GetVisibilityGrant(ctx context.Context, in transport.GetVisibilityGrantRequestObject) (transport.GetVisibilityGrantResponseObject, error) {
	return transport.GetVisibilityGrantResponseFunc(func(w http.ResponseWriter) { a.visibilityGrant(w, in.HTTPRequest, requestScope(ctx), in.CorpusId) }), nil
}

func (a *API) SetVisibilityGrant(ctx context.Context, in transport.SetVisibilityGrantRequestObject) (transport.SetVisibilityGrantResponseObject, error) {
	return transport.SetVisibilityGrantResponseFunc(func(w http.ResponseWriter) { a.visibilityGrant(w, in.HTTPRequest, requestScope(ctx), in.CorpusId) }), nil
}

func (a *API) DeleteVisibilityGrant(ctx context.Context, in transport.DeleteVisibilityGrantRequestObject) (transport.DeleteVisibilityGrantResponseObject, error) {
	return transport.DeleteVisibilityGrantResponseFunc(func(w http.ResponseWriter) { a.visibilityGrant(w, in.HTTPRequest, requestScope(ctx), in.CorpusId) }), nil
}
