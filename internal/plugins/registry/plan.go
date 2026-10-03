package registry

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

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
	routing := ingestionRouting(roles, registrations)
	return resolveMembers(members, &routing)
}

// resolveMembers loads registrations and routes them with the startup rules.
func resolveMembers(members []Registration, routing *plugins.IngestionRouting) (*plugins.PinSet, map[*plugins.Pin]Registration, error) {
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
	if routing != nil {
		if err := set.ConfigureIngestion(*routing); err != nil {
			return nil, nil, &IssueError{Kind: ErrConflict, Issues: issuesOf(err, "")}
		}
	}
	if _, err := plugins.PinsExtensionRegistry(set); err != nil {
		return nil, nil, &IssueError{Kind: ErrConflict, Issues: issuesOf(err, "")}
	}
	return set, byPin, nil
}

// ingestionRouting reconstructs the explicit source-media choices stored in a
// plan. Both the canonical default and the old "ingestion" role select the
// default; keyed membership roles only keep a plugin in the plan and do not
// choose it for new Versions. Evaluation routes retain the media type from
// the role and use the assignment's PluginID to remove the plugin suffix
// safely.
func ingestionRouting(roles []Assignment, registrations map[string]Registration) plugins.IngestionRouting {
	routing := plugins.IngestionRouting{Routes: map[string]string{}, Evaluation: map[string][]string{}}
	for _, a := range roles {
		if !isIngestionRoutingRole(a.Role) {
			continue
		}
		pluginID := a.PluginID
		if pluginID == "" {
			pluginID = registrations[a.RegistrationID].PluginID
		}
		switch {
		case a.Role == ingestionRole || a.Role == legacyIngestionRole:
			routing.Default = pluginID
		case strings.HasPrefix(a.Role, ingestionRoutePrefix):
			routing.Routes[strings.TrimPrefix(a.Role, ingestionRoutePrefix)] = pluginID
		case strings.HasPrefix(a.Role, ingestionEvaluationPrefix):
			role := strings.TrimPrefix(a.Role, ingestionEvaluationPrefix)
			suffix := ":" + pluginID
			if pluginID == "" || !strings.HasSuffix(role, suffix) {
				continue
			}
			mediaType := strings.TrimSuffix(role, suffix)
			routing.Evaluation[mediaType] = append(routing.Evaluation[mediaType], pluginID)
		}
	}
	if len(routing.Routes) == 0 {
		routing.Routes = nil
	}
	if len(routing.Evaluation) == 0 {
		routing.Evaluation = nil
	} else {
		for mediaType := range routing.Evaluation {
			sort.Strings(routing.Evaluation[mediaType])
		}
	}
	return routing
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
// normalizer per media type, one provider per connector kind, the configured
// ingestion memberships and routes, the retrieval providers, and extension
// namespace ownership); a registration target overlaps only in part is a
// conflict. validate adds the checks of the running engine.
func PlanActivation(active Plan, members map[string]Registration, target Registration, validate func(*plugins.PinSet) error) (Activation, error) {
	if target.State == StateActive {
		return promoteEvaluation(active, members, target, validate)
	}
	switch target.State {
	case StateValidated, StateInactive, StateDraining:
	default:
		return Activation{}, fmt.Errorf("%w: %s@%s is %s; only a validated registration can be activated", ErrNotValidated, target.PluginID, target.Version, target.State)
	}
	pin, err := target.Pin()
	if err != nil {
		return Activation{}, &IssueError{Kind: ErrConflict, Issues: issuesOf(err, "/registrations/"+target.ID)}
	}
	takes := map[string]bool{}
	// Membership roles do not invent ingestion defaults or source routes;
	// upgrades redirect only the routes already naming this plugin below.
	for _, role := range declaredRoles(pin.Manifest) {
		if isIngestionRoutingRole(role) {
			continue
		}
		if strings.HasPrefix(role, "normalizer:") && !pin.Routed(strings.TrimPrefix(role, "normalizer:")) {
			continue
		}
		takes[role] = true
	}
	if len(takes) == 0 {
		return Activation{}, &IssueError{Kind: ErrConflict, Issues: []plugins.Issue{{Code: plugins.CodeInvalidPin, Path: "/routes", Message: fmt.Sprintf("%s@%s would serve no role: route at least one media type to its normalizer", target.PluginID, target.Version)}}}
	}
	served := map[string][]string{}
	canonicalActiveRoles := canonicalRoles(active.Roles, members)
	for _, a := range canonicalActiveRoles {
		if _, ok := members[a.RegistrationID]; !ok {
			return Activation{}, &IssueError{Kind: ErrConflict, Issues: []plugins.Issue{{Code: CodePlanUnresolvable, Path: "/registrations/" + a.RegistrationID, Message: "the active plan names a registration the registry does not have"}}}
		}
	}
	hasTargetIngestion := pin.Manifest.Contributions.Ingestion != nil
	for _, a := range canonicalActiveRoles {
		m := members[a.RegistrationID]
		if IsIngestionRole(a.Role) && m.PluginID == target.PluginID && !hasTargetIngestion {
			return Activation{}, &IssueError{Kind: ErrConflict, Issues: []plugins.Issue{{Code: plugins.CodeInvalidPin, Path: "/contributions/ingestion", Message: fmt.Sprintf("%s@%s removes the ingestion Contribution still used by the active default or source routes", target.PluginID, target.Version)}}}
		}
	}
	for _, a := range canonicalActiveRoles {
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
		// An unrelated ingestion registration remains a member even when the
		// new plugin happens to provide every non-ingestion role it serves.
		if !replaced && !registrationHasIngestion(m) {
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
	routing := ingestionRouting(canonicalActiveRoles, members)
	set, byPin, err := resolveMembers(append(kept, target), &routing)
	if err != nil {
		return Activation{}, err
	}

	if validate != nil {
		if err := validate(set); err != nil {
			return Activation{}, &IssueError{Kind: ErrConflict, Issues: issuesOf(err, "")}
		}
	}
	return Activation{Roles: planRoles(set, byPin), Set: set, Retired: retired}, nil
}

// promoteEvaluation activates the formats on which an installed registration
// is being evaluated. Memberships and old projections remain available.
func promoteEvaluation(active Plan, members map[string]Registration, target Registration, validate func(*plugins.PinSet) error) (Activation, error) {
	routing := ingestionRouting(active.Roles, members)
	changed := false
	for mediaType, owners := range routing.Evaluation {
		for i, owner := range owners {
			if owner != target.PluginID {
				continue
			}
			previous := routing.Routes[mediaType]
			if previous == "" {
				previous = routing.Default
			}
			if routing.Routes == nil {
				routing.Routes = map[string]string{}
			}
			routing.Routes[mediaType] = target.PluginID
			owners = append(owners[:i:i], owners[i+1:]...)
			if previous != "" && previous != target.PluginID {
				owners = append(owners, previous)
			}
			sort.Strings(owners)
			routing.Evaluation[mediaType] = owners
			changed = true
			break
		}
	}
	if !changed {
		return Activation{Roles: active.Roles, Unchanged: true}, nil
	}
	var kept []Registration
	seen := map[string]bool{}
	for _, a := range active.Roles {
		if !seen[a.RegistrationID] {
			kept = append(kept, members[a.RegistrationID])
			seen[a.RegistrationID] = true
		}
	}
	set, byPin, err := resolveMembers(kept, &routing)
	if err != nil {
		return Activation{}, err
	}
	if validate != nil {
		if err = validate(set); err != nil {
			return Activation{}, &IssueError{Kind: ErrConflict, Issues: issuesOf(err, "")}
		}
	}
	return Activation{Roles: planRoles(set, byPin), Set: set}, nil
}

// IsIngestionRole recognizes keyed memberships, source and evaluation routes,
// and both canonical and legacy defaults.
func IsIngestionRole(role string) bool {
	return role == ingestionRole || role == legacyIngestionRole || strings.HasPrefix(role, ingestionMembershipPrefix) || strings.HasPrefix(role, ingestionRoutePrefix) || strings.HasPrefix(role, ingestionEvaluationPrefix)
}

func isIngestionRoutingRole(role string) bool {
	return role == ingestionRole || role == legacyIngestionRole || strings.HasPrefix(role, ingestionRoutePrefix) || strings.HasPrefix(role, ingestionEvaluationPrefix)
}

func registrationHasIngestion(r Registration) bool {
	for _, contribution := range r.Contributions {
		if contribution == "ingestion" {
			return true
		}
	}
	for _, role := range r.Roles {
		if IsIngestionRole(role) {
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
	if err := scope.Require(corpus.ActionPluginActivate); err != nil {
		return Plan{}, err
	}
	if s.Store == nil {
		return Plan{}, ErrNotFound
	}
	plan, err := s.Store.Activate(ctx, id, func(active Plan, members map[string]Registration, target Registration) (Activation, error) {
		a, err := PlanActivation(active, members, target, s.Validate)
		if err == nil && target.State == StateActive && !a.Unchanged {
			reach := s.Reach
			if reach == nil {
				reach = Discover
			}
			if err = reach(ctx, target); err != nil {
				return Activation{}, &IssueError{Kind: ErrUnreachable, Issues: []plugins.Issue{{Code: CodeUnreachable, Path: "/registrations/" + target.ID, PluginID: target.PluginID, PluginVersion: target.Version, Cause: discoveryCause(err), Message: "start the evaluation plugin before promoting it to served"}}}
			}
		}
		if err == nil && !a.Unchanged && s.Spaces != nil {
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
	configuredRoles := canonicalRoles(configured.Roles, registrations)
	activeRoles := []Assignment(nil)
	originalActiveRoles := []Assignment(nil)
	if active != nil {
		originalActiveRoles = active.Roles
		activeRoles = canonicalRoles(active.Roles, registrations)
	}
	snapshot = canonicalSnapshot(snapshot, registrations)
	whole := func(reason string) Reconciliation {
		r := Reconciliation{Roles: configuredRoles, Whole: reason != "", Reason: reason}
		r.Changed = active == nil || !sameRoles(originalActiveRoles, configuredRoles)
		if active != nil && r.Changed && snapshot != nil {
			r.Overridden = overrides(snapshot, activeRoles, configuredRoles)
		}
		return r
	}
	if active == nil || snapshot == nil {
		return whole("")
	}
	want := map[string]Assignment{}
	for _, a := range configuredRoles {
		want[a.Role] = a
	}
	retrievalSnapshotCount := 0
	for role, id := range snapshot {
		if role == retrievalRole(registrations[id].PluginID) {
			retrievalSnapshotCount++
		}
	}
	merged := map[string]Assignment{}
	// A legacy activation could swap the singleton to another plugin. Once
	// configuration explicitly includes that provider, restore missing
	// configured providers beside it. Normalize both inputs first: an older
	// process can write a singleton plan after the snapshot was migrated. A
	// complete multi-provider snapshot preserves operator rollbacks unchanged.
	restoreRetrieval := false
	for _, a := range activeRoles {
		merged[a.Role] = a
		if retrievalSnapshotCount == 1 && a.Role == retrievalRole(a.PluginID) {
			if _, configured := want[a.Role]; configured {
				restoreRetrieval = true
			}
		}
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
			if !restoreRetrieval || role != retrievalRole(c.PluginID) || merged[role].RegistrationID != "" {
				continue
			}
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
	return Reconciliation{Roles: out, Changed: !sameRoles(originalActiveRoles, out), Overridden: overridden}
}

// canonicalRoles gives plans one role vocabulary while keeping old plans
// readable. The old singleton ingestion role becomes an explicit canonical
// default and every default or source route also supplies the keyed
// membership needed to retain that plugin in a multi-ingestion plan.
func canonicalRoles(roles []Assignment, registrations map[string]Registration) []Assignment {
	type candidate struct {
		assignment Assignment
		priority   int
	}
	byRole := map[string]candidate{}
	add := func(a Assignment, priority int) {
		if r, ok := registrations[a.RegistrationID]; ok {
			if a.PluginID == "" {
				a.PluginID = r.PluginID
			}
			if a.Version == "" {
				a.Version = r.Version
			}
		}
		switch a.Role {
		case legacyIngestionRole:
			a.Role = ingestionRole
			priority = 1
		case "retrieval":
			a.Role = retrievalRole(a.PluginID)
		}
		if previous, ok := byRole[a.Role]; !ok || priority >= previous.priority {
			byRole[a.Role] = candidate{assignment: a, priority: priority}
		}
	}
	for _, a := range roles {
		priority := 1
		if a.Role == ingestionRole {
			priority = 2
		}
		add(a, priority)
	}
	for _, original := range roles {
		if !isIngestionRoutingRole(original.Role) {
			continue
		}
		a := original
		if r, ok := registrations[a.RegistrationID]; ok {
			if a.PluginID == "" {
				a.PluginID = r.PluginID
			}
			if a.Version == "" {
				a.Version = r.Version
			}
		}
		if a.PluginID == "" {
			continue
		}
		membership := ingestionMembershipRole(a.PluginID)
		if _, exists := byRole[membership]; !exists {
			a.Role = membership
			byRole[membership] = candidate{assignment: a, priority: 1}
		}
	}
	out := make([]Assignment, 0, len(byRole))
	for _, role := range sortedKeys(byRole) {
		out = append(out, byRole[role].assignment)
	}
	return out
}

// canonicalSnapshot applies the same role vocabulary to the startup snapshot
// used by Reconcile. A legacy default is retained as the explicit canonical
// default, and its registration is added as a membership when absent.
func canonicalSnapshot(snapshot map[string]string, registrations map[string]Registration) map[string]string {
	if snapshot == nil {
		return nil
	}
	roles := make([]Assignment, 0, len(snapshot))
	for role, id := range snapshot {
		roles = append(roles, Assignment{Role: role, RegistrationID: id})
	}
	canonical := canonicalRoles(roles, registrations)
	result := make(map[string]string, len(canonical))
	for _, assignment := range canonical {
		result[assignment.Role] = assignment.RegistrationID
	}
	return result
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

// DefaultIngestion names the default registration of a recorded plan. Legacy
// singleton plans and keyed singleton/core.ingest plans remain readable.
func DefaultIngestion(plan Plan) string {
	var sole string
	seen := map[string]bool{}
	var core string
	for _, a := range plan.Roles {
		if a.Role == ingestionRole || a.Role == legacyIngestionRole {
			return a.RegistrationID
		}
		if strings.HasPrefix(a.Role, ingestionMembershipPrefix) {
			seen[a.RegistrationID] = true
			sole = a.RegistrationID
			if a.PluginID == "core.ingest" {
				core = a.RegistrationID
			}
		}
	}
	if core != "" {
		return core
	}
	if len(seen) == 1 {
		return sole
	}
	return ""
}

// HasIngestion reports whether the registration is an ingestion member.
func HasIngestion(plan Plan, id string) bool {
	for _, a := range plan.Roles {
		if a.RegistrationID == id && IsIngestionRole(a.Role) {
			return true
		}
	}
	return false
}
