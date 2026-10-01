package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// WithPlugins enables the operator routes of the plugin registry.
func WithPlugins(service registry.Service) Option {
	return func(a *API) { a.Plugins = service }
}

const pluginsPath = "/v0/admin/plugins"

// pluginRoutes serves the plugin registry, every route behind plugins:admin:
// GET and POST /v0/admin/plugins, GET /v0/admin/plugins/plan,
// POST /v0/admin/plugins/plan/rollback, GET /v0/admin/plugins/plans,
// GET /v0/admin/plugins/plans/{plan_id}, GET /v0/admin/plugins/{registration_id}
// and POST /v0/admin/plugins/{registration_id}/activate.
func (a *API) pluginRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	rest, ok := strings.CutPrefix(r.URL.Path, pluginsPath)
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
		return false
	}
	rest = strings.TrimPrefix(rest, "/")
	segments := strings.Split(rest, "/")
	method := "GET"
	switch {
	case rest == "":
		if r.Method == "POST" {
			method = "POST"
		}
	case len(segments) == 2 && segments[0] == "plans" && segments[1] != "":
	case rest == "plan/rollback":
		method = "POST"
	case len(segments) == 2 && segments[1] == "activate" && segments[0] != "":
		method = "POST"
	case len(segments) == 1:
	default:
		return false
	}
	switch {
	case r.Method != method:
		failure(w, 405, "method_not_allowed")
	case !scope.Allows(registry.Action):
		failure(w, 403, "forbidden")
	case a.Plugins.Store == nil:
		failure(w, 404, "not_found")
	case rest == "" && method == "POST":
		a.registerPlugin(w, r, scope)
	case rest == "":
		registrations, err := a.Plugins.Registrations(r.Context(), scope)
		if err != nil {
			pluginFailure(w, err)
			return true
		}
		out := transport.PluginRegistrationList{Items: make([]transport.PluginRegistration, 0, len(registrations))}
		for _, reg := range registrations {
			reg.Check = nil
			out.Items = append(out.Items, registrationToTransport(reg))
		}
		send(w, 200, out)
	case rest == "plan":
		plan, err := a.Plugins.ActivePlan(r.Context(), scope)
		sendPlan(w, plan, err)
	case rest == "plan/rollback":
		a.rollbackPlan(w, r, scope)
	case rest == "plans":
		a.listPlans(w, r, scope)
	case segments[0] == "plans":
		plan, err := a.Plugins.PipelinePlan(r.Context(), scope, segments[1])
		sendPlan(w, plan, err)
	case len(segments) == 2:
		plan, err := a.Plugins.Activate(r.Context(), scope, segments[0])
		sendPlan(w, plan, err)
	default:
		reg, err := a.Plugins.Registration(r.Context(), scope, segments[0])
		if err != nil {
			pluginFailure(w, err)
			return true
		}
		send(w, 200, registrationToTransport(reg))
	}
	return true
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
	if !decodeIntoAtMost(w, r, maxPluginRegistrationBytes, a.pluginSchema, &in) {
		return
	}
	reg, err := a.Plugins.Register(r.Context(), scope, registry.Request{Key: in.IdempotencyKey, Manifest: []byte(in.Manifest), Endpoint: in.Endpoint,
		Configuration: in.Configuration, Routes: in.Routes, Kinds: in.Kinds, Spaces: in.Spaces, Fixtures: in.Fixtures})
	if err != nil {
		pluginFailure(w, err)
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
	if !decodeInto(w, r, a.pluginRollbackSchema, &in) {
		return
	}
	plan, err := a.Plugins.Rollback(r.Context(), scope, registry.RollbackRequest{Key: in.IdempotencyKey, Plan: in.PlanID, PinnedWork: in.PinnedWork})
	if err == nil {
		slog.Info("pipeline plan rolled back", "plan", plan.ID, "previous", plan.PreviousPlanID, "pinned_work", in.PinnedWork)
	}
	sendPlan(w, plan, err)
}

// maxPlanList bounds one read of the plan history; earlier plans stay
// reachable through each plan's previous_plan_id.
const maxPlanList = 100

func (a *API) listPlans(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPlanList {
			failure(w, 422, "invalid_limit")
			return
		}
		limit = n
	}
	plans, err := a.Plugins.PipelinePlans(r.Context(), scope, limit)
	if err != nil {
		pluginFailure(w, err)
		return
	}
	out := transport.PipelinePlanList{Items: make([]transport.PipelinePlan, 0, len(plans))}
	for _, plan := range plans {
		out.Items = append(out.Items, planToTransport(plan))
	}
	send(w, 200, out)
}

func registrationToTransport(reg registry.Registration) transport.PluginRegistration {
	item := transport.PluginRegistration{RegistrationId: reg.ID, PluginId: reg.PluginID, Version: reg.Version, Endpoint: reg.Endpoint, ManifestDigest: reg.ManifestDigest, Contributions: nonNil(reg.Contributions), Roles: nonNil(reg.Roles), State: transport.PluginRegistrationState(reg.State), PinnedWork: reg.PinnedWork, CreatedAt: reg.CreatedAt, UpdatedAt: reg.UpdatedAt}
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
		pluginFailure(w, err)
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

// pluginFailure maps registry errors; refusals that list issues carry them
// in the message.
func pluginFailure(w http.ResponseWriter, err error) {
	detailed := func(status int, code string) {
		e := apiError(status, code)
		e.Message = err.Error()
		send(w, status, e)
	}
	switch {
	case errors.Is(err, corpus.ErrForbidden):
		failure(w, 403, "forbidden")
	case errors.Is(err, registry.ErrNoPlan), errors.Is(err, registry.ErrNotFound):
		failure(w, 404, "not_found")
	case errors.Is(err, registry.ErrIdempotencyConflict):
		failure(w, 409, "idempotency_conflict")
	case errors.Is(err, registry.ErrInvalid):
		detailed(422, "invalid_plugin")
	case errors.Is(err, registry.ErrUnsupportedRole):
		detailed(422, "unsupported_role")
	case errors.Is(err, registry.ErrNotValidated):
		detailed(409, "registration_not_validated")
	case errors.Is(err, registry.ErrConflict):
		detailed(409, "plugin_conflict")
	case errors.Is(err, registry.ErrUnreachable):
		detailed(409, "plugin_unreachable")
	case errors.Is(err, registry.ErrNoPreviousPlan):
		failure(w, 409, "no_previous_plan")
	case errors.Is(err, content.ErrSpaceOwner), errors.Is(err, content.ErrSpaceChanged):
		// The vector space registry refuses the plan's spaces.
		detailed(409, "plugin_conflict")
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
