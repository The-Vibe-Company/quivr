package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/routing"
	"github.com/jackc/pgx/v5"
)

var errRoutingBusy = errors.New("routing publication busy")
var errRoutingDirty = errors.New("routing changed during metadata preparation")

func (s RoutingStore) cutoverRouting(ctx context.Context, tx pgx.Tx, w *routingWork) (bool, error) {
	// Prepare command metadata while holding only admin locks. These writes
	// stay uncommitted until the routing publication; imports continue using
	// the prior metadata. Failed publication attempts abort this whole transaction.
	for _, key := range []int64{pluginPlanLock, spacesLock} {
		var acquired bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, key).Scan(&acquired); err != nil {
			return false, err
		}
		if !acquired {
			return false, errRoutingBusy
		}
	}
	var dirty bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM routing_dirty_records WHERE epoch=$1)
 OR EXISTS(SELECT FROM routing_dirty_corpora WHERE epoch=$1)
 OR EXISTS(SELECT FROM routing_dirty_generations WHERE epoch=$1)`, w.id).Scan(&dirty); err != nil {
		return false, err
	}
	if dirty && !w.settings.NoRoutingChange {
		_, err := tx.Exec(ctx, `UPDATE routing_operations SET phase='reconcile' WHERE organization=$1 AND id=$2`, w.org, w.id)
		return false, err
	}
	var epoch, plan string
	var missing int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT epoch FROM routing_switch_state WHERE singleton),''),COALESCE((SELECT plan_id FROM active_pipeline_plan),''),COALESCE((counters->>'required_missing')::bigint,0) FROM routing_operations WHERE organization=$1 AND id=$2`, w.org, w.id).Scan(&epoch, &plan, &missing); err != nil {
		return false, err
	}
	if epoch != w.previousEpoch || plan != w.previousPlan {
		return false, s.failRouting(ctx, tx, w, fmt.Errorf("%w: routing settings changed while the command was prepared", registry.ErrConflict))
	}
	if missing > 0 && w.command.Kind != routing.KindRollback && !(w.command.Kind == routing.KindPromotion && w.command.Force) {
		return false, s.failRouting(ctx, tx, w, fmt.Errorf("%w: %d current Versions have incomplete target coverage", publicerr.CoverageIncomplete, missing))
	}
	result := operations.Admin{Target: w.command.Target}
	if w.command.Kind == routing.KindPromotion {
		var role, owner string
		if err := tx.QueryRow(ctx, `SELECT role,owner_plugin_id FROM vector_spaces WHERE id=$1`, w.command.Target).Scan(&role, &owner); err != nil {
			return false, err
		}
		if role != content.SpaceServed && role != content.SpaceEvaluation {
			return false, s.failRouting(ctx, tx, w, publicerr.NotEvaluationSpace)
		}
		if owner != w.settings.Owner {
			return false, s.failRouting(ctx, tx, w, registry.ErrConflict)
		}
		if _, err := tx.Exec(ctx, `UPDATE vector_spaces SET role=CASE WHEN id=$1 THEN 'served' ELSE 'evaluation' END WHERE owner_plugin_id=$2 AND (id=$1 OR role='served')`, w.command.Target, owner); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO vector_space_promotions(served_space_id,previous_space_id,owner_plugin_id) VALUES($1,$2,$3)
 ON CONFLICT(owner_plugin_id) DO UPDATE SET served_space_id=EXCLUDED.served_space_id,previous_space_id=EXCLUDED.previous_space_id,promoted_at=now()`, w.command.Target, w.settings.PreviousSpace, owner); err != nil {
			return false, err
		}
		result.ServedSpaceID, result.PreviousSpaceID = w.command.Target, w.settings.PreviousSpace
	} else {
		if w.settings.Spaces != nil {
			if err := registerSpaces(ctx, tx, w.settings.RegistrySpaces); err != nil {
				return false, err
			}
		}
		source := registry.SourceActivation
		if w.command.Kind == routing.KindRollback {
			source = registry.SourceRollback
		}
		id, err := w.previousPlan, error(nil)
		if !w.settings.Unchanged {
			id, err = recordPlan(ctx, tx, source, w.previousPlan, w.settings.Roles)
		}
		if err != nil {
			return false, err
		}
		result.PlanID, result.PreviousPlanID = id, w.previousPlan
	}
	if w.command.Kind == routing.KindRollback && w.command.PinnedWork == registry.PinnedWorkStop && !w.settings.Unchanged && w.settings.Routing != nil {
		route, _ := json.Marshal(w.settings.Routing)
		if _, err := tx.Exec(ctx, `INSERT INTO routing_work_fences(registration_id,epoch,next_routing)
   SELECT DISTINCT rr.registration_id,$1,CASE WHEN rr.registration_id=ANY($3::text[]) THEN NULL ELSE $4::jsonb END
   FROM pipeline_plan_roles rr WHERE rr.plan_id=$2 AND (rr.registration_id=ANY($3::text[]) OR rr.role IN ('ingestion','ingestion-default') OR rr.role LIKE 'ingestion:%')
   ON CONFLICT DO NOTHING`, w.id, w.previousPlan, w.settings.Retired, route); err != nil {
			return false, err
		}
	}
	// Start the exclusive deadline only after all metadata fan-out is complete.
	// A queued exclusive request would block otherwise independent shared writers.
	w.publishCtx, w.publishCancel = context.WithTimeout(ctx, 50*time.Millisecond)
	ctx = w.publishCtx
	var acquired bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, projectionRoutingLock).Scan(&acquired); err != nil {
		return false, err
	}
	if !acquired {
		return false, errRoutingBusy
	}
	// Writers may have changed coverage during preparation. Abort its metadata
	// too, then reconcile outside this fence before trying publication again.
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM routing_dirty_records WHERE epoch=$1)
 OR EXISTS(SELECT FROM routing_dirty_corpora WHERE epoch=$1)
 OR EXISTS(SELECT FROM routing_dirty_generations WHERE epoch=$1)`, w.id).Scan(&dirty); err != nil {
		return false, err
	}
	if dirty && !w.settings.NoRoutingChange {
		return false, errRoutingDirty
	}
	// This is the only installation-wide routing publication: one pointer.
	if !w.settings.NoRoutingChange {
		if _, err := tx.Exec(ctx, `INSERT INTO routing_switch_state(singleton,epoch) VALUES(true,$1) ON CONFLICT(singleton) DO UPDATE SET epoch=EXCLUDED.epoch`, w.id); err != nil {
			return false, err
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return false, err
	}
	state, phase := operations.StateSucceeded, "cleanup"
	if w.command.Kind != routing.KindPromotion {
		phase = "recovery"
		if !w.settings.Unchanged {
			state, phase = operations.StateRunning, "refresh"
		}
	}
	_, err = tx.Exec(ctx, `UPDATE routing_operations SET state=$5,observing=false,phase=$4,cursor_organization='',cursor_record='',result=$3,updated_at=now() WHERE organization=$1 AND id=$2`, w.org, w.id, raw, phase, state)
	return false, err
}
