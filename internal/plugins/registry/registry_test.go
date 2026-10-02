package registry_test

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
)

// TestSeedMirrorsWhatThePinsResolve owns the mapping from startup pins to the
// seeded registry: one active registration per pinned plugin, keeping its
// exact manifest and settings, and a plan whose roles are the routed media
// types, the alert rules, the connector kinds and the retrieval role the
// engine resolves from the same pins.
func TestSeedMirrorsWhatThePinsResolve(t *testing.T) {
	pins, err := plugins.LoadPins([]plugins.PinConfig{
		{Manifest: "../../../plugins/pdf-text/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9900", Routes: []plugins.RouteConfig{{MediaType: "application/pdf"}}},
		{Manifest: "../../../plugins/alerts/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9910/"},
		{Manifest: "../../../plugins/rss/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9920"},
		{Manifest: "../../../sdks/go/examples/fusion-retriever/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9930"},
	})
	if err != nil {
		t.Fatal(err)
	}
	seed := registry.FromPins(pins)
	if len(seed.Registrations) != 4 {
		t.Fatalf("registrations %+v, want one per pin", seed.Registrations)
	}
	ids := map[string]string{}
	for i, r := range seed.Registrations {
		pin := pins.Pins()[i]
		if r.State != registry.StateActive || r.PluginID != pin.Manifest.ID || r.Endpoint != pin.Endpoint || r.ManifestDigest != pin.ManifestDigest || r.ID != registry.RegistrationID(r.PluginID, r.Version, r.ManifestDigest, r.Endpoint, r.Settings.Digest()) {
			t.Fatalf("registration %+v does not mirror pin %s@%s at %s", r, pin.Manifest.ID, pin.Manifest.Version, pin.Endpoint)
		}
		// The registration alone resolves to the same plugin.
		again, err := r.Pin()
		if err != nil || again.ManifestDigest != pin.ManifestDigest || !reflect.DeepEqual(again.Routes(), pin.Routes()) || string(again.Configuration) != string(pin.Configuration) {
			t.Fatalf("registration %s resolves to %+v (%v), want the pin it came from", r.ID, again, err)
		}
		ids[r.PluginID] = r.ID
	}
	if got := seed.Registrations[2].Roles; !reflect.DeepEqual(got, []string{"connector:rss"}) {
		t.Fatalf("rss declared roles %v", got)
	}
	var roles []string
	for _, a := range seed.Roles {
		roles = append(roles, a.Role)
		if want := ids[a.PluginID]; a.RegistrationID != want {
			t.Fatalf("role %s served by %s, want the registration of %s (%s)", a.Role, a.RegistrationID, a.PluginID, want)
		}
	}
	if want := []string{"connector:rss", "normalizer:application/pdf", "retrieval:example.fusion_retriever", "subscription:alerts"}; !reflect.DeepEqual(roles, want) {
		t.Fatalf("plan roles %v, want %v", roles, want)
	}
	if empty := registry.FromPins(nil); len(empty.Registrations) != 0 || len(empty.Roles) != 0 {
		t.Fatalf("no pins seeded %+v", empty)
	}
	// Other settings are another registration: plans never change under it.
	other, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: "../../../plugins/alerts/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9910", Kinds: []string{"keywords"}}})
	if err != nil {
		t.Fatal(err)
	}
	if id := registry.FromPins(other).Registrations[0].ID; id == ids["alerts"] {
		t.Fatalf("alerts offering fewer kinds kept registration %s", id)
	}
}

// manifest is a test plugin manifest declaring the given contributions YAML.
func manifest(id, version, contributions string) string {
	return "id: " + id + "\nversion: " + version + "\ncompatibility:\n  engine: \">=0.1.0 <0.2.0\"\n  plugin_api: \">=0.8.0 <0.9.0\"\ncontributions:\n" + contributions
}

func normalizer(mediaTypes ...string) string {
	return "  normalizer:\n    media_types: [" + strings.Join(mediaTypes, ", ") + "]\n    timeout_ms: 5000\n"
}

func ingestion(space string) string {
	return "  ingestion:\n    spaces:\n      " + space + ":\n        version: \"1\"\n        model: fake/hash-8\n        dimensions: 8\n        metric: cosine\n        indexes: [text]\n        query_modalities: [text]\n    timeout_ms: 5000\n    query_timeout_ms: 1000\n"
}

// registered is a validated registration of a test manifest.
func registered(t *testing.T, yaml string, routes ...string) registry.Registration {
	t.Helper()
	c := plugins.PinConfig{Endpoint: "http://127.0.0.1:9900"}
	for _, mediaType := range routes {
		c.Routes = append(c.Routes, plugins.RouteConfig{MediaType: mediaType})
	}
	pin, err := plugins.LoadPinManifest([]byte(yaml), "test", c)
	if err != nil {
		t.Fatal(err)
	}
	set, err := plugins.NewPinSet([]*plugins.Pin{pin})
	if err != nil {
		t.Fatal(err)
	}
	r := registry.FromPins(set).Registrations[0]
	r.State = registry.StateValidated
	return r
}

// plan is the active plan these registrations serve, as their pins resolve.
func plan(t *testing.T, members ...registry.Registration) (registry.Plan, map[string]registry.Registration) {
	t.Helper()
	byID := map[string]registry.Registration{}
	var pins []*plugins.Pin
	for _, m := range members {
		byID[m.ID] = m
		pin, err := m.Pin()
		if err != nil {
			t.Fatal(err)
		}
		pins = append(pins, pin)
	}
	set, err := plugins.NewPinSet(pins)
	if err != nil {
		t.Fatal(err)
	}
	seed := registry.FromPins(set)
	for i := range seed.Roles {
		seed.Roles[i].RegistrationID = members[indexOf(seed.Registrations, seed.Roles[i].RegistrationID)].ID
	}
	return registry.Plan{ID: "plan_active", Roles: seed.Roles}, byID
}

func indexOf(list []registry.Registration, id string) int {
	for i, r := range list {
		if r.ID == id {
			return i
		}
	}
	return -1
}

func roleMap(roles []registry.Assignment) map[string]string {
	out := map[string]string{}
	for _, a := range roles {
		out[a.Role] = a.PluginID + "@" + a.Version
	}
	return out
}

func routedPlan(t *testing.T, routing plugins.IngestionRouting, members ...registry.Registration) (registry.Plan, map[string]registry.Registration) {
	t.Helper()
	byID := map[string]registry.Registration{}
	var pins []*plugins.Pin
	for _, member := range members {
		byID[member.ID] = member
		pin, err := member.Pin()
		if err != nil {
			t.Fatal(err)
		}
		pins = append(pins, pin)
	}
	set, err := plugins.NewPinSet(pins)
	if err != nil {
		t.Fatal(err)
	}
	if err := set.ConfigureIngestion(routing); err != nil {
		t.Fatal(err)
	}
	seed := registry.FromPins(set)
	return registry.Plan{ID: "plan_active", Roles: seed.Roles}, byID
}

// TestIngestionPlanRoutingOwnsSourceRoutesAndPluginMembership verifies that
// a plan stores each ingestion plugin as a member, keeps one explicit default,
// and resolves an explicit source-media route to its owner.
func TestIngestionPlanRouting(t *testing.T) {
	words := registered(t, manifest("example.words", "1.0.0", ingestion("example.words.small")))
	pdf := registered(t, manifest("example.pdf", "1.0.0", ingestion("example.pdf.small")))
	active, members := routedPlan(t, plugins.IngestionRouting{Default: "example.words", Routes: map[string]string{"application/pdf": "example.pdf"}}, words, pdf)
	set, _, err := registry.Resolve(active.Roles, members)
	if err != nil {
		t.Fatal(err)
	}
	if set.Ingestion() == nil || set.Ingestion().Manifest.ID != "example.words" || set.IngestionFor("application/pdf").Manifest.ID != "example.pdf" || set.IngestionFor("text/plain").Manifest.ID != "example.words" {
		t.Fatalf("resolved ingestion routing: default=%v pdf=%v text=%v", set.Ingestion(), set.IngestionFor("application/pdf"), set.IngestionFor("text/plain"))
	}
	want := map[string]string{"ingestion-default": "example.words@1.0.0", "ingestion:example.pdf": "example.pdf@1.0.0", "ingestion:example.words": "example.words@1.0.0", "ingestion-route:application/pdf": "example.pdf@1.0.0"}
	if got := roleMap(active.Roles); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan roles %v, want %v", got, want)
	}
	legacy := []registry.Assignment{{Role: "ingestion", RegistrationID: words.ID, PluginID: words.PluginID, Version: words.Version}}
	if resolved, _, err := registry.Resolve(legacy, map[string]registry.Registration{words.ID: words}); err != nil || resolved.Ingestion() == nil || resolved.Ingestion().Manifest.ID != words.PluginID {
		t.Fatalf("legacy default plan did not resolve: %+v (%v)", resolved, err)
	}
}

// TestActivationKeepsTheStartupRules owns how an activation computes the new
// plan: it replaces the other version of the same plugin and any plugin whose
// roles it takes over entirely, keeps the rest, and refuses what startup
// would refuse, a registration that is not validated, and an alert rule.
func TestActivationKeepsTheStartupRules(t *testing.T) {
	embedderV1 := registered(t, manifest("example.embedder", "1.0.0", ingestion("example.embedder.small")))
	embedderV2 := registered(t, manifest("example.embedder", "2.0.0", ingestion("example.embedder.small")))
	otherEmbedder := registered(t, manifest("other.embedder", "1.0.0", ingestion("other.embedder.small")))
	markdown := registered(t, manifest("example.markdown", "1.0.0", normalizer("text/markdown", "text/x-rst")), "text/markdown", "text/x-rst")
	markdownOnly := registered(t, manifest("other.markdown", "1.0.0", normalizer("text/markdown")), "text/markdown")
	active, members := plan(t, embedderV1, markdown)

	upgrade, err := registry.PlanActivation(active, members, embedderV2, nil)
	if err != nil || !reflect.DeepEqual(roleMap(upgrade.Roles), map[string]string{"ingestion-default": "example.embedder@2.0.0", "ingestion:example.embedder": "example.embedder@2.0.0", "normalizer:text/markdown": "example.markdown@1.0.0", "normalizer:text/x-rst": "example.markdown@1.0.0"}) || !reflect.DeepEqual(upgrade.Retired, []string{embedderV1.ID}) {
		t.Fatalf("upgrade: %+v (%v); want 2.0.0 serving ingestion beside the untouched normalizer, 1.0.0 retired", upgrade, err)
	}
	takeover, err := registry.PlanActivation(active, members, otherEmbedder, nil)
	if err != nil || roleMap(takeover.Roles)["ingestion-default"] != "example.embedder@1.0.0" || roleMap(takeover.Roles)["ingestion:other.embedder"] != "other.embedder@1.0.0" || len(takeover.Retired) != 0 {
		t.Fatalf("another ingestion plugin joins without taking the default: %+v (%v)", takeover, err)
	}

	// A new alert-rule version takes the plugin's role; the old one leaves the
	// plan and drains while Subscription Versions pin it (THE-805).
	ruleV1, ruleV2 := registered(t, manifest("example.rule", "1.0.0", rule)), registered(t, manifest("example.rule", "2.0.0", rule))
	withRule, ruleMembers := plan(t, embedderV1, ruleV1)
	if next, err := registry.PlanActivation(withRule, ruleMembers, ruleV2, nil); err != nil || roleMap(next.Roles)["subscription:example.rule"] != "example.rule@2.0.0" || !reflect.DeepEqual(next.Retired, []string{ruleV1.ID}) {
		t.Fatalf("an alert-rule upgrade: %+v (%v); want 2.0.0 serving the rule's role, 1.0.0 retired", next, err)
	}

	// A registration still draining can serve again (a rollback).
	draining := embedderV2
	draining.State = registry.StateDraining
	if _, err := registry.PlanActivation(active, members, draining, nil); err != nil {
		t.Fatalf("activating a draining registration: %v", err)
	}

	for name, c := range map[string]struct {
		target registry.Registration
		want   error
		says   string
	}{
		// example.markdown would keep text/x-rst and still claim text/markdown.
		"partial overlap": {markdownOnly, registry.ErrConflict, plugins.CodeRouteConflict},
		"not validated":   {func() registry.Registration { r := embedderV2; r.State = registry.StateRejected; return r }(), registry.ErrNotValidated, "rejected"},
	} {
		_, err := registry.PlanActivation(active, members, c.target, nil)
		if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want %v naming %q", name, err, c.want, c.says)
		}
	}
	refused := errors.New("connector kind rss is built in")
	if _, err := registry.PlanActivation(active, members, embedderV2, func(*plugins.PinSet) error { return refused }); !errors.Is(err, registry.ErrConflict) || !strings.Contains(err.Error(), refused.Error()) {
		t.Fatalf("a check of the running engine refuses: %v", err)
	}
}

// TestIngestionUpgradeKeepsUnrelatedRoutes verifies that an ingestion
// activation upgrades only its own registration and redirects routes that
// already name it, while an unrelated default and ingestion member stay put.
func TestIngestionUpgradeKeepsUnrelatedRoutes(t *testing.T) {
	words := registered(t, manifest("example.words", "1.0.0", ingestion("example.words.small")))
	pdfV1 := registered(t, manifest("example.pdf", "1.0.0", ingestion("example.pdf.small")))
	pdfV2 := registered(t, manifest("example.pdf", "2.0.0", ingestion("example.pdf.small")))
	active, members := routedPlan(t, plugins.IngestionRouting{Default: "example.words", Routes: map[string]string{"application/pdf": "example.pdf"}}, words, pdfV1)
	upgrade, err := registry.PlanActivation(active, members, pdfV2, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := roleMap(upgrade.Roles)
	if got["ingestion-default"] != "example.words@1.0.0" || got["ingestion:example.words"] != "example.words@1.0.0" || got["ingestion-route:application/pdf"] != "example.pdf@2.0.0" || got["ingestion:example.pdf"] != "example.pdf@2.0.0" {
		t.Fatalf("upgraded routing %v", got)
	}
	if !reflect.DeepEqual(upgrade.Retired, []string{pdfV1.ID}) {
		t.Fatalf("retired %v, want only %s", upgrade.Retired, pdfV1.ID)
	}
}

func TestIngestionUpgradeCannotRemoveConfiguredContribution(t *testing.T) {
	current := registered(t, manifest("example.words", "1.0.0", ingestion("example.words.small")))
	withoutIngestion := registered(t, manifest("example.words", "2.0.0", normalizer("text/plain")), "text/plain")
	active, members := plan(t, current)
	if _, err := registry.PlanActivation(active, members, withoutIngestion, nil); !errors.Is(err, registry.ErrConflict) || !strings.Contains(err.Error(), plugins.CodeInvalidPin) {
		t.Fatalf("removing the active ingestion contribution: %v, want a conflict", err)
	}
}

// TestLegacyIngestionReconciliationAddsMembershipPreservesActivation verifies
// that startup normalizes an old default-only plan without replacing an
// operator-selected default that configuration did not change.
func TestLegacyIngestionReconciliationAddsMembershipPreservesActivation(t *testing.T) {
	configured := registered(t, manifest("core.embedder", "1.0.0", ingestion("core.embedder.small")))
	operator := registered(t, manifest("other.embedder", "1.0.0", ingestion("other.embedder.small")))
	configuredPlan, members := routedPlan(t, plugins.IngestionRouting{Default: "core.embedder"}, configured)
	legacy := registry.Plan{ID: "legacy", Roles: []registry.Assignment{{Role: "ingestion", RegistrationID: operator.ID, PluginID: operator.PluginID, Version: operator.Version}}}
	members[operator.ID] = operator
	configuredSeed := registry.Seed{Roles: configuredPlan.Roles}
	r := registry.Reconcile(configuredSeed, configuredSeed.Snapshot(), &legacy, members)
	if !r.Changed || roleMap(r.Roles)["ingestion-default"] != "other.embedder@1.0.0" || roleMap(r.Roles)["ingestion:other.embedder"] != "other.embedder@1.0.0" {
		t.Fatalf("legacy reconciliation %+v, want the operator default plus normalized membership", r)
	}
}

// TestSeededRegistrationsKeepTheStartupKeyIdentity owns the idempotency key
// across the move to the registry: a plugin the configuration pinned and the
// same plugin resolved from the registration the configuration seeded give
// its invocations the same idempotency key identity, so work in flight keeps
// converging. A registration an operator recorded is named by its id, which
// covers its settings.
func TestSeededRegistrationsKeepTheStartupKeyIdentity(t *testing.T) {
	configured, err := plugins.LoadPinManifest([]byte(manifest("example.embedder", "1.0.0", ingestion("example.embedder.small"))), "test", plugins.PinConfig{Endpoint: "http://127.0.0.1:9900"})
	if err != nil {
		t.Fatal(err)
	}
	set, err := plugins.NewPinSet([]*plugins.Pin{configured})
	if err != nil {
		t.Fatal(err)
	}
	seeded := registry.FromPins(set).Registrations[0]
	resolved, err := seeded.Pin()
	if err != nil {
		t.Fatal(err)
	}
	if want := "startup:example.embedder@1.0.0#" + configured.ManifestDigest; configured.Generation() != want || resolved.Generation() != want {
		t.Fatalf("configured %q, seeded %q; want both %q", configured.Generation(), resolved.Generation(), want)
	}
	byOperator := seeded
	byOperator.Origin = registry.OriginRegistration
	if resolved, err = byOperator.Pin(); err != nil || resolved.Generation() != "registration:"+seeded.ID {
		t.Fatalf("an operator registration: %q (%v), want registration:%s", resolved.Generation(), err, seeded.ID)
	}
}

// TestConfigurationAppliesOnlyTheRolesItChanged owns the startup rule: the
// configuration applies a role only when it changed since it last applied, so
// an operator activation survives restarts, while a deliberate configuration
// change wins and names the activation it replaces. Without a snapshot (a
// registry seeded before THE-781) the configuration applies whole.
func TestConfigurationAppliesOnlyTheRolesItChanged(t *testing.T) {
	core := registered(t, manifest("core.embedder", "1.0.0", ingestion("core.embedder.small")))
	activated := registered(t, manifest("other.embedder", "1.0.0", ingestion("other.embedder.small")))
	markdown := registered(t, manifest("example.markdown", "1.0.0", normalizer("text/markdown")), "text/markdown")
	markdownV2 := registered(t, manifest("example.markdown", "2.0.0", normalizer("text/markdown")), "text/markdown")
	all := map[string]registry.Registration{}
	for _, r := range []registry.Registration{core, activated, markdown, markdownV2} {
		all[r.ID] = r
	}
	seed := func(members ...registry.Registration) registry.Seed {
		p, _ := plan(t, members...)
		return registry.Seed{Registrations: members, Roles: p.Roles}
	}
	configured := seed(core, markdown)
	operator, _ := plan(t, activated, markdown)

	// THE-780 seeded a plan without the ingestion role and no snapshot.
	legacy, _ := plan(t, markdown)
	if r := registry.Reconcile(configured, nil, &legacy, all); !r.Changed || !reflect.DeepEqual(roleMap(r.Roles), map[string]string{"ingestion-default": "core.embedder@1.0.0", "ingestion:core.embedder": "core.embedder@1.0.0", "normalizer:text/markdown": "example.markdown@1.0.0"}) {
		t.Fatalf("a registry without snapshot takes the configuration: %+v", r)
	}
	// Unchanged configuration: the operator's ingestion plugin stays.
	if r := registry.Reconcile(configured, configured.Snapshot(), &operator, all); r.Changed || roleMap(r.Roles)["ingestion-default"] != "other.embedder@1.0.0" {
		t.Fatalf("an unchanged configuration changed the plan: %+v", r)
	}
	// The configuration upgrades the normalizer: that role only.
	upgraded := seed(core, markdownV2)
	r := registry.Reconcile(upgraded, configured.Snapshot(), &operator, all)
	if !r.Changed || len(r.Overridden) != 0 || !reflect.DeepEqual(roleMap(r.Roles), map[string]string{"ingestion-default": "other.embedder@1.0.0", "ingestion:other.embedder": "other.embedder@1.0.0", "normalizer:text/markdown": "example.markdown@2.0.0"}) {
		t.Fatalf("a configuration change of another role: %+v", r)
	}
	// The configuration removes the normalizer the operator never touched.
	if r := registry.Reconcile(seed(core), configured.Snapshot(), &operator, all); !r.Changed || !reflect.DeepEqual(roleMap(r.Roles), map[string]string{"ingestion-default": "other.embedder@1.0.0", "ingestion:other.embedder": "other.embedder@1.0.0"}) {
		t.Fatalf("a role removed from the configuration: %+v", r)
	}
	// The configuration changes ingestion itself: it wins and names the activation.
	previous := seed(activated, markdown)
	previous.Roles[0].RegistrationID = "plugin_registration_earlier"
	r = registry.Reconcile(configured, previous.Snapshot(), &operator, all)
	if !r.Changed || roleMap(r.Roles)["ingestion-default"] != "core.embedder@1.0.0" || len(r.Overridden) != 1 || r.Overridden[0].Role != "ingestion-default" || !strings.Contains(r.Overridden[0].Active, "other.embedder@1.0.0") || !strings.Contains(r.Overridden[0].Configured, "core.embedder@1.0.0") {
		t.Fatalf("a configuration change over an activation: %+v", r)
	}
}

// rule is the subscription Contribution of a test alert-rule manifest.
const rule = "  subscription:\n    expression_schema: {type: object}\n    timeout_ms: 1000\n"

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// Retrieval providers have independent lifecycles: upgrading one retires only
// its old registration, and rollback restores it beside the other provider.
// Legacy plans and startup snapshots must still preserve operator upgrades.
func TestRetrievalPluginsActivateAndRollbackIndependently(t *testing.T) {
	source := must(os.ReadFile("../../../sdks/go/examples/fusion-retriever/quivr-plugin.yaml"))
	first := registered(t, string(source))
	second := registered(t, strings.Replace(string(source), "example.fusion_retriever", "example.other_retriever", 1))
	upgrade := registered(t, strings.Replace(string(source), "version: 0.1.0", "version: 0.2.0", 1))
	active, members := plan(t, first, second)
	next, err := registry.PlanActivation(active, members, upgrade, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roleMap(next.Roles), map[string]string{"retrieval:example.fusion_retriever": "example.fusion_retriever@0.2.0", "retrieval:example.other_retriever": "example.other_retriever@0.1.0"}) || !reflect.DeepEqual(next.Retired, []string{first.ID}) {
		t.Fatalf("activation %+v", next)
	}
	members[upgrade.ID] = upgrade
	back, err := registry.PlanRollback(registry.Plan{ID: "new", Roles: next.Roles}, active, members, nil)
	if err != nil || !reflect.DeepEqual(roleMap(back.Roles), roleMap(active.Roles)) || !reflect.DeepEqual(back.Retired, []string{upgrade.ID}) || len(back.Returning) != 1 || back.Returning[0].ID != first.ID {
		t.Fatalf("rollback %+v: %v", back, err)
	}
	// Adding another provider does not take over the existing provider.
	alone, aloneMembers := plan(t, first)
	added, err := registry.PlanActivation(alone, aloneMembers, second, nil)
	if err != nil || len(added.Roles) != 2 || len(added.Retired) != 0 {
		t.Fatalf("addition %+v: %v", added, err)
	}
	// A deployment upgrading from the legacy singleton keeps an operator's
	// activated version on restart, then can roll back to its old plan.
	legacy := alone
	legacy.Roles = append([]registry.Assignment(nil), alone.Roles...)
	legacy.Roles[0].Role = "retrieval"
	operator, _ := plan(t, upgrade)
	operator.Roles[0].Role = "retrieval"
	members[first.ID] = first
	configuredPin, err := first.Pin()
	if err != nil {
		t.Fatal(err)
	}
	configured, err := plugins.NewPinSet([]*plugins.Pin{configuredPin})
	if err != nil {
		t.Fatal(err)
	}
	reconciled := registry.Reconcile(registry.FromPins(configured), map[string]string{"retrieval": first.ID}, &operator, members)
	if reconciled.Whole || roleMap(reconciled.Roles)["retrieval:example.fusion_retriever"] != "example.fusion_retriever@0.2.0" {
		t.Fatalf("legacy reconciliation %+v", reconciled)
	}
	back, err = registry.PlanRollback(registry.Plan{Roles: reconciled.Roles}, legacy, members, nil)
	if err != nil || roleMap(back.Roles)["retrieval:example.fusion_retriever"] != "example.fusion_retriever@0.1.0" {
		t.Fatalf("legacy rollback %+v: %v", back, err)
	}
	// The legacy singleton could be switched to a different plugin by an
	// operator. Pinning both now must restore the original provider alongside it.
	switched, _ := plan(t, second)
	switched.Roles[0].Role = "retrieval"
	both := registry.Seed{Registrations: []registry.Registration{first, second}, Roles: active.Roles}
	for _, snapshot := range []map[string]string{{"retrieval": first.ID}, {"retrieval:example.fusion_retriever": first.ID}} {
		reconciled = registry.Reconcile(both, snapshot, &switched, members)
		if len(reconciled.Roles) != 2 || !reflect.DeepEqual(roleMap(reconciled.Roles), roleMap(active.Roles)) {
			t.Fatalf("legacy provider switch followed by multiple pins with snapshot %v: %+v", snapshot, reconciled)
		}
	}
	// An unchanged single-plugin configuration still preserves that override.
	reconciled = registry.Reconcile(registry.FromPins(configured), map[string]string{"retrieval": first.ID}, &switched, members)
	if len(reconciled.Roles) != 1 || roleMap(reconciled.Roles)["retrieval:example.other_retriever"] != "example.other_retriever@0.1.0" {
		t.Fatalf("legacy single-provider override: %+v", reconciled)
	}
	// Rolling back a multi-provider plan may deliberately remove a provider.
	// An unchanged multi-provider configuration must preserve that decision.
	rolled, err := registry.PlanRollback(active, alone, members, nil)
	if err != nil {
		t.Fatal(err)
	}
	afterRollback := registry.Plan{Roles: rolled.Roles}
	reconciled = registry.Reconcile(both, both.Snapshot(), &afterRollback, members)
	if reconciled.Changed || !reflect.DeepEqual(roleMap(reconciled.Roles), roleMap(alone.Roles)) {
		t.Fatalf("unchanged multi-provider configuration undid rollback: %+v", reconciled)
	}
}

// Activation checks dependencies against its entire resulting plan. Updating
// a provider cannot silently break an already-active dependent.
func TestActivationPreservesSearchDependencies(t *testing.T) {
	raw := func(id, version, requires string) string {
		return "id: " + id + "\nversion: " + version + "\ncompatibility: {engine: '>=0.1.0 <0.2.0', plugin_api: '>=0.12.0 <0.13.0'}\ncontributions:\n  retrieval:\n    profiles:\n      default: {max_latency_ms: 100, max_cost_cents: 1}\n" + requires
	}
	base := dependencyRegistration(t, raw("core.retrieve", "1.0.0", ""))
	dependent := dependencyRegistration(t, raw("example.rerank", "1.0.0", "requires: [{plugin: core.retrieve, version: '>=1.0.0 <2.0.0', profiles: [default]}]\n"))
	active, members := plan(t, base)
	next, err := registry.PlanActivation(active, members, dependent, nil)
	if err != nil || len(next.Roles) != 2 {
		t.Fatalf("valid dependency activation: %+v, %v", next, err)
	}
	members[dependent.ID] = dependent
	active.Roles = next.Roles
	for _, upgrade := range []registry.Registration{
		dependencyRegistration(t, raw("core.retrieve", "2.0.0", "")),
		dependencyRegistration(t, raw("core.retrieve", "1.1.0", "requires: [{plugin: example.rerank, version: '>=1.0.0', profiles: [default]}]\n")),
	} {
		_, err := registry.PlanActivation(active, members, upgrade, nil)
		var issue *registry.IssueError
		if !errors.As(err, &issue) || len(issue.Issues) == 0 || issue.Issues[0].Code != "plugin_dependency" {
			t.Fatalf("provider upgrade admitted: %v", err)
		}
	}
	_, err = registry.PlanActivation(registry.Plan{}, nil, dependent, nil)
	if !errors.Is(err, registry.ErrConflict) {
		t.Fatalf("missing dependency activated: %v", err)
	}
}

func dependencyRegistration(t *testing.T, raw string) registry.Registration {
	t.Helper()
	p, err := plugins.LoadPinManifest([]byte(raw), "activation", plugins.PinConfig{Endpoint: "http://127.0.0.1:9900"})
	if err != nil {
		t.Fatal(err)
	}
	settings := registry.SettingsOf(p)
	return registry.Registration{ID: registry.RegistrationID(p.Manifest.ID, p.Manifest.Version, p.ManifestDigest, p.Endpoint, settings.Digest()), PluginID: p.Manifest.ID, Version: p.Manifest.Version, Endpoint: p.Endpoint, ManifestDigest: p.ManifestDigest, Manifest: p.Source, Settings: settings, State: registry.StateValidated}
}
