package registry_test

import (
	"reflect"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
)

// TestSeedMirrorsWhatThePinsResolve owns the mapping from startup pins to the
// seeded registry: one active registration per pinned plugin, and a plan whose
// roles are the routed media types, the alert rules and the connector kinds
// the engine resolves from the same pins.
func TestSeedMirrorsWhatThePinsResolve(t *testing.T) {
	pins, err := plugins.LoadPins([]plugins.PinConfig{
		{Manifest: "../../../plugins/pdf-text/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9900", Routes: []plugins.RouteConfig{{MediaType: "application/pdf"}}},
		{Manifest: "../../../plugins/alerts/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9910/"},
		{Manifest: "../../../plugins/rss/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9920"},
	})
	if err != nil {
		t.Fatal(err)
	}
	seed := registry.FromPins(pins)
	if len(seed.Registrations) != 3 {
		t.Fatalf("registrations %+v, want one per pin", seed.Registrations)
	}
	ids := map[string]string{}
	for i, r := range seed.Registrations {
		pin := pins.Pins()[i]
		if r.State != registry.StateActive || r.PluginID != pin.Manifest.ID || r.Endpoint != pin.Endpoint || r.ManifestDigest != pin.ManifestDigest || r.ID != registry.RegistrationID(r.PluginID, r.Version, r.ManifestDigest, r.Endpoint) {
			t.Fatalf("registration %+v does not mirror pin %s@%s at %s", r, pin.Manifest.ID, pin.Manifest.Version, pin.Endpoint)
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
	if want := []string{"connector:rss", "normalizer:application/pdf", "subscription:alerts"}; !reflect.DeepEqual(roles, want) {
		t.Fatalf("plan roles %v, want %v", roles, want)
	}
	if empty := registry.FromPins(nil); len(empty.Registrations) != 0 || len(empty.Roles) != 0 {
		t.Fatalf("no pins seeded %+v", empty)
	}
}
