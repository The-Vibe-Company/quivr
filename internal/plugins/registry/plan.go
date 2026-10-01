package registry

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// Resolve loads the registrations of a plan as the engine calls them: every
// member once, routed with the startup rules. Registrations must hold every
// member the roles name.
func Resolve(roles []Assignment, registrations map[string]Registration) (*plugins.PinSet, map[*plugins.Pin]Registration, error) {
	var ids []string
	seen := map[string]bool{}
	for _, a := range roles {
		if !seen[a.RegistrationID] {
			seen[a.RegistrationID] = true
			ids = append(ids, a.RegistrationID)
		}
	}
	sort.Strings(ids)
	var members []Registration
	for _, id := range ids {
		r, ok := registrations[id]
		if !ok {
			return nil, nil, &IssueError{Kind: ErrConflict, Issues: []plugins.Issue{{Code: CodePlanUnresolvable, Path: "/registrations/" + id, Message: "the plan names a registration the registry does not have"}}}
		}
		members = append(members, r)
	}
	return resolveMembers(members)
}

// resolveMembers loads registrations and routes them with the startup rules.
func resolveMembers(members []Registration) (*plugins.PinSet, map[*plugins.Pin]Registration, error) {
	pins := make([]*plugins.Pin, 0, len(members))
	byPin := map[*plugins.Pin]Registration{}
	var issues []plugins.Issue
	for _, r := range members {
		pin, err := r.Pin()
		if err != nil {
			issues = append(issues, issuesOf(err, "/registrations/"+r.ID)...)
			continue
		}
		pins = append(pins, pin)
		byPin[pin] = r
	}
	if len(issues) > 0 {
		return nil, nil, &IssueError{Kind: ErrConflict, Issues: issues}
	}
	set, err := plugins.NewPinSet(pins)
	if err != nil {
		return nil, nil, &IssueError{Kind: ErrConflict, Issues: issuesOf(err, "")}
	}
	if _, err := plugins.PinsExtensionRegistry(set); err != nil {
		return nil, nil, &IssueError{Kind: ErrConflict, Issues: issuesOf(err, "")}
	}
	return set, byPin, nil
}

// issuesOf lists the issues of a pin refusal; paths of a set name pins by
// position, so messages keep the plugin they concern.
func issuesOf(err error, prefix string) []plugins.Issue {
	var ie *IssueError
	if errors.As(err, &ie) {
		return ie.Issues
	}
	var pe *plugins.PinError
	if errors.As(err, &pe) {
		out := make([]plugins.Issue, len(pe.Issues))
		for i, issue := range pe.Issues {
			issue.Path = prefix + issue.Path
			out[i] = issue
		}
		return out
	}
	return []plugins.Issue{{Code: CodePlanUnresolvable, Path: prefix, Message: err.Error()}}
}

// Activation is the plan an activation records.
type Activation struct {
	// Roles are the new plan's roles.
	Roles []Assignment
	// Set is the new plan resolved.
	Set *plugins.PinSet
	// Retired are the registrations that leave the plan.
	Retired []string
	// Spaces are the vector spaces the new plan registers.
	Spaces []content.RegisteredSpace
	// Returning are the registrations a rollback brings back into the plan.
	Returning []Registration
	// Unchanged reports a rollback to the active plan's roles: nothing is
	// recorded.
	Unchanged bool
}

// PlanActivation computes the plan that activates target on top of the active
// plan: every other version of the same plugin leaves it, and so does every
// registration whose roles target takes over entirely; target joins with
// every role it declares. For an alert-rule plugin, new Subscription Versions
// pin target from then on, while those pinning the version that left keep it
// until an operator migrates them (THE-805). The result must satisfy the startup rules (one
// normalizer per media type, one provider per connector kind, one ingestion
// and one retrieval plugin, extension namespace ownership); a registration
// target overlaps only in part is a conflict. validate adds the checks of
// the running engine.
func PlanActivation(active Plan, members map[string]Registration, target Registration, validate func(*plugins.PinSet) error) (Activation, error) {
	switch target.State {
	case StateValidated, StateInactive, StateDraining:
	default:
		return Activation{}, fmt.Errorf("%w: %s@%s is %s; only a validated registration can be activated", ErrNotValidated, target.PluginID, target.Version, target.State)
	}
	pin, err := target.Pin()
	if err != nil {
		return Activation{}, &IssueError{Kind: ErrConflict, Issues: issuesOf(err, "/registrations/"+target.ID)}
	}
	alone, err := plugins.NewPinSet([]*plugins.Pin{pin})
	if err != nil {
		return Activation{}, &IssueError{Kind: ErrConflict, Issues: issuesOf(err, "")}
	}
	takes := map[string]bool{}
	for _, a := range planRoles(alone, map[*plugins.Pin]Registration{pin: target}) {
		takes[a.Role] = true
	}
	if len(takes) == 0 {
		return Activation{}, &IssueError{Kind: ErrConflict, Issues: []plugins.Issue{{Code: plugins.CodeInvalidPin, Path: "/routes", Message: fmt.Sprintf("%s@%s would serve no role: route at least one media type to its normalizer", target.PluginID, target.Version)}}}
	}
	served := map[string][]string{}
	for _, a := range active.Roles {
		served[a.RegistrationID] = append(served[a.RegistrationID], a.Role)
	}
	var kept []Registration
	var retired []string
	for _, id := range sortedKeys(served) {
		m, ok := members[id]
		if !ok {
			return Activation{}, &IssueError{Kind: ErrConflict, Issues: []plugins.Issue{{Code: CodePlanUnresolvable, Path: "/registrations/" + id, Message: "the active plan names a registration the registry does not have"}}}
		}
		replaced := m.PluginID == target.PluginID
		if !replaced {
			replaced = true
			for _, role := range served[id] {
				replaced = replaced && takes[role]
			}
		}
		if replaced {
			retired = append(retired, id)
		} else {
			kept = append(kept, m)
		}
	}
	set, byPin, err := resolveMembers(append(kept, target))
	if err != nil {
		return Activation{}, err
	}
	if (set.Retrieval() == nil) != !hasRole(active.Roles, retrievalRole) {
		return Activation{}, &IssueError{Kind: ErrConflict, Issues: []plugins.Issue{{Code: plugins.CodeRetrievalConflict, Path: "/contributions/retrieval",
			Message: "this activation would add or remove the retrieval role; a running search switches its retrieval plugin but only starts or stops using one after a restart, so pin it in the configuration"}}}
	}
	if validate != nil {
		if err := validate(set); err != nil {
			return Activation{}, &IssueError{Kind: ErrConflict, Issues: issuesOf(err, "")}
		}
	}
	return Activation{Roles: planRoles(set, byPin), Set: set, Retired: retired}, nil
}

func hasRole(roles []Assignment, role string) bool {
	for _, a := range roles {
		if a.Role == role {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Activate makes a validated registration serve every role it declares, as a
// new immutable plan. The previous plan stays readable.
func (s Service) Activate(ctx context.Context, scope corpus.Scope, id string) (Plan, error) {
	if !scope.Allows(Action) {
		return Plan{}, corpus.ErrForbidden
	}
	plan, err := s.Store.Activate(ctx, id, func(active Plan, members map[string]Registration, target Registration) (Activation, error) {
		a, err := PlanActivation(active, members, target, s.Validate)
		if err == nil && s.Spaces != nil {
			a.Spaces = s.Spaces(a.Set)
		}
		return a, err
	})
	if err == nil && s.Activated != nil {
		s.Activated(ctx)
	}
	return plan, err
}

// Override is a role an operator activation had set that a configuration
// change replaces.
type Override struct {
	Role string
	// Active and Configured name the replaced and the configured registration.
	Active, Configured string
}

// Reconciliation is how the startup configuration changes the active plan.
type Reconciliation struct {
	// Roles are the plan's roles after the configuration applies.
	Roles []Assignment
	// Changed reports a new plan: Roles differ from the active plan's.
	Changed bool
	// Overridden lists the roles an operator activation had set and the
	// configuration replaces.
	Overridden []Override
	// Whole reports that the configuration replaced the plan whole, because
	// the roles it changed could not be merged with the others.
	Whole bool
	// Reason explains Whole.
	Reason string
}

// Reconcile applies the startup configuration to the active plan, role by
// role, against the configuration that last applied (snapshot; nil when none
// was recorded). A role the configuration changed since then is added,
// replaced or removed; a role it did not change keeps the plan's value, so an
// operator activation survives restarts. Without a snapshot, or without an
// active plan, the configuration's plan applies whole. When the merged plan
// breaks a startup rule, the configuration's plan applies whole too.
// registrations must hold the active plan's members and the configured ones.
func Reconcile(configured Seed, snapshot map[string]string, active *Plan, registrations map[string]Registration) Reconciliation {
	whole := func(reason string) Reconciliation {
		r := Reconciliation{Roles: configured.Roles, Whole: reason != "", Reason: reason}
		r.Changed = active == nil || !sameRoles(active.Roles, configured.Roles)
		if active != nil && r.Changed && snapshot != nil {
			r.Overridden = overrides(snapshot, active.Roles, configured.Roles)
		}
		return r
	}
	if active == nil || snapshot == nil {
		return whole("")
	}
	want := map[string]Assignment{}
	for _, a := range configured.Roles {
		want[a.Role] = a
	}
	merged := map[string]Assignment{}
	for _, a := range active.Roles {
		merged[a.Role] = a
	}
	roles := map[string]bool{}
	for role := range want {
		roles[role] = true
	}
	for role := range snapshot {
		roles[role] = true
	}
	var overridden []Override
	for _, role := range sortedKeys(roles) {
		c, configuredNow := want[role]
		before, configuredBefore := snapshot[role]
		if configuredNow == configuredBefore && c.RegistrationID == before {
			continue
		}
		current, set := merged[role]
		if configuredNow {
			if set && current.RegistrationID != c.RegistrationID && current.RegistrationID != before {
				overridden = append(overridden, Override{Role: role, Active: current.describe(), Configured: c.describe()})
			}
			merged[role] = c
		} else if set && current.RegistrationID == before {
			delete(merged, role)
		}
	}
	out := make([]Assignment, 0, len(merged))
	for _, role := range sortedKeys(merged) {
		out = append(out, merged[role])
	}
	set, byPin, err := Resolve(out, registrations)
	if err != nil {
		return whole(err.Error())
	}
	if derived := planRoles(set, byPin); !sameRoles(derived, out) {
		return whole("a plugin would keep only part of the roles it declares")
	}
	return Reconciliation{Roles: out, Changed: !sameRoles(active.Roles, out), Overridden: overridden}
}

// overrides lists the roles the operator had changed since the snapshot and
// the configuration replaces.
func overrides(snapshot map[string]string, active, configured []Assignment) []Override {
	want := map[string]Assignment{}
	for _, a := range configured {
		want[a.Role] = a
	}
	var out []Override
	for _, a := range active {
		c := want[a.Role]
		if a.RegistrationID != snapshot[a.Role] && a.RegistrationID != c.RegistrationID {
			out = append(out, Override{Role: a.Role, Active: a.describe(), Configured: c.describe()})
		}
	}
	return out
}

func sameRoles(a, b []Assignment) bool {
	key := func(roles []Assignment) map[string]string {
		m := map[string]string{}
		for _, r := range roles {
			m[r.Role] = r.RegistrationID
		}
		return m
	}
	return reflect.DeepEqual(key(a), key(b))
}

// Snapshot is the role → registration map a configuration applies, stored to
// tell later which roles it changed.
func (s Seed) Snapshot() map[string]string {
	m := map[string]string{}
	for _, a := range s.Roles {
		m[a.Role] = a.RegistrationID
	}
	return m
}
