package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/devhost"
)

// What happens to work pinned to a registration a rollback retires.
const (
	// PinnedWorkDrain lets the work finish on the version it started on.
	PinnedWorkDrain = "drain"
	// PinnedWorkStop stops the work at its next call to that version, with
	// the diagnostic plugins.CodePinnedPlanStopped.
	PinnedWorkStop = "stop"
)

// CodeUnreachable is the issue of a registration a rollback would bring back
// that does not answer discovery with its manifest.
const CodeUnreachable = "plugin_unreachable"

// RollbackRequest makes an earlier plan's roles active again.
type RollbackRequest struct {
	// Key is the idempotency key of the request.
	Key string
	// Plan is the plan to return to; "" is the plan the active one replaced.
	Plan string
	// PinnedWork is PinnedWorkDrain ("" too) or PinnedWorkStop.
	PinnedWork string
}

// Stop reports that work pinned to the abandoned version stops.
func (r RollbackRequest) Stop() bool { return r.PinnedWork == PinnedWorkStop }

// PlanRollback computes the plan that restores target's roles on top of the
// active plan: the registrations of target serve again, every other member
// of the active plan leaves it. The result must satisfy the rules of an
// activation: the startup rules, the running engine's checks (validate), the
// same retrieval role presence, and unchanged alert-rule roles.
func PlanRollback(active, target Plan, members map[string]Registration, validate func(*plugins.PinSet) error) (Activation, error) {
	if sameRoles(active.Roles, target.Roles) {
		return Activation{Roles: active.Roles, Unchanged: true}, nil
	}
	for _, a := range target.Roles {
		switch r, ok := members[a.RegistrationID]; {
		case !ok:
		case r.State == StateRegistered || r.State == StateRejected:
			return Activation{}, fmt.Errorf("%w: %s@%s is %s", ErrNotValidated, r.PluginID, r.Version, r.State)
		}
	}
	set, byPin, err := Resolve(target.Roles, members)
	if err != nil {
		return Activation{}, err
	}
	roles := planRoles(set, byPin)
	if !sameRoles(roles, target.Roles) {
		return Activation{}, &IssueError{Kind: ErrConflict, Issues: []plugins.Issue{{Code: CodePlanUnresolvable, Path: "/plans/" + target.ID,
			Message: "the plan's plugins no longer resolve to the roles it recorded; activate the version you want instead"}}}
	}
	if hasRole(roles, retrievalRole) != hasRole(active.Roles, retrievalRole) {
		return Activation{}, &IssueError{Kind: ErrConflict, Issues: []plugins.Issue{{Code: plugins.CodeRetrievalConflict, Path: "/contributions/retrieval",
			Message: "this rollback would add or remove the retrieval role; a running search switches its retrieval plugin but only starts or stops using one after a restart, so pin it in the configuration"}}}
	}
	if !sameRoles(subscriptionRoles(active.Roles), subscriptionRoles(roles)) {
		return Activation{}, fmt.Errorf("%w: this rollback would change an alert rule (subscription) plugin; each Subscription pins its rule's version, so switch alert rules in the configuration", ErrUnsupportedRole)
	}
	if validate != nil {
		if err := validate(set); err != nil {
			return Activation{}, &IssueError{Kind: ErrConflict, Issues: issuesOf(err, "")}
		}
	}
	a := Activation{Roles: roles, Set: set}
	kept, joined := map[string]bool{}, map[string]bool{}
	for _, r := range roles {
		kept[r.RegistrationID] = true
	}
	for _, r := range active.Roles {
		if !kept[r.RegistrationID] && !joined[r.RegistrationID] {
			a.Retired = append(a.Retired, r.RegistrationID)
		}
		joined[r.RegistrationID] = true
	}
	for _, r := range roles {
		if !joined[r.RegistrationID] {
			joined[r.RegistrationID] = true
			a.Returning = append(a.Returning, members[r.RegistrationID])
		}
	}
	return a, nil
}

func subscriptionRoles(roles []Assignment) []Assignment {
	var out []Assignment
	for _, a := range roles {
		if strings.HasPrefix(a.Role, "subscription:") {
			out = append(out, a)
		}
	}
	return out
}

// Rollback makes an earlier plan's roles active again as a new immutable
// plan, after checking that every registration it brings back answers at its
// endpoint. Nothing already produced is rewritten.
func (s Service) Rollback(ctx context.Context, scope corpus.Scope, req RollbackRequest) (Plan, error) {
	if !scope.Allows(Action) {
		return Plan{}, corpus.ErrForbidden
	}
	if req.PinnedWork == "" {
		// The default, stored as such so a retry that names it replays.
		req.PinnedWork = PinnedWorkDrain
	}
	reach := s.Reach
	if reach == nil {
		reach = Discover
	}
	plan, err := s.Store.Rollback(ctx, req, func(active, target Plan, members map[string]Registration) (Activation, error) {
		a, err := PlanRollback(active, target, members, s.Validate)
		if err != nil || a.Unchanged {
			return a, err
		}
		var issues []plugins.Issue
		for _, r := range a.Returning {
			if err := reach(ctx, r); err != nil {
				issues = append(issues, plugins.Issue{Code: CodeUnreachable, Path: "/registrations/" + r.ID,
					Message: fmt.Sprintf("%s@%s at %s: %v; start it again, or roll back to another plan", r.PluginID, r.Version, r.Endpoint, err)})
			}
		}
		if len(issues) > 0 {
			return Activation{}, &IssueError{Kind: ErrUnreachable, Issues: issues}
		}
		if s.Spaces != nil {
			a.Spaces = s.Spaces(a.Set)
		}
		return a, nil
	})
	if err == nil && s.Activated != nil {
		s.Activated(ctx)
	}
	return plan, err
}

// Discover checks that the plugin at a registration's endpoint answers
// discovery as the build of its manifest.
func Discover(ctx context.Context, r Registration) error {
	pin, err := r.Pin()
	if err != nil {
		return err
	}
	_, issues, err := devhost.Discover(ctx, r.Endpoint, plugins.Report{Path: "its registration", ManifestDigest: r.ManifestDigest, Manifest: &pin.Manifest})
	if err != nil {
		return err
	}
	if len(issues) > 0 {
		return &IssueError{Kind: ErrUnreachable, Issues: issues}
	}
	return nil
}
