package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// rebuildGapSQL selects current eligible Versions of Corpus $2 that target
// generation $3 does not yet cover: no projection coverage, or a segment of
// the target's segmentation whose vectors the routed generation serves
// without target vector coverage. A target with another segmentation (another
// ingestion plugin or version) gets its vectors with its projection. The same
// predicate lists work and validates activation, so they cannot drift. It
// requires aliases r and v and parameters $1 (organization), $2, $3.
// Served coverage is unique; the scalar probe keeps the bounded candidate
// lookup from hashing every covered Version into an EXISTS subplan.
var rebuildGapSQL = `r.organization=$1 AND r.corpus_id=$2 AND ` + eligibleVersionSQL + ` AND (
 NOT COALESCE((SELECT true FROM projection_coverage t WHERE t.organization=v.organization AND t.version_id=v.id AND t.generation_id=$3 AND t.role='served'),false)
 OR EXISTS(SELECT 1 FROM segments sg JOIN LATERAL ` + embeddingCoverageForSegmentSQL("sg.organization", "sg.id") + ` ec ON true
  JOIN projection_coverage tc ON (tc.organization,tc.version_id,tc.segmentation_id,tc.generation_id)=(sg.organization,sg.version_id,sg.segmentation_id,$3) AND tc.role='served'
  WHERE sg.organization=v.organization AND sg.version_id=v.id AND ec.generation_id<>$3
   AND ec.generation_id=` + routedGenerationSQL("r.organization", "r.corpus_id") + `
   AND NOT EXISTS(SELECT 1 FROM ` + embeddingCoverageForSegmentSQL("ec.organization", "ec.segment_id") + ` te WHERE te.organization=ec.organization AND te.segment_id=ec.segment_id AND te.generation_id=$3)))`

const currentVersionsSQL = `records r JOIN record_versions v ON (v.organization,v.id)=(r.organization,r.current_version_id)`

func lockOperation(ctx context.Context, tx pgx.Tx, org, id string) (operations.Operation, error) {
	return scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE organization=$1 AND id=$2 FOR UPDATE`, org, id))
}

func (s RebuildStore) BeginRebuild(ctx context.Context, org, id string) (retrieval.RebuildTarget, error) {
	var result0 retrieval.RebuildTarget
	err := retryJournalWrite(ctx, "BeginRebuild", func(ctx context.Context) error {
		var err error
		result0, err = s.beginRebuildAttempt(ctx, org, id)
		return err
	})
	return result0, err
}

func (s RebuildStore) beginRebuildAttempt(ctx context.Context, org, id string) (retrieval.RebuildTarget, error) {
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
	// A target's source routing belongs to the same immutable plan as its
	// derivation. Record it before this generation can become a serving route.
	plan := ""
	if work, ok := plugins.WorkOf(ctx); ok {
		plan = work.Plan
	}
	if err = recordGenerationIngestion(ctx, tx, op.TargetGenerationID, plan); err != nil {
		return out, err
	}
	g := &out.Generation
	var cfg, spaces []byte
	if err = tx.QueryRow(ctx, `SELECT g.id,g.collection,g.profile_version,g.space_id,g.source_namespace_projected,g.spaces,g.spaces_projected,g.metadata_projected,g.item_keywords_projected,COALESCE(g.retrieval,c.retrieval) FROM projection_generations g, corpora c WHERE g.id=$1 AND c.organization=$2 AND c.id=$3`, op.TargetGenerationID, org, op.CorpusID).Scan(&g.ID, &g.Collection, &g.ProfileVersion, &g.SpaceID, &g.SourceNamespaceProjected, &spaces, &g.SpacesProjected, &g.MetadataProjected, &g.ItemKeywordsProjected, &cfg); err != nil {
		return out, err
	}
	if g.Spaces, err = scanSpaces(spaces); err != nil {
		return out, err
	}
	if g.Fields, err = retrievalFields(cfg); err != nil {
		return out, err
	}
	if err = loadGenerationIngestion(ctx, tx, g); err != nil {
		return out, err
	}
	out.Operation = op
	if err = tx.QueryRow(ctx, `SELECT rebuild_cursor FROM operations WHERE organization=$1 AND id=$2`, org, id).Scan(&out.Cursor); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s RebuildStore) RebuildCandidates(ctx context.Context, org, id string, limit int) ([]retrieval.RebuildCandidate, error) {
	var corpusID, generationID, after string
	if err := s.Pool.QueryRow(ctx, `SELECT corpus_id,target_generation_id,rebuild_cursor FROM operations WHERE organization=$1 AND id=$2`, org, id).Scan(&corpusID, &generationID, &after); err != nil {
		return nil, notFound(err)
	}
	out, err := s.rebuildCandidatesAfter(ctx, org, corpusID, generationID, after, limit)
	if err != nil || len(out) > 0 || after == "" {
		return out, err
	}
	// A final full gap sweep catches promotions and enrichment behind the cursor.
	// Activation independently checks again under its canonical cutover fence.
	return s.rebuildCandidatesAfter(ctx, org, corpusID, generationID, "", limit)
}

// RebuildCandidatesAfter advances only the dispatch scan. Persisted progress
// and the final gap sweep remain owned by RebuildCandidates/CheckpointRebuild.
func (s RebuildStore) RebuildCandidatesAfter(ctx context.Context, org, id, after string, limit int) ([]retrieval.RebuildCandidate, error) {
	var corpusID, generationID string
	if err := s.Pool.QueryRow(ctx, `SELECT corpus_id,target_generation_id FROM operations WHERE organization=$1 AND id=$2`, org, id).Scan(&corpusID, &generationID); err != nil {
		return nil, notFound(err)
	}
	return s.rebuildCandidatesAfter(ctx, org, corpusID, generationID, after, limit)
}

// RebuildCandidatePending distinguishes withdrawn content from a still-eligible
// unreadable Version without depending on page size or dispatch progress.
func (s RebuildStore) RebuildCandidatePending(ctx context.Context, org, id, version string) (bool, error) {
	var corpusID, generationID string
	if err := s.Pool.QueryRow(ctx, `SELECT corpus_id,target_generation_id FROM operations WHERE organization=$1 AND id=$2`, org, id).Scan(&corpusID, &generationID); err != nil {
		return false, notFound(err)
	}
	var pending bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+currentVersionsSQL+` WHERE `+rebuildGapSQL+` AND v.id=$4)`, org, corpusID, generationID, version).Scan(&pending)
	return pending, err
}

var rebuildCandidatesSQL = `SELECT r.id,v.id,r.namespace,EXISTS(SELECT 1 FROM segments sg JOIN LATERAL ` + embeddingCoverageForSegmentSQL("sg.organization", "sg.id") + ` ec ON true JOIN projection_generations rg ON rg.id=ec.generation_id JOIN projection_generations tg ON tg.id=$3 WHERE sg.organization=v.organization AND sg.version_id=v.id AND ec.space_id=tg.space_id AND rg.space_id=tg.space_id AND ec.generation_id=` + routedGenerationSQL("r.organization", "r.corpus_id") + `)
FROM ` + currentVersionsSQL + ` WHERE ` + rebuildGapSQL + ` AND r.current_version_id > $5 AND v.id > $5 ORDER BY r.current_version_id LIMIT $4`

func (s RebuildStore) rebuildCandidatesAfter(ctx context.Context, org, corpusID, generationID, after string, limit int) ([]retrieval.RebuildCandidate, error) {
	// The current-version key gives the corpus index both the range and ordering.
	rows, err := s.Pool.Query(ctx, rebuildCandidatesSQL, org, corpusID, generationID, limit, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []retrieval.RebuildCandidate{}
	for rows.Next() {
		var c retrieval.RebuildCandidate
		if err = rows.Scan(&c.RecordID, &c.VersionID, &c.SourceNamespace, &c.VectorsRequired); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s RebuildStore) CoverRebuild(ctx context.Context, org, id string, seg content.Segmentation, artifacts []content.Embedding) (bool, error) {
	var result0 bool
	err := retryJournalWrite(ctx, "CoverRebuild", func(ctx context.Context) error {
		var err error
		result0, err = s.coverRebuildAttempt(ctx, org, id, seg, artifacts)
		return err
	})
	return result0, err
}

func (s RebuildStore) coverRebuildAttempt(ctx context.Context, org, id string, seg content.Segmentation, artifacts []content.Embedding) (bool, error) {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return false, err
	}
	var hintOp operations.Operation
	hintOp, err = scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE organization=$1 AND id=$2`, org, id))
	if err != nil {
		return false, err
	}
	var hint bool
	if err = tx.QueryRow(ctx, `SELECT `+eligibleVersionSQL+` AND r.corpus_id=$3 AND r.current_version_id=v.id FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2`, org, seg.VersionID, hintOp.CorpusID).Scan(&hint); err != nil {
		return false, notFound(err)
	}
	prepared := hint && hintOp.State == operations.StateRunning
	var indexed, reused int64
	if prepared {
		if _, err = prepareJournal(ctx, tx, func(tx pgx.Tx) error {
			op := hintOp
			var digest string
			if err = tx.QueryRow(ctx, `SELECT digest FROM segmentations WHERE organization=$1 AND id=$2 AND version_id=$3`, org, seg.ID, seg.VersionID).Scan(&digest); err != nil {
				if err == pgx.ErrNoRows {
					return content.ErrConflict
				}
				return err
			}
			if digest != content.SegmentationDigest(seg) {
				return content.ErrConflict
			}
			target := content.Generation{ID: op.TargetGenerationID}
			var spaces []byte
			if err = tx.QueryRow(ctx, `SELECT space_id,spaces,spaces_projected FROM projection_generations WHERE id=$1`, op.TargetGenerationID).Scan(&target.SpaceID, &spaces, &target.SpacesProjected); err != nil {
				return err
			}
			if target.Spaces, err = scanSpaces(spaces); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `INSERT INTO projection_coverage(organization,version_id,generation_id,segmentation_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, org, seg.VersionID, op.TargetGenerationID, seg.ID)
			if err != nil {
				return err
			}
			indexed = tag.RowsAffected()
			reused, err = insertEmbeddingCoverage(ctx, tx, org, seg, target, artifacts)
			if err == pgx.ErrNoRows {
				return content.ErrConflict
			}
			return err
		}); err != nil {
			return false, err
		}
	}
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
		return false, nil
	}
	if !prepared {
		return false, ErrGenerationChanged
	}
	// Coverage insert deltas count Versions and passage/space entries separately.
	if _, err = tx.Exec(ctx, `UPDATE operations SET counters=counters || jsonb_build_object(
 'versions_covered',coalesce((counters->>'versions_covered')::bigint,0)+$3,
 'passages_covered',coalesce((counters->>'passages_covered')::bigint,0)+$4),
 updated_at=now() WHERE organization=$1 AND id=$2`, org, id, indexed, reused); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (s RebuildStore) ActivateRebuild(ctx context.Context, org, id string) (bool, error) {
	var result0 bool
	err := retryJournalWrite(ctx, "ActivateRebuild", func(ctx context.Context) error {
		var err error
		result0, err = s.activateRebuildAttempt(ctx, org, id)
		return err
	})
	return result0, err
}

func (s RebuildStore) activateRebuildAttempt(ctx context.Context, org, id string) (bool, error) {
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
			if err = succeed(ctx, tx, op, result); err != nil {
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
	// An owner cutover cannot be undone by an older already-pinned rebuild.
	// Its target routing and its served projections must still agree with the
	// authoritative active plan before the new generation becomes visible.
	// Configuration replacement intentionally retains old Corpus routes until
	// a fresh rebuild moves them onto the new owner's prepared projections.
	var activePlan string
	if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT plan_id FROM active_pipeline_plan),'')`).Scan(&activePlan); err != nil {
		return false, err
	}
	routing, err := planIngestionRouting(ctx, tx, activePlan)
	if err != nil {
		return false, err
	}
	expected, err := json.Marshal(routing)
	if err != nil {
		return false, err
	}
	var compatible bool
	if err = tx.QueryRow(ctx, `SELECT t.ingestion_routing IS NOT NULL AND ($4::jsonb->>'default'='' OR (COALESCE(t.ingestion_routing->>'default','')=COALESCE($4::jsonb->>'default','') AND COALESCE(t.ingestion_routing->'routes','{}'::jsonb)=COALESCE($4::jsonb->'routes','{}'::jsonb)))
 AND NOT EXISTS(SELECT 1 FROM records rec JOIN record_versions v ON (v.organization,v.id)=(rec.organization,rec.current_version_id)
 JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)
 JOIN projection_coverage pc ON (pc.organization,pc.version_id,pc.generation_id)=(v.organization,v.id,t.id) AND pc.role='served'
 WHERE rec.organization=$1 AND rec.corpus_id=$2 AND $4::jsonb->>'default'<>'' AND pc.plugin_id<>COALESCE(t.ingestion_routing->'routes'->>COALESCE(NULLIF(ar.source_media_type,''),'text/plain'),t.ingestion_routing->>'default',''))
 FROM projection_generations t WHERE t.id=$3`, org, op.CorpusID, op.TargetGenerationID, expected).Scan(&compatible); err != nil {
		return false, err
	}
	if !compatible {
		if err = failOperation(ctx, tx, op, operations.Error{Code: "unsupported_vector_space", Message: "the rebuilt generation no longer matches the serving ingestion routing"}); err != nil {
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
	if err = succeed(ctx, tx, op, result); err != nil {
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

func succeed(ctx context.Context, tx pgx.Tx, op operations.Operation, result []byte) error {
	if _, err := tx.Exec(ctx, `UPDATE operations SET state='succeeded',result=$3,updated_at=now() WHERE organization=$1 AND id=$2`, op.Organization, op.ID, result); err != nil {
		return err
	}
	return operationEvent(ctx, tx, op.Organization, op.CorpusID, op.ID, operations.StateSucceeded)
}

func (s RebuildStore) FailRebuild(ctx context.Context, org, id string, failure operations.Error) error {
	err := retryJournalWrite(ctx, "FailRebuild", func(ctx context.Context) error {
		return s.failRebuildAttempt(ctx, org, id, failure)
	})
	return err
}

func (s RebuildStore) failRebuildAttempt(ctx context.Context, org, id string, failure operations.Error) error {
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

var _ retrieval.RebuildStore = RebuildStore{}

// RebuildStore persists rebuild state.
type RebuildStore struct{ Pool *pgxpool.Pool }

// CheckpointRebuild persists an exclusive position below unfinished work.
// A reconciliation scan may restart below the old cursor.
func (s RebuildStore) CheckpointRebuild(ctx context.Context, org, id, after string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE operations SET rebuild_cursor=$3,updated_at=now() WHERE organization=$1 AND id=$2 AND state='running'`, org, id, after)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return operations.ErrNotRunning
	}
	return nil
}
