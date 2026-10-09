package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/routing"
	"github.com/jackc/pgx/v5"
)

type routingRecord struct {
	org, id  string
	revision int64
}

func (s RoutingStore) scanRoutingRecords(ctx context.Context, tx pgx.Tx, w *routingWork) error {
	rows, err := tx.Query(ctx, `SELECT organization,id FROM records WHERE (organization,id)>($1,$2) ORDER BY organization,id LIMIT $3`, w.cursorOrg, w.cursorRecord, routingBatch)
	if err != nil {
		return err
	}
	batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (routingRecord, error) {
		var v routingRecord
		err := r.Scan(&v.org, &v.id)
		return v, err
	})
	if err != nil {
		return err
	}
	for _, r := range batch {
		if err = s.countRoutingRecord(ctx, tx, w, r); err != nil {
			return err
		}
		w.cursorOrg, w.cursorRecord = r.org, r.id
	}
	phase := "scan"
	if len(batch) < routingBatch {
		phase = "reconcile"
		if w.settings.NoRoutingChange {
			phase = "cutover"
		}
	}
	_, err = tx.Exec(ctx, `UPDATE routing_operations SET phase=$3,cursor_organization=$4,cursor_record=$5,
 counters=jsonb_set(counters,'{records_scanned}',to_jsonb(COALESCE((counters->>'records_scanned')::bigint,0)+$6::bigint)) WHERE organization=$1 AND id=$2`, w.org, w.id, phase, w.cursorOrg, w.cursorRecord, len(batch))
	return err
}

func (s RoutingStore) countRoutingRecord(ctx context.Context, tx pgx.Tx, w *routingWork, r routingRecord) error {
	var corpus, version, generation, media, primary string
	var oldRoute, nextRoute, spaces []byte
	err := tx.QueryRow(ctx, `SELECT r.corpus_id,v.id,g.id,COALESCE(NULLIF(ar.source_media_type,''),'text/plain'),g.ingestion_routing,o.ingestion_routing,o.spaces,o.space_id
 FROM records r JOIN record_versions v ON (v.organization,v.id)=(r.organization,r.current_version_id)
 JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(v.organization,v.record_id,v.slot)
 JOIN `+effectiveGenerationsSQL+` g ON g.id=`+routedGenerationSQL("r.organization", "r.corpus_id")+`
 JOIN routing_generation_settings o ON (o.epoch,o.generation_id)=($3,g.id)
 WHERE r.organization=$1 AND r.id=$2 AND `+eligibleVersionPointSQL, r.org, r.id, w.id).Scan(&corpus, &version, &generation, &media, &oldRoute, &nextRoute, &spaces, &primary)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.replaceRoutingGap(ctx, tx, w, r, "", "", "", "", 0, "", "")
	}
	if err != nil {
		return err
	}
	var previous content.IngestionRouting
	if len(oldRoute) > 0 {
		if err = json.Unmarshal(oldRoute, &previous); err != nil {
			return err
		}
	} else if w.previousPlan != "" {
		previous, err = planIngestionRouting(ctx, tx, w.previousPlan)
		if err != nil {
			return err
		}
	}
	owner, space := w.settings.Owner, w.command.Target
	if w.command.Kind == routing.KindPromotion && previous.For(media) != "" && previous.For(media) != owner {
		return s.preserveRoutingGap(ctx, tx, w, r, corpus, version)
	}
	if w.command.Kind != routing.KindPromotion {
		var next content.IngestionRouting
		if len(nextRoute) > 0 {
			if err = json.Unmarshal(nextRoute, &next); err != nil {
				return err
			}
		}
		if w.settings.NoRoutingChange || w.settings.Routing == nil || previous.For(media) == next.For(media) {
			return s.preserveRoutingGap(ctx, tx, w, r, corpus, version)
		}
		owner = next.For(media)
		space = ""
		for _, sp := range w.settings.Spaces {
			if sp.OwnerPluginID == owner && sp.Role == content.SpaceServed {
				space = sp.ID
				break
			}
		}
	}
	carried, err := scanSpaces(spaces)
	if err != nil {
		return err
	}
	carries := (content.Generation{SpaceID: primary, Spaces: carried, SpacesProjected: true}).Carries(space)
	var segmentation string
	err = tx.QueryRow(ctx, `SELECT segmentation_id FROM projection_coverage WHERE organization=$1 AND version_id=$2 AND generation_id=$3 AND plugin_id=$4`, r.org, version, generation, owner).Scan(&segmentation)
	var missing int64
	if errors.Is(err, pgx.ErrNoRows) && w.command.Kind == routing.KindPromotion {
		return s.preserveRoutingGap(ctx, tx, w, r, corpus, version)
	}
	if errors.Is(err, pgx.ErrNoRows) || space == "" {
		missing = 1
	} else if err != nil {
		return err
	} else {
		err = tx.QueryRow(ctx, `SELECT count(*) FROM segments sg WHERE sg.organization=$1 AND sg.segmentation_id=$2 AND sg.version_id=$6 AND
 ($5 OR NOT EXISTS(SELECT FROM `+embeddingCoverageForSegmentSQL("sg.organization", "sg.id")+` ec WHERE ec.organization=sg.organization AND ec.segment_id=sg.id AND ec.generation_id=$3 AND ec.space_id=$4))`, r.org, segmentation, generation, space, !carries, version).Scan(&missing)
		if err != nil {
			return err
		}
	}
	if missing == 0 {
		return s.replaceRoutingGap(ctx, tx, w, r, "", "", "", "", 0, "", "")
	}
	fallbackOwner, fallbackSpace := "", ""
	if w.command.Kind == routing.KindRollback {
		fallbackOwner = previous.For(media)
		var inheritedOwner, inheritedSpace string
		err = tx.QueryRow(ctx, `SELECT fallback_owner,fallback_space FROM routing_coverage_gaps WHERE epoch=$1 AND organization=$2 AND record_id=$3 AND version_id=$4`, w.previousEpoch, r.org, r.id, version).Scan(&inheritedOwner, &inheritedSpace)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if inheritedOwner != "" {
			return s.replaceRoutingGap(ctx, tx, w, r, corpus, version, owner, space, missing, inheritedOwner, inheritedSpace)
		}
		oldG, err := routedGeneration(ctx, tx, r.org, corpus)
		if err != nil {
			return err
		}
		fallbackSpace = oldG.ServedFor(fallbackOwner)
	}
	return s.replaceRoutingGap(ctx, tx, w, r, corpus, version, owner, space, missing, fallbackOwner, fallbackSpace)
}

// Unchanged formats carry their exact rollback fallback into the next epoch.
// Those gaps remain visible for recovery but do not gate an unrelated switch.
func (s RoutingStore) preserveRoutingGap(ctx context.Context, tx pgx.Tx, w *routingWork, r routingRecord, corpus, version string) error {
	var fallbackOwner, fallbackSpace, targetOwner, targetSpace string
	var missing int64
	err := tx.QueryRow(ctx, `SELECT owner_plugin_id,space_id,missing_segments,fallback_owner,fallback_space FROM routing_coverage_gaps WHERE epoch=$1 AND organization=$2 AND record_id=$3 AND version_id=$4`, w.previousEpoch, r.org, r.id, version).Scan(&targetOwner, &targetSpace, &missing, &fallbackOwner, &fallbackSpace)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.replaceRoutingGap(ctx, tx, w, r, "", "", "", "", 0, "", "")
	}
	if err != nil {
		return err
	}
	return s.replaceRoutingGapRequired(ctx, tx, w, r, corpus, version, targetOwner, targetSpace, missing, fallbackOwner, fallbackSpace, false)
}

func (s RoutingStore) replaceRoutingGap(ctx context.Context, tx pgx.Tx, w *routingWork, r routingRecord, corpus, version, owner, space string, missing int64, fallbackOwner, fallbackSpace string) error {
	return s.replaceRoutingGapRequired(ctx, tx, w, r, corpus, version, owner, space, missing, fallbackOwner, fallbackSpace, true)
}

func (s RoutingStore) replaceRoutingGapRequired(ctx context.Context, tx pgx.Tx, w *routingWork, r routingRecord, corpus, version, owner, space string, missing int64, fallbackOwner, fallbackSpace string, required bool) error {
	var previous int64
	var previousRequired bool
	err := tx.QueryRow(ctx, `DELETE FROM routing_coverage_gaps WHERE epoch=$1 AND organization=$2 AND record_id=$3 RETURNING missing_segments,cutover_required`, w.id, r.org, r.id).Scan(&previous, &previousRequired)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if missing > 0 {
		_, err = tx.Exec(ctx, `INSERT INTO routing_coverage_gaps(epoch,organization,record_id,corpus_id,version_id,owner_plugin_id,space_id,missing_segments,fallback_owner,fallback_space,cutover_required) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, w.id, r.org, r.id, corpus, version, owner, space, missing, fallbackOwner, fallbackSpace, required)
		if err != nil {
			return err
		}
	}
	versions := int64(0)
	if missing > 0 {
		versions++
	}
	if previous > 0 {
		versions--
	}
	if missing > 0 && required {
		w.requiredDelta++
	}
	if previous > 0 && previousRequired {
		w.requiredDelta--
	}
	w.missingDelta += versions
	w.segmentsDelta += missing - previous
	return nil
}

func (s RoutingStore) reconcileRouting(ctx context.Context, tx pgx.Tx, w *routingWork) error {
	// A rebuild route invalidates a whole Corpus. Turn that invalidation into
	// bounded record batches, never a fan-out in the publication transaction.
	var org, corpus, cursor string
	var revision int64
	err := tx.QueryRow(ctx, `SELECT organization,corpus_id,cursor_record,revision FROM routing_dirty_corpora WHERE epoch=$1 ORDER BY organization,corpus_id LIMIT 1`, w.id).Scan(&org, &corpus, &cursor, &revision)
	if err == nil {
		rows, err := tx.Query(ctx, `SELECT id FROM records WHERE organization=$1 AND corpus_id=$2 AND id>$3 ORDER BY id LIMIT $4`, org, corpus, cursor, routingBatch)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		orgs := make([]string, len(ids))
		for i := range ids {
			orgs[i] = org
		}
		if len(ids) > 0 {
			if _, err = tx.Exec(ctx, markRoutingRecordsSQL, orgs, ids); err != nil {
				return err
			}
			cursor = ids[len(ids)-1]
		}
		if len(ids) < routingBatch {
			_, err = tx.Exec(ctx, `DELETE FROM routing_dirty_corpora WHERE epoch=$1 AND organization=$2 AND corpus_id=$3 AND revision=$4`, w.id, org, corpus, revision)
		} else {
			_, err = tx.Exec(ctx, `UPDATE routing_dirty_corpora SET cursor_record=$4 WHERE epoch=$1 AND organization=$2 AND corpus_id=$3 AND revision=$5`, w.id, org, corpus, cursor, revision)
		}
		return err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var generation string
	err = tx.QueryRow(ctx, `SELECT generation_id FROM routing_dirty_generations WHERE epoch=$1 ORDER BY generation_id LIMIT 1`, w.id).Scan(&generation)
	if err == nil {
		// Restage from the current effective settings; records of this
		// generation must also be rechecked before the pointer can change.
		if _, err = tx.Exec(ctx, `DELETE FROM routing_dirty_generations WHERE epoch=$1 AND generation_id=$2`, w.id, generation); err != nil {
			return err
		}
		// Generation counts are small control metadata, but staging still
		// advances in batches. Restarting that cursor preserves cumulative data.
		_, err = tx.Exec(ctx, `UPDATE routing_operations SET phase='generations',cursor_generation='',cursor_organization='',cursor_record='' WHERE organization=$1 AND id=$2`, w.org, w.id)
		return err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT organization,record_id,revision FROM routing_dirty_records WHERE epoch=$1 ORDER BY organization,record_id LIMIT $2`, w.id, routingBatch)
	if err != nil {
		return err
	}
	batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (routingRecord, error) {
		var v routingRecord
		err := r.Scan(&v.org, &v.id, &v.revision)
		return v, err
	})
	if err != nil {
		return err
	}
	for _, r := range batch {
		if err = s.countRoutingRecord(ctx, tx, w, r); err != nil {
			return err
		}
		// Compare the revision we observed; a concurrent writer's newer dirty
		// entry survives this step and is reconciled again.
		if _, err = tx.Exec(ctx, `DELETE FROM routing_dirty_records WHERE epoch=$1 AND organization=$2 AND record_id=$3 AND revision=$4`, w.id, r.org, r.id, r.revision); err != nil {
			return err
		}
	}
	if len(batch) == 0 {
		_, err = tx.Exec(ctx, `UPDATE routing_operations SET phase='cutover' WHERE organization=$1 AND id=$2`, w.org, w.id)
	}
	return err
}
