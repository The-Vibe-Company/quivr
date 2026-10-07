package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"github.com/The-Vibe-Company/quivr/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// queueWorkPool creates an isolated, empty database for queue tests. Keeping
// database creation here lets queue tests exercise both an old migration head
// and the current schema without touching the adapter database.
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

// queueWorkPrePurgeMigrations models a process that was upgraded before the
// enrichment observation migration was installed. The migration itself scans
// no history, so existing rows remain the meaningful fixture.
func queueWorkPrePurgeMigrations(t *testing.T) fstest.MapFS {
	t.Helper()
	names, err := migrations.Names()
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	for _, name := range names {
		if strings.HasSuffix(name, "_queue_purge_work.sql") {
			continue
		}
		data, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		fsys[name] = &fstest.MapFile{Data: data}
	}
	return fsys
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
// persisted facts that queueBacklogSQL reads. These setup writes model an
// older binary, so the recurring bounded repair must discover them.
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

// appendLegacyQueueEvent models an older writer that appends its record event
// without the current application observation hook. The bounded journal repair
// must discover the event and refresh the canonical record membership.
func appendLegacyQueueEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, event eventInput) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `INSERT INTO organization_journals(organization) VALUES($1) ON CONFLICT DO NOTHING`, event.Organization); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, appendEventSQL, eventArguments(ctx, event)...); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func seedQueueHistory(t *testing.T, ctx context.Context, pool *pgxpool.Pool, organization string, count int) {
	t.Helper()
	seedQueueCorpus(t, ctx, pool, organization)
	if _, err := pool.Exec(ctx, `
INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_version_id,desired_version_id)
SELECT $1,'record-'||lpad(i::text,4,'0'),'corpus','queue',i::text,
       'version-'||lpad(i::text,4,'0'),'version-'||lpad(i::text,4,'0')
FROM generate_series(1,$2::int) AS s(i)`, organization, count); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO record_versions(
 organization,id,record_id,slot,digest,acceptance_order,source_position,
 text_blob_id,manifest_blob_id,provenance,baseline_ready,quarantined,processing,enrichment_state)
SELECT $1,'version-'||lpad(i::text,4,'0'),'record-'||lpad(i::text,4,'0'),'text',
       'version-'||lpad(i::text,4,'0'),i,i::text,'blob','blob','{}',true,false,'idle','queued'
FROM generate_series(1,$2::int) AS s(i)`, organization, count); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO accepted_revisions(
 organization,record_id,slot,digest,version_id,acceptance_order,source_position,command,accepted_at)
SELECT $1,'record-'||lpad(i::text,4,'0'),'text','version-'||lpad(i::text,4,'0'),
       'version-'||lpad(i::text,4,'0'),i,i::text,'{}',clock_timestamp()
FROM generate_series(1,$2::int) AS s(i)`, organization, count); err != nil {
		t.Fatal(err)
	}
}

func refreshQueueWork(t *testing.T, ctx context.Context, snapshots QueueSnapshots) map[string]workqueue.Status {
	t.Helper()
	if _, err := snapshots.Pool.Exec(ctx, `UPDATE queue_backlog_snapshots SET observed_at='-infinity'`); err != nil {
		t.Fatal(err)
	}
	if err := snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := snapshots.QueueBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("queue status rows = %d, want live and bulk", len(rows))
	}
	status := make(map[string]workqueue.Status, len(rows))
	for _, row := range rows {
		status[row.Queue] = row
	}
	return status
}

func TestQueueBacklogUpgradeBootstrapsPreexistingEnrichmentInBoundedBatches(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := MigrateFS(ctx, pool, queueWorkPrePurgeMigrations(t)); err != nil {
		t.Fatal(err)
	}
	seedQueueHistory(t, ctx, pool, "upgrade", 1001)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	snapshots := QueueSnapshots{Pool: pool}
	first := refreshQueueWork(t, ctx, snapshots)
	if first[workqueue.Live].Waiting != 1001 || first[workqueue.Live].InProgress != 0 || first[workqueue.Bulk].Waiting != 0 {
		t.Fatalf("first bounded bootstrap backlog = %+v, want live waiting 1001 and no bulk work", first)
	}
	var organization, record string
	var initialized bool
	if err := pool.QueryRow(ctx, `SELECT organization,record_id,initialized FROM queue_enrichment_bootstrap WHERE singleton`).Scan(&organization, &record, &initialized); err != nil {
		t.Fatal(err)
	}
	if organization != "upgrade" || record != "record-1000" || initialized {
		t.Fatalf("first bounded bootstrap = %s/%s initialized=%v, want cursor record-1000 and unfinished", organization, record, initialized)
	}
	var observed int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM queue_enrichment_records`).Scan(&observed); err != nil {
		t.Fatal(err)
	}
	if observed != 1000 {
		t.Fatalf("first bounded bootstrap observations = %d, want 1000", observed)
	}

	second := refreshQueueWork(t, ctx, snapshots)
	if second[workqueue.Live].Waiting != 1001 || second[workqueue.Live].InProgress != 0 || second[workqueue.Bulk].Waiting != 0 {
		t.Fatalf("completed bootstrap backlog = %+v, want live waiting 1001 and no bulk work", second)
	}
	if err := pool.QueryRow(ctx, `SELECT organization,record_id,initialized FROM queue_enrichment_bootstrap WHERE singleton`).Scan(&organization, &record, &initialized); err != nil {
		t.Fatal(err)
	}
	if organization != "" || record != "" || !initialized {
		t.Fatalf("completed bounded bootstrap = %s/%s initialized=%v, want empty cursor and initialized", organization, record, initialized)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM queue_enrichment_records`).Scan(&observed); err != nil {
		t.Fatal(err)
	}
	if observed != 1001 {
		t.Fatalf("completed bootstrap observations = %d, want 1001", observed)
	}
}

func TestQueueBacklogRepairRecoversOldBinaryChangesBehindCursor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := MigrateFS(ctx, pool, queueWorkPrePurgeMigrations(t)); err != nil {
		t.Fatal(err)
	}
	seedQueueHistory(t, ctx, pool, "repair", 1001)
	if _, err := pool.Exec(ctx, `INSERT INTO organization_journals(organization) VALUES('repair') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	snapshots := QueueSnapshots{Pool: pool}
	status := refreshQueueWork(t, ctx, snapshots)
	if status[workqueue.Live].Waiting != 1001 || status[workqueue.Bulk].Waiting != 0 {
		t.Fatalf("initial legacy backlog = %+v, want live waiting 1001", status)
	}
	var initialized bool
	if err := pool.QueryRow(ctx, `SELECT initialized FROM queue_enrichment_bootstrap WHERE singleton`).Scan(&initialized); err != nil {
		t.Fatal(err)
	}
	if initialized {
		t.Fatal("first bounded repair unexpectedly completed")
	}
	var journalPosition int64
	if err := pool.QueryRow(ctx, `SELECT position FROM queue_observation_journals WHERE organization='repair'`).Scan(&journalPosition); err != nil {
		t.Fatal(err)
	}
	if journalPosition != 0 {
		t.Fatalf("initial queue journal checkpoint = %d, want seeded head 0", journalPosition)
	}

	// A previous binary writes a replacement behind the current cursor and
	// appends its record event without the new observation hook. The bounded
	// journal repair must preserve the canonical count while it catches up.
	seedQueueVersion(t, ctx, pool, "repair", "record-0002", "version-0002-new", "text-correction", "queued", 1002, false)
	if _, err := pool.Exec(ctx, `
UPDATE records SET current_version_id='version-0002-new',desired_version_id='version-0002-new'
WHERE organization='repair' AND id='record-0002'`); err != nil {
		t.Fatal(err)
	}
	appendLegacyQueueEvent(t, ctx, pool, eventInput{
		Organization: "repair",
		CorpusID:     "corpus",
		Kind:         "record.retrieval_ready",
		Resource:     "record",
		ResourceID:   "record-0002",
		MutationID:   "legacy-pointer-record-0002",
		VersionID:    "version-0002-new",
	})
	status = refreshQueueWork(t, ctx, snapshots)
	if status[workqueue.Live].Waiting != 1001 {
		t.Fatalf("backlog after behind-cursor pointer event repair = %+v, want live waiting 1001", status)
	}
	if err := pool.QueryRow(ctx, `SELECT initialized FROM queue_enrichment_bootstrap WHERE singleton`).Scan(&initialized); err != nil {
		t.Fatal(err)
	}
	if !initialized {
		t.Fatal("bounded repair did not complete its first cycle")
	}
	if err := pool.QueryRow(ctx, `SELECT position FROM queue_observation_journals WHERE organization='repair'`).Scan(&journalPosition); err != nil {
		t.Fatal(err)
	}
	if journalPosition != 1 {
		t.Fatalf("repaired queue journal checkpoint = %d, want old event position 1", journalPosition)
	}

	var version, desired string
	if err := pool.QueryRow(ctx, `
SELECT version_id,desired_version_id FROM queue_enrichment_records
WHERE organization='repair' AND record_id='record-0002'`).Scan(&version, &desired); err != nil {
		t.Fatal(err)
	}
	if version != "version-0002-new" || desired != "version-0002-new" {
		t.Fatalf("repaired pointer observation = %s/%s, want version-0002-new", version, desired)
	}

	// State and tombstone writes made by the old binary have no new observation
	// hook. The recurring keyset pass still recomputes their canonical
	// membership behind the reset cursor.
	if _, err := pool.Exec(ctx, `UPDATE record_versions SET enrichment_state='idle' WHERE organization='repair' AND id='version-0001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tombstones(organization,record_id) VALUES('repair','record-0003')`); err != nil {
		t.Fatal(err)
	}
	status = refreshQueueWork(t, ctx, snapshots)
	if status[workqueue.Live].Waiting != 999 {
		t.Fatalf("backlog before recurring state repair = %+v, want live waiting 999", status)
	}
	status = refreshQueueWork(t, ctx, snapshots)
	if status[workqueue.Live].Waiting != 999 {
		t.Fatalf("backlog after recurring state repair = %+v, want live waiting 999", status)
	}
	var pending, gone bool
	if err := pool.QueryRow(ctx, `
SELECT pending,gone FROM queue_enrichment_records
WHERE organization='repair' AND record_id='record-0001'`).Scan(&pending, &gone); err != nil {
		t.Fatal(err)
	}
	if pending || gone {
		t.Fatalf("idle state observation = pending %v gone %v, want false/false", pending, gone)
	}
	if err := pool.QueryRow(ctx, `
SELECT pending,gone FROM queue_enrichment_records
WHERE organization='repair' AND record_id='record-0003'`).Scan(&pending, &gone); err != nil {
		t.Fatal(err)
	}
	if pending || !gone {
		t.Fatalf("tombstone observation = pending %v gone %v, want false/true", pending, gone)
	}
}

func seedQueueSegmentations(t *testing.T, ctx context.Context, pool *pgxpool.Pool, organization string, count int) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO segmentations(organization,id,version_id,recipe,digest,provenance)
SELECT $1,'seg-'||lpad(i::text,4,'0'),'version-'||lpad(i::text,4,'0'),'fixture',
       'digest-'||lpad(i::text,4,'0'),'{}'
FROM generate_series(1,$2::int) AS s(i)`, organization, count); err != nil {
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
	var journalPosition int64
	if err := pool.QueryRow(ctx, `SELECT position FROM queue_observation_journals WHERE organization='race'`).Scan(&journalPosition); err != nil {
		t.Fatal(err)
	}
	if journalPosition != 1 {
		t.Fatalf("bootstrap journal checkpoint = %d, want appended event position 1", journalPosition)
	}
}

func TestProjectionPurgeBootstrapAdvancesAndNoticesLaterDeadVersions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := MigrateFS(ctx, pool, queueWorkPrePurgeMigrations(t)); err != nil {
		t.Fatal(err)
	}
	seedQueueHistory(t, ctx, pool, "purge-upgrade", 4)
	seedQueueSegmentations(t, ctx, pool, "purge-upgrade", 4)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := PurgeStore{Pool: pool}

	if noticed, err := store.NoticePurges(ctx, 2); err != nil || noticed != 0 {
		t.Fatalf("first live bootstrap notice = %d, %v; want zero notices", noticed, err)
	}
	var organization, segmentationID string
	var initialized bool
	if err := pool.QueryRow(ctx, `SELECT organization,segmentation_id,initialized FROM projection_purge_bootstrap WHERE singleton`).Scan(&organization, &segmentationID, &initialized); err != nil {
		t.Fatal(err)
	}
	if organization != "purge-upgrade" || segmentationID != "seg-0002" || initialized {
		t.Fatalf("first purge bootstrap = %s/%s initialized=%v, want cursor seg-0002 and unfinished", organization, segmentationID, initialized)
	}
	var candidates int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM projection_purge_candidates`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 0 {
		t.Fatalf("live bootstrap left %d candidates, want zero", candidates)
	}

	if noticed, err := store.NoticePurges(ctx, 2); err != nil || noticed != 0 {
		t.Fatalf("second bounded live bootstrap notice = %d, %v; want zero notices", noticed, err)
	}
	if err := pool.QueryRow(ctx, `SELECT organization,segmentation_id,initialized FROM projection_purge_bootstrap WHERE singleton`).Scan(&organization, &segmentationID, &initialized); err != nil {
		t.Fatal(err)
	}
	if initialized {
		t.Fatalf("second bounded purge repair unexpectedly completed at %s/%s", organization, segmentationID)
	}

	// A new Version is segmented after the first cycle, then an older binary
	// advances a pointer and tombstones another Record without enqueuing either
	// dead Version. The recurring segmentation repair must find those rows.
	seedQueueVersion(t, ctx, pool, "purge-upgrade", "record-0001", "version-0005", "text-correction", "idle", 5, false)
	if _, err := pool.Exec(ctx, `
INSERT INTO segmentations(organization,id,version_id,recipe,digest,provenance)
	VALUES('purge-upgrade','seg-0005','version-0005','fixture','digest-0005','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE records SET current_version_id='version-0005',desired_version_id='version-0005'
WHERE organization='purge-upgrade' AND id='record-0001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tombstones(organization,record_id) VALUES('purge-upgrade','record-0002')`); err != nil {
		t.Fatal(err)
	}

	if noticed, err := store.NoticePurges(ctx, 2); err != nil || noticed != 0 {
		t.Fatalf("second bounded repair after old-binary writes = %d, %v; want zero notices", noticed, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM projection_purge_candidates`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 0 {
		t.Fatalf("second bounded purge repair left %d live candidates, want zero", candidates)
	}
	if err := pool.QueryRow(ctx, `SELECT initialized FROM projection_purge_bootstrap WHERE singleton`).Scan(&initialized); err != nil {
		t.Fatal(err)
	}
	if !initialized {
		t.Fatal("short purge repair did not persist completion after segment-0005")
	}

	if err := pool.QueryRow(ctx, `SELECT count(*) FROM projection_purge_candidates`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 0 {
		t.Fatalf("completed purge repair left %d live candidates, want zero", candidates)
	}

	if noticed, err := store.NoticePurges(ctx, 2); err != nil || noticed != 2 {
		t.Fatalf("recurring dead-version notice = %d, %v; want two notices", noticed, err)
	}
	var purges int
	for _, version := range []string{"version-0001", "version-0002"} {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM projection_purges WHERE organization='purge-upgrade' AND kind='version' AND version_id=$1`, version).Scan(&purges); err != nil {
			t.Fatal(err)
		}
		if purges != 1 {
			t.Fatalf("dead Version %s purges = %d, want 1", version, purges)
		}
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
