package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Queue refresh owns this work bound: superseded queued enrichment must not
// require scanning history, and a pending rebuild must not enumerate its scope
// on every observation. Unlike the count owner, this test observes database work,
// without a timing assertion or a forced planner setting.
func TestQueueBacklogWorkDoesNotGrowWithSupersededEnrichment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval)
VALUES('example','corpus','corpus','','Example','{}');
INSERT INTO content_blobs(organization,blob_id,object_key,sha256,byte_length)
VALUES('example','blob','example','example',1)`)
	exec(`INSERT INTO projection_generations(id,collection,profile_version) VALUES('queue-target','queue-target','example');
INSERT INTO operations(organization,id,kind,corpus_id,request_key,canonical_request,target_generation_id,counters)
VALUES('example','rebuild','projection_rebuild','corpus','rebuild','','queue-target','{"versions_in_scope":1000}')`)
	var firstBlocks, firstObservationBlocks int
	for _, size := range []int{1000, 10000} {
		low := 1
		if size == 10000 {
			low = 1001
		}
		exec(`INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_version_id,desired_version_id)
SELECT 'example','record-'||i,'corpus','example',i::text,'current-'||i,'current-'||i FROM generate_series($1::int,$2::int) i`, low, size)
		exec(`INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,text_blob_id,manifest_blob_id,provenance,baseline_ready,processing,enrichment_state)
SELECT 'example',kind||'-'||i,'record-'||i,kind,kind,CASE kind WHEN 'old' THEN 1 ELSE 2 END,'','blob','blob','{}',true,'idle',CASE WHEN kind='old' OR i=1 THEN 'queued' ELSE 'idle' END
FROM generate_series($1::int,$2::int) i CROSS JOIN (VALUES('old'),('current')) k(kind)`, low, size)
		exec(`INSERT INTO accepted_revisions(organization,record_id,slot,digest,version_id,acceptance_order,source_position,command,accepted_at)
SELECT organization,record_id,slot,digest,id,acceptance_order,source_position,'{}',now() FROM record_versions
WHERE record_id IN (SELECT 'record-'||i FROM generate_series($1::int,$2::int) i)`, low, size)
		exec("ANALYZE")
		exec(`UPDATE operations SET counters=jsonb_build_object('versions_in_scope',$1::bigint) WHERE organization='example' AND id='rebuild'`, size)
		// Exercise refresh and let the one-time bounded bootstrap finish without
		// waiting for the production snapshot interval.
		for batch := 0; batch <= size/1000; batch++ {
			exec("UPDATE queue_backlog_snapshots SET observed_at='-infinity',published_at='-infinity'")
			if err := (QueueSnapshots{Pool: pool}).Refresh(ctx); err != nil {
				t.Fatal(err)
			}
		}
		statuses, err := (QueueSnapshots{Pool: pool}).QueueBacklog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range statuses {
			expected := int64(size)
			if status.Queue == "live" {
				expected = 1
			}
			if status.Waiting != expected || status.InProgress != 0 {
				t.Fatalf("history contributed queue work: %+v, want waiting %d and no active attempts", status, expected)
			}
		}
		exec("ANALYZE")
		// Observation must also point-read a fixed batch rather than hash unrelated
		// history. Explain the exact statements queued by the production owner.
		var organizations, records []string
		for i := 1; i <= 100; i++ {
			organizations = append(organizations, "example")
			records = append(records, fmt.Sprintf("record-%d", i))
		}
		observation := &pgx.Batch{}
		queueRecordObservations(observation, organizations, records)
		observationTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		observationBlocks := 0
		for _, query := range observation.QueuedQueries {
			var raw []byte
			if err = observationTx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query.SQL, query.Arguments...).Scan(&raw); err != nil {
				_ = observationTx.Rollback(ctx)
				t.Fatal(err)
			}
			observationBlocks += queueWorkBlocks(t, raw)
		}
		if err = observationTx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		t.Logf("history=%d records: fixed 100-record observation buffers=%d", size, observationBlocks)
		if size == 1000 {
			firstObservationBlocks = observationBlocks
		} else if observationBlocks > 2*firstObservationBlocks+100 {
			t.Fatalf("fixed observation grew with unrelated history: buffers %d -> %d (bound %d)", firstObservationBlocks, observationBlocks, 2*firstObservationBlocks+100)
		}

		var raw []byte
		if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+queueBacklogSQL()).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		blocks := queueWorkBlocks(t, raw)
		t.Logf("history=%d records: shared buffers=%d, live waiting=1, bulk waiting=%d", size, blocks, size)
		if size == 1000 {
			firstBlocks = blocks
		} else if blocks > 2*firstBlocks+100 {
			t.Fatalf("backlog query work grew with history/rebuild scope: buffers %d -> %d (bound %d)", firstBlocks, blocks, 2*firstBlocks+100)
		}
	}
}

func queueWorkBlocks(t *testing.T, raw []byte) int {
	t.Helper()
	var plans []struct{ Plan map[string]any }
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	return int(plans[0].Plan["Shared Hit Blocks"].(float64) + plans[0].Plan["Shared Read Blocks"].(float64))
}

// Selection correctness belongs to TestPurgeSelectsOnlyDeadVersionsAndSurvivesRevert.
// This owner guards a distinct cost contract: one live candidate costs bounded
// storage work even as unrelated live Versions and segmentations accumulate.
func TestVersionPurgeWorkDoesNotGrowWithLiveHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seedQueueCorpus(t, ctx, pool, "example")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var firstBlocks int
	for _, size := range []int{1000, 10000} {
		low := 1
		if size == 10000 {
			low = 1001
		}
		exec(`INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_version_id,desired_version_id)
SELECT 'example','record-'||i,'corpus','example',i::text,'current-'||i,'current-'||i FROM generate_series($1::int,$2::int) i`, low, size)
		exec(`INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,text_blob_id,manifest_blob_id,provenance,baseline_ready,processing,enrichment_state)
SELECT 'example','current-'||i,'record-'||i,'text',i::text,1,'','blob','blob','{}',true,'idle','idle' FROM generate_series($1::int,$2::int) i`, low, size)
		exec(`INSERT INTO segmentations(organization,id,version_id,recipe,digest)
SELECT 'example','segmentation-'||i,'current-'||i,'text',i::text FROM generate_series($1::int,$2::int) i`, low, size)
		exec(`DELETE FROM projection_purge_candidates`)
		exec(`INSERT INTO projection_purge_candidates(organization,version_id) VALUES('example','current-1')`)
		exec("ANALYZE")
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var raw []byte
		err = tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+noticeVersionPurgesSQL, 1).Scan(&raw)
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		var noticed int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM projection_purges WHERE kind='version'`).Scan(&noticed); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if noticed != 0 {
			t.Fatalf("history=%d: noticed %d Versions, want no dead Versions", size, noticed)
		}
		blocks := queueWorkBlocks(t, raw)
		t.Logf("history=%d live Versions: shared buffers=%d, noticed=0", size, blocks)
		if size == 1000 {
			firstBlocks = blocks
		} else if blocks > 2*firstBlocks+100 {
			t.Fatalf("fixed purge work grew with live history: buffers %d -> %d (bound %d)", firstBlocks, blocks, 2*firstBlocks+100)
		}
	}
}

// queueWorkPool creates an isolated, empty database for queue tests, without
// touching the adapter database.
func queueWorkPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	configPath := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if configPath == "" {
		t.Skip("real PostgreSQL suite runs inside make adapter-postgres")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		DatabaseURL string `json:"database_url"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, config.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	name := "queue_work_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}

	connection, err := pgxpool.ParseConfig(config.DatabaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	connection.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, connection)
	if err != nil {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
		admin.Close()
	})
	return pool
}

func seedQueueCorpus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, organization string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval)
VALUES($1,'corpus','corpus',decode('','hex'),'Queue fixture','{}')`, organization); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO content_blobs(organization,blob_id,object_key,sha256,byte_length)
VALUES($1,'blob','queue-fixture','queue-fixture',1)`, organization); err != nil {
		t.Fatal(err)
	}
}

// seedQueueRecord inserts a current or historical Version using the same
// persisted facts that queueBacklogSQL reads, without observing it.
func seedQueueRecord(t *testing.T, ctx context.Context, pool *pgxpool.Pool, organization, recordID, versionID, slot, state string, acceptanceOrder int64, current bool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO records(organization,id,corpus_id,namespace,record_key)
VALUES($1,$2,'corpus','queue',$2)`, organization, recordID); err != nil {
		t.Fatal(err)
	}
	seedQueueVersion(t, ctx, pool, organization, recordID, versionID, slot, state, acceptanceOrder, current)
}

func seedQueueVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, organization, recordID, versionID, slot, state string, acceptanceOrder int64, current bool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO record_versions(
 organization,id,record_id,slot,digest,acceptance_order,source_position,
 text_blob_id,manifest_blob_id,provenance,baseline_ready,quarantined,processing,enrichment_state)
VALUES($1,$2,$3,$4,$2,$5,$6,'blob','blob','{}',true,false,'idle',$7)`, organization, versionID, recordID, slot, acceptanceOrder, fmt.Sprint(acceptanceOrder), state); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO accepted_revisions(
 organization,record_id,slot,digest,version_id,acceptance_order,source_position,command,accepted_at)
VALUES($1,$3,$4,$2,$2,$5,$6,'{}',clock_timestamp())`, organization, versionID, recordID, slot, acceptanceOrder, fmt.Sprint(acceptanceOrder)); err != nil {
		t.Fatal(err)
	}
	if current {
		setQueueCurrent(t, ctx, pool, organization, recordID, versionID)
	}
}

func setQueueCurrent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, organization, recordID, versionID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE records SET current_version_id=$3 WHERE organization=$1 AND id=$2`, organization, recordID, versionID); err != nil {
		t.Fatal(err)
	}
}

func waitForPostgresBlock(ctx context.Context, observer *pgxpool.Conn, waitingPID, blockerPID int32) error {
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		var blocked bool
		if err := observer.QueryRow(deadline, `SELECT $2 = ANY(pg_blocking_pids($1))`, waitingPID, blockerPID).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return nil
		}
		if err := deadline.Err(); err != nil {
			return fmt.Errorf("waiter pid %d was not blocked by pid %d: %w", waitingPID, blockerPID, err)
		}
		runtime.Gosched()
	}
}

// Owns the one-time bootstrap: it recomputes state committed by a concurrent
// observation writer, then never scans records again.
func TestQueueEnrichmentBootstrapRecomputesAfterObservationWriterCommits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seedQueueCorpus(t, ctx, pool, "race")
	seedQueueRecord(t, ctx, pool, "race", "record", "version", "text", "queued", 1, true)
	if _, err := pool.Exec(ctx, `INSERT INTO organization_journals(organization,last_sequence) VALUES('race',0) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	setupTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := observeQueueRecords(ctx, setupTx, []string{"race"}, []string{"record"}); err != nil {
		_ = setupTx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := setupTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	writer, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Release()
	bootstrap, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Release()
	observer, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Release()

	writerTx, err := writer.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writerTx.Rollback(context.Background())
	if err := lockJournal(ctx, writerTx, "race"); err != nil {
		t.Fatal(err)
	}
	if _, err := writerTx.Exec(ctx, `UPDATE record_versions SET enrichment_state='idle' WHERE organization='race' AND id='version'`); err != nil {
		t.Fatal(err)
	}
	if err := observeQueueRecords(ctx, writerTx, []string{"race"}, []string{"record"}); err != nil {
		t.Fatal(err)
	}
	writerPID := int32(writer.Conn().PgConn().PID())
	bootstrapPID := int32(bootstrap.Conn().PgConn().PID())
	bootstrapTx, err := bootstrap.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrapTx.Rollback(context.Background())
	bootstrapDone := make(chan error, 1)
	bootstrapStarted := make(chan struct{})
	go func() {
		close(bootstrapStarted)
		err := advanceQueueObservations(ctx, bootstrapTx, 1000)
		if err == nil {
			err = bootstrapTx.Commit(ctx)
		}
		bootstrapDone <- err
	}()
	<-bootstrapStarted
	if err := waitForPostgresBlock(ctx, observer, bootstrapPID, writerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := appendEventAt(ctx, writerTx, eventInput{
		Organization: "race",
		CorpusID:     "corpus",
		Kind:         "record.enrichment_available",
		Resource:     "record",
		ResourceID:   "record",
		MutationID:   "fixture-idle",
	}); err != nil {
		t.Fatalf("writer event append while bootstrap waits: %v", err)
	}
	if err := writerTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-bootstrapDone; err != nil {
		t.Fatal(err)
	}

	var pending bool
	if err := pool.QueryRow(ctx, `SELECT pending FROM queue_enrichment_records WHERE organization='race' AND record_id='record'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("bootstrap restored queued membership after the committed state writer set the Version idle")
	}
	var initialized bool
	if err := pool.QueryRow(ctx, `SELECT initialized FROM queue_enrichment_bootstrap WHERE singleton`).Scan(&initialized); err != nil {
		t.Fatal(err)
	}
	if !initialized {
		t.Fatal("bootstrap did not persist completion after recomputing the committed state")
	}

	// Writers observe their own records, so a completed bootstrap never scans
	// records again: a record written without observation stays unobserved.
	seedQueueRecord(t, ctx, pool, "race", "unobserved", "unobserved-version", "text", "queued", 1, true)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err := advanceQueueObservations(ctx, tx, 1000); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var observed int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM queue_enrichment_records WHERE organization='race' AND record_id='unobserved'`).Scan(&observed); err != nil {
		t.Fatal(err)
	}
	if observed != 0 {
		t.Fatal("completed bootstrap scanned records again")
	}
}

func TestProjectionPurgeCandidateUpsertSurvivesConcurrentNotice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seedQueueCorpus(t, ctx, pool, "purge-race")
	seedQueueRecord(t, ctx, pool, "purge-race", "record", "version-one", "text", "idle", 1, true)
	seedQueueVersion(t, ctx, pool, "purge-race", "record", "version-two", "text-correction", "idle", 2, false)
	if _, err := pool.Exec(ctx, `
UPDATE records SET current_version_id='version-two',desired_version_id='version-two'
WHERE organization='purge-race' AND id='record'`); err != nil {
		t.Fatal(err)
	}
	setupTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := enqueueProjectionPurge(ctx, setupTx, "purge-race", "version-one"); err != nil {
		_ = setupTx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := setupTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var candidates int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM projection_purge_candidates WHERE organization='purge-race' AND version_id='version-one'`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 1 {
		t.Fatalf("setup candidates = %d, want 1", candidates)
	}

	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if holder != nil {
			holder.Release()
		}
	}()
	holderTx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holderTx.Rollback(context.Background())
	if tag, err := holderTx.Exec(ctx, noticeVersionPurgesSQL, 1); err != nil {
		t.Fatal(err)
	} else if tag.RowsAffected() != 0 {
		t.Fatalf("unsegmented candidate inserted %d purges, want zero", tag.RowsAffected())
	}
	if err := holderTx.QueryRow(ctx, `SELECT count(*) FROM projection_purge_candidates WHERE organization='purge-race' AND version_id='version-one'`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 0 {
		t.Fatalf("open notice retained %d candidate rows, want consumed candidate", candidates)
	}

	late, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if late != nil {
			late.Release()
		}
	}()
	observer, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if observer != nil {
			observer.Release()
		}
	}()
	holderPID := int32(holder.Conn().PgConn().PID())
	latePID := int32(late.Conn().PgConn().PID())
	lateDone := make(chan error, 1)
	lateTx, err := late.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lateTx.Rollback(context.Background())
	go func() {
		_, err := lateTx.Exec(ctx, `
INSERT INTO segmentations(organization,id,version_id,recipe,digest,provenance)
VALUES('purge-race','seg-late','version-one','fixture','digest-late','{}')`)
		if err == nil {
			err = enqueueProjectionPurge(ctx, lateTx, "purge-race", "version-one")
		}
		if err == nil {
			err = lateTx.Commit(ctx)
		}
		lateDone <- err
	}()
	if err := waitForPostgresBlock(ctx, observer, latePID, holderPID); err != nil {
		_ = holderTx.Rollback(context.Background())
		<-lateDone
		t.Fatal(err)
	}
	if err := holderTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-lateDone; err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM projection_purge_candidates WHERE organization='purge-race' AND version_id='version-one'`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 1 {
		t.Fatalf("late segmentation candidates = %d, want retained candidate after the open notice committed", candidates)
	}

	holder.Release()
	holder = nil
	late.Release()
	late = nil
	observer.Release()
	observer = nil
	if noticed, err := (PurgeStore{Pool: pool}).NoticePurges(ctx, 1); err != nil || noticed != 1 {
		t.Fatalf("retained late candidate notice = %d, %v; want one notice", noticed, err)
	}
	var purges int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM projection_purges WHERE organization='purge-race' AND kind='version' AND version_id='version-one'`).Scan(&purges); err != nil {
		t.Fatal(err)
	}
	if purges != 1 {
		t.Fatalf("late segmented Version purges = %d, want 1", purges)
	}
}

func refreshQueueWork(t *testing.T, ctx context.Context, snapshots QueueSnapshots) {
	t.Helper()
	if _, err := snapshots.Pool.Exec(ctx, `UPDATE queue_backlog_snapshots SET observed_at='-infinity',published_at='-infinity'`); err != nil {
		t.Fatal(err)
	}
	if err := snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
}
