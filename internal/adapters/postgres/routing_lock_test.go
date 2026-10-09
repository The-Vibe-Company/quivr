package postgres_test

import (
	"context"
	"testing"
	"time"
)

// A blocked scope count must not pin the shared routing fence and prevent an
// admin cutover. Relation locks synchronize the actual SQL count, without a
// production hook or a wall-clock delay.
func TestBackfillCountReleasesRouting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f := newBackfillFixture(t, ctx, 1, 0)
	op, err := f.store.AcceptBackfill(ctx, f.org, f.corpusID, "unlocked-count", []byte("unlocked-count"), f.spec(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.BeginBackfill(ctx, f.org, op.ID, f.plan); err != nil {
		t.Fatal(err)
	}
	block, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer block.Rollback(context.Background())
	if _, err = block.Exec(ctx, "LOCK TABLE record_versions IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := f.store.CarryBackfillSpaces(ctx, f.org, op.ID); done <- err }()
	// Observe the scope query waiting on its relation lock. No other query in
	// this test can wait on record_versions after fixture setup.
	for {
		var waiting bool
		err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_locks WHERE relation='record_versions'::regclass AND NOT granted)`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("count ended before blocked phase: %v", err)
		default:
		}
	}
	probe, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Rollback(context.Background())
	var acquired bool
	if err = probe.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(642003)").Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("backfill scope count holds the routing lock; exclusive switch must proceed during counting")
	}
	if err = probe.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = block.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	stored, err := f.store.Operation(ctx, f.org, op.ID)
	if err != nil || stored.Counters["versions_in_scope"] != 1 {
		t.Fatalf("scope count: %+v (%v), want one Version", stored.Counters, err)
	}
	if _, err = f.store.CarryBackfillSpaces(ctx, f.org, op.ID); err != nil {
		t.Fatal(err)
	}
	stored, err = f.store.Operation(ctx, f.org, op.ID)
	if err != nil || stored.Counters["versions_in_scope"] != 1 {
		t.Fatalf("retry scope count: %+v (%v), want one Version", stored.Counters, err)
	}
}
