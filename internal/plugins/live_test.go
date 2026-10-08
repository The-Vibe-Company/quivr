package plugins_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
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
	if !content.Service.ExtensionDeclared(content.Service{Extensions: live}, "certified.fake.stats") || validator.Validate(context.Background(), owned) == nil || !live.Routed(context.Background(), "text/markdown") {
		t.Fatalf("plan_a: the plugin's namespace must be declared and refused to clients, and its media type routed")
	}
	if err = live.Store("plan_b", nil); err != nil {
		t.Fatal(err)
	}
	if content.Service.ExtensionDeclared(content.Service{Extensions: live}, "certified.fake.stats") || live.Routed(context.Background(), "text/markdown") || live.Plan() != "plan_b" {
		t.Fatalf("plan_b without the plugin still declares its namespace or routes its media type")
	}
}

// TestPinnedWorkFinishesOnItsPlan owns how work pinned to a Pipeline Plan
// resolves plugins (Spec 5): under its context every lookup stays in its
// plan after another plan becomes current, a restarted process resolves the
// plan again, and an Operation whose plugin cannot be reached keeps the work
// retrying while it serves the current plan, then stops it with a diagnostic
// naming the plan and the plugin once it has left and the budget is spent.
func TestPinnedWorkFinishesOnItsPlan(t *testing.T) {
	load := func(registration string) *plugins.PinSet {
		t.Helper()
		set, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: "../../tests/plugin-contract/valid/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9900", Routes: []plugins.RouteConfig{{MediaType: "text/markdown"}}}})
		if err != nil {
			t.Fatal(err)
		}
		set.Pins()[0].Registration = registration
		return set
	}
	planA := load("registration_a")
	live, err := plugins.NewLive("plan_a", planA)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	count := func(context.Context) (int, error) { attempts++; return attempts, nil }
	work, err := live.Pin(context.Background(), plugins.Work{Kind: plugins.WorkOperation, Organization: "org", ID: "receipt", Plan: "plan_a"}, count, 2)
	if err != nil {
		t.Fatal(err)
	}
	pinA, _, _ := live.Normalizer(work, "text/markdown")
	if reason, err := plugins.Unavailable(work, pinA, "normalizer"); reason != nil || err != nil || attempts != 0 {
		t.Fatalf("a plugin of the current plan: %v (%v), %d attempts counted; want retries, uncounted", reason, err, attempts)
	}

	// Plan B routes nothing: the pinned work keeps plan A's route.
	if err = live.Store("plan_b", nil); err != nil {
		t.Fatal(err)
	}
	if !live.Routed(work, "text/markdown") || live.Routed(context.Background(), "text/markdown") {
		t.Fatalf("after plan_b: pinned work routed %v, new work routed %v; want only the pinned work routed", live.Routed(work, "text/markdown"), live.Routed(context.Background(), "text/markdown"))
	}
	if reason, err := plugins.Unavailable(work, pinA, "normalizer"); reason != nil || err != nil || attempts != 1 {
		t.Fatalf("the first attempt after the plugin left the plan: %v (%v), %d attempts; want a counted retry", reason, err, attempts)
	}
	reason, err := plugins.Unavailable(work, pinA, "normalizer")
	if err != nil || reason == nil || reason.Code != plugins.CodePinnedPluginUnavailable || reason.Plan != "plan_a" || reason.Plugin != "certified.fake" || reason.PluginVersion != "0.1.0" || reason.Contribution != "normalizer" {
		t.Fatalf("the budget spent: %+v (%v); want pinned_plugin_unavailable naming plan_a and certified.fake@0.1.0", reason, err)
	}

	// A restarted worker follows plan_b and resolves plan_a for work pinned
	// to it, once.
	restarted, err := plugins.NewLive("plan_b", nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved := 0
	restarted.Resolve = func(_ context.Context, plan string) (*plugins.PinSet, error) {
		resolved++
		if plan != "plan_a" {
			t.Fatalf("resolved %s", plan)
		}
		return load("registration_a"), nil
	}
	for range 2 {
		again, err := restarted.Pin(context.Background(), plugins.Work{Plan: "plan_a"}, count, 2)
		if err != nil || !restarted.Routed(again, "text/markdown") {
			t.Fatalf("after a restart, work pinned to plan_a: routed %v (%v)", restarted.Routed(again, "text/markdown"), err)
		}
	}
	if resolved != 1 {
		t.Fatalf("plan_a resolved %d times; a plan never changes, so once", resolved)
	}
}

// Historical plan reuse is bounded without invalidating work already pinned
// to an evicted plan. Resolution count observes the real cache contract.
func TestHistoricalPlanCacheEvictsWithoutChangingPinnedWork(t *testing.T) {
	set, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: "../../tests/plugin-contract/valid/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9900", Routes: []plugins.RouteConfig{{MediaType: "text/markdown"}}}})
	if err != nil {
		t.Fatal(err)
	}
	live, err := plugins.NewLive("current", nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	live.Resolve = func(_ context.Context, plan string) (*plugins.PinSet, error) {
		calls[plan]++
		return set, nil
	}
	pin := func(plan string) context.Context {
		t.Helper()
		ctx, err := live.Pin(context.Background(), plugins.Work{Plan: plan}, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		return ctx
	}
	oldest := pin("oldest")
	pin("recent")
	for n := range 30 {
		pin(fmt.Sprintf("other_%d", n))
	}
	pin("oldest") // Reuse makes this the most recently used historical plan.
	pin("overflow")
	pin("oldest")
	pin("recent")
	if calls["oldest"] != 1 || calls["recent"] != 2 {
		t.Fatalf("resolved oldest %d times, recent %d; want 1 and 2 after LRU eviction", calls["oldest"], calls["recent"])
	}
	for n := range 40 {
		pin(fmt.Sprintf("later_%d", n))
	}
	pin("oldest")
	if calls["oldest"] != 2 || !live.Routed(oldest, "text/markdown") || live.Routed(context.Background(), "text/markdown") {
		t.Fatalf("evicted plan must reload, while existing work keeps its route: calls=%d pinned=%v current=%v", calls["oldest"], live.Routed(oldest, "text/markdown"), live.Routed(context.Background(), "text/markdown"))
	}
}
