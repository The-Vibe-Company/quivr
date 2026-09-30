package httpapi

import (
	"errors"
	"net/http"
	"sort"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// activePluginRoutes serves GET /v0/admin/active-plugins (THE-797): the
// plugins the active Pipeline Plan runs, for the operator views, behind
// observability:read. It names each plugin version and the roles it serves,
// and nothing that locates or configures it.
func (a *API) activePluginRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	if r.URL.Path != "/v0/admin/active-plugins" {
		return false
	}
	if r.Method != "GET" {
		failure(w, 405, "method_not_allowed")
		return true
	}
	// The plan is the whole deployment's, so only a key for every Corpus
	// reads it, as for the stats.
	if !scope.Allows(content.ObservabilityRead) || !scope.AllCorpora() {
		failure(w, 403, "forbidden")
		return true
	}
	if a.Plugins.Store == nil {
		failure(w, 404, "not_found")
		return true
	}
	plan, err := a.Plugins.ActivePlugins(r.Context(), scope)
	if errors.Is(err, registry.ErrNoPlan) {
		send(w, 200, transport.ActivePluginList{Items: []transport.ActivePlugin{}})
		return true
	}
	if err != nil {
		pluginFailure(w, err)
		return true
	}
	byVersion := map[[2]string]*transport.ActivePlugin{}
	for _, role := range plan.Roles {
		key := [2]string{role.PluginID, role.Version}
		if byVersion[key] == nil {
			byVersion[key] = &transport.ActivePlugin{PluginId: role.PluginID, Version: role.Version, Roles: []string{}}
		}
		byVersion[key].Roles = append(byVersion[key].Roles, role.Role)
	}
	activated := plan.ActivatedAt
	out := transport.ActivePluginList{PlanActivatedAt: &activated, Items: make([]transport.ActivePlugin, 0, len(byVersion))}
	for _, item := range byVersion {
		out.Items = append(out.Items, *item)
	}
	sort.Slice(out.Items, func(i, j int) bool {
		if out.Items[i].PluginId != out.Items[j].PluginId {
			return out.Items[i].PluginId < out.Items[j].PluginId
		}
		return out.Items[i].Version < out.Items[j].Version
	})
	send(w, 200, out)
	return true
}
