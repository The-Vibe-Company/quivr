package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/quarantine"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// stuckSQL selects the quarantined Versions of Organization $1 that can
// still become current (their Record desires them and is not withdrawn),
// kept by the filters $2 (Corpus, ” for any), $3 (Corpora, NULL for any),
// $4 (the plugin the reason names), $5 (the reason code) and the window
// [$6, $7) on when each was quarantined, or else accepted. It exposes
// aliases v, r and rc.
const stuckSQL = `record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 JOIN ingestion_receipts rc ON rc.organization=v.organization AND rc.record_id=v.record_id AND rc.acceptance_order=v.acceptance_order
 WHERE v.organization=$1 AND v.quarantined AND r.desired_version_id=v.id AND NOT ` + recordGoneSQL + `
 AND ($2::text='' OR r.corpus_id=$2) AND ($3::text[] IS NULL OR r.corpus_id=ANY($3::text[]))
 AND ($4::text='' OR v.quarantine->>'plugin'=$4) AND ($5::text='' OR v.error_code=$5)
 AND ($6::timestamptz IS NULL OR coalesce(v.quarantined_at,rc.accepted_at)>=$6) AND ($7::timestamptz IS NULL OR coalesce(v.quarantined_at,rc.accepted_at)<$7)`

// legacyQuarantineMessage explains a quarantine that predates structured
// reasons, as the Version read does.
const legacyQuarantineMessage = "Processing could not complete safely; the Version is withheld from search."

// reasonSQL is the recorded reason of the Version aliased v, or for a
// quarantine that predates structured reasons its code.
const reasonSQL = `coalesce(v.quarantine,jsonb_build_object('code',v.error_code,'message','` + legacyQuarantineMessage + `','retryable',false))`

func stuckArgs(org string, corpora []string, f quarantine.Filter) []any {
	var within any
	if corpora != nil {
		within = corpora
	}
	return []any{org, f.CorpusID, within, f.Plugin, f.Code, f.After, f.Before}
}

// Quarantined lists one page of stuck Versions in Version id order.
func (s QuarantineStore) Quarantined(ctx context.Context, org string, corpora []string, f quarantine.Filter, after string, limit int) ([]quarantine.Entry, error) {
	args := append(stuckArgs(org, corpora, f), after, limit)
	rows, err := database(ctx, s.Pool).Query(ctx, `SELECT v.id,v.record_id,r.corpus_id,rc.id,coalesce(v.quarantine_stage,'ingestion'),`+reasonSQL+`,coalesce(v.quarantined_at,rc.accepted_at)
FROM `+stuckSQL+` AND v.id>$8 ORDER BY v.id LIMIT $9`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []quarantine.Entry{}
	for rows.Next() {
		var e quarantine.Entry
		var reason []byte
		if err = rows.Scan(&e.VersionID, &e.RecordID, &e.CorpusID, &e.ReceiptID, &e.Stage, &reason, &e.QuarantinedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(reason, &e.Reason); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ReprocessSize counts the Versions a reprocess of f would take now.
// It is corpus.ErrNotFound for an unknown Corpus.
func (s QuarantineStore) ReprocessSize(ctx context.Context, org string, f quarantine.Filter) (operations.ReprocessEstimate, error) {
	e := operations.ReprocessEstimate{Stages: map[string]int64{content.QuarantineNormalization: 0, content.QuarantineIngestion: 0}, Codes: map[string]int64{}}
	var exists bool
	if err := database(ctx, s.Pool).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM corpora WHERE organization=$1 AND id=$2)`, org, f.CorpusID).Scan(&exists); err != nil {
		return e, err
	}
	if !exists {
		return e, corpus.ErrNotFound
	}
	rows, err := database(ctx, s.Pool).Query(ctx, `SELECT coalesce(v.quarantine_stage,'ingestion'),v.error_code,count(*) FROM `+stuckSQL+` GROUP BY 1,2`, stuckArgs(org, nil, f)...)
	if err != nil {
		return e, err
	}
	defer rows.Close()
	for rows.Next() {
		var stage, code string
		var n int64
		if err = rows.Scan(&stage, &code, &n); err != nil {
			return e, err
		}
		e.Versions += n
		e.Stages[stage] += n
		e.Codes[code] += n
	}
	return e, rows.Err()
}

// RecordReprocessEstimate keeps a dry run under its key.
func (s QuarantineStore) RecordReprocessEstimate(ctx context.Context, org, corpusID, key string, canonical []byte, e operations.ReprocessEstimate) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tag, err := database(ctx, s.Pool).Exec(ctx, `INSERT INTO quarantine_reprocess_estimates(organization,corpus_id,request_key,canonical_request,estimate) VALUES($1,$2,$3,$4,$5)
ON CONFLICT(organization,corpus_id,request_key) DO UPDATE SET estimate=EXCLUDED.estimate,created_at=now() WHERE quarantine_reprocess_estimates.canonical_request=EXCLUDED.canonical_request`, org, corpusID, key, canonical, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return operations.ErrConflict
	}
	return nil
}

// ReprocessEstimate returns the dry run recorded under a key.
func (s QuarantineStore) ReprocessEstimate(ctx context.Context, org, corpusID, key string) ([]byte, operations.ReprocessEstimate, error) {
	var canonical, raw []byte
	var e operations.ReprocessEstimate
	err := database(ctx, s.Pool).QueryRow(ctx, `SELECT canonical_request,estimate FROM quarantine_reprocess_estimates WHERE organization=$1 AND corpus_id=$2 AND request_key=$3`, org, corpusID, key).Scan(&canonical, &raw)
	if err != nil {
		return nil, e, notFound(err)
	}
	return canonical, e, json.Unmarshal(raw, &e)
}

// AcceptReprocess commits a queued reprocess with the Versions it takes,
// pinned to the active plan, or replays the one accepted under the key.
func (s QuarantineStore) AcceptReprocess(ctx context.Context, org, key string, canonical []byte, f quarantine.Filter, fromStage string, e operations.ReprocessEstimate) (operations.Operation, error) {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return operations.Operation{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return operations.Operation{}, err
	}
	var id string
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT id,canonical_request FROM operations WHERE organization=$1 AND kind=$2 AND corpus_id=$3 AND request_key=$4 AND previous_operation_id IS NULL`, org, operations.KindQuarantineReprocess, f.CorpusID, key).Scan(&id, &previous)
	if err == nil {
		if !bytes.Equal(previous, canonical) {
			return operations.Operation{}, operations.ErrConflict
		}
		op, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE organization=$1 AND id=$2`, org, id))
		if err != nil {
			return op, err
		}
		return op, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return operations.Operation{}, err
	}
	op, err := insertReprocess(ctx, tx, org, content.StableID("operation", org, operations.KindQuarantineReprocess, f.CorpusID, key), key, canonical, "", f, fromStage, e)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

// insertReprocess commits a queued reprocess Operation, optionally a rerun of
// previous: it takes the Versions f keeps now, pins them to the active plan
// and records its dispatch intent. The caller holds the journal lock.
func insertReprocess(ctx context.Context, tx pgx.Tx, org, id, key string, canonical []byte, previous string, f quarantine.Filter, fromStage string, e operations.ReprocessEstimate) (operations.Operation, error) {
	var generation string
	if err := tx.QueryRow(ctx, `SELECT `+routedGenerationSQL("$1", "$2")+` FROM corpora WHERE organization=$1 AND id=$2`, org, f.CorpusID).Scan(&generation); err != nil {
		return operations.Operation{}, notFound(err)
	}
	// Two reprocesses of one Corpus would rerun the same Versions.
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operations WHERE organization=$1 AND corpus_id=$2 AND kind=$3 AND state NOT IN ('succeeded','failed','canceled'))`, org, f.CorpusID, operations.KindQuarantineReprocess).Scan(&busy); err != nil {
		return operations.Operation{}, err
	}
	if busy {
		return operations.Operation{}, quarantine.ErrInProgress
	}
	// Pinned at acceptance: every step reruns with the plan active now,
	// even on a worker that has not followed it yet.
	var plan string
	if err := tx.QueryRow(ctx, `SELECT plan_id FROM active_pipeline_plan`).Scan(&plan); err != nil {
		return operations.Operation{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operations(organization,id,kind,corpus_id,request_key,canonical_request,target_generation_id,previous_operation_id) VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''))`, org, id, operations.KindQuarantineReprocess, f.CorpusID, key, canonical, generation, previous); err != nil {
		return operations.Operation{}, err
	}
	estimate, err := json.Marshal(e)
	if err != nil {
		return operations.Operation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO quarantine_reprocesses(organization,operation_id,plugin,code,quarantined_after,quarantined_before,plan_id,estimate,from_stage) VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''))`, org, id, f.Plugin, f.Code, f.After, f.Before, plan, estimate, fromStage); err != nil {
		return operations.Operation{}, err
	}
	args := append(stuckArgs(org, nil, f), id)
	tag, err := tx.Exec(ctx, `INSERT INTO quarantine_reprocess_items(organization,operation_id,version_id,record_id,receipt_id,stage,previous)
SELECT $1,$8,v.id,v.record_id,rc.id,coalesce(v.quarantine_stage,'ingestion'),`+reasonSQL+` FROM `+stuckSQL, args...)
	if err != nil {
		return operations.Operation{}, err
	}
	if err = addCounters(ctx, tx, org, id, map[string]int64{"versions_in_scope": tag.RowsAffected(), "versions_recovered": 0, "versions_quarantined": 0, "versions_skipped": 0}); err != nil {
		return operations.Operation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO pipeline_plan_work(kind,organization,work_id,plan_id) VALUES('operation',$1,$2,$3)`, org, id, plan); err != nil {
		return operations.Operation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operation_outbox(organization,operation_id,trace_context) VALUES($1,$2,$3)`, org, id, telemetry.Encode(ctx)); err != nil {
		return operations.Operation{}, err
	}
	if err = operationEvent(ctx, tx, org, f.CorpusID, id, operations.StateQueued); err != nil {
		return operations.Operation{}, err
	}
	return scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE organization=$1 AND id=$2`, org, id))
}

// reprocessOf decodes the reprocess columns of an Operation row.
func reprocessOf(raw []byte) (*operations.Reprocess, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var row struct {
		Plugin            string                       `json:"plugin"`
		Code              string                       `json:"code"`
		QuarantinedAfter  *time.Time                   `json:"quarantined_after"`
		QuarantinedBefore *time.Time                   `json:"quarantined_before"`
		FromStage         string                       `json:"from_stage"`
		Plan              string                       `json:"plan_id"`
		Estimate          operations.ReprocessEstimate `json:"estimate"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, err
	}
	return &operations.Reprocess{Plugin: row.Plugin, Code: row.Code, QuarantinedAfter: row.QuarantinedAfter, QuarantinedBefore: row.QuarantinedBefore, PlanID: row.Plan, FromStage: row.FromStage, Estimate: row.Estimate}, nil
}

// reprocessFilter is the scope a reprocess Operation was accepted for.
func reprocessFilter(op operations.Operation) quarantine.Filter {
	r := op.Reprocess
	return quarantine.Filter{CorpusID: op.CorpusID, Plugin: r.Plugin, Code: r.Code, After: r.QuarantinedAfter, Before: r.QuarantinedBefore}
}

const itemColumns = `version_id,record_id,receipt_id,stage,phase`

func scanItem(row pgx.Row) (*quarantine.Item, error) {
	var it quarantine.Item
	if err := row.Scan(&it.VersionID, &it.RecordID, &it.ReceiptID, &it.Stage, &it.Phase); err != nil {
		return nil, err
	}
	return &it, nil
}

// BeginReprocess starts a reprocess step: see quarantine.RunStore.
func (s QuarantineStore) BeginReprocess(ctx context.Context, org, id string) (operations.Operation, *quarantine.Item, error) {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return operations.Operation{}, nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return operations.Operation{}, nil, err
	}
	op, err := lockOperation(ctx, tx, org, id)
	if err != nil {
		return op, nil, err
	}
	if op.Kind != operations.KindQuarantineReprocess || op.Reprocess == nil {
		return op, nil, operations.ErrUnsupportedKind
	}
	if op.State == operations.StateQueued {
		if err = transition(ctx, tx, op, operations.StateRunning); err != nil {
			return op, nil, err
		}
		op.State = operations.StateRunning
	}
	started, err := scanItem(tx.QueryRow(ctx, `SELECT `+itemColumns+` FROM quarantine_reprocess_items WHERE organization=$1 AND operation_id=$2 AND phase IN ('renormalizing','released') ORDER BY version_id LIMIT 1`, org, id))
	if errors.Is(err, pgx.ErrNoRows) {
		started, err = nil, nil
	}
	if err != nil {
		return op, nil, err
	}
	return op, started, tx.Commit(ctx)
}

// versionState is what a reprocess reads of a Version under its row locks.
type versionState struct {
	corpusID, stage, code         string
	quarantined, ready, withdrawn bool
	desired                       bool
}

func lockVersion(ctx context.Context, tx pgx.Tx, org, versionID string) (versionState, error) {
	var v versionState
	err := tx.QueryRow(ctx, `SELECT r.corpus_id,coalesce(v.quarantine_stage,'ingestion'),v.error_code,v.quarantined,v.baseline_ready,
 `+recordGoneSQL+`,coalesce(r.desired_version_id=v.id,false)
FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2 FOR UPDATE OF v,r`, org, versionID).
		Scan(&v.corpusID, &v.stage, &v.code, &v.quarantined, &v.ready, &v.withdrawn, &v.desired)
	return v, err
}

// settle ends an item with an outcome and counts it.
func settle(ctx context.Context, tx pgx.Tx, org, id, versionID, phase, code string) error {
	if _, err := tx.Exec(ctx, `UPDATE quarantine_reprocess_items SET phase=$4,outcome_code=$5,updated_at=now() WHERE organization=$1 AND operation_id=$2 AND version_id=$3`, org, id, versionID, phase, code); err != nil {
		return err
	}
	counters := map[string]int64{"versions_" + phase: 1}
	if phase == quarantine.PhaseSkipped {
		counters["skipped_"+code] = 1
	}
	return addCounters(ctx, tx, org, id, counters)
}

// release lifts a Version's quarantine so its baseline runs like a new
// Version's; quarantined_at is cleared, and set again by a new quarantine.
func release(ctx context.Context, tx pgx.Tx, org, versionID string) error {
	_, err := tx.Exec(ctx, `UPDATE record_versions SET quarantined=false,quarantine=NULL,quarantine_stage=NULL,quarantined_at=NULL,processing='queued',error_code='' WHERE organization=$1 AND id=$2`, org, versionID)
	return err
}

// StartReprocessItem takes and prepares the next pending item: see
// quarantine.RunStore.
func (s QuarantineStore) StartReprocessItem(ctx context.Context, org, id string) (*quarantine.Item, error) {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return nil, err
	}
	op, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE organization=$1 AND id=$2 FOR SHARE`, org, id))
	if err != nil {
		return nil, err
	}
	if op.State != operations.StateRunning || op.Reprocess == nil {
		return nil, operations.ErrNotRunning
	}
	it, err := scanItem(tx.QueryRow(ctx, `SELECT `+itemColumns+` FROM quarantine_reprocess_items WHERE organization=$1 AND operation_id=$2 AND phase='pending' ORDER BY version_id LIMIT 1 FOR UPDATE`, org, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	v, err := lockVersion(ctx, tx, org, it.VersionID)
	if err != nil {
		return nil, err
	}
	skip := ""
	switch {
	case v.withdrawn:
		skip = quarantine.SkipWithdrawn
	case !v.desired:
		skip = quarantine.SkipSuperseded
	case !v.quarantined:
		skip = quarantine.SkipNotQuarantined
	}
	if skip != "" {
		it.Phase = quarantine.PhaseSkipped
		return it, commitAfter(ctx, tx, settle(ctx, tx, org, id, it.VersionID, quarantine.PhaseSkipped, skip))
	}
	it.Stage, it.Phase = v.stage, quarantine.PhaseReleased
	if op.Reprocess.FromStage != "" {
		it.Stage = op.Reprocess.FromStage
	}
	if it.Stage == content.QuarantineNormalization {
		// Do not invalidate a stored outcome that cannot be republished.
		var republishable bool
		if err = tx.QueryRow(ctx, `SELECT a.command->'content'->>'kind'='blob'
 AND NOT EXISTS(SELECT 1 FROM segmentations WHERE organization=$1 AND version_id=$2)
 FROM accepted_revisions a JOIN ingestion_receipts rc ON (rc.organization,rc.record_id,rc.slot)=(a.organization,a.record_id,a.slot)
 WHERE rc.organization=$1 AND rc.id=$3`, org, it.VersionID, it.ReceiptID).Scan(&republishable); err != nil {
			return nil, err
		}
		if !republishable {
			it.Phase = quarantine.PhaseSkipped
			return it, commitAfter(ctx, tx, settle(ctx, tx, org, id, it.VersionID, quarantine.PhaseSkipped, quarantine.SkipNotRepublishable))
		}
		// The previous normalization is moved aside, with its retry budget:
		// the normalizer runs again and its outcome is recorded as for a
		// first normalization. The item keeps the reason it replaces.
		if _, err = tx.Exec(ctx, `DELETE FROM normalizations WHERE organization=$1 AND version_id=$2`, org, it.VersionID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM normalization_attempts WHERE organization=$1 AND version_id=$2`, org, it.VersionID); err != nil {
			return nil, err
		}
		it.Phase = quarantine.PhaseRenormalizing
	} else if err = release(ctx, tx, org, it.VersionID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE quarantine_reprocess_items SET phase=$4,stage=$5,updated_at=now() WHERE organization=$1 AND operation_id=$2 AND version_id=$3`, org, id, it.VersionID, it.Phase, it.Stage); err != nil {
		return nil, err
	}
	return it, tx.Commit(ctx)
}

func commitAfter(ctx context.Context, tx pgx.Tx, err error) error {
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// lockItem locks a started item; ok is false once it has an outcome.
func lockItem(ctx context.Context, tx pgx.Tx, org, id, versionID string) (quarantine.Item, []byte, bool, error) {
	var it quarantine.Item
	var previous []byte
	err := tx.QueryRow(ctx, `SELECT `+itemColumns+`,previous FROM quarantine_reprocess_items WHERE organization=$1 AND operation_id=$2 AND version_id=$3 FOR UPDATE`, org, id, versionID).
		Scan(&it.VersionID, &it.RecordID, &it.ReceiptID, &it.Stage, &it.Phase, &previous)
	if err != nil {
		return it, nil, false, notFound(err)
	}
	return it, previous, it.Phase == quarantine.PhaseRenormalizing || it.Phase == quarantine.PhaseReleased, nil
}

// RepublishItem publishes a renormalized Version again and lifts its
// quarantine. The Version keeps its identity, which derives from the
// accepted input; it was never searchable, so no segment derives from the
// input Manifest it was published with. It reports whether it released the
// Version; content.ErrConflict when the Version was segmented meanwhile.
func (s QuarantineStore) RepublishItem(ctx context.Context, org, id string, item quarantine.Item, w content.Work, p content.Publication) (bool, error) {
	released := false
	err := s.republish(ctx, org, id, item, w, p, &released)
	return released && err == nil, err
}

func (s QuarantineStore) republish(ctx context.Context, org, id string, item quarantine.Item, w content.Work, p content.Publication, released *bool) error {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	it, _, started, err := lockItem(ctx, tx, org, id, item.VersionID)
	if err != nil || !started || it.Phase != quarantine.PhaseRenormalizing {
		return commitAfter(ctx, tx, err)
	}
	v, err := lockVersion(ctx, tx, org, item.VersionID)
	if err != nil {
		return err
	}
	if !v.quarantined {
		return commitAfter(ctx, tx, settle(ctx, tx, org, id, item.VersionID, quarantine.PhaseSkipped, quarantine.SkipNotQuarantined))
	}
	var segmented bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM segmentations WHERE organization=$1 AND version_id=$2)`, org, item.VersionID).Scan(&segmented); err != nil {
		return err
	}
	if segmented {
		return content.ErrConflict
	}
	blobs := []content.Blob{p.Normalized, p.Manifest}
	for _, part := range p.Parts {
		blobs = append(blobs, part.Blob)
	}
	for _, b := range blobs {
		if _, err = tx.Exec(ctx, "INSERT INTO content_blobs VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", org, content.StableID("blob", org, b.SHA256), b.Key, b.SHA256, b.Size); err != nil {
			return err
		}
	}
	provenance, err := json.Marshal(w.Command.Provenance)
	if err != nil {
		return err
	}
	extensions := w.Command.Extensions
	if extensions == nil {
		extensions = content.Extensions{}
	}
	extensionsJSON, err := json.Marshal(extensions)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM version_parts WHERE organization=$1 AND version_id=$2`, org, item.VersionID); err != nil {
		return err
	}
	for _, part := range p.Parts {
		if _, err = tx.Exec(ctx, "INSERT INTO version_parts VALUES($1,$2,$3,$4,$5)", org, item.VersionID, part.Key, part.Role, content.StableID("blob", org, part.Blob.SHA256)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE record_versions SET text_blob_id=$3,manifest_blob_id=$4,provenance=$5,extensions=$6,materialized_at=clock_timestamp() WHERE organization=$1 AND id=$2`,
		org, item.VersionID, content.StableID("blob", org, p.Normalized.SHA256), content.StableID("blob", org, p.Manifest.SHA256), provenance, extensionsJSON); err != nil {
		return err
	}
	if err = release(ctx, tx, org, item.VersionID); err != nil {
		return err
	}
	// The Version's content changed: consumers reread it.
	if err = appendEvent(ctx, tx, eventInput{Organization: org, CorpusID: v.corpusID, Kind: "record.materialized", Resource: "record", ResourceID: item.RecordID, MutationID: content.StableID("republished", item.VersionID, p.Manifest.SHA256)}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE quarantine_reprocess_items SET phase='released',updated_at=now() WHERE organization=$1 AND operation_id=$2 AND version_id=$3`, org, id, item.VersionID); err != nil {
		return err
	}
	*released = true
	return tx.Commit(ctx)
}

// RequarantineItem keeps a renormalizing Version quarantined with its new
// reason and finishes the item.
func (s QuarantineStore) RequarantineItem(ctx context.Context, org, id string, item quarantine.Item, reason content.Diagnostic) error {
	raw, err := json.Marshal(reason)
	if err != nil {
		return err
	}
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	_, _, started, err := lockItem(ctx, tx, org, id, item.VersionID)
	if err != nil || !started {
		return commitAfter(ctx, tx, err)
	}
	v, err := lockVersion(ctx, tx, org, item.VersionID)
	if err != nil {
		return err
	}
	if !v.quarantined {
		return commitAfter(ctx, tx, settle(ctx, tx, org, id, item.VersionID, quarantine.PhaseSkipped, quarantine.SkipNotQuarantined))
	}
	if _, err = tx.Exec(ctx, `UPDATE record_versions SET quarantine=$3,error_code=$4,processing='blocked',quarantine_stage='normalization',quarantined_at=clock_timestamp() WHERE organization=$1 AND id=$2`, org, item.VersionID, raw, reason.Code); err != nil {
		return err
	}
	if err = quarantinedEvent(ctx, tx, org, v.corpusID, item.RecordID, item.VersionID, reason.Code); err != nil {
		return err
	}
	return commitAfter(ctx, tx, settle(ctx, tx, org, id, item.VersionID, quarantine.PhaseQuarantined, reason.Code))
}

// FinishItem records a started item's outcome from its Version.
func (s QuarantineStore) FinishItem(ctx context.Context, org, id string, item quarantine.Item) error {
	return s.endItem(ctx, org, id, item, false)
}

// AbandonItem ends the started item of a canceled reprocess.
func (s QuarantineStore) AbandonItem(ctx context.Context, org, id string, item quarantine.Item) error {
	return s.endItem(ctx, org, id, item, true)
}

// endItem settles a started item from its Version's state. A Version left
// neither searchable nor quarantined, which a canceled reprocess released,
// is quarantined again with its previous reason: it never waits for a
// processing nobody runs.
func (s QuarantineStore) endItem(ctx context.Context, org, id string, item quarantine.Item, canceled bool) error {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	it, previous, started, err := lockItem(ctx, tx, org, id, item.VersionID)
	if err != nil || !started {
		return commitAfter(ctx, tx, err)
	}
	v, err := lockVersion(ctx, tx, org, item.VersionID)
	if err != nil {
		return err
	}
	switch {
	case v.ready:
		err = settle(ctx, tx, org, id, it.VersionID, quarantine.PhaseRecovered, "")
	case v.quarantined && it.Phase == quarantine.PhaseReleased:
		err = settle(ctx, tx, org, id, it.VersionID, quarantine.PhaseQuarantined, v.code)
	case v.withdrawn:
		err = settle(ctx, tx, org, id, it.VersionID, quarantine.PhaseSkipped, quarantine.SkipWithdrawn)
	case v.quarantined:
		// Canceled before its normalization was decided: it keeps its reason.
		err = settle(ctx, tx, org, id, it.VersionID, quarantine.PhaseSkipped, quarantine.SkipCanceled)
	default:
		var reason content.Diagnostic
		if err = json.Unmarshal(previous, &reason); err != nil {
			return err
		}
		// Its content is published now: a later reprocess reruns its ingestion.
		if _, err = tx.Exec(ctx, `UPDATE record_versions SET quarantined=true,quarantine=$3,error_code=$4,processing='blocked',quarantine_stage='ingestion',quarantined_at=clock_timestamp() WHERE organization=$1 AND id=$2`, org, it.VersionID, previous, reason.Code); err != nil {
			return err
		}
		// Consumers that reread it once released learn it is held again.
		if err = appendEvent(ctx, tx, eventInput{Organization: org, CorpusID: v.corpusID, Kind: "record.quarantined", Resource: "record", ResourceID: it.RecordID, MutationID: content.StableID("requarantine", it.VersionID, id)}); err != nil {
			return err
		}
		code := quarantine.SkipCanceled
		switch {
		case canceled:
		case !v.desired:
			code = quarantine.SkipSuperseded
		default:
			code = quarantine.SkipNotSettled
		}
		err = settle(ctx, tx, org, id, it.VersionID, quarantine.PhaseSkipped, code)
	}
	return commitAfter(ctx, tx, err)
}

// SkipItem ends a started item that no retry can carry further, leaving its
// Version quarantined with its reason.
func (s QuarantineStore) SkipItem(ctx context.Context, org, id string, item quarantine.Item, code string) error {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	_, _, started, err := lockItem(ctx, tx, org, id, item.VersionID)
	if err != nil || !started {
		return commitAfter(ctx, tx, err)
	}
	return commitAfter(ctx, tx, settle(ctx, tx, org, id, item.VersionID, quarantine.PhaseSkipped, code))
}

// CompleteReprocess records a reprocess's success.
func (s QuarantineStore) CompleteReprocess(ctx context.Context, org, id string) error {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	op, err := lockOperation(ctx, tx, org, id)
	if err != nil {
		return err
	}
	if op.State != operations.StateRunning {
		return tx.Commit(ctx)
	}
	return commitAfter(ctx, tx, succeed(ctx, tx, op, []byte(`{}`)))
}

var (
	_ quarantine.Store    = QuarantineStore{}
	_ quarantine.RunStore = QuarantineStore{}
)

// QuarantineStore persists quarantine state.
type QuarantineStore struct{ Pool *pgxpool.Pool }
