package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/backfill"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5/pgxpool"
)

// restoreRegistry puts back every registered space's role and forgets any
// promotion when the test ends: the registry is the deployment's.
func restoreRegistry(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	roles := map[string]string{}
	rows, err := pool.Query(ctx, `SELECT id,role FROM vector_spaces`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, role string
		if err = rows.Scan(&id, &role); err != nil {
			t.Fatal(err)
		}
		roles[id] = role
	}
	rows.Close()
	t.Cleanup(func() {
		ctx := context.Background()
		for id, role := range roles {
			if _, err := pool.Exec(ctx, `UPDATE vector_spaces SET role=$2 WHERE id=$1`, id, role); err != nil {
				t.Errorf("restore the registry: %v", err)
			}
		}
		if _, err := pool.Exec(ctx, `DELETE FROM vector_space_promotions`); err != nil {
			t.Errorf("restore the registry: %v", err)
		}
	})
}

// Promotion swaps the served space in the registry and in every generation
// carrying it, only once coverage is complete unless forced; promoting the
// former space back restores it; and registering the deployment's spaces
// keeps the operator's choice while the plugin enables both spaces.
func TestPromoteSpace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newBackfillFixture(t, ctx, 2, 0)
	other := newBackfillFixtureForPlugin(t, ctx, 1, 0, "example.other_fill")
	store := f.store
	restoreRegistry(t, ctx, f.pool)
	if _, err := f.pool.Exec(ctx, `UPDATE vector_spaces SET role=CASE WHEN id=$1 OR id=$2 THEN 'served' ELSE 'evaluation' END WHERE id=$1 OR id=$2 OR role='served'`, f.served, other.served); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE projection_generations SET spaces=spaces||jsonb_build_array(jsonb_build_object('id',$2::text,'metric','cosine')) WHERE id=$1`, f.generation.ID, f.target); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.segments[f.versions[0]].Segments {
		f.cover(t, ctx, f.versions[0], p.ID, f.target)
	}
	roleOf := func(space string) string {
		var role string
		if err := f.pool.QueryRow(ctx, `SELECT role FROM vector_spaces WHERE id=$1`, space).Scan(&role); err != nil {
			t.Fatal(err)
		}
		return role
	}
	served := func() string {
		g, err := store.Generation(ctx, f.org, f.corpusID)
		if err != nil {
			t.Fatal(err)
		}
		if g.Spaces[0].ID != g.SpaceID {
			t.Fatalf("the served space is not listed first: %+v", g)
		}
		return g.SpaceID
	}

	// Other Corpora of the database may lack the space too, so the test
	// reads how covering this Corpus's last segments changes the measure.
	_, err := store.PromoteSpace(ctx, f.target, false)
	var incomplete *backfill.IncompleteError
	if !errors.As(err, &incomplete) || incomplete.Promotion.SegmentsMissing < 2 {
		t.Fatalf("an incomplete space: %v", err)
	}
	before := incomplete.Promotion
	if roleOf(f.target) != "evaluation" || served() != f.served {
		t.Fatal("a refused promotion changed the served space")
	}
	for _, p := range f.segments[f.versions[1]].Segments {
		f.cover(t, ctx, f.versions[1], p.ID, f.target)
	}
	_, err = store.PromoteSpace(ctx, f.target, false)
	switch {
	case err == nil && before.CorporaIncomplete != 1:
		t.Fatalf("promoted with %d other incomplete Corpora", before.CorporaIncomplete-1)
	case err != nil && (!errors.As(err, &incomplete) || incomplete.Promotion.CorporaIncomplete != before.CorporaIncomplete-1 || incomplete.Promotion.SegmentsMissing != before.SegmentsMissing-2):
		t.Fatalf("the measure after covering this Corpus: %v, before %+v", err, before)
	}

	p, err := store.PromoteSpace(ctx, f.target, true)
	if err != nil || p.Served != f.target || p.Previous != f.served || p.GenerationsSwitched != 1 {
		t.Fatalf("forced promotion %+v %v", p, err)
	}
	otherGeneration, otherErr := store.Generation(ctx, other.org, other.corpusID)
	if roleOf(other.served) != "served" || otherErr != nil || otherGeneration.SpaceID != other.served {
		t.Fatalf("promoting one owner changed the other: %+v (%v), role %s", otherGeneration, otherErr, roleOf(other.served))
	}
	if roleOf(f.target) != "served" || roleOf(f.served) != "evaluation" || served() != f.target {
		t.Fatal("the promotion did not swap the served space")
	}
	if again, err := store.PromoteSpace(ctx, f.target, false); err != nil || again.GenerationsSwitched != 0 {
		t.Fatalf("promoting the served space %+v %v", again, err)
	}
	// Registering the plugin's spaces again (a restart, an activation) keeps
	// the promotion while the plugin enables both.
	deployed := func(spaces ...string) []content.RegisteredSpace {
		out := []content.RegisteredSpace{}
		for i, id := range spaces {
			role := content.SpaceEvaluation
			if i == 0 {
				role = content.SpaceServed
			}
			out = append(out, content.RegisteredSpace{VectorSpace: content.VectorSpace{ID: id, Manifest: []byte(`{}`), Dimensions: 2}, Name: id, Version: "1", OwnerPluginID: "example.fill", OwnerPluginVersion: "0.1.0", Model: "m", Metric: "cosine", Indexes: []string{"text"}, QueryModalities: []string{"text"}, Role: role})
		}
		return out
	}
	if err = store.RegisterSpaces(ctx, deployed(f.served, f.target)); err != nil {
		t.Fatal(err)
	}
	if roleOf(f.target) != "served" || roleOf(f.served) != "evaluation" {
		t.Fatal("registering the spaces again undid the promotion")
	}
	// Promoting the former space back restores it; the plugin's own roles
	// then agree, and the promotion is forgotten.
	if back, err := store.PromoteSpace(ctx, f.served, true); err != nil || back.Served != f.served || back.Previous != f.target || served() != f.served {
		t.Fatalf("promote back %+v %v", back, err)
	}
	if err = store.RegisterSpaces(ctx, deployed(f.served, f.target)); err != nil {
		t.Fatal(err)
	}
	var kept int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM vector_space_promotions`).Scan(&kept); err != nil || kept != 0 || roleOf(f.served) != "served" {
		t.Fatalf("a promotion the plugin's roles agree with is kept: %d %v", kept, err)
	}
	// A plugin that stops enabling the promoted space decides again.
	if _, err = store.PromoteSpace(ctx, f.target, true); err != nil {
		t.Fatal(err)
	}
	if err = store.RegisterSpaces(ctx, deployed(f.served)); err != nil {
		t.Fatal(err)
	}
	if roleOf(f.served) != "served" || roleOf(f.target) != "retired" {
		t.Fatalf("roles after the plugin dropped the promoted space: %s %s", roleOf(f.served), roleOf(f.target))
	}
	if _, err = store.PromoteSpace(ctx, f.target, true); !errors.Is(err, backfill.ErrNotEvaluation) {
		t.Fatalf("promoting a retired space: %v", err)
	}
	if _, err = store.PromoteSpace(ctx, "example.unknown@1", true); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("promoting an unknown space: %v", err)
	}
}
