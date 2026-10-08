package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"strconv"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubmissionStore owns durable acceptance and absorbing withdrawal commands.
type SubmissionStore struct {
	Pool                    *pgxpool.Pool
	RetainImportAuditDetail bool
}

var _ content.SubmissionStore = SubmissionStore{}

// digestSlot picks the Version slot of a submission without a source revision
// (ADR 0003). Content already reserved converges on its latest reservation,
// unless that reservation is no longer the Record's desired Version and this
// submission would become desired: a revert to earlier bytes then reserves a
// new slot, qualified by its acceptance order, and so mints a new Version with
// identical content. Callers hold the Organization journal lock.
func digestSlot(ctx context.Context, tx pgx.Tx, org, recordID, digest string, order int64, incoming, desiredPosition string) (string, error) {
	base := "digest:" + digest
	var latest string
	var desired bool
	err := tx.QueryRow(ctx, `SELECT a.slot,a.version_id IS NOT DISTINCT FROM r.desired_version_id FROM accepted_revisions a JOIN records r ON (r.organization,r.id)=(a.organization,a.record_id)
WHERE a.organization=$1 AND a.record_id=$2 AND a.digest=$3 AND (a.slot=$4 OR a.slot LIKE $4||'@%') ORDER BY a.acceptance_order DESC LIMIT 1`, org, recordID, digest, base).Scan(&latest, &desired)
	if errors.Is(err, pgx.ErrNoRows) {
		return base, nil
	}
	if err != nil {
		return "", err
	}
	if desired || !content.NewerPosition(incoming, desiredPosition) {
		return latest, nil
	}
	return base + "@" + strconv.FormatInt(order, 10), nil
}

func (s SubmissionStore) Accept(ctx context.Context, scope corpus.Scope, c content.Command) (content.Receipt, error) {
	var result0 content.Receipt
	err := retryJournalWrite(ctx, "Accept", func(ctx context.Context) error {
		var err error
		result0, err = s.acceptAttempt(ctx, scope, c)
		return err
	})
	return result0, err
}

func (s SubmissionStore) acceptAttempt(ctx context.Context, scope corpus.Scope, c content.Command) (content.Receipt, error) {
	canonical, err := json.Marshal(c)
	if err != nil {
		return content.Receipt{}, err
	}
	recordID := content.StableID("record", scope.Organization, c.Source.CorpusID, c.Source.Namespace, c.Source.RecordKey)
	receiptID := content.StableID("receipt", scope.Organization, "ingestion", c.Key)
	digest := content.Digest(c)
	slot := "digest:" + digest
	if c.Revision != "" {
		slot = "revision:" + c.Revision
	}
	db := database(ctx, s.Pool)
	// The implicit batch owns its transaction. Audited commands must instead
	// stay inside their request transaction, including any refusal rollback.
	if db == s.Pool {
		created, err := acceptFirstRevision(ctx, s.Pool, scope.Organization, c, canonical, recordID, receiptID, slot, digest, s.RetainImportAuditDetail)
		if err != nil {
			return content.Receipt{}, err
		}
		if created {
			return pendingReceipt(receiptID, recordID, c.Source, true), nil
		}
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return content.Receipt{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return content.Receipt{}, err
	}
	requestCopy, receiptCommand, execution := importRequestStorage(canonical, s.RetainImportAuditDetail)
	requestDigest := sha256.Sum256(canonical)
	var previous, previousDigest []byte
	var replayID *string
	var exists bool
	err = readJournal(ctx, tx, scope.Organization, `SELECT previous.id,previous.canonical_request,previous.request_digest,
 EXISTS(SELECT 1 FROM corpora WHERE organization=$1 AND id=$3)
 FROM (VALUES(1)) seed(n) LEFT JOIN ingestion_receipts previous
 ON previous.organization=$1 AND previous.route_family='ingestion' AND previous.request_key=$2`,
		[]any{scope.Organization, c.Key, c.Source.CorpusID}, &replayID, &previous, &previousDigest, &exists)
	if err != nil {
		return content.Receipt{}, err
	}
	if replayID != nil {
		if !requestMatches(previous, previousDigest, canonical) {
			return content.Receipt{}, content.ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return content.Receipt{}, err
		}
		return (ReceiptStore{Pool: s.Pool}).Receipt(ctx, scope.Organization, *replayID)
	}
	if !exists {
		return content.Receipt{}, corpus.ErrNotFound
	}
	_, err = tx.Exec(ctx, `INSERT INTO records(organization,id,corpus_id,namespace,record_key) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, scope.Organization, recordID, c.Source.CorpusID, c.Source.Namespace, c.Source.RecordKey)
	if err != nil {
		return content.Receipt{}, err
	}
	var order int64
	var position string
	var withdrawn bool
	err = tx.QueryRow(ctx, `UPDATE records SET acceptance_order=acceptance_order+1 WHERE organization=$1 AND id=$2 RETURNING acceptance_order,desired_position,withdrawn`, scope.Organization, recordID).Scan(&order, &position, &withdrawn)
	if err != nil {
		return content.Receipt{}, err
	}
	if withdrawn {
		return content.Receipt{}, content.ErrConflict
	}
	if c.Revision == "" {
		slot, err = digestSlot(ctx, tx, scope.Organization, recordID, digest, order, c.Position, position)
	}
	if err != nil {
		return content.Receipt{}, err
	}
	versionID := content.StableID("version", scope.Organization, recordID, slot)
	// Reserve the original revision order and lineage once, before any worker can publish it.
	var predecessor string
	err = tx.QueryRow(ctx, `SELECT version_id FROM accepted_revisions WHERE organization=$1 AND record_id=$2 AND ($3='' OR source_position='' OR length(source_position)<length($3) OR (length(source_position)=length($3) AND source_position COLLATE "C" < $3 COLLATE "C")) ORDER BY acceptance_order DESC LIMIT 1`, scope.Organization, recordID, c.Position).Scan(&predecessor)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return content.Receipt{}, err
	}
	reservation, err := tx.Exec(ctx, `INSERT INTO accepted_revisions(organization,record_id,slot,digest,version_id,acceptance_order,source_position,predecessor_id,command,accepted_at,title,source_media_type) VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''),$9,now(),nullif($10,''),COALESCE(NULLIF($11,''),'text/plain')) ON CONFLICT DO NOTHING`, scope.Organization, recordID, slot, digest, versionID, order, c.Position, predecessor, execution, content.Title(c), c.SourceMediaType)
	if err != nil {
		return content.Receipt{}, err
	}
	if reservation.RowsAffected() == 1 && content.NewerPosition(c.Position, position) {
		_, err = tx.Exec(ctx, "UPDATE records SET desired_order=$3,desired_position=$4,desired_version_id=$5 WHERE organization=$1 AND id=$2", scope.Organization, recordID, order, c.Position, versionID)
		if err != nil {
			return content.Receipt{}, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO ingestion_receipts(organization,id,request_key,canonical_request,command,corpus_id,record_id,acceptance_order,slot,digest,work_queue,request_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, scope.Organization, receiptID, c.Key, requestCopy, receiptCommand, c.Source.CorpusID, recordID, order, slot, digest, workqueue.Class(ctx), requestDigest[:])
	if err != nil {
		return content.Receipt{}, err
	}
	outbox := "ingestion_outbox"
	if workqueue.Class(ctx) == workqueue.Bulk {
		outbox = "bulk_ingestion_outbox"
	}
	if _, err = tx.Exec(ctx, "INSERT INTO "+outbox+"(organization,receipt_id,legacy_workflow,lease_until,trace_context,work_queue) VALUES($1,$2,false,'infinity',$3,$4)", scope.Organization, receiptID, telemetry.Encode(ctx), workqueue.Class(ctx)); err != nil {
		return content.Receipt{}, err
	}
	if err = appendEvent(ctx, tx, eventInput{Organization: scope.Organization, CorpusID: c.Source.CorpusID, Kind: "receipt.pending", Resource: "receipt", ResourceID: receiptID}); err != nil {
		return content.Receipt{}, err
	}
	// Catalog invalidation belongs to the same acceptance transaction, including a Record first seen before materialization.
	if err = appendEvent(ctx, tx, eventInput{Organization: scope.Organization, CorpusID: c.Source.CorpusID, Kind: "record.accepted", Resource: "record", ResourceID: recordID, MutationID: receiptID}); err != nil {
		return content.Receipt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return content.Receipt{}, err
	}
	return pendingReceipt(receiptID, recordID, c.Source, reservation.RowsAffected() == 1), nil
}

func pendingReceipt(receiptID, recordID string, source content.Source, newRevision bool) content.Receipt {
	return content.Receipt{ID: receiptID, State: "pending", RecordID: recordID, Source: source,
		Processing:  content.Processing{State: "queued", Phase: "materialization"},
		Diagnostics: []content.Diagnostic{}, NewRevision: newRevision}
}

// Withdraw commits the absorbing fence atomically: an existing or first-seen
// Record identity is marked withdrawn, its mutation fence advances, a single
// Tombstone is inserted, the Record event is appended and a resolved
// withdrawal_applied Receipt is stored. No projection work is dispatched; stale
// projection objects are hidden by canonical hydration.
func (s SubmissionStore) Withdraw(ctx context.Context, scope corpus.Scope, w content.Withdrawal) (content.Receipt, error) {
	var result0 content.Receipt
	err := retryJournalWrite(ctx, "Withdraw", func(ctx context.Context) error {
		var err error
		result0, err = s.withdrawAttempt(ctx, scope, w)
		return err
	})
	return result0, err
}

func (s SubmissionStore) withdrawAttempt(ctx context.Context, scope corpus.Scope, w content.Withdrawal) (content.Receipt, error) {
	canonical, err := json.Marshal(w)
	if err != nil {
		return content.Receipt{}, err
	}
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return content.Receipt{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return content.Receipt{}, err
	}
	requestCopy, receiptCommand, _ := importRequestStorage(canonical, s.RetainImportAuditDetail)
	requestDigest := sha256.Sum256(canonical)
	if err = lockJournal(ctx, tx, scope.Organization); err != nil {
		return content.Receipt{}, err
	}
	var previous, previousDigest []byte
	var receiptID string
	err = tx.QueryRow(ctx, "SELECT id,canonical_request,request_digest FROM ingestion_receipts WHERE organization=$1 AND route_family='withdrawal' AND request_key=$2", scope.Organization, w.Key).Scan(&receiptID, &previous, &previousDigest)
	if err == nil {
		if !requestMatches(previous, previousDigest, canonical) {
			return content.Receipt{}, content.ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return content.Receipt{}, err
		}
		return (ReceiptStore{Pool: s.Pool}).Receipt(ctx, scope.Organization, receiptID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return content.Receipt{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM corpora WHERE organization=$1 AND id=$2)", scope.Organization, w.Source.CorpusID).Scan(&exists); err != nil {
		return content.Receipt{}, err
	}
	if !exists {
		return content.Receipt{}, corpus.ErrNotFound
	}
	recordID := content.StableID("record", scope.Organization, w.Source.CorpusID, w.Source.Namespace, w.Source.RecordKey)
	if _, err = tx.Exec(ctx, `INSERT INTO records(organization,id,corpus_id,namespace,record_key) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, scope.Organization, recordID, w.Source.CorpusID, w.Source.Namespace, w.Source.RecordKey); err != nil {
		return content.Receipt{}, err
	}
	var priorWithdrawn bool
	if err = tx.QueryRow(ctx, "SELECT withdrawn FROM records WHERE organization=$1 AND id=$2 FOR UPDATE", scope.Organization, recordID).Scan(&priorWithdrawn); err != nil {
		return content.Receipt{}, err
	}
	var order int64
	if err = tx.QueryRow(ctx, "UPDATE records SET acceptance_order=acceptance_order+1,withdrawn=true,withdrawn_at=CASE WHEN withdrawn THEN withdrawn_at ELSE clock_timestamp() END WHERE organization=$1 AND id=$2 RETURNING acceptance_order", scope.Organization, recordID).Scan(&order); err != nil {
		return content.Receipt{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO tombstones(organization,record_id) VALUES($1,$2) ON CONFLICT DO NOTHING", scope.Organization, recordID); err != nil {
		return content.Receipt{}, err
	}
	digest := content.Hash(canonical)
	receiptID = content.StableID("receipt", scope.Organization, "withdrawal", w.Key)
	_, err = tx.Exec(ctx, `INSERT INTO ingestion_receipts(organization,id,route_family,request_key,canonical_request,command,corpus_id,record_id,acceptance_order,slot,digest,state,outcome,processing,error_code,request_digest) VALUES($1,$2,'withdrawal',$3,$4,$5,$6,$7,$8,$9,$10,'resolved','withdrawal_applied','idle','',$11)`, scope.Organization, receiptID, w.Key, requestCopy, receiptCommand, w.Source.CorpusID, recordID, order, "withdrawal:"+w.Key, digest, requestDigest[:])
	if err != nil {
		return content.Receipt{}, err
	}
	// Emit the Record event exactly once per identity, even across repeat
	// withdrawals with different request keys.
	if !priorWithdrawn {
		if err = appendEvent(ctx, tx, eventInput{Organization: scope.Organization, CorpusID: w.Source.CorpusID, Kind: "record.withdrawn", Resource: "record", ResourceID: recordID, MutationID: content.StableID("withdrawal", recordID)}); err != nil {
			return content.Receipt{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return content.Receipt{}, err
	}
	return content.Receipt{ID: receiptID, State: "resolved", Outcome: "withdrawal_applied", RecordID: recordID, Source: w.Source, Processing: content.Processing{State: "idle"}, Diagnostics: []content.Diagnostic{}}, nil
}
