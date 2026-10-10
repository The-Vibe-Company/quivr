package postgres

import "github.com/jackc/pgx/v5"

// Canonical locks precede ledger locks. Lock the entire ledger set in one
// statement, then read canonical state in the next READ COMMITTED snapshot.
// Aggregate writes follow the same table/key order in every batch. The small
// previous-key object survives only until cleanup in this transaction.
var corpusRecordObservationSQL = []string{
	`INSERT INTO corpus_record_observations(organization,record_id)
 SELECT DISTINCT organization,record_id FROM (` + queueObservationKeysSQL + `) k
 ORDER BY organization,record_id ON CONFLICT DO NOTHING`,
	`SELECT o.record_id FROM (` + queueObservationKeysSQL + `) k
 JOIN LATERAL (SELECT record_id FROM corpus_record_observations o
 WHERE (o.organization,o.record_id)=(k.organization,k.record_id) LIMIT 1 FOR UPDATE) o ON true`,
	`WITH before AS MATERIALIZED (
 SELECT o.* FROM (` + queueObservationKeysSQL + `) k
 JOIN LATERAL (SELECT * FROM corpus_record_observations o WHERE (o.organization,o.record_id)=(k.organization,k.record_id) LIMIT 1) o ON true
 ), states AS MATERIALIZED (
 SELECT b.organization,b.record_id,coalesce(r.corpus_id,b.corpus_id) AS corpus_id,
 coalesce(r.namespace,b.namespace) AS namespace,
 CASE WHEN r.id IS NULL THEN b.connector_id ELSE coalesce(a.connector_instance_id,p.connector_instance_id,'unknown') END AS connector_id,
 CASE WHEN r.id IS NULL THEN b.shard ELSE (hashtextextended(r.id,0) & 15)::smallint END AS shard,
 CASE WHEN r.id IS NULL THEN b.hour ELSE date_trunc('hour',r.current_accepted_at,'UTC') END AS hour,
 r.id IS NOT NULL AS catalog,
 coalesce(v.baseline_ready AND NOT v.quarantined AND NOT r.withdrawn AND
 NOT EXISTS(SELECT FROM tombstones t WHERE (t.organization,t.record_id)=(b.organization,r.id)),false) AS eligible
 FROM before b
 LEFT JOIN LATERAL (SELECT id,corpus_id,namespace,current_version_id,desired_version_id,current_accepted_at,withdrawn FROM records r
 WHERE (r.organization,r.id)=(b.organization,b.record_id) LIMIT 1) r ON true
 LEFT JOIN LATERAL (SELECT id,slot,baseline_ready,quarantined FROM record_versions v
 WHERE (v.organization,v.id)=(b.organization,r.current_version_id) LIMIT 1) v ON true
 LEFT JOIN LATERAL (SELECT connector_instance_id FROM accepted_revisions a
 WHERE (a.organization,a.record_id,a.slot)=(b.organization,b.record_id,v.slot) LIMIT 1) a ON true
 LEFT JOIN LATERAL (SELECT connector_instance_id FROM accepted_revisions p
 WHERE v.id IS NULL AND (p.organization,p.version_id)=(b.organization,r.desired_version_id)
 AND p.accepted_at IS NOT NULL LIMIT 1) p ON true
 ), updated AS (
 UPDATE corpus_record_observations o SET corpus_id=s.corpus_id,namespace=s.namespace,
 connector_id=s.connector_id,shard=s.shard,hour=s.hour,catalog=s.catalog,eligible=s.eligible,
 previous=jsonb_build_object('corpus_id',o.corpus_id,'namespace',o.namespace,'connector_id',o.connector_id,'shard',o.shard,'hour',o.hour)
 FROM states s WHERE (o.organization,o.record_id)=(s.organization,s.record_id)
 AND (o.corpus_id,o.namespace,o.connector_id,o.shard,o.hour,o.catalog,o.eligible)
 IS DISTINCT FROM (s.corpus_id,s.namespace,s.connector_id,s.shard,s.hour,s.catalog,s.eligible)
 RETURNING o.*
 ), changes AS MATERIALIZED (
 SELECT b.organization,b.corpus_id,b.namespace,b.connector_id,b.shard,b.hour,b.catalog,b.eligible,-1 AS sign
 FROM before b JOIN updated u USING(organization,record_id)
 UNION ALL
 SELECT organization,corpus_id,namespace,connector_id,shard,hour,catalog,eligible,1 FROM updated
 ), totals AS (
 INSERT INTO corpus_record_totals(organization,corpus_id,shard,eligible,catalog,undated,catalog_undated)
 SELECT organization,corpus_id,shard,sum(eligible::int*sign),sum(catalog::int*sign),
 sum((eligible AND hour IS NULL)::int*sign),sum((catalog AND hour IS NULL)::int*sign)
 FROM changes WHERE corpus_id<>'' GROUP BY organization,corpus_id,shard
 HAVING sum(eligible::int*sign)<>0 OR sum(catalog::int*sign)<>0 OR sum((eligible AND hour IS NULL)::int*sign)<>0 OR sum((catalog AND hour IS NULL)::int*sign)<>0
 ORDER BY organization,corpus_id,shard
 ON CONFLICT(organization,corpus_id,shard) DO UPDATE SET
 eligible=corpus_record_totals.eligible+EXCLUDED.eligible,catalog=corpus_record_totals.catalog+EXCLUDED.catalog,
 undated=corpus_record_totals.undated+EXCLUDED.undated,catalog_undated=corpus_record_totals.catalog_undated+EXCLUDED.catalog_undated
 RETURNING 1
 ), hours AS (
 INSERT INTO corpus_record_hours(organization,corpus_id,hour,shard,eligible,catalog)
 SELECT organization,corpus_id,hour,shard,sum(eligible::int*sign),sum(catalog::int*sign)
 FROM changes
 WHERE (SELECT count(*) FROM totals)>=0 AND corpus_id<>'' AND hour IS NOT NULL GROUP BY organization,corpus_id,hour,shard
 HAVING sum(eligible::int*sign)<>0 OR sum(catalog::int*sign)<>0
 ORDER BY organization,corpus_id,hour,shard
 ON CONFLICT(organization,corpus_id,hour,shard) DO UPDATE SET
 eligible=corpus_record_hours.eligible+EXCLUDED.eligible,catalog=corpus_record_hours.catalog+EXCLUDED.catalog
 RETURNING 1
 )
 INSERT INTO corpus_record_sources(organization,corpus_id,namespace,connector_id,shard,eligible,catalog)
 SELECT organization,corpus_id,namespace,connector_id,shard,sum(eligible::int*sign),sum(catalog::int*sign)
 FROM changes
 WHERE (SELECT count(*) FROM hours)>=0 AND corpus_id<>'' GROUP BY organization,corpus_id,namespace,connector_id,shard
 HAVING sum(eligible::int*sign)<>0 OR sum(catalog::int*sign)<>0
 ORDER BY organization,corpus_id,namespace COLLATE "C",connector_id COLLATE "C",shard
 ON CONFLICT(organization,corpus_id,namespace,connector_id,shard) DO UPDATE SET
 eligible=corpus_record_sources.eligible+EXCLUDED.eligible,catalog=corpus_record_sources.catalog+EXCLUDED.catalog`,
	// Delete only this batch's changed keys. A zero key must disappear so occupied
	// range seeks and source pagination never accumulate dead history.
	`DELETE FROM corpus_record_hours h USING (` + corpusChangedKeysSQL + `) d
 WHERE (h.organization,h.corpus_id,h.hour,h.shard)=(d.organization,d.corpus_id,d.hour,d.shard) AND h.catalog=0 AND h.eligible=0`,
	`DELETE FROM corpus_record_sources s USING (` + corpusChangedKeysSQL + `) d
 WHERE (s.organization,s.corpus_id,s.namespace,s.connector_id,s.shard)=(d.organization,d.corpus_id,d.namespace,d.connector_id,d.shard) AND s.catalog=0 AND s.eligible=0`,
	`UPDATE corpus_record_observations o SET previous=NULL FROM (` + queueObservationKeysSQL + `) k
 WHERE (o.organization,o.record_id)=(k.organization,k.record_id) AND previous IS NOT NULL`,
}

const corpusChangedKeysSQL = `SELECT o.organization,d.* FROM (` + queueObservationKeysSQL + `) k
 JOIN LATERAL (SELECT * FROM corpus_record_observations o WHERE (o.organization,o.record_id)=(k.organization,k.record_id) LIMIT 1) o ON true
 CROSS JOIN LATERAL (
 SELECT o.corpus_id,o.namespace,o.connector_id,o.shard,o.hour
 UNION ALL SELECT p.corpus_id,p.namespace,p.connector_id,p.shard,p.hour
 FROM jsonb_to_record(o.previous) p(corpus_id text,namespace text,connector_id text,shard smallint,hour timestamptz)
 ) d WHERE o.previous IS NOT NULL`

func queueCorpusRecordObservations(batch *pgx.Batch, organizations, records []string) {
	for _, sql := range corpusRecordObservationSQL {
		batch.Queue(sql, organizations, records)
	}
}
