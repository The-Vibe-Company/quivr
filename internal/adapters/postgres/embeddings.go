package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (s EmbeddingStore) Embedding(ctx context.Context, org, derivation string) (content.Embedding, error) {
	var e content.Embedding
	var b []byte
	err := s.Pool.QueryRow(ctx, `SELECT metadata FROM `+embeddingArtifactsRelation+` WHERE organization=$1 AND derivation_id=$2`, org, derivation).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &e)
	}
	return e, notFound(err)
}
func (s EmbeddingStore) LegacyEmbedding(ctx context.Context, org, derivation string) (content.Embedding, error) {
	var e content.Embedding
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT metadata FROM embedding_artifacts WHERE organization=$1 AND derivation_id=$2`, org, derivation).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &e)
	}
	return e, notFound(err)
}
func (s EmbeddingStore) SaveEmbedding(ctx context.Context, e content.Embedding, space content.VectorSpace) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockProcessingVersion(ctx, tx, e.Organization, e.VersionID); err != nil {
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
	err = tx.QueryRow(ctx, `SELECT id FROM `+embeddingArtifactsRelation+` WHERE organization=$1 AND derivation_id=$2`, e.Organization, e.DerivationID).Scan(&prior)
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

// Only withdrawn work can settle without publishing its enrichment. The
// quarantine guard preserves its terminal diagnostics.
var settleWithdrawnEnrichmentSQL = `UPDATE record_versions v SET enrichment_state='idle',enrichment_error='',enrichment_reason=NULL
FROM records r WHERE (r.organization,r.id)=(v.organization,v.record_id) AND v.organization=$1 AND v.id=$2 AND v.baseline_ready AND NOT v.quarantined AND ` + recordGoneSQL

func (s EmbeddingStore) EnrichmentProgress(ctx context.Context, org, id, state, code string) error {
	err := retryJournalWrite(ctx, "EnrichmentProgress", func(ctx context.Context) error {
		return s.enrichmentProgressAttempt(ctx, org, id, state, code)
	})
	return err
}

func (s EmbeddingStore) enrichmentProgressAttempt(ctx context.Context, org, id, state, code string) error {
	if state == "idle" {
		// Withdrawn work settles even if its pinned owner stopped serving.
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = lockJournal(ctx, tx, org); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, settleWithdrawnEnrichmentSQL, org, id); err != nil {
			return err
		}
		if err = observeQueueVersion(ctx, tx, org, id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	return updatePinnedVersion(ctx, s.Pool, org, id, `UPDATE record_versions SET enrichment_state=$3,enrichment_error=$4,enrichment_reason=NULL WHERE organization=$1 AND id=$2 AND baseline_ready AND NOT quarantined AND enrichment_state!='idle'`, state, code)
}

// BlockEnrichment stops an enrichment with its reason, under the guards of
// EnrichmentProgress: a searchable Version whose enrichment is not done.
func (s EmbeddingStore) BlockEnrichment(ctx context.Context, org, id string, reason content.Diagnostic) error {
	raw, err := json.Marshal(reason)
	if err != nil {
		return err
	}
	if reason.Code == content.CodeRebuildRequired {
		tx, err := database(ctx, s.Pool).Begin(ctx)
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
			if _, err = tx.Exec(ctx, `UPDATE record_versions SET enrichment_state='blocked',enrichment_error=$3,enrichment_reason=$4 WHERE organization=$1 AND id=$2 AND baseline_ready AND NOT quarantined AND enrichment_state!='idle'`, org, id, reason.Code, raw); err != nil {
				return err
			}
			if err = queueServingProjection(ctx, tx, org, id); err != nil {
				return err
			}
			if err = observeQueueVersion(ctx, tx, org, id); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}
	return updatePinnedVersion(ctx, s.Pool, org, id, `UPDATE record_versions SET enrichment_state='blocked',enrichment_error=$3,enrichment_reason=$4 WHERE organization=$1 AND id=$2 AND baseline_ready AND NOT quarantined AND enrichment_state!='idle'`, reason.Code, raw)
}

// ReconcileServingEnrichment prevents historical cuts from becoming vectors
// of a generation whose canonical serving coverage uses another recipe.
func (s EmbeddingStore) ReconcileServingEnrichment(ctx context.Context, org string, seg content.Segmentation, g content.Generation) (bool, error) {
	owner := content.PluginOfRecipe(seg.Recipe)
	if g.IngestionRouting == nil || g.ServedFor(owner) == "" {
		return true, nil
	}
	var current string
	err := database(ctx, s.Pool).QueryRow(ctx, `SELECT COALESCE((SELECT segmentation_id FROM projection_coverage WHERE organization=$1 AND version_id=$2 AND generation_id=$3 AND plugin_id=$4 AND role='served'),'')`, org, seg.VersionID, g.ID, owner).Scan(&current)
	if err != nil || current == seg.ID {
		return err == nil, err
	}
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return false, err
	}
	var eligible, active, complete bool
	err = readJournal(ctx, tx, org, `SELECT `+eligibleVersionSQL+` AND r.current_version_id=v.id,
 $3=`+routedGenerationSQL("r.organization", "r.corpus_id")+`, `+servedVectorsCompleteSQL+`,
 COALESCE((SELECT segmentation_id FROM projection_coverage WHERE organization=$1 AND version_id=$2 AND generation_id=$3 AND plugin_id=$4 AND role='served'),'')
 FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 WHERE v.organization=$1 AND v.id=$2 FOR UPDATE OF r,v`, []any{org, seg.VersionID, g.ID, owner}, &eligible, &active, &complete, &current)
	if err != nil {
		return false, err
	}
	if !active {
		return false, ErrGenerationChanged
	}
	if !eligible {
		if _, err = tx.Exec(ctx, settleWithdrawnEnrichmentSQL, org, seg.VersionID); err != nil {
			return false, err
		}
		if err = observeQueueVersion(ctx, tx, org, seg.VersionID); err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}
	if current == seg.ID {
		return true, tx.Commit(ctx)
	}
	if complete {
		if _, err = tx.Exec(ctx, `UPDATE record_versions SET enrichment_state='idle',enrichment_error='',enrichment_reason=NULL,enriched_at=`+firstStep("enriched_at")+` WHERE organization=$1 AND id=$2`, org, seg.VersionID); err != nil {
			return false, err
		}
	} else {
		reason, _ := json.Marshal(content.Diagnostic{Code: content.CodeRebuildRequired, Message: "The serving generation uses another segmentation; its current recipe will finish in an independently pinned projection."})
		if _, err = tx.Exec(ctx, `UPDATE record_versions SET enrichment_state='blocked',enrichment_error=$3,enrichment_reason=$4 WHERE organization=$1 AND id=$2`, org, seg.VersionID, content.CodeRebuildRequired, reason); err != nil {
			return false, err
		}
		if err = queueServingProjection(ctx, tx, org, seg.VersionID); err != nil {
			return false, err
		}
	}
	if err = observeQueueVersion(ctx, tx, org, seg.VersionID); err != nil {
		return false, err
	}
	return false, tx.Commit(ctx)
}
func (s EmbeddingStore) CountEnrichmentTimeout(ctx context.Context, org, id string) (int, error) {
	var result0 int
	err := retryJournalWrite(ctx, "CountEnrichmentTimeout", func(ctx context.Context) error {
		var err error
		result0, err = s.countEnrichmentTimeoutAttempt(ctx, org, id)
		return err
	})
	return result0, err
}

func (s EmbeddingStore) countEnrichmentTimeoutAttempt(ctx context.Context, org, id string) (int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = lockJournal(ctx, tx, org); err != nil {
		return 0, err
	}
	serves, err := pinnedOwnerServes(ctx, tx, org, id)
	if err != nil {
		return 0, err
	}
	if !serves {
		return 0, tx.Commit(ctx)
	}
	var timeouts int
	err = tx.QueryRow(ctx, `INSERT INTO enrichment_timeouts(organization,version_id,timeouts) VALUES($1,$2,1)
ON CONFLICT(organization,version_id) DO UPDATE SET timeouts=enrichment_timeouts.timeouts+1,updated_at=now() RETURNING timeouts`, org, id).Scan(&timeouts)
	if err != nil {
		return 0, err
	}
	return timeouts, tx.Commit(ctx)
}
func (s EmbeddingStore) EnrichmentEligible(ctx context.Context, org, id string) (bool, error) {
	var eligible bool
	err := s.Pool.QueryRow(ctx, `SELECT coalesce(r.current_version_id=v.id,false) AND `+eligibleVersionSQL+` FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2`, org, id).Scan(&eligible)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return eligible, err
}
func (s EmbeddingStore) CommitEnrichment(ctx context.Context, org string, seg content.Segmentation, g content.Generation, artifacts []content.Embedding) error {
	if content.IngestionBatchActive(ctx) {
		var record string
		if err := s.Pool.QueryRow(ctx, `SELECT record_id FROM record_versions WHERE organization=$1 AND id=$2`, org, seg.VersionID).Scan(&record); err != nil {
			return err
		}
		if handled, err := content.EnqueueIngestion(ctx, content.IngestionCommit{Kind: content.CommitVectors, Organization: org, RecordID: record, Segmentation: seg, Generation: g, Artifacts: artifacts}); handled {
			return err
		}
	}
	err := retryJournalWrite(ctx, "CommitEnrichment", func(ctx context.Context) error {
		return s.commitEnrichmentAttempt(ctx, org, seg, g, artifacts)
	})
	return err
}

func (s EmbeddingStore) commitEnrichmentAttempt(ctx context.Context, org string, seg content.Segmentation, g content.Generation, artifacts []content.Embedding) error {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	finish, err := prepareEnrichment(ctx, tx, org, seg, g, artifacts)
	if err != nil {
		return err
	}
	if finish == nil {
		return nil
	}
	if err = finish(); err != nil {
		if errors.Is(err, errJournalReplay) {
			return nil
		}
		return err
	}
	return tx.Commit(ctx)
}

func prepareEnrichment(ctx context.Context, tx pgx.Tx, org string, seg content.Segmentation, g content.Generation, artifacts []content.Embedding) (journalFinish, error) {
	var err error

	var recordID, corpusID string
	var eligible bool
	var routing []byte
	var sourceMediaType *string
	guard := `SELECT r.id,r.corpus_id,` + eligibleVersionSQL + `,
 (SELECT ingestion_routing FROM projection_generations WHERE id=$3),
 (SELECT coalesce(nullif(ar.source_media_type,''),'text/plain') FROM accepted_revisions ar WHERE (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot))
 FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 WHERE v.organization=$1 AND v.id=$2`
	args := []any{org, seg.VersionID, g.ID}
	read := func(fence bool) error {
		if fence {
			return readJournal(ctx, tx, org, guard+" FOR UPDATE OF r,v", args, &recordID, &corpusID, &eligible, &routing, &sourceMediaType)
		}
		return tx.QueryRow(ctx, guard, args...).Scan(&recordID, &corpusID, &eligible, &routing, &sourceMediaType)
	}
	if err = lockProjectionRouting(ctx, tx); err != nil {
		return nil, err
	}
	if err = read(false); err != nil {
		return nil, err
	}
	if len(routing) > 0 {
		if err = json.Unmarshal(routing, &g.IngestionRouting); err != nil {
			return nil, err
		}
	}
	ownerPrepared := sourceMediaType != nil && g.IngestionRouting != nil && g.IngestionRouting.For(*sourceMediaType) != "" && g.IngestionRouting.For(*sourceMediaType) != content.PluginOfRecipe(seg.Recipe)
	var stage pgx.Tx
	if eligible && len(artifacts) > 0 {
		stage, err = prepareJournal(ctx, tx, func(stage pgx.Tx) error {
			if ownerPrepared {
				return coverOwnerProjection(ctx, stage, org, g, seg, artifacts)
			}
			_, err := insertEmbeddingCoverage(ctx, stage, org, seg, g, artifacts)
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	// Queue every group evaluation after the journal fence, before any member
	// takes the generation's shared row lock: snapshotting can update that row.
	// This retains routing -> journal -> mutable-row lock ordering.
	guarded, evaluationsPrepared := false, false
	if group := journalGroupOf(ctx); group != nil && eligible && !ownerPrepared && stage != nil {
		group.beforeFinishes = append(group.beforeFinishes, func() error {
			if err := read(true); err != nil {
				return err
			}
			if !eligible {
				return ErrGenerationChanged
			}
			plan := ""
			if w, ok := plugins.WorkOf(ctx); ok {
				plan = w.Plan
			}
			if err := queueIngestionEvaluations(ctx, tx, org, recordID, seg.VersionID, g.ID, plan, content.PluginOfRecipe(seg.Recipe)); err != nil {
				return err
			}
			guarded, evaluationsPrepared = true, true
			return nil
		})
	}
	return func() error {
		if !guarded {
			if err = read(true); err != nil {
				return err
			}
		}
		if !eligible {
			if stage != nil {
				if journalGroupOf(ctx) != nil {
					return ErrGenerationChanged
				}
				if err = stage.Rollback(ctx); err != nil {
					return err
				}
				if err = read(true); err != nil {
					return err
				}
			}
			if eligible {
				return ErrGenerationChanged
			}
			if _, err = tx.Exec(ctx, settleWithdrawnEnrichmentSQL, org, seg.VersionID); err != nil {
				return err
			}
			return nil
		}
		if len(routing) > 0 {
			if err = json.Unmarshal(routing, &g.IngestionRouting); err != nil {
				return err
			}
		}
		if sourceMediaType == nil {
			return pgx.ErrNoRows
		}
		if g.IngestionRouting != nil && g.IngestionRouting.For(*sourceMediaType) != "" && g.IngestionRouting.For(*sourceMediaType) != content.PluginOfRecipe(seg.Recipe) {
			var routed bool
			if err = tx.QueryRow(ctx, `SELECT `+routedGenerationSQL("$1", "$2")+`=$3`, org, corpusID, g.ID).Scan(&routed); err != nil {
				return err
			}
			if !routed {
				return ErrGenerationChanged
			}
			if len(artifacts) == 0 {
				return content.ErrInvalid
			}
			if !ownerPrepared || stage == nil {
				return ErrGenerationChanged
			}
			return nil
		}
		// Queue and snapshot before taking the generation's shared row lock:
		// the first evaluation snapshot may update that row. All effects still
		// become visible only with the successful served commit below.
		plan := ""
		if w, ok := plugins.WorkOf(ctx); ok {
			plan = w.Plan
		}
		if !evaluationsPrepared {
			if err = queueIngestionEvaluations(ctx, tx, org, recordID, seg.VersionID, g.ID, plan, content.PluginOfRecipe(seg.Recipe)); err != nil {
				return err
			}
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
		if stage == nil {
			// Eligibility changed after the speculative read. Let the caller repeat
			// preparation without doing hash inserts while holding the journal.
			if journalGroupOf(ctx) != nil {
				return ErrGenerationChanged
			}
			if err = tx.Rollback(ctx); err != nil {
				return err
			}
			return ErrGenerationChanged
		}
		writes := &pgx.Batch{}
		writes.Queue(`UPDATE record_versions SET enrichment_state='idle',enrichment_error='',enrichment_reason=NULL,enriched_at=`+firstStep("enriched_at")+` WHERE organization=$1 AND id=$2`, org, seg.VersionID)
		mutation := content.StableID("enrichment", seg.ID, g.ID)
		var emitted bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM change_events WHERE organization=$1 AND event_id=$2)`, org, content.StableID("event", org, "record.enrichment_available", "record", mutation)).Scan(&emitted); err != nil {
			return err
		}
		if !emitted {
			queueEvent(ctx, writes, eventInput{Organization: org, CorpusID: corpusID, Kind: "record.enrichment_available", Resource: "record", ResourceID: recordID, MutationID: mutation, VersionID: seg.VersionID})
		} else {
			observeRecordInBatch(ctx, writes, org, recordID)
		}
		return tx.SendBatch(ctx, writes).Close()
	}, nil
}

var _ content.EmbeddingRepository = EmbeddingStore{}

// EmbeddingStore persists embeddings state.
type EmbeddingStore struct{ Pool *pgxpool.Pool }
