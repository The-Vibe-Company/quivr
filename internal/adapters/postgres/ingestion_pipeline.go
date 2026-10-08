package postgres

import (
	"context"
	"encoding/json"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/jackc/pgx/v5"
)

// Each guard is a separate statement after the journal fence. READ COMMITTED
// therefore observes the preceding writer's commit, rather than the snapshot
// from before waiting for that writer. Immutable preparation remains outside
// this critical section; no mutable writes are sent until all guards pass.
var ingestionPipelineGuardSQL = `SELECT r.id,r.corpus_id,coalesce(r.desired_version_id,''),` + recordGoneSQL + `,v.quarantined,v.baseline_ready,` + eligibleVersionSQL + `,
 $3=` + routedGenerationSQL("r.organization", "r.corpus_id") + `,
 (SELECT digest FROM segmentations WHERE organization=$1 AND id=$4 AND version_id=$2),
 (SELECT ingestion_routing FROM projection_generations WHERE id=$3),
 (SELECT space_id FROM projection_generations WHERE id=$3),
 (SELECT spaces FROM projection_generations WHERE id=$3),
 (SELECT coalesce(nullif(ar.source_media_type,''),'text/plain') FROM accepted_revisions ar WHERE (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)),
 EXISTS(SELECT 1 FROM change_events WHERE organization=$1 AND event_id=$5)
 FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
 WHERE v.organization=$1 AND v.id=$2 FOR UPDATE OF r,v`

type ingestionPipelineGuard struct {
	record, corpus, desired                             string
	gone, quarantined, ready, eligible, active, emitted bool
	digest, source                                      *string
	routing, spaces                                     []byte
	primary                                             string
	receipt                                             string
	reserved                                            *string
	serves                                              bool
	recipe                                              string
	evaluations                                         bool
}

// pipeline handles only the ordinary served case. Any exceptional member
// leaves the whole group to the existing finishes under the same fence,
// before allocating events or sending mutable writes.
func (group *journalGroup) pipeline(ctx context.Context, tx pgx.Tx) (bool, error) {
	for _, e := range group.entries {
		if e.Kind != content.CommitPublication && e.Kind != content.CommitBaseline && e.Kind != content.CommitVectors {
			return false, lockJournal(ctx, tx, group.organization)
		}
	}
	reads := journalBatch(group.organization)
	for _, e := range group.entries {
		seg, g := pipelineProjection(e)
		mutation := content.StableID("enrichment", seg.ID, g.ID)
		reads.Queue(ingestionPipelineGuardSQL, group.organization, seg.VersionID, g.ID, seg.ID,
			eventID(eventInput{Organization: group.organization, Kind: "record.enrichment_available", Resource: "record", MutationID: mutation}))
		if e.Kind == content.CommitPublication {
			w := e.Publication.Work
			reads.Queue(`SELECT rc.state,(SELECT digest FROM accepted_revisions WHERE organization=$1 AND record_id=$3 AND slot=$4) FROM ingestion_receipts rc WHERE organization=$1 AND id=$2 FOR UPDATE OF rc`, group.organization, w.ReceiptID, w.RecordID, w.Slot)
		}
		w, pinned := plugins.WorkOf(e.Context)
		if pinned && w.Kind == plugins.WorkIngestion && e.Kind != content.CommitVectors {
			reads.Queue(currentServingRecipeSQL, group.organization, seg.VersionID, g.ID)
			if e.Kind == content.CommitPublication {
				reads.Queue(pinnedOwnerServesSQL, group.organization, seg.VersionID, w.Plan)
			}
		}
		if e.Kind == content.CommitVectors {
			plan := ""
			if pinned {
				plan = w.Plan
			}
			reads.Queue(ingestionEvaluationTargetsSQL, group.organization, seg.VersionID, plan, content.PluginOfRecipe(seg.Recipe))
		}
	}
	results := tx.SendBatch(ctx, reads)
	defer results.Close()
	for range 3 {
		if _, err := results.Exec(); err != nil {
			return false, err
		}
	}
	guards := make([]ingestionPipelineGuard, len(group.entries))
	for i, e := range group.entries {
		_, g := pipelineProjection(e)
		guard := &guards[i]
		guard.serves = true
		if err := results.QueryRow().Scan(&guard.record, &guard.corpus, &guard.desired, &guard.gone, &guard.quarantined, &guard.ready, &guard.eligible, &guard.active, &guard.digest, &guard.routing, &guard.primary, &guard.spaces, &guard.source, &guard.emitted); err != nil {
			return false, err
		}
		if e.Kind == content.CommitPublication {
			if err := results.QueryRow().Scan(&guard.receipt, &guard.reserved); err != nil {
				return false, err
			}
		}
		w, pinned := plugins.WorkOf(e.Context)
		if pinned && w.Kind == plugins.WorkIngestion && e.Kind != content.CommitVectors {
			var err error
			guard.recipe, err = scanServingRecipe(results.QueryRow(), g)
			if err != nil {
				return false, err
			}
			if e.Kind == content.CommitPublication {
				if err = results.QueryRow().Scan(&guard.serves); err != nil {
					return false, err
				}
			}
		}
		if e.Kind == content.CommitVectors {
			rows, err := results.Query()
			if err != nil {
				return false, err
			}
			guard.evaluations = rows.Next()
			rows.Close()
			if err = rows.Err(); err != nil {
				return false, err
			}
		}
	}
	if err := results.Close(); err != nil {
		return false, err
	}
	for i, e := range group.entries {
		guard := guards[i]
		seg, g := pipelineProjection(e)
		if guard.record != e.RecordID || guard.gone || guard.quarantined || !guard.active || guard.digest == nil || *guard.digest != content.SegmentationDigest(seg) || guard.source == nil {
			return false, nil
		}
		if len(guard.routing) > 0 {
			if err := json.Unmarshal(guard.routing, &g.IngestionRouting); err != nil {
				return false, err
			}
		}
		owner := content.PluginOfRecipe(seg.Recipe)
		if g.IngestionRouting != nil && g.IngestionRouting.For(*guard.source) != "" && g.IngestionRouting.For(*guard.source) != owner {
			return false, nil
		}
		if guard.recipe != "" && content.PluginOfRecipe(guard.recipe) == owner && guard.recipe != seg.Recipe {
			return false, nil
		}
		if e.Kind == content.CommitPublication && (guard.receipt == "resolved" || guard.reserved == nil || *guard.reserved != e.Publication.Work.Digest || !guard.serves) {
			return false, nil
		}
		if e.Kind == content.CommitVectors {
			current := content.Generation{SpaceID: guard.primary}
			var err error
			if current.Spaces, err = scanSpaces(guard.spaces); err != nil {
				return false, err
			}
			if !guard.eligible || !group.preparedVectors[seg.VersionID] || len(e.Artifacts) == 0 || guard.evaluations || current.ServedFor(owner) != g.ServedFor(owner) {
				return false, nil
			}
		}
	}
	group.locked = true
	writes := &pgx.Batch{}
	for i, e := range group.entries {
		guard := guards[i]
		seg, g := pipelineProjection(e)
		member := context.WithValue(ingestionMemberContext{ctx, e.Context}, journalGroupKey{}, group)
		event := func(kind, mutation, version string) {
			queueEvent(member, writes, eventInput{Organization: group.organization, CorpusID: guard.corpus, Kind: kind, Resource: "record", ResourceID: guard.record, MutationID: mutation, VersionID: version})
		}
		if e.Kind == content.CommitPublication {
			w := e.Publication.Work
			event("record.materialized", w.VersionID, "")
			writes.Queue(`UPDATE ingestion_receipts SET state='resolved',outcome='created',version_id=$3,processing='idle',error_code='' WHERE organization=$1 AND id=$2`, group.organization, w.ReceiptID, w.VersionID)
			queueEvent(member, writes, eventInput{Organization: group.organization, CorpusID: guard.corpus, Kind: "receipt.resolved", Resource: "receipt", ResourceID: w.ReceiptID})
			if group.segmented[seg.VersionID] {
				writes.Queue(`UPDATE record_versions SET segmented_at=clock_timestamp() WHERE organization=$1 AND id=$2 AND segmented_at IS NULL AND materialized_at IS NOT NULL`, group.organization, seg.VersionID)
				writes.Queue(`INSERT INTO projection_purge_candidates(organization,version_id) VALUES($1,$2) ON CONFLICT(organization,version_id) DO UPDATE SET version_id=EXCLUDED.version_id`, group.organization, seg.VersionID)
			}
		}
		if e.Kind == content.CommitVectors {
			// Evaluation snapshotting uses the fallback, so taking this generation's
			// shared lock here cannot precede a same-group snapshot update.
			writes.Queue(`SELECT id FROM projection_generations WHERE id=$1 FOR SHARE`, g.ID)
			writes.Queue(`UPDATE record_versions SET enrichment_state='idle',enrichment_error='',enrichment_reason=NULL,enriched_at=`+firstStep("enriched_at")+` WHERE organization=$1 AND id=$2`, group.organization, seg.VersionID)
			if !guard.emitted {
				event("record.enrichment_available", content.StableID("enrichment", seg.ID, g.ID), seg.VersionID)
			} else {
				group.records[guard.record] = true
			}
		} else {
			writes.Queue(`INSERT INTO projection_coverage(organization,version_id,generation_id,segmentation_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, group.organization, seg.VersionID, g.ID, seg.ID)
			writes.Queue(`UPDATE record_versions SET baseline_ready=true,processing='idle',error_code='',retrieval_ready_at=`+firstStep("retrieval_ready_at")+` WHERE organization=$1 AND id=$2`, group.organization, seg.VersionID)
			if guard.desired == seg.VersionID {
				writes.Queue(`UPDATE records SET current_version_id=$3 WHERE organization=$1 AND id=$2`, group.organization, guard.record, seg.VersionID)
			}
			if !guard.ready {
				event("record.retrieval_ready", content.StableID("baseline", seg.VersionID, g.ID), seg.VersionID)
			} else {
				group.records[guard.record] = true
			}
		}
	}
	group.queueAppend(writes)
	return true, tx.SendBatch(ctx, writes).Close()
}

func pipelineProjection(e content.IngestionCommit) (content.Segmentation, content.Generation) {
	if e.Kind == content.CommitPublication {
		return e.Publication.Segmentation, e.Publication.Generation
	}
	return e.Segmentation, e.Generation
}
