// Package registry keeps the list of plugins a deployment knows about and the
// active Pipeline Plan, which says which registered plugin serves each role
// (THE-780, Spec 5 slice 1). Quivr never starts a plugin: the operator runs it
// at an address, and the registry records it. In this slice the registry is
// seeded from the startup pins and only read; the engine still resolves
// plugins from the configuration.
package registry

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// Action is the operator permission for the plugin registry. Keys get it only
// when the configuration lists it; organization keys and the demo web app
// never do.
const Action = "plugins:admin"

// Registration states.
const (
	StateRegistered = "registered"
	StateValidated  = "validated"
	StateActive     = "active"
	StateDraining   = "draining"
	StateInactive   = "inactive"
	StateRejected   = "rejected"
)

// ErrNoPlan is returned when no Pipeline Plan is active: the registry was
// never seeded because the configuration pins no plugin.
var ErrNoPlan = errors.New("no active pipeline plan")

// Registration is one plugin version the operator runs at an address.
type Registration struct {
	ID             string
	PluginID       string
	Version        string
	Endpoint       string
	ManifestDigest string
	// ArtifactDigest is the artifact digest the plugin reports, if any.
	ArtifactDigest string
	Contributions  []string
	// Roles lists the roles the manifest declares it can serve.
	Roles     []string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

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
	Roles       []Assignment
}

// Seed is what the startup configuration declares: the registrations of the
// pinned plugins and the plan they form.
type Seed struct {
	Registrations []Registration
	Roles         []Assignment
}

// Difference is one role on which the configuration and the active plan
// disagree. Configured and Active name the registration on each side as
// plugin@version [registration id]; empty means the role is absent there.
type Difference struct {
	Role       string
	Configured string
	Active     string
}

// Store persists the registry.
type Store interface {
	// SeedPlugins writes seed as active registrations and the first active
	// plan, in one transaction, when the registry has no registration, and
	// reports whether it did. Otherwise it writes nothing.
	SeedPlugins(ctx context.Context, seed Seed) (seeded bool, err error)
	PluginRegistrations(ctx context.Context) ([]Registration, error)
	// ActivePlan returns ErrNoPlan when no plan is active.
	ActivePlan(ctx context.Context) (Plan, error)
}

// Service reads the registry for operators.
type Service struct {
	Store Store
}

// Registrations lists every registration, oldest first.
func (s Service) Registrations(ctx context.Context, scope corpus.Scope) ([]Registration, error) {
	if !scope.Allows(Action) {
		return nil, corpus.ErrForbidden
	}
	return s.Store.PluginRegistrations(ctx)
}

// ActivePlan returns the active Pipeline Plan.
func (s Service) ActivePlan(ctx context.Context, scope corpus.Scope) (Plan, error) {
	if !scope.Allows(Action) {
		return Plan{}, corpus.ErrForbidden
	}
	return s.Store.ActivePlan(ctx)
}

// SeedFrom seeds an empty registry from the startup configuration. On a
// registry that is not empty it changes nothing and returns how the
// configuration differs from the active plan, for the caller to warn about.
func (s Service) SeedFrom(ctx context.Context, seed Seed) (seeded bool, differences []Difference, err error) {
	if seeded, err = s.Store.SeedPlugins(ctx, seed); err != nil || seeded {
		return seeded, nil, err
	}
	active, err := s.Store.ActivePlan(ctx)
	if err != nil && !errors.Is(err, ErrNoPlan) {
		return false, nil, err
	}
	return false, Compare(seed, active), nil
}

// Role names.
func normalizerRole(mediaType string) string  { return "normalizer:" + mediaType }
func subscriptionRole(pluginID string) string { return "subscription:" + pluginID }
func connectorRole(kind string) string        { return "connector:" + kind }

// ingestionRole is the one ingestion role of a deployment (Plugin API 0.6).
const ingestionRole = "ingestion"

// retrievalRole is the one retrieval role of a deployment (Plugin API 0.7).
const retrievalRole = "retrieval"

// RegistrationID identifies a plugin version at an address.
func RegistrationID(pluginID, version, manifestDigest, endpoint string) string {
	return content.StableID("plugin_registration", pluginID, version, manifestDigest, endpoint)
}

// FromPins derives the seed of the startup pins: one active registration per
// pinned plugin, and a plan whose roles are the routed media types, the
// subscription evaluators and the connector kinds the pins resolve to. A nil
// set declares nothing.
func FromPins(pins *plugins.PinSet) Seed {
	var seed Seed
	byPin := map[*plugins.Pin]Registration{}
	for _, pin := range pins.Pins() {
		m := pin.Manifest
		r := Registration{ID: RegistrationID(m.ID, m.Version, pin.ManifestDigest, pin.Endpoint), PluginID: m.ID, Version: m.Version, Endpoint: pin.Endpoint, ManifestDigest: pin.ManifestDigest, Contributions: m.Contributions.Names(), Roles: declaredRoles(m), State: StateActive}
		byPin[pin] = r
		seed.Registrations = append(seed.Registrations, r)
		if m.Contributions.Normalizer != nil {
			for _, mediaType := range m.Contributions.Normalizer.MediaTypes {
				if pin.Routed(mediaType) {
					seed.Roles = append(seed.Roles, assign(normalizerRole(mediaType), r))
				}
			}
		}
	}
	for _, pin := range pins.Evaluators() {
		seed.Roles = append(seed.Roles, assign(subscriptionRole(pin.Manifest.ID), byPin[pin]))
	}
	for _, pinned := range pins.Connectors() {
		seed.Roles = append(seed.Roles, assign(connectorRole(pinned.Kind), byPin[pinned.Pin]))
	}
	if pin := pins.Ingestion(); pin != nil {
		seed.Roles = append(seed.Roles, assign(ingestionRole, byPin[pin]))
	}
	if pin := pins.Retrieval(); pin != nil {
		seed.Roles = append(seed.Roles, assign(retrievalRole, byPin[pin]))
	}
	sort.Slice(seed.Roles, func(i, j int) bool { return seed.Roles[i].Role < seed.Roles[j].Role })
	return seed
}

func (a Assignment) describe() string {
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

// Compare lists, sorted by role, the roles on which the configured seed and
// the active plan disagree: a role only one side has, or one served by
// different registrations.
func Compare(seed Seed, active Plan) []Difference {
	configured, current := map[string]Assignment{}, map[string]Assignment{}
	for _, a := range seed.Roles {
		configured[a.Role] = a
	}
	for _, a := range active.Roles {
		current[a.Role] = a
	}
	roles := map[string]bool{}
	for role := range configured {
		roles[role] = true
	}
	for role := range current {
		roles[role] = true
	}
	var out []Difference
	for role := range roles {
		c, inConfig := configured[role]
		a, inPlan := current[role]
		if inConfig && inPlan && c.RegistrationID == a.RegistrationID {
			continue
		}
		d := Difference{Role: role}
		if inConfig {
			d.Configured = c.describe()
		}
		if inPlan {
			d.Active = a.describe()
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Role < out[j].Role })
	return out
}
