package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Queue refresh owns this work bound: waiting work and superseded enrichment must
// not require scanning the backlog, and a pending rebuild must not enumerate its
// scope on every observation. Unlike the count owner, this test observes database work,
// without a timing assertion or a forced planner setting.
func TestQueueBacklogWorkDoesNotGrowWithWaitingDocumentsAndHistory(t *testing.T) {
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
SELECT 'example',kind||'-'||i,'record-'||i,kind,kind,CASE kind WHEN 'old' THEN 1 ELSE 2 END,'','blob','blob','{}',true,'idle','queued'
FROM generate_series($1::int,$2::int) i CROSS JOIN (VALUES('old'),('current')) k(kind)`, low, size)
		exec(`INSERT INTO accepted_revisions(organization,record_id,slot,digest,version_id,acceptance_order,source_position,command,accepted_at)
SELECT organization,record_id,slot,digest,id,acceptance_order,source_position,'{}',now() FROM record_versions
WHERE record_id IN (SELECT 'record-'||i FROM generate_series($1::int,$2::int) i)`, low, size)
		exec("ANALYZE")
		exec(`UPDATE operations SET counters=jsonb_build_object('versions_in_scope',$1::bigint) WHERE organization='example' AND id='rebuild'`, size)
		// Direct fixture writes bypass application observations. Reinitialize the
		// legacy observation pass so both fixture sizes represent waiting work.
		exec(`UPDATE queue_enrichment_bootstrap SET initialized=false,organization='',record_id=''`)
		// Exercise refresh and let the one-time bounded bootstrap finish without
		// waiting for the production snapshot interval.
		for batch := 0; batch <= size/1000; batch++ {
			exec("UPDATE queue_backlog_snapshots SET observed_at='-infinity',published_at='-infinity'")
			if _, err := (QueueSnapshots{Pool: pool}).Maintain(ctx); err != nil {
				t.Fatal(err)
			}
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
		// Include the actual queue-ledger maintenance invoked by source commits
		// and bootstrap, so a full-ledger sweep cannot hide behind cheap reads.
		// Queue ledger statements are appended to the same observation batch.
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
		t.Logf("history=%d records: shared buffers=%d, live waiting=%d, bulk waiting=%d", size, blocks, size, size)
		if size == 1000 {
			firstBlocks = blocks
		} else if blocks > 2*firstBlocks+100 {
			t.Fatalf("backlog query work grew with history/rebuild scope: buffers %d -> %d (bound %d)", firstBlocks, blocks, 2*firstBlocks+100)
		}
	}
	// The freshness cutoff must find one ancient unresolved change without
	// scanning every recent dirty row. Managed-index registration alone cannot
	// protect this work contract; observe the actual production probe's plan.
	var firstCutoffBlocks int
	for _, size := range []int{1000, 10000} {
		exec(`INSERT INTO parallel_queue_observations(organization,record_id,changed_at,dirty_since)
 SELECT 'example','dirty-'||i,clock_timestamp(),clock_timestamp()
 FROM generate_series(1,$1::int) i ON CONFLICT DO NOTHING`, size)
		exec(`UPDATE parallel_queue_observations SET dirty_since=clock_timestamp()-interval '2 minutes'
 WHERE organization='example' AND record_id='dirty-1'; ANALYZE parallel_queue_observations`)
		var age float64
		if err := pool.QueryRow(ctx, `SELECT extract(epoch FROM statement_timestamp()-observed_at) FROM (`+queueCountersCutoffSQL+`) cutoff`).Scan(&age); err != nil || age < 119 || age > 130 {
			t.Fatalf("dirty cutoff age=%f error=%v", age, err)
		}
		var raw []byte
		if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+queueCountersCutoffSQL).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		blocks := queueWorkBlocks(t, raw)
		if !strings.Contains(string(raw), `"Index Name": "parallel_queue_observations_by_age"`) {
			t.Fatalf("oldest dirty lookup missed its age index: %s", raw)
		}
		t.Logf("dirty=%d rows: cutoff buffers=%d", size, blocks)
		if size == 1000 {
			firstCutoffBlocks = blocks
		} else if blocks > 2*firstCutoffBlocks+10 {
			t.Fatalf("oldest dirty work grew with ledger size: buffers %d -> %d", firstCutoffBlocks, blocks)
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

// The lookup indexes are installation-wide contracts: the replacement
// accepted-revision lookup must be present before its partial predecessor is
// retired, and receipt/metadata/notice lookups must be usable by the queue and
// cleanup owners. The plan owners below are the production backlog query, the
// VersionActivity query, and the work-queue fallback subquery.
func TestManagedLookupIndexesAndAcceptedRevisionReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p := queueWorkPool(t, ctx)
	if err := Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIndexes(ctx, p); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"projection_metadata_by_generation":   "CREATE INDEX projection_metadata_by_generation ON public.projection_metadata USING btree (organization, generation_id, version_id)",
		"ingestion_receipts_by_version":       "CREATE INDEX ingestion_receipts_by_version ON public.ingestion_receipts USING btree (organization, version_id, accepted_at)",
		"ingestion_receipts_pending_revision": "CREATE INDEX ingestion_receipts_pending_revision ON public.ingestion_receipts USING btree (organization, record_id, slot) WHERE (state = 'pending'::text)",
		"evaluation_intents_decided_pair":     "CREATE INDEX evaluation_intents_decided_pair ON public.evaluation_intents USING btree (organization, subscription_version_id, record_version_id, sequence) WHERE ((kind = 'evaluation'::text) AND (state = 'done'::text) AND (outcome = ANY (ARRAY['matched'::text, 'duplicate'::text, 'no_match'::text, 'no_longer_matches'::text])))",
		"accepted_revisions_version_lookup":   "CREATE INDEX accepted_revisions_version_lookup ON public.accepted_revisions USING btree (organization, version_id)",
	}
	for name, definition := range want {
		var got string
		var valid bool
		if err := p.QueryRow(ctx, `SELECT i.indisvalid,pg_get_indexdef(i.indexrelid)
FROM pg_index i WHERE i.indexrelid=to_regclass($1)`, name).Scan(&valid, &got); err != nil {
			t.Fatalf("lookup index %s: %v", name, err)
		}
		if !valid || got != definition {
			t.Fatalf("index %s: valid=%t definition=%q, want %q", name, valid, got, definition)
		}
	}
	var retired bool
	if err := p.QueryRow(ctx, `SELECT to_regclass('accepted_revisions_by_version') IS NOT NULL`).Scan(&retired); err != nil {
		t.Fatal(err)
	}
	if retired {
		t.Fatal("partial accepted-revision lookup was retained after its full replacement became valid")
	}

	// Exercise the real queue owner after the duplicate disappears. A tiny
	// installation may choose a sequential plan, but the query must still read
	// the accepted-revision join successfully with the replacement catalog.
	if _, err := p.Exec(ctx, `INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval)
VALUES('index-owner','corpus','corpus','','Index owner','{}');
INSERT INTO content_blobs(organization,blob_id,object_key,sha256,byte_length)
VALUES('index-owner','blob','blob','blob',1);
INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_version_id,desired_version_id)
VALUES('index-owner','record','corpus','source','record','version','version');
INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,text_blob_id,manifest_blob_id,provenance,baseline_ready,processing,enrichment_state)
VALUES('index-owner','version','record','text','digest',1,'1','blob','blob','{}',true,'idle','idle');
INSERT INTO accepted_revisions(organization,record_id,slot,digest,version_id,acceptance_order,source_position,command,accepted_at)
VALUES('index-owner','record','text','digest','version',1,'1','{}',now());
INSERT INTO ingestion_receipts(organization,id,request_key,canonical_request,command,corpus_id,record_id,acceptance_order,slot,digest,version_id,state,outcome,processing,accepted_at,work_queue)
VALUES
 ('index-owner','receipt-old','request-old','', '{}','corpus','record',1,'text','digest','version','resolved','created','idle',now()-interval '1 hour','live'),
 ('index-owner','receipt-new','request-new','', '{}','corpus','record',2,'text','digest','version','resolved','created','idle',now(),'bulk');
INSERT INTO records(organization,id,corpus_id,namespace,record_key)
SELECT 'index-owner','bulk-record-'||i,'corpus','source',i::text FROM generate_series(1,10000) i;
INSERT INTO accepted_revisions(organization,record_id,slot,digest,version_id,acceptance_order,source_position,command,accepted_at)
SELECT 'index-owner','bulk-record-'||i,'text','bulk-digest-'||i,'bulk-version-'||i,i,i::text,'{}',now()
FROM generate_series(1,10000) i;
INSERT INTO ingestion_receipts(organization,id,request_key,canonical_request,command,corpus_id,record_id,acceptance_order,slot,digest,version_id,state,outcome,processing,accepted_at,work_queue)
SELECT 'index-owner','bulk-receipt-'||i,'bulk-request-'||i,'','{}','corpus','bulk-record-'||i,i,'text','bulk-digest-'||i,'bulk-version-'||i,'resolved','created','idle',now(),'bulk'
FROM generate_series(1,10000) i;
INSERT INTO queue_document_attempts(organization,kind,work_id,document_id,token,lease_until)
VALUES('index-owner','operation','missing-operation','version',1,now()+interval '1 hour');
ANALYZE accepted_revisions;
ANALYZE ingestion_receipts`); err != nil {
		t.Fatal(err)
	}
	var queue string
	if err := p.QueryRow(ctx, `SELECT work_queue FROM ingestion_receipts
WHERE organization=$1 AND version_id=$2 ORDER BY accepted_at LIMIT 1`, "index-owner", "version").Scan(&queue); err != nil || queue != "live" {
		t.Fatalf("earliest receipt queue=%q err=%v, want live", queue, err)
	}
	// Raw fixture writes bypass transactional observations. Run the independent
	// startup maintenance before reading the maintained backlog.
	for {
		more, err := (QueueSnapshots{Pool: p}).Maintain(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	rows, err := p.Query(ctx, queueBacklogSQL())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var queues int
	for rows.Next() {
		queues++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if queues != 2 {
		t.Fatalf("backlog owner returned %d queues, want live and bulk", queues)
	}
	var backlogPlan []byte
	if err := p.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+queueBacklogSQL()).Scan(&backlogPlan); err != nil {
		t.Fatal(err)
	}
	if !planContainsRelation(t, backlogPlan, "accepted_revisions") {
		t.Fatal("queue backlog owner did not execute an accepted-revision join")
	}
	var versionPlan []byte
	// VersionActivity uses this exact activityColumns statement for its first
	// accepted-revision lookup by Version id.
	versionActivitySQL := activityColumns + ` WHERE a.organization=$1 AND a.version_id=$2 AND a.accepted_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM corpora c WHERE c.organization=r.organization AND c.id=r.corpus_id AND c.archived)`
	if err := p.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+versionActivitySQL, "index-owner", "version").Scan(&versionPlan); err != nil {
		t.Fatal(err)
	}
	if !planContainsIndex(t, versionPlan, "accepted_revisions_version_lookup") {
		t.Fatal("version activity owner did not use the full accepted-revision lookup")
	}
	var fallbackPlan []byte
	// This is the standalone form of the fallback subquery used by the
	// evaluation and serving projection INSERT owners.
	if err := p.QueryRow(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)

SELECT work_queue FROM ingestion_receipts
WHERE organization=$1 AND version_id=$2 ORDER BY accepted_at LIMIT 1`, "index-owner", "version").Scan(&fallbackPlan); err != nil {
		t.Fatal(err)
	}
	if !planContainsIndex(t, fallbackPlan, "ingestion_receipts_by_version") {
		t.Fatal("work-queue fallback did not use the receipt-by-version lookup")
	}
	if _, err := p.Exec(ctx, "CREATE INDEX CONCURRENTLY accepted_revisions_by_version ON accepted_revisions(version_id)"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIndexes(ctx, p); err == nil || !errors.Is(err, ErrIndexConflict) {
		t.Fatalf("incompatible retired index was not rejected: %v", err)
	}
	var conflictingDefinition string
	if err := p.QueryRow(ctx, "SELECT pg_get_indexdef('accepted_revisions_by_version'::regclass)").Scan(&conflictingDefinition); err != nil {
		t.Fatal(err)
	}
	if conflictingDefinition != "CREATE INDEX accepted_revisions_by_version ON public.accepted_revisions USING btree (version_id)" {
		t.Fatalf("incompatible retired index changed: %s", conflictingDefinition)
	}
}

func planContainsIndex(t *testing.T, raw []byte, name string) bool {
	t.Helper()
	var plans any
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	var visit func(any) bool
	visit = func(value any) bool {
		switch node := value.(type) {
		case map[string]any:
			if node["Index Name"] == name {
				return true
			}
			for _, child := range node {
				if visit(child) {
					return true
				}
			}
		case []any:
			for _, child := range node {
				if visit(child) {
					return true
				}
			}
		}
		return false
	}
	return visit(plans)
}

func planContainsRelation(t *testing.T, raw []byte, name string) bool {
	t.Helper()
	var plans any
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	var visit func(any) bool
	visit = func(value any) bool {
		switch node := value.(type) {
		case map[string]any:
			if node["Relation Name"] == name {
				return true
			}
			for _, child := range node {
				if visit(child) {
					return true
				}
			}
		case []any:
			for _, child := range node {
				if visit(child) {
					return true
				}
			}
		}
		return false
	}
	return visit(plans)
}

// Generation metadata discovery is a distinct work owner from compact
// coverage. The trace is captured from RecordPurge itself, then explained in a
// rollback transaction, so the assertion cannot drift to a hand-maintained
// copy of the production statement.
func TestGenerationMetadataPurgeWorkDoesNotGrowWithOtherGenerations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var first int
	for _, size := range []int{1000, 10000} {
		pool := queueWorkPool(t, ctx)
		if err := Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if err := EnsureIndexes(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO projection_purges(organization,kind,corpus_id,generation_id,version_id)
VALUES('metadata-work','generation','target','target-generation','')`); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO projection_metadata(organization,version_id,generation_id,data)
VALUES('metadata-work','target-version','target-generation','{}')`); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO projection_metadata(organization,version_id,generation_id,data)
SELECT 'metadata-work','other-version-'||i,'other-generation','{}' FROM generate_series(1,$1::int) i`, size); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `ANALYZE projection_metadata`); err != nil {
			t.Fatal(err)
		}
		trace := &purgeQueryTrace{}
		config := pool.Config()
		config.ConnConfig.Tracer = trace
		observed, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		defer observed.Close()
		item := retrieval.PurgeItem{Kind: retrieval.PurgeGeneration, Organization: "metadata-work", CorpusID: "target", GenerationID: "target-generation"}
		if _, err := (PurgeStore{Pool: observed}).RecordPurge(ctx, item, 0, true, 1); err != nil {
			t.Fatal(err)
		}
		var captured tracedPurgeQuery
		for _, query := range trace.queries {
			if strings.Contains(query.sql, "FROM projection_metadata pm") {
				captured = query
			}
		}
		if captured.sql == "" {
			t.Fatal("RecordPurge did not issue the generation metadata query")
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var raw []byte
		explainArgs := make([]any, len(captured.args))
		for i, arg := range captured.args {
			if value, ok := arg.(string); ok {
				explainArgs[i] = pgtype.Text{String: value, Valid: true}
			} else {
				explainArgs[i] = arg
			}
		}
		if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+captured.sql, explainArgs...).Scan(&raw); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		blocks := queueWorkBlocks(t, raw)
		t.Logf("other generations=%d: generation metadata purge buffers=%d", size, blocks)
		if size == 1000 {
			first = blocks
		} else if blocks > 2*first+100 {
			t.Fatalf("generation-selective metadata work grew with unrelated rows: buffers %d -> %d (bound %d)", first, blocks, 2*first+100)
		}
	}
}

type tracedPurgeQuery struct {
	sql  string
	args []any
}

type purgeQueryTrace struct {
	queries []tracedPurgeQuery
}

func (t *purgeQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	t.queries = append(t.queries, tracedPurgeQuery{sql: data.SQL, args: append([]any(nil), data.Args...)})
	return ctx
}

func (*purgeQueryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

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
	// Match the smallest default pool on CI, independent of host CPU count.
	connection.MaxConns = 4
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
func TestQueueBootstrapRecomputesAfterObservationWriterCommits(t *testing.T) {
	for _, ledger := range []string{"enrichment", "backlog"} {
		t.Run(ledger, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			pool := queueWorkPool(t, ctx)
			if err := Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			seedQueueCorpus(t, ctx, pool, "race")
			seedQueueRecord(t, ctx, pool, "race", "record", "version", "text", "queued", 1, true)
			bootstrapTable := "queue_enrichment_bootstrap"
			advance := func(ctx context.Context, tx pgx.Tx) error { return advanceQueueObservations(ctx, tx, 1000) }
			if ledger == "backlog" {
				bootstrapTable = "queue_backlog_bootstrap"
				advance = func(ctx context.Context, tx pgx.Tx) error {
					_, err := advanceQueueBacklogBootstrap(ctx, tx, 1000)
					return err
				}
			}
			if _, err := pool.Exec(ctx, "UPDATE "+bootstrapTable+" SET initialized=false,organization='',record_id=''"); err != nil {
				t.Fatal(err)
			}
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
				err := advance(ctx, bootstrapTx)
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
			var count int64
			if err := pool.QueryRow(ctx, `SELECT sum(pending)::bigint FROM queue_backlog_totals`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("bootstrap restored queue membership: pending=%d error=%v", count, err)
			}
			var initialized bool
			if err := pool.QueryRow(ctx, "SELECT initialized FROM "+bootstrapTable+" WHERE singleton").Scan(&initialized); err != nil {
				t.Fatal(err)
			}
			if !initialized {
				t.Fatal("bootstrap did not persist completion after recomputing the committed state")
			}

			// Return the race participants before the independent lock probe. Keeping
			// all three checked out starves a four-connection pool on its fifth acquire.
			writer.Release()
			bootstrap.Release()
			observer.Release()

			// A completed bootstrap does not need the records table again, even while
			// an unrelated writer holds an exclusive lock on it.
			locked, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer locked.Rollback(ctx)
			if _, err = locked.Exec(ctx, `LOCK TABLE records IN ACCESS EXCLUSIVE MODE`); err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			read, stop := context.WithTimeout(ctx, time.Second)
			defer stop()
			if err = advance(read, tx); err != nil {
				t.Fatalf("completed bootstrap accessed records: %v", err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}

		})
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
	if _, err := snapshots.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshots.Pool.Exec(ctx, `UPDATE queue_backlog_snapshots SET observed_at='-infinity',published_at='-infinity'`); err != nil {
		t.Fatal(err)
	}
	if err := snapshots.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
}
