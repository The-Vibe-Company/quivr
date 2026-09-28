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

const operationColumns = `id,organization,kind,corpus_id,state,coalesce(previous_operation_id,''),target_generation_id,counters,errors,coalesce(result->>'projection_generation_id','')`

func scanOperation(row pgx.Row) (operations.Operation, error) {
	var op operations.Operation
	var counters, errs []byte
	err := row.Scan(&op.ID, &op.Organization, &op.Kind, &op.CorpusID, &op.State, &op.PreviousID, &op.TargetGenerationID, &counters, &errs, &op.ResultGenerationID)
	if err != nil {
		return op, notFound(err)
	}
	op.Counters, op.Errors = map[string]int{}, []operations.Error{}
	if err = json.Unmarshal(counters, &op.Counters); err == nil {
		err = json.Unmarshal(errs, &op.Errors)
	}
	return op, err
}

func (s ContentStore) AcceptRebuild(ctx context.Context, org, corpusID, key string, canonical []byte) (operations.Operation, error) {
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
	err = tx.QueryRow(ctx, `SELECT id,canonical_request FROM operations WHERE organization=$1 AND kind=$2 AND corpus_id=$3 AND request_key=$4 AND previous_operation_id IS NULL`, org, operations.KindProjectionRebuild, corpusID, key).Scan(&id, &previous)
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
	id = content.StableID("operation", org, operations.KindProjectionRebuild, corpusID, key)
	op, err := insertOperation(ctx, tx, org, id, operations.KindProjectionRebuild, corpusID, key, canonical, "")
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

// insertOperation commits a queued Operation with its own new logical target
// generation, dispatch intent and journal event.
func insertOperation(ctx context.Context, tx pgx.Tx, org, id, kind, corpusID, key string, canonical []byte, previous string) (operations.Operation, error) {
	if kind != operations.KindProjectionRebuild {
		// Other kinds must define their own target before they become controllable.
		return operations.Operation{}, operations.ErrUnsupportedKind
	}
	target := content.StableID("generation", org, id)
	// The target is a new logical generation in the default generation's shared
	// physical collection and pinned profile/vector space.
	tag, err := tx.Exec(ctx, `INSERT INTO projection_generations(id,collection,profile_version,active,space_id,organization,corpus_id) SELECT $1,collection,profile_version,false,space_id,$2,$3 FROM projection_generations WHERE active`, target, org, corpusID)
	if err != nil {
		return operations.Operation{}, err
	}
	if tag.RowsAffected() != 1 {
		return operations.Operation{}, errors.New("default projection generation missing")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operations(organization,id,kind,corpus_id,request_key,canonical_request,target_generation_id,previous_operation_id) VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''))`, org, id, kind, corpusID, key, canonical, target, previous); err != nil {
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
func (s ContentStore) CancelOperation(ctx context.Context, org, id string) (operations.Operation, error) {
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
	case operations.StateRunning:
		next = operations.StateCancelRequested
	}
	if next != "" {
		if err = transition(ctx, tx, op, next); err != nil {
			return operations.Operation{}, err
		}
		op.State = next
	}
	return op, tx.Commit(ctx)
}

// ConfirmCancel settles a cancellation request once the worker has stopped.
func (s ContentStore) ConfirmCancel(ctx context.Context, org, id string) error {
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
func (s ContentStore) AcceptRerun(ctx context.Context, org, sourceID, key string, canonical []byte) (operations.Operation, error) {
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
	op, err := insertOperation(ctx, tx, org, content.StableID("operation", org, "rerun", sourceID, key), source.Kind, source.CorpusID, key, canonical, sourceID)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

func (s ContentStore) Operation(ctx context.Context, org, id string) (operations.Operation, error) {
	return scanOperation(s.Pool.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE organization=$1 AND id=$2`, org, id))
}

// ClaimOperation leases one undispatched Operation, mirroring the ingestion outbox.
func (s ContentStore) ClaimOperation(ctx context.Context) (operations.Dispatch, error) {
	var d operations.Dispatch
	err := s.Pool.QueryRow(ctx, `UPDATE operation_outbox SET lease_until=now()+interval '5 seconds' WHERE (organization,operation_id)=(SELECT organization,operation_id FROM operation_outbox WHERE NOT dispatched AND lease_until<now() ORDER BY operation_id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING organization,operation_id`).Scan(&d.Organization, &d.OperationID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = operations.ErrNoDispatch
	}
	return d, err
}

func (s ContentStore) OperationDispatched(ctx context.Context, d operations.Dispatch) error {
	_, err := s.Pool.Exec(ctx, `UPDATE operation_outbox SET dispatched=true WHERE organization=$1 AND operation_id=$2`, d.Organization, d.OperationID)
	return err
}

var _ operations.Store = ContentStore{}
