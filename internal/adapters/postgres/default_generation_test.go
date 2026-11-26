package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
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
	if err := app.BootstrapDatabase(ctx, pool, app.Config{}.DeploymentSpaces(nil)); err != nil {
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
	if err := app.BootstrapDatabase(ctx, pool, app.Config{}.DeploymentSpaces(nil)); err != nil {
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
	// A changed index setting moves new Corpora onto a default that records
	// it; a Corpus created before keeps the index it was built with.
	served.Index = &content.VectorIndex{Quantization: content.QuantizationRQ1, RescoreLimit: 64}
	if err := app.BootstrapDatabase(ctx, pool, []content.RegisteredSpace{served, evaluation}); err != nil {
		t.Fatal(err)
	}
	if got := f.routed(f.corpus("later")); got != g.ID {
		t.Fatalf("a Corpus created before the index change moved to %s, want %s", got, g.ID)
	}
	indexed, err := store.Generation(ctx, f.org, f.corpus("indexed"))
	if err != nil {
		t.Fatal(err)
	}
	if indexed.ID == g.ID || len(indexed.Spaces) != 2 || indexed.Spaces[0].Index == nil || *indexed.Spaces[0].Index != *served.Index || indexed.Spaces[1].Index != nil {
		t.Fatalf("a new Corpus after the index change starts on %s with spaces %+v, want a new default recording %+v", indexed.ID, indexed.Spaces, *served.Index)
	}
	if err := app.BootstrapDatabase(ctx, pool, []content.RegisteredSpace{served, evaluation}); err != nil {
		t.Fatal(err)
	}
	if again := f.routed(f.corpus("indexed-later")); again != indexed.ID {
		t.Fatalf("an unchanged index setting moved the default from %s to %s", indexed.ID, again)
	}
}

func TestCollectionUpgradeKeepsExistingRoutes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	spaces := app.Config{}.DeploymentSpaces(nil)
	if err := app.BootstrapDatabase(ctx, pool, spaces); err != nil {
		t.Fatal(err)
	}
	var legacy string
	if err := pool.QueryRow(ctx, `UPDATE projection_generations SET collection='QuivrTextV4' WHERE active RETURNING id`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	f := controlFixture{t: t, ctx: ctx, pool: pool, org: "adapter-collection-upgrade", store: store}
	old := f.corpus("old")
	if err := app.BootstrapDatabase(ctx, pool, spaces); err != nil {
		t.Fatal(err)
	}
	got, err := store.Generation(ctx, f.org, old)
	if err != nil || got.Collection != "QuivrTextV4" || got.ID != legacy {
		t.Fatalf("upgrade moved an existing Corpus: %+v (%v)", got, err)
	}

	duringRoll := f.corpus("previous-api-still-serving")
	if g, err := store.Generation(ctx, f.org, duringRoll); err != nil || g.ID != legacy {
		t.Fatalf("the previous API could not create a legacy Corpus during rollout: %+v (%v)", g, err)
	}

	projection := &recordedCorpusProjection{failNextEnsure: true}
	corpora := postgres.Store{Pool: pool, Projection: projection, TenantCollection: "QuivrTextV5"}
	input := corpus.CreateInput{Key: "fresh", Name: "Fresh"}
	lost, _, err := corpora.Create(ctx, f.org, input)
	if err == nil {
		t.Fatal("expected the tenant provisioning response to be lost")
	}
	fresh, conflict, err := corpora.Create(ctx, f.org, input)
	if err != nil || conflict || fresh.ID != lost.ID {
		t.Fatalf("creation retry with the same tenant identity: got %s, prior %s, conflict %v (%v)", fresh.ID, lost.ID, conflict, err)
	}
	route, err := store.Generation(ctx, f.org, fresh.ID)
	if err != nil || route.Collection != "QuivrTextV5" || route.ID == legacy {
		t.Fatalf("new Corpus route: %+v (%v)", route, err)
	}
	if replay, conflict, err := corpora.Create(ctx, f.org, input); err != nil || conflict || replay.ID != fresh.ID {
		t.Fatalf("creation replay: %+v %v (%v)", replay, conflict, err)
	}
	if c, err := corpora.Archive(ctx, f.org, fresh.ID, true); err != nil || !c.Archived {
		t.Fatalf("archive: %+v (%v)", c, err)
	}
	projection.failNextActivity = true
	if worked, err := corpora.ReconcileTenantActivity(ctx); !worked || err == nil {
		t.Fatalf("expected temporary provider failure after archive commit, got %v (%v)", worked, err)
	}
	if c, err := corpora.Read(ctx, f.org, fresh.ID); err != nil || !c.Archived {
		t.Fatalf("provider outage changed committed archive: %+v (%v)", c, err)
	}
	if worked, err := corpora.ReconcileTenantActivity(ctx); worked || err != nil {
		t.Fatalf("provider failure did not back off: %v (%v)", worked, err)
	}
	if c, err := corpora.Archive(ctx, f.org, fresh.ID, false); err != nil || c.Archived {
		t.Fatalf("unarchive: %+v (%v)", c, err)
	}
	if worked, err := corpora.ReconcileTenantActivity(ctx); !worked || err != nil {
		t.Fatalf("unarchive should supersede the pending archive retry: %v (%v)", worked, err)
	}
	if worked, err := corpora.ReconcileTenantActivity(ctx); worked || err != nil {
		t.Fatalf("activity remained queued after successful reconciliation: %v (%v)", worked, err)
	}
	ensure := "ensure/QuivrTextV5/" + f.org + "/" + fresh.ID + "/" + route.ID
	want := []string{
		ensure, ensure,
		"false/QuivrTextV5/" + f.org + "/" + fresh.ID + "/" + route.ID,
		"true/QuivrTextV5/" + f.org + "/" + fresh.ID + "/" + route.ID,
	}
	if !slices.Equal(projection.calls, want) {
		t.Fatalf("projection lifecycle calls %v, want %v", projection.calls, want)
	}

	operations := postgres.OperationStore{Pool: pool, RebuildCollection: "QuivrTextV5"}
	op, err := operations.AcceptRebuild(ctx, f.org, old, "switch-collection", []byte(`{"idempotency_key":"switch-collection"}`))
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := store.Generation(ctx, f.org, old); err != nil || pending.ID != legacy {
		t.Fatalf("queued rebuild switched the serving route: %+v (%v)", pending, err)
	}
	rebuilder := postgres.RebuildStore{Pool: pool, Projection: projection}
	target, err := rebuilder.BeginRebuild(ctx, f.org, op.ID)
	wantEnsure := "ensure/QuivrTextV5/" + f.org + "/" + old + "/" + op.TargetGenerationID
	if err != nil || target.Generation.Collection != "QuivrTextV5" || projection.calls[len(projection.calls)-1] != wantEnsure {
		t.Fatalf("rebuild must provision its target before coverage work: %+v, calls %v (%v)", target, projection.calls, err)
	}
	if _, err := corpora.Archive(ctx, f.org, old, true); err != nil {
		t.Fatal(err)
	}
	projection.failNextActivity = true
	if done, err := rebuilder.ActivateRebuild(ctx, f.org, op.ID); err == nil || done {
		t.Fatalf("failed temperature acknowledgement should defer cutover: %v (%v)", done, err)
	}
	if _, err := corpora.Archive(ctx, f.org, old, false); err != nil {
		t.Fatal(err)
	}
	if done, err := rebuilder.ActivateRebuild(ctx, f.org, op.ID); err != nil || !done {
		t.Fatalf("activate rebuild: %v (%v)", done, err)
	}
	wantActive := "true/QuivrTextV5/" + f.org + "/" + old + "/" + op.TargetGenerationID
	if projection.calls[len(projection.calls)-1] != wantActive {
		t.Fatalf("activation retry must warm the target of an unarchived Corpus: %v", projection.calls)
	}
	if rebuilt, err := store.Generation(ctx, f.org, old); err != nil || rebuilt.ID != op.TargetGenerationID || rebuilt.Collection != "QuivrTextV5" {
		t.Fatalf("activated rebuild route: %+v (%v)", rebuilt, err)
	}
	second, err := operations.AcceptRebuild(ctx, f.org, fresh.ID, "refresh-tenant", []byte(`{"idempotency_key":"refresh-tenant"}`))
	if err != nil {
		t.Fatal(err)
	}
	if done, err := f.activate(second.ID); err != nil || !done {
		t.Fatalf("activate native rebuild: %v (%v)", done, err)
	}
	purges := postgres.PurgeStore{Pool: pool, TenantCollection: "QuivrTextV5"}
	if _, err := purges.NoticePurges(ctx, 100); err != nil {
		t.Fatal(err)
	}
	gotPurges := noticed(t, ctx, pool, f.org)
	expectedPurges := []string{"generation:" + old + ":" + legacy + ":", "generation:" + fresh.ID + ":" + route.ID + ":"}
	sort.Strings(expectedPurges)
	if !slices.Equal(gotPurges, expectedPurges) {
		t.Fatalf("retire only the old Corpus routes: got %v, want %v", gotPurges, expectedPurges)
	}
}

type recordedCorpusProjection struct {
	calls            []string
	failNextEnsure   bool
	failNextActivity bool
}

func (p *recordedCorpusProjection) EnsureGeneration(_ context.Context, collection, org, corpusID, generation string) error {
	p.calls = append(p.calls, strings.Join([]string{"ensure", collection, org, corpusID, generation}, "/"))
	if p.failNextEnsure {
		p.failNextEnsure = false
		return errors.New("tenant provisioning response lost")
	}
	return nil
}

func (p *recordedCorpusProjection) SetGenerationActive(_ context.Context, collection, org, corpusID, generation string, active bool) error {
	p.calls = append(p.calls, strings.Join([]string{strconv.FormatBool(active), collection, org, corpusID, generation}, "/"))
	if p.failNextActivity {
		p.failNextActivity = false
		return errors.New("tenant activity unavailable")
	}
	return nil
}
