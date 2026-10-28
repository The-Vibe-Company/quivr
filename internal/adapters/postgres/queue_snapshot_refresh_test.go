package postgres

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/autoscaling"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
	"net/http"
	"net/http/httptest"
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
	if _, err := pool.Exec(ctx, `INSERT INTO queue_backlog_snapshots(queue,waiting,in_progress,oldest_waiting_age_seconds,observed_at,published_at,ingestion_waiting,ingestion_observed_at,counters_observed_at)
	 VALUES('live',123,0,0,clock_timestamp()-interval '2 seconds',clock_timestamp()-interval '2 seconds',0,clock_timestamp()-interval '2 seconds',clock_timestamp()-interval '2 seconds'),
	       ('bulk',456,0,0,clock_timestamp()-interval '2 seconds',clock_timestamp()-interval '2 seconds',0,clock_timestamp()-interval '2 seconds',clock_timestamp()-interval '2 seconds')`); err != nil {
		t.Fatal(err)
	}
	snapshots := QueueSnapshots{Pool: pool}
	if _, err := snapshots.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
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
	// Previous writers can stamp publication without supplying the new proof.
	// The conservative default must force a new refresh within the cached interval.
	exec(`UPDATE queue_backlog_snapshots SET waiting=123,published_at=clock_timestamp(),counters_observed_at='-infinity'`)
	if _, err := snapshots.QueueBacklog(ctx); err == nil {
		t.Fatal("missing counter cutoff presented as current")
	}
	if err := snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	assertWaiting(0)
	// An older writer can refresh totals without updating the new split. Its
	// publication must not make an old ingestion count look current.
	exec(`UPDATE queue_backlog_snapshots SET ingestion_observed_at=clock_timestamp()-interval '2 minutes'`)
	statuses, err = snapshots.QueueBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.IngestionWaiting != nil {
			t.Fatalf("stale split presented as fresh: %+v", status)
		}
	}
	// Fresh legacy totals cannot starve a new writer's missing/stale split.
	exec(`UPDATE queue_backlog_snapshots SET published_at=clock_timestamp()`)
	if err := snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	statuses, err = snapshots.QueueBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.IngestionWaiting == nil || *status.IngestionWaiting != 0 {
			t.Fatalf("legacy total publication starved split refresh: %+v", status)
		}
	}
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
	// Bootstrap and operation scope discovery belong to independent maintenance.
	// Even unavailable bootstrap storage must not prevent publishing a fresh
	// observation for a queued, as-yet uncounted rebuild.
	seedQueueCorpus(t, ctx, pool, "example")
	exec(`INSERT INTO projection_generations(id,collection,profile_version) VALUES('target','target','example');
INSERT INTO operations(organization,id,kind,corpus_id,request_key,canonical_request,target_generation_id)
VALUES('example','first','projection_rebuild','corpus','first','','target'),
('example','second','retrieval_configuration','corpus','second','','target')`)
	exec(`ALTER TABLE queue_enrichment_bootstrap RENAME TO queue_refresh_unavailable`)
	defer exec(`ALTER TABLE queue_refresh_unavailable RENAME TO queue_enrichment_bootstrap`)
	if err = snapshots.Refresh(ctx); err != nil {
		t.Fatalf("refresh coupled to bootstrap: %v", err)
	}
	statuses, err = snapshots.QueueBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.Queue == "bulk" && status.Waiting == 0 {
			t.Fatalf("uncounted rebuild hidden from scaling: %+v", status)
		}
	}
	var untouched bool
	if err = pool.QueryRow(ctx, `SELECT bool_and(NOT counters ? 'versions_in_scope') FROM operations WHERE organization='example'`).Scan(&untouched); err != nil || !untouched {
		t.Fatalf("refresh enumerated rebuild scope: untouched=%v error=%v", untouched, err)
	}
	// Persisted discovery can lag processing. Completion counters must not hide
	// the still-undiscovered scope from the ordinary production scaling target.
	exec(`INSERT INTO queue_operation_scope_progress(organization,operation_id,cursor,seen) VALUES('example','first','partial',50000);
 UPDATE operations SET counters='{"versions_covered":50000}' WHERE id='first';
 UPDATE queue_backlog_snapshots SET published_at='-infinity'`)
	if err = snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	// A rebuild-only installation reaches the real admin response and the same
	// QueueSource/policy used by the scaler, without any acquisition backlog.
	handler, err := httpapi.New(nil, content.Service{}, retrieval.Service{}, uploads.Service{}, map[string]corpus.Scope{"operator": {Organization: "example", Actions: []string{"queues:read"}, Corpora: []string{"*"}}}, []byte("queue-fixture-cursor-key-0123456789"), httpapi.WithQueues(snapshots))
	if err != nil {
		t.Fatal(err)
	}
	source := autoscaling.QueueSource{URL: "http://example.invalid/v0/admin/queues", Key: "operator", Queue: "bulk", Client: &http.Client{Transport: queueHTTPTransport{handler}}}
	waiting, err := source.Waiting(ctx)
	if err != nil || waiting != 50001 {
		t.Fatalf("rebuild-only scaler signal=%d error=%v", waiting, err)
	}
	policy, err := autoscaling.NewPolicy(autoscaling.Config{Min: 1, Max: 8, DocumentsPerReplica: 20000})
	if err != nil {
		t.Fatal(err)
	}
	target, err := policy.Decide(waiting, 1, time.Now())
	if err != nil || target != 3 {
		t.Fatalf("rebuild-only recommendation=%d error=%v", target, err)
	}

}

type queueHTTPTransport struct{ handler http.Handler }

func (tr queueHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	tr.handler.ServeHTTP(recorder, req)
	return recorder.Result(), nil
}
