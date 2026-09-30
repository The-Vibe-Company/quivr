package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// WithMonitoring enables Saved Query and Subscription routes.
func WithMonitoring(service monitoring.Service) Option {
	return func(a *API) { a.Monitoring = service }
}

type monitoringSchemas struct {
	savedQuery, savedQueryVersion, subscription, subscriptionVersion, action, preview, rename *jsonschema.Schema
}

func monitoringFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, monitoring.ErrForbidden):
		failure(w, 403, "forbidden")
	case errors.Is(err, monitoring.ErrNotFound):
		failure(w, 404, "not_found")
	case errors.Is(err, monitoring.ErrConflict):
		failure(w, 409, "idempotency_conflict")
	case errors.Is(err, monitoring.ErrSubscriptionDeleted), errors.Is(err, monitoring.ErrSavedQueryDeleted),
		errors.Is(err, monitoring.ErrSavedQueryInUse):
		failure(w, 409, publicCode(err, "conflict"))
	case errors.Is(err, monitoring.ErrInvalidOwner):
		failure(w, 422, "invalid_owner")
	case errors.Is(err, monitoring.ErrUnsupportedProfile), errors.Is(err, monitoring.ErrUnsupportedEvaluator),
		errors.Is(err, monitoring.ErrUnknownDestination), errors.Is(err, monitoring.ErrUnknownSavedQuery),
		errors.Is(err, monitoring.ErrTooLarge):
		failure(w, 422, publicCode(err, "invalid_input"))
	case errors.Is(err, monitoring.ErrInvalidExpression), errors.Is(err, monitoring.ErrInvalidEvaluatorConfiguration):
		// The evaluator's declared schema refused the pinned expression or
		// configuration: name the request member and the first schema issue.
		e := apiError(422, publicCode(err, "invalid_input"))
		if field, message := monitoring.Field(err); field != "" {
			e.Field = &field
			if message != "" {
				e.Message = message
			}
		}
		send(w, 422, e)
	case errors.Is(err, monitoring.ErrPreviewUnavailable):
		failure(w, 503, "evaluator_unavailable")
	case errors.Is(err, monitoring.ErrPreviewFailed):
		failure(w, 502, "evaluator_error")
	default:
		failure(w, 503, "storage_unavailable")
	}
}

// monitoringRoutes serves /v0/saved-queries and /v0/subscriptions: creation,
// reads, the Subscription listing by owner, editing by new Version, renaming,
// disable, enable and deletion.
func (a *API) monitoringRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	if r.URL.Path == "/v0/subscription-previews" {
		a.previewSubscription(w, r, scope)
		return true
	}
	var resource string
	switch {
	case strings.HasPrefix(r.URL.Path, "/v0/saved-queries"):
		resource = "saved-queries"
	case strings.HasPrefix(r.URL.Path, "/v0/subscriptions"):
		resource = "subscriptions"
	default:
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v0/"+resource), "/")
	if parts[0] != "" || a.Monitoring.Store == nil {
		failure(w, 404, "not_found")
		return true
	}
	parts = parts[1:]
	for _, p := range parts {
		if p == "" {
			failure(w, 404, "not_found")
			return true
		}
	}
	method := "GET"
	switch {
	case len(parts) == 0, len(parts) == 2 && (parts[1] == "versions" || parts[1] == "delete" || parts[1] == "rename"):
		method = "POST"
	case resource == "subscriptions" && len(parts) == 2 && (parts[1] == "disable" || parts[1] == "enable"):
		method = "POST"
	case len(parts) == 1, len(parts) == 3 && parts[1] == "versions":
	default:
		failure(w, 404, "not_found")
		return true
	}
	// The Subscription collection also lists by owner.
	if resource == "subscriptions" && len(parts) == 0 && r.Method == "GET" {
		method = "GET"
	}
	if r.Method != method {
		failure(w, 405, "method_not_allowed")
		return true
	}
	action := "monitoring:read"
	if method == "POST" {
		action = "monitoring:write"
	}
	if !scope.Allows(action) {
		failure(w, 403, "forbidden")
		return true
	}
	ctx := r.Context()
	switch {
	case resource == "saved-queries" && len(parts) == 0:
		var in monitoring.SavedQueryInput
		if !a.decodeMonitoring(w, r, a.monitoringSchemas.savedQuery, &in) {
			return true
		}
		q, err := a.Monitoring.CreateSavedQuery(ctx, scope, in)
		respondMonitoring(w, 201, savedQueryToTransport(q), err)
	case resource == "saved-queries" && len(parts) == 1:
		q, err := a.Monitoring.SavedQuery(ctx, scope, parts[0])
		respondMonitoring(w, 200, savedQueryToTransport(q), err)
	case resource == "saved-queries" && parts[1] == "versions" && len(parts) == 2:
		var in monitoring.SavedQueryVersionInput
		if !a.decodeMonitoring(w, r, a.monitoringSchemas.savedQueryVersion, &in) {
			return true
		}
		v, err := a.Monitoring.CreateSavedQueryVersion(ctx, scope, parts[0], in)
		respondMonitoring(w, 201, savedQueryVersionToTransport(v), err)
	case resource == "saved-queries" && parts[1] == "rename":
		var in renameRequest
		if !a.decodeMonitoring(w, r, a.monitoringSchemas.rename, &in) {
			return true
		}
		q, err := a.Monitoring.RenameSavedQuery(ctx, scope, in.Key, parts[0], in.Name)
		respondMonitoring(w, 200, savedQueryToTransport(q), err)
	case resource == "saved-queries" && parts[1] == "delete":
		key, ok := a.decodeAction(w, r)
		if !ok {
			return true
		}
		q, err := a.Monitoring.DeleteSavedQuery(ctx, scope, key, parts[0])
		respondMonitoring(w, 200, savedQueryToTransport(q), err)
	case resource == "saved-queries":
		v, err := a.Monitoring.SavedQueryVersion(ctx, scope, parts[0], parts[2])
		respondMonitoring(w, 200, savedQueryVersionToTransport(v), err)
	case len(parts) == 0 && method == "GET":
		a.listSubscriptions(w, r, scope)
	case len(parts) == 0:
		var in monitoring.SubscriptionInput
		if !a.decodeMonitoring(w, r, a.monitoringSchemas.subscription, &in) {
			return true
		}
		s, err := a.Monitoring.CreateSubscription(ctx, scope, in)
		respondMonitoring(w, 201, subscriptionToTransport(s), err)
	case len(parts) == 1:
		s, err := a.Monitoring.Subscription(ctx, scope, parts[0])
		respondMonitoring(w, 200, subscriptionToTransport(s), err)
	case parts[1] == "versions" && len(parts) == 2:
		var in monitoring.SubscriptionVersionInput
		if !a.decodeMonitoring(w, r, a.monitoringSchemas.subscriptionVersion, &in) {
			return true
		}
		v, err := a.Monitoring.CreateSubscriptionVersion(ctx, scope, parts[0], in)
		respondMonitoring(w, 201, subscriptionVersionToTransport(v), err)
	case parts[1] == "rename":
		var in renameRequest
		if !a.decodeMonitoring(w, r, a.monitoringSchemas.rename, &in) {
			return true
		}
		s, err := a.Monitoring.RenameSubscription(ctx, scope, in.Key, parts[0], in.Name)
		respondMonitoring(w, 200, subscriptionToTransport(s), err)
	case parts[1] == "disable" || parts[1] == "enable" || parts[1] == "delete":
		key, ok := a.decodeAction(w, r)
		if !ok {
			return true
		}
		command := map[string]func(context.Context, corpus.Scope, string, string) (monitoring.Subscription, error){
			"disable": a.Monitoring.DisableSubscription, "enable": a.Monitoring.EnableSubscription, "delete": a.Monitoring.DeleteSubscription,
		}[parts[1]]
		s, err := command(ctx, scope, key, parts[0])
		respondMonitoring(w, 200, subscriptionToTransport(s), err)
	default:
		v, err := a.Monitoring.SubscriptionVersion(ctx, scope, parts[0], parts[2])
		respondMonitoring(w, 200, subscriptionVersionToTransport(v), err)
	}
	return true
}

// renameRequest is the body of a Saved Query or Subscription rename.
type renameRequest struct {
	Key  string `json:"idempotency_key"`
	Name string `json:"name"`
}

// decodeAction decodes an idempotent action request and returns its key.
func (a *API) decodeAction(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in struct {
		Key string `json:"idempotency_key"`
	}
	ok := a.decodeMonitoring(w, r, a.monitoringSchemas.action, &in)
	return in.Key, ok
}

// decodeMonitoring validates the body against its public schema and decodes
// it with exact numbers, because pinned plugin-defined objects are immutable.
func (a *API) decodeMonitoring(w http.ResponseWriter, r *http.Request, schema *jsonschema.Schema, v any) bool {
	raw, payload, ok := readJSON(w, r, maxRequestBytes)
	if !ok {
		return false
	}
	if err := schema.Validate(raw); err != nil {
		failure(w, 422, "invalid_schema")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(v); err != nil {
		failure(w, 422, "invalid_schema")
		return false
	}
	return true
}

func respondMonitoring(w http.ResponseWriter, status int, body any, err error) {
	if err != nil {
		monitoringFailure(w, err)
		return
	}
	send(w, status, body)
}

func savedQueryVersionToTransport(v monitoring.SavedQueryVersion) transport.SavedQueryVersion {
	return transport.SavedQueryVersion{SavedQueryId: v.SavedQueryID, VersionId: v.VersionID, Definition: transport.SavedQueryDefinition{
		CorpusIds: v.Definition.CorpusIDs, Expression: v.Definition.Expression, RetrievalProfile: v.Definition.RetrievalProfile,
		TemporalPolicy: transport.SavedQueryDefinitionTemporalPolicy(v.Definition.TemporalPolicy),
	}}
}

func savedQueryToTransport(q monitoring.SavedQuery) transport.SavedQuery {
	return transport.SavedQuery{SavedQueryId: q.ID, Name: q.Name, Deleted: q.Deleted, CurrentVersion: savedQueryVersionToTransport(q.Current)}
}

func subscriptionVersionToTransport(v monitoring.SubscriptionVersion) transport.SubscriptionVersion {
	return transport.SubscriptionVersion{SubscriptionId: v.SubscriptionID, VersionId: v.VersionID, SavedQueryId: v.SavedQueryID, SavedQueryVersionId: v.SavedQueryVersionID,
		Evaluator:     transport.EvaluatorConfig{PluginId: v.Evaluator.PluginID, Version: v.Evaluator.Version, Configuration: v.Evaluator.Configuration},
		DestinationId: v.DestinationID, Owner: owner(v.Owner)}
}

func subscriptionToTransport(s monitoring.Subscription) transport.Subscription {
	return transport.Subscription{SubscriptionId: s.ID, Name: s.Name, Owner: owner(s.Owner), Enabled: s.Enabled, Deleted: s.Deleted, CurrentVersion: subscriptionVersionToTransport(s.Current)}
}

// owner is the public Subscription Owner: absent for a global Subscription.
func owner(o string) *string {
	if o == "" {
		return nil
	}
	return &o
}

// subscriptionPage is the signed payload of a Subscription listing cursor,
// bound to the owner filter and authorization scope in its own domain.
type subscriptionPage struct {
	Version int    `json:"v"`
	Owner   string `json:"o"`
	Scope   string `json:"s"`
	After   string `json:"a"`
}

func (a *API) encodeSubscriptionPage(p subscriptionPage) string {
	b, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(a.signCursor(subscriptionPageDomain, b))
}

func (a *API) decodeSubscriptionPage(token, owner string, s corpus.Scope) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", errors.New("invalid_cursor")
	}
	b, e1 := base64.RawURLEncoding.DecodeString(parts[0])
	sig, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	var p subscriptionPage
	if e1 != nil || e2 != nil || !hmac.Equal(sig, a.signCursor(subscriptionPageDomain, b)) || json.Unmarshal(b, &p) != nil || p.Version != 1 || p.After == "" {
		return "", errors.New("invalid_cursor")
	}
	if p.Owner != owner || p.Scope != scopeDigest(s) {
		return "", errPageScope
	}
	return p.After, nil
}

// listSubscriptions pages the active Subscriptions of one owner, or the
// global ones with owner=none, that the key sees.
func (a *API) listSubscriptions(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	q := r.URL.Query()
	for k, v := range q {
		if (k != "owner" && k != "page_cursor" && k != "limit") || len(v) != 1 {
			failure(w, 422, "invalid_query")
			return
		}
	}
	ownerRef := q.Get("owner")
	if ownerRef == "" {
		failure(w, 422, "invalid_query")
		return
	}
	filter := monitoring.OwnerFilter{Owner: ownerRef}
	if ownerRef == monitoring.NoOwner {
		filter = monitoring.OwnerFilter{Global: true}
	}
	if q.Has("page_cursor") && q.Get("page_cursor") == "" {
		failure(w, 422, "invalid_cursor")
		return
	}
	limit := 100
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 100 {
			failure(w, 422, "invalid_limit")
			return
		}
		limit = n
	}
	var after string
	if q.Has("page_cursor") {
		var err error
		if after, err = a.decodeSubscriptionPage(q.Get("page_cursor"), ownerRef, scope); errors.Is(err, errPageScope) {
			failure(w, 409, "cursor_scope_changed")
			return
		} else if err != nil {
			failure(w, 422, "invalid_cursor")
			return
		}
	}
	subs, err := a.Monitoring.Subscriptions(r.Context(), scope, filter, after, limit+1)
	if err != nil {
		monitoringFailure(w, err)
		return
	}
	page := transport.SubscriptionPage{Items: make([]transport.Subscription, 0, min(len(subs), limit))}
	for i, s := range subs {
		if i == limit {
			next := a.encodeSubscriptionPage(subscriptionPage{Version: 1, Owner: ownerRef, Scope: scopeDigest(scope), After: subs[limit-1].ID})
			page.NextPageCursor = &next
			break
		}
		page.Items = append(page.Items, subscriptionToTransport(s))
	}
	send(w, 200, page)
}

// previewSubscription serves the dry run of a proposed Subscription. It
// writes nothing, so it takes no idempotency key.
func (a *API) previewSubscription(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	if a.Monitoring.Store == nil {
		failure(w, 404, "not_found")
		return
	}
	if r.Method != "POST" {
		failure(w, 405, "method_not_allowed")
		return
	}
	if !scope.Allows("monitoring:write") {
		failure(w, 403, "forbidden")
		return
	}
	var in monitoring.PreviewInput
	if !a.decodeMonitoring(w, r, a.monitoringSchemas.preview, &in) {
		return
	}
	result, err := a.Monitoring.Preview(r.Context(), scope, in)
	if err != nil {
		monitoringFailure(w, err)
		return
	}
	out := transport.SubscriptionPreview{Evaluated: result.Evaluated, Matched: len(result.Matches), NotReady: result.NotReady, Complete: result.Complete,
		Matches: make([]transport.SubscriptionPreviewMatch, 0, len(result.Matches))}
	if !result.Oldest.IsZero() {
		out.OldestAcceptedAt = &result.Oldest
	}
	for _, m := range result.Matches {
		evidence := evidenceToTransport(m.Evidence)
		if evidence.Evaluator.Configuration == nil {
			evidence.Evaluator.Configuration = map[string]any{}
		}
		out.Matches = append(out.Matches, transport.SubscriptionPreviewMatch{CorpusId: m.CorpusID, RecordId: m.RecordID, RecordVersionId: m.VersionID, AcceptedAt: m.AcceptedAt, Evidence: evidence})
	}
	send(w, 200, out)
}
