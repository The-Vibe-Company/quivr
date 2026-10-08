package postgres

import (
	"context"
	"testing"
	"time"
)

// The snapshot adapter owns refresh caching. A two-second-old observation
// reproduces the former one-second refresh loop without a wall-clock wait.
func TestQueueSnapshotRefreshReusesDefaultInterval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO queue_backlog_snapshots(queue,waiting,in_progress,oldest_waiting_age_seconds,observed_at,published_at)
	 VALUES('live',123,0,0,clock_timestamp()-interval '2 seconds',clock_timestamp()-interval '2 seconds'),
	       ('bulk',456,0,0,clock_timestamp()-interval '2 seconds',clock_timestamp()-interval '2 seconds')`); err != nil {
		t.Fatal(err)
	}
	snapshots := QueueSnapshots{Pool: pool}
	if err := snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	statuses, err := snapshots.QueueBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		want := int64(123)
		if status.Queue == "bulk" {
			want = 456
		}
		if status.Waiting != want {
			t.Fatalf("%s refreshed inside default interval: waiting=%d, want cached %d", status.Queue, status.Waiting, want)
		}
	}
	exec := func(query string) {
		t.Helper()
		if _, err := pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	assertWaiting := func(want int64) {
		t.Helper()
		statuses, err := snapshots.QueueBacklog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range statuses {
			if status.Waiting != want {
				t.Fatalf("%s waiting=%d, want %d", status.Queue, status.Waiting, want)
			}
		}
	}
	// A slow query's old observation cutoff must not bypass a recent
	// publication. Age still comes from the observation, not publication.
	exec(`UPDATE queue_backlog_snapshots SET waiting=7,oldest_waiting_age_seconds=10,
	 observed_at=clock_timestamp()-interval '40 seconds',published_at=clock_timestamp()`)
	if err := snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	assertWaiting(7)
	statuses, err = snapshots.QueueBacklog(ctx)
	if err != nil || statuses[0].OldestAgeSeconds < 49 {
		t.Fatalf("slow observation lost its age: statuses=%+v error=%v", statuses, err)
	}
	// Custom pacing reuses the same snapshot and becomes due independently of
	// its observation cutoff. The maximum interval has a two-interval stale window.
	snapshots.RefreshInterval = time.Minute
	exec(`UPDATE queue_backlog_snapshots SET observed_at=clock_timestamp()-interval '70 seconds',published_at=clock_timestamp()-interval '30 seconds'`)
	if err := snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	assertWaiting(7)
	snapshots.RefreshInterval = time.Second
	if err := snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	assertWaiting(0)
	// A competing refresher holds the actual installation election lock.
	exec(`UPDATE queue_backlog_snapshots SET waiting=9,published_at='-infinity'`)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('quivr.queue-backlog-snapshot.v1',0))`); err != nil {
		t.Fatal(err)
	}
	if err = snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	assertWaiting(9)
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// Missing refresh storage produces an immediate SQL failure. Existing
	// observations and publication timestamps survive its transaction rollback.
	// Scope assignments commit before aggregation, releasing worker row locks.
	// A failed snapshot must therefore retain newly captured scopes as well as
	// the previous publication, even for operations sharing the same corpus.
	seedQueueCorpus(t, ctx, pool, "example")
	exec(`INSERT INTO projection_generations(id,collection,profile_version) VALUES('target','target','example');
INSERT INTO operations(organization,id,kind,corpus_id,request_key,canonical_request,target_generation_id)
VALUES('example','first','projection_rebuild','corpus','first','','target'),
('example','second','retrieval_configuration','corpus','second','','target')`)
	exec(`ALTER TABLE queue_enrichment_bootstrap RENAME TO queue_refresh_unavailable`)
	defer exec(`ALTER TABLE queue_refresh_unavailable RENAME TO queue_enrichment_bootstrap`)
	if err = snapshots.Refresh(ctx); err == nil {
		t.Fatal("refresh succeeded with missing observation storage")
	}
	assertWaiting(9)
	var captured bool
	if err = pool.QueryRow(ctx, `SELECT count(*)=2 AND bool_and(counters->>'versions_in_scope'='0') FROM operations WHERE organization='example'`).Scan(&captured); err != nil || !captured {
		t.Fatalf("failed aggregation rolled back scope assignments: captured=%v error=%v", captured, err)
	}
	var unpublished bool
	if err = pool.QueryRow(ctx, `SELECT bool_and(published_at='-infinity') FROM queue_backlog_snapshots`).Scan(&unpublished); err != nil || !unpublished {
		t.Fatalf("failed refresh changed publication time: unpublished=%v error=%v", unpublished, err)
	}
}
