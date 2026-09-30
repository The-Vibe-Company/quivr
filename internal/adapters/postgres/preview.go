package postgres

import (
	"context"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/jackc/pgx/v5"
)

// Recent lists up to limit current eligible Record Versions of the Corpora,
// the most recently accepted first, for a Subscription preview. Eligibility
// and enrichment are read as evaluation reads them. It walks the Receipts
// newest first through ingestion_receipts_recent.
func (s EvaluationStore) Recent(ctx context.Context, org string, corpora []string, after time.Time, limit int) ([]monitoring.RecentVersion, error) {
	var since *time.Time
	if !after.IsZero() {
		since = &after
	}
	rows, err := s.Pool.Query(ctx, `SELECT r.corpus_id,r.id,v.id,rc.accepted_at,
  EXISTS(SELECT 1 FROM segments sg JOIN embedding_coverage ec ON (ec.organization,ec.segment_id)=(sg.organization,sg.id) WHERE sg.organization=$1 AND sg.version_id=v.id AND ec.generation_id=`+routedGenerationSQL("$1", "r.corpus_id")+`)
FROM ingestion_receipts rc
JOIN records r ON (r.organization,r.id)=(rc.organization,rc.record_id)
JOIN record_versions v ON (v.organization,v.id)=(r.organization,r.current_version_id) AND v.acceptance_order=rc.acceptance_order
WHERE rc.organization=$1 AND rc.corpus_id=ANY($2::text[]) AND ($3::timestamptz IS NULL OR rc.accepted_at>$3) AND `+eligibleVersionSQL+`
ORDER BY rc.accepted_at DESC,v.id DESC LIMIT $4`, org, corpora, since, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (monitoring.RecentVersion, error) {
		var v monitoring.RecentVersion
		err := r.Scan(&v.CorpusID, &v.RecordID, &v.VersionID, &v.AcceptedAt, &v.Enriched)
		v.AcceptedAt = v.AcceptedAt.UTC()
		return v, err
	})
}
