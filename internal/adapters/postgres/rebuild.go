package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/jackc/pgx/v5"
)

// rebuildGapSQL selects current eligible Versions of Corpus $2 that target
// generation $3 does not yet cover: no projection coverage, or a segment whose
// vectors the routed generation serves without target vector coverage. The
// same predicate lists work and validates activation, so they cannot drift.
// It requires aliases r and v and parameters $1 (organization), $2, $3.
var rebuildGapSQL = `r.organization=$1 AND r.corpus_id=$2 AND ` + eligibleVersionSQL + ` AND (
 NOT EXISTS(SELECT 1 FROM projection_coverage t WHERE t.organization=v.organization AND t.version_id=v.id AND t.generation_id=$3)
 OR EXISTS(SELECT 1 FROM embedding_coverage ec JOIN segments sg ON (sg.organization,sg.id)=(ec.organization,ec.segment_id)
  WHERE ec.organization=v.organization AND sg.version_id=v.id AND ec.generation_id<>$3
   AND ec.generation_id=` + routedGenerationSQL("r.organization", "r.corpus_id") + `
   AND NOT EXISTS(SELECT 1 FROM embedding_coverage te WHERE te.organization=ec.organization AND te.segment_id=ec.segment_id AND te.generation_id=$3)))`

const currentVersionsSQL = `records r JOIN record_versions v ON (v.organization,v.id)=(r.organization,r.current_version_id)`

func lockOperation(ctx context.Context, tx pgx.Tx, org, id string) (operations.Operation, error) {
	return scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE organization=$1 AND id=$2 FOR UPDATE`, org, id))
}

func (s ContentStore) BeginRebuild(ctx context.Context, org, id string) (retrieval.RebuildTarget, error) {
	var out retrieval.RebuildTarget
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
	if op.State == operations.StateQueued || op.State == operations.StateRunning {
		// An Operation already outranked by the routed generation can never
		// activate; it fails before building anything.
		rank, err := rankAgainstRoute(ctx, tx, op)
		if err != nil {
			return out, err
		}
		if rank.failure != nil {
			if err = failOperation(ctx, tx, op, *rank.failure); err != nil {
				return out, err
			}
			op.State = operations.StateFailed
			op.Errors = []operations.Error{*rank.failure}
			out.Operation = op
			return out, tx.Commit(ctx)
		}
	}
	if op.State == operations.StateQueued {
		if _, err = tx.Exec(ctx, `UPDATE operations SET state='running',updated_at=now() WHERE organization=$1 AND id=$2`, org, id); err != nil {
			return out, err
		}
		if err = operationEvent(ctx, tx, org, op.CorpusID, id, operations.StateRunning); err != nil {
			return out, err
		}
		op.State = operations.StateRunning
	}
	g := &out.Generation
	var cfg []byte
	if err = tx.QueryRow(ctx, `SELECT g.id,g.collection,g.profile_version,g.space_id,COALESCE(g.retrieval,c.retrieval) FROM projection_generations g, corpora c WHERE g.id=$1 AND c.organization=$2 AND c.id=$3`, op.TargetGenerationID, org, op.CorpusID).Scan(&g.ID, &g.Collection, &g.ProfileVersion, &g.SpaceID, &cfg); err != nil {
		return out, err
	}
	if g.Fields, err = retrievalFields(cfg); err != nil {
		return out, err
	}
	out.Operation = op
	return out, tx.Commit(ctx)
}

func (s ContentStore) RebuildCandidates(ctx context.Context, org, id string, limit int) ([]retrieval.RebuildCandidate, error) {
	op, err := s.Operation(ctx, org, id)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT r.id,v.id,EXISTS(SELECT 1 FROM embedding_coverage ec JOIN segments sg ON (sg.organization,sg.id)=(ec.organization,ec.segment_id) WHERE ec.organization=v.organization AND sg.version_id=v.id AND ec.generation_id=`+routedGenerationSQL("r.organization", "r.corpus_id")+`)
FROM `+currentVersionsSQL+` WHERE `+rebuildGapSQL+` ORDER BY v.id LIMIT $4`, org, op.CorpusID, op.TargetGenerationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []retrieval.RebuildCandidate{}
	for rows.Next() {
		var c retrieval.RebuildCandidate
		if err = rows.Scan(&c.RecordID, &c.VersionID, &c.VectorsRequired); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s ContentStore) CoverRebuild(ctx context.Context, org, id string, seg content.Segmentation, artifacts []content.Embedding) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return false, err
	}
	op, err := lockOperation(ctx, tx, org, id)
	if err != nil {
		return false, err
	}
	if op.State != operations.StateRunning {
		return false, operations.ErrNotRunning
	}
	var eligible bool
	err = tx.QueryRow(ctx, `SELECT `+eligibleVersionSQL+` AND r.corpus_id=$3 AND r.current_version_id=v.id FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2`, org, seg.VersionID, op.CorpusID).Scan(&eligible)
	if err != nil {
		return false, notFound(err)
	}
	if !eligible {
		// Withdrawn or superseded meanwhile; canonical hydration already hides it.
		return false, tx.Commit(ctx)
	}
	var digest string
	if err = tx.QueryRow(ctx, `SELECT digest FROM segmentations WHERE organization=$1 AND id=$2 AND version_id=$3`, org, seg.ID, seg.VersionID).Scan(&digest); err != nil {
		if err == pgx.ErrNoRows {
			return false, content.ErrConflict
		}
		return false, err
	}
	if digest != content.SegmentationDigest(seg) {
		return false, content.ErrConflict
	}
	var space string
	if err = tx.QueryRow(ctx, `SELECT space_id FROM projection_generations WHERE id=$1`, op.TargetGenerationID).Scan(&space); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO projection_coverage VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, org, seg.VersionID, op.TargetGenerationID, seg.ID)
	if err != nil {
		return false, err
	}
	indexed, reused := tag.RowsAffected(), int64(0)
	segments := map[string]bool{}
	for _, p := range seg.Segments {
		segments[p.ID] = true
	}
	for _, e := range artifacts {
		if e.Organization != org || !segments[e.SegmentID] || e.SpaceID != space {
			return false, content.ErrInvalid
		}
		var stored string
		if err = tx.QueryRow(ctx, `SELECT id FROM embedding_artifacts WHERE organization=$1 AND derivation_id=$2 AND segment_id=$3 AND space_id=$4`, org, e.DerivationID, e.SegmentID, space).Scan(&stored); err != nil {
			if err == pgx.ErrNoRows {
				return false, content.ErrConflict
			}
			return false, err
		}
		if stored != e.ID {
			return false, content.ErrConflict
		}
		tag, err = tx.Exec(ctx, `INSERT INTO embedding_coverage VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, org, e.SegmentID, op.TargetGenerationID, e.ID)
		if err != nil {
			return false, err
		}
		reused += tag.RowsAffected()
	}
	if _, err = tx.Exec(ctx, `UPDATE operations SET counters=jsonb_build_object('indexed',coalesce((counters->>'indexed')::bigint,0)+$3,'vectors_reused',coalesce((counters->>'vectors_reused')::bigint,0)+$4),updated_at=now() WHERE organization=$1 AND id=$2`, org, id, indexed, reused); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (s ContentStore) ActivateRebuild(ctx context.Context, org, id string) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	// Promotion and enrichment commits hold this lock too, so no coverage can
	// land on the prior generation between validation and the route switch.
	if err = lockJournal(ctx, tx, org); err != nil {
		return false, err
	}
	op, err := lockOperation(ctx, tx, org, id)
	if err != nil {
		return false, err
	}
	rank, err := rankAgainstRoute(ctx, tx, op)
	if err != nil {
		return false, err
	}
	result, _ := json.Marshal(map[string]string{"projection_generation_id": op.TargetGenerationID})
	if rank.routed == op.TargetGenerationID {
		// Recovery after activation: record the same target's outcome once.
		if op.State != operations.StateSucceeded {
			if err = s.succeed(ctx, tx, op, result); err != nil {
				return false, err
			}
		}
		return true, tx.Commit(ctx)
	}
	if op.State != operations.StateRunning {
		return false, operations.ErrNotRunning
	}
	// An outranked generation would revert the configuration or undo a later
	// request; it fails instead of activating.
	if rank.failure != nil {
		if err = failOperation(ctx, tx, op, *rank.failure); err != nil {
			return false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return false, err
		}
		return false, operations.ErrNotRunning
	}
	var gap bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+currentVersionsSQL+` WHERE `+rebuildGapSQL+`)`, org, op.CorpusID, op.TargetGenerationID).Scan(&gap); err != nil {
		return false, err
	}
	if gap {
		return false, tx.Commit(ctx)
	}
	// Compare-and-set on the generation read under the journal lock: a route
	// changed by any other writer aborts this activation instead of being
	// overwritten.
	tag, err := tx.Exec(ctx, `INSERT INTO corpus_projection_routes(organization,corpus_id,generation_id,acceptance_seq) VALUES($1,$2,$3,$4)
ON CONFLICT(organization,corpus_id) DO UPDATE SET generation_id=EXCLUDED.generation_id,acceptance_seq=EXCLUDED.acceptance_seq WHERE corpus_projection_routes.generation_id=$5`, org, op.CorpusID, op.TargetGenerationID, rank.sequence, rank.routed)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, errRouteChanged
	}
	if err = s.succeed(ctx, tx, op, result); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

var errRouteChanged = errors.New("projection route changed during activation")

// routeRank compares an Operation with the route currently serving its Corpus.
type routeRank struct {
	routed   string
	sequence int64
	failure  *operations.Error
}

// rankAgainstRoute orders an Operation against the routed generation by
// (retrieval configuration version, acceptance sequence): a newer
// configuration always outranks an older one, and with the same configuration
// the later-accepted Operation wins. Callers hold the Organization journal
// lock, which every route writer takes, so ranking and the route switch are
// serialized.
func rankAgainstRoute(ctx context.Context, tx pgx.Tx, op operations.Operation) (routeRank, error) {
	var rank routeRank
	var olderConfig, laterAccepted bool
	err := tx.QueryRow(ctx, `SELECT r.id,o.acceptance_seq,t.retrieval_version<r.retrieval_version,
 t.retrieval_version=r.retrieval_version AND o.acceptance_seq<COALESCE((SELECT cr.acceptance_seq FROM corpus_projection_routes cr WHERE cr.organization=o.organization AND cr.corpus_id=o.corpus_id AND cr.generation_id=r.id),0)
FROM operations o JOIN projection_generations t ON t.id=o.target_generation_id
JOIN projection_generations r ON r.id=`+routedGenerationSQL("o.organization", "o.corpus_id")+`
WHERE o.organization=$1 AND o.id=$2`, op.Organization, op.ID).Scan(&rank.routed, &rank.sequence, &olderConfig, &laterAccepted)
	if err != nil || rank.routed == op.TargetGenerationID {
		return rank, err
	}
	switch {
	case olderConfig:
		rank.failure = &operations.Error{Code: operations.ErrSuperseded.Error(), Message: "a newer retrieval configuration is already effective"}
	case laterAccepted:
		rank.failure = &operations.Error{Code: operations.ErrOperationSuperseded.Error(), Message: "a later-accepted Operation for this Corpus is already effective"}
	}
	return rank, nil
}

func (s ContentStore) succeed(ctx context.Context, tx pgx.Tx, op operations.Operation, result []byte) error {
	if _, err := tx.Exec(ctx, `UPDATE operations SET state='succeeded',result=$3,updated_at=now() WHERE organization=$1 AND id=$2`, op.Organization, op.ID, result); err != nil {
		return err
	}
	return operationEvent(ctx, tx, op.Organization, op.CorpusID, op.ID, operations.StateSucceeded)
}

func (s ContentStore) FailRebuild(ctx context.Context, org, id string, failure operations.Error) error {
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
	if op.State != operations.StateQueued && op.State != operations.StateRunning {
		return tx.Commit(ctx)
	}
	if err = failOperation(ctx, tx, op, failure); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func failOperation(ctx context.Context, tx pgx.Tx, op operations.Operation, failure operations.Error) error {
	errs, _ := json.Marshal([]operations.Error{failure})
	if _, err := tx.Exec(ctx, `UPDATE operations SET state='failed',errors=$3,updated_at=now() WHERE organization=$1 AND id=$2`, op.Organization, op.ID, errs); err != nil {
		return err
	}
	return operationEvent(ctx, tx, op.Organization, op.CorpusID, op.ID, operations.StateFailed)
}

var _ retrieval.RebuildStore = ContentStore{}
