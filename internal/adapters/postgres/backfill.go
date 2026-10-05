package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/backfill"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// backfillScopeSQL selects current eligible Versions for the requested
// registration and generation. Parameters are:
//
//	$1 organization, $2 corpus, $3 generation, $4 accepted-after,
//	$5 accepted-before, $6 served space, $7 target spaces,
//	$8 registration, $9 pinned plan (or empty for the active plan),
//	$10 authorize complete owner coverage instead of selecting gaps.
//
// The served projection remains the baseline that makes a Version eligible
// for backfill. The owner projection is optional: an absent owner projection
// is an independent-fill candidate. An explicit evaluation assignment can
// admit an existing owner cut without requiring served-space coverage, while
// that stored cut still uses the Fill/segmentation-differs policy. A source
// route assignment takes precedence over the default assignment; an
// evaluation assignment is considered independently of the served route.
//
// The source media type comes only from accepted_revisions. The empty value is
// normalized to text/plain, matching the value recorded at acceptance; no
// normalizer output is consulted.
const backfillScopeSQL = `(
WITH selected AS (
 SELECT r.id AS record_id,r.namespace,v.id AS version_id,
   COALESCE(NULLIF(ar.source_media_type,''),'text/plain') AS source_media_type,
   selected_pr.plugin_id AS plugin_id,
   served_pc.segmentation_id AS served_segmentation_id,
   owner_pc.segmentation_id AS owner_segmentation_id,
   COALESCE(owner_s.recipe,'') AS owner_recipe,
   EXISTS(
     SELECT 1
     FROM pipeline_plan_roles er
     WHERE er.plan_id=COALESCE(NULLIF($9,''),(SELECT plan_id FROM active_pipeline_plan))
       AND er.registration_id=$8
       AND er.role='ingestion-evaluation:'||COALESCE(NULLIF(ar.source_media_type,''),'text/plain')||':'||selected_pr.plugin_id
   ) AS evaluation_match
 FROM records r
 JOIN record_versions v ON (v.organization,v.id)=(r.organization,r.current_version_id)
 JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)
 JOIN projection_coverage served_pc ON served_pc.organization=v.organization AND served_pc.version_id=v.id AND served_pc.generation_id=$3 AND served_pc.role='served'
 JOIN plugin_registrations selected_pr ON selected_pr.id=$8
 LEFT JOIN projection_coverage owner_pc ON owner_pc.organization=v.organization AND owner_pc.version_id=v.id AND owner_pc.generation_id=$3 AND owner_pc.plugin_id=selected_pr.plugin_id
 LEFT JOIN segmentations owner_s ON owner_s.organization=owner_pc.organization AND owner_s.id=owner_pc.segmentation_id
 LEFT JOIN ingestion_receipts rc ON rc.organization=v.organization AND rc.record_id=v.record_id AND rc.acceptance_order=v.acceptance_order
 WHERE r.organization=$1 AND r.corpus_id=$2 AND ` + eligibleVersionSQL + `
   AND ($4::timestamptz IS NULL OR rc.accepted_at>=$4)
   AND ($5::timestamptz IS NULL OR rc.accepted_at<$5)
   AND NOT EXISTS(
     SELECT 1
     FROM vector_spaces target_vs
     WHERE target_vs.id=ANY($7::text[])
       AND target_vs.owner_plugin_id IS DISTINCT FROM selected_pr.plugin_id
   )
   AND (
     EXISTS(
       SELECT 1
       FROM pipeline_plan_roles er
       WHERE er.plan_id=COALESCE(NULLIF($9,''),(SELECT plan_id FROM active_pipeline_plan))
         AND er.registration_id=$8
         AND er.role='ingestion-evaluation:'||COALESCE(NULLIF(ar.source_media_type,''),'text/plain')||':'||selected_pr.plugin_id
     )
     OR EXISTS(
       SELECT 1
     FROM pipeline_plan_roles rr
       WHERE rr.plan_id=COALESCE(NULLIF($9,''),(SELECT plan_id FROM active_pipeline_plan))
         AND rr.registration_id=$8
         AND rr.role='ingestion-route:'||COALESCE(NULLIF(ar.source_media_type,''),'text/plain')
         AND served_pc.plugin_id=selected_pr.plugin_id
     )
     OR (
       NOT EXISTS(
         SELECT 1
         FROM pipeline_plan_roles rr
         WHERE rr.plan_id=COALESCE(NULLIF($9,''),(SELECT plan_id FROM active_pipeline_plan))
           AND rr.role='ingestion-route:'||COALESCE(NULLIF(ar.source_media_type,''),'text/plain')
       )
       AND served_pc.plugin_id=selected_pr.plugin_id
       AND EXISTS(
         SELECT 1
         FROM pipeline_plan_roles mr
         WHERE mr.plan_id=COALESCE(NULLIF($9,''),(SELECT plan_id FROM active_pipeline_plan))
           AND mr.registration_id=$8
           AND mr.role='ingestion:'||selected_pr.plugin_id
       )
       AND owner_s.recipe LIKE 'plugin:'||selected_pr.plugin_id||'@%'
     )
     OR (
       NOT EXISTS(
         SELECT 1
         FROM pipeline_plan_roles rr
         WHERE rr.plan_id=COALESCE(NULLIF($9,''),(SELECT plan_id FROM active_pipeline_plan))
           AND rr.role='ingestion-route:'||COALESCE(NULLIF(ar.source_media_type,''),'text/plain')
       )
       AND served_pc.plugin_id=selected_pr.plugin_id
       AND EXISTS(
         SELECT 1
         FROM pipeline_plan_roles dr
         WHERE dr.plan_id=COALESCE(NULLIF($9,''),(SELECT plan_id FROM active_pipeline_plan))
           AND dr.registration_id=$8
           AND (
             dr.role IN ('ingestion','ingestion-default')
             OR (
               dr.role LIKE 'ingestion:%'
               AND NOT EXISTS(
                 SELECT 1
                 FROM pipeline_plan_roles defaults
                 WHERE defaults.plan_id=dr.plan_id
                   AND defaults.role IN ('ingestion','ingestion-default')
               )
               AND (
                 NOT EXISTS(
                   SELECT 1
                   FROM pipeline_plan_roles members
                   WHERE members.plan_id=dr.plan_id
                     AND members.role LIKE 'ingestion:%'
                     AND members.registration_id<>$8
                 )
                 OR selected_pr.plugin_id='core.ingest'
               )
             )
           )
       )
     )
   )
)
SELECT selected.record_id,selected.namespace,selected.version_id,selected.source_media_type,
  selected.owner_recipe,
  selected.plugin_id,
  (selected.owner_segmentation_id IS NULL) AS independent,
  COALESCE(selected.owner_segmentation_id,selected.served_segmentation_id) AS segmentation_id
FROM selected
WHERE $10::bool OR (
  (
    selected.owner_segmentation_id IS NULL
    OR selected.evaluation_match
    OR NOT EXISTS(
      SELECT 1
      FROM segments sg
      WHERE sg.organization=$1 AND sg.version_id=selected.version_id AND sg.segmentation_id=selected.owner_segmentation_id
        AND NOT EXISTS(
          SELECT 1
          FROM embedding_coverage ec
          JOIN vector_spaces vs ON vs.id=ec.space_id
          WHERE ec.organization=sg.organization AND ec.segment_id=sg.id AND ec.generation_id=$3
            AND vs.owner_plugin_id=selected.plugin_id
            AND (ec.space_id=$6 OR ec.space_id<>ALL($7::text[]))
        )
    )
  )
  AND (
    selected.owner_segmentation_id IS NULL
    OR EXISTS(
      SELECT 1
      FROM segments sg
      CROSS JOIN unnest($7::text[]) target(space)
      WHERE sg.organization=$1 AND sg.version_id=selected.version_id AND sg.segmentation_id=selected.owner_segmentation_id
        AND NOT EXISTS(
          SELECT 1
          FROM embedding_coverage ec
          WHERE ec.organization=sg.organization AND ec.segment_id=sg.id AND ec.generation_id=$3 AND ec.space_id=target.space
        )
    )
  )
)
)`

// BackfillSize measures a backfill scope in the Corpus's routed generation.
func (s BackfillStore) BackfillSize(ctx context.Context, org string, spec operations.Backfill, corpusID string) (backfill.Size, error) {
	var size backfill.Size
	g, err := (ProjectionStore{Pool: s.Pool}).Generation(ctx, org, corpusID)
	if err != nil {
		return size, notFound(err)
	}
	if !g.SpacesProjected {
		return size, backfill.ErrRebuildRequired
	}
	err = s.Pool.QueryRow(ctx, `SELECT count(DISTINCT scope.version_id),count(*),COALESCE(sum(sg.end_offset-sg.start_offset),0)
FROM `+backfillScopeSQL+` scope
JOIN segments sg ON sg.organization=$1 AND sg.version_id=scope.version_id AND sg.segmentation_id=scope.segmentation_id`,
		org, corpusID, g.ID, spec.AcceptedAfter, spec.AcceptedBefore, g.SpaceID, spec.Spaces, spec.RegistrationID, spec.PlanID, false).Scan(&size.Versions, &size.Segments, &size.CodePoints)
	return size, err
}

// RecordEstimate keeps a dry run under its key.
func (s BackfillStore) RecordEstimate(ctx context.Context, org, corpusID, key string, canonical []byte, e operations.BackfillEstimate) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO backfill_estimates(organization,corpus_id,request_key,canonical_request,estimate) VALUES($1,$2,$3,$4,$5)
ON CONFLICT(organization,corpus_id,request_key) DO UPDATE SET estimate=EXCLUDED.estimate,created_at=now() WHERE backfill_estimates.canonical_request=EXCLUDED.canonical_request`, org, corpusID, key, canonical, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return operations.ErrConflict
	}
	return nil
}

// BackfillEstimate returns the dry run recorded under a key.
func (s BackfillStore) BackfillEstimate(ctx context.Context, org, corpusID, key string) ([]byte, operations.BackfillEstimate, error) {
	var canonical, raw []byte
	var e operations.BackfillEstimate
	err := s.Pool.QueryRow(ctx, `SELECT canonical_request,estimate FROM backfill_estimates WHERE organization=$1 AND corpus_id=$2 AND request_key=$3`, org, corpusID, key).Scan(&canonical, &raw)
	if err != nil {
		return nil, e, notFound(err)
	}
	return canonical, e, json.Unmarshal(raw, &e)
}

// AcceptBackfill commits a queued backfill Operation with its spec, dispatch
// intent and journal event. Its target generation is the Corpus's routed
// one at acceptance; each step follows the route.
func (s BackfillStore) AcceptBackfill(ctx context.Context, org, corpusID, key string, canonical []byte, spec operations.Backfill) (operations.Operation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return operations.Operation{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return operations.Operation{}, err
	}
	var id string
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT id,canonical_request FROM operations WHERE organization=$1 AND kind=$2 AND corpus_id=$3 AND request_key=$4 AND previous_operation_id IS NULL`, org, operations.KindBackfill, corpusID, key).Scan(&id, &previous)
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
	op, err := insertBackfill(ctx, tx, org, content.StableID("operation", org, operations.KindBackfill, corpusID, key), corpusID, key, canonical, "", spec)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

// insertBackfill commits a queued backfill Operation, optionally a rerun of
// previous.
func insertBackfill(ctx context.Context, tx pgx.Tx, org, id, corpusID, key string, canonical []byte, previous string, spec operations.Backfill) (operations.Operation, error) {
	// Two backfills of one Corpus would each rewrite its segments' vectors
	// from what they read; the caller holds the journal lock.
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operations WHERE organization=$1 AND corpus_id=$2 AND kind=$3 AND state NOT IN ('succeeded','failed','canceled'))`, org, corpusID, operations.KindBackfill).Scan(&busy); err != nil {
		return operations.Operation{}, err
	}
	if busy {
		return operations.Operation{}, backfill.ErrInProgress
	}
	// The backfill is pinned to the plan active when it is accepted, which
	// must still name its registration for ingestion: its first step then
	// runs on that plan even if a worker has not followed it yet.
	var plan string
	err := tx.QueryRow(ctx, `SELECT a.plan_id FROM active_pipeline_plan a JOIN pipeline_plan_roles r ON r.plan_id=a.plan_id
 AND r.registration_id=$1
 AND (r.role IN ('ingestion','ingestion-default') OR r.role LIKE 'ingestion:%' OR r.role LIKE 'ingestion-route:%' OR r.role LIKE 'ingestion-evaluation:%')`, spec.RegistrationID).Scan(&plan)
	if errors.Is(err, pgx.ErrNoRows) {
		return operations.Operation{}, backfill.ErrRegistrationNotActive
	}
	if err != nil {
		return operations.Operation{}, err
	}
	var generation string
	if err = tx.QueryRow(ctx, `SELECT `+routedGenerationSQL("$1", "$2")+` FROM corpora WHERE organization=$1 AND id=$2`, org, corpusID).Scan(&generation); err != nil {
		return operations.Operation{}, notFound(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operations(organization,id,kind,corpus_id,request_key,canonical_request,target_generation_id,previous_operation_id) VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''))`, org, id, operations.KindBackfill, corpusID, key, canonical, generation, previous); err != nil {
		return operations.Operation{}, err
	}
	estimate, err := json.Marshal(spec.Estimate)
	if err != nil {
		return operations.Operation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO backfills(organization,operation_id,registration_id,spaces,accepted_after,accepted_before,estimate,plan_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, org, id, spec.RegistrationID, spec.Spaces, spec.AcceptedAfter, spec.AcceptedBefore, estimate, plan); err != nil {
		return operations.Operation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO pipeline_plan_work(kind,organization,work_id,plan_id,ingestion_registration_id) VALUES('operation',$1,$2,$3,$4)`, org, id, plan, spec.RegistrationID); err != nil {
		return operations.Operation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operation_outbox(organization,operation_id,trace_context) VALUES($1,$2,$3)`, org, id, telemetry.Encode(ctx)); err != nil {
		return operations.Operation{}, err
	}
	if err = operationEvent(ctx, tx, org, corpusID, id, operations.StateQueued); err != nil {
		return operations.Operation{}, err
	}
	return scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE organization=$1 AND id=$2`, org, id))
}

// BeginBackfill starts a backfill step: see backfill.RunStore.
func (s BackfillStore) BeginBackfill(ctx context.Context, org, id, plan string) (backfill.Target, error) {
	var out backfill.Target
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return out, err
	}
	op, err := lockOperation(ctx, tx, org, id)
	if err != nil {
		return out, err
	}
	if op.Kind != operations.KindBackfill || op.Backfill == nil {
		return out, operations.ErrUnsupportedKind
	}
	if op.State == operations.StateQueued {
		if err = transition(ctx, tx, op, operations.StateRunning); err != nil {
			return out, err
		}
		op.State = operations.StateRunning
	}
	out.Operation = op
	if op.State != operations.StateRunning {
		return out, tx.Commit(ctx)
	}
	if out.Generation, err = routedGeneration(ctx, tx, org, op.CorpusID); err != nil {
		return out, err
	}
	if op.Backfill.PlanID == "" && plan != "" {
		if _, err = tx.Exec(ctx, `UPDATE backfills SET plan_id=$3 WHERE organization=$1 AND operation_id=$2`, org, id, plan); err != nil {
			return out, err
		}
		out.Operation.Backfill.PlanID = plan
	}
	return out, tx.Commit(ctx)
}

// CarryBackfillSpaces adds the target spaces to the Corpus's routed
// generation, so live enrichment fills them for every Version it enriches
// afterwards (a generation routed later, by a rebuild, gets them too), and
// counts the scope the first time.
func (s BackfillStore) CarryBackfillSpaces(ctx context.Context, org, id string) (content.Generation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return content.Generation{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return content.Generation{}, err
	}
	op, err := runningBackfill(ctx, tx, org, id)
	if err != nil {
		return content.Generation{}, err
	}
	g, err := routedGeneration(ctx, tx, org, op.CorpusID)
	if err != nil || !g.SpacesProjected {
		return g, err
	}
	if g, err = carrySpaces(ctx, tx, g, op.Backfill.Spaces); err != nil {
		return g, err
	}
	if _, counted := op.Counters["versions_in_scope"]; !counted {
		var size int64
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM `+backfillScopeSQL+` scope`, org, op.CorpusID, g.ID, op.Backfill.AcceptedAfter, op.Backfill.AcceptedBefore, g.SpaceID, op.Backfill.Spaces, op.Backfill.RegistrationID, op.Backfill.PlanID, false).Scan(&size); err != nil {
			return g, err
		}
		if err = addCounters(ctx, tx, org, id, map[string]int64{"versions_in_scope": size}); err != nil {
			return g, err
		}
	}
	return g, tx.Commit(ctx)
}

// BackfillByKey returns the backfill accepted under a key, or
// corpus.ErrNotFound.
func (s BackfillStore) BackfillByKey(ctx context.Context, org, corpusID, key string) (operations.Operation, error) {
	return scanOperation(s.Pool.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE organization=$1 AND kind=$2 AND corpus_id=$3 AND request_key=$4 AND previous_operation_id IS NULL`, org, operations.KindBackfill, corpusID, key))
}

// routedGeneration reads the generation a Corpus is routed to inside tx.
func routedGeneration(ctx context.Context, tx pgx.Tx, org, corpusID string) (content.Generation, error) {
	var g content.Generation
	var spaces []byte
	err := tx.QueryRow(ctx, `SELECT g.id,g.collection,g.profile_version,g.space_id,g.source_namespace_projected,g.spaces,g.spaces_projected FROM projection_generations g WHERE g.id=`+routedGenerationSQL("$1", "$2"), org, corpusID).
		Scan(&g.ID, &g.Collection, &g.ProfileVersion, &g.SpaceID, &g.SourceNamespaceProjected, &spaces, &g.SpacesProjected)
	if err != nil {
		return g, notFound(err)
	}
	g.Spaces, err = scanSpaces(spaces)
	return g, err
}

// carrySpaces adds to a generation the spaces it does not carry yet, with
// the registry's metric, after its existing ones.
func carrySpaces(ctx context.Context, tx pgx.Tx, g content.Generation, spaces []string) (content.Generation, error) {
	var missing []string
	for _, id := range spaces {
		if !g.Carries(id) {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return g, nil
	}
	// Backfills of other Organizations may extend the same shared generation:
	// read its spaces again under its row lock before adding any.
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT spaces FROM projection_generations WHERE id=$1 FOR UPDATE`, g.ID).Scan(&raw); err != nil {
		return g, err
	}
	current, err := scanSpaces(raw)
	if err != nil {
		return g, err
	}
	g.Spaces, missing = current, missing[:0]
	for _, id := range spaces {
		if !g.Carries(id) {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return g, nil
	}
	err = tx.QueryRow(ctx, `UPDATE projection_generations SET spaces=spaces||(SELECT jsonb_agg(jsonb_build_object('id',vs.id,'metric',vs.metric,'role',vs.role,'owner_plugin_id',vs.owner_plugin_id) ORDER BY vs.id) FROM vector_spaces vs WHERE vs.id=ANY($2))
WHERE id=$1 RETURNING spaces`, g.ID, missing).Scan(&raw)
	if err != nil {
		return g, err
	}
	g.Spaces, err = scanSpaces(raw)
	return g, err
}

// addCounters adds to an Operation's counters.
func addCounters(ctx context.Context, tx pgx.Tx, org, id string, add map[string]int64) error {
	raw, err := json.Marshal(add)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE operations SET counters=(SELECT jsonb_object_agg(k,COALESCE((counters->>k)::bigint,0)+COALESCE((($3::jsonb)->>k)::bigint,0))
 FROM (SELECT jsonb_object_keys(counters) k UNION SELECT jsonb_object_keys($3::jsonb)) keys),updated_at=now() WHERE organization=$1 AND id=$2`, org, id, raw)
	return err
}

// BackfillCandidates lists the next Versions to fill after the checkpoint.
func (s BackfillStore) BackfillCandidates(ctx context.Context, org, id string, g content.Generation, limit int) ([]backfill.Candidate, error) {
	op, err := (OperationStore{Pool: s.Pool}).Operation(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if op.Backfill == nil {
		return nil, operations.ErrUnsupportedKind
	}
	rows, err := s.Pool.Query(ctx, `SELECT scope.record_id,scope.version_id,scope.namespace,scope.source_media_type,scope.owner_recipe,scope.plugin_id,scope.independent
FROM `+backfillScopeSQL+` scope
WHERE scope.version_id>$11
ORDER BY scope.version_id
LIMIT $12`,
		org, op.CorpusID, g.ID, op.Backfill.AcceptedAfter, op.Backfill.AcceptedBefore, g.SpaceID, op.Backfill.Spaces, op.Backfill.RegistrationID, op.Backfill.PlanID, false, op.Backfill.Checkpoint, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []backfill.Candidate{}
	for rows.Next() {
		var c backfill.Candidate
		if err = rows.Scan(&c.RecordID, &c.VersionID, &c.Namespace, &c.SourceMediaType, &c.Recipe, &c.OwnerPluginID, &c.Independent); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CoveredEmbeddings lists the artifacts a generation holds for the segments
// of seg.
func (s BackfillStore) CoveredEmbeddings(ctx context.Context, org, generationID string, seg content.Segmentation) ([]content.Embedding, error) {
	ids := make([]string, len(seg.Segments))
	for i, p := range seg.Segments {
		ids[i] = p.ID
	}
	rows, err := s.Pool.Query(ctx, `SELECT a.metadata FROM embedding_coverage ec JOIN embedding_artifacts a ON (a.organization,a.id)=(ec.organization,ec.artifact_id)
WHERE ec.organization=$1 AND ec.generation_id=$2 AND ec.segment_id=ANY($3) ORDER BY ec.segment_id,ec.space_id`, org, generationID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []content.Embedding{}
	for rows.Next() {
		var raw []byte
		var e content.Embedding
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// runningBackfill locks a backfill Operation for an effect: shared, so a
// pause or a cancellation, which lock it exclusively, waits for the effect
// and every later effect sees it. It is operations.ErrNotRunning otherwise.
func runningBackfill(ctx context.Context, tx pgx.Tx, org, id string) (operations.Operation, error) {
	op, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE organization=$1 AND id=$2 FOR SHARE`, org, id))
	if err != nil {
		return op, err
	}
	if op.State != operations.StateRunning || op.Backfill == nil {
		return op, operations.ErrNotRunning
	}
	return op, nil
}

// CoverBackfill records the target vectors of one Version, with no journal
// event, and moves the checkpoint past it.
func (s BackfillStore) CoverBackfill(ctx context.Context, org, id string, g content.Generation, versionID string, artifacts []content.Embedding) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return err
	}
	op, err := runningBackfill(ctx, tx, org, id)
	if err != nil {
		return err
	}
	var eligible, routed bool
	err = tx.QueryRow(ctx, `SELECT `+eligibleVersionSQL+` AND r.corpus_id=$3 AND r.current_version_id=v.id,`+routedGenerationSQL("r.organization", "r.corpus_id")+`=$4
FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2 FOR SHARE OF r,v`, org, versionID, op.CorpusID, g.ID).Scan(&eligible, &routed)
	if err != nil {
		return notFound(err)
	}
	if !routed {
		// A rebuild routed another generation meanwhile: the next step fills
		// that one.
		return ErrGenerationChanged
	}
	segments := map[string]bool{}
	if eligible {
		for _, e := range artifacts {
			if e.Organization != org || e.VersionID != versionID || !g.Carries(e.SpaceID) {
				return content.ErrInvalid
			}
			var stored string
			if err = tx.QueryRow(ctx, `SELECT id FROM embedding_artifacts WHERE organization=$1 AND derivation_id=$2 AND segment_id=$3 AND space_id=$4`, org, e.DerivationID, e.SegmentID, e.SpaceID).Scan(&stored); err != nil {
				return notFound(err)
			}
			if stored != e.ID {
				return content.ErrConflict
			}
			if _, err = tx.Exec(ctx, `INSERT INTO embedding_coverage(organization,segment_id,generation_id,artifact_id,space_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, org, e.SegmentID, g.ID, e.ID, e.SpaceID); err != nil {
				return err
			}
			segments[e.SegmentID] = true
		}
	}
	counters := map[string]int64{"versions_done": 1, "segments": int64(len(segments))}
	if !eligible {
		// Withdrawn or superseded meanwhile; search no longer serves it.
		counters = map[string]int64{"versions_skipped": 1, "skipped_" + backfill.SkipUnavailable: 1}
	}
	if err = advance(ctx, tx, org, id, versionID, counters); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CoverBackfillEvaluation records one independent owner's projection and
// target vectors, then advances the backfill checkpoint in the same
// transaction. The external projection writes happen before this database
// fence and are idempotent on retry.
func (s BackfillStore) CoverBackfillEvaluation(ctx context.Context, org, id string, g content.Generation, versionID string, seg content.Segmentation, artifacts []content.Embedding) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return err
	}
	op, err := runningBackfill(ctx, tx, org, id)
	if err != nil {
		return err
	}
	var eligible, routed bool
	err = tx.QueryRow(ctx, `SELECT `+eligibleVersionSQL+` AND r.corpus_id=$3 AND r.current_version_id=v.id,`+routedGenerationSQL("r.organization", "r.corpus_id")+`=$4
FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2 FOR SHARE OF r,v`, org, versionID, op.CorpusID, g.ID).Scan(&eligible, &routed)
	if err != nil {
		return notFound(err)
	}
	if !routed {
		return ErrGenerationChanged
	}
	if eligible {
		if seg.VersionID != versionID {
			return content.ErrInvalid
		}
		var owner string
		err = tx.QueryRow(ctx, `SELECT scope.plugin_id
FROM `+backfillScopeSQL+` scope
WHERE scope.version_id=$11`, org, op.CorpusID, g.ID, op.Backfill.AcceptedAfter, op.Backfill.AcceptedBefore, g.SpaceID, op.Backfill.Spaces, op.Backfill.RegistrationID, op.Backfill.PlanID, true, versionID).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return backfill.ErrInvalid
		}
		if err != nil {
			return err
		}
		if owner == "" || owner != content.PluginOfRecipe(seg.Recipe) {
			return backfill.ErrInvalid
		}
		if len(artifacts) == 0 {
			return content.ErrInvalid
		}
		if len(op.Backfill.Spaces) == 0 || len(seg.Segments) == 0 {
			return content.ErrInvalid
		}
		targets := make(map[string]bool, len(op.Backfill.Spaces))
		for _, space := range op.Backfill.Spaces {
			targets[space] = true
		}
		segmentTargets := make(map[string]map[string]bool, len(seg.Segments))
		for _, p := range seg.Segments {
			segmentTargets[p.ID] = map[string]bool{}
		}
		for _, e := range artifacts {
			covered, ok := segmentTargets[e.SegmentID]
			if e.Organization != org || e.VersionID != versionID || e.SegmentationID != seg.ID || !targets[e.SpaceID] || !ok {
				return content.ErrInvalid
			}
			covered[e.SpaceID] = true
		}
		for _, covered := range segmentTargets {
			for target := range targets {
				if !covered[target] {
					return content.ErrInvalid
				}
			}
		}
		if err = coverOwnerProjection(ctx, tx, org, g, seg, artifacts); err != nil {
			return err
		}
	}
	counters := map[string]int64{"versions_done": 1, "segments": int64(len(seg.Segments))}
	if !eligible {
		counters = map[string]int64{"versions_skipped": 1, "skipped_" + backfill.SkipUnavailable: 1}
	}
	if err = advance(ctx, tx, org, id, versionID, counters); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// advance moves a backfill's checkpoint and adds to its counters.
func advance(ctx context.Context, tx pgx.Tx, org, id, versionID string, counters map[string]int64) error {
	tag, err := tx.Exec(ctx, `UPDATE backfills SET checkpoint=$3 WHERE organization=$1 AND operation_id=$2 AND checkpoint<$3`, org, id, versionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	return addCounters(ctx, tx, org, id, counters)
}

// SkipBackfill moves the checkpoint past a Version the backfill cannot fill.
func (s BackfillStore) SkipBackfill(ctx context.Context, org, id, versionID, code string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = runningBackfill(ctx, tx, org, id); err != nil {
		return err
	}
	if err = advance(ctx, tx, org, id, versionID, map[string]int64{"versions_skipped": 1, "skipped_" + code: 1}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CompleteBackfill records a backfill's success; its result names the
// generation it filled.
func (s BackfillStore) CompleteBackfill(ctx context.Context, org, id, generationID string) error {
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
	if op.State == operations.StatePaused || op.State == operations.StateCancelRequested {
		return operations.ErrNotRunning
	}
	if op.State != operations.StateRunning {
		return tx.Commit(ctx)
	}
	result, _ := json.Marshal(map[string]string{"projection_generation_id": generationID})
	if err = succeed(ctx, tx, op, result); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FailBackfill records a terminal failure of a queued or running backfill.
func (s BackfillStore) FailBackfill(ctx context.Context, org, id string, failure operations.Error) error {
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
	// Keep the existing workflow and pin alive when an operator won the
	// lifecycle race. A later step waits for resume or confirms cancellation.
	if op.State == operations.StatePaused || op.State == operations.StateCancelRequested {
		return operations.ErrNotRunning
	}
	if op.State != operations.StateQueued && op.State != operations.StateRunning {
		return tx.Commit(ctx)
	}
	if err = failOperation(ctx, tx, op, failure); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// backfillOf decodes the backfill columns of an Operation row.
func backfillOf(raw []byte) (*operations.Backfill, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var row struct {
		Registration   string                      `json:"registration_id"`
		Spaces         []string                    `json:"spaces"`
		AcceptedAfter  *time.Time                  `json:"accepted_after"`
		AcceptedBefore *time.Time                  `json:"accepted_before"`
		Plan           *string                     `json:"plan_id"`
		Checkpoint     string                      `json:"checkpoint"`
		Estimate       operations.BackfillEstimate `json:"estimate"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, err
	}
	b := &operations.Backfill{RegistrationID: row.Registration, Spaces: row.Spaces, AcceptedAfter: row.AcceptedAfter, AcceptedBefore: row.AcceptedBefore, Checkpoint: row.Checkpoint, Estimate: row.Estimate}
	if row.Plan != nil {
		b.PlanID = *row.Plan
	}
	return b, nil
}

var (
	_ backfill.Store    = BackfillStore{}
	_ backfill.RunStore = BackfillStore{}
)

// BackfillStore persists backfill checkpoints, coverage and space promotion.
type BackfillStore struct{ Pool *pgxpool.Pool }
