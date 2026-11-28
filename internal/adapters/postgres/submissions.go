package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"slices"
	"strconv"
	"time"

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

// resolveDigestSlot picks the Version slot of a submission without a source
// revision (ADR 0003). Content already reserved converges on its latest
// reservation, unless that reservation is no longer the Record's desired
// Version and this submission would become desired: a revert to earlier bytes
// then reserves a new slot, qualified by its acceptance order, and so mints a
// new Version with identical content.
func resolveDigestSlot(base, latest string, desired bool, slotErr error, order int64, incoming, desiredPosition string) (string, error) {
	if errors.Is(slotErr, pgx.ErrNoRows) {
		return base, nil
	}
	if slotErr != nil {
		return "", slotErr
	}
	if desired || !content.NewerPosition(incoming, desiredPosition) {
		return latest, nil
	}
	return base + "@" + strconv.FormatInt(order, 10), nil
}

func (s SubmissionStore) Accept(ctx context.Context, scope corpus.Scope, c content.Command) (content.Receipt, error) {
	var result0 content.Receipt
	err := retryJournalWriteWithLimit(ctx, "Accept", acceptJournalRetryMaxAttempts, func(ctx context.Context) error {
		var err error
		result0, err = s.acceptAttempt(ctx, scope, c)
		return err
	})
	return result0, err
}

func rollbackTx(ctx context.Context, tx pgx.Tx) {
	rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = tx.Rollback(rbCtx)
}

func (s SubmissionStore) acceptAttempt(ctx context.Context, scope corpus.Scope, c content.Command) (content.Receipt, error) {
	canonical, err := json.Marshal(c)
	if err != nil {
		return content.Receipt{}, err
	}
	requestDigest := sha256.Sum256(canonical)
	recordID := content.StableID("record", scope.Organization, c.Source.CorpusID, c.Source.Namespace, c.Source.RecordKey)
	receiptID := content.StableID("receipt", scope.Organization, "ingestion", c.Key)
	digest := content.Digest(c)
	slot := "digest:" + digest
	if c.Revision != "" {
		slot = "revision:" + c.Revision
	}
	db := database(ctx, s.Pool)
	if db == s.Pool {
		// Pin a single pool connection across lookup, optional transaction, and
		// replay hydration so every accept path uses at most one connection.
		conn, err := s.Pool.Acquire(ctx)
		if err != nil {
			return content.Receipt{}, err
		}
		defer conn.Release()
		query, args := buildAcceptLookupQuery(ctx, scope.Organization, "ingestion", c.Key, recordID, c.Source.CorpusID)
		row, err := scanReplayReceiptRow(conn.QueryRow(ctx, query, args...))
		if err != nil {
			return content.Receipt{}, err
		}
		if row.replayID != nil {
			if !requestMatchesDigest(row.previous, row.previousDigest, canonical, requestDigest) {
				return content.Receipt{}, content.ErrConflict
			}
			if !row.visible {
				return content.Receipt{}, corpus.ErrNotFound
			}
			return (ReceiptStore{Pool: s.Pool}).hydrateReceipt(ctx, conn, scope.Organization, row)
		}
		if !row.corpusExists {
			return content.Receipt{}, corpus.ErrNotFound
		}
		if !row.recordExists {
			tx, err := conn.Begin(ctx)
			if err != nil {
				return content.Receipt{}, err
			}
			created, err := acceptFirstRevision(ctx, tx, scope.Organization, c, canonical, requestDigest, recordID, receiptID, slot, digest, s.RetainImportAuditDetail)
			if err != nil {
				rollbackTx(ctx, tx)
				return content.Receipt{}, err
			}
			if created {
				if err := tx.Commit(ctx); err != nil {
					rollbackTx(ctx, tx)
					return content.Receipt{}, err
				}
				return pendingReceipt(receiptID, recordID, c.Source, true), nil
			}
			// A concurrent writer raced and created the Record or Receipt
			// between lookup and lock; discard any speculative observation and
			// converge cleanly on the general path using this same connection.
			rollbackTx(ctx, tx)
		}
		return s.acceptGeneral(ctx, conn, scope, c, canonical, requestDigest, recordID, receiptID, slot, digest, row.recordExists)
	}
	return s.acceptGeneral(ctx, db, scope, c, canonical, requestDigest, recordID, receiptID, slot, digest, false)
}

func (s SubmissionStore) acceptGeneral(ctx context.Context, db transactionalDatabase, scope corpus.Scope, c content.Command, canonical []byte, requestDigest [32]byte, recordID, receiptID, slot, digest string, recordKnown bool) (content.Receipt, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return content.Receipt{}, err
	}
	defer rollbackTx(ctx, tx)

	// Acquire routing, claim, and journal fences together with the guarded
	// replay/corpus check, conditional Record upsert/order advance, and
	// optional digest-slot lookup in a single round trip. Separate statements
	// preserve fresh READ COMMITTED snapshots after lock acquisition.
	guard := &pgx.Batch{}
	guard.Queue(`WITH mode AS MATERIALIZED (SELECT pg_advisory_xact_lock_shared($2))
 SELECT pg_advisory_xact_lock_shared($1) FROM mode`, projectionRoutingLock, journalModeLock)
	queueJournalClaim(guard, scope.Organization, "ingestion", c.Key)
	queueJournalClaim(guard, scope.Organization, "record", recordID)
	guard.Queue(`INSERT INTO organization_journals(organization) SELECT $1 WHERE NOT `+parallelJournalSQL+` ON CONFLICT DO NOTHING`, scope.Organization)
	guard.Queue(`SELECT last_sequence FROM organization_journals WHERE organization=$1 AND NOT `+parallelJournalSQL+` FOR UPDATE`, scope.Organization)
	lookupIndex := guard.Len()
	query, args := buildAcceptLookupQuery(ctx, scope.Organization, "ingestion", c.Key, recordID, c.Source.CorpusID)
	guard.Queue(query, args...)
	if !recordKnown {
		guard.Queue(`WITH record AS (
 INSERT INTO records(organization,id,corpus_id,namespace,record_key,record_acl)
 SELECT $1,$2,$3,$4,$5,$6::text[]
 WHERE EXISTS(SELECT 1 FROM corpora WHERE organization=$1 AND id=$3)
 AND NOT EXISTS(SELECT 1 FROM ingestion_receipts WHERE organization=$1 AND route_family='ingestion' AND request_key=$7)
 ON CONFLICT DO NOTHING RETURNING organization,corpus_id,namespace,record_acl
)`+recordVisibilityAdmissionSQL+` SELECT 1`, scope.Organization, recordID, c.Source.CorpusID, c.Source.Namespace, c.Source.RecordKey, c.ACL, c.Key)
	}
	guard.Queue(`UPDATE records SET acceptance_order=acceptance_order+1
 WHERE organization=$1 AND id=$2
 AND NOT EXISTS(SELECT 1 FROM ingestion_receipts WHERE organization=$1 AND route_family='ingestion' AND request_key=$3)
 RETURNING acceptance_order,desired_position,withdrawn,record_acl`, scope.Organization, recordID, c.Key)
	baseSlot := "digest:" + digest
	if c.Revision == "" {
		guard.Queue(`SELECT a.slot,a.version_id IS NOT DISTINCT FROM r.desired_version_id FROM accepted_revisions a JOIN records r ON (r.organization,r.id)=(a.organization,a.record_id)
WHERE a.organization=$1 AND a.record_id=$2 AND a.digest=$3 AND (a.slot=$4 OR a.slot LIKE $4||'@%') ORDER BY a.acceptance_order DESC LIMIT 1`, scope.Organization, recordID, digest, baseSlot)
	}
	guardResults := tx.SendBatch(ctx, guard)
	for range lookupIndex {
		if _, err = guardResults.Exec(); err != nil {
			_ = guardResults.Close()
			return content.Receipt{}, err
		}
	}
	row, err := scanReplayReceiptRow(guardResults.QueryRow())
	if err != nil {
		_ = guardResults.Close()
		return content.Receipt{}, err
	}
	if !recordKnown {
		if _, err = guardResults.Exec(); err != nil {
			_ = guardResults.Close()
			return content.Receipt{}, err
		}
	}
	var order int64
	var position string
	var withdrawn bool
	var acl []string
	updateErr := guardResults.QueryRow().Scan(&order, &position, &withdrawn, &acl)
	var latestSlot string
	var latestDesired bool
	var slotErr error
	if c.Revision == "" {
		slotErr = guardResults.QueryRow().Scan(&latestSlot, &latestDesired)
	}
	closeErr := guardResults.Close()
	if row.replayID != nil {
		if closeErr != nil {
			return content.Receipt{}, closeErr
		}
		if !requestMatchesDigest(row.previous, row.previousDigest, canonical, requestDigest) {
			return content.Receipt{}, content.ErrConflict
		}
		if !row.visible {
			return content.Receipt{}, corpus.ErrNotFound
		}
		receipt, err := (ReceiptStore{Pool: s.Pool}).hydrateReceipt(ctx, tx, scope.Organization, row)
		if err != nil {
			return content.Receipt{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return content.Receipt{}, err
		}
		return receipt, nil
	}
	if !row.corpusExists {
		return content.Receipt{}, corpus.ErrNotFound
	}
	if updateErr != nil {
		return content.Receipt{}, updateErr
	}
	if closeErr != nil {
		return content.Receipt{}, closeErr
	}
	if withdrawn || len(c.ACL) > 0 && !slices.Equal(c.ACL, acl) {
		return content.Receipt{}, content.ErrConflict
	}
	if c.Revision == "" {
		slot, err = resolveDigestSlot(baseSlot, latestSlot, latestDesired, slotErr, order, c.Position, position)
		if err != nil {
			return content.Receipt{}, err
		}
	}
	requestCopy, receiptCommand, execution := importRequestStorage(canonical, s.RetainImportAuditDetail)
	versionID := content.StableID("version", scope.Organization, recordID, slot)
	// Reserve lineage, currentness, receipt, outbox and journal facts together.
	// Separate statements retain fresh READ COMMITTED snapshots; the caller
	// still owns the journal fence and this entry's all-or-nothing transaction.
	writes := &pgx.Batch{}
	writes.Queue(`INSERT INTO accepted_revisions(organization,record_id,slot,digest,version_id,acceptance_order,source_position,predecessor_id,command,accepted_at,title,source_media_type,connector_instance_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,
 (SELECT version_id FROM accepted_revisions WHERE organization=$1 AND record_id=$2 AND ($7='' OR source_position='' OR length(source_position)<length($7) OR (length(source_position)=length($7) AND source_position COLLATE "C" < $7 COLLATE "C")) ORDER BY acceptance_order DESC LIMIT 1),
 $8,now(),nullif($9,''),COALESCE(NULLIF($10,''),'text/plain'),COALESCE(NULLIF($11,''),'unknown')) ON CONFLICT DO NOTHING`, scope.Organization, recordID, slot, digest, versionID, order, c.Position, execution, content.Title(c), c.SourceMediaType, c.ConnectorInstanceID)
	if content.NewerPosition(c.Position, position) {
		writes.Queue(`UPDATE records SET desired_order=$3,desired_position=$4,desired_version_id=$5 WHERE organization=$1 AND id=$2
 AND EXISTS(SELECT 1 FROM accepted_revisions WHERE organization=$1 AND record_id=$2 AND slot=$6 AND acceptance_order=$3)`, scope.Organization, recordID, order, c.Position, versionID, slot)
	}
	writes.Queue(`INSERT INTO ingestion_receipts(organization,id,request_key,canonical_request,command,corpus_id,record_id,acceptance_order,slot,digest,work_queue,request_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, scope.Organization, receiptID, c.Key, requestCopy, receiptCommand, c.Source.CorpusID, recordID, order, slot, digest, workqueue.Class(ctx), requestDigest[:])
	outbox := "ingestion_outbox"
	if workqueue.Class(ctx) == workqueue.Bulk {
		outbox = "bulk_ingestion_outbox"
	}
	writes.Queue("INSERT INTO "+outbox+"(organization,receipt_id,trace_context,work_queue) VALUES($1,$2,$3,$4)", scope.Organization, receiptID, telemetry.Encode(ctx), workqueue.Class(ctx))
	queueEvent(ctx, writes, eventInput{Organization: scope.Organization, CorpusID: c.Source.CorpusID, Kind: "receipt.pending", Resource: "receipt", ResourceID: receiptID})
	// Catalog invalidation belongs to the same acceptance transaction, including a Record first seen before materialization.
	queueEvent(ctx, writes, eventInput{Organization: scope.Organization, CorpusID: c.Source.CorpusID, Kind: "record.accepted", Resource: "record", ResourceID: recordID, MutationID: receiptID})
	results := tx.SendBatch(ctx, writes)
	reservation, err := results.Exec()
	if err != nil {
		_ = results.Close()
		return content.Receipt{}, err
	}
	if err = results.Close(); err != nil {
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
	err := retryJournalWriteWithLimit(ctx, "Withdraw", acceptJournalRetryMaxAttempts, func(ctx context.Context) error {
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
	defer rollbackTx(ctx, tx)
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return content.Receipt{}, err
	}
	if err = lockJournalRequest(ctx, tx, scope.Organization, "withdrawal", w.Key); err != nil {
		return content.Receipt{}, err
	}
	recordID := content.StableID("record", scope.Organization, w.Source.CorpusID, w.Source.Namespace, w.Source.RecordKey)
	if err = lockJournalRecord(ctx, tx, scope.Organization, recordID); err != nil {
		return content.Receipt{}, err
	}
	requestDigest := sha256.Sum256(canonical)
	if err = lockJournal(ctx, tx, scope.Organization); err != nil {
		return content.Receipt{}, err
	}
	query, args := buildAcceptLookupQuery(ctx, scope.Organization, "withdrawal", w.Key, recordID, w.Source.CorpusID)
	row, err := scanReplayReceiptRow(tx.QueryRow(ctx, query, args...))
	if err != nil {
		return content.Receipt{}, err
	}
	if row.replayID != nil {
		if !requestMatchesDigest(row.previous, row.previousDigest, canonical, requestDigest) {
			return content.Receipt{}, content.ErrConflict
		}
		if !row.visible {
			return content.Receipt{}, corpus.ErrNotFound
		}
		receipt, err := (ReceiptStore{Pool: s.Pool}).hydrateReceipt(ctx, tx, scope.Organization, row)
		if err != nil {
			return content.Receipt{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return content.Receipt{}, err
		}
		return receipt, nil
	}
	if !row.corpusExists {
		return content.Receipt{}, corpus.ErrNotFound
	}
	requestCopy, receiptCommand, _ := importRequestStorage(canonical, s.RetainImportAuditDetail)
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
	receiptID := content.StableID("receipt", scope.Organization, "withdrawal", w.Key)
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
