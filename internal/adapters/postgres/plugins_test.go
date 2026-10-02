package postgres_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/app"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
)

const hashEmbedder = "../../../sdks/go/examples/hash-embedder/quivr-plugin.yaml"

var hashSpaces = map[string]string{"example.hash_embedder.small": plugins.SpaceServed, "example.hash_embedder.large": plugins.SpaceEvaluation}

// configured is the seed of startup pins.
func configured(t *testing.T, configs ...plugins.PinConfig) registry.Seed {
	t.Helper()
	pins, err := plugins.LoadPins(configs)
	if err != nil {
		t.Fatal(err)
	}
	return registry.FromPins(pins)
}

// embedderVersion is the sample ingestion plugin's manifest at another
// version, edited as a new build would be.
func embedderVersion(t *testing.T, version string, edits ...string) registry.Registration {
	t.Helper()
	raw, err := os.ReadFile(hashEmbedder)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "version: 0.1.0", "version: "+version, 1)
	for i := 0; i+1 < len(edits); i += 2 {
		text = strings.Replace(text, edits[i], edits[i+1], 1)
	}
	pin, err := plugins.LoadPinManifest([]byte(text), version, plugins.PinConfig{Endpoint: "http://127.0.0.1:9961", Spaces: hashSpaces})
	if err != nil {
		t.Fatal(err)
	}
	set, err := plugins.NewPinSet([]*plugins.Pin{pin})
	if err != nil {
		t.Fatal(err)
	}
	r := registry.FromPins(set).Registrations[0]
	r.State = registry.StateRegistered
	return r
}

func roles(p registry.Plan) map[string]string {
	out := map[string]string{}
	for _, a := range p.Roles {
		out[a.Role] = a.PluginID + "@" + a.Version
	}
	return out
}

// TestPluginConfigurationReconcilesAnEarlierPlan owns the startup contract on a
// database of its own: a registry THE-780 seeded before the ingestion role
// existed takes the configuration's roles as one new plan, written once when
// api and worker start together, the earlier plan staying readable; the same
// configuration again changes nothing.
func TestPluginConfigurationReconcilesAnEarlierPlan(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	// The registry reads the monitoring tables too: an alert-rule version
	// drains while Subscriptions pin it (THE-805).
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := postgres.PluginStore{Pool: pool}
	if applied, err := store.ApplyConfiguration(ctx, registry.Seed{}); err != nil || applied.Changed {
		t.Fatalf("a configuration without pins on an empty registry: %+v (%v)", applied, err)
	}
	if _, err := store.ActivePlan(ctx); err != registry.ErrNoPlan {
		t.Fatalf("plan before any configuration: %v, want ErrNoPlan", err)
	}

	// What THE-780 wrote: a registration without manifest, a plan without
	// ingestion, no configuration snapshot.
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_registrations(id,plugin_id,version,endpoint,manifest_digest,contributions,roles,state) VALUES('plugin_registration_legacy','pdf-text','0.1.0','http://127.0.0.1:9900','sha256:legacy','{normalizer}','{normalizer:application/pdf}','active');
 INSERT INTO pipeline_plans(id) VALUES('plan_legacy');
 INSERT INTO pipeline_plan_roles VALUES('plan_legacy','normalizer:application/pdf','plugin_registration_legacy');
 INSERT INTO active_pipeline_plan(plan_id) VALUES('plan_legacy')`); err != nil {
		t.Fatal(err)
	}
	seed := configured(t,
		plugins.PinConfig{Manifest: "../../../plugins/pdf-text/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9900", Routes: []plugins.RouteConfig{{MediaType: "application/pdf"}}},
		plugins.PinConfig{Manifest: hashEmbedder, Endpoint: "http://127.0.0.1:9960", Spaces: hashSpaces})
	var wg sync.WaitGroup
	results := make([]registry.Applied, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = store.ApplyConfiguration(ctx, seed)
		}(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || results[0].Changed == results[1].Changed || results[0].Plan != results[1].Plan {
		t.Fatalf("api and worker applying together: %+v, errors %v; want one new plan both follow", results, errs)
	}
	plan, err := store.ActivePlan(ctx)
	want := map[string]string{"ingestion": "example.hash_embedder@0.1.0", "normalizer:application/pdf": "pdf-text@0.1.0"}
	if err != nil || plan.ID != results[0].Plan || plan.Source != registry.SourceConfiguration || !reflect.DeepEqual(roles(plan), want) {
		t.Fatalf("reconciled plan %+v (%v), want roles %v from the configuration", plan, err, want)
	}
	if legacy, err := store.PipelinePlan(ctx, "plan_legacy"); err != nil || len(legacy.Roles) != 1 || legacy.Roles[0].RegistrationID != "plugin_registration_legacy" {
		t.Fatalf("the earlier plan stays readable: %+v (%v)", legacy, err)
	}
	states := map[string]string{}
	list, err := store.PluginRegistrations(ctx)
	for _, r := range list {
		states[r.ID] = r.State
	}
	if err != nil || len(list) != 3 || states["plugin_registration_legacy"] != registry.StateInactive || states[seed.Registrations[0].ID] != registry.StateActive || states[seed.Registrations[1].ID] != registry.StateActive {
		t.Fatalf("registration states %v (%v): the legacy one inactive, the configured ones active", states, err)
	}
	if again, err := store.ApplyConfiguration(ctx, seed); err != nil || again.Changed || again.Plan != plan.ID {
		t.Fatalf("the same configuration again: %+v (%v), want the plan unchanged", again, err)
	}
}

// TestPluginActivationCommitsWithTheSpaceRegistry owns registration and
// activation on real PostgreSQL: idempotent registration, one check claim at a
// time, and an activation that records a new plan with the vector spaces it
// registers in one transaction, so a space the registry refuses leaves the
// active plan as it was.
func TestPluginActivationCommitsWithTheSpaceRegistry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := postgres.PluginStore{Pool: pool}
	service := registry.Service{Store: store, Spaces: app.DeploymentSpaces}
	applied, err := store.ApplyConfiguration(ctx, configured(t, plugins.PinConfig{Manifest: hashEmbedder, Endpoint: "http://127.0.0.1:9960", Spaces: hashSpaces}))
	if err != nil {
		t.Fatal(err)
	}
	startup, _ := store.ActivePlan(ctx)
	_, members, _ := store.ActiveMembers(ctx)
	set, _, err := registry.Resolve(startup.Roles, members)
	if err != nil {
		t.Fatal(err)
	}
	if err = (postgres.ContentStore{Pool: pool}).RegisterSpaces(ctx, app.DeploymentSpaces(set)); err != nil {
		t.Fatal(err)
	}

	next := embedderVersion(t, "0.2.0")
	next.Fixtures = map[string][]byte{"article.json": []byte(`{"segment":{}}`), "inputs/article.txt": {0xff, 0x00}}
	stored, queued, err := store.RegisterPlugin(ctx, next, "register-0.2.0")
	if err != nil || !queued || stored.State != registry.StateRegistered || string(stored.Manifest) != string(next.Manifest) {
		t.Fatalf("registration: %+v queued %v (%v)", stored, queued, err)
	}
	if replay, queued, err := store.RegisterPlugin(ctx, next, "register-0.2.0"); err != nil || queued || replay.ID != next.ID {
		t.Fatalf("replay: %+v queued %v (%v), want the same registration and no second check", replay, queued, err)
	}
	otherFixtures := next
	otherFixtures.Fixtures = map[string][]byte{"article.json": []byte(`{}`)}
	if _, _, err := store.RegisterPlugin(ctx, otherFixtures, "register-0.2.0"); !errors.Is(err, registry.ErrIdempotencyConflict) {
		t.Fatalf("a key reused with other fixtures: %v, want ErrIdempotencyConflict", err)
	}
	changed := embedderVersion(t, "0.3.0", "dimensions: 16", "dimensions: 24")
	if _, _, err := store.RegisterPlugin(ctx, changed, "register-0.2.0"); !errors.Is(err, registry.ErrIdempotencyConflict) {
		t.Fatalf("a key reused for another registration: %v, want ErrIdempotencyConflict", err)
	}
	if _, _, err := store.RegisterPlugin(ctx, changed, "register-0.3.0"); err != nil {
		t.Fatal(err)
	}
	// Each check runs with the fixtures of the request that queued it: 0.3.0
	// is rejected without any, then checked again with those of a new key.
	settle := func(want map[string]map[string][]byte) {
		t.Helper()
		for range want {
			claimed, ok, err := store.ClaimCheck(ctx, time.Minute)
			if err != nil || !ok {
				t.Fatalf("claim: %v %v", ok, err)
			}
			if !reflect.DeepEqual(claimed.Fixtures, want[claimed.ID]) {
				t.Fatalf("%s@%s claimed with fixtures %q, want %q", claimed.PluginID, claimed.Version, claimed.Fixtures, want[claimed.ID])
			}
			if err = store.RecordCheck(ctx, claimed.ID, registry.CheckReport{Certified: claimed.Fixtures != nil, Passed: 1, Checks: []registry.CheckResult{}}); err != nil {
				t.Fatal(err)
			}
		}
		if _, ok, err := store.ClaimCheck(ctx, time.Minute); err != nil || ok {
			t.Fatalf("a claim with nothing waiting: %v %v", ok, err)
		}
	}
	settle(map[string]map[string][]byte{next.ID: next.Fixtures, changed.ID: nil})
	changed.Fixtures = otherFixtures.Fixtures
	if again, queued, err := store.RegisterPlugin(ctx, changed, "register-0.3.0-fixed"); err != nil || !queued || again.State != registry.StateRegistered {
		t.Fatalf("a rejected registration under a new key: %+v queued %v (%v), want checked again", again, queued, err)
	}
	settle(map[string]map[string][]byte{changed.ID: changed.Fixtures})

	// 0.3.0 changes the served space's dimensions under the same version.
	if _, err := service.Activate(ctx, operatorScope, changed.ID); !errors.Is(err, content.ErrSpaceChanged) {
		t.Fatalf("activating a changed space: %v, want ErrSpaceChanged", err)
	}
	if plan, _ := store.ActivePlan(ctx); plan.ID != applied.Plan {
		t.Fatalf("a refused activation changed the plan to %s", plan.ID)
	}
	plan, err := service.Activate(ctx, operatorScope, next.ID)
	if err != nil || plan.ID == applied.Plan || plan.Source != registry.SourceActivation || roles(plan)["ingestion"] != "example.hash_embedder@0.2.0" {
		t.Fatalf("activation: %+v (%v), want a new plan with 0.2.0 serving ingestion", plan, err)
	}
	if active, _ := store.ActivePlanID(ctx); active != plan.ID {
		t.Fatalf("active plan %s, want %s", active, plan.ID)
	}
	if earlier, err := store.PipelinePlan(ctx, applied.Plan); err != nil || roles(earlier)["ingestion"] != "example.hash_embedder@0.1.0" {
		t.Fatalf("the earlier plan stays readable: %+v (%v)", earlier, err)
	}
	var owner string
	if err = pool.QueryRow(ctx, `SELECT owner_plugin_version FROM vector_spaces WHERE id='example.hash_embedder.small@1'`).Scan(&owner); err != nil || owner != "0.2.0" {
		t.Fatalf("the served space's owner version %q (%v), want 0.2.0", owner, err)
	}
	if again, err := service.Activate(ctx, operatorScope, next.ID); err != nil || again.ID != plan.ID {
		t.Fatalf("activating the active registration again: %+v (%v), want the same plan", again, err)
	}
}

var operatorScope = corpus.Scope{Organization: "org_ops", Actions: []string{registry.Action}, Corpora: []string{"*"}}

// TestPinnedWorkDrainsTheRegistrationItNames owns the durable side of
// pinning work to its plan (THE-782) on real PostgreSQL: work keeps the plan
// it was first pinned to whatever plan is active when it retries, a
// registration an activation leaves out reads as draining with the count of
// work pinned to a plan naming it, and as inactive once that work is
// released, and each unreachable attempt is counted on the work.
func TestPinnedWorkDrainsTheRegistrationItNames(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := postgres.PluginStore{Pool: pool}
	seed := configured(t, plugins.PinConfig{Manifest: hashEmbedder, Endpoint: "http://127.0.0.1:9960", Spaces: hashSpaces})
	first, err := store.ApplyConfiguration(ctx, seed)
	if err != nil {
		t.Fatal(err)
	}
	_, members, _ := store.ActiveMembers(ctx)
	set, _, err := registry.Resolve(seed.Roles, members)
	if err != nil {
		t.Fatal(err)
	}
	if err = (postgres.ContentStore{Pool: pool}).RegisterSpaces(ctx, app.DeploymentSpaces(set)); err != nil {
		t.Fatal(err)
	}
	if pinned, _, err := store.PinWork(ctx, plugins.WorkIngestion, "org_a", "receipt_old", first.Plan); err != nil || pinned != first.Plan {
		t.Fatalf("pinning work to the active plan: %q (%v)", pinned, err)
	}

	next := embedderVersion(t, "0.2.0")
	if _, _, err = store.RegisterPlugin(ctx, next, "register-0.2.0"); err != nil {
		t.Fatal(err)
	}
	if err = store.RecordCheck(ctx, next.ID, registry.CheckReport{Certified: true, Checks: []registry.CheckResult{}}); err != nil {
		t.Fatal(err)
	}
	second, err := registry.Service{Store: store, Spaces: app.DeploymentSpaces}.Activate(ctx, operatorScope, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pinned, _, err := store.PinWork(ctx, plugins.WorkIngestion, "org_a", "receipt_old", second.ID); err != nil || pinned != first.Plan {
		t.Fatalf("a retry after the activation is pinned to %q (%v), want its first plan %s", pinned, err, first.Plan)
	}
	if pinned, _, err := store.PinWork(ctx, plugins.WorkIngestion, "org_a", "receipt_new", second.ID); err != nil || pinned != second.ID {
		t.Fatalf("new work is pinned to %q (%v), want the active plan", pinned, err)
	}
	if plan, byID, err := store.PlanMembers(ctx, first.Plan); err != nil || roles(plan)["ingestion"] != "example.hash_embedder@0.1.0" || len(byID) != 1 {
		t.Fatalf("the pinned plan's members: %+v %v (%v)", plan, byID, err)
	}
	for want := 1; want <= 2; want++ {
		if n, err := store.CountUnavailable(ctx, plugins.WorkIngestion, "org_a", "receipt_old"); err != nil || n != want {
			t.Fatalf("unavailable attempt %d counted as %d (%v)", want, n, err)
		}
	}

	state := func(id string) (string, int) {
		t.Helper()
		r, err := store.PluginRegistration(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return r.State, r.PinnedWork
	}
	old := seed.Registrations[0].ID
	if s, n := state(old); s != registry.StateDraining || n != 1 {
		t.Fatalf("0.1.0 after the activation: %s with %d pinned, want draining with 1", s, n)
	}
	if s, n := state(next.ID); s != registry.StateActive || n != 1 {
		t.Fatalf("0.2.0 after the activation: %s with %d pinned, want active with 1", s, n)
	}
	if err = store.ReleaseWork(ctx, plugins.WorkIngestion, "org_a", "receipt_old"); err != nil {
		t.Fatal(err)
	}
	if s, n := state(old); s != registry.StateInactive || n != 0 {
		t.Fatalf("0.1.0 once its work is released: %s with %d pinned, want inactive", s, n)
	}
}

// TestRollbackRestoresThePreviousPlan owns the rollback on real PostgreSQL
// (THE-783): with no earlier plan there is nothing to return to; after an
// activation, one call records the previous plan's roles as a new plan naming
// the one it replaced, the version it brings back serves again and the one it
// takes out drains; with pinned_work=stop only work pinned to a plan naming
// that version is stopped; the key replays the plan and refuses another
// request; a plugin that does not answer leaves the plan as it was; and the
// history lists the plans newest first.
func TestRollbackRestoresThePreviousPlan(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := postgres.PluginStore{Pool: pool}
	var unreachable error
	service := registry.Service{Store: store, Spaces: app.DeploymentSpaces, Reach: func(context.Context, registry.Registration) error { return unreachable }}
	seed := configured(t, plugins.PinConfig{Manifest: hashEmbedder, Endpoint: "http://127.0.0.1:9960", Spaces: hashSpaces})
	first, err := store.ApplyConfiguration(ctx, seed)
	if err != nil {
		t.Fatal(err)
	}
	_, members, _ := store.ActiveMembers(ctx)
	set, _, err := registry.Resolve(seed.Roles, members)
	if err != nil {
		t.Fatal(err)
	}
	if err = (postgres.ContentStore{Pool: pool}).RegisterSpaces(ctx, app.DeploymentSpaces(set)); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Rollback(ctx, operatorScope, registry.RollbackRequest{Key: "too-early"}); !errors.Is(err, registry.ErrNoPreviousPlan) {
		t.Fatalf("a rollback of the first plan: %v, want ErrNoPreviousPlan", err)
	}

	next := embedderVersion(t, "0.2.0")
	if _, _, err = store.RegisterPlugin(ctx, next, "register-0.2.0"); err != nil {
		t.Fatal(err)
	}
	if err = store.RecordCheck(ctx, next.ID, registry.CheckReport{Certified: true, Checks: []registry.CheckResult{}}); err != nil {
		t.Fatal(err)
	}
	bad, err := service.Activate(ctx, operatorScope, next.ID)
	if err != nil || bad.PreviousPlanID != first.Plan {
		t.Fatalf("activation: %+v (%v), want a plan naming %s as previous", bad, err, first.Plan)
	}
	for _, w := range []struct{ id, plan string }{{"receipt_on_old", first.Plan}, {"receipt_on_bad", bad.ID}} {
		if _, _, err = store.PinWork(ctx, plugins.WorkIngestion, "org_a", w.id, w.plan); err != nil {
			t.Fatal(err)
		}
	}

	unreachable = errors.New("connection refused")
	if _, err = service.Rollback(ctx, operatorScope, registry.RollbackRequest{Key: "down"}); !errors.Is(err, registry.ErrUnreachable) || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("a rollback to a plugin that does not answer: %v, want ErrUnreachable with the cause", err)
	}
	if active, _ := store.ActivePlanID(ctx); active != bad.ID {
		t.Fatalf("a refused rollback changed the plan to %s", active)
	}
	unreachable = nil
	stop := registry.RollbackRequest{Key: "rollback-1", PinnedWork: registry.PinnedWorkStop}
	back, err := service.Rollback(ctx, operatorScope, stop)
	if err != nil || back.Source != registry.SourceRollback || back.PreviousPlanID != bad.ID || roles(back)["ingestion"] != "example.hash_embedder@0.1.0" {
		t.Fatalf("rollback: %+v (%v), want a rollback plan after %s with 0.1.0 serving ingestion", back, err, bad.ID)
	}
	if active, _ := store.ActivePlanID(ctx); active != back.ID {
		t.Fatalf("active plan %s, want the rollback's %s", active, back.ID)
	}
	if r, _ := store.PluginRegistration(ctx, seed.Registrations[0].ID); r.State != registry.StateActive {
		t.Fatalf("0.1.0 after the rollback: %s, want active", r.State)
	}
	if r, _ := store.PluginRegistration(ctx, next.ID); r.State != registry.StateDraining || r.PinnedWork != 1 {
		t.Fatalf("0.2.0 after the rollback: %s with %d pinned, want draining with the work pinned to it", r.State, r.PinnedWork)
	}
	for id, want := range map[string]bool{"receipt_on_bad": true, "receipt_on_old": false} {
		if _, stopped, err := store.PinWork(ctx, plugins.WorkIngestion, "org_a", id, back.ID); err != nil || stopped != want {
			t.Fatalf("%s stopped: %v (%v), want %v", id, stopped, err, want)
		}
		if stopped, err := store.WorkStopped(ctx, plugins.WorkIngestion, "org_a", id); err != nil || stopped != want {
			t.Fatalf("%s read as stopped: %v (%v), want %v", id, stopped, err, want)
		}
	}

	if replay, err := service.Rollback(ctx, operatorScope, stop); err != nil || replay.ID != back.ID {
		t.Fatalf("the same request again: %+v (%v), want plan %s", replay, err, back.ID)
	}
	if _, err = service.Rollback(ctx, operatorScope, registry.RollbackRequest{Key: "rollback-1"}); !errors.Is(err, registry.ErrIdempotencyConflict) {
		t.Fatalf("the key with another request: %v, want ErrIdempotencyConflict", err)
	}
	named, err := service.Rollback(ctx, operatorScope, registry.RollbackRequest{Key: "rollback-2", Plan: bad.ID})
	if err != nil || named.PreviousPlanID != back.ID || roles(named)["ingestion"] != "example.hash_embedder@0.2.0" {
		t.Fatalf("a rollback to a named plan: %+v (%v), want 0.2.0 serving again", named, err)
	}
	if replay, err := service.Rollback(ctx, operatorScope, registry.RollbackRequest{Key: "rollback-2", Plan: bad.ID, PinnedWork: registry.PinnedWorkDrain}); err != nil || replay.ID != named.ID {
		t.Fatalf("a retry naming the default drain: %+v (%v), want plan %s", replay, err, named.ID)
	}
	history, err := store.PipelinePlans(ctx, 2)
	if err != nil || len(history) != 2 || history[0].ID != named.ID || history[1].ID != back.ID {
		t.Fatalf("history %+v (%v), want the two latest plans newest first", history, err)
	}
}

// Per-provider retrieval roles survive persistence: an upgrade drains only the
// old provider, rollback restores it, and releasing its pinned work makes it
// inactive while the other provider stays active.
func TestRetrievalProvidersPersistIndependentLifecycles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := postgres.PluginStore{Pool: pool}
	seed := configured(t,
		plugins.PinConfig{Manifest: "../../../plugins/core-retrieve/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9970"},
		plugins.PinConfig{Manifest: "../../../sdks/go/examples/fusion-retriever/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9971"},
	)
	first, err := store.ApplyConfiguration(ctx, seed)
	if err != nil {
		t.Fatal(err)
	}
	old, other := seed.Registrations[0], seed.Registrations[1]
	if _, _, err = store.PinWork(ctx, plugins.WorkIngestion, "org_a", "search_provider_work", first.Plan); err != nil {
		t.Fatal(err)
	}
	service := registry.Service{Store: store, Reach: func(context.Context, registry.Registration) error { return nil }}
	next, err := service.Register(ctx, operatorScope, registry.Request{Key: "upgrade-search",
		Manifest: []byte(strings.Replace(string(old.Manifest), "version: 1.0.0", "version: 1.1.0", 1)), Endpoint: old.Endpoint})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecordCheck(ctx, next.ID, registry.CheckReport{Certified: true, Checks: []registry.CheckResult{}}); err != nil {
		t.Fatal(err)
	}
	check := func(id, want string) {
		t.Helper()
		r, err := store.PluginRegistration(ctx, id)
		if err != nil || r.State != want {
			t.Fatalf("registration %s: %+v (%v), want %s", id, r, err, want)
		}
	}
	activate := func() {
		t.Helper()
		p, err := service.Activate(ctx, operatorScope, next.ID)
		if err != nil || roles(p)["retrieval:core.retrieve"] != "core.retrieve@1.1.0" || roles(p)["retrieval:example.fusion_retriever"] != "example.fusion_retriever@0.1.0" {
			t.Fatalf("activation %+v: %v", p, err)
		}
		check(old.ID, registry.StateDraining)
		check(other.ID, registry.StateActive)
	}
	activate()
	back, err := service.Rollback(ctx, operatorScope, registry.RollbackRequest{Key: "restore-search", Plan: first.Plan})
	if err != nil || roles(back)["retrieval:core.retrieve"] != "core.retrieve@1.0.0" || roles(back)["retrieval:example.fusion_retriever"] != "example.fusion_retriever@0.1.0" {
		t.Fatalf("rollback %+v: %v", back, err)
	}
	check(old.ID, registry.StateActive)
	check(other.ID, registry.StateActive)
	activate()
	if err = store.ReleaseWork(ctx, plugins.WorkIngestion, "org_a", "search_provider_work"); err != nil {
		t.Fatal(err)
	}
	check(old.ID, registry.StateInactive)
	check(other.ID, registry.StateActive)
}

// A plan recorded before per-provider roles stays immutable and remains a
// rollback target after startup reconciliation and an operator upgrade.
func TestLegacyRetrievalPlanCanReconcileAndRollback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := postgres.PluginStore{Pool: pool}
	seed := configured(t, plugins.PinConfig{Manifest: "../../../plugins/core-retrieve/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:9970"})
	legacy := seed
	legacy.Roles = append([]registry.Assignment(nil), seed.Roles...)
	legacy.Roles[0].Role = "retrieval"
	legacy.Registrations = append([]registry.Registration(nil), seed.Registrations...)
	legacy.Registrations[0].Roles = []string{"retrieval"}
	first, err := store.ApplyConfiguration(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	service := registry.Service{Store: store, Reach: func(context.Context, registry.Registration) error { return nil }}
	old := seed.Registrations[0]
	next, err := service.Register(ctx, operatorScope, registry.Request{Key: "upgrade-legacy-search",
		Manifest: []byte(strings.Replace(string(old.Manifest), "version: 1.0.0", "version: 1.1.0", 1)), Endpoint: old.Endpoint})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecordCheck(ctx, next.ID, registry.CheckReport{Certified: true, Checks: []registry.CheckResult{}}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Activate(ctx, operatorScope, next.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ApplyConfiguration(ctx, seed); err != nil {
		t.Fatal(err)
	}
	active, err := store.ActivePlan(ctx)
	if err != nil || roles(active)["retrieval:core.retrieve"] != "core.retrieve@1.1.0" {
		t.Fatalf("startup preserved operator upgrade: %+v (%v)", active, err)
	}
	back, err := service.Rollback(ctx, operatorScope, registry.RollbackRequest{Key: "restore-legacy-search", Plan: first.Plan})
	if err != nil || roles(back)["retrieval:core.retrieve"] != "core.retrieve@1.0.0" {
		t.Fatalf("rollback to recorded singleton: %+v (%v)", back, err)
	}
	recorded, err := store.PipelinePlan(ctx, first.Plan)
	if err != nil || roles(recorded)["retrieval"] != "core.retrieve@1.0.0" {
		t.Fatalf("historical plan remained readable and unchanged: %+v (%v)", recorded, err)
	}
}
