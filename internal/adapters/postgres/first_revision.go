package postgres

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// acceptFirstRevision writes a new Record's reservation, receipt, dispatch
// intent and both journal facts atomically. Canonical writes precede the
// journal fence; unique keys arbitrate concurrent new Records. Existing Records
// take the general revision path after this batch closes, including reverts and
// source-position arbitration under their existing READ COMMITTED fence.
// Every ID is still computed by the domain's canonical hash function.
func acceptFirstRevision(ctx context.Context, pool *pgxpool.Pool, org string, c content.Command, canonical []byte, recordID, receiptID, slot, digest string) (bool, error) {
	var result0 bool
	err := retryJournalWrite(ctx, "acceptFirstRevision", func(ctx context.Context) error {
		var err error
		result0, err = acceptFirstRevisionAttempt(ctx, pool, org, c, canonical, recordID, receiptID, slot, digest)
		return err
	})
	return result0, err
}

func acceptFirstRevisionAttempt(ctx context.Context, pool *pgxpool.Pool, org string, c content.Command, canonical []byte, recordID, receiptID, slot, digest string) (bool, error) {
	versionID := content.StableID("version", org, recordID, slot)
	pending := eventInput{Organization: org, Kind: "receipt.pending", Resource: "receipt", ResourceID: receiptID}
	accepted := eventInput{Organization: org, Kind: "record.accepted", Resource: "record", ResourceID: recordID, MutationID: receiptID}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	batch := &pgx.Batch{}
	batch.Queue("SELECT pg_advisory_xact_lock_shared($1)", projectionRoutingLock)
	batch.Queue(`WITH record AS (
 INSERT INTO records(organization,id,corpus_id,namespace,record_key,acceptance_order,desired_order,desired_position,desired_version_id)
 SELECT $1,$2,$4,$5,$6,1,1,$7,$8
 WHERE EXISTS(SELECT 1 FROM corpora WHERE organization=$1 AND id=$4)
 AND NOT EXISTS(SELECT 1 FROM ingestion_receipts WHERE organization=$1 AND route_family='ingestion' AND request_key=$14)
 ON CONFLICT DO NOTHING RETURNING id
), revision AS (
 INSERT INTO accepted_revisions(organization,record_id,slot,digest,version_id,acceptance_order,source_position,command,accepted_at,title,source_media_type)
 SELECT $1,id,$9,$10,$8,1,$7,convert_from($11::bytea,'UTF8')::jsonb,now(),nullif($12,''),coalesce(nullif($13,''),'text/plain') FROM record
 RETURNING record_id
), receipt AS (
 INSERT INTO ingestion_receipts(organization,id,request_key,canonical_request,command,corpus_id,record_id,acceptance_order,slot,digest,work_queue)
 SELECT $1,$3,$14,$11::bytea,convert_from($11::bytea,'UTF8')::jsonb,$4,record_id,1,$9,$10,$16 FROM revision ON CONFLICT DO NOTHING RETURNING id
), outbox AS (
 INSERT INTO ingestion_outbox(organization,receipt_id,legacy_workflow,lease_until,trace_context,work_queue)
 SELECT $1,id,false,'infinity'::timestamptz,$15,$16 FROM receipt WHERE $16<>'bulk' RETURNING receipt_id
), bulk_outbox AS (
 INSERT INTO bulk_ingestion_outbox(organization,receipt_id,legacy_workflow,lease_until,trace_context,work_queue)
 SELECT $1,id,false,'infinity'::timestamptz,$15,$16 FROM receipt WHERE $16='bulk' RETURNING receipt_id
) SELECT EXISTS(SELECT 1 FROM receipt)`, org, recordID, receiptID, c.Source.CorpusID, c.Source.Namespace, c.Source.RecordKey, c.Position, versionID, slot, digest, canonical, content.Title(c), c.SourceMediaType, c.Key, telemetry.Encode(ctx), workqueue.Class(ctx))
	results := tx.SendBatch(ctx, batch)
	defer results.Close()
	for range batch.Len() - 1 {
		if _, err := results.Exec(); err != nil {
			return false, err
		}
	}
	var created bool
	err = results.QueryRow().Scan(&created)
	closeErr := results.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if !created {
		return false, nil
	}
	// Acquire and append last. The separate statements keep the public cursor
	// gap-free and ordered by commit, including concurrent acceptance replays.
	events := journalBatch(org)
	pending.CorpusID, accepted.CorpusID = c.Source.CorpusID, c.Source.CorpusID
	queueEvent(ctx, events, pending)
	queueEvent(ctx, events, accepted)
	if err = tx.SendBatch(ctx, events).Close(); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
