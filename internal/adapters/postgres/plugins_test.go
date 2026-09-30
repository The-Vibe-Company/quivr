package postgres_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"regexp"
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
	if err := postgres.MigrateFS(ctx, pool, embedded(t, regexp.MustCompile(`_plugin_(registry|activation)\.sql$`))); err != nil {
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
	stored, queued, err := store.RegisterPlugin(ctx, next, "register-0.2.0")
	if err != nil || !queued || stored.State != registry.StateRegistered || string(stored.Manifest) != string(next.Manifest) {
		t.Fatalf("registration: %+v queued %v (%v)", stored, queued, err)
	}
	if replay, queued, err := store.RegisterPlugin(ctx, next, "register-0.2.0"); err != nil || queued || replay.ID != next.ID {
		t.Fatalf("replay: %+v queued %v (%v), want the same registration and no second check", replay, queued, err)
	}
	changed := embedderVersion(t, "0.3.0", "dimensions: 16", "dimensions: 24")
	if _, _, err := store.RegisterPlugin(ctx, changed, "register-0.2.0"); !errors.Is(err, registry.ErrIdempotencyConflict) {
		t.Fatalf("a key reused for another registration: %v, want ErrIdempotencyConflict", err)
	}
	if _, _, err := store.RegisterPlugin(ctx, changed, "register-0.3.0"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		claimed, ok, err := store.ClaimCheck(ctx, time.Minute)
		if err != nil || !ok {
			t.Fatalf("claim: %v %v", ok, err)
		}
		if err = store.RecordCheck(ctx, claimed.ID, registry.CheckReport{Certified: true, Passed: 1, Checks: []registry.CheckResult{}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := store.ClaimCheck(ctx, time.Minute); err != nil || ok {
		t.Fatalf("a third claim with nothing waiting: %v %v", ok, err)
	}

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
