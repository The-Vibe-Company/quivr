package postgres

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
)

func (s ProjectionStore) IngestionPage(ctx context.Context, org, version, recipe, spaces string, number int) (content.IngestionPage, bool, error) {
	var page content.IngestionPage
	err := database(ctx, s.Pool).QueryRow(ctx, `SELECT segments,next_cursor FROM ingestion_pages WHERE organization=$1 AND version_id=$2 AND recipe=$3 AND spaces_key=$4 AND page_number=$5`, org, version, recipe, spaces, number).Scan(&page.Segments, &page.Next)
	if errors.Is(err, pgx.ErrNoRows) {
		return page, false, nil
	}
	return page, err == nil, err
}

func (s ProjectionStore) SaveIngestionPage(ctx context.Context, org, version, recipe, spaces string, number int, page content.IngestionPage) (content.IngestionPage, error) {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return content.IngestionPage{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockProcessingVersion(ctx, tx, org, version); err != nil {
		return content.IngestionPage{}, err
	}
	var finished bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ingestion_page_completions WHERE organization=$1 AND version_id=$2 AND recipe=$3 AND spaces_key=$4)
 OR NOT EXISTS(SELECT 1 FROM record_versions v JOIN records r ON(r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2 AND NOT `+deadVersionSQL+`)`, org, version, recipe, spaces).Scan(&finished); err != nil {
		return content.IngestionPage{}, err
	}
	// The in-flight caller still needs its provider result. Canonical artifacts
	// arbitrate its eventual publication, but this response needs no new page.
	if finished {
		return page, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ingestion_pages(organization,version_id,recipe,spaces_key,page_number,segments,next_cursor) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, org, version, recipe, spaces, number, page.Segments, page.Next); err != nil {
		return content.IngestionPage{}, err
	}
	var winner content.IngestionPage
	if err = tx.QueryRow(ctx, `SELECT segments,next_cursor FROM ingestion_pages WHERE organization=$1 AND version_id=$2 AND recipe=$3 AND spaces_key=$4 AND page_number=$5`, org, version, recipe, spaces, number).Scan(&winner.Segments, &winner.Next); err != nil {
		return winner, err
	}
	return winner, tx.Commit(ctx)
}

// DeleteIngestionPages retires only the caller's completed derivation input.
// The caller has already committed segmentation and every requested vector.
func (s ProjectionStore) DeleteIngestionPages(ctx context.Context, org, version, recipe, inputKey string) error {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockProcessingVersion(ctx, tx, org, version); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ingestion_page_completions(organization,version_id,recipe,spaces_key)
 SELECT $1,$2,$3,$4 WHERE EXISTS(SELECT 1 FROM record_versions v JOIN records r ON(r.organization,r.id)=(v.organization,v.record_id) WHERE v.organization=$1 AND v.id=$2 AND NOT `+deadVersionSQL+`) ON CONFLICT DO NOTHING`, org, version, recipe, inputKey); err != nil {
		return err
	}
	if _, err = deleteIngestionPageBatch(ctx, tx, org, version, recipe, inputKey, 1000); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var _ content.IngestionPageStore = ProjectionStore{}

func deleteIngestionPageBatch(ctx context.Context, tx pgx.Tx, org, version, recipe, key string, limit int) (int, error) {
	tag, err := tx.Exec(ctx, `WITH candidates AS MATERIALIZED (
 SELECT organization,version_id,recipe,spaces_key,page_number FROM ingestion_pages
 WHERE organization=$1 AND version_id=$2 AND recipe=$3 AND spaces_key=$4 ORDER BY page_number LIMIT $5
 ) DELETE FROM ingestion_pages p USING candidates c WHERE(p.organization,p.version_id,p.recipe,p.spaces_key,p.page_number)=(c.organization,c.version_id,c.recipe,c.spaces_key,c.page_number)`, org, version, recipe, key, limit)
	return int(tag.RowsAffected()), err
}
