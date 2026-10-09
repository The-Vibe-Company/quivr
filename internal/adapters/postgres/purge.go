package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/jackc/pgx/v5/pgxpool"
)

// abandonedGenerationsSQL selects (Organization, Corpus, generation) triples
// that can never be routed again. A route only switches to the target of a
// running Operation, a terminal Operation never runs again (a rerun is a new
// Operation with a new generation), and routes are never deleted, so a
// routed Corpus never falls back to the default generation, nor to a former
// default (default_until set): a Corpus served by one when the default moved
// was routed to it, and a Corpus created later never followed it.
var abandonedGenerationsSQL = `SELECT o.organization,o.corpus_id,o.target_generation_id FROM operations o
WHERE o.state IN ('succeeded','failed','canceled') AND o.target_generation_id<>` + routedGenerationSQL("o.organization", "o.corpus_id") + `
UNION
SELECT cr.organization,cr.corpus_id,dg.id FROM corpus_projection_routes cr JOIN ` + effectiveGenerationsSQL + ` dg ON dg.active OR dg.default_until IS NOT NULL WHERE cr.generation_id<>dg.id`

// deadVersionSQL is true for a Version aliased v of Record r that can never be
// served again: its Record is withdrawn or tombstoned (absorbing fences), or it
// is neither the Record's current nor its desired Version. desired only moves
// to a newly reserved slot at a newer source position, a Version identity is
// reserved once per Record slot (a revert to earlier bytes reserves a new slot
// and so a new Version, ADR 0003), and current only moves to desired.
const deadVersionSQL = `(` + recordGoneSQL + `
 OR (v.id IS DISTINCT FROM r.current_version_id AND v.id IS DISTINCT FROM r.desired_version_id))`

// Materialize a bounded batch before point-reading canonical state. LIMIT 1
// on unique-key lookups prevents a planner alternative that pre-hashes all
// Versions/Records. The candidate deletion and ledger insert share one atomic
// statement; concurrent application upserts wait and enqueue again after commit.
const noticeVersionPurgesSQL = `WITH candidates AS MATERIALIZED (
 SELECT organization,version_id FROM projection_purge_candidates
 ORDER BY organization,version_id LIMIT $1 FOR UPDATE SKIP LOCKED
), consumed AS (
 DELETE FROM projection_purge_candidates q USING candidates c
 WHERE (q.organization,q.version_id)=(c.organization,c.version_id)
 RETURNING q.organization,q.version_id
)
INSERT INTO projection_purges(organization,kind,version_id)
SELECT v.organization,'version',v.id FROM consumed c
JOIN LATERAL (SELECT * FROM record_versions v WHERE (v.organization,v.id)=(c.organization,c.version_id) LIMIT 1) v ON true
JOIN LATERAL (SELECT * FROM records r WHERE (r.organization,r.id)=(v.organization,v.record_id) LIMIT 1) r ON true
WHERE (r.withdrawn OR COALESCE((SELECT true FROM tombstones t WHERE t.organization=r.organization AND t.record_id=r.id),false)
 OR (v.id IS DISTINCT FROM r.current_version_id AND v.id IS DISTINCT FROM r.desired_version_id))
 AND COALESCE((SELECT true FROM segmentations sg WHERE sg.organization=v.organization AND sg.version_id=v.id LIMIT 1),false)
ON CONFLICT DO NOTHING`

// NoticePurges records up to limit newly dead items; their grace period starts now.
func (s PurgeStore) NoticePurges(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO projection_purges(organization,kind,corpus_id,generation_id)
SELECT d.organization,'generation',d.corpus_id,d.target_generation_id FROM (`+abandonedGenerationsSQL+`) d
WHERE NOT EXISTS(SELECT 1 FROM projection_purges p WHERE p.organization=d.organization AND p.kind='generation' AND p.corpus_id=d.corpus_id AND p.generation_id=d.target_generation_id AND p.version_id='')
LIMIT $1 ON CONFLICT DO NOTHING`, limit)
	if err != nil {
		return 0, err
	}
	noticed := int(tag.RowsAffected())
	tag, err = s.Pool.Exec(ctx, noticeVersionPurgesSQL, limit)
	if err != nil {
		return noticed, err
	}
	return noticed + int(tag.RowsAffected()), nil
}

// ClaimPurges leases up to limit unpurged items noticed before now-grace. The
// claim rechecks that each item is still dead; permanence makes that a guard,
// not a race.
func (s PurgeStore) ClaimPurges(ctx context.Context, grace, lease time.Duration, limit int) ([]retrieval.PurgeItem, error) {
	rows, err := s.Pool.Query(ctx, `WITH due AS (
 SELECT p.organization,p.kind,p.corpus_id,p.generation_id,p.version_id FROM projection_purges p
 WHERE (p.purged_at IS NULL OR p.cleanup_stage<3) AND p.lease_until<now() AND p.noticed_at<now()-make_interval(secs=>$1::double precision)
  AND CASE p.kind
   WHEN 'generation' THEN p.generation_id<>`+routedGenerationSQL("p.organization", "p.corpus_id")+`
    AND NOT EXISTS(SELECT 1 FROM operations o WHERE o.organization=p.organization AND o.target_generation_id=p.generation_id AND o.state NOT IN ('succeeded','failed','canceled'))
   ELSE EXISTS(SELECT 1 FROM record_versions v JOIN records r ON (r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=p.organization AND v.id=p.version_id AND `+deadVersionSQL+`)
  END
 ORDER BY p.noticed_at LIMIT $3 FOR UPDATE OF p SKIP LOCKED)
UPDATE projection_purges p SET lease_until=now()+make_interval(secs=>$2::double precision) FROM due
WHERE (p.organization,p.kind,p.corpus_id,p.generation_id,p.version_id)=(due.organization,due.kind,due.corpus_id,due.generation_id,due.version_id)
RETURNING p.organization,p.kind,p.corpus_id,p.generation_id,p.version_id,p.noticed_at`, grace.Seconds(), lease.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	items := []retrieval.PurgeItem{}
	for rows.Next() {
		var it retrieval.PurgeItem
		if err = rows.Scan(&it.Organization, &it.Kind, &it.CorpusID, &it.GenerationID, &it.VersionID, &it.NoticedAt); err != nil {
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
		// in any collection its Organization's generations or the current or
		// former defaults use.
		query, args := `SELECT collection FROM `+effectiveGenerationsSQL+` WHERE id=$1`, []any{it.GenerationID}
		if it.Kind == retrieval.PurgeVersion {
			query, args = `SELECT DISTINCT collection FROM `+effectiveGenerationsSQL+` WHERE active OR default_until IS NOT NULL OR organization=$1 ORDER BY collection`, []any{it.Organization}
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

// RecordPurge records projection deletion and advances one bounded coverage
// scan. Completion requires both the external deletion and every SQL stage.
func (s PurgeStore) RecordPurge(ctx context.Context, it retrieval.PurgeItem, deleted int, complete bool, limit int) (bool, error) {
	if limit <= 0 {
		return false, errors.New("purge batch must be positive")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var stage int
	var cursor json.RawMessage
	err = tx.QueryRow(ctx, `SELECT cleanup_stage,cleanup_cursor FROM projection_purges WHERE organization=$1 AND kind=$2 AND corpus_id=$3 AND generation_id=$4 AND version_id=$5 AND (purged_at IS NULL OR cleanup_stage<3) FOR UPDATE`, it.Organization, it.Kind, it.CorpusID, it.GenerationID, it.VersionID).Scan(&stage, &cursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if complete {
		stage, cursor, err = purgeCoverageBatch(ctx, tx, it, stage, cursor, limit)
		if err != nil {
			return false, err
		}
		complete = stage == 3
	}
	_, err = tx.Exec(ctx, `UPDATE projection_purges SET objects_deleted=objects_deleted+$6,lease_until='-infinity',purged_at=CASE WHEN $7 THEN now() END,cleanup_stage=$8,cleanup_cursor=$9
 WHERE organization=$1 AND kind=$2 AND corpus_id=$3 AND generation_id=$4 AND version_id=$5`, it.Organization, it.Kind, it.CorpusID, it.GenerationID, it.VersionID, deleted, complete, stage, cursor)
	if err != nil {
		return false, err
	}
	return complete, tx.Commit(ctx)
}

// Each stage scans primary keys before checking corpus/generation ownership.
// Advancing over live rows bounds discovery as well as deletion, and prevents
// a neighboring corpus sharing the default from starving reclamation.
func purgeCoverageBatch(ctx context.Context, tx pgx.Tx, it retrieval.PurgeItem, stage int, cursor json.RawMessage, limit int) (int, json.RawMessage, error) {
	for stage < 3 {
		var query string
		args := []any{it.Organization, it.Kind, it.CorpusID, it.GenerationID, it.VersionID, limit, cursor}
		if stage == 0 {
			query = `WITH candidates AS MATERIALIZED (
    SELECT cc.organization_id,cc.file_id,cc.generation_id FROM compact_embedding_coverage cc
    WHERE cc.organization_id=(SELECT id FROM storage_organizations WHERE organization=$1)
     AND (cc.file_id,cc.generation_id)>(COALESCE(($7::jsonb->>0)::bigint,0),COALESCE($7::jsonb->>1,''))
    ORDER BY cc.file_id,cc.generation_id LIMIT $6
   ), removed AS (
    DELETE FROM compact_embedding_coverage cc USING candidates c
    WHERE (cc.organization_id,cc.file_id,cc.generation_id)=(c.organization_id,c.file_id,c.generation_id)
     AND EXISTS(SELECT 1 FROM embedding_files f WHERE f.id=c.file_id AND f.organization_id=c.organization_id
      AND CASE WHEN $2='generation' THEN c.generation_id=$4 AND f.corpus_id=$3 ELSE f.version_id=$5 END LIMIT 1)
   ) SELECT count(*),COALESCE((SELECT jsonb_build_array(file_id,generation_id) FROM candidates ORDER BY file_id DESC,generation_id DESC LIMIT 1),'[]'::jsonb) FROM candidates`
		} else {
			table, pluginKey := "projection_coverage", ",pc.plugin_id"
			cursorKeys := "COALESCE($7::jsonb->>0,''),COALESCE($7::jsonb->>1,''),COALESCE($7::jsonb->>2,'')"
			if stage == 2 {
				table = "projection_metadata"
				pluginKey = ""
				cursorKeys = "COALESCE($7::jsonb->>0,''),COALESCE($7::jsonb->>1,'')"
			}
			keys := "pc.version_id,pc.generation_id" + pluginKey
			desc := "version_id DESC,generation_id DESC"
			if stage == 1 {
				desc += ",plugin_id DESC"
			}
			candidateKeys := "c.version_id,c.generation_id"
			jsonKeys := "version_id,generation_id"
			if stage == 1 {
				candidateKeys += ",c.plugin_id"
				jsonKeys += ",plugin_id"
			}
			query = `WITH candidates AS MATERIALIZED (
    SELECT pc.organization,` + keys + ` FROM ` + table + ` pc WHERE pc.organization=$1 AND (` + keys + `)>(` + cursorKeys + `)
    ORDER BY ` + keys + ` LIMIT $6
   ), removed AS (
    DELETE FROM ` + table + ` pc USING candidates c WHERE pc.organization=c.organization AND (` + keys + `)=(` + candidateKeys + `)
    AND CASE WHEN $2='generation' THEN c.generation_id=$4 AND EXISTS(
     SELECT 1 FROM record_versions v JOIN records r ON(r.organization,r.id)=(v.organization,v.record_id)
     WHERE v.organization=c.organization AND v.id=c.version_id AND r.corpus_id=$3 LIMIT 1)
    ELSE c.version_id=$5 END
   ) SELECT count(*),COALESCE((SELECT jsonb_build_array(` + jsonKeys + `) FROM candidates ORDER BY ` + desc + ` LIMIT 1),'[]'::jsonb) FROM candidates`
		}
		var scanned int
		if err := tx.QueryRow(ctx, query, args...).Scan(&scanned, &cursor); err != nil {
			return stage, cursor, err
		}
		if scanned == limit {
			return stage, cursor, nil
		}
		stage++
		cursor = json.RawMessage(`[]`)
		limit -= scanned
		if limit == 0 {
			return stage, cursor, nil
		}
	}
	return stage, cursor, nil
}

var _ retrieval.PurgeStore = PurgeStore{}

// PurgeStore persists purge state.
type PurgeStore struct{ Pool *pgxpool.Pool }
