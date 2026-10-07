package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Observation locks follow canonical writer locks. Repair locks observations
// only, then reads canonical state in a fresh statement snapshot. It never
// waits for canonical row locks, which would invert the writer lock order.
const queueObservationKeysSQL = `SELECT organization,record_id FROM unnest($1::text[],$2::text[]) k(organization,record_id) ORDER BY organization,record_id OFFSET 0`

func queueRecordObservations(batch *pgx.Batch, organizations, records []string) {
	batch.Queue(`INSERT INTO queue_enrichment_records(organization,record_id)
 SELECT DISTINCT organization,record_id FROM (`+queueObservationKeysSQL+`) k ORDER BY organization,record_id
 ON CONFLICT DO NOTHING`, organizations, records)
	batch.Queue(`SELECT q.record_id FROM (`+queueObservationKeysSQL+`) k
 JOIN LATERAL (SELECT record_id FROM queue_enrichment_records q
 WHERE (q.organization,q.record_id)=(k.organization,k.record_id) LIMIT 1 FOR UPDATE) q ON true`, organizations, records)
	// Only current/desired Versions newly become dead on a fence. Older
	// Versions already died on pointer changes; bounded segmentation repair
	// catches any candidates missed by an older binary without scanning a
	// Record's entire Version history during observation repair.
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

// Keyset repair also sees writes made by a previous binary. Completion selects
// the narrow membership branch; subsequent passes repair a bounded number of
// records, including changes and new identities behind the previous cursor.
func advanceQueueObservations(ctx context.Context, tx pgx.Tx, limit int) error {
	tag, err := tx.Exec(ctx, `INSERT INTO queue_enrichment_bootstrap(singleton) VALUES(true) ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 0 {
		// History preceding initialization is covered by the record pass. Capture
		// journal heads before that pass so older writers behind its cursor are
		// repaired before publishing a membership-based snapshot.
		if _, err = tx.Exec(ctx, `INSERT INTO queue_observation_journals(organization,position)
 SELECT organization,last_sequence FROM organization_journals ON CONFLICT DO NOTHING`); err != nil {
			return err
		}
	}
	var organization, record string
	var initialized bool
	if err := tx.QueryRow(ctx, `SELECT organization,record_id,initialized FROM queue_enrichment_bootstrap WHERE singleton FOR UPDATE`).Scan(&organization, &record, &initialized); err != nil {
		return err
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
	if err != nil {
		return err
	}
	return repairQueueJournal(ctx, tx, limit)
}

// Segmentation repair is independent of journal retention and progress. It
// revisits bounded key ranges to capture late writes from previous binaries.
func advancePurgeCandidates(ctx context.Context, tx pgx.Tx, limit int) error {
	if _, err := tx.Exec(ctx, `INSERT INTO projection_purge_bootstrap(singleton) VALUES(true) ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	var organization, segmentation string
	var initialized bool
	if err := tx.QueryRow(ctx, `SELECT organization,segmentation_id,initialized FROM projection_purge_bootstrap WHERE singleton FOR UPDATE`).Scan(&organization, &segmentation, &initialized); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT organization,id,version_id FROM segmentations WHERE (organization,id)>($1,$2) ORDER BY organization,id LIMIT $3`, organization, segmentation, limit)
	if err != nil {
		return err
	}
	var organizations, versions []string
	for rows.Next() {
		var version string
		if err = rows.Scan(&organization, &segmentation, &version); err != nil {
			rows.Close()
			return err
		}
		organizations = append(organizations, organization)
		versions = append(versions, version)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if len(versions) > 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO projection_purge_candidates(organization,version_id)
 SELECT DISTINCT organization,version_id FROM unnest($1::text[],$2::text[]) k(organization,version_id) ORDER BY organization,version_id
 ON CONFLICT(organization,version_id) DO UPDATE SET version_id=EXCLUDED.version_id`, organizations, versions); err != nil {
			return err
		}
	}
	if len(versions) < limit {
		initialized = true
		organization = ""
		segmentation = ""
	}
	_, err = tx.Exec(ctx, `UPDATE projection_purge_bootstrap SET organization=$1,segmentation_id=$2,initialized=$3 WHERE singleton`, organization, segmentation, initialized)
	return err
}

// Advance only over a contiguous position maintained by this binary. A gap
// left by an older writer must be replayed before it can be acknowledged.
const acknowledgeQueueJournalSQL = `WITH available AS MATERIALIZED (
 SELECT q.organization,j.last_sequence FROM queue_observation_journals q
 JOIN organization_journals j USING(organization)
 WHERE q.organization=$1 AND q.position=j.last_sequence-$2::bigint
 FOR UPDATE OF q SKIP LOCKED
)
UPDATE queue_observation_journals q SET position=a.last_sequence FROM available a
WHERE q.organization=a.organization`

// Initialization/repair may already hold observation locks. A writer can have
// acknowledged an earlier event in its transaction before observing a later
// Record event, so checkpoint writes never wait while holding observations.
const repairQueueCheckpointSQL = `WITH available AS MATERIALIZED (
 SELECT organization FROM queue_observation_journals WHERE organization=$1 FOR UPDATE SKIP LOCKED
), created AS (
 INSERT INTO queue_observation_journals(organization,position)
 SELECT $1,$2 WHERE NOT EXISTS(SELECT FROM queue_observation_journals WHERE organization=$1)
 ON CONFLICT DO NOTHING
)
UPDATE queue_observation_journals q SET position=greatest(q.position,$2)
FROM available a WHERE q.organization=a.organization`

// Record events include pointer changes and withdrawals from older binaries.
// A bounded journal window repairs them before snapshots. If a window remains,
// the backlog query retains its canonical branch until the checkpoint catches up.
// Pruning resets the bounded record pass instead of silently skipping lost facts.
func repairQueueJournal(ctx context.Context, tx pgx.Tx, budget int) error {
	rows, err := tx.Query(ctx, `SELECT j.organization,j.last_sequence,coalesce(q.position,0),coalesce(p.pruned_through,0)
 FROM organization_journals j LEFT JOIN queue_observation_journals q USING(organization)
 LEFT JOIN change_journal_prunes p USING(organization)
 WHERE j.last_sequence>coalesce(q.position,0) ORDER BY j.organization`)
	if err != nil {
		return err
	}
	type head struct {
		organization           string
		last, position, pruned int64
	}
	var heads []head
	for rows.Next() {
		var h head
		if err = rows.Scan(&h.organization, &h.last, &h.position, &h.pruned); err != nil {
			rows.Close()
			return err
		}
		heads = append(heads, h)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, h := range heads {
		if h.position < h.pruned {
			if _, err = tx.Exec(ctx, `UPDATE queue_enrichment_bootstrap SET organization='',record_id='',initialized=false WHERE singleton`); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, repairQueueCheckpointSQL, h.organization, h.last); err != nil {
				return err
			}
			continue
		}
		if budget <= 0 {
			break
		}
		upper := min(h.last, h.position+int64(budget))
		events, err := tx.Query(ctx, `SELECT DISTINCT resource_id FROM change_events WHERE organization=$1 AND sequence>$2 AND sequence<=$3 AND resource_type='record' ORDER BY resource_id`, h.organization, h.position, upper)
		if err != nil {
			return err
		}
		var organizations, records []string
		for events.Next() {
			var record string
			if err = events.Scan(&record); err != nil {
				events.Close()
				return err
			}
			organizations = append(organizations, h.organization)
			records = append(records, record)
		}
		events.Close()
		if err = events.Err(); err != nil {
			return err
		}
		if err = observeQueueRecords(ctx, tx, organizations, records); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, repairQueueCheckpointSQL, h.organization, upper); err != nil {
			return err
		}
		budget -= int(upper - h.position)
	}
	return nil
}
