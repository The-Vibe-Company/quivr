package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
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
	savedQuery, subscription, action *jsonschema.Schema
}

func monitoringFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, monitoring.ErrForbidden):
		failure(w, 403, "forbidden")
	case errors.Is(err, monitoring.ErrNotFound):
		failure(w, 404, "not_found")
	case errors.Is(err, monitoring.ErrConflict):
		failure(w, 409, "idempotency_conflict")
	case errors.Is(err, monitoring.ErrUnsupportedProfile), errors.Is(err, monitoring.ErrUnsupportedEvaluator),
		errors.Is(err, monitoring.ErrUnknownDestination), errors.Is(err, monitoring.ErrUnknownSavedQuery),
		errors.Is(err, monitoring.ErrTooLarge):
		failure(w, 422, publicCode(err, "invalid_input"))
	default:
		failure(w, 503, "storage_unavailable")
	}
}

// monitoringRoutes serves /v0/saved-queries and /v0/subscriptions.
func (a *API) monitoringRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
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
	case len(parts) == 0:
		method = "POST"
	case resource == "subscriptions" && len(parts) == 2 && (parts[1] == "disable" || parts[1] == "enable"):
		method = "POST"
	case len(parts) == 1, len(parts) == 3 && parts[1] == "versions":
	default:
		failure(w, 404, "not_found")
		return true
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
	case resource == "saved-queries":
		v, err := a.Monitoring.SavedQueryVersion(ctx, scope, parts[0], parts[2])
		respondMonitoring(w, 200, savedQueryVersionToTransport(v), err)
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
	case parts[1] == "disable" || parts[1] == "enable":
		var in struct {
			Key string `json:"idempotency_key"`
		}
		if !a.decodeMonitoring(w, r, a.monitoringSchemas.action, &in) {
			return true
		}
		toggle := a.Monitoring.DisableSubscription
		if parts[1] == "enable" {
			toggle = a.Monitoring.EnableSubscription
		}
		s, err := toggle(ctx, scope, in.Key, parts[0])
		respondMonitoring(w, 200, subscriptionToTransport(s), err)
	default:
		v, err := a.Monitoring.SubscriptionVersion(ctx, scope, parts[0], parts[2])
		respondMonitoring(w, 200, subscriptionVersionToTransport(v), err)
	}
	return true
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
	return transport.SavedQuery{SavedQueryId: q.ID, Name: q.Name, CurrentVersion: savedQueryVersionToTransport(q.Current)}
}

func subscriptionVersionToTransport(v monitoring.SubscriptionVersion) transport.SubscriptionVersion {
	return transport.SubscriptionVersion{SubscriptionId: v.SubscriptionID, VersionId: v.VersionID, SavedQueryId: v.SavedQueryID, SavedQueryVersionId: v.SavedQueryVersionID,
		Evaluator:     transport.EvaluatorConfig{PluginId: v.Evaluator.PluginID, Version: v.Evaluator.Version, Configuration: v.Evaluator.Configuration},
		DestinationId: v.DestinationID}
}

func subscriptionToTransport(s monitoring.Subscription) transport.Subscription {
	return transport.Subscription{SubscriptionId: s.ID, Name: s.Name, Enabled: s.Enabled, CurrentVersion: subscriptionVersionToTransport(s.Current)}
}
