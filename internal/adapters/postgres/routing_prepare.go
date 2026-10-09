package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/The-Vibe-Company/quivr/internal/backfill"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr/internal/routing"
	"github.com/jackc/pgx/v5"
)

func (s RoutingStore) prepareRouting(ctx context.Context, tx pgx.Tx, w *routingWork) error {
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT epoch FROM routing_switch_state WHERE singleton),'')`).Scan(&w.previousEpoch); err != nil {
		return err
	}
	active, err := activePlan(ctx, tx)
	if err != nil && !errors.Is(err, registry.ErrNoPlan) {
		return err
	}
	w.previousPlan = active.ID
	if w.command.Kind == routing.KindPromotion {
		var role string
		if err = tx.QueryRow(ctx, `SELECT owner_plugin_id,role FROM vector_spaces WHERE id=$1`, w.command.Target).Scan(&w.settings.Owner, &role); err != nil {
			return notFound(err)
		}
		if role != content.SpaceEvaluation && role != content.SpaceServed {
			return backfill.ErrNotEvaluation
		}
		if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT id FROM vector_spaces WHERE owner_plugin_id=$1 AND role='served' ORDER BY id LIMIT 1),'')`, w.settings.Owner).Scan(&w.settings.PreviousSpace); err != nil {
			return err
		}
	} else {
		members := map[string]registry.Registration{}
		list, err := metadataRegistrations(ctx, tx, `WHERE id IN (SELECT registration_id FROM pipeline_plan_roles WHERE plan_id=$1) OR id=$2`, active.ID, w.command.Target)
		if err != nil {
			return err
		}
		for _, r := range list {
			members[r.ID] = r
		}
		var activation registry.Activation
		if w.command.Kind == routing.KindActivation {
			target, ok := members[w.command.Target]
			if !ok {
				return registry.ErrNotFound
			}
			activation, err = registry.PlanActivation(active, members, target, s.Registry.Validate)
			if err == nil {
				err = s.reachRouting(ctx, target)
			}
		} else {
			w.targetPlan = w.command.Target
			if w.targetPlan == "" {
				w.targetPlan = active.PreviousPlanID
			}
			if w.targetPlan == "" {
				return registry.ErrNoPreviousPlan
			}
			target, err := pipelinePlan(ctx, tx, w.targetPlan)
			if err != nil {
				return err
			}
			list, err = metadataRegistrations(ctx, tx, `WHERE id IN (SELECT registration_id FROM pipeline_plan_roles WHERE plan_id=$1)`, target.ID)
			if err != nil {
				return err
			}
			for _, r := range list {
				members[r.ID] = r
			}
			activation, err = registry.PlanRollback(active, target, members, s.Registry.Validate)
			if err == nil {
				for _, r := range activation.Returning {
					if err = s.reachRouting(ctx, r); err != nil {
						break
					}
				}
			}
		}
		if err != nil {
			return err
		}
		w.settings.Unchanged = activation.Unchanged
		w.settings.Roles = activation.Roles
		w.settings.Retired = activation.Retired
		if activation.Set != nil {
			r := activation.Set.IngestionRouting()
			w.settings.Routing = &content.IngestionRouting{Default: r.Default, Routes: r.Routes}
			if s.Registry.Spaces != nil {
				w.settings.RegistrySpaces = s.Registry.Spaces(activation.Set)
				w.settings.Spaces, err = retainedPromotions(ctx, tx, w.settings.RegistrySpaces, false)
				if err != nil {
					return err
				}
			}
		}
	}
	raw, err := json.Marshal(w.settings)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE routing_operations SET phase='observe',previous_epoch=$3,previous_plan=$4,target_plan=$5,settings=$6 WHERE organization=$1 AND id=$2`, w.org, w.id, w.previousEpoch, w.previousPlan, w.targetPlan, raw)
	return err
}

func (s RoutingStore) observeRouting(ctx context.Context, tx pgx.Tx, w *routingWork) (bool, error) {
	// Observation begins only when prior shared writers have committed. Never
	// queue an exclusive request that would make new imports wait behind it.
	var acquired bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1) AND pg_try_advisory_xact_lock($2)`, projectionRoutingLock, pluginPlanLock).Scan(&acquired); err != nil {
		return false, err
	}
	if !acquired {
		return false, nil
	}
	var observing bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM routing_operations WHERE observing AND id<>$1)`, w.id).Scan(&observing); err != nil {
		return false, err
	}
	if observing {
		return false, nil
	}
	var epoch, plan string
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT epoch FROM routing_switch_state WHERE singleton),''),COALESCE((SELECT plan_id FROM active_pipeline_plan),'')`).Scan(&epoch, &plan); err != nil {
		return false, err
	}
	if epoch != w.previousEpoch || plan != w.previousPlan {
		_, err := tx.Exec(ctx, `UPDATE routing_operations SET phase='prepare' WHERE organization=$1 AND id=$2`, w.org, w.id)
		return false, err
	}
	_, err := tx.Exec(ctx, `UPDATE routing_operations SET state='running',phase='generations',observing=true,updated_at=now() WHERE organization=$1 AND id=$2`, w.org, w.id)
	return false, err
}

func (s RoutingStore) reachRouting(ctx context.Context, r registry.Registration) error {
	reach := s.Registry.Reach
	if reach == nil {
		reach = registry.Discover
	}
	if err := reach(ctx, r); err != nil {
		return registry.ErrUnreachable
	}
	return nil
}

func (s RoutingStore) stageRoutingGenerations(ctx context.Context, tx pgx.Tx, w *routingWork) error {
	rows, err := tx.Query(ctx, `SELECT g.id,g.space_id,g.spaces,g.ingestion_routing,g.active OR EXISTS(SELECT FROM corpus_projection_routes cr WHERE cr.generation_id=g.id),g.spaces_projected,g.coverage_routed
 FROM `+effectiveGenerationsSQL+` g WHERE g.id>$1 ORDER BY g.id LIMIT $2`, w.cursorGeneration, routingBatch)
	if err != nil {
		return err
	}
	type generation struct {
		id, primary                       string
		spaces                            []content.GenerationSpace
		route                             *content.IngestionRouting
		routed, projected, coverageRouted bool
	}
	batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (generation, error) {
		var g generation
		var spaces, route []byte
		err := r.Scan(&g.id, &g.primary, &spaces, &route, &g.routed, &g.projected, &g.coverageRouted)
		if err != nil {
			return g, err
		}
		if err = json.Unmarshal(spaces, &g.spaces); err != nil {
			return g, err
		}
		if len(route) > 0 {
			err = json.Unmarshal(route, &g.route)
		}
		return g, err
	})
	if err != nil {
		return err
	}
	for _, g := range batch {
		if w.command.Kind == routing.KindPromotion {
			carries, swaps := false, false
			for _, sp := range g.spaces {
				carries = carries || sp.ID == w.command.Target
				swaps = swaps || (sp.ID == w.settings.PreviousSpace && sp.Role == content.SpaceServed)
			}
			if carries && (swaps || g.primary == w.settings.PreviousSpace) {
				if g.primary == w.settings.PreviousSpace {
					g.primary = w.command.Target
				}
				for i := range g.spaces {
					if g.spaces[i].ID == w.command.Target {
						g.spaces[i].Role = content.SpaceServed
						g.spaces[i].OwnerPluginID = w.settings.Owner
					}
					if g.spaces[i].ID == w.settings.PreviousSpace && g.spaces[i].ID != w.command.Target {
						g.spaces[i].Role = content.SpaceEvaluation
						g.spaces[i].OwnerPluginID = w.settings.Owner
					}
				}
			}
		} else if g.routed && w.settings.Routing != nil {
			if g.route == nil {
				old, err := planIngestionRouting(ctx, tx, w.previousPlan)
				if err != nil {
					return err
				}
				g.route = &old
			}
			changedOwners := map[string]bool{}
			if g.route.Default != w.settings.Routing.Default {
				changedOwners[w.settings.Routing.Default] = true
			}
			for media := range g.route.Routes {
				if g.route.For(media) != w.settings.Routing.For(media) {
					changedOwners[w.settings.Routing.For(media)] = true
				}
			}
			for media := range w.settings.Routing.Routes {
				if g.route.For(media) != w.settings.Routing.For(media) {
					changedOwners[w.settings.Routing.For(media)] = true
				}
			}
			if len(changedOwners) > 0 {
				g.coverageRouted = true
			}
			for _, sp := range w.settings.Spaces {
				found := false
				for i := range g.spaces {
					if g.spaces[i].ID == sp.ID {
						found = true
						// Rollback retains outgoing named spaces for gap fallback.
						if w.command.Kind != routing.KindRollback || sp.Role == content.SpaceServed {
							g.spaces[i].Role = sp.Role
						}
					}
				}
				if w.command.Kind == routing.KindActivation && sp.Role == content.SpaceServed && changedOwners[sp.OwnerPluginID] && (!found || !g.projected) {
					return &registry.CoverageError{Gaps: []registry.CoverageGap{{Owner: sp.OwnerPluginID, Space: sp.ID, MissingGenerations: 1}}}
				}
				if !found && w.command.Kind == routing.KindRollback && sp.Role == content.SpaceServed {
					g.spaces = append(g.spaces, content.GenerationSpace{ID: sp.ID, Role: sp.Role, Metric: sp.Metric, OwnerPluginID: sp.OwnerPluginID})
					found = true
				}
				if sp.Role == content.SpaceServed && sp.OwnerPluginID == w.settings.Routing.Default && found {
					g.primary = sp.ID
				}
			}
			g.route = w.settings.Routing
		}
		slices.SortStableFunc(g.spaces, func(a, b content.GenerationSpace) int {
			if a.Role == content.SpaceServed && b.Role != content.SpaceServed {
				return -1
			}
			if b.Role == content.SpaceServed && a.Role != content.SpaceServed {
				return 1
			}
			return 0
		})
		// A complete cumulative snapshot preserves earlier overrides even when
		// this command changes only a different owner or generation.
		spaces, _ := json.Marshal(g.spaces)
		var route []byte
		if g.route != nil {
			route, _ = json.Marshal(g.route)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO routing_generation_settings(epoch,generation_id,space_id,spaces,ingestion_routing,coverage_routed) VALUES($1,$2,$3,$4,$5,$6)
 ON CONFLICT(epoch,generation_id) DO UPDATE SET space_id=EXCLUDED.space_id,spaces=EXCLUDED.spaces,ingestion_routing=EXCLUDED.ingestion_routing,coverage_routed=EXCLUDED.coverage_routed`, w.id, g.id, g.primary, spaces, route, g.coverageRouted); err != nil {
			return err
		}
		w.cursorGeneration = g.id
	}
	phase := "generations"
	if len(batch) < routingBatch {
		phase = "scan"
	}
	_, err = tx.Exec(ctx, `UPDATE routing_operations SET phase=$3,cursor_generation=$4 WHERE organization=$1 AND id=$2`, w.org, w.id, phase, w.cursorGeneration)
	return err
}
