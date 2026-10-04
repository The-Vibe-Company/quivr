// Package registry keeps the list of plugins a deployment knows about and the
// active Pipeline Plan, which says which registered plugin serves each role
// (Spec 5). Quivr never starts a plugin: the operator runs it at an address,
// registers it, and Quivr checks it with the Contract Runner. Activating a
// checked registration records a new immutable plan, which api and worker
// follow without restarting. The startup configuration seeds the registry and
// applies the roles it changes (Reconcile).
package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
)

// Action is the operator permission for the plugin registry. Keys get it only
// when the configuration lists it; organization keys and the demo web app
// never do.
const Action = "plugins:admin"

// Registration states. A registration is registered while its check runs,
// then validated or rejected; the plan makes it active. A later plan that
// leaves it out makes it draining while work pinned to a plan that names it
// is unfinished, or Subscriptions still pin its alert rule, and inactive once
// none is.
const (
	StateRegistered = "registered"
	StateValidated  = "validated"
	StateActive     = "active"
	StateDraining   = "draining"
	StateInactive   = "inactive"
	StateRejected   = "rejected"
)

// Plan sources: the startup configuration, an operator activation, or an
// operator rollback.
const (
	SourceConfiguration = "configuration"
	SourceActivation    = "activation"
	SourceRollback      = "rollback"
)

var (
	// ErrNoPlan is returned when no Pipeline Plan is active: the registry was
	// never seeded because the configuration pins no plugin.
	ErrNoPlan = errors.New("no active pipeline plan")
	// ErrNotFound is an unknown registration or plan.
	ErrNotFound = publicerr.NotFound
	// ErrIdempotencyConflict is an idempotency key reused for another
	// registration.
	ErrIdempotencyConflict = publicerr.IdempotencyConflict
	// ErrInvalid is a registration request whose manifest or settings the
	// engine refuses; the error lists the issues.
	ErrInvalid = publicerr.InvalidPlugin
	// ErrNotValidated refuses to activate a registration the Contract Runner
	// has not certified.
	ErrNotValidated = publicerr.RegistrationNotValidated
	// ErrConflict refuses an activation that breaks a startup rule; the error
	// lists the issues.
	ErrConflict = publicerr.PluginConflict
	// ErrUnreachable refuses a rollback to a plugin that does not answer
	// discovery with its manifest; the error lists the issues.
	ErrUnreachable = publicerr.PluginUnreachable
	// ErrNoPreviousPlan refuses a rollback when no earlier plan exists.
	ErrNoPreviousPlan = publicerr.NoPreviousPlan
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
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&v) != nil {
		return json.RawMessage(`{}`)
	}
	b, _ := json.Marshal(canonicalNumbers(v))
	return b
}

func canonicalNumbers(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			v[key] = canonicalNumbers(item)
		}
	case []any:
		for i, item := range v {
			v[i] = canonicalNumbers(item)
		}
	case json.Number:
		return canonicalNumber(v)
	}
	return value
}

// Normalize decimal spelling without float64 rounding. JSONB may expand an
// exponent or retain fractional zeros. Keep exponent arithmetic symbolic so
// even a large exponent never expands into a large allocation.
func canonicalNumber(number json.Number) json.Number {
	text := string(number)
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(text, "-")
	mantissa, exponent, hasExponent := strings.Cut(strings.ToLower(text), "e")
	power := new(big.Int)
	if hasExponent {
		power.SetString(exponent, 10)
	}
	whole, fraction, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(whole+fraction, "0")
	trimmed := strings.TrimRight(digits, "0")
	if trimmed == "" {
		return json.Number("0")
	}
	power.Add(power, big.NewInt(int64(len(digits)-len(trimmed)-len(fraction))))
	digits = trimmed
	exp := new(big.Int).Add(power, big.NewInt(int64(len(digits)-1)))
	if exp.IsInt64() && exp.Int64() >= -6 && exp.Int64() < 21 {
		point := int(exp.Int64()) + 1
		switch {
		case point <= 0:
			text = "0." + strings.Repeat("0", -point) + digits
		case point >= len(digits):
			text = digits + strings.Repeat("0", point-len(digits))
		default:
			text = digits[:point] + "." + digits[point:]
		}
	} else {
		text = digits[:1]
		if len(digits) > 1 {
			text += "." + digits[1:]
		}
		text += "e"
		if exp.Sign() > 0 {
			text += "+"
		}
		text += exp.String()
	}
	if negative {
		text = "-" + text
	}
	return json.Number(text)
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
	// registration, and for an alert-rule plugin the pending evaluations of
	// Subscription Versions that pin its version and the earlier such
	// Versions that changes not dispatched yet may reach: a registration that left
	// the active plan is draining until it and Subscriptions reach zero.
	PinnedWork int
	// Subscriptions counts, for an alert-rule plugin, the Subscriptions that
	// are not deleted whose current Version pins its version: they keep it
	// until an operator migrates them (THE-805).
	Subscriptions int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// Check is the Contract Runner's report, once the check ran.
	Check *CheckReport
	// Fixtures are the plugin's own test files the check runs, by path in
	// its fixtures folder. Only RegisterPlugin and ClaimCheck carry them.
	Fixtures map[string][]byte
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
	ID           string `json:"id"`
	Title        string `json:"title"`
	Contribution string `json:"contribution,omitempty"`
	Status       string `json:"status"`
	// Fixture names the fixture the check ran, if any.
	Fixture string          `json:"fixture,omitempty"`
	Issues  []plugins.Issue `json:"issues"`
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
	// Source says what recorded the plan: the startup configuration, an
	// operator activation or a rollback.
	Source string
	// PreviousPlanID is the plan this one replaced, "" for the first.
	PreviousPlanID string
	Roles          []Assignment
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
	// PipelinePlans returns the latest limit plans, newest first.
	PipelinePlans(ctx context.Context, limit int) ([]Plan, error)
	// ActivePlanID is the active plan's id, or "" when none is.
	ActivePlanID(ctx context.Context) (string, error)
	// ActiveMembers returns the active plan and its registrations by id.
	ActiveMembers(ctx context.Context) (Plan, map[string]Registration, error)
	// PlanMembers returns any plan and its registrations by id, or
	// ErrNotFound.
	PlanMembers(ctx context.Context, id string) (Plan, map[string]Registration, error)
	// EvaluatorRegistrations returns every registration a plan named for an
	// alert-rule role, ordered by the latest plan naming each, oldest first:
	// the evaluators Subscription Versions may still pin.
	EvaluatorRegistrations(ctx context.Context) ([]Registration, error)
	// RegisterPlugin records a registration under an idempotency key: it
	// replays the key's registration, refuses a key used for another one or
	// with other fixtures (ErrIdempotencyConflict), queues a new registration
	// for its check with r's fixtures, and re-queues a rejected one under a
	// new key with that request's fixtures. queued reports a check to run.
	RegisterPlugin(ctx context.Context, r Registration, key string) (stored Registration, queued bool, err error)
	// ClaimCheck leases the oldest registration waiting for its check, with
	// the fixtures it is checked with.
	ClaimCheck(ctx context.Context, lease time.Duration) (Registration, bool, error)
	// RecordCheck stores the report and moves a registered registration to
	// validated or rejected.
	RecordCheck(ctx context.Context, id string, report CheckReport) error
	// Activate records the plan decide returns for the target registration
	// and makes it active, with the vector spaces it registers, in one
	// transaction serialized with every other plan change.
	Activate(ctx context.Context, id string, decide func(active Plan, members map[string]Registration, target Registration) (Activation, error)) (Plan, error)
	// Rollback records the plan decide returns for the rollback target, the
	// request's plan or the plan the active one replaced, with its vector
	// spaces, in one transaction serialized with every other plan change.
	// The request's key replays the plan it recorded; the key with another
	// request is ErrIdempotencyConflict. members holds the registrations of
	// both plans. With Stop, work pinned to a plan naming a registration that
	// leaves the plan is marked stopped.
	Rollback(ctx context.Context, req RollbackRequest, decide func(active, target Plan, members map[string]Registration) (Activation, error)) (Plan, error)
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
	// Activated runs after an activation or a rollback commits, so the
	// process that served it follows the new plan at once.
	Activated func(context.Context)
	// Reach checks that a registration a rollback brings back answers at its
	// endpoint; nil uses Discover.
	Reach func(context.Context, Registration) error
}

// Registrations lists every registration, oldest first.
func (s Service) Registrations(ctx context.Context, scope corpus.Scope) ([]Registration, error) {
	if err := scope.Require(corpus.ActionPluginRegistrations); err != nil {
		return nil, err
	}
	if s.Store == nil {
		return nil, ErrNotFound
	}
	return s.Store.PluginRegistrations(ctx)
}

// Registration returns one registration with its check report.
func (s Service) Registration(ctx context.Context, scope corpus.Scope, id string) (Registration, error) {
	if err := scope.Require(corpus.ActionPluginRegistration); err != nil {
		return Registration{}, err
	}
	if s.Store == nil {
		return Registration{}, ErrNotFound
	}
	return s.Store.PluginRegistration(ctx, id)
}

// ActivePlan returns the active Pipeline Plan.
func (s Service) ActivePlan(ctx context.Context, scope corpus.Scope) (Plan, error) {
	if err := scope.Require(corpus.ActionPluginActivePlan); err != nil {
		return Plan{}, err
	}
	if s.Store == nil {
		return Plan{}, ErrNotFound
	}
	plan, err := s.Store.ActivePlan(ctx)
	if errors.Is(err, ErrNoPlan) {
		// An operator asked for the active plan as a resource. Internal
		// consumers still distinguish ErrNoPlan when initializing a pipeline.
		err = fmt.Errorf("%w: %w", publicerr.NotFound, err)
	}
	return plan, err
}

// ActivePlugins returns the active plan for the operator views, behind
// observability:read on every Corpus rather than plugins:admin: callers
// expose only its roles and the plugin versions serving them, never a
// registration's address, settings or manifest.
func (s Service) ActivePlugins(ctx context.Context, scope corpus.Scope) (Plan, error) {
	if err := scope.Require(corpus.ActionPluginActivePlugins); err != nil {
		return Plan{}, err
	}
	if s.Store == nil {
		return Plan{}, ErrNotFound
	}
	return s.Store.ActivePlan(ctx)
}

// PipelinePlan returns any recorded plan: plans are immutable, so earlier
// ones stay readable.
func (s Service) PipelinePlan(ctx context.Context, scope corpus.Scope, id string) (Plan, error) {
	if err := scope.Require(corpus.ActionPluginPipelinePlan); err != nil {
		return Plan{}, err
	}
	if s.Store == nil {
		return Plan{}, ErrNotFound
	}
	return s.Store.PipelinePlan(ctx, id)
}

// PipelinePlans returns the latest limit plans, newest first: the history of
// plan changes, each naming the plan it replaced and what recorded it.
func (s Service) PipelinePlans(ctx context.Context, scope corpus.Scope, limit int, prepare ...func() (int, error)) ([]Plan, error) {
	if err := scope.Require(corpus.ActionPluginPipelinePlans); err != nil {
		return nil, err
	}
	if s.Store == nil {
		return nil, ErrNotFound
	}
	for _, load := range prepare {
		var err error
		limit, err = load()
		if err != nil {
			return nil, err
		}
	}
	return s.Store.PipelinePlans(ctx, limit)
}

// Role names.
func normalizerRole(mediaType string) string  { return "normalizer:" + mediaType }
func subscriptionRole(pluginID string) string { return "subscription:" + pluginID }
func connectorRole(kind string) string        { return "connector:" + kind }

// ingestionRole is the canonical default ingestion assignment. The old
// singleton role remains readable through canonicalRoles below.
const ingestionRole = "ingestion-default"
const legacyIngestionRole = "ingestion"
const ingestionMembershipPrefix = "ingestion:"
const ingestionRoutePrefix = "ingestion-route:"
const ingestionEvaluationPrefix = "ingestion-evaluation:"

// retrievalRole identifies the retrieval provider independently of its version.
func retrievalRole(pluginID string) string { return "retrieval:" + pluginID }

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
// subscription evaluators, the connector kinds, the keyed ingestion
// memberships plus default/source/evaluation routes, and the retrieval role
// the pins resolve to. A nil set declares nothing.
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
	for _, pin := range set.Ingestions() {
		roles = append(roles, assign(ingestionMembershipRole(pin.Manifest.ID), byPin[pin]))
	}
	if pin := set.Ingestion(); pin != nil {
		roles = append(roles, assign(ingestionRole, byPin[pin]))
	}
	routing := set.IngestionRouting()
	for mediaType := range routing.Routes {
		if pin := set.IngestionFor(mediaType); pin != nil {
			roles = append(roles, assign(ingestionRouteRole(mediaType), byPin[pin]))
		}
	}
	evaluationMediaTypes := sortedKeys(routing.Evaluation)
	for _, mediaType := range evaluationMediaTypes {
		pluginIDs := append([]string(nil), routing.Evaluation[mediaType]...)
		sort.Strings(pluginIDs)
		for _, pluginID := range pluginIDs {
			for _, pin := range set.EvaluationFor(mediaType) {
				if pin.Manifest.ID == pluginID {
					roles = append(roles, assign(ingestionEvaluationRole(mediaType, pluginID), byPin[pin]))
					break
				}
			}
		}
	}
	for _, pin := range set.Retrievals() {
		roles = append(roles, assign(retrievalRole(pin.Manifest.ID), byPin[pin]))
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

func ingestionMembershipRole(pluginID string) string { return ingestionMembershipPrefix + pluginID }
func ingestionRouteRole(mediaType string) string     { return ingestionRoutePrefix + mediaType }
func ingestionEvaluationRole(mediaType, pluginID string) string {
	return ingestionEvaluationPrefix + mediaType + ":" + pluginID
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
		roles = append(roles, ingestionMembershipRole(m.ID))
	}
	if m.Contributions.Retrieval != nil {
		roles = append(roles, retrievalRole(m.ID))
	}
	sort.Strings(roles)
	return roles
}
