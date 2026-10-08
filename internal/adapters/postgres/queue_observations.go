package postgres

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/content"

	"github.com/jackc/pgx/v5"
)

// Observation locks follow canonical writer locks. The bootstrap locks
// observations only, then reads canonical state in a fresh statement snapshot.
// It never waits for canonical row locks, which would invert the writer lock order.
const queueObservationKeysSQL = `SELECT organization,record_id FROM unnest($1::text[],$2::text[]) k(organization,record_id) ORDER BY organization,record_id OFFSET 0`

func queueRecordObservations(batch *pgx.Batch, organizations, records []string) {
	batch.Queue(`INSERT INTO queue_enrichment_records(organization,record_id)
 SELECT DISTINCT organization,record_id FROM (`+queueObservationKeysSQL+`) k ORDER BY organization,record_id
 ON CONFLICT DO NOTHING`, organizations, records)
	batch.Queue(`SELECT q.record_id FROM (`+queueObservationKeysSQL+`) k
 JOIN LATERAL (SELECT record_id FROM queue_enrichment_records q
 WHERE (q.organization,q.record_id)=(k.organization,k.record_id) LIMIT 1 FOR UPDATE) q ON true`, organizations, records)
	// Only current/desired Versions newly become dead on a fence. Older
	// Versions already died on pointer changes, so observation never scans a
	// Record's entire Version history.
	batch.Queue(`INSERT INTO projection_purge_candidates(organization,version_id)
 SELECT DISTINCT q.organization,p.version_id FROM (`+queueObservationKeysSQL+`) k
 JOIN LATERAL (SELECT * FROM queue_enrichment_records q WHERE (q.organization,q.record_id)=(k.organization,k.record_id) LIMIT 1) q ON true
 JOIN LATERAL (SELECT current_version_id,desired_version_id,withdrawn FROM records r WHERE (r.organization,r.id)=(k.organization,k.record_id) LIMIT 1) r ON true
 CROSS JOIN LATERAL (VALUES(q.version_id,r.current_version_id),(q.desired_version_id,r.desired_version_id),
 (coalesce(r.current_version_id,''),r.current_version_id),(coalesce(r.desired_version_id,''),r.desired_version_id)) p(version_id,current_id)
 WHERE p.version_id<>'' AND (p.version_id IS DISTINCT FROM p.current_id OR
 (NOT q.gone AND (r.withdrawn OR EXISTS(SELECT FROM tombstones t WHERE (t.organization,t.record_id)=(k.organization,k.record_id)))))
 ORDER BY q.organization,p.version_id
 ON CONFLICT(organization,version_id) DO UPDATE SET version_id=EXCLUDED.version_id`, organizations, records)
	batch.Queue(`UPDATE queue_enrichment_records q SET
 version_id=coalesce(r.current_version_id,''),desired_version_id=coalesce(r.desired_version_id,''),
 gone=coalesce(r.withdrawn,true) OR EXISTS(SELECT FROM tombstones t WHERE (t.organization,t.record_id)=(q.organization,q.record_id)),
 pending=coalesce(v.baseline_ready AND NOT v.quarantined AND v.enrichment_state IN ('queued','running','retrying') AND NOT r.withdrawn
 AND NOT EXISTS(SELECT FROM tombstones t WHERE (t.organization,t.record_id)=(q.organization,q.record_id)),false)
 FROM (`+queueObservationKeysSQL+`) k
 LEFT JOIN LATERAL (SELECT current_version_id,desired_version_id,withdrawn FROM records r WHERE (r.organization,r.id)=(k.organization,k.record_id) LIMIT 1) r ON true
 LEFT JOIN LATERAL (SELECT baseline_ready,quarantined,enrichment_state FROM record_versions v WHERE (v.organization,v.id)=(k.organization,r.current_version_id) LIMIT 1) v ON true
 WHERE (q.organization,q.record_id)=(k.organization,k.record_id)
 AND (q.version_id,q.desired_version_id,q.gone,q.pending) IS DISTINCT FROM (
 coalesce(r.current_version_id,''),coalesce(r.desired_version_id,''),
 coalesce(r.withdrawn,true) OR EXISTS(SELECT FROM tombstones t WHERE (t.organization,t.record_id)=(k.organization,k.record_id)),
 coalesce(v.baseline_ready AND NOT v.quarantined AND v.enrichment_state IN ('queued','running','retrying') AND NOT r.withdrawn
 AND NOT EXISTS(SELECT FROM tombstones t WHERE (t.organization,t.record_id)=(k.organization,k.record_id)),false))
 AND q.ctid=ANY(ARRAY(SELECT locked.ctid FROM (`+queueObservationKeysSQL+`) ids
 JOIN LATERAL (SELECT ctid FROM queue_enrichment_records p WHERE (p.organization,p.record_id)=(ids.organization,ids.record_id) LIMIT 1) locked ON true))`, organizations, records)
}

func observeQueueRecords(ctx context.Context, tx pgx.Tx, organizations, records []string) error {
	if group := journalGroupOf(ctx); group != nil {
		for i, record := range records {
			if organizations[i] != group.organization {
				return content.ErrInvalid
			}
			group.records[record] = true
		}
		return nil
	}
	if len(records) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	queueRecordObservations(batch, organizations, records)
	return tx.SendBatch(ctx, batch).Close()
}

func observeQueueVersion(ctx context.Context, tx pgx.Tx, organization, version string) error {
	var record string
	err := tx.QueryRow(ctx, `SELECT record_id FROM record_versions WHERE organization=$1 AND id=$2`, organization, version).Scan(&record)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return observeQueueRecords(ctx, tx, []string{organization}, []string{record})
}

func enqueueProjectionPurge(ctx context.Context, tx pgx.Tx, organization, version string) error {
	_, err := tx.Exec(ctx, `INSERT INTO projection_purge_candidates(organization,version_id)
 VALUES($1,$2) ON CONFLICT(organization,version_id) DO UPDATE SET version_id=EXCLUDED.version_id`, organization, version)
	return err
}

// The bootstrap observes records written before observation existed, in
// bounded keyset passes. Writers maintain observations themselves, so once a
// pass reaches the end the bootstrap is done and never scans records again.
func advanceQueueObservations(ctx context.Context, tx pgx.Tx, limit int) error {
	if _, err := tx.Exec(ctx, `INSERT INTO queue_enrichment_bootstrap(singleton) VALUES(true) ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	var organization, record string
	var initialized bool
	if err := tx.QueryRow(ctx, `SELECT organization,record_id,initialized FROM queue_enrichment_bootstrap WHERE singleton FOR UPDATE`).Scan(&organization, &record, &initialized); err != nil {
		return err
	}
	if initialized {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT organization,id FROM records WHERE (organization,id)>($1,$2) ORDER BY organization,id LIMIT $3`, organization, record, limit)
	if err != nil {
		return err
	}
	var organizations, records []string
	for rows.Next() {
		if err = rows.Scan(&organization, &record); err != nil {
			rows.Close()
			return err
		}
		organizations = append(organizations, organization)
		records = append(records, record)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if err = observeQueueRecords(ctx, tx, organizations, records); err != nil {
		return err
	}
	if len(records) < limit {
		initialized = true
		organization = ""
		record = ""
	}
	_, err = tx.Exec(ctx, `UPDATE queue_enrichment_bootstrap SET organization=$1,record_id=$2,initialized=$3 WHERE singleton`, organization, record, initialized)
	return err
}
