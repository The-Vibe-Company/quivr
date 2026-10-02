package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/jackc/pgx/v5"
)

func (s ContentStore) Embedding(ctx context.Context, org, derivation string) (content.Embedding, error) {
	var e content.Embedding
	var b []byte
	err := s.Pool.QueryRow(ctx, `SELECT metadata FROM embedding_artifacts WHERE organization=$1 AND derivation_id=$2`, org, derivation).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &e)
	}
	return e, notFound(err)
}
func (s ContentStore) SaveEmbedding(ctx context.Context, e content.Embedding, space content.VectorSpace) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, e.Organization); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO vector_spaces VALUES($1,$2) ON CONFLICT DO NOTHING`, space.ID, space.Manifest); err != nil {
		return err
	}
	var same bool
	if err = tx.QueryRow(ctx, `SELECT manifest=$2::jsonb FROM vector_spaces WHERE id=$1`, space.ID, space.Manifest).Scan(&same); err != nil {
		return err
	}
	if !same {
		return content.ErrConflict
	}
	var prior string
	err = tx.QueryRow(ctx, `SELECT id FROM embedding_artifacts WHERE organization=$1 AND derivation_id=$2`, e.Organization, e.DerivationID).Scan(&prior)
	if err == nil {
		if prior != e.ID {
			return content.ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	b, _ := json.Marshal(e)
	_, err = tx.Exec(ctx, `INSERT INTO embedding_artifacts VALUES($1,$2,$3,$4,$5,$6)`, e.Organization, e.ID, e.DerivationID, e.SegmentID, e.SpaceID, b)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s ContentStore) EnrichmentProgress(ctx context.Context, org, id, state, code string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE record_versions SET enrichment_state=$3,enrichment_error=$4,enrichment_reason=NULL WHERE organization=$1 AND id=$2 AND baseline_ready AND NOT quarantined AND enrichment_state!='idle'`, org, id, state, code)
	return err
}

// BlockEnrichment stops an enrichment with its reason, under the guards of
// EnrichmentProgress: a searchable Version whose enrichment is not done.
func (s ContentStore) BlockEnrichment(ctx context.Context, org, id string, reason content.Diagnostic) error {
	raw, err := json.Marshal(reason)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE record_versions SET enrichment_state='blocked',enrichment_error=$3,enrichment_reason=$4 WHERE organization=$1 AND id=$2 AND baseline_ready AND NOT quarantined AND enrichment_state!='idle'`, org, id, reason.Code, raw)
	return err
}
func (s ContentStore) CountEnrichmentTimeout(ctx context.Context, org, id string) (int, error) {
	var timeouts int
	err := s.Pool.QueryRow(ctx, `INSERT INTO enrichment_timeouts(organization,version_id,timeouts) VALUES($1,$2,1)
ON CONFLICT(organization,version_id) DO UPDATE SET timeouts=enrichment_timeouts.timeouts+1,updated_at=now() RETURNING timeouts`, org, id).Scan(&timeouts)
	return timeouts, err
}
func (s ContentStore) EnrichmentEligible(ctx context.Context, org, id string) (bool, error) {
	var eligible bool
	err := s.Pool.QueryRow(ctx, `SELECT coalesce(r.current_version_id=v.id,false) AND `+eligibleVersionSQL+` FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2`, org, id).Scan(&eligible)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return eligible, err
}
func (s ContentStore) CommitEnrichment(ctx context.Context, org string, seg content.Segmentation, g content.Generation, artifacts []content.Embedding) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return err
	}
	var recordID, corpusID string
	var eligible bool
	err = tx.QueryRow(ctx, `SELECT r.id,r.corpus_id,`+eligibleVersionSQL+` FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2 FOR UPDATE OF r,v`, org, seg.VersionID).Scan(&recordID, &corpusID, &eligible)
	if err != nil {
		return err
	}
	if !eligible {
		return tx.Commit(ctx)
	}
	// Queue and snapshot before taking the generation's shared row lock:
	// the first evaluation snapshot may update that row. All effects still
	// become visible only with the successful served commit below.
	plan := ""
	if w, ok := plugins.WorkOf(ctx); ok {
		plan = w.Plan
	}
	if err = queueIngestionEvaluations(ctx, tx, org, recordID, seg.VersionID, g.ID, plan); err != nil {
		return err
	}
	var active bool
	var current content.Generation
	var spaces []byte
	if err = tx.QueryRow(ctx, `SELECT id=`+routedGenerationSQL("$2", "$3")+`,space_id,spaces FROM projection_generations WHERE id=$1 FOR SHARE`, g.ID, org, corpusID).Scan(&active, &current.SpaceID, &spaces); err != nil {
		return err
	}
	if current.Spaces, err = scanSpaces(spaces); err != nil {
		return err
	}
	owner := content.PluginOfRecipe(seg.Recipe)
	if !active || current.ServedFor(owner) != g.ServedFor(owner) {
		return ErrGenerationChanged
	}
	if len(artifacts) == 0 {
		return content.ErrInvalid
	}
	segments := map[string]bool{}
	for _, p := range seg.Segments {
		segments[p.ID] = true
	}
	for _, e := range artifacts {
		if e.Organization != org || !segments[e.SegmentID] || !g.Carries(e.SpaceID) {
			return content.ErrInvalid
		}
		var stored string
		if err = tx.QueryRow(ctx, `SELECT id FROM embedding_artifacts WHERE organization=$1 AND derivation_id=$2 AND segment_id=$3 AND space_id=$4`, org, e.DerivationID, e.SegmentID, e.SpaceID).Scan(&stored); err != nil {
			return err
		}
		if stored != e.ID {
			return content.ErrConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO embedding_coverage(organization,segment_id,generation_id,artifact_id,space_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, org, e.SegmentID, g.ID, e.ID, e.SpaceID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE record_versions SET enrichment_state='idle',enrichment_error='',enrichment_reason=NULL,enriched_at=`+firstStep("enriched_at")+` WHERE organization=$1 AND id=$2`, org, seg.VersionID); err != nil {
		return err
	}
	mutation := content.StableID("enrichment", seg.ID, g.ID)
	var emitted bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM change_events WHERE organization=$1 AND event_id=$2)`, org, content.StableID("event", org, "record.enrichment_available", "record", mutation)).Scan(&emitted); err != nil {
		return err
	}
	if !emitted {
		if err = appendEvent(ctx, tx, eventInput{Organization: org, CorpusID: corpusID, Kind: "record.enrichment_available", Resource: "record", ResourceID: recordID, MutationID: mutation, VersionID: seg.VersionID}); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

var _ content.EmbeddingRepository = ContentStore{}
