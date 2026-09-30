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
	if want := []string{"connector:rss", "normalizer:application/pdf", "retrieval", "subscription:alerts"}; !reflect.DeepEqual(roles, want) {
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
	if err != nil || !reflect.DeepEqual(roleMap(upgrade.Roles), map[string]string{"ingestion": "example.embedder@2.0.0", "normalizer:text/markdown": "example.markdown@1.0.0", "normalizer:text/x-rst": "example.markdown@1.0.0"}) || !reflect.DeepEqual(upgrade.Retired, []string{embedderV1.ID}) {
		t.Fatalf("upgrade: %+v (%v); want 2.0.0 serving ingestion beside the untouched normalizer, 1.0.0 retired", upgrade, err)
	}
	takeover, err := registry.PlanActivation(active, members, otherEmbedder, nil)
	if err != nil || roleMap(takeover.Roles)["ingestion"] != "other.embedder@1.0.0" || !reflect.DeepEqual(takeover.Retired, []string{embedderV1.ID}) {
		t.Fatalf("another ingestion plugin takes the role over: %+v (%v)", takeover, err)
	}

	for name, c := range map[string]struct {
		target registry.Registration
		want   error
		says   string
	}{
		// example.markdown would keep text/x-rst and still claim text/markdown.
		"partial overlap": {markdownOnly, registry.ErrConflict, plugins.CodeRouteConflict},
		"not validated":   {func() registry.Registration { r := embedderV2; r.State = registry.StateRejected; return r }(), registry.ErrNotValidated, "rejected"},
		"alert rule":      {registered(t, manifest("example.rule", "1.0.0", "  subscription:\n    expression_schema: {type: object}\n    timeout_ms: 1000\n")), registry.ErrUnsupportedRole, "THE-782"},
		"adds retrieval":  {registered(t, string(must(os.ReadFile("../../../sdks/go/examples/fusion-retriever/quivr-plugin.yaml")))), registry.ErrConflict, plugins.CodeRetrievalConflict},
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
	if r := registry.Reconcile(configured, nil, &legacy, all); !r.Changed || !reflect.DeepEqual(roleMap(r.Roles), map[string]string{"ingestion": "core.embedder@1.0.0", "normalizer:text/markdown": "example.markdown@1.0.0"}) {
		t.Fatalf("a registry without snapshot takes the configuration: %+v", r)
	}
	// Unchanged configuration: the operator's ingestion plugin stays.
	if r := registry.Reconcile(configured, configured.Snapshot(), &operator, all); r.Changed || roleMap(r.Roles)["ingestion"] != "other.embedder@1.0.0" {
		t.Fatalf("an unchanged configuration changed the plan: %+v", r)
	}
	// The configuration upgrades the normalizer: that role only.
	upgraded := seed(core, markdownV2)
	r := registry.Reconcile(upgraded, configured.Snapshot(), &operator, all)
	if !r.Changed || len(r.Overridden) != 0 || !reflect.DeepEqual(roleMap(r.Roles), map[string]string{"ingestion": "other.embedder@1.0.0", "normalizer:text/markdown": "example.markdown@2.0.0"}) {
		t.Fatalf("a configuration change of another role: %+v", r)
	}
	// The configuration removes the normalizer the operator never touched.
	if r := registry.Reconcile(seed(core), configured.Snapshot(), &operator, all); !r.Changed || !reflect.DeepEqual(roleMap(r.Roles), map[string]string{"ingestion": "other.embedder@1.0.0"}) {
		t.Fatalf("a role removed from the configuration: %+v", r)
	}
	// The configuration changes ingestion itself: it wins and names the activation.
	previous := seed(activated, markdown)
	previous.Roles[0].RegistrationID = "plugin_registration_earlier"
	r = registry.Reconcile(configured, previous.Snapshot(), &operator, all)
	if !r.Changed || roleMap(r.Roles)["ingestion"] != "core.embedder@1.0.0" || len(r.Overridden) != 1 || r.Overridden[0].Role != "ingestion" || !strings.Contains(r.Overridden[0].Active, "other.embedder@1.0.0") || !strings.Contains(r.Overridden[0].Configured, "core.embedder@1.0.0") {
		t.Fatalf("a configuration change over an activation: %+v", r)
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}
