package postgres_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/app"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// An upgraded deployment whose ingestion spaces changed (THE-787): after
// `quivr migrate` a new Corpus starts on the registered spaces, a Corpus that
// was never rebuilt keeps the legacy default serving it, a rebuilt Corpus
// keeps its route, and running migrate again moves nothing. Once the kept
// Corpus is rebuilt, its objects in the former default are purged like those
// of the active default. It runs on a scratch database because it moves the
// deployment's default generation.
func TestNewCorporaStartOnTheRegisteredSpaces(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	store := contentStores(pool)
	if err := app.BootstrapDatabase(ctx, pool, app.DeploymentSpaces(nil)); err != nil {
		t.Fatal(err)
	}
	// The default as a deployment built it before named spaces: the E5 space alone.
	var legacy string
	if err := pool.QueryRow(ctx, `UPDATE projection_generations SET spaces='[]',spaces_projected=false WHERE active RETURNING id`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	f := controlFixture{t: t, ctx: ctx, pool: pool, org: fmt.Sprintf("adapter-default-%d", time.Now().UnixNano()), store: store}
	kept, rebuilt := f.corpus("kept"), f.corpus("rebuilt")
	first := f.rebuild(rebuilt, "first")
	if done, err := f.activate(first.ID); err != nil || !done {
		t.Fatalf("activate %s: %v %v", first.ID, done, err)
	}
	// The same space registered again moves nothing.
	if err := app.BootstrapDatabase(ctx, pool, app.DeploymentSpaces(nil)); err != nil {
		t.Fatal(err)
	}
	var current string
	if err := pool.QueryRow(ctx, `SELECT id FROM projection_generations WHERE active`).Scan(&current); err != nil || current != legacy {
		t.Fatalf("the default moved to %s while its space is still the registered one: %v", current, err)
	}

	served, evaluation := pluginSpace("example.words", "example.words.small", 4, content.SpaceServed), pluginSpace("example.words", "example.words.large", 6, content.SpaceEvaluation)
	if err := app.BootstrapDatabase(ctx, pool, []content.RegisteredSpace{served, evaluation}); err != nil {
		t.Fatal(err)
	}
	if got := f.routed(kept); got != legacy {
		t.Fatalf("a Corpus never rebuilt moved to %s, want the legacy default %s", got, legacy)
	}
	if got := f.routed(rebuilt); got != first.TargetGenerationID {
		t.Fatalf("a rebuilt Corpus moved to %s, want its route %s", got, first.TargetGenerationID)
	}
	g, err := store.Generation(ctx, f.org, f.corpus("after"))
	if err != nil {
		t.Fatal(err)
	}
	if g.ID == legacy || !g.SpacesProjected || g.SpaceID != served.ID || fmt.Sprint(g.VectorSpaces()) != fmt.Sprint([]string{served.ID, evaluation.ID}) {
		t.Fatalf("a new Corpus starts on %+v, want the registered spaces %s and %s", g, served.ID, evaluation.ID)
	}
	if err := app.BootstrapDatabase(ctx, pool, []content.RegisteredSpace{served, evaluation}); err != nil {
		t.Fatal(err)
	}
	if again := f.routed(f.corpus("later")); again != g.ID {
		t.Fatalf("migrate again moved the default from %s to %s", g.ID, again)
	}

	second := f.rebuild(kept, "second")
	if done, err := f.activate(second.ID); err != nil || !done {
		t.Fatalf("activate %s: %v %v", second.ID, done, err)
	}
	notice(t, ctx, store)
	want := []string{}
	for _, c := range []string{kept, rebuilt} {
		for _, d := range []string{legacy, g.ID} {
			want = append(want, "generation:"+c+":"+d+":")
		}
	}
	sort.Strings(want)
	if got := noticed(t, ctx, pool, f.org); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("noticed\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
