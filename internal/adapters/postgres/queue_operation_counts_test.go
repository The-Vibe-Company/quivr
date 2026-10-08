package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/workqueue"
)

// Operation waiting is a progress signal from persisted scope/progress counters.
// Unlike the stage-count owner, this contract permits approximate overlap and
// remains available without enumerating the original documents again.
func TestQueueOperationBacklogUsesScopeAndProgressCounters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seedQueueCorpus(t, ctx, pool, "example")
	exec := func(query string) {
		t.Helper()
		if _, err := pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projection_generations(id,collection,profile_version) VALUES('target','target','example');
INSERT INTO plugin_registrations(id,plugin_id,version,endpoint,manifest_digest,contributions,roles,state)
VALUES('registration','example.counter','1','http://example.invalid','example','{}','{}','active');
INSERT INTO operations(organization,id,kind,corpus_id,request_key,canonical_request,target_generation_id,counters,created_at)
VALUES('example','rebuild','projection_rebuild','corpus','rebuild','','target','{"versions_in_scope":8,"indexed":3,"versions_quarantined":1}',clock_timestamp()-interval '30 seconds'),
('example','backfill','backfill','corpus','backfill','','target','{"versions_in_scope":8,"versions_done":3,"versions_skipped":1}',clock_timestamp()-interval '30 seconds'),
('example','estimated','backfill','corpus','estimated','','target','{"versions_done":3,"versions_skipped":1}',clock_timestamp()-interval '30 seconds');
INSERT INTO backfills(organization,operation_id,registration_id,spaces,estimate)
VALUES('example','backfill','registration','{}','{"versions":100}'),('example','estimated','registration','{}','{"versions":9}')`)
	snapshots := QueueSnapshots{Pool: pool}
	read := func(waiting, active int64) {
		t.Helper()
		exec(`UPDATE queue_backlog_snapshots SET published_at='-infinity'`)
		if err := snapshots.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		statuses, err := snapshots.QueueBacklog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range statuses {
			if status.Queue == workqueue.Bulk && (status.Waiting != waiting || status.InProgress != active) {
				t.Fatalf("bulk=%+v, want waiting=%d active=%d", status, waiting, active)
			}
			if status.Queue == workqueue.Bulk && waiting > 0 && status.OldestAgeSeconds < 25 {
				t.Fatalf("operation admission age lost: %+v", status)
			}
		}
	}
	read(13, 0)
	// Two stage observations of the same executing document consume one unit
	// of the operation's remaining scope and one in-progress document.
	exec(`INSERT INTO queue_document_attempts(organization,kind,work_id,document_id,token,lease_until)
VALUES('example','operation','rebuild','document',nextval('queue_document_attempt_tokens'),clock_timestamp()+interval '1 minute'),
('example','rebuild','rebuild','document',nextval('queue_document_attempt_tokens'),clock_timestamp()+interval '1 minute')`)
	read(12, 1)
	exec(`UPDATE operations SET state='paused' WHERE id='rebuild'`)
	read(9, 0)
	exec(`UPDATE operations SET counters=counters || '{"versions_done":30}' WHERE id IN ('backfill','estimated')`)
	read(0, 0)
}
