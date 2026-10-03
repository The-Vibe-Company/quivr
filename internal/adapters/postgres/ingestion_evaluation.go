package postgres

import (
	"context"
	"encoding/json"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CoverEvaluation publishes coverage for another owner's independent segments.
// It never changes Version readiness, the current Version or enrichment state.
func (s IngestionEvaluationStore) CoverEvaluation(ctx context.Context, org string, g content.Generation, seg content.Segmentation, artifacts []content.Embedding) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	var eligible, routed bool
	err = tx.QueryRow(ctx, `SELECT r.current_version_id=v.id AND `+eligibleVersionSQL+`,`+routedGenerationSQL("r.organization", "r.corpus_id")+`=$3 FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2 FOR SHARE OF r,v`, org, seg.VersionID, g.ID).Scan(&eligible, &routed)
	if err != nil {
		return notFound(err)
	}
	if !eligible {
		return tx.Commit(ctx)
	}
	if !routed {
		return ErrGenerationChanged
	}
	if len(artifacts) == 0 {
		return content.ErrInvalid
	}
	if err = coverOwnerProjection(ctx, tx, org, g, seg, artifacts); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// coverOwnerProjection records an independent owner's coverage inside the
// caller's eligibility and operation transaction; it does not publish readiness.
func coverOwnerProjection(ctx context.Context, tx pgx.Tx, org string, g content.Generation, seg content.Segmentation, artifacts []content.Embedding) error {
	var err error
	var digest string
	if err = tx.QueryRow(ctx, `SELECT digest FROM segmentations WHERE organization=$1 AND id=$2 AND version_id=$3`, org, seg.ID, seg.VersionID).Scan(&digest); err != nil {
		return err
	}
	if digest != content.SegmentationDigest(seg) {
		return content.ErrConflict
	}
	segments := map[string]bool{}
	for _, p := range seg.Segments {
		segments[p.ID] = true
	}
	owner := content.PluginOfRecipe(seg.Recipe)
	if owner == "" {
		return content.ErrInvalid
	}
	for _, e := range artifacts {
		if e.Organization != org || e.VersionID != seg.VersionID || e.SegmentationID != seg.ID || !segments[e.SegmentID] || !g.Carries(e.SpaceID) {
			return content.ErrInvalid
		}
		var stored string
		if err = tx.QueryRow(ctx, `SELECT a.id FROM embedding_artifacts a JOIN vector_spaces vs ON vs.id=a.space_id WHERE a.organization=$1 AND a.derivation_id=$2 AND a.segment_id=$3 AND a.space_id=$4 AND vs.owner_plugin_id=$5`, org, e.DerivationID, e.SegmentID, e.SpaceID, owner).Scan(&stored); err != nil {
			return err
		}
		if stored != e.ID {
			return content.ErrConflict
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO projection_coverage(organization,version_id,generation_id,segmentation_id,role) VALUES($1,$2,$3,$4,'evaluation')
 ON CONFLICT(organization,version_id,generation_id,plugin_id) DO UPDATE SET segmentation_id=EXCLUDED.segmentation_id WHERE projection_coverage.role='evaluation' OR projection_coverage.segmentation_id=EXCLUDED.segmentation_id`, org, seg.VersionID, g.ID, seg.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return content.ErrConflict
	}
	for _, e := range artifacts {
		if _, err = tx.Exec(ctx, `INSERT INTO embedding_coverage(organization,segment_id,generation_id,artifact_id,space_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, org, e.SegmentID, g.ID, e.ID, e.SpaceID); err != nil {
			return err
		}
	}
	return nil
}

// PrepareEvaluation adds an owner's spaces to the routed generation before
// publication. The serving route is recorded without changing existing coverage.
func (s IngestionEvaluationStore) PrepareEvaluation(ctx context.Context, org, corpusID string, spaces []string) (content.Generation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return content.Generation{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return content.Generation{}, err
	}
	g, err := routedGeneration(ctx, tx, org, corpusID)
	if err != nil {
		return g, err
	}
	if !g.SpacesProjected {
		return g, content.ErrInvalid
	}
	g, err = carrySpaces(ctx, tx, g, spaces)
	if err != nil {
		return g, err
	}
	if err = recordGenerationIngestion(ctx, tx, g.ID, ""); err != nil {
		return g, err
	}
	if err = tx.Commit(ctx); err != nil {
		return g, err
	}
	return (ProjectionStore{Pool: s.Pool}).Generation(ctx, org, corpusID)
}

// recordGenerationIngestion captures the served work's pinned source routing.
// Existing recorded routes win; later optional dispatch cannot change them.
func recordGenerationIngestion(ctx context.Context, tx pgx.Tx, id, plan string) error {
	_, err := tx.Exec(ctx, `UPDATE projection_generations g SET ingestion_routing=jsonb_build_object('default',COALESCE(
 (SELECT pr.plugin_id FROM pipeline_plan_roles rr JOIN plugin_registrations pr ON pr.id=rr.registration_id WHERE rr.plan_id=COALESCE(NULLIF($2,''),(SELECT plan_id FROM active_pipeline_plan)) AND rr.role IN ('ingestion-default','ingestion') AND EXISTS(SELECT 1 FROM jsonb_array_elements(g.spaces) sp WHERE sp->>'owner_plugin_id'=pr.plugin_id AND sp->>'role'='served') LIMIT 1),
 (SELECT sp->>'owner_plugin_id' FROM jsonb_array_elements(g.spaces) sp WHERE sp->>'id'=g.space_id),''),
 'routes',COALESCE((SELECT jsonb_object_agg(substring(rr.role from 17),pr.plugin_id) FROM pipeline_plan_roles rr JOIN plugin_registrations pr ON pr.id=rr.registration_id WHERE rr.plan_id=COALESCE(NULLIF($2,''),(SELECT plan_id FROM active_pipeline_plan)) AND rr.role LIKE 'ingestion-route:%'), '{}'::jsonb)) WHERE g.id=$1 AND g.ingestion_routing IS NULL`, id, plan)
	return err
}

func loadGenerationIngestion(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, g *content.Generation) error {
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT ingestion_routing FROM projection_generations WHERE id=$1`, g.ID).Scan(&raw); err != nil {
		return err
	}
	if len(raw) > 0 {
		return json.Unmarshal(raw, &g.IngestionRouting)
	}
	return nil
}

// queueIngestionEvaluations fills the plan-selected source owner and evaluation
// owners other than the owner that just served this Corpus. Existing Corpora
// can retain another serving route after configuration changes. It makes no
// plugin calls and pins each optional job before the receipt releases.
func queueIngestionEvaluations(ctx context.Context, tx pgx.Tx, org, recordID, versionID, generationID, planID, servingOwner string) error {
	rows, err := tx.Query(ctx, `SELECT pr.id,pr.plugin_id,pr.version,pr.endpoint,pr.manifest_digest,pr.manifest,pr.settings FROM pipeline_plan_roles rr
 JOIN plugin_registrations pr ON pr.id=rr.registration_id
 JOIN record_versions v ON v.organization=$1 AND v.id=$2
 JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)
 WHERE rr.plan_id=COALESCE(NULLIF($3,''),(SELECT plan_id FROM active_pipeline_plan))
 AND pr.plugin_id<>$4 AND (rr.role='ingestion-evaluation:'||COALESCE(NULLIF(ar.source_media_type,''),'text/plain')||':'||pr.plugin_id
 OR rr.role='ingestion-route:'||COALESCE(NULLIF(ar.source_media_type,''),'text/plain')
 OR (rr.role IN ('ingestion-default','ingestion') AND NOT EXISTS(SELECT 1 FROM pipeline_plan_roles source_route WHERE source_route.plan_id=rr.plan_id AND source_route.role='ingestion-route:'||COALESCE(NULLIF(ar.source_media_type,''),'text/plain'))))
 ORDER BY pr.id`, org, versionID, planID, servingOwner)
	if err != nil {
		return err
	}
	type target struct {
		plugin, registration string
		spaces               []string
	}
	var targets []target
	for rows.Next() {
		var r registry.Registration
		var settings []byte
		if err = rows.Scan(&r.ID, &r.PluginID, &r.Version, &r.Endpoint, &r.ManifestDigest, &r.Manifest, &settings); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(settings, &r.Settings); err != nil {
			rows.Close()
			return err
		}
		pin, err := r.Pin()
		if err != nil {
			rows.Close()
			return err
		}
		t := target{plugin: r.PluginID, registration: r.ID}
		// Space keys and enabled roles belong to this immutable registration,
		// even after the live registry has retired its model versions.
		for _, space := range pin.EnabledSpaces() {
			t.spaces = append(t.spaces, space.Key)
		}
		if len(t.spaces) > 0 {
			targets = append(targets, t)
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	if planID == "" {
		if err = tx.QueryRow(ctx, `SELECT plan_id FROM active_pipeline_plan`).Scan(&planID); err != nil {
			return err
		}
	}
	if err = recordGenerationIngestion(ctx, tx, generationID, planID); err != nil {
		return err
	}
	for _, t := range targets {
		id := content.StableID("ingestion-evaluation", org, versionID, generationID, t.registration)
		tag, err := tx.Exec(ctx, `INSERT INTO ingestion_evaluations(organization,id,record_id,version_id,generation_id,plugin_id,registration_id,plan_id,spaces) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, org, id, recordID, versionID, generationID, t.plugin, t.registration, planID, t.spaces)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			if _, err = tx.Exec(ctx, `INSERT INTO pipeline_plan_work(kind,organization,work_id,plan_id,ingestion_registration_id) VALUES('evaluation',$1,$2,$3,$4) ON CONFLICT DO NOTHING`, org, id, planID, t.registration); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s IngestionEvaluationStore) ClaimIngestionEvaluations(ctx context.Context, limit int) ([]content.IngestionEvaluation, error) {
	rows, err := s.Pool.Query(ctx, `UPDATE ingestion_evaluations SET lease_until=now()+interval '5 seconds' WHERE (organization,id) IN (SELECT organization,id FROM ingestion_evaluations WHERE NOT dispatched AND state='queued' AND lease_until<now() ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT $1) RETURNING organization,id,record_id,version_id,generation_id,plugin_id,registration_id,plan_id,spaces,state`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []content.IngestionEvaluation
	for rows.Next() {
		j, err := scanIngestionEvaluation(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}
func scanIngestionEvaluation(row interface{ Scan(...any) error }) (j content.IngestionEvaluation, err error) {
	err = row.Scan(&j.Organization, &j.ID, &j.RecordID, &j.VersionID, &j.GenerationID, &j.PluginID, &j.RegistrationID, &j.PlanID, &j.Spaces, &j.State)
	return
}
func (s IngestionEvaluationStore) IngestionEvaluation(ctx context.Context, org, id string) (content.IngestionEvaluation, error) {
	j, err := scanIngestionEvaluation(s.Pool.QueryRow(ctx, `SELECT organization,id,record_id,version_id,generation_id,plugin_id,registration_id,plan_id,spaces,state FROM ingestion_evaluations WHERE organization=$1 AND id=$2`, org, id))
	return j, notFound(err)
}
func (s IngestionEvaluationStore) IngestionEvaluationDispatched(ctx context.Context, j content.IngestionEvaluation) error {
	_, err := s.Pool.Exec(ctx, `UPDATE ingestion_evaluations SET dispatched=true WHERE organization=$1 AND id=$2`, j.Organization, j.ID)
	return err
}
func (s IngestionEvaluationStore) CompleteIngestionEvaluation(ctx context.Context, org, id, state string, reason *content.Diagnostic) error {
	if state != "succeeded" && state != "failed" && state != "skipped" {
		return content.ErrInvalid
	}
	var raw []byte
	var err error
	if reason != nil {
		raw, err = json.Marshal(reason)
		if err != nil {
			return err
		}
	}
	_, err = s.Pool.Exec(ctx, `UPDATE ingestion_evaluations SET state=$3,diagnostic=$4,completed_at=now() WHERE organization=$1 AND id=$2 AND state='queued'`, org, id, state, raw)
	return err
}

// IngestionEvaluationStore persists evaluation-owner ingestion jobs.
type IngestionEvaluationStore struct{ Pool *pgxpool.Pool }
