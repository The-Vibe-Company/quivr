package plugins_test

import (
	"context"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// TestLiveServesTheNamespacesOfTheCurrentSet owns the extension side of
// following a plan: the namespaces a plugin declares are declared (so
// retrieval mappings may address them) and refused to clients while its set
// is current, and neither once a set without it replaces it.
func TestLiveServesTheNamespacesOfTheCurrentSet(t *testing.T) {
	set, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: "../../tests/plugin-contract/valid/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9900", Routes: []plugins.RouteConfig{{MediaType: "text/markdown"}}}})
	if err != nil {
		t.Fatal(err)
	}
	live, err := plugins.NewLive("plan_a", set)
	if err != nil {
		t.Fatal(err)
	}
	var validator content.ExtensionValidator = live
	owned := content.Extensions{"certified.fake.stats": {SchemaVersion: "1", Data: map[string]any{"characters": 1}}}
	if !content.Service.ExtensionDeclared(content.Service{Extensions: live}, "certified.fake.stats") || validator.Validate(context.Background(), owned) == nil || !live.Routed("text/markdown") {
		t.Fatalf("plan_a: the plugin's namespace must be declared and refused to clients, and its media type routed")
	}
	if err = live.Store("plan_b", nil); err != nil {
		t.Fatal(err)
	}
	if content.Service.ExtensionDeclared(content.Service{Extensions: live}, "certified.fake.stats") || live.Routed("text/markdown") || live.Plan() != "plan_b" {
		t.Fatalf("plan_b without the plugin still declares its namespace or routes its media type")
	}
}
