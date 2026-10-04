package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// WithMonitoring enables Saved Query and Subscription routes.
func WithMonitoring(service monitoring.Service) Option {
	return func(a *API) { a.Monitoring = service }
}

func (a *API) monitoringAvailable(w http.ResponseWriter) bool {
	if a.Monitoring.Store == nil {
		writeError(w, publicerr.NotFound, nil)
		return false
	}
	return true
}

func validMonitoringIDs(w http.ResponseWriter, ids ...string) bool {
	for _, id := range ids {
		if id == "" {
			writeError(w, publicerr.NotFound, nil)
			return false
		}
	}
	return true
}

func (a *API) handleCreateSavedQuery(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	if !a.monitoringAvailable(w) {
		return
	}
	var in monitoring.SavedQueryInput
	q, err := a.Monitoring.CreateSavedQuery(r.Context(), scope, monitoring.SavedQueryInput{}, func() (monitoring.SavedQueryInput, error) {
		if !a.decodeMonitoring(w, r, a.schemas["SavedQueryCreate"], &in) {
			return monitoring.SavedQueryInput{}, errResponseWritten
		}
		return in, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	respondMonitoring(w, 201, savedQueryToTransport(q), err)
}

func (a *API) handleGetSavedQuery(w http.ResponseWriter, r *http.Request, scope corpus.Scope, savedQueryID string) {
	if !a.monitoringAvailable(w) {
		return
	}
	if !validMonitoringIDs(w, savedQueryID) {
		return
	}
	q, err := a.Monitoring.SavedQuery(r.Context(), scope, savedQueryID)
	respondMonitoring(w, 200, savedQueryToTransport(q), err)
}

func (a *API) handleCreateSavedQueryVersion(w http.ResponseWriter, r *http.Request, scope corpus.Scope, savedQueryID string) {
	if !a.monitoringAvailable(w) {
		return
	}
	if !validMonitoringIDs(w, savedQueryID) {
		return
	}
	var in monitoring.SavedQueryVersionInput
	v, err := a.Monitoring.CreateSavedQueryVersion(r.Context(), scope, "", monitoring.SavedQueryVersionInput{}, func() (string, monitoring.SavedQueryVersionInput, error) {
		if !a.decodeMonitoring(w, r, a.schemas["SavedQueryVersionCreate"], &in) {
			return "", monitoring.SavedQueryVersionInput{}, errResponseWritten
		}
		return savedQueryID, in, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	respondMonitoring(w, 201, savedQueryVersionToTransport(v), err)
}

func (a *API) handleRenameSavedQuery(w http.ResponseWriter, r *http.Request, scope corpus.Scope, savedQueryID string) {
	if !a.monitoringAvailable(w) {
		return
	}
	if !validMonitoringIDs(w, savedQueryID) {
		return
	}
	var in renameRequest
	q, err := a.Monitoring.RenameSavedQuery(r.Context(), scope, "", "", "", func() (string, string, string, error) {
		if !a.decodeMonitoring(w, r, a.schemas["RenameRequest"], &in) {
			return "", "", "", errResponseWritten
		}
		return in.Key, savedQueryID, in.Name, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	respondMonitoring(w, 200, savedQueryToTransport(q), err)
}

func (a *API) handleDeleteSavedQuery(w http.ResponseWriter, r *http.Request, scope corpus.Scope, savedQueryID string) {
	if !a.monitoringAvailable(w) {
		return
	}
	if !validMonitoringIDs(w, savedQueryID) {
		return
	}
	q, err := a.Monitoring.DeleteSavedQuery(r.Context(), scope, "", "", func() (string, string, error) {
		key, ok := a.decodeAction(w, r)
		if !ok {
			return "", "", errResponseWritten
		}
		return key, savedQueryID, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	respondMonitoring(w, 200, savedQueryToTransport(q), err)
}

func (a *API) handleGetSavedQueryVersion(w http.ResponseWriter, r *http.Request, scope corpus.Scope, savedQueryID, versionID string) {
	if !a.monitoringAvailable(w) {
		return
	}
	if !validMonitoringIDs(w, savedQueryID, versionID) {
		return
	}
	v, err := a.Monitoring.SavedQueryVersion(r.Context(), scope, savedQueryID, versionID)
	respondMonitoring(w, 200, savedQueryVersionToTransport(v), err)
}

func (a *API) handleCreateSubscription(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	if !a.monitoringAvailable(w) {
		return
	}
	var in monitoring.SubscriptionInput
	s, err := a.Monitoring.CreateSubscription(r.Context(), scope, monitoring.SubscriptionInput{}, func() (monitoring.SubscriptionInput, error) {
		if !a.decodeMonitoring(w, r, a.schemas["SubscriptionCreate"], &in) {
			return monitoring.SubscriptionInput{}, errResponseWritten
		}
		return in, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	respondMonitoring(w, 201, subscriptionToTransport(s), err)
}

func (a *API) handleGetSubscription(w http.ResponseWriter, r *http.Request, scope corpus.Scope, subscriptionID string) {
	if !a.monitoringAvailable(w) {
		return
	}
	if !validMonitoringIDs(w, subscriptionID) {
		return
	}
	s, err := a.Monitoring.Subscription(r.Context(), scope, subscriptionID)
	respondMonitoring(w, 200, subscriptionToTransport(s), err)
}

func (a *API) handleCreateSubscriptionVersion(w http.ResponseWriter, r *http.Request, scope corpus.Scope, subscriptionID string) {
	if !a.monitoringAvailable(w) {
		return
	}
	if !validMonitoringIDs(w, subscriptionID) {
		return
	}
	var in monitoring.SubscriptionVersionInput
	v, err := a.Monitoring.CreateSubscriptionVersion(r.Context(), scope, "", monitoring.SubscriptionVersionInput{}, func() (string, monitoring.SubscriptionVersionInput, error) {
		if !a.decodeMonitoring(w, r, a.schemas["SubscriptionVersionCreate"], &in) {
			return "", monitoring.SubscriptionVersionInput{}, errResponseWritten
		}
		return subscriptionID, in, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	respondMonitoring(w, 201, subscriptionVersionToTransport(v), err)
}

func (a *API) handleRenameSubscription(w http.ResponseWriter, r *http.Request, scope corpus.Scope, subscriptionID string) {
	if !a.monitoringAvailable(w) {
		return
	}
	if !validMonitoringIDs(w, subscriptionID) {
		return
	}
	var in renameRequest
	s, err := a.Monitoring.RenameSubscription(r.Context(), scope, "", "", "", func() (string, string, string, error) {
		if !a.decodeMonitoring(w, r, a.schemas["RenameRequest"], &in) {
			return "", "", "", errResponseWritten
		}
		return in.Key, subscriptionID, in.Name, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	respondMonitoring(w, 200, subscriptionToTransport(s), err)
}

func (a *API) subscriptionAction(w http.ResponseWriter, r *http.Request, scope corpus.Scope, subscriptionID, action string) {
	if !a.monitoringAvailable(w) {
		return
	}
	if !validMonitoringIDs(w, subscriptionID) {
		return
	}
	command := map[string]func(context.Context, corpus.Scope, string, string, ...func() (string, string, error)) (monitoring.Subscription, error){
		"disable": a.Monitoring.DisableSubscription, "enable": a.Monitoring.EnableSubscription, "delete": a.Monitoring.DeleteSubscription,
	}[action]
	s, err := command(r.Context(), scope, "", "", func() (string, string, error) {
		key, ok := a.decodeAction(w, r)
		if !ok {
			return "", "", errResponseWritten
		}
		return key, subscriptionID, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	respondMonitoring(w, 200, subscriptionToTransport(s), err)
}

func (a *API) handleDeleteSubscription(w http.ResponseWriter, r *http.Request, scope corpus.Scope, subscriptionID string) {
	a.subscriptionAction(w, r, scope, subscriptionID, "delete")
}

func (a *API) handleDisableSubscription(w http.ResponseWriter, r *http.Request, scope corpus.Scope, subscriptionID string) {
	a.subscriptionAction(w, r, scope, subscriptionID, "disable")
}

func (a *API) handleEnableSubscription(w http.ResponseWriter, r *http.Request, scope corpus.Scope, subscriptionID string) {
	a.subscriptionAction(w, r, scope, subscriptionID, "enable")
}

func (a *API) handleGetSubscriptionVersion(w http.ResponseWriter, r *http.Request, scope corpus.Scope, subscriptionID, versionID string) {
	if !a.monitoringAvailable(w) {
		return
	}
	if !validMonitoringIDs(w, subscriptionID, versionID) {
		return
	}
	v, err := a.Monitoring.SubscriptionVersion(r.Context(), scope, subscriptionID, versionID)
	respondMonitoring(w, 200, subscriptionVersionToTransport(v), err)
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
	ok := a.decodeMonitoring(w, r, a.schemas["ActionRequest"], &in)
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
		// A creation's owner answers the same code whether the request schema
		// or the service refuses it, and in a body as in the listing filter.
		// Elsewhere owner is not a member, so naming it stays invalid_schema.
		if schema == a.schemas["SubscriptionCreate"] && connectors.SchemaPointer(err) == "/owner" {
			writeError(w, publicerr.InvalidOwner, nil)
		} else {
			writeError(w, publicerr.InvalidSchema, nil)
		}
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(v); err != nil {
		writeError(w, publicerr.InvalidSchema, nil)
		return false
	}
	return true
}

func respondMonitoring(w http.ResponseWriter, status int, body any, err error) {
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
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
	return a.encodePage(subscriptionPageDomain, p)
}

func (a *API) decodeSubscriptionPage(token, owner string, s corpus.Scope) (string, error) {
	var p subscriptionPage
	if a.decodePage(subscriptionPageDomain, token, &p) != nil || p.Version != 1 || p.After == "" {
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
	if !a.monitoringAvailable(w) {
		return
	}
	var limit int
	var ok bool
	var ownerRef string
	var after string
	subs, err := a.Monitoring.Subscriptions(r.Context(), scope, monitoring.OwnerFilter{}, "", 0, func() (monitoring.OwnerFilter, string, int, error) {
		q := r.URL.Query()
		for k, v := range q {
			if (k != "owner" && k != "page_cursor" && k != "limit") || len(v) != 1 {
				writeError(w, publicerr.InvalidQuery, nil)
				return monitoring.OwnerFilter{}, "", 0, errResponseWritten
			}
		}
		if !q.Has("owner") {
			writeError(w, publicerr.InvalidQuery, nil)
			return monitoring.OwnerFilter{}, "", 0, errResponseWritten
		}
		// An empty owner is refused like any other invalid one, as on creation.
		ownerRef = q.Get("owner")
		if ownerRef == "" {
			writeError(w, publicerr.InvalidOwner, nil)
			return monitoring.OwnerFilter{}, "", 0, errResponseWritten
		}
		filter := monitoring.OwnerFilter{Owner: ownerRef}
		if ownerRef == monitoring.NoOwner {
			filter = monitoring.OwnerFilter{Global: true}
		}
		if q.Has("page_cursor") && q.Get("page_cursor") == "" {
			writeError(w, publicerr.InvalidCursor, nil)
			return monitoring.OwnerFilter{}, "", 0, errResponseWritten
		}
		limit, ok = pageLimit(w, q, 100, 100)
		if !ok {
			return monitoring.OwnerFilter{}, "", 0, errResponseWritten
		}
		if q.Has("page_cursor") {
			var err error
			if after, err = a.decodeSubscriptionPage(q.Get("page_cursor"), ownerRef, scope); errors.Is(err, errPageScope) {
				writeError(w, publicerr.CursorScopeChanged, nil)
				return monitoring.OwnerFilter{}, "", 0, errResponseWritten
			} else if err != nil {
				writeError(w, publicerr.InvalidCursor, nil)
				return monitoring.OwnerFilter{}, "", 0, errResponseWritten
			}
		}

		return filter, after, limit + 1, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
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
		writeError(w, publicerr.NotFound, nil)
		return
	}
	if r.Method != "POST" {
		writeError(w, publicerr.MethodNotAllowed, nil)
		return
	}
	var in monitoring.PreviewInput
	result, err := a.Monitoring.Preview(r.Context(), scope, monitoring.PreviewInput{}, func() (monitoring.PreviewInput, error) {
		if !a.decodeMonitoring(w, r, a.schemas["SubscriptionPreviewRequest"], &in) {
			return monitoring.PreviewInput{}, errResponseWritten
		}

		return in, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
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
