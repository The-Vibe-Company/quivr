package postgres

import (
	"context"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
)

// acceptFirstRevision writes a new Record's reservation, receipt, dispatch
// intent and both journal facts in one statement. Existing Records take the
// general revision path, including reverts and source-position arbitration.
// The caller already holds the journal lock and checked the request key and
// Corpus. Every ID is still computed by the domain's canonical hash function.
func acceptFirstRevision(ctx context.Context, tx pgx.Tx, org string, c content.Command, canonical []byte, recordID, receiptID, slot, digest string) (bool, error) {
	versionID := content.StableID("version", org, recordID, slot)
	pending := eventInput{Organization: org, Kind: "receipt.pending", Resource: "receipt", ResourceID: receiptID}
	accepted := eventInput{Organization: org, Kind: "record.accepted", Resource: "record", ResourceID: recordID, MutationID: receiptID}
	var created bool
	err := tx.QueryRow(ctx, `WITH record AS (
 INSERT INTO records(organization,id,corpus_id,namespace,record_key,acceptance_order,desired_order,desired_position,desired_version_id)
 VALUES($1,$2,$4,$5,$6,1,1,$7,$8) ON CONFLICT DO NOTHING RETURNING id
), revision AS (
 INSERT INTO accepted_revisions(organization,record_id,slot,digest,version_id,acceptance_order,source_position,command,accepted_at,title,source_media_type)
 SELECT $1,id,$9,$10,$8,1,$7,convert_from($11::bytea,'UTF8')::jsonb,now(),nullif($12,''),coalesce(nullif($13,''),'text/plain') FROM record
 RETURNING record_id
), receipt AS (
 INSERT INTO ingestion_receipts(organization,id,request_key,canonical_request,command,corpus_id,record_id,acceptance_order,slot,digest)
 SELECT $1,$3,$14,$11::bytea,convert_from($11::bytea,'UTF8')::jsonb,$4,record_id,1,$9,$10 FROM revision RETURNING id
), outbox AS (
 INSERT INTO ingestion_outbox(organization,receipt_id,legacy_workflow,lease_until)
 SELECT $1,id,false,'infinity'::timestamptz FROM receipt RETURNING receipt_id
), positions AS (
 UPDATE organization_journals SET last_sequence=last_sequence+2
 WHERE organization=$1 AND EXISTS(SELECT 1 FROM outbox) RETURNING last_sequence
), events AS (
 INSERT INTO change_events(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id)
 SELECT $1,p.last_sequence+e.delta,e.id,$4,e.kind,e.resource,e.resource_id
 FROM positions p CROSS JOIN (VALUES
   ($15::text,'receipt.pending','receipt',$3::text,-1),
   ($16::text,'record.accepted','record',$2::text,0)
 ) e(id,kind,resource,resource_id,delta)
) SELECT EXISTS(SELECT 1 FROM record)`, org, recordID, receiptID, c.Source.CorpusID, c.Source.Namespace, c.Source.RecordKey, c.Position, versionID, slot, digest, canonical, content.Title(c), c.SourceMediaType, c.Key, eventID(pending), eventID(accepted)).Scan(&created)
	return created, err
}
