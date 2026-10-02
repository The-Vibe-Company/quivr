package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/jackc/pgx/v5"
)

var _ content.ActivityStore = ContentStore{}

// activityColumns reads one document's activity. accepted_revisions is the
// spine: a Version id exists there from acceptance, before the Version is
// materialized. The reserving receipt, which shares the revision's acceptance
// order, dates Versions accepted before the revision recorded it.
const activityColumns = `SELECT a.version_id,a.record_id,r.corpus_id,r.namespace,r.record_key,coalesce(a.title,''),
 coalesce(a.accepted_at,rc.accepted_at),v.materialized_at,v.segmented_at,v.retrieval_ready_at,v.enriched_at,v.evaluated_at,v.quarantined_at,r.withdrawn_at,
 v.id IS NOT NULL,coalesce(v.baseline_ready,false),coalesce(v.quarantined,false),coalesce(v.processing,''),
 r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id),
 coalesce(r.current_version_id=a.version_id,false)
FROM accepted_revisions a
JOIN records r ON (r.organization,r.id)=(a.organization,a.record_id)
LEFT JOIN record_versions v ON (v.organization,v.id)=(a.organization,a.version_id)
LEFT JOIN ingestion_receipts rc ON (rc.organization,rc.record_id,rc.acceptance_order)=(a.organization,a.record_id,a.acceptance_order)`

func scanActivity(row pgx.Row) (content.Activity, error) {
	var a content.Activity
	s := &a.Steps
	var materialized, baseline, quarantined, withdrawn bool
	var processing string
	err := row.Scan(&a.VersionID, &a.RecordID, &a.Source.CorpusID, &a.Source.Namespace, &a.Source.RecordKey, &a.Title,
		&s.Accepted, &s.Materialized, &s.Segmented, &s.RetrievalReady, &s.Enriched, &s.Evaluated, &s.Quarantined, &s.Withdrawn,
		&materialized, &baseline, &quarantined, &processing, &withdrawn, &a.Current)
	if err != nil {
		return a, err
	}
	a.Steps = utcSteps(a.Steps)
	// The same rule as VersionStatus, extended to the states before and
	// after a Version exists.
	switch {
	case withdrawn:
		a.State = content.ActivityWithdrawn
	case !materialized:
		a.State = content.ActivityReceived
	case quarantined:
		a.State = "quarantined"
	case baseline:
		a.State = "retrieval_ready"
	case processing == "running" || processing == "retrying":
		a.State = "building_baseline"
	default:
		a.State = "materialized"
	}
	a.Current = a.Current && !withdrawn && !quarantined
	return a, nil
}

// LatestActivity reads one keyset page of the Organization's most recently
// accepted Versions, newest first, in a single statement over
// accepted_revisions_latest. Versions accepted before step times were
// recorded have no acceptance time here and are not listed.
func (s ContentStore) LatestActivity(ctx context.Context, org string, after *content.ActivityCursor, limit int) ([]content.Activity, error) {
	query := activityColumns + ` WHERE a.organization=$1 AND a.accepted_at IS NOT NULL`
	args := []any{org, limit}
	if after != nil {
		query += ` AND (a.accepted_at,a.version_id) < ($3,$4)`
		args = append(args, after.AcceptedAt, after.VersionID)
	}
	rows, err := s.Pool.Query(ctx, query+` ORDER BY a.accepted_at DESC,a.version_id DESC LIMIT $2`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (content.Activity, error) { return scanActivity(r) })
}

// VersionActivity reads one Version's activity with the plugins that ran
// its steps: the normalizer whose output was published, and the ingestion
// plugin whose segmentation the Corpus's routed generation serves.
func (s ContentStore) VersionActivity(ctx context.Context, org, versionID string) (content.Activity, error) {
	// A revision accepted since step times were recorded is found by its
	// Version id; an earlier one only once materialized, through its Version.
	a, err := scanActivity(s.Pool.QueryRow(ctx, activityColumns+` WHERE a.organization=$1 AND a.version_id=$2 AND a.accepted_at IS NOT NULL`, org, versionID))
	if errors.Is(err, pgx.ErrNoRows) {
		a, err = scanActivity(s.Pool.QueryRow(ctx, activityColumns+` WHERE (a.organization,a.record_id,a.slot)=(SELECT organization,record_id,slot FROM record_versions WHERE organization=$1 AND id=$2)`, org, versionID))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return a, corpus.ErrNotFound
	}
	if err != nil {
		return a, err
	}
	var ref content.PluginRef
	err = s.Pool.QueryRow(ctx, `SELECT plugin_id,plugin_version FROM normalizations WHERE organization=$1 AND version_id=$2 AND outcome=$3`, org, versionID, content.OutcomeNormalized).Scan(&ref.ID, &ref.Version)
	if err == nil {
		normalizer := ref
		a.Normalizer = &normalizer
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return a, err
	}
	err = s.Pool.QueryRow(ctx, `SELECT sg.provenance->>'plugin_id',coalesce(sg.provenance->>'plugin_version','') FROM projection_coverage pc
JOIN segmentations sg ON (sg.organization,sg.id)=(pc.organization,pc.segmentation_id)
WHERE pc.organization=$1 AND pc.version_id=$2 AND pc.generation_id=`+routedGenerationSQL("$1", "$3")+` AND pc.role='served' AND sg.provenance ? 'plugin_id'`, org, versionID, a.Source.CorpusID).Scan(&ref.ID, &ref.Version)
	if err == nil {
		a.Ingestion = &ref
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return a, err
	}
	return a, nil
}

// utcSteps reports every step time in UTC.
func utcSteps(s content.Steps) content.Steps {
	for _, t := range []**time.Time{&s.Accepted, &s.Materialized, &s.Segmented, &s.RetrievalReady, &s.Enriched, &s.Evaluated, &s.Quarantined, &s.Withdrawn} {
		if *t != nil {
			at := (*t).UTC()
			*t = &at
		}
	}
	return s
}

// firstStep is the value that records a step once: the first time it
// finishes, and never for Versions materialized before step times were
// recorded, whose earlier steps are unknown.
func firstStep(column string) string {
	return `CASE WHEN materialized_at IS NULL THEN NULL ELSE coalesce(` + column + `,clock_timestamp()) END`
}
