package postgres

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Readers take this shared fence before journal, operation and generation
// locks. A route cutover takes it exclusively, after validating the plan.
const projectionRoutingLock = 642003

func lockProjectionRouting(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, projectionRoutingLock)
	return err
}

// lockProcessingVersion serializes artifact and progress writes for one
// Version, under the same route-cutover fence as publication. These writes
// emit no events and do not need to hold up other Versions' journal commits.
// They never acquire a journal lock after the Version lock.
func lockProcessingVersion(ctx context.Context, tx pgx.Tx, org, id string) error {
	batch := &pgx.Batch{}
	batch.Queue("SELECT pg_advisory_xact_lock_shared($1)", projectionRoutingLock)
	batch.Queue("SELECT id FROM record_versions WHERE organization=$1 AND id=$2 FOR UPDATE", org, id)
	return tx.SendBatch(ctx, batch).Close()
}

// applyIngestionRouting swaps only source types whose serving owner changes.
// Missing owner projections are gaps even if the old owner's cuts have vectors.
func applyIngestionRouting(ctx context.Context, tx pgx.Tx, previous registry.Plan, next registry.Activation) (bool, error) {
	if next.Set == nil {
		return false, nil
	}
	routing := next.Set.IngestionRouting()
	if previous.ID == "" || routing.Default == "" {
		return false, nil
	}
	old, err := planIngestionRouting(ctx, tx, previous.ID)
	if err != nil {
		return false, err
	}
	if old.Default == routing.Default && maps.Equal(old.Routes, routing.Routes) {
		return false, nil
	}
	raw, err := json.Marshal(content.IngestionRouting{Default: routing.Default, Routes: routing.Routes})
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, projectionRoutingLock); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, spacesLock); err != nil {
		return false, err
	}
	// Preflight and generation roles must match the effective model selection
	// that registration will retain, including a previous space promotion.
	next.Spaces, err = keepPromotion(ctx, tx, next.Spaces)
	if err != nil {
		return false, err
	}
	// Capture older generations before replacing their routes. Former,
	// unrouted generations stay historical and are not switched.
	rows, err := tx.Query(ctx, `SELECT g.id FROM `+effectiveGenerationsSQL+` g WHERE g.active OR EXISTS(SELECT 1 FROM corpus_projection_routes cr WHERE cr.generation_id=g.id) ORDER BY g.id`)
	if err != nil {
		return false, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if err = recordGenerationIngestion(ctx, tx, id, previous.ID); err != nil {
			return false, err
		}
	}
	// Empty generations need the incoming primary too: new admissions use
	// that route immediately, even when there are no current Versions to count.
	changedOwners := map[string]bool{}
	if old.Default != routing.Default {
		changedOwners[routing.Default] = true
	}
	formats := map[string]bool{}
	for media := range old.Routes {
		formats[media] = true
	}
	for media := range routing.Routes {
		formats[media] = true
	}
	selectedRouting := content.IngestionRouting{Default: routing.Default, Routes: routing.Routes}
	for media := range formats {
		if old.For(media) != selectedRouting.For(media) {
			changedOwners[selectedRouting.For(media)] = true
		}
	}
	primaryOwners := map[string]string{}
	var primaries []string
	for _, sp := range next.Spaces {
		if sp.Role == content.SpaceServed && changedOwners[sp.OwnerPluginID] {
			primaries = append(primaries, sp.ID)
			primaryOwners[sp.ID] = sp.OwnerPluginID
		}
	}
	rows, err = tx.Query(ctx, `SELECT sp.id,count(*) FROM `+effectiveGenerationsSQL+` g
 CROSS JOIN unnest($2::text[]) sp(id) WHERE g.id=ANY($1::text[])
 AND (NOT g.spaces_projected OR NOT g.spaces @> jsonb_build_array(jsonb_build_object('id',sp.id)))
 GROUP BY sp.id ORDER BY sp.id`, ids, primaries)
	if err != nil {
		return false, err
	}
	gaps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (registry.CoverageGap, error) {
		var gap registry.CoverageGap
		err := row.Scan(&gap.Space, &gap.MissingGenerations)
		gap.Owner = primaryOwners[gap.Space]
		return gap, err
	})
	if err != nil {
		return false, err
	}
	if len(gaps) > 0 {
		return false, &registry.CoverageError{Gaps: gaps}
	}
	// The route expression uses accepted source media, independent of any
	// normalizer's output format.
	const selected = `COALESCE($1::jsonb->'routes'->>COALESCE(NULLIF(ar.source_media_type,''),'text/plain'),$1::jsonb->>'default','')`
	const prior = `COALESCE(g.ingestion_routing->'routes'->>COALESCE(NULLIF(ar.source_media_type,''),'text/plain'),g.ingestion_routing->>'default','')`
	rows, err = tx.Query(ctx, `SELECT `+selected+`,COALESCE(vs.id,''),count(DISTINCT (v.organization,v.id)) FROM records r JOIN record_versions v ON (v.organization,v.id)=(r.organization,r.current_version_id)
 JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)
 JOIN `+effectiveGenerationsSQL+` g ON g.id=`+routedGenerationSQL("r.organization", "r.corpus_id")+`
 LEFT JOIN projection_coverage target ON (target.organization,target.version_id,target.generation_id,target.plugin_id)=(v.organization,v.id,g.id,`+selected+`)
 LEFT JOIN vector_spaces vs ON vs.owner_plugin_id=`+selected+` AND vs.id=ANY($2::text[])
 WHERE `+eligibleVersionSQL+` AND `+selected+`<>`+prior+` AND (target.segmentation_id IS NULL OR vs.id IS NULL OR EXISTS(
 SELECT 1 FROM segments sg WHERE sg.organization=v.organization AND sg.segmentation_id=target.segmentation_id AND (NOT g.spaces @> jsonb_build_array(jsonb_build_object('id',vs.id)) OR NOT EXISTS(SELECT 1 FROM `+embeddingCoverageForSegmentSQL("sg.organization", "sg.id")+` ec WHERE ec.organization=sg.organization AND ec.segment_id=sg.id AND ec.generation_id=g.id AND ec.space_id=vs.id))))
 GROUP BY `+selected+`,vs.id ORDER BY `+selected+`,vs.id`, raw, servingSpaceIDs(next.Spaces))
	if err != nil {
		return false, err
	}
	gaps, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (registry.CoverageGap, error) {
		var gap registry.CoverageGap
		err := row.Scan(&gap.Owner, &gap.Space, &gap.MissingVersions)
		return gap, err
	})
	if err != nil {
		return false, err
	}
	if len(gaps) > 0 {
		return false, &registry.CoverageError{Gaps: gaps}
	}
	// Demote before promoting to preserve the unique served row per Version.
	changed := `SELECT v.organization,v.id,g.id AS generation_id,` + selected + ` AS owner FROM records r JOIN record_versions v ON (v.organization,v.id)=(r.organization,r.current_version_id) JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot) JOIN ` + effectiveGenerationsSQL + ` g ON g.id=` + routedGenerationSQL("r.organization", "r.corpus_id") + ` WHERE ` + eligibleVersionSQL + ` AND ` + selected + `<>` + prior
	if _, err = tx.Exec(ctx, `UPDATE projection_coverage pc SET role='evaluation' FROM (`+changed+`) c WHERE (pc.organization,pc.version_id,pc.generation_id)=(c.organization,c.id,c.generation_id) AND pc.role='served'`, raw); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE projection_coverage pc SET role='served' FROM (`+changed+`) c WHERE (pc.organization,pc.version_id,pc.generation_id,pc.plugin_id)=(c.organization,c.id,c.generation_id,c.owner)`, raw); err != nil {
		return false, err
	}
	// Retain all carried spaces and vectors. Change their roles to the new
	// plan's roles, retaining retired model versions for older pinned work.
	roles := map[string]string{}
	defaultSpace := ""
	for _, sp := range next.Spaces {
		roles[sp.ID] = sp.Role
		if sp.OwnerPluginID == routing.Default && sp.Role == content.SpaceServed {
			defaultSpace = sp.ID
		}
	}
	roleJSON, err := json.Marshal(roles)
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE projection_generations g SET ingestion_routing=$1,space_id=CASE WHEN $4<>'' AND spaces @> jsonb_build_array(jsonb_build_object('id',$4::text)) THEN $4 ELSE space_id END,
 spaces=(SELECT jsonb_agg(CASE WHEN $2::jsonb ? (sp->>'id') THEN sp||jsonb_build_object('role',$2::jsonb->>(sp->>'id')) ELSE sp END ORDER BY n) FROM jsonb_array_elements(g.spaces) WITH ORDINALITY e(sp,n)) WHERE g.id=ANY($3::text[])`, raw, roleJSON, ids, defaultSpace)
	return true, err
}

func servingSpaceIDs(spaces []content.RegisteredSpace) []string {
	var ids []string
	for _, sp := range spaces {
		if sp.Role == content.SpaceServed {
			ids = append(ids, sp.ID)
		}
	}
	return ids
}

// pinnedOwnerServes fences global Version state from work whose source route
// now belongs to another owner. Such work keeps its original plugin pin.
var pinnedOwnerServesSQL = `SELECT g.ingestion_routing IS NULL OR COALESCE(g.ingestion_routing->'routes'->>COALESCE(NULLIF(ar.source_media_type,''),'text/plain'),g.ingestion_routing->>'default','')=COALESCE(
 (SELECT pr.plugin_id FROM pipeline_plan_roles rr JOIN plugin_registrations pr ON pr.id=rr.registration_id WHERE rr.plan_id=$3 AND rr.role='ingestion-route:'||COALESCE(NULLIF(ar.source_media_type,''),'text/plain') LIMIT 1),
 (SELECT pr.plugin_id FROM pipeline_plan_roles rr JOIN plugin_registrations pr ON pr.id=rr.registration_id WHERE rr.plan_id=$3 AND rr.role IN ('ingestion','ingestion-default') LIMIT 1),'')
 FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)
 JOIN ` + effectiveGenerationsSQL + ` g ON g.id=` + routedGenerationSQL("r.organization", "r.corpus_id") + ` WHERE v.organization=$1 AND v.id=$2`

func pinnedOwnerServes(ctx context.Context, tx pgx.Tx, org, versionID string) (bool, error) {
	w, ok := plugins.WorkOf(ctx)
	if !ok || w.Kind != plugins.WorkIngestion {
		return true, nil
	}
	var serves bool
	err := tx.QueryRow(ctx, pinnedOwnerServesSQL, org, versionID, w.Plan).Scan(&serves)
	return serves, err
}

func updatePinnedVersion(ctx context.Context, pool *pgxpool.Pool, org, id, sql string, values ...any) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockProcessingVersion(ctx, tx, org, id); err != nil {
		return err
	}
	serves, err := pinnedOwnerServes(ctx, tx, org, id)
	if err != nil {
		return err
	}
	if serves {
		if _, err = tx.Exec(ctx, sql, append([]any{org, id}, values...)...); err != nil {
			return err
		}
	}
	if err = observeQueueVersion(ctx, tx, org, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func planIngestionRouting(ctx context.Context, q querier, plan string) (content.IngestionRouting, error) {
	var oldRaw []byte
	err := q.QueryRow(ctx, `SELECT jsonb_build_object('default',COALESCE((SELECT pr.plugin_id FROM pipeline_plan_roles rr JOIN plugin_registrations pr ON pr.id=rr.registration_id WHERE rr.plan_id=$1 AND rr.role IN ('ingestion','ingestion-default') LIMIT 1),''),'routes',COALESCE((SELECT jsonb_object_agg(substring(rr.role from 17),pr.plugin_id) FROM pipeline_plan_roles rr JOIN plugin_registrations pr ON pr.id=rr.registration_id WHERE rr.plan_id=$1 AND rr.role LIKE 'ingestion-route:%'),'{}'::jsonb))`, plan).Scan(&oldRaw)
	var routing content.IngestionRouting
	if err != nil {
		return routing, err
	}
	err = json.Unmarshal(oldRaw, &routing)
	return routing, err
}

func stopOutgoingIngestion(ctx context.Context, tx pgx.Tx, active registry.Plan, next registry.Activation) error {
	if next.Set == nil {
		return nil
	}
	old, err := planIngestionRouting(ctx, tx, active.ID)
	if err != nil {
		return err
	}
	oldRaw, err := json.Marshal(old)
	if err != nil {
		return err
	}
	routing := next.Set.IngestionRouting()
	nextRaw, err := json.Marshal(content.IngestionRouting{Default: routing.Default, Routes: routing.Routes})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE pipeline_plan_work w SET stopped_at=now()
 FROM ingestion_receipts rc JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(rc.organization,rc.record_id,rc.slot)
 WHERE w.kind='ingestion' AND w.stopped_at IS NULL AND (w.organization,w.work_id)=(rc.organization,rc.id)
 AND COALESCE($1::jsonb->'routes'->>COALESCE(NULLIF(ar.source_media_type,''),'text/plain'),$1::jsonb->>'default','')<>COALESCE($2::jsonb->'routes'->>COALESCE(NULLIF(ar.source_media_type,''),'text/plain'),$2::jsonb->>'default','')
 AND COALESCE((SELECT pr.plugin_id FROM pipeline_plan_roles rr JOIN plugin_registrations pr ON pr.id=rr.registration_id WHERE rr.plan_id=w.plan_id AND rr.role='ingestion-route:'||COALESCE(NULLIF(ar.source_media_type,''),'text/plain') LIMIT 1),
 (SELECT pr.plugin_id FROM pipeline_plan_roles rr JOIN plugin_registrations pr ON pr.id=rr.registration_id WHERE rr.plan_id=w.plan_id AND rr.role IN ('ingestion','ingestion-default') LIMIT 1),'')<>COALESCE($2::jsonb->'routes'->>COALESCE(NULLIF(ar.source_media_type,''),'text/plain'),$2::jsonb->>'default','')`, oldRaw, nextRaw)
	return err
}
