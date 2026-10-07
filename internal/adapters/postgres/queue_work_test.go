package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"
)

// Queue refresh owns this work bound: superseded queued enrichment must not
// require scanning history while one actual waiting document stays fixed. Unlike
// the count owner, this test observes database work, without a timing assertion
// or a forced planner setting.
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
	var firstBlocks, firstRepairBlocks int
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
		// Exercise refresh and allow bounded initialization to finish without
		// waiting for the production one-second snapshot interval.
		for batch := 0; batch <= size/1000; batch++ {
			exec("UPDATE queue_backlog_snapshots SET observed_at='-infinity'")
			if err := (QueueSnapshots{Pool: pool}).Refresh(ctx); err != nil {
				t.Fatal(err)
			}
		}
		statuses, err := (QueueSnapshots{Pool: pool}).QueueBacklog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range statuses {
			expected := int64(0)
			if status.Queue == "live" {
				expected = 1
			}
			if status.Waiting != expected || status.InProgress != 0 {
				t.Fatalf("history contributed queue work: %+v, want waiting %d and no active attempts", status, expected)
			}
		}
		exec("ANALYZE")
		// Repair must also point-read a fixed batch rather than hash unrelated
		// history. Explain the exact statements queued by the production owner.
		var organizations, records []string
		for i := 1; i <= 100; i++ {
			organizations = append(organizations, "example")
			records = append(records, fmt.Sprintf("record-%d", i))
		}
		repair := &pgx.Batch{}
		queueRecordObservations(repair, organizations, records)
		repairTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		repairBlocks := 0
		for _, query := range repair.QueuedQueries {
			var raw []byte
			if err = repairTx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query.SQL, query.Arguments...).Scan(&raw); err != nil {
				_ = repairTx.Rollback(ctx)
				t.Fatal(err)
			}
			repairBlocks += queueWorkBlocks(t, raw)
		}
		if err = repairTx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		t.Logf("history=%d records: fixed 100-record observation repair buffers=%d", size, repairBlocks)
		if size == 1000 {
			firstRepairBlocks = repairBlocks
		} else if repairBlocks > 2*firstRepairBlocks+100 {
			t.Fatalf("fixed observation repair grew with unrelated history: buffers %d -> %d (bound %d)", firstRepairBlocks, repairBlocks, 2*firstRepairBlocks+100)
		}

		var raw []byte
		if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+queueBacklogSQL()).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		blocks := queueWorkBlocks(t, raw)
		t.Logf("history=%d records: shared buffers=%d, waiting=1", size, blocks)
		if size == 1000 {
			firstBlocks = blocks
		} else if blocks > 2*firstBlocks+100 {
			t.Fatalf("fixed one-document backlog work grew with superseded history: buffers %d -> %d (bound %d)", firstBlocks, blocks, 2*firstBlocks+100)
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
		// Finish the one-time keyset bootstrap, then model the steady-state batch.
		for batch := 0; batch <= size/1000; batch++ {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err = advancePurgeCandidates(ctx, tx, 1000); err != nil {
				_ = tx.Rollback(ctx)
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
		exec(`DELETE FROM projection_purge_candidates`)
		exec(`VACUUM projection_purge_candidates`)
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
