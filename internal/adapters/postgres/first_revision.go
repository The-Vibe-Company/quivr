package postgres

import (
	"context"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

// acceptFirstRevision writes a new Record's reservation, receipt, dispatch
// intent, both journal facts, and queue observations in a single batch round
// trip inside the caller's transaction. If a concurrent writer raced and
// created the Record or Receipt first, created is false and the caller rolls
// back this transaction before falling through to the general revision path.
func acceptFirstRevision(ctx context.Context, tx pgx.Tx, org string, c content.Command, canonical []byte, requestDigest [32]byte, recordID, receiptID, slot, digest string, retainDetail bool) (bool, error) {
	versionID := content.StableID("version", org, recordID, slot)
	pending := eventInput{Organization: org, CorpusID: c.Source.CorpusID, Kind: "receipt.pending", Resource: "receipt", ResourceID: receiptID}
	accepted := eventInput{Organization: org, CorpusID: c.Source.CorpusID, Kind: "record.accepted", Resource: "record", ResourceID: recordID, MutationID: receiptID}
	requestCopy, receiptCommand, execution := importRequestStorage(canonical, retainDetail)
	batch := &pgx.Batch{}
	batch.Queue(`WITH mode AS MATERIALIZED (SELECT pg_advisory_xact_lock_shared($2))
 SELECT pg_advisory_xact_lock_shared($1) FROM mode`, projectionRoutingLock, journalModeLock)
	queueJournalClaim(batch, org, "ingestion", c.Key)
	queueJournalClaim(batch, org, "record", recordID)
	batch.Queue(`INSERT INTO organization_journals(organization) SELECT $1 WHERE NOT `+parallelJournalSQL+` ON CONFLICT DO NOTHING`, org)
	batch.Queue(`SELECT last_sequence FROM organization_journals WHERE organization=$1 AND NOT `+parallelJournalSQL+` FOR UPDATE`, org)
	cteIndex := batch.Len()
	batch.Queue(`WITH record AS (
 INSERT INTO records(organization,id,corpus_id,namespace,record_key,acceptance_order,desired_order,desired_position,desired_version_id,record_acl)
 SELECT $1,$2,$4,$5,$6,1,1,$7,$8,$21::text[]
 WHERE EXISTS(SELECT 1 FROM corpora WHERE organization=$1 AND id=$4)
 AND NOT EXISTS(SELECT 1 FROM ingestion_receipts WHERE organization=$1 AND route_family='ingestion' AND request_key=$14)
 ON CONFLICT DO NOTHING RETURNING id,organization,corpus_id,namespace,record_acl
)`+recordVisibilityAdmissionSQL+`, revision AS (
 INSERT INTO accepted_revisions(organization,record_id,slot,digest,version_id,acceptance_order,source_position,command,accepted_at,title,source_media_type,connector_instance_id)
 SELECT $1,id,$9,$10,$8,1,$7,$19::jsonb,now(),nullif($12,''),coalesce(nullif($13,''),'text/plain'),coalesce(nullif($20,''),'unknown') FROM record
 RETURNING record_id
), receipt AS (
 INSERT INTO ingestion_receipts(organization,id,request_key,canonical_request,command,corpus_id,record_id,acceptance_order,slot,digest,work_queue,request_digest)
 SELECT $1,$3,$14,$11::bytea,$17::jsonb,$4,record_id,1,$9,$10,$16,$18::bytea FROM revision ON CONFLICT DO NOTHING RETURNING id
), outbox AS (
 INSERT INTO ingestion_outbox(organization,receipt_id,trace_context,work_queue)
 SELECT $1,id,$15,$16 FROM receipt WHERE $16<>'bulk' RETURNING receipt_id
), bulk_outbox AS (
 INSERT INTO bulk_ingestion_outbox(organization,receipt_id,trace_context,work_queue)
 SELECT $1,id,$15,$16 FROM receipt WHERE $16='bulk' RETURNING receipt_id
), mode AS MATERIALIZED (
 SELECT `+parallelJournalSQL+` AS parallel
), writer AS MATERIALIZED (
 SELECT pg_current_xact_id() AS xid FROM mode WHERE parallel AND EXISTS(SELECT 1 FROM receipt)
), legacy AS (
 UPDATE organization_journals SET last_sequence=last_sequence+2
 WHERE organization=$1 AND NOT (SELECT parallel FROM mode) AND EXISTS(SELECT 1 FROM receipt)
 RETURNING last_sequence-2 AS base
), positions AS MATERIALIZED (
 SELECT base,NULL::xid8 AS xid,false AS parallel FROM legacy
 UNION ALL SELECT 0::bigint,xid,true FROM writer
), inputs AS MATERIALIZED (
 SELECT * FROM unnest($22::text[],$23::text[],$24::text[],$25::text[])
 WITH ORDINALITY e(id,kind,resource,resource_id,n) ORDER BY e.n
), events AS (
 INSERT INTO change_events(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id,record_version_id,trace_context,writer_xid)
 SELECT $1,CASE WHEN p.parallel THEN nextval('change_event_positions') ELSE p.base+e.n END,
 e.id,$4,e.kind,e.resource,e.resource_id,NULL::text,$15,p.xid
 FROM inputs e CROSS JOIN positions p ORDER BY e.n
 RETURNING 1
) SELECT EXISTS(SELECT 1 FROM receipt)`,
		org, recordID, receiptID, c.Source.CorpusID, c.Source.Namespace, c.Source.RecordKey, c.Position, versionID,
		slot, digest, requestCopy, content.Title(c), c.SourceMediaType, c.Key, telemetry.Encode(ctx), workqueue.Class(ctx),
		receiptCommand, requestDigest[:], execution, c.ConnectorInstanceID, c.ACL,
		[]string{eventID(pending), eventID(accepted)},
		[]string{pending.Kind, accepted.Kind},
		[]string{pending.Resource, accepted.Resource},
		[]string{pending.ResourceID, accepted.ResourceID},
	)
	queueRecordObservations(batch, []string{org}, []string{recordID})
	results := tx.SendBatch(ctx, batch)
	defer results.Close()
	for range cteIndex {
		if _, err := results.Exec(); err != nil {
			return false, err
		}
	}
	var created bool
	if err := results.QueryRow().Scan(&created); err != nil {
		return false, err
	}
	for range batch.Len() - cteIndex - 1 {
		if _, err := results.Exec(); err != nil {
			return false, err
		}
	}
	if err := results.Close(); err != nil {
		return false, err
	}
	return created, nil
}
