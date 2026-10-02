package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// WithPlugins enables the operator routes of the plugin registry.
func WithPlugins(service registry.Service) Option {
	return func(a *API) { a.Plugins = service }
}

const pluginsPath = "/v0/admin/plugins"

func (a *API) handleListPluginRegistrations(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	registrations, err := a.Plugins.Registrations(r.Context(), scope)
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
		return
	}
	out := transport.PluginRegistrationList{Items: make([]transport.PluginRegistration, 0, len(registrations))}
	for _, reg := range registrations {
		reg.Check = nil
		out.Items = append(out.Items, registrationToTransport(reg))
	}
	send(w, 200, out)
}

func (a *API) handleGetActivePipelinePlan(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	plan, err := a.Plugins.ActivePlan(r.Context(), scope)
	sendPlan(w, plan, err)
}

func (a *API) handleGetPipelinePlan(w http.ResponseWriter, r *http.Request, scope corpus.Scope, planID string) {
	if planID == "" {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	plan, err := a.Plugins.PipelinePlan(r.Context(), scope, planID)
	sendPlan(w, plan, err)
}

func (a *API) handleGetPluginRegistration(w http.ResponseWriter, r *http.Request, scope corpus.Scope, registrationID string) {
	if registrationID == "" {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	reg, err := a.Plugins.Registration(r.Context(), scope, registrationID)
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
		return
	}
	send(w, 200, registrationToTransport(reg))
}

func (a *API) handleActivatePlugin(w http.ResponseWriter, r *http.Request, scope corpus.Scope, registrationID string) {
	if registrationID == "" {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	plan, err := a.Plugins.Activate(r.Context(), scope, registrationID)
	sendPlan(w, plan, err)
}

// pluginRegistrationRequest is the registration command; configuration stays
// raw for the manifest's configuration schema.
type pluginRegistrationRequest struct {
	IdempotencyKey string                `json:"idempotency_key"`
	Endpoint       string                `json:"endpoint"`
	Manifest       string                `json:"manifest"`
	Configuration  json.RawMessage       `json:"configuration"`
	Routes         []plugins.RouteConfig `json:"routes"`
	Kinds          []string              `json:"kinds"`
	Spaces         map[string]string     `json:"spaces"`
	// Fixtures are base64 in JSON.
	Fixtures map[string][]byte `json:"fixtures"`
}

// maxPluginRegistrationBytes bounds a registration request: its fixtures,
// base64-encoded, and its manifest.
const maxPluginRegistrationBytes = 8 << 20

func (a *API) registerPlugin(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	var in pluginRegistrationRequest
	reg, err := a.Plugins.Register(r.Context(), scope, registry.Request{}, func() (registry.Request, error) {
		if a.Plugins.Store == nil {
			writeError(w, publicerr.NotFound, nil)
			return registry.Request{}, errResponseWritten
		}
		if !decodeIntoAtMost(w, r, maxPluginRegistrationBytes, a.schemas["PluginRegistrationRequest"], &in) {
			return registry.Request{}, errResponseWritten
		}

		return registry.Request{Key: in.IdempotencyKey, Manifest: []byte(in.Manifest), Endpoint: in.Endpoint,
			Configuration: in.Configuration, Routes: in.Routes, Kinds: in.Kinds, Spaces: in.Spaces, Fixtures: in.Fixtures}, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
		return
	}
	w.Header().Set("Location", pluginsPath+"/"+reg.ID)
	send(w, 202, registrationToTransport(reg))
}

// pluginRollbackRequest is the rollback command.
type pluginRollbackRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	PlanID         string `json:"plan_id"`
	PinnedWork     string `json:"pinned_work"`
}

func (a *API) rollbackPlan(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	var in pluginRollbackRequest
	plan, err := a.Plugins.Rollback(r.Context(), scope, registry.RollbackRequest{}, func() (registry.RollbackRequest, error) {
		if !decodeInto(w, r, a.schemas["PipelinePlanRollbackRequest"], &in) {
			return registry.RollbackRequest{}, errResponseWritten
		}

		return registry.RollbackRequest{Key: in.IdempotencyKey, Plan: in.PlanID, PinnedWork: in.PinnedWork}, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err == nil {
		slog.Info("pipeline plan rolled back", "plan", plan.ID, "previous", plan.PreviousPlanID, "pinned_work", in.PinnedWork)
	}
	sendPlan(w, plan, err)
}

// maxPlanList bounds one read of the plan history; earlier plans stay
// reachable through each plan's previous_plan_id.
const maxPlanList = 100

func (a *API) listPlans(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	plans, err := a.Plugins.PipelinePlans(r.Context(), scope, 0, func() (int, error) {
		limit, ok := pageLimit(w, r.URL.Query(), 20, maxPlanList)
		if !ok {
			return 0, errResponseWritten
		}

		return limit, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
		return
	}
	out := transport.PipelinePlanList{Items: make([]transport.PipelinePlan, 0, len(plans))}
	for _, plan := range plans {
		out.Items = append(out.Items, planToTransport(plan))
	}
	send(w, 200, out)
}

func registrationToTransport(reg registry.Registration) transport.PluginRegistration {
	item := transport.PluginRegistration{RegistrationId: reg.ID, PluginId: reg.PluginID, Version: reg.Version, Endpoint: reg.Endpoint, ManifestDigest: reg.ManifestDigest, Contributions: nonNil(reg.Contributions), Roles: nonNil(reg.Roles), State: transport.PluginRegistrationState(reg.State), PinnedWork: reg.PinnedWork, Subscriptions: reg.Subscriptions, CreatedAt: reg.CreatedAt, UpdatedAt: reg.UpdatedAt}
	if reg.ArtifactDigest != "" {
		digest := reg.ArtifactDigest
		item.ArtifactDigest = &digest
	}
	if c := reg.Check; c != nil {
		report := transport.PluginCheckReport{Certified: c.Certified, CheckedAt: c.CheckedAt, Passed: c.Passed, Failed: c.Failed, Skipped: c.Skipped, Checks: make([]transport.PluginCheck, 0, len(c.Checks))}
		for _, check := range c.Checks {
			out := transport.PluginCheck{Id: check.ID, Title: check.Title, Status: transport.PluginCheckStatus(check.Status), Fixture: optionalString(check.Fixture), Issues: make([]transport.PluginIssue, 0, len(check.Issues))}
			if check.Contribution != "" {
				contribution := check.Contribution
				out.Contribution = &contribution
			}
			for _, issue := range check.Issues {
				out.Issues = append(out.Issues, transport.PluginIssue{Code: issue.Code, Message: issue.Message, Path: optionalString(issue.Path)})
			}
			report.Checks = append(report.Checks, out)
		}
		item.Check = &report
	}
	return item
}

func sendPlan(w http.ResponseWriter, plan registry.Plan, err error) {
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
		return
	}
	send(w, 200, planToTransport(plan))
}

func planToTransport(plan registry.Plan) transport.PipelinePlan {
	out := transport.PipelinePlan{PlanId: plan.ID, CreatedAt: plan.CreatedAt, ActivatedAt: plan.ActivatedAt, Source: transport.PipelinePlanSource(plan.Source), PreviousPlanId: optionalString(plan.PreviousPlanID), Roles: make([]transport.PipelinePlanRole, 0, len(plan.Roles))}
	if out.Source == "" {
		out.Source = transport.PipelinePlanSource(registry.SourceConfiguration)
	}
	for _, role := range plan.Roles {
		out.Roles = append(out.Roles, transport.PipelinePlanRole{Role: role.Role, RegistrationId: role.RegistrationID, PluginId: role.PluginID, Version: role.Version})
	}
	return out
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
