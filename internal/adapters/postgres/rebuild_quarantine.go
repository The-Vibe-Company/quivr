package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/jackc/pgx/v5"
)

const rebuildErrorSample = 20

func (s RebuildStore) QuarantineRebuild(ctx context.Context, org, id, versionID string, reason content.Diagnostic) error {
	return retryJournalWrite(ctx, "QuarantineRebuild", func(ctx context.Context) error { return s.quarantineRebuildAttempt(ctx, org, id, versionID, reason) })
}

func (s RebuildStore) quarantineRebuildAttempt(ctx context.Context, org, id, versionID string, reason content.Diagnostic) error {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return err
	}
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	op, err := lockOperation(ctx, tx, org, id)
	if err != nil {
		return err
	}
	if op.State != operations.StateRunning {
		return operations.ErrNotRunning
	}
	var recordID, corpusID string
	var gap bool
	err = tx.QueryRow(ctx, `SELECT r.id,r.corpus_id,`+rebuildGapSQL+` FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$4 AND r.current_version_id=v.id FOR UPDATE OF v,r`, org, op.CorpusID, op.TargetGenerationID, versionID).Scan(&recordID, &corpusID, &gap)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if !gap {
		return tx.Commit(ctx)
	}
	raw, err := json.Marshal(reason)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE record_versions SET quarantined=true,baseline_ready=false,enriched_at=NULL,enrichment_state='queued',enrichment_error='',processing='blocked',error_code=$3,quarantine=$4,quarantine_stage='ingestion',quarantined_at=clock_timestamp() WHERE organization=$1 AND id=$2`, org, versionID, reason.Code, raw); err != nil {
		return err
	}
	if err = quarantinedEvent(ctx, tx, org, corpusID, recordID, versionID, reason.Code); err != nil {
		return err
	}
	if len(op.Errors) < rebuildErrorSample {
		op.Errors = append(op.Errors, operations.Error{Code: reason.Code, Message: fmt.Sprintf("Version %s: %s", versionID, reason.Message)})
	}
	errorsJSON, err := json.Marshal(op.Errors)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE operations SET counters=counters || jsonb_build_object('versions_quarantined',coalesce((counters->>'versions_quarantined')::bigint,0)+1),errors=$3,updated_at=now() WHERE organization=$1 AND id=$2`, org, id, errorsJSON); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
