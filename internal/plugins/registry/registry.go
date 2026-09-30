// Package registry keeps the list of plugins a deployment knows about and the
// active Pipeline Plan, which says which registered plugin serves each role
// (Spec 5). Quivr never starts a plugin: the operator runs it at an address,
// registers it, and Quivr checks it with the Contract Runner. Activating a
// checked registration records a new immutable plan, which api and worker
// follow without restarting. The startup configuration seeds the registry and
// applies the roles it changes (Reconcile).
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// Action is the operator permission for the plugin registry. Keys get it only
// when the configuration lists it; organization keys and the demo web app
// never do.
const Action = "plugins:admin"

// Registration states. A registration is registered while its check runs,
// then validated or rejected; the plan makes it active. A later plan that
// leaves it out makes it draining while work pinned to a plan that names it
// is unfinished, and inactive once none is.
const (
	StateRegistered = "registered"
	StateValidated  = "validated"
	StateActive     = "active"
	StateDraining   = "draining"
	StateInactive   = "inactive"
	StateRejected   = "rejected"
)

// Plan sources: the startup configuration, or an operator activation.
const (
	SourceConfiguration = "configuration"
	SourceActivation    = "activation"
)

var (
	// ErrNoPlan is returned when no Pipeline Plan is active: the registry was
	// never seeded because the configuration pins no plugin.
	ErrNoPlan = errors.New("no active pipeline plan")
	// ErrNotFound is an unknown registration or plan.
	ErrNotFound = errors.New("not_found")
	// ErrIdempotencyConflict is an idempotency key reused for another
	// registration.
	ErrIdempotencyConflict = errors.New("idempotency_conflict")
	// ErrInvalid is a registration request whose manifest or settings the
	// engine refuses; the error lists the issues.
	ErrInvalid = errors.New("invalid_plugin")
	// ErrNotValidated refuses to activate a registration the Contract Runner
	// has not certified.
	ErrNotValidated = errors.New("registration_not_validated")
	// ErrConflict refuses an activation that breaks a startup rule; the error
	// lists the issues.
	ErrConflict = errors.New("plugin_conflict")
	// ErrUnsupportedRole refuses to activate a plugin for a role that cannot
	// switch without a restart yet.
	ErrUnsupportedRole = errors.New("unsupported_role")
)

// IssueError carries the issues of an ErrInvalid or ErrConflict refusal.
type IssueError struct {
	Kind   error
	Issues []plugins.Issue
}

func (e *IssueError) Error() string {
	parts := make([]string, len(e.Issues))
	for i, issue := range e.Issues {
		parts[i] = fmt.Sprintf("%s %s: %s", issue.Code, issue.Path, issue.Message)
	}
	return strings.Join(parts, "; ")
}

func (e *IssueError) Unwrap() error { return e.Kind }

// Settings are how the deployment installs a plugin version, with the shape
// and meaning of a QUIVR_CONFIG pin: its configuration, the media types
// routed to its normalizer, the alert kinds it offers and its vector spaces.
type Settings struct {
	Configuration json.RawMessage       `json:"configuration"`
	Routes        []plugins.RouteConfig `json:"routes"`
	Kinds         []string              `json:"kinds,omitempty"`
	Spaces        map[string]string     `json:"spaces,omitempty"`
}

// SettingsOf are the resolved settings of a loaded pin.
func SettingsOf(pin *plugins.Pin) Settings {
	s := Settings{Configuration: canonicalJSON(pin.Configuration), Routes: pin.Routes(), Spaces: pin.Spaces}
	if s.Routes == nil {
		s.Routes = []plugins.RouteConfig{}
	}
	if pin.Kinds != nil {
		s.Kinds = append([]string{}, pin.Kinds...)
		sort.Strings(s.Kinds)
	}
	return s
}

// Digest identifies the settings: sha256 over their canonical JSON.
func (s Settings) Digest() string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func canonicalJSON(raw json.RawMessage) json.RawMessage {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return json.RawMessage(`{}`)
	}
	b, _ := json.Marshal(v)
	return b
}

// Registration is one plugin version the operator runs at an address, with
// the settings it is installed with.
type Registration struct {
	ID             string
	PluginID       string
	Version        string
	Endpoint       string
	ManifestDigest string
	// Manifest is the exact quivr-plugin.yaml the digest covers. Registrations
	// seeded before THE-781 have none and cannot serve a plan again.
	Manifest []byte
	Settings Settings
	// ArtifactDigest is the artifact digest the plugin reports, if any.
	ArtifactDigest string
	// Origin says what recorded the registration first: the startup
	// configuration or the operator API (KeyIdentity).
	Origin        string
	Contributions []string
	// Roles lists the roles the manifest declares it can serve.
	Roles []string
	State string
	// PinnedWork counts the unfinished work pinned to a plan that names the
	// registration: a registration that left the active plan is draining
	// until it reaches zero.
	PinnedWork int
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// Check is the Contract Runner's report, once the check ran.
	Check *CheckReport
}

// CheckReport is what the Contract Runner reported on a registration.
type CheckReport struct {
	Certified bool          `json:"certified"`
	CheckedAt time.Time     `json:"checked_at"`
	Passed    int           `json:"passed"`
	Failed    int           `json:"failed"`
	Skipped   int           `json:"skipped"`
	Checks    []CheckResult `json:"checks"`
}

// CheckResult is one check of the report.
type CheckResult struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Contribution string          `json:"contribution,omitempty"`
	Status       string          `json:"status"`
	Issues       []plugins.Issue `json:"issues"`
}

// Registration origins.
const (
	// OriginConfiguration is a registration the startup configuration
	// recorded.
	OriginConfiguration = "configuration"
	// OriginRegistration is a registration the operator API recorded.
	OriginRegistration = "registration"
)

// KeyIdentity is the registration's identity in the first component of every
// idempotency key of its invocations (Spec 2). A registration the
// configuration recorded keeps exactly the startup placeholder of its pin,
// so work in flight across the upgrade to the registry keeps converging.
// One the operator registered is named by its id, which covers its settings:
// two installs of one version with different configurations never share a
// key.
func (r Registration) KeyIdentity() string {
	if r.Origin == OriginRegistration {
		return "registration:" + r.ID
	}
	return plugins.StartupGeneration(r.PluginID, r.Version, r.ManifestDigest)
}

// Pin loads the registration as the engine calls it.
func (r Registration) Pin() (*plugins.Pin, error) {
	if len(r.Manifest) == 0 {
		return nil, &IssueError{Kind: ErrConflict, Issues: []plugins.Issue{{Code: CodePlanUnresolvable, Path: "/registrations/" + r.ID,
			Message: fmt.Sprintf("%s@%s was recorded before registrations kept their manifest; register it again", r.PluginID, r.Version)}}}
	}
	pin, err := plugins.LoadPinManifest(r.Manifest, "registration "+r.ID, plugins.PinConfig{Endpoint: r.Endpoint, Configuration: r.Settings.Configuration, Routes: r.Settings.Routes, Kinds: r.Settings.Kinds, Spaces: r.Settings.Spaces})
	if err != nil {
		return nil, err
	}
	pin.Registration, pin.KeyIdentity = r.ID, r.KeyIdentity()
	return pin, nil
}

// CodePlanUnresolvable is a plan member the engine cannot load.
const CodePlanUnresolvable = "plan_unresolvable"

// Assignment maps one role of the deployment to the registration serving it.
type Assignment struct {
	Role           string
	RegistrationID string
	// PluginID and Version name the registration for readers.
	PluginID string
	Version  string
}

// Plan is an immutable Pipeline Plan: every role and the registration that
// serves it, sorted by role.
type Plan struct {
	ID          string
	CreatedAt   time.Time
	ActivatedAt time.Time
	// Source says what recorded the plan: the startup configuration or an
	// operator activation.
	Source string
	Roles  []Assignment
}

// Seed is what the startup configuration declares: the registrations of the
// pinned plugins and the plan they form.
type Seed struct {
	Registrations []Registration
	Roles         []Assignment
}

// Store persists the registry.
type Store interface {
	// ApplyConfiguration records seed's registrations and applies to the
	// active plan the roles the configuration changed since it last applied
	// (Reconcile), in one transaction serialized with the other processes.
	ApplyConfiguration(ctx context.Context, seed Seed) (Applied, error)
	PluginRegistrations(ctx context.Context) ([]Registration, error)
	// PluginRegistration returns one registration with its manifest and
	// check report, or ErrNotFound.
	PluginRegistration(ctx context.Context, id string) (Registration, error)
	// ActivePlan returns ErrNoPlan when no plan is active.
	ActivePlan(ctx context.Context) (Plan, error)
	// PipelinePlan returns any plan, active or not, or ErrNotFound.
	PipelinePlan(ctx context.Context, id string) (Plan, error)
	// ActivePlanID is the active plan's id, or "" when none is.
	ActivePlanID(ctx context.Context) (string, error)
	// ActiveMembers returns the active plan and its registrations by id.
	ActiveMembers(ctx context.Context) (Plan, map[string]Registration, error)
	// PlanMembers returns any plan and its registrations by id, or
	// ErrNotFound.
	PlanMembers(ctx context.Context, id string) (Plan, map[string]Registration, error)
	// RegisterPlugin records a registration under an idempotency key: it
	// replays the key's registration, refuses a key used for another one
	// (ErrIdempotencyConflict), queues a new registration for its check and
	// re-queues a rejected one under a new key. queued reports a check to run.
	RegisterPlugin(ctx context.Context, r Registration, key string) (stored Registration, queued bool, err error)
	// ClaimCheck leases the oldest registration waiting for its check.
	ClaimCheck(ctx context.Context, lease time.Duration) (Registration, bool, error)
	// RecordCheck stores the report and moves a registered registration to
	// validated or rejected.
	RecordCheck(ctx context.Context, id string, report CheckReport) error
	// Activate records the plan decide returns for the target registration
	// and makes it active, with the vector spaces it registers, in one
	// transaction serialized with every other plan change.
	Activate(ctx context.Context, id string, decide func(active Plan, members map[string]Registration, target Registration) (Activation, error)) (Plan, error)
}

// Applied is what ApplyConfiguration did.
type Applied struct {
	// Plan is the active plan after it.
	Plan string
	Reconciliation
}

// Service is the operator API of the registry.
type Service struct {
	Store Store
	// Check runs the Contract Runner against a registration; nil uses
	// RunCheck.
	Check func(context.Context, Registration) CheckReport
	// Spaces lists the vector spaces a plugin set registers.
	Spaces func(*plugins.PinSet) []content.RegisteredSpace
	// Validate applies checks of the running engine to a candidate set (for
	// example connector kinds beside the built-in ones); nil checks nothing.
	Validate func(*plugins.PinSet) error
	// Wake nudges the checker after a registration is queued.
	Wake chan struct{}
	// Activated runs after an activation commits, so the process that served
	// it follows the new plan at once.
	Activated func(context.Context)
}

// Registrations lists every registration, oldest first.
func (s Service) Registrations(ctx context.Context, scope corpus.Scope) ([]Registration, error) {
	if !scope.Allows(Action) {
		return nil, corpus.ErrForbidden
	}
	return s.Store.PluginRegistrations(ctx)
}

// Registration returns one registration with its check report.
func (s Service) Registration(ctx context.Context, scope corpus.Scope, id string) (Registration, error) {
	if !scope.Allows(Action) {
		return Registration{}, corpus.ErrForbidden
	}
	return s.Store.PluginRegistration(ctx, id)
}

// ActivePlan returns the active Pipeline Plan.
func (s Service) ActivePlan(ctx context.Context, scope corpus.Scope) (Plan, error) {
	if !scope.Allows(Action) {
		return Plan{}, corpus.ErrForbidden
	}
	return s.Store.ActivePlan(ctx)
}

// PipelinePlan returns any recorded plan: plans are immutable, so earlier
// ones stay readable.
func (s Service) PipelinePlan(ctx context.Context, scope corpus.Scope, id string) (Plan, error) {
	if !scope.Allows(Action) {
		return Plan{}, corpus.ErrForbidden
	}
	return s.Store.PipelinePlan(ctx, id)
}

// Role names.
func normalizerRole(mediaType string) string  { return "normalizer:" + mediaType }
func subscriptionRole(pluginID string) string { return "subscription:" + pluginID }
func connectorRole(kind string) string        { return "connector:" + kind }

// ingestionRole is the one ingestion role of a deployment (Plugin API 0.6).
const ingestionRole = "ingestion"

// retrievalRole is the one retrieval role of a deployment (Plugin API 0.7).
const retrievalRole = "retrieval"

// RegistrationID identifies a plugin version at an address with its settings.
func RegistrationID(pluginID, version, manifestDigest, endpoint, settingsDigest string) string {
	return content.StableID("plugin_registration", pluginID, version, manifestDigest, endpoint, settingsDigest)
}

// registrationOf is the registration of a loaded pin, in state.
func registrationOf(pin *plugins.Pin, state string) Registration {
	m := pin.Manifest
	settings := SettingsOf(pin)
	return Registration{ID: RegistrationID(m.ID, m.Version, pin.ManifestDigest, pin.Endpoint, settings.Digest()), PluginID: m.ID, Version: m.Version, Endpoint: pin.Endpoint,
		ManifestDigest: pin.ManifestDigest, Manifest: pin.Source, Settings: settings, Contributions: m.Contributions.Names(), Roles: declaredRoles(m), State: state, Origin: OriginConfiguration}
}

// FromPins derives the seed of the startup pins: one active registration per
// pinned plugin, and a plan whose roles are the routed media types, the
// subscription evaluators, the connector kinds and the ingestion and
// retrieval roles the pins resolve to. A nil set declares nothing.
func FromPins(pins *plugins.PinSet) Seed {
	var seed Seed
	byPin := map[*plugins.Pin]Registration{}
	for _, pin := range pins.Pins() {
		r := registrationOf(pin, StateActive)
		byPin[pin] = r
		seed.Registrations = append(seed.Registrations, r)
	}
	seed.Roles = planRoles(pins, byPin)
	return seed
}

// planRoles lists the roles a resolved set serves, sorted, each naming the
// registration of its pin.
func planRoles(set *plugins.PinSet, byPin map[*plugins.Pin]Registration) []Assignment {
	roles := []Assignment{}
	for _, pin := range set.Pins() {
		if n := pin.Manifest.Contributions.Normalizer; n != nil {
			for _, mediaType := range n.MediaTypes {
				if pin.Routed(mediaType) {
					roles = append(roles, assign(normalizerRole(mediaType), byPin[pin]))
				}
			}
		}
	}
	for _, pin := range set.Evaluators() {
		roles = append(roles, assign(subscriptionRole(pin.Manifest.ID), byPin[pin]))
	}
	for _, pinned := range set.Connectors() {
		roles = append(roles, assign(connectorRole(pinned.Kind), byPin[pinned.Pin]))
	}
	if pin := set.Ingestion(); pin != nil {
		roles = append(roles, assign(ingestionRole, byPin[pin]))
	}
	if pin := set.Retrieval(); pin != nil {
		roles = append(roles, assign(retrievalRole, byPin[pin]))
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].Role < roles[j].Role })
	return roles
}

func (a Assignment) describe() string {
	if a.RegistrationID == "" {
		return ""
	}
	return a.PluginID + "@" + a.Version + " [" + a.RegistrationID + "]"
}

func assign(role string, r Registration) Assignment {
	return Assignment{Role: role, RegistrationID: r.ID, PluginID: r.PluginID, Version: r.Version}
}

// declaredRoles lists every role a manifest can serve, sorted.
func declaredRoles(m plugins.Manifest) []string {
	roles := []string{}
	if n := m.Contributions.Normalizer; n != nil {
		for _, mediaType := range n.MediaTypes {
			roles = append(roles, normalizerRole(mediaType))
		}
	}
	if m.Contributions.Subscription != nil {
		roles = append(roles, subscriptionRole(m.ID))
	}
	if c := m.Contributions.Connector; c != nil {
		for kind := range c.Kinds {
			roles = append(roles, connectorRole(kind))
		}
	}
	if m.Contributions.Ingestion != nil {
		roles = append(roles, ingestionRole)
	}
	if m.Contributions.Retrieval != nil {
		roles = append(roles, retrievalRole)
	}
	sort.Strings(roles)
	return roles
}
