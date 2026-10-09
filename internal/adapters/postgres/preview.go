package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/jackc/pgx/v5"
)

// Recent reads current eligible Versions in acceptance order through the
// catalog index. Equal timestamps use descending byte-wise Record ID, just as
// the catalog does. Receipt history cannot contribute to page selection.
func (s EvaluationStore) Recent(ctx context.Context, org string, corpora []string, after time.Time, limit int) ([]monitoring.RecentVersion, error) {
	// ANY previously treated scope IDs as a set, including saved queries
	// that repeat an ID. Preserve that before building independent pages.
	corpora = union(corpora, nil)
	if len(corpora) == 0 {
		return []monitoring.RecentVersion{}, nil
	}
	args := []any{org, corpora[0]}
	corpusSQL := "$2"
	if len(corpora) > 1 {
		args[1] = corpora
		corpusSQL = "requested.corpus_id"
	}
	where := `r.organization=$1 AND r.corpus_id=` + corpusSQL + ` AND r.current_accepted_at IS NOT NULL`
	if !after.IsZero() {
		args = append(args, after)
		where += fmt.Sprintf(` AND coalesce(r.current_accepted_at,'-infinity'::timestamptz)>$%d`, len(args))
	}
	// Keep eligibility a point read while the ordered records scan walks to its
	// limit. OFFSET prevents the planner from turning EXISTS into a corpus join.
	where += ` AND EXISTS(SELECT 1 FROM record_versions v WHERE v.organization=r.organization AND v.id=r.current_version_id AND ` + eligibleVersionPointSQL + ` OFFSET 0)`
	args = append(args, limit)
	bound := fmt.Sprintf(" LIMIT $%d", len(args))
	page := `SELECT r.corpus_id,r.id AS record_id,r.current_version_id AS version_id,r.current_accepted_at AS accepted_at,
 coalesce(r.current_accepted_at,'-infinity'::timestamptz) AS sort_time
 FROM records r WHERE ` + where + ` ORDER BY sort_time DESC,r.id COLLATE "C" DESC` + bound
	order := ` ORDER BY page.sort_time DESC,page.record_id COLLATE "C" DESC`
	if len(corpora) > 1 {
		page = `SELECT page.* FROM unnest($2::text[]) AS requested(corpus_id) CROSS JOIN LATERAL (` + page + `) page` + order + bound
	}
	// The candidate subquery limits globally before computing enrichment.
	rows, err := s.Pool.Query(ctx, `SELECT page.corpus_id,page.record_id,page.version_id,page.accepted_at,
 EXISTS(SELECT 1 FROM segments sg JOIN LATERAL `+embeddingCoverageForSegmentSQL("sg.organization", "sg.id")+` ec ON true WHERE sg.organization=$1 AND sg.version_id=page.version_id AND ec.generation_id=`+routedGenerationSQL("$1", "page.corpus_id")+`)
 FROM (`+page+`) page`+order, args...)
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
