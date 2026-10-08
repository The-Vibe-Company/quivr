package postgres

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
)

// IngestionPageCandidates advances by pages scanned, even when their inputs are
// unfinished. Durable primary-key cursors revisit late writes after each cycle.
func (s ProjectionStore) IngestionPageCandidates(ctx context.Context, limit int) ([]content.IngestionPageNamespace, error) {
	if limit <= 0 {
		return nil, nil
	}
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = prunePageCompletions(ctx, tx, limit); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ingestion_page_sweep(kind) VALUES('pages') ON CONFLICT DO NOTHING`); err != nil {
		return nil, err
	}
	var cursor content.IngestionPageNamespace
	var pageNumber int
	if err = tx.QueryRow(ctx, `SELECT organization,version_id,recipe,spaces_key,page_number FROM ingestion_page_sweep WHERE kind='pages' FOR UPDATE`).Scan(&cursor.Organization, &cursor.VersionID, &cursor.Recipe, &cursor.InputKey, &pageNumber); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `WITH candidates AS MATERIALIZED (
 SELECT * FROM ingestion_pages WHERE(organization,version_id,recipe,spaces_key,page_number)>($1,$2,$3,$4,$5)
 ORDER BY organization,version_id,recipe,spaces_key,page_number LIMIT $6
 ) SELECT p.organization,p.version_id,p.recipe,p.spaces_key,p.page_number,p.segments,
 COALESCE(v.record_id,''),COALESCE(r.corpus_id,''),COALESCE(v.manifest_blob_id,''),
 v.id IS NULL OR `+deadVersionSQL+` OR EXISTS(SELECT 1 FROM ingestion_page_completions c WHERE(c.organization,c.version_id,c.recipe,c.spaces_key)=(p.organization,p.version_id,p.recipe,p.spaces_key))
 FROM candidates p LEFT JOIN record_versions v ON(v.organization,v.id)=(p.organization,p.version_id)
 LEFT JOIN records r ON(r.organization,r.id)=(v.organization,v.record_id)
 ORDER BY p.organization,p.version_id,p.recipe,p.spaces_key,p.page_number`, cursor.Organization, cursor.VersionID, cursor.Recipe, cursor.InputKey, pageNumber, limit)
	if err != nil {
		return nil, err
	}
	out := []content.IngestionPageNamespace{}
	scanned := 0
	for rows.Next() {
		var n content.IngestionPageNamespace
		var raw json.RawMessage
		if err = rows.Scan(&n.Organization, &n.VersionID, &n.Recipe, &n.InputKey, &pageNumber, &raw, &n.RecordID, &n.CorpusID, &n.ManifestID, &n.Settled); err != nil {
			rows.Close()
			return nil, err
		}
		scanned++
		cursor = n
		if !n.Settled {
			var segments []struct{ Vectors map[string]json.RawMessage }
			if err = json.Unmarshal(raw, &segments); err != nil {
				rows.Close()
				return nil, err
			}
			n.Spaces = []string{}
			for _, seg := range segments {
				for key := range seg.Vectors {
					if !slices.Contains(n.Spaces, key) {
						n.Spaces = append(n.Spaces, key)
					}
				}
			}
			slices.Sort(n.Spaces)
		}
		if len(out) > 0 {
			last := &out[len(out)-1]
			if last.Organization == n.Organization && last.VersionID == n.VersionID && last.Recipe == n.Recipe && last.InputKey == n.InputKey {
				for _, space := range n.Spaces {
					if !slices.Contains(last.Spaces, space) {
						last.Spaces = append(last.Spaces, space)
					}
				}
				continue
			}
		}
		out = append(out, n)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if scanned < limit {
		cursor = content.IngestionPageNamespace{}
		pageNumber = -1
	}
	if _, err = tx.Exec(ctx, `UPDATE ingestion_page_sweep SET organization=$1,version_id=$2,recipe=$3,spaces_key=$4,page_number=$5 WHERE kind='pages'`, cursor.Organization, cursor.VersionID, cursor.Recipe, cursor.InputKey, pageNumber); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

// RetireIngestionPages verifies canonical completeness under the writer fence.
// segmentation is empty unless the caller independently verified the exact
// input hash and segmentation against its current source Manifest.
func (s ProjectionStore) RetireIngestionPages(ctx context.Context, n content.IngestionPageNamespace, segmentation string, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = lockProcessingVersion(ctx, tx, n.Organization, n.VersionID); err != nil {
		return 0, err
	}
	var settled bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ingestion_page_completions WHERE organization=$1 AND version_id=$2 AND recipe=$3 AND spaces_key=$4)
 OR NOT EXISTS(SELECT 1 FROM record_versions v JOIN records r ON(r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2 AND NOT `+deadVersionSQL+`)`, n.Organization, n.VersionID, n.Recipe, n.InputKey).Scan(&settled); err != nil {
		return 0, err
	}
	if !settled && segmentation != "" {
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM record_versions v JOIN segmentations st ON(st.organization,st.version_id)=(v.organization,v.id)
   WHERE v.organization=$1 AND v.id=$2 AND v.manifest_blob_id=$4 AND st.id=$5 AND st.recipe=$3
    AND EXISTS(SELECT 1 FROM segments sg WHERE sg.organization=$1 AND sg.segmentation_id=st.id)
    AND NOT EXISTS(SELECT 1 FROM segments sg CROSS JOIN unnest($6::text[]) requested(space_id)
     WHERE sg.organization=$1 AND sg.segmentation_id=st.id AND NOT EXISTS(
      SELECT 1 FROM storage_organizations o JOIN storage_segments k ON k.organization_id=o.id AND k.segment_id=sg.id
      JOIN storage_spaces sp ON sp.space_id=requested.space_id
      JOIN compact_embeddings e ON(e.organization_id,e.segment_id,e.space_id)=(o.id,k.id,sp.id)
      JOIN embedding_files f ON f.id=e.file_id AND f.organization_id=o.id AND f.segmentation_id=st.id
      WHERE o.organization=$1 LIMIT 1)))`, n.Organization, n.VersionID, n.Recipe, n.ManifestID, segmentation, n.Spaces).Scan(&settled)
		if err != nil {
			return 0, err
		}
		if settled {
			if _, err = tx.Exec(ctx, `INSERT INTO ingestion_page_completions(organization,version_id,recipe,spaces_key) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, n.Organization, n.VersionID, n.Recipe, n.InputKey); err != nil {
				return 0, err
			}
		}
	}
	if !settled {
		return 0, nil
	}
	deleted, err := deleteIngestionPageBatch(ctx, tx, n.Organization, n.VersionID, n.Recipe, n.InputKey, limit)
	if err != nil {
		return 0, err
	}
	return deleted, tx.Commit(ctx)
}

// Completion fences live as long as their Version can resume. Dead identities
// cannot become current again, and SaveIngestionPage refuses to persist them.
func prunePageCompletions(ctx context.Context, tx pgx.Tx, limit int) error {
	if _, err := tx.Exec(ctx, `INSERT INTO ingestion_page_sweep(kind) VALUES('completions') ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	var org, version, recipe, key string
	if err := tx.QueryRow(ctx, `SELECT organization,version_id,recipe,spaces_key FROM ingestion_page_sweep WHERE kind='completions' FOR UPDATE`).Scan(&org, &version, &recipe, &key); err != nil {
		return err
	}
	var scanned int
	err := tx.QueryRow(ctx, `WITH candidates AS MATERIALIZED (
 SELECT * FROM ingestion_page_completions WHERE(organization,version_id,recipe,spaces_key)>($1,$2,$3,$4)
 ORDER BY organization,version_id,recipe,spaces_key LIMIT $5
 ),removed AS (
 DELETE FROM ingestion_page_completions c USING candidates q WHERE(c.organization,c.version_id,c.recipe,c.spaces_key)=(q.organization,q.version_id,q.recipe,q.spaces_key)
 AND NOT EXISTS(SELECT 1 FROM record_versions v JOIN records r ON(r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=q.organization AND v.id=q.version_id AND NOT `+deadVersionSQL+` LIMIT 1)
 ) SELECT count(*),COALESCE((SELECT organization FROM candidates ORDER BY organization DESC,version_id DESC,recipe DESC,spaces_key DESC LIMIT 1),''),
 COALESCE((SELECT version_id FROM candidates ORDER BY organization DESC,version_id DESC,recipe DESC,spaces_key DESC LIMIT 1),''),
 COALESCE((SELECT recipe FROM candidates ORDER BY organization DESC,version_id DESC,recipe DESC,spaces_key DESC LIMIT 1),''),
 COALESCE((SELECT spaces_key FROM candidates ORDER BY organization DESC,version_id DESC,recipe DESC,spaces_key DESC LIMIT 1),'') FROM candidates`, org, version, recipe, key, limit).Scan(&scanned, &org, &version, &recipe, &key)
	if err != nil {
		return err
	}
	if scanned < limit {
		org = ""
		version = ""
		recipe = ""
		key = ""
	}
	_, err = tx.Exec(ctx, `UPDATE ingestion_page_sweep SET organization=$1,version_id=$2,recipe=$3,spaces_key=$4 WHERE kind='completions'`, org, version, recipe, key)
	return err
}

var _ content.IngestionPageSweepStore = ProjectionStore{}
