package httpapi

import (
	"errors"
	"net/http"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// WithPlugins enables the operator reads of the plugin registry.
func WithPlugins(service registry.Service) Option {
	return func(a *API) { a.Plugins = service }
}

// pluginRoutes serves GET /v0/admin/plugins and GET /v0/admin/plugins/plan,
// both behind plugins:admin.
func (a *API) pluginRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	if r.URL.Path != "/v0/admin/plugins" && r.URL.Path != "/v0/admin/plugins/plan" {
		return false
	}
	switch {
	case r.Method != "GET":
		failure(w, 405, "method_not_allowed")
	case !scope.Allows(registry.Action):
		failure(w, 403, "forbidden")
	case a.Plugins.Store == nil:
		failure(w, 404, "not_found")
	case r.URL.Path == "/v0/admin/plugins":
		registrations, err := a.Plugins.Registrations(r.Context(), scope)
		if err != nil {
			pluginFailure(w, err)
			return true
		}
		out := transport.PluginRegistrationList{Items: make([]transport.PluginRegistration, 0, len(registrations))}
		for _, reg := range registrations {
			item := transport.PluginRegistration{RegistrationId: reg.ID, PluginId: reg.PluginID, Version: reg.Version, Endpoint: reg.Endpoint, ManifestDigest: reg.ManifestDigest, Contributions: nonNil(reg.Contributions), Roles: nonNil(reg.Roles), State: transport.PluginRegistrationState(reg.State), CreatedAt: reg.CreatedAt, UpdatedAt: reg.UpdatedAt}
			if reg.ArtifactDigest != "" {
				digest := reg.ArtifactDigest
				item.ArtifactDigest = &digest
			}
			out.Items = append(out.Items, item)
		}
		send(w, 200, out)
	default:
		plan, err := a.Plugins.ActivePlan(r.Context(), scope)
		if err != nil {
			pluginFailure(w, err)
			return true
		}
		out := transport.PipelinePlan{PlanId: plan.ID, CreatedAt: plan.CreatedAt, ActivatedAt: plan.ActivatedAt, Roles: make([]transport.PipelinePlanRole, 0, len(plan.Roles))}
		for _, role := range plan.Roles {
			out.Roles = append(out.Roles, transport.PipelinePlanRole{Role: role.Role, RegistrationId: role.RegistrationID, PluginId: role.PluginID, Version: role.Version})
		}
		send(w, 200, out)
	}
	return true
}

func pluginFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, corpus.ErrForbidden):
		failure(w, 403, "forbidden")
	case errors.Is(err, registry.ErrNoPlan):
		failure(w, 404, "not_found")
	default:
		failure(w, 503, "storage_unavailable")
	}
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
