package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// operationEvent records one Operation state transition in the shared journal.
// The mutation identity makes a repeated transition idempotent.
func operationEvent(ctx context.Context, tx pgx.Tx, org, corpusID, id, state string) error {
	mutation := content.StableID("operation-state", id, state)
	var emitted bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM change_events WHERE organization=$1 AND event_id=$2)`, org, content.StableID("event", org, "operation.updated", "operation", mutation)).Scan(&emitted); err != nil || emitted {
		return err
	}
	return appendEvent(ctx, tx, eventInput{Organization: org, CorpusID: corpusID, Kind: "operation.updated", Resource: "operation", ResourceID: id, MutationID: mutation})
}

const operationColumns = `id,organization,kind,corpus_id,state,coalesce(previous_operation_id,''),target_generation_id,counters,errors,coalesce(result->>'projection_generation_id',''),
 (SELECT to_jsonb(b) FROM backfills b WHERE b.organization=operations.organization AND b.operation_id=operations.id),
 (SELECT to_jsonb(q) FROM quarantine_reprocesses q WHERE q.organization=operations.organization AND q.operation_id=operations.id)`

func scanOperation(row pgx.Row) (operations.Operation, error) {
	var op operations.Operation
	var counters, errs, fill, reprocess []byte
	err := row.Scan(&op.ID, &op.Organization, &op.Kind, &op.CorpusID, &op.State, &op.PreviousID, &op.TargetGenerationID, &counters, &errs, &op.ResultGenerationID, &fill, &reprocess)
	if err != nil {
		return op, notFound(err)
	}
	op.Counters, op.Errors = map[string]int{}, []operations.Error{}
	if err = json.Unmarshal(counters, &op.Counters); err == nil {
		err = json.Unmarshal(errs, &op.Errors)
	}
	if err == nil {
		op.Backfill, err = backfillOf(fill)
	}
	if err == nil {
		op.Reprocess, err = reprocessOf(reprocess)
	}
	return op, err
}

func (s OperationStore) AcceptRebuild(ctx context.Context, org, corpusID, key string, canonical []byte) (operations.Operation, error) {
	return s.acceptCommand(ctx, org, operations.KindProjectionRebuild, corpusID, key, canonical, nil)
}

// AcceptRetrievalConfiguration pins the next configuration version of the
// Corpus on a new target generation. The latest accepted configuration wins:
// older pending configuration Operations are canceled in the same commit.
func (s OperationStore) AcceptRetrievalConfiguration(ctx context.Context, org, corpusID, key string, canonical, resolved []byte) (operations.Operation, error) {
	return s.acceptCommand(ctx, org, operations.KindRetrievalConfiguration, corpusID, key, canonical, resolved)
}

// acceptCommand commits an originating Operation command, or returns the one
// already accepted for Organization + kind + Corpus + key + canonical request.
func (s OperationStore) acceptCommand(ctx context.Context, org, kind, corpusID, key string, canonical, resolved []byte) (operations.Operation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return operations.Operation{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return operations.Operation{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM corpora WHERE organization=$1 AND id=$2)`, org, corpusID).Scan(&exists); err != nil {
		return operations.Operation{}, err
	}
	if !exists {
		return operations.Operation{}, corpus.ErrNotFound
	}
	var id string
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT id,canonical_request FROM operations WHERE organization=$1 AND kind=$2 AND corpus_id=$3 AND request_key=$4 AND previous_operation_id IS NULL`, org, kind, corpusID, key).Scan(&id, &previous)
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
	var target *pin
	if kind == operations.KindRetrievalConfiguration {
		target = &pin{Retrieval: resolved}
		if err = tx.QueryRow(ctx, `SELECT COALESCE(max(retrieval_version),1)+1 FROM projection_generations WHERE organization=$1 AND corpus_id=$2`, org, corpusID).Scan(&target.Version); err != nil {
			return operations.Operation{}, err
		}
		if err = supersedeConfigurations(ctx, tx, org, corpusID); err != nil {
			return operations.Operation{}, err
		}
	}
	id = content.StableID("operation", org, kind, corpusID, key)
	op, err := insertOperation(ctx, tx, org, id, kind, corpusID, key, canonical, "", target)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

// supersedeConfigurations cancels the Corpus's pending configuration
// Operations: queued ones immediately, running ones by request.
func supersedeConfigurations(ctx context.Context, tx pgx.Tx, org, corpusID string) error {
	rows, err := tx.Query(ctx, `SELECT `+operationColumns+` FROM operations WHERE organization=$1 AND corpus_id=$2 AND kind=$3 AND state IN ('queued','running') ORDER BY id FOR UPDATE`, org, corpusID, operations.KindRetrievalConfiguration)
	if err != nil {
		return err
	}
	pending := []operations.Operation{}
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, op)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, op := range pending {
		next := operations.StateCancelRequested
		if op.State == operations.StateQueued {
			next = operations.StateCanceled
		}
		if err = transition(ctx, tx, op, next); err != nil {
			return err
		}
	}
	return nil
}

// pin is the retrieval configuration a new target generation is built with.
type pin struct {
	Retrieval []byte
	Version   int
}

// insertOperation commits a queued Operation with its own new logical target
// generation, dispatch intent and journal event. A nil pin builds the target
// with the Corpus's currently effective retrieval configuration.
func insertOperation(ctx context.Context, tx pgx.Tx, org, id, kind, corpusID, key string, canonical []byte, previous string, target *pin) (operations.Operation, error) {
	if kind != operations.KindProjectionRebuild && kind != operations.KindRetrievalConfiguration {
		// Other kinds must define their own target before they become controllable.
		return operations.Operation{}, operations.ErrUnsupportedKind
	}
	generation := content.StableID("generation", org, id)
	var retrieval []byte
	var version *int
	if target != nil {
		retrieval, version = target.Retrieval, &target.Version
	}
	// The target is a new logical generation in the default generation's shared
	// physical collection and pinned profile. It carries the registry's served
	// and evaluation spaces (the default's space when nothing is registered),
	// each as a named vector. Every object it gets is written with its Source
	// Namespace, so it is projected for filtering.
	tag, err := tx.Exec(ctx, `INSERT INTO projection_generations(id,collection,profile_version,active,space_id,organization,corpus_id,retrieval,retrieval_version,source_namespace_projected,spaces,spaces_projected)
SELECT $1,d.collection,d.profile_version,false,COALESCE(`+servedSpaceSQL+`,d.space_id),$2,$3,COALESCE($4::jsonb,r.retrieval,c.retrieval),COALESCE($5::integer,r.retrieval_version),true,
 COALESCE(`+deploymentSpacesSQL+`,jsonb_build_array(jsonb_build_object('id',d.space_id,'metric','cosine'))),true
FROM projection_generations d, projection_generations r, corpora c
WHERE d.active AND c.organization=$2 AND c.id=$3 AND r.id=`+routedGenerationSQL("$2", "$3"), generation, org, corpusID, retrieval, version)
	if err != nil {
		return operations.Operation{}, err
	}
	if tag.RowsAffected() != 1 {
		return operations.Operation{}, errors.New("default projection generation missing")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operations(organization,id,kind,corpus_id,request_key,canonical_request,target_generation_id,previous_operation_id) VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''))`, org, id, kind, corpusID, key, canonical, generation, previous); err != nil {
		return operations.Operation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operation_outbox(organization,operation_id) VALUES($1,$2)`, org, id); err != nil {
		return operations.Operation{}, err
	}
	if err = operationEvent(ctx, tx, org, corpusID, id, operations.StateQueued); err != nil {
		return operations.Operation{}, err
	}
	return scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE organization=$1 AND id=$2`, org, id))
}

// CancelOperation applies an operator cancellation under the journal lock and
// Operation row lock that every effect-committing transition also holds.
func (s OperationStore) CancelOperation(ctx context.Context, org, id string) (operations.Operation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return operations.Operation{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return operations.Operation{}, err
	}
	op, err := lockOperation(ctx, tx, org, id)
	if err != nil {
		return op, err
	}
	next := ""
	switch op.State {
	case operations.StateQueued:
		// No step has begun, so no effect can be in flight.
		next = operations.StateCanceled
	case operations.StateRunning, operations.StatePaused:
		// A paused backfill may still be finishing its last Version.
		next = operations.StateCancelRequested
	}
	if next != "" {
		if err = transition(ctx, tx, op, next); err != nil {
			return operations.Operation{}, err
		}
		op.State = next
	}
	if next == operations.StateCanceled && (op.Kind == operations.KindBackfill || op.Kind == operations.KindQuarantineReprocess) {
		// Pinned at acceptance, it never ran a step that would release it.
		if _, err = tx.Exec(ctx, `DELETE FROM pipeline_plan_work WHERE kind='operation' AND organization=$1 AND work_id=$2`, org, id); err != nil {
			return operations.Operation{}, err
		}
	}
	return op, tx.Commit(ctx)
}

// ConfirmCancel settles a cancellation request once the worker has stopped.
func (s OperationStore) ConfirmCancel(ctx context.Context, org, id string) error {
	tx, err := s.Pool.Begin(ctx)
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
	if op.State == operations.StateCancelRequested {
		if err = transition(ctx, tx, op, operations.StateCanceled); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func transition(ctx context.Context, tx pgx.Tx, op operations.Operation, state string) error {
	if _, err := tx.Exec(ctx, `UPDATE operations SET state=$3,updated_at=now() WHERE organization=$1 AND id=$2`, op.Organization, op.ID, state); err != nil {
		return err
	}
	return operationEvent(ctx, tx, op.Organization, op.CorpusID, op.ID, state)
}

// AcceptRerun links a new Operation to a terminal source. Replays are resolved
// before the terminal check, so they return the same rerun forever.
func (s OperationStore) AcceptRerun(ctx context.Context, org, sourceID, key string, canonical []byte) (operations.Operation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return operations.Operation{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return operations.Operation{}, err
	}
	source, err := lockOperation(ctx, tx, org, sourceID)
	if err != nil {
		return source, err
	}
	var id string
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT id,canonical_request FROM operations WHERE organization=$1 AND previous_operation_id=$2 AND request_key=$3`, org, sourceID, key).Scan(&id, &previous)
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
	if !operations.Terminal(source.State) {
		return operations.Operation{}, operations.ErrNotTerminal
	}
	if source.Kind == operations.KindBackfill && source.Backfill != nil {
		// A backfill rerun fills the same scope and spaces from the start;
		// the Versions already filled are no longer candidates.
		spec := *source.Backfill
		spec.PlanID, spec.Checkpoint = "", ""
		op, err := insertBackfill(ctx, tx, org, content.StableID("operation", org, "rerun", sourceID, key), source.CorpusID, key, canonical, sourceID, spec)
		if err != nil {
			return op, err
		}
		return op, tx.Commit(ctx)
	}
	if source.Kind == operations.KindQuarantineReprocess && source.Reprocess != nil {
		// A reprocess rerun takes what is still stuck in the same scope,
		// with the plan active now.
		op, err := insertReprocess(ctx, tx, org, content.StableID("operation", org, "rerun", sourceID, key), key, canonical, sourceID, reprocessFilter(source), source.Reprocess.Estimate)
		if err != nil {
			return op, err
		}
		return op, tx.Commit(ctx)
	}
	// A configuration rerun re-targets its source's configuration; a rebuild
	// rerun rebuilds with whatever configuration is effective now.
	var target *pin
	if source.Kind == operations.KindRetrievalConfiguration {
		target = &pin{}
		if err = tx.QueryRow(ctx, `SELECT retrieval,retrieval_version FROM projection_generations WHERE id=$1`, source.TargetGenerationID).Scan(&target.Retrieval, &target.Version); err != nil {
			return operations.Operation{}, err
		}
	}
	op, err := insertOperation(ctx, tx, org, content.StableID("operation", org, "rerun", sourceID, key), source.Kind, source.CorpusID, key, canonical, sourceID, target)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

func (s OperationStore) Operation(ctx context.Context, org, id string) (operations.Operation, error) {
	return scanOperation(s.Pool.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE organization=$1 AND id=$2`, org, id))
}

// ClaimOperations leases a bounded batch of undispatched Operations. Locked
// claims are skipped so concurrent dispatchers take disjoint work.
func (s OperationStore) ClaimOperations(ctx context.Context, limit int) ([]operations.Dispatch, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.Pool.Query(ctx, `WITH claimed AS (
 UPDATE operation_outbox o SET lease_until=now()+interval '5 seconds'
 FROM (SELECT organization,operation_id FROM operation_outbox WHERE NOT dispatched AND lease_until<now() ORDER BY operation_id,organization FOR UPDATE SKIP LOCKED LIMIT $1) pending
 WHERE o.organization=pending.organization AND o.operation_id=pending.operation_id
 RETURNING o.organization,o.operation_id)
 SELECT c.organization,c.operation_id,p.kind FROM claimed c JOIN operations p ON p.organization=c.organization AND p.id=c.operation_id ORDER BY c.operation_id,c.organization`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batch []operations.Dispatch
	for rows.Next() {
		var d operations.Dispatch
		if err = rows.Scan(&d.Organization, &d.OperationID, &d.Kind); err != nil {
			return nil, err
		}
		batch = append(batch, d)
	}
	return batch, rows.Err()
}

func (s OperationStore) OperationDispatched(ctx context.Context, d operations.Dispatch) error {
	_, err := s.Pool.Exec(ctx, `UPDATE operation_outbox SET dispatched=true WHERE organization=$1 AND operation_id=$2`, d.Organization, d.OperationID)
	return err
}

var _ operations.Store = OperationStore{}

// PauseOperation pauses a queued or running Operation.
func (s OperationStore) PauseOperation(ctx context.Context, org, id string) (operations.Operation, error) {
	return s.control(ctx, org, id, map[string]string{operations.StateQueued: operations.StatePaused, operations.StateRunning: operations.StatePaused})
}

// ResumeOperation resumes a paused Operation. One that never started runs
// its first step when the worker next reads it.
func (s OperationStore) ResumeOperation(ctx context.Context, org, id string) (operations.Operation, error) {
	return s.control(ctx, org, id, map[string]string{operations.StatePaused: operations.StateRunning})
}

// control applies an operator transition under the journal lock and the
// Operation row lock that every effect also takes; other states are
// returned unchanged.
func (s OperationStore) control(ctx context.Context, org, id string, next map[string]string) (operations.Operation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return operations.Operation{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return operations.Operation{}, err
	}
	op, err := lockOperation(ctx, tx, org, id)
	if err != nil {
		return op, err
	}
	if state, ok := next[op.State]; ok {
		if err = transition(ctx, tx, op, state); err != nil {
			return operations.Operation{}, err
		}
		op.State = state
	}
	return op, tx.Commit(ctx)
}

// OperationStore persists operation commands and lifecycle control.
type OperationStore struct{ Pool *pgxpool.Pool }
