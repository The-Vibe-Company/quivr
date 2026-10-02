package registry_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
)

// TestRollbackRestoresTheTargetsRoles owns how a rollback computes the new
// plan: exactly the target plan's roles, bringing back its registrations even
// when they drain and retiring the ones it leaves out, and refusing what an
// activation refuses. A target with the active plan's roles changes nothing.
func TestRollbackRestoresTheTargetsRoles(t *testing.T) {
	embedderV1 := registered(t, manifest("example.embedder", "1.0.0", ingestion("example.embedder.small")))
	embedderV1.State = registry.StateDraining
	embedderV2 := registered(t, manifest("example.embedder", "2.0.0", ingestion("example.embedder.small")))
	embedderV2.State = registry.StateActive
	markdown := registered(t, manifest("example.markdown", "1.0.0", normalizer("text/markdown")), "text/markdown")
	active, members := plan(t, embedderV2, markdown)
	target, targetMembers := plan(t, embedderV1, markdown)
	target.ID = "plan_target"
	for id, r := range targetMembers {
		members[id] = r
	}

	back, err := registry.PlanRollback(active, target, members, nil)
	if err != nil || back.Unchanged || !reflect.DeepEqual(roleMap(back.Roles), roleMap(target.Roles)) || !reflect.DeepEqual(back.Retired, []string{embedderV2.ID}) || len(back.Returning) != 1 || back.Returning[0].ID != embedderV1.ID {
		t.Fatalf("rollback: %+v (%v); want the target's roles, 2.0.0 retired and the draining 1.0.0 back", back, err)
	}
	// An earlier alert-rule version comes back like any plugin (THE-805).
	ruleV1, ruleV2 := registered(t, manifest("example.rule", "1.0.0", rule)), registered(t, manifest("example.rule", "2.0.0", rule))
	upgraded, ruleMembers := plan(t, embedderV2, ruleV2)
	earlier, earlierMembers := plan(t, embedderV2, ruleV1)
	for id, r := range earlierMembers {
		ruleMembers[id] = r
	}
	if back, err := registry.PlanRollback(upgraded, earlier, ruleMembers, nil); err != nil || roleMap(back.Roles)["subscription:example.rule"] != "example.rule@1.0.0" || !reflect.DeepEqual(back.Retired, []string{ruleV2.ID}) {
		t.Fatalf("an alert-rule rollback: %+v (%v); want 1.0.0 back, 2.0.0 retired", back, err)
	}
	if same, err := registry.PlanRollback(active, active, members, nil); err != nil || !same.Unchanged {
		t.Fatalf("a rollback to the active roles: %+v (%v), want unchanged", same, err)
	}

	with := func(extra ...registry.Registration) (registry.Plan, map[string]registry.Registration) {
		p, byID := plan(t, extra...)
		p.ID = "plan_target"
		for id, r := range members {
			byID[id] = r
		}
		return p, byID
	}
	rejected := embedderV1
	rejected.State = registry.StateRejected
	unkept := embedderV1
	unkept.Manifest = nil
	for name, c := range map[string]struct {
		target []registry.Registration
		// member replaces the target's registration of the same id.
		member *registry.Registration
		want   error
		says   string
	}{
		"rejected member":   {target: []registry.Registration{embedderV1}, member: &rejected, want: registry.ErrNotValidated, says: "rejected"},
		"manifest not kept": {target: []registry.Registration{embedderV1}, member: &unkept, want: registry.ErrConflict, says: registry.CodePlanUnresolvable},
	} {
		p, byID := with(c.target...)
		for _, r := range c.target {
			byID[r.ID] = r
		}
		if c.member != nil {
			byID[c.member.ID] = *c.member
		}
		_, err := registry.PlanRollback(active, p, byID, nil)
		if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want %v naming %q", name, err, c.want, c.says)
		}
	}
	refused := errors.New("connector kind rss is built in")
	if _, err := registry.PlanRollback(active, target, members, func(*plugins.PinSet) error { return refused }); !errors.Is(err, registry.ErrConflict) || !strings.Contains(err.Error(), refused.Error()) {
		t.Fatalf("a check of the running engine refuses: %v", err)
	}
}
