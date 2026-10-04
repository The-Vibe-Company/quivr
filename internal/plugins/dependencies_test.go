package plugins_test

import (
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"testing"
)

// Startup admission owns the dependency graph contract. Broken installation,
// upgrades, missing profiles and graph loops must fail before any plugin I/O.
func TestSearchDependenciesAtPinTime(t *testing.T) {
	pin := func(id, requires string) *plugins.Pin {
		t.Helper()
		raw := fmt.Sprintf(`id: %s
version: 1.0.0
compatibility: {engine: ">=0.1.0 <0.3.0", plugin_api: ">=0.12.0 <0.13.0"}
contributions:
  retrieval:
    profiles:
      default: {max_latency_ms: 100, max_cost_cents: 1}
%s`, id, requires)
		p, err := plugins.LoadPinManifest([]byte(raw), "dependency-test", plugins.PinConfig{Endpoint: "http://127.0.0.1:9900"})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	requires := func(id, version, profile string) string {
		return fmt.Sprintf("requires: [{plugin: %s, version: '%s', profiles: [%s]}]\n", id, version, profile)
	}
	base := pin("core.retrieve", "")
	outer := pin("example.rerank", requires("core.retrieve", ">=1.0.0 <2.0.0", "default"))
	for _, tc := range []struct {
		name string
		pins []*plugins.Pin
		want string
	}{
		{"valid reverse installation order", []*plugins.Pin{outer, base}, ""},
		{"missing plugin", []*plugins.Pin{outer}, "plugin_dependency"},
		{"wrong version", []*plugins.Pin{pin("example.rerank", requires("core.retrieve", ">=2.0.0 <3.0.0", "default")), base}, "plugin_dependency"},
		{"missing profile", []*plugins.Pin{pin("example.rerank", requires("core.retrieve", ">=1.0.0", "deep")), base}, "plugin_dependency"},
		{"self loop", []*plugins.Pin{pin("example.rerank", requires("example.rerank", ">=1.0.0", "default"))}, "plugin_dependency"},
		{"cycle", []*plugins.Pin{outer, pin("core.retrieve", requires("example.rerank", ">=1.0.0", "default"))}, "plugin_dependency"},
		{"three levels", []*plugins.Pin{base, outer, pin("example.filter", requires("example.rerank", ">=1.0.0", "default"))}, "plugin_dependency"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := plugins.NewPinSet(tc.pins)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertPinCode(t, err, tc.want)
		})
	}
}
