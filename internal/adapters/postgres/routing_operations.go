package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/backfill"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/routing"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const routingBatch = 256

// RoutingStore separates durable preparation from the short deployment switch.
type RoutingStore struct {
	Pool     *pgxpool.Pool
	Registry registry.Service
}

const registrationMetadataColumns = `id,plugin_id,version,endpoint,manifest_digest,coalesce(artifact_digest,''),contributions,roles,
 state,created_at,updated_at,manifest,settings,check_report,origin,0::bigint,0::bigint`

func metadataRegistrations(ctx context.Context, q querier, where string, args ...any) ([]registry.Registration, error) {
	rows, err := q.Query(ctx, `SELECT `+registrationMetadataColumns+` FROM plugin_registrations `+where, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (registry.Registration, error) { return scanRegistration(r) })
}

func (s RoutingStore) AcceptRouting(ctx context.Context, org string, c routing.Command) (operations.Operation, error) {
	if !routing.IsKind(c.Kind) {
		return operations.Operation{}, operations.ErrUnsupportedKind
	}
	if c.Kind == routing.KindRollback && c.PinnedWork == "" {
		c.PinnedWork = registry.PinnedWorkDrain
	}
	if c.Kind == routing.KindRollback && c.PinnedWork != registry.PinnedWorkDrain && c.PinnedWork != registry.PinnedWorkStop {
		return operations.Operation{}, registry.ErrConflict
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return operations.Operation{}, err
	}
	defer tx.Rollback(ctx)
	// Admission serializes only bounded command metadata, without routing.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, pluginPlanLock); err != nil {
		return operations.Operation{}, err
	}
	if c.Key == "" {
		// Repeated unkeyed requests share in-flight work, but a terminal command
		// does not permanently prevent another operator attempt.
		raw, _ := json.Marshal(c)
		var id string
		err = tx.QueryRow(ctx, `SELECT id FROM routing_operations WHERE organization=$1 AND kind=$2 AND request=$3 AND state IN ('queued','running') ORDER BY created_at LIMIT 1`, org, c.Kind, raw).Scan(&id)
		if err == nil {
			return s.routingOperation(ctx, tx, org, id)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return operations.Operation{}, err
		}
		var random [16]byte
		if _, err = rand.Read(random[:]); err != nil {
			return operations.Operation{}, err
		}
		c.Key = hex.EncodeToString(random[:])
	}
	key := c.Key
	c.Key = ""
	raw, err := json.Marshal(c)
	if err != nil {
		return operations.Operation{}, err
	}
	id := content.StableID("routing-operation", org, c.Kind, key)
	var same bool
	err = tx.QueryRow(ctx, `SELECT request=$4::jsonb FROM routing_operations WHERE organization=$1 AND kind=$2 AND request_key=$3`, org, c.Kind, key, raw).Scan(&same)
	if err == nil {
		if !same {
			return operations.Operation{}, operations.ErrConflict
		}
		return s.routingOperation(ctx, tx, org, id)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return operations.Operation{}, err
	}
	var exists bool
	if c.Kind == routing.KindPromotion {
		var role string
		if err = tx.QueryRow(ctx, `SELECT role FROM vector_spaces WHERE id=$1`, c.Target).Scan(&role); err != nil {
			return operations.Operation{}, notFound(err)
		}
		if role != content.SpaceServed && role != content.SpaceEvaluation {
			return operations.Operation{}, backfill.ErrNotEvaluation
		}
	} else if c.Kind == routing.KindActivation {
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM plugin_registrations WHERE id=$1)`, c.Target).Scan(&exists); err != nil {
			return operations.Operation{}, err
		}
		if !exists {
			return operations.Operation{}, registry.ErrNotFound
		}
	}
	if c.Kind == routing.KindRollback && c.Target != "" {
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pipeline_plans WHERE id=$1)`, c.Target).Scan(&exists); err != nil {
			return operations.Operation{}, err
		}
		if !exists {
			return operations.Operation{}, registry.ErrNotFound
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO routing_operations(organization,id,kind,request_key,request) VALUES($1,$2,$3,$4,$5)`, org, id, c.Kind, key, raw); err != nil {
		return operations.Operation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO routing_operation_outbox(organization,operation_id,trace_context) VALUES($1,$2,$3)`, org, id, telemetry.Encode(ctx)); err != nil {
		return operations.Operation{}, err
	}
	op, err := s.routingOperation(ctx, tx, org, id)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

func (s RoutingStore) routingOperation(ctx context.Context, q querier, org, id string) (operations.Operation, error) {
	op := operations.Operation{Organization: org, ID: id, Counters: map[string]int{}, Errors: []operations.Error{}}
	var counters, diagnostics, request, result []byte
	err := q.QueryRow(ctx, `SELECT kind,state,counters,errors,request,result FROM routing_operations WHERE organization=$1 AND id=$2`, org, id).Scan(&op.Kind, &op.State, &counters, &diagnostics, &request, &result)
	if err != nil {
		return op, notFound(err)
	}
	if err = json.Unmarshal(counters, &op.Counters); err != nil {
		return op, err
	}
	if err = json.Unmarshal(diagnostics, &op.Errors); err != nil {
		return op, err
	}
	var command routing.Command
	if err = json.Unmarshal(request, &command); err != nil {
		return op, err
	}
	op.Admin = &operations.Admin{Target: command.Target}
	if len(result) > 0 {
		err = json.Unmarshal(result, op.Admin)
	}
	return op, err
}

type routingSettings struct {
	Unchanged      bool                      `json:"unchanged,omitempty"`
	Roles          []registry.Assignment     `json:"roles,omitempty"`
	RegistrySpaces []content.RegisteredSpace `json:"registry_spaces,omitempty"`
	Spaces         []content.RegisteredSpace `json:"spaces,omitempty"`
	Routing        *content.IngestionRouting `json:"routing,omitempty"`
	Owner          string                    `json:"owner,omitempty"`
	PreviousSpace  string                    `json:"previous_space,omitempty"`
	Retired        []string                  `json:"retired,omitempty"`
}

type routingWork struct {
	publishCtx                                                                                                context.Context
	publishCancel                                                                                             context.CancelFunc
	missingDelta, segmentsDelta, requiredDelta                                                                int64
	org, id, state, phase, previousEpoch, previousPlan, targetPlan, cursorOrg, cursorRecord, cursorGeneration string
	command                                                                                                   routing.Command
	settings                                                                                                  routingSettings
}

func (s RoutingStore) StepRouting(ctx context.Context, org, id string) (routing.Progress, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return routing.Progress{}, err
	}
	defer tx.Rollback(ctx)
	w := routingWork{org: org, id: id}
	var request, settings []byte
	err = tx.QueryRow(ctx, `SELECT state,phase,previous_epoch,previous_plan,target_plan,cursor_organization,cursor_record,cursor_generation,request,settings FROM routing_operations WHERE organization=$1 AND id=$2 FOR UPDATE`, org, id).Scan(&w.state, &w.phase, &w.previousEpoch, &w.previousPlan, &w.targetPlan, &w.cursorOrg, &w.cursorRecord, &w.cursorGeneration, &request, &settings)
	if err != nil {
		return routing.Progress{}, notFound(err)
	}
	if operations.Terminal(w.state) && w.phase != "recovery" && w.phase != "cleanup" {
		return routing.Progress{Done: true}, nil
	}
	if err = json.Unmarshal(request, &w.command); err != nil {
		return routing.Progress{}, err
	}
	if err = json.Unmarshal(settings, &w.settings); err != nil {
		return routing.Progress{}, err
	}
	var done bool
	switch w.phase {
	case "prepare":
		err = s.prepareRouting(ctx, tx, &w)
	case "generations":
		err = s.stageRoutingGenerations(ctx, tx, &w)
	case "scan":
		err = s.scanRoutingRecords(ctx, tx, &w)
	case "cleanup":
		done, err = s.cleanupRouting(ctx, tx, &w)
	case "refresh":
		// The process-local registry follows a committed plan outside all routing
		// locks. A crash retries this idempotent refresh before reporting success.
		if s.Registry.Activated != nil {
			s.Registry.Activated(ctx)
		}
		_, err = tx.Exec(ctx, `UPDATE routing_operations SET state='succeeded',phase='recovery',updated_at=now() WHERE organization=$1 AND id=$2`, org, id)
	case "recovery":
		done, err = s.recoverRouting(ctx, tx, &w)
	case "reconcile":
		err = s.reconcileRouting(ctx, tx, &w)
	case "observe", "cutover":
		// The exclusive attempt and transaction lifetime are separately bounded.
		// Commit uses this deadline too; a canceled commit is recovered by state.
		commitCtx := ctx
		if w.phase == "observe" {
			cutover, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
			commitCtx = cutover
			done, err = s.observeRouting(cutover, tx, &w)
		} else {
			done, err = s.cutoverRouting(ctx, tx, &w)
			if w.publishCancel != nil {
				defer w.publishCancel()
				commitCtx = w.publishCtx
			}
			if errors.Is(err, errRoutingBusy) || errors.Is(err, errRoutingDirty) {
				if rollback := tx.Rollback(ctx); rollback != nil {
					return routing.Progress{}, rollback
				}
				if errors.Is(err, errRoutingDirty) {
					_, err = s.Pool.Exec(ctx, `UPDATE routing_operations SET phase='reconcile' WHERE organization=$1 AND id=$2 AND phase='cutover' AND state='running'`, org, id)
				} else {
					err = nil
				}
				return routing.Progress{Wait: 10 * time.Millisecond}, err
			}
		}
		if routingRefusal(err) {
			// Refusal must not commit any earlier metadata write from the attempted
			// switch. Record it only after aborting and releasing the routing lock.
			if rollback := tx.Rollback(ctx); rollback != nil {
				return routing.Progress{}, rollback
			}
			return s.failUnpublishedCutover(ctx, &w, err)
		}
		if err == nil {
			err = tx.Commit(commitCtx)
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return routing.Progress{Wait: 10 * time.Millisecond}, nil
		}
		return routing.Progress{Done: done, Wait: 10 * time.Millisecond}, err
	default:
		return routing.Progress{}, fmt.Errorf("unknown routing phase %q", w.phase)
	}
	if err != nil {
		if routingRefusal(err) {
			if update := s.failRouting(ctx, tx, &w, err); update != nil {
				return routing.Progress{}, update
			}
			done = false
		} else {
			return routing.Progress{}, err
		}
	}
	if w.missingDelta != 0 || w.segmentsDelta != 0 || w.requiredDelta != 0 {
		if _, err = tx.Exec(ctx, `UPDATE routing_operations SET counters=counters||jsonb_build_object('versions_missing',COALESCE((counters->>'versions_missing')::bigint,0)+$3::bigint,'segments_missing',COALESCE((counters->>'segments_missing')::bigint,0)+$4::bigint,'required_missing',COALESCE((counters->>'required_missing')::bigint,0)+$5::bigint) WHERE organization=$1 AND id=$2`, org, id, w.missingDelta, w.segmentsDelta, w.requiredDelta); err != nil {
			return routing.Progress{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return routing.Progress{}, err
	}
	return routing.Progress{Done: done, Wait: time.Millisecond}, nil
}

func routingRefusal(err error) bool {
	if err == nil {
		return false
	}
	var public *publicerr.Error
	var incomplete *backfill.IncompleteError
	var coverage *registry.CoverageError
	var space *content.SpaceError
	return errors.As(err, &public) || errors.As(err, &incomplete) || errors.As(err, &coverage) || errors.As(err, &space)
}

func (s RoutingStore) failUnpublishedCutover(ctx context.Context, w *routingWork, cause error) (routing.Progress, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return routing.Progress{}, err
	}
	defer tx.Rollback(ctx)
	var state, phase string
	if err = tx.QueryRow(ctx, `SELECT state,phase FROM routing_operations WHERE organization=$1 AND id=$2 FOR UPDATE`, w.org, w.id).Scan(&state, &phase); err != nil {
		return routing.Progress{}, err
	}
	if operations.Terminal(state) || phase != w.phase {
		return routing.Progress{}, nil
	}
	if err = s.failRouting(ctx, tx, w, cause); err != nil {
		return routing.Progress{}, err
	}
	return routing.Progress{}, tx.Commit(ctx)
}

func (s RoutingStore) failRouting(ctx context.Context, tx pgx.Tx, w *routingWork, cause error) error {
	var space *content.SpaceError
	if errors.As(cause, &space) {
		cause = fmt.Errorf("%w: %v", registry.ErrConflict, cause)
	}
	diagnostic := operations.Error{Code: "routing_change_refused", Message: cause.Error()}
	if code, ok := publicerr.Code(cause); ok {
		diagnostic.Code = code
	}
	var incomplete *backfill.IncompleteError
	if errors.As(cause, &incomplete) {
		diagnostic.Code = "coverage_incomplete"
	}
	if len(diagnostic.Message) > 512 {
		diagnostic.Message = diagnostic.Message[:512]
	}
	raw, _ := json.Marshal([]operations.Error{diagnostic})
	_, err := tx.Exec(ctx, `UPDATE routing_operations SET state='failed',observing=false,phase='cleanup',errors=$3,updated_at=now() WHERE organization=$1 AND id=$2`, w.org, w.id, raw)
	return err
}
