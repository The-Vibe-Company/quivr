package httpapi

import (
	"errors"
	"net/http"
	"sort"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

// handleListActivePlugins serves GET /v0/admin/active-plugins (THE-797): the
// plugins the active Pipeline Plan runs, for the operator views, behind
// observability:read. It names each plugin version and the roles it serves,
// and nothing that locates or configures it.
func (a *API) handleListActivePlugins(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	// The plan is the whole deployment's, so only a key for every Corpus
	// reads it, as for the stats.
	plan, err := a.Plugins.ActivePlugins(r.Context(), scope)
	if errors.Is(err, registry.ErrNoPlan) {
		send(w, 200, transport.ActivePluginList{Items: []transport.ActivePlugin{}})
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
		return
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
}
