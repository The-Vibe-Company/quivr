package postgres

import (
	"context"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

// abandonedGenerationsSQL selects (Organization, Corpus, generation) triples
// that can never be routed again. A route only switches to the target of a
// running Operation, a terminal Operation never runs again (a rerun is a new
// Operation with a new generation), and routes are never deleted, so a
// routed Corpus never falls back to the default generation.
var abandonedGenerationsSQL = `SELECT o.organization,o.corpus_id,o.target_generation_id FROM operations o
WHERE o.state IN ('succeeded','failed','canceled') AND o.target_generation_id<>` + routedGenerationSQL("o.organization", "o.corpus_id") + `
UNION
SELECT cr.organization,cr.corpus_id,dg.id FROM corpus_projection_routes cr JOIN projection_generations dg ON dg.active WHERE cr.generation_id<>dg.id`

// deadVersionSQL is true for a Version aliased v of Record r that can never be
// served again: its Record is withdrawn or tombstoned (absorbing fences), or it
// is neither the Record's current nor its desired Version. desired only moves
// to a newly reserved slot at a newer source position, a Version identity is
// reserved once per Record slot (a revert to earlier bytes reserves a new slot
// and so a new Version, ADR 0003), and current only moves to desired.
const deadVersionSQL = `(r.withdrawn OR EXISTS(SELECT 1 FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id)
 OR (v.id IS DISTINCT FROM r.current_version_id AND v.id IS DISTINCT FROM r.desired_version_id))`

// NoticePurges records up to limit newly dead items; their grace period starts now.
func (s ContentStore) NoticePurges(ctx context.Context, limit int) (int, error) {
	tag, err := s.Pool.Exec(ctx, `INSERT INTO projection_purges(organization,kind,corpus_id,generation_id)
SELECT d.organization,'generation',d.corpus_id,d.target_generation_id FROM (`+abandonedGenerationsSQL+`) d
WHERE NOT EXISTS(SELECT 1 FROM projection_purges p WHERE p.organization=d.organization AND p.kind='generation' AND p.corpus_id=d.corpus_id AND p.generation_id=d.target_generation_id AND p.version_id='')
LIMIT $1 ON CONFLICT DO NOTHING`, limit)
	if err != nil {
		return 0, err
	}
	noticed := int(tag.RowsAffected())
	// Only Versions that were segmented can have been projected.
	tag, err = s.Pool.Exec(ctx, `INSERT INTO projection_purges(organization,kind,version_id)
SELECT v.organization,'version',v.id FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id)
WHERE `+deadVersionSQL+`
 AND EXISTS(SELECT 1 FROM segmentations sg WHERE sg.organization=v.organization AND sg.version_id=v.id)
 AND NOT EXISTS(SELECT 1 FROM projection_purges p WHERE p.organization=v.organization AND p.kind='version' AND p.corpus_id='' AND p.generation_id='' AND p.version_id=v.id)
LIMIT $1 ON CONFLICT DO NOTHING`, limit)
	if err != nil {
		return noticed, err
	}
	return noticed + int(tag.RowsAffected()), nil
}

// ClaimPurges leases up to limit unpurged items noticed before now-grace. The
// claim rechecks that each item is still dead; permanence makes that a guard,
// not a race.
func (s ContentStore) ClaimPurges(ctx context.Context, grace, lease time.Duration, limit int) ([]retrieval.PurgeItem, error) {
	rows, err := s.Pool.Query(ctx, `WITH due AS (
 SELECT p.organization,p.kind,p.corpus_id,p.generation_id,p.version_id FROM projection_purges p
 WHERE p.purged_at IS NULL AND p.lease_until<now() AND p.noticed_at<now()-make_interval(secs=>$1::double precision)
  AND CASE p.kind
   WHEN 'generation' THEN p.generation_id<>`+routedGenerationSQL("p.organization", "p.corpus_id")+`
    AND NOT EXISTS(SELECT 1 FROM operations o WHERE o.organization=p.organization AND o.target_generation_id=p.generation_id AND o.state NOT IN ('succeeded','failed','canceled'))
   ELSE EXISTS(SELECT 1 FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=p.organization AND v.id=p.version_id AND `+deadVersionSQL+`)
  END
 ORDER BY p.noticed_at LIMIT $3 FOR UPDATE OF p SKIP LOCKED)
UPDATE projection_purges p SET lease_until=now()+make_interval(secs=>$2::double precision) FROM due
WHERE (p.organization,p.kind,p.corpus_id,p.generation_id,p.version_id)=(due.organization,due.kind,due.corpus_id,due.generation_id,due.version_id)
RETURNING p.organization,p.kind,p.corpus_id,p.generation_id,p.version_id`, grace.Seconds(), lease.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	items := []retrieval.PurgeItem{}
	for rows.Next() {
		var it retrieval.PurgeItem
		if err = rows.Scan(&it.Organization, &it.Kind, &it.CorpusID, &it.GenerationID, &it.VersionID); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i := range items {
		it := &items[i]
		// A generation lives in its own collection; a Version may have objects
		// in any collection its Organization's generations or the default use.
		query, args := `SELECT collection FROM projection_generations WHERE id=$1`, []any{it.GenerationID}
		if it.Kind == retrieval.PurgeVersion {
			query, args = `SELECT DISTINCT collection FROM projection_generations WHERE active OR organization=$1 ORDER BY collection`, []any{it.Organization}
		}
		cols, err := s.Pool.Query(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for cols.Next() {
			var c string
			if err = cols.Scan(&c); err != nil {
				cols.Close()
				return nil, err
			}
			it.Collections = append(it.Collections, c)
		}
		cols.Close()
		if err = cols.Err(); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// RecordPurge adds deleted objects to the item and releases its lease; a
// complete purge is stamped and never claimed again.
func (s ContentStore) RecordPurge(ctx context.Context, it retrieval.PurgeItem, deleted int, complete bool) error {
	_, err := s.Pool.Exec(ctx, `UPDATE projection_purges SET objects_deleted=objects_deleted+$6,lease_until='-infinity',purged_at=CASE WHEN $7 THEN now() END
WHERE organization=$1 AND kind=$2 AND corpus_id=$3 AND generation_id=$4 AND version_id=$5 AND purged_at IS NULL`, it.Organization, it.Kind, it.CorpusID, it.GenerationID, it.VersionID, deleted, complete)
	return err
}

var _ retrieval.PurgeStore = ContentStore{}
