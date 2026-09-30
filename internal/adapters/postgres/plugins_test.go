package postgres_test

import (
	"context"
	"reflect"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
)

// TestPluginRegistrySeedsOnceFromTheConfiguration owns the seed contract on a
// database of its own (the shared one is seeded by the running stack): api and
// worker seeding an empty registry together write it once; a later start with
// another configuration changes nothing and reports the differences.
func TestPluginRegistrySeedsOnceFromTheConfiguration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := postgres.MigrateFS(ctx, pool, embedded(t, regexp.MustCompile(`_plugin_registry\.sql$`))); err != nil {
		t.Fatal(err)
	}
	store := postgres.PluginStore{Pool: pool}
	service := registry.Service{Store: store}

	if seeded, err := store.SeedPlugins(ctx, registry.Seed{}); err != nil || seeded {
		t.Fatalf("a configuration without pins seeded %v (%v)", seeded, err)
	}
	if _, err := store.ActivePlan(ctx); err != registry.ErrNoPlan {
		t.Fatalf("plan before any seed: %v, want ErrNoPlan", err)
	}

	pdf := registration("pdf-text", "0.1.0", "normalizer:application/pdf")
	rss := registration("connector.rss", "1.0.0", "connector:rss")
	first := registry.Seed{Registrations: []registry.Registration{pdf, rss}, Roles: []registry.Assignment{
		{Role: "connector:rss", RegistrationID: rss.ID}, {Role: "normalizer:application/pdf", RegistrationID: pdf.ID}}}
	var wg sync.WaitGroup
	results := make([]bool, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = store.SeedPlugins(ctx, first)
		}(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || results[0] == results[1] {
		t.Fatalf("concurrent seeds: seeded %v, errors %v; want exactly one seed", results, errs)
	}
	registrations, err := service.Store.PluginRegistrations(ctx)
	if err != nil || len(registrations) != 2 {
		t.Fatalf("registrations after seed: %+v (%v)", registrations, err)
	}
	for _, r := range registrations {
		if r.State != registry.StateActive || r.ArtifactDigest != "" || r.CreatedAt.IsZero() {
			t.Fatalf("seeded registration %+v: want active, no artifact digest, a creation time", r)
		}
	}
	plan, err := store.ActivePlan(ctx)
	want := []registry.Assignment{{Role: "connector:rss", RegistrationID: rss.ID, PluginID: "connector.rss", Version: "1.0.0"}, {Role: "normalizer:application/pdf", RegistrationID: pdf.ID, PluginID: "pdf-text", Version: "0.1.0"}}
	if err != nil || plan.ID == "" || !reflect.DeepEqual(plan.Roles, want) {
		t.Fatalf("active plan %+v (%v), want roles %+v", plan, err, want)
	}

	// Restart with pdf-text upgraded and rss removed from the configuration.
	upgraded := registration("pdf-text", "0.2.0", "normalizer:application/pdf")
	next := registry.Seed{Registrations: []registry.Registration{upgraded}, Roles: []registry.Assignment{{Role: "normalizer:application/pdf", RegistrationID: upgraded.ID, PluginID: "pdf-text", Version: "0.2.0"}}}
	seeded, differences, err := service.SeedFrom(ctx, next)
	if err != nil || seeded {
		t.Fatalf("second start seeded %v (%v); the registry must stay as it is", seeded, err)
	}
	wantDifferences := []registry.Difference{
		{Role: "connector:rss", Active: "connector.rss@1.0.0 [" + rss.ID + "]"},
		{Role: "normalizer:application/pdf", Configured: "pdf-text@0.2.0 [" + upgraded.ID + "]", Active: "pdf-text@0.1.0 [" + pdf.ID + "]"},
	}
	if !reflect.DeepEqual(differences, wantDifferences) {
		t.Fatalf("differences %+v, want %+v", differences, wantDifferences)
	}
	after, err := store.ActivePlan(ctx)
	if err != nil || !reflect.DeepEqual(after, plan) {
		t.Fatalf("plan after a differing start %+v (%v), want unchanged %+v", after, err, plan)
	}
	if again, _ := service.Store.PluginRegistrations(ctx); !reflect.DeepEqual(again, registrations) {
		t.Fatalf("registrations after a differing start %+v, want unchanged %+v", again, registrations)
	}
	if _, differences, err = service.SeedFrom(ctx, first); err != nil || len(differences) != 0 {
		t.Fatalf("same configuration again: differences %+v (%v), want none", differences, err)
	}
}

func registration(pluginID, version, role string) registry.Registration {
	endpoint, digest := "http://127.0.0.1:9900", "sha256:"+pluginID+version
	return registry.Registration{ID: registry.RegistrationID(pluginID, version, digest, endpoint), PluginID: pluginID, Version: version, Endpoint: endpoint, ManifestDigest: digest, Contributions: []string{"normalizer"}, Roles: []string{role}, State: registry.StateActive}
}
