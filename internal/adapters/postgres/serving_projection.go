package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// currentServingRecipe returns the active registration's recipe only when it
// can serve this generation's primary. A retired model waits for rebuild;
// dispatching a current-plan job into that old space would recreate retries.
func currentServingRecipe(ctx context.Context, q querier, org, versionID string, g content.Generation) (string, error) {
	var r registry.Registration
	var settings, provenance []byte
	var recipe string
	err := q.QueryRow(ctx, `SELECT pr.id,pr.plugin_id,pr.version,pr.endpoint,pr.manifest_digest,pr.manifest,pr.settings,COALESCE(rr.ingestion_recipe,''),rr.ingestion_provenance
 FROM record_versions v JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)
 JOIN projection_generations g ON g.id=$3 JOIN active_pipeline_plan a ON true
 JOIN plugin_registrations pr ON pr.plugin_id=COALESCE(g.ingestion_routing->'routes'->>COALESCE(NULLIF(ar.source_media_type,''),'text/plain'),g.ingestion_routing->>'default','')
 JOIN pipeline_plan_roles rr ON rr.plan_id=a.plan_id AND rr.registration_id=pr.id AND rr.role='ingestion:'||pr.plugin_id
 WHERE v.organization=$1 AND v.id=$2`, org, versionID, g.ID).Scan(&r.ID, &r.PluginID, &r.Version, &r.Endpoint, &r.ManifestDigest, &r.Manifest, &settings, &recipe, &provenance)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if err = json.Unmarshal(settings, &r.Settings); err != nil {
		return "", err
	}
	pin, err := r.Pin()
	if err != nil {
		return "", err
	}
	if recipe != "" {
		pin.IngestionDerivation = &plugins.IngestionDerivation{Recipe: recipe, Provenance: provenance}
	}
	// Serving jobs require recorded owner/space metadata for eligibility.
	// A legacy primary alone cannot authorize a historical handoff.
	primary := g.ServedFor(r.PluginID)
	known := false
	for _, sp := range g.Spaces {
		if sp.ID == primary && sp.OwnerPluginID == r.PluginID && sp.Role == content.SpaceServed {
			known = true
		}
	}
	if !known {
		return "", nil
	}
	for _, space := range pin.EnabledSpaces() {
		if space.Key == primary {
			return registry.IngestionRecipe(pin), nil
		}
	}
	return "", nil
}

// queueServingProjection is called only after canonical Parts exist. The
// target gets a separate durable plan pin; the original work is never rebound.
func queueServingProjection(ctx context.Context, tx pgx.Tx, org, versionID string, supersededID ...string) error {
	var job content.IngestionEvaluation
	var registration registry.Registration
	var settings, generationSpaces, provenance []byte
	var recipe string
	var modelSelection string
	err := tx.QueryRow(ctx, `SELECT v.record_id,g.id,pr.plugin_id,pr.id,a.plan_id,pr.version,pr.endpoint,pr.manifest_digest,pr.manifest,pr.settings,COALESCE(rr.ingestion_recipe,''),rr.ingestion_provenance,g.spaces,COALESCE((SELECT promoted_at::text FROM vector_space_promotions WHERE owner_plugin_id=pr.plugin_id),'')
 FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)
 JOIN projection_generations g ON g.id=`+routedGenerationSQL("r.organization", "r.corpus_id")+`
 JOIN active_pipeline_plan a ON true JOIN plugin_registrations pr ON pr.plugin_id=COALESCE(g.ingestion_routing->'routes'->>COALESCE(NULLIF(ar.source_media_type,''),'text/plain'),g.ingestion_routing->>'default','')
 JOIN pipeline_plan_roles rr ON rr.plan_id=a.plan_id AND rr.registration_id=pr.id AND rr.role='ingestion:'||pr.plugin_id
 WHERE v.organization=$1 AND v.id=$2 AND r.desired_version_id=v.id AND (NOT v.baseline_ready OR v.enrichment_error=$3) AND NOT v.quarantined AND NOT r.withdrawn AND NOT EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id)`, org, versionID, content.CodeRebuildRequired).Scan(&job.RecordID, &job.GenerationID, &job.PluginID, &job.RegistrationID, &job.PlanID, &registration.Version, &registration.Endpoint, &registration.ManifestDigest, &registration.Manifest, &settings, &recipe, &provenance, &generationSpaces, &modelSelection)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	registration.ID, registration.PluginID = job.RegistrationID, job.PluginID
	if err = json.Unmarshal(settings, &registration.Settings); err != nil {
		return err
	}
	pin, err := registration.Pin()
	if err != nil {
		return err
	}
	if recipe != "" {
		pin.IngestionDerivation = &plugins.IngestionDerivation{Recipe: recipe, Provenance: provenance}
	}
	var spaces []string
	// The accepted registration's primary keys survive later model upgrades.
	for _, space := range pin.EnabledSpaces() {
		if space.Role == plugins.SpaceServed {
			spaces = append(spaces, space.Key)
		}
	}
	// A model promotion may select another enabled space of this same
	// immutable registration. Snapshot that generation's effective primary.
	carried, err := scanSpaces(generationSpaces)
	if err != nil {
		return err
	}
	primary := (content.Generation{Spaces: carried}).ServedFor(job.PluginID)
	for _, space := range pin.EnabledSpaces() {
		if space.Key == primary {
			spaces = []string{primary}
			break
		}
	}
	if len(spaces) == 0 {
		return content.ErrInvalid
	}
	if primary == "" || !slices.Contains(spaces, primary) {
		return nil
	}
	job.ID = content.StableID("serving-projection", org, versionID, job.GenerationID, job.PlanID, strings.Join(spaces, ","), modelSelection)
	// A retry with the same effective target waits for generation compatibility.
	// A later model selection gets its own identity, even when a model returns.
	if len(supersededID) > 0 && job.ID == supersededID[0] {
		return ErrGenerationChanged
	}
	tag, err := tx.Exec(ctx, `INSERT INTO serving_projections(organization,id,record_id,version_id,generation_id,plugin_id,registration_id,plan_id,spaces,work_queue) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,COALESCE(NULLIF($10,''),(SELECT work_queue FROM ingestion_receipts WHERE organization=$1 AND version_id=$4 ORDER BY accepted_at LIMIT 1),'live')) ON CONFLICT DO NOTHING`, org, job.ID, job.RecordID, versionID, job.GenerationID, job.PluginID, job.RegistrationID, job.PlanID, spaces, selectedQueue(ctx))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO pipeline_plan_work(kind,organization,work_id,plan_id,ingestion_registration_id) VALUES('serving_projection',$1,$2,$3,$4)`, org, job.ID, job.PlanID, job.RegistrationID)
	return err
}

func queuePendingServingProjections(ctx context.Context, tx pgx.Tx, previous registry.Plan, next registry.Activation) error {
	old, err := planIngestionRouting(ctx, tx, previous.ID)
	if err != nil {
		return err
	}
	selected := next.Set.IngestionRouting()
	routing := content.IngestionRouting{Default: selected.Default, Routes: selected.Routes}
	rows, err := tx.Query(ctx, `SELECT v.organization,v.id,COALESCE(NULLIF(ar.source_media_type,''),'text/plain') FROM record_versions v JOIN records r ON (r.organization,r.desired_version_id)=(v.organization,v.id) JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot) WHERE NOT v.baseline_ready AND NOT v.quarantined ORDER BY v.organization,v.id`)
	if err != nil {
		return err
	}
	type version struct{ org, id, mediaType string }
	versions, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (v version, err error) { err = r.Scan(&v.org, &v.id, &v.mediaType); return })
	if err != nil {
		return err
	}
	for _, v := range versions {
		if old.For(v.mediaType) == routing.For(v.mediaType) {
			continue
		}
		if err = queueServingProjection(ctx, tx, v.org, v.id); err != nil {
			return err
		}
	}
	return nil
}

func (s ServingProjectionStore) ClaimServingProjections(ctx context.Context, limit int) ([]content.IngestionEvaluation, error) {
	q, _ := workqueue.Selected(ctx)
	rows, err := s.Pool.Query(ctx, `UPDATE serving_projections SET lease_until=now()+interval '5 seconds' WHERE (organization,id) IN (SELECT organization,id FROM serving_projections WHERE ($2='' OR work_queue=$2 OR (work_queue='' AND $2='live')) AND NOT dispatched AND state='queued' AND lease_until<now() ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT $1) RETURNING organization,id,record_id,version_id,generation_id,plugin_id,registration_id,plan_id,spaces,state,work_queue`, limit, q)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (content.IngestionEvaluation, error) { return scanIngestionEvaluation(r) })
}

func (s ServingProjectionStore) ServingProjection(ctx context.Context, org, id string) (content.IngestionEvaluation, error) {
	j, err := scanIngestionEvaluation(s.Pool.QueryRow(ctx, `SELECT organization,id,record_id,version_id,generation_id,plugin_id,registration_id,plan_id,spaces,state,work_queue FROM serving_projections WHERE organization=$1 AND id=$2`, org, id))
	return j, notFound(err)
}

func (s ServingProjectionStore) ServingProjectionDispatched(ctx context.Context, j content.IngestionEvaluation) error {
	_, err := s.Pool.Exec(ctx, `UPDATE serving_projections SET dispatched=true WHERE organization=$1 AND id=$2`, j.Organization, j.ID)
	return err
}

func servingProjectionEligible(ctx context.Context, q querier, j content.IngestionEvaluation) (bool, error) {
	var eligible bool
	err := q.QueryRow(ctx, `SELECT r.desired_version_id=v.id AND NOT r.withdrawn AND NOT v.quarantined AND NOT EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id)
 AND g.id=$3 AND EXISTS(SELECT 1 FROM jsonb_array_elements(g.spaces) sp WHERE sp->>'owner_plugin_id'=$4 AND sp->>'role'='served' AND sp->>'id'=ANY($5::text[])) AND COALESCE(g.ingestion_routing->'routes'->>COALESCE(NULLIF(ar.source_media_type,''),'text/plain'),g.ingestion_routing->>'default','')=$4
 FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)
 JOIN projection_generations g ON g.id=`+routedGenerationSQL("r.organization", "r.corpus_id")+` WHERE v.organization=$1 AND v.id=$2`, j.Organization, j.VersionID, j.GenerationID, j.PluginID, j.Spaces).Scan(&eligible)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return eligible, err
}

func (s ServingProjectionStore) ServingProjectionEligible(ctx context.Context, j content.IngestionEvaluation) (bool, error) {
	return servingProjectionEligible(ctx, s.Pool, j)
}

func (s ServingProjectionStore) CountServingProjectionTimeout(ctx context.Context, j content.IngestionEvaluation) (int, error) {
	var result0 int
	err := retryJournalWrite(ctx, "CountServingProjectionTimeout", func(ctx context.Context) error {
		var err error
		result0, err = s.countServingProjectionTimeoutAttempt(ctx, j)
		return err
	})
	return result0, err
}

func (s ServingProjectionStore) countServingProjectionTimeoutAttempt(ctx context.Context, j content.IngestionEvaluation) (int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, j.Organization); err != nil {
		return 0, err
	}
	eligible, err := servingProjectionEligible(ctx, tx, j)
	if err != nil {
		return 0, err
	}
	if !eligible {
		return 0, tx.Commit(ctx)
	}
	var count int
	if err = tx.QueryRow(ctx, `UPDATE serving_projections SET deadline_attempts=deadline_attempts+1 WHERE organization=$1 AND id=$2 RETURNING deadline_attempts`, j.Organization, j.ID).Scan(&count); err != nil {
		return 0, err
	}
	return count, tx.Commit(ctx)
}

func (s ServingProjectionStore) CompleteServingProjection(ctx context.Context, j content.IngestionEvaluation, state string, reason *content.Diagnostic) error {
	err := retryJournalWrite(ctx, "CompleteServingProjection", func(ctx context.Context) error {
		return s.completeServingProjectionAttempt(ctx, j, state, reason)
	})
	return err
}

func (s ServingProjectionStore) completeServingProjectionAttempt(ctx context.Context, j content.IngestionEvaluation, state string, reason *content.Diagnostic) error {
	if state != "succeeded" && state != "failed" && state != "skipped" {
		return content.ErrInvalid
	}
	var raw []byte
	var err error
	if reason != nil {
		if raw, err = json.Marshal(reason); err != nil {
			return err
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, j.Organization); err != nil {
		return err
	}
	eligible, err := servingProjectionEligible(ctx, tx, j)
	if err != nil {
		return err
	}
	if !eligible {
		state = "skipped"
		raw = nil
	}
	if state == "failed" && eligible && reason != nil {
		tag, err := tx.Exec(ctx, `UPDATE record_versions SET processing='blocked',error_code=$3,quarantined=true,quarantine=$4,quarantine_stage='ingestion',quarantined_at=`+firstStep("quarantined_at")+` WHERE organization=$1 AND id=$2 AND NOT baseline_ready AND NOT quarantined`, j.Organization, j.VersionID, reason.Code, raw)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			var corpusID string
			if err = tx.QueryRow(ctx, `SELECT corpus_id FROM records WHERE organization=$1 AND id=$2`, j.Organization, j.RecordID).Scan(&corpusID); err != nil {
				return err
			}
			if err = quarantinedEvent(ctx, tx, j.Organization, corpusID, j.RecordID, j.VersionID, reason.Code); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE serving_projections SET state=$3,diagnostic=$4,completed_at=now() WHERE organization=$1 AND id=$2 AND state='queued'`, j.Organization, j.ID, state, raw); err != nil {
		return err
	}
	if state == "skipped" {
		// A rebuild or rollback can move the pending Version again. A fresh
		// job gets the new route's own pin; this job's pin remains immutable.
		if err = queueServingProjection(ctx, tx, j.Organization, j.VersionID, j.ID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// CoverServingProjection publishes readiness and currentness only after the
// current routed owner has its own complete projection and primary vectors.
func (s ServingProjectionStore) CoverServingProjection(ctx context.Context, j content.IngestionEvaluation, g content.Generation, seg content.Segmentation, artifacts []content.Embedding) error {
	err := retryJournalWrite(ctx, "CoverServingProjection", func(ctx context.Context) error {
		return s.coverServingProjectionAttempt(ctx, j, g, seg, artifacts)
	})
	return err
}

func (s ServingProjectionStore) coverServingProjectionAttempt(ctx context.Context, j content.IngestionEvaluation, g content.Generation, seg content.Segmentation, artifacts []content.Embedding) error {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return err
	}
	hint, err := servingProjectionEligible(ctx, tx, j)
	if err != nil {
		return err
	}
	prepared := hint && len(artifacts) > 0
	if prepared {
		if seg.VersionID != j.VersionID || content.PluginOfRecipe(seg.Recipe) != j.PluginID || g.ID != j.GenerationID {
			return content.ErrInvalid
		}
		if _, err = prepareJournal(ctx, tx, func(stage pgx.Tx) error { return coverOwnerProjection(ctx, stage, j.Organization, g, seg, artifacts) }); err != nil {
			return err
		}
	}
	if err = lockJournal(ctx, tx, j.Organization); err != nil {
		return err
	}
	eligible, err := servingProjectionEligible(ctx, tx, j)
	if err != nil {
		return err
	}
	if !eligible {
		return nil
	}
	var carriesPrimary bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jsonb_array_elements(spaces) sp WHERE sp->>'owner_plugin_id'=$2 AND sp->>'role'='served' AND sp->>'id'=ANY($3::text[])) FROM projection_generations WHERE id=$1`, j.GenerationID, j.PluginID, j.Spaces).Scan(&carriesPrimary); err != nil {
		return err
	}
	if !carriesPrimary || len(j.Spaces) == 0 {
		return ErrGenerationChanged
	}
	if seg.VersionID != j.VersionID || content.PluginOfRecipe(seg.Recipe) != j.PluginID || g.ID != j.GenerationID || len(artifacts) == 0 {
		return content.ErrInvalid
	}
	if !prepared {
		return ErrGenerationChanged
	}
	var complete bool
	err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM segments sg CROSS JOIN unnest($4::text[]) sp(id) WHERE sg.organization=$1 AND sg.segmentation_id=$2 AND NOT EXISTS(SELECT 1 FROM embedding_coverage ec WHERE ec.organization=$1 AND ec.segment_id=sg.id AND ec.generation_id=$3 AND ec.space_id=sp.id))`, j.Organization, seg.ID, g.ID, j.Spaces).Scan(&complete)
	if err != nil {
		return err
	}
	if !complete {
		return content.ErrInvalid
	}
	if _, err = tx.Exec(ctx, `UPDATE projection_coverage SET role='evaluation' WHERE organization=$1 AND version_id=$2 AND generation_id=$3 AND role='served' AND plugin_id<>$4`, j.Organization, j.VersionID, g.ID, j.PluginID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE projection_coverage SET role='served' WHERE organization=$1 AND version_id=$2 AND generation_id=$3 AND plugin_id=$4`, j.Organization, j.VersionID, g.ID, j.PluginID); err != nil {
		return err
	}
	var ready, enriched bool
	if err = tx.QueryRow(ctx, `SELECT baseline_ready,enriched_at IS NOT NULL FROM record_versions WHERE organization=$1 AND id=$2`, j.Organization, j.VersionID).Scan(&ready, &enriched); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE record_versions SET baseline_ready=true,processing='idle',error_code='',retrieval_ready_at=`+firstStep("retrieval_ready_at")+`,enrichment_state='idle',enrichment_error='',enrichment_reason=NULL,enriched_at=`+firstStep("enriched_at")+` WHERE organization=$1 AND id=$2`, j.Organization, j.VersionID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE records SET current_version_id=$3 WHERE organization=$1 AND id=$2`, j.Organization, j.RecordID, j.VersionID); err != nil {
		return err
	}
	var corpusID string
	if err = tx.QueryRow(ctx, `SELECT corpus_id FROM records WHERE organization=$1 AND id=$2`, j.Organization, j.RecordID).Scan(&corpusID); err != nil {
		return err
	}
	if !ready {
		if err = appendEvent(ctx, tx, eventInput{Organization: j.Organization, CorpusID: corpusID, Kind: "record.retrieval_ready", Resource: "record", ResourceID: j.RecordID, MutationID: content.StableID("baseline", j.VersionID, g.ID), VersionID: j.VersionID}); err != nil {
			return err
		}
	}
	if !enriched {
		if err = appendEvent(ctx, tx, eventInput{Organization: j.Organization, CorpusID: corpusID, Kind: "record.enrichment_available", Resource: "record", ResourceID: j.RecordID, MutationID: content.StableID("enrichment", seg.ID, g.ID), VersionID: j.VersionID}); err != nil {
			return err
		}
	}
	if err = queueIngestionEvaluations(ctx, tx, j.Organization, j.RecordID, j.VersionID, g.ID, j.PlanID, j.PluginID); err != nil {
		return err
	}
	if err = observeQueueRecords(ctx, tx, []string{j.Organization}, []string{j.RecordID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ServingProjectionStore persists serving-owner projection jobs.
type ServingProjectionStore struct{ Pool *pgxpool.Pool }
