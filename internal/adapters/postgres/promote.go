package postgres

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/backfill"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/jackc/pgx/v5"
)

// carriesSQL reports whether generation g carries space $1 as a named vector.
const carriesSQL = `(g.spaces_projected AND g.spaces @> jsonb_build_array(jsonb_build_object('id',$1::text)))`

// promotionGapSQL measures, over every Corpus of the deployment, what space
// $1 does not cover in the Corpus's routed generation: the Corpora with a
// current segment that lacks a vector in it (every one, when the generation
// does not carry it), and those segments.
var promotionGapSQL = `WITH routed AS (
 SELECT c.organization,c.id AS corpus_id,g.id AS generation_id,` + carriesSQL + ` AS carries
 FROM corpora c JOIN projection_generations g ON g.id=` + routedGenerationSQL("c.organization", "c.id") + `
), gaps AS (
 SELECT rt.organization,rt.corpus_id FROM routed rt
 JOIN records r ON r.organization=rt.organization AND r.corpus_id=rt.corpus_id
 JOIN record_versions v ON (v.organization,v.id)=(r.organization,r.current_version_id)
 JOIN projection_coverage pc ON pc.organization=v.organization AND pc.version_id=v.id AND pc.generation_id=rt.generation_id
 JOIN segments sg ON sg.organization=v.organization AND sg.version_id=v.id AND sg.segmentation_id=pc.segmentation_id
 WHERE ` + eligibleVersionSQL + ` AND (NOT rt.carries OR NOT EXISTS(SELECT 1 FROM embedding_coverage ec WHERE ec.organization=sg.organization AND ec.segment_id=sg.id AND ec.generation_id=rt.generation_id AND ec.space_id=$1))
)
SELECT count(DISTINCT (organization,corpus_id)),count(*) FROM gaps`

// PromoteSpace makes a registered evaluation space the deployment's served
// space: see backfill.PromotionStore.
func (s ContentStore) PromoteSpace(ctx context.Context, space string, force bool) (backfill.Promotion, error) {
	out := backfill.Promotion{Served: space}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	// Space registrations at startup, activation and rollback wait for it.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, spacesLock); err != nil {
		return out, err
	}
	var role string
	if err = tx.QueryRow(ctx, `SELECT role FROM vector_spaces WHERE id=$1`, space).Scan(&role); err != nil {
		return out, notFound(err)
	}
	switch role {
	case content.SpaceServed:
		return out, tx.Commit(ctx)
	case content.SpaceEvaluation:
	default:
		return out, backfill.ErrNotEvaluation
	}
	err = tx.QueryRow(ctx, `SELECT id FROM vector_spaces WHERE role='served' ORDER BY id LIMIT 1`).Scan(&out.Previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if err = tx.QueryRow(ctx, promotionGapSQL, space).Scan(&out.CorporaIncomplete, &out.SegmentsMissing); err != nil {
		return out, err
	}
	if out.CorporaIncomplete > 0 && !force {
		return out, &backfill.IncompleteError{Promotion: out}
	}
	if _, err = tx.Exec(ctx, `UPDATE vector_spaces SET role=CASE WHEN id=$1 THEN 'served' ELSE 'evaluation' END WHERE id=$1 OR role='served'`, space); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO vector_space_promotions(served_space_id,previous_space_id) VALUES($1,$2)
ON CONFLICT(singleton) DO UPDATE SET served_space_id=EXCLUDED.served_space_id,previous_space_id=EXCLUDED.previous_space_id,promoted_at=now()`, space, out.Previous); err != nil {
		return out, err
	}
	// Every generation that carries the space and served the one it replaces
	// serves it from now on, the default one included, so new Corpora start
	// on it; promoting back switches exactly those again. Their other spaces
	// keep their vectors.
	tag, err := tx.Exec(ctx, `UPDATE projection_generations g SET space_id=$1,
 spaces=(SELECT jsonb_agg(e ORDER BY e->>'id'<>$1) FROM jsonb_array_elements(g.spaces) e)
WHERE `+carriesSQL+` AND g.space_id<>$1 AND ($2='' OR g.space_id=$2)`, space, out.Previous)
	if err != nil {
		return out, err
	}
	out.GenerationsSwitched = tag.RowsAffected()
	return out, tx.Commit(ctx)
}

// keepPromotion applies an operator's promotion to the spaces the deployment
// registers: the promoted space stays served, and the one it replaced stays
// for evaluation, while the ingestion plugin enables both and still serves
// the replaced one. Otherwise the plugin's roles decide again and the
// promotion is forgotten.
func keepPromotion(ctx context.Context, tx pgx.Tx, spaces []content.RegisteredSpace) ([]content.RegisteredSpace, error) {
	var served, previous string
	err := tx.QueryRow(ctx, `SELECT served_space_id,previous_space_id FROM vector_space_promotions`).Scan(&served, &previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return spaces, nil
	}
	if err != nil {
		return nil, err
	}
	roles := map[string]string{}
	for _, sp := range spaces {
		roles[sp.ID] = sp.Role
	}
	if roles[served] != content.SpaceEvaluation || roles[previous] != content.SpaceServed {
		_, err = tx.Exec(ctx, `DELETE FROM vector_space_promotions`)
		return spaces, err
	}
	out := make([]content.RegisteredSpace, len(spaces))
	for i, sp := range spaces {
		switch sp.ID {
		case served:
			sp.Role = content.SpaceServed
		case previous:
			sp.Role = content.SpaceEvaluation
		}
		out[i] = sp
	}
	return out, nil
}

var _ backfill.PromotionStore = ContentStore{}
