package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RecordStore reads Record identities, Corpus catalogs and Relation targets.
type RecordStore struct{ Pool *pgxpool.Pool }

var _ content.RecordReader = RecordStore{}
var _ content.RecordCatalog = RecordStore{}
var _ content.RelationResolver = RecordStore{}

func (s RecordStore) Record(ctx context.Context, org, id string) (content.Record, error) {
	r := content.Record{}
	err := database(ctx, s.Pool).QueryRow(ctx, "SELECT id,corpus_id,namespace,record_key,withdrawn,coalesce(current_version_id,'') FROM records WHERE organization=$1 AND id=$2", org, id).Scan(&r.ID, &r.Source.CorpusID, &r.Source.Namespace, &r.Source.RecordKey, &r.Withdrawn, &r.CurrentVersionID)
	return r, notFound(err)
}

// catalogTimeSQL maps undated Records below every finite acceptance timestamp.
// The matching expression index permits one tuple seek across dated and undated
// pages, without an OR predicate that scans earlier pages.
const catalogTimeSQL = "coalesce(current_accepted_at,'-infinity'::timestamptz)"

// PostgreSQL dates lie on a microsecond lattice. Ceil either bound so >= and <
// preserve their meaning when the RFC3339 input has nanosecond precision.
func catalogBound(t time.Time) time.Time {
	micro := t.Truncate(time.Microsecond)
	if !micro.Equal(t) {
		micro = micro.Add(time.Microsecond)
	}
	return micro
}

func catalogRange(q content.RecordQuery, args *[]any) string {
	where := "records.organization=$1 AND records.corpus_id=$2"
	if len(q.CorpusIDs) == 1 {
		(*args)[1] = q.CorpusIDs[0]
	} else if len(q.CorpusIDs) > 1 {
		(*args)[1] = q.CorpusIDs
		where = "records.organization=$1 AND records.corpus_id=ANY($2::text[])"
	}
	corpusSQL := "$2"
	if len(q.CorpusIDs) > 1 {
		corpusSQL = "records.corpus_id"
	}
	return where + catalogPredicates(q, args, corpusSQL)
}

func catalogPredicates(q content.RecordQuery, args *[]any, corpusSQL string) string {
	// The scalar primary-key lookup preserves the ordered plan even without
	// corpus statistics. A bound corpus needs only one archive check per page.
	where := " AND NOT COALESCE((SELECT c.archived FROM corpora c WHERE c.organization=$1 AND c.id=" + corpusSQL + "),false)"
	where += catalogMetadata(q, args)
	if q.AcceptedAfter != nil || q.AcceptedBefore != nil {
		where += " AND current_accepted_at IS NOT NULL"
	}
	if q.AcceptedAfter != nil {
		*args = append(*args, catalogBound(*q.AcceptedAfter))
		where += fmt.Sprintf(" AND %s >= $%d", catalogTimeSQL, len(*args))
	}
	if q.AcceptedBefore != nil {
		*args = append(*args, catalogBound(*q.AcceptedBefore))
		where += fmt.Sprintf(" AND %s < $%d", catalogTimeSQL, len(*args))
	}
	return where
}

// Records reads one keyset page, defaulting to the original byte-wise ID order.
func (s RecordStore) Records(ctx context.Context, org, corpusID string, q content.RecordQuery) ([]content.Record, error) {
	if len(q.Metadata) > 0 {
		return s.metadataRecords(ctx, org, corpusID, q)
	}
	q.CorpusIDs = union(q.CorpusIDs, nil)
	args := []any{org, corpusID}
	where := ""
	if len(q.CorpusIDs) > 1 {
		args[1] = q.CorpusIDs
		where = "records.organization=$1 AND records.corpus_id=requested.corpus_id" + catalogPredicates(q, &args, "requested.corpus_id")
	} else {
		where = catalogRange(q, &args)
	}
	cursor, order := catalogCursor(q, &args)
	where += cursor
	args = append(args, q.Limit)
	// Each corpus contributes at most one page. The outer merge can never
	// sort the corpus; its input is bounded by the requested corpus count.
	page := `SELECT id,corpus_id,namespace,record_key,withdrawn,coalesce(current_version_id,'') AS current_version_id,current_accepted_at FROM records WHERE ` + where + ` ORDER BY ` + order + fmt.Sprintf(" LIMIT $%d", len(args))
	sql := page
	if len(q.CorpusIDs) > 1 {
		sql = `SELECT page.* FROM unnest($2::text[]) AS requested(corpus_id) CROSS JOIN LATERAL (` + page + `) page ORDER BY ` + order + fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, err := database(ctx, s.Pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []content.Record{}
	for rows.Next() {
		var r content.Record
		if err = rows.Scan(&r.ID, &r.Source.CorpusID, &r.Source.Namespace, &r.Source.RecordKey, &r.Withdrawn, &r.CurrentVersionID, &r.CurrentAcceptedAt); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// catalogCursor is shared by ordered windows and candidate pages, so the
// adaptive path preserves the unfiltered catalog's exclusive tuple seek.
func catalogCursor(q content.RecordQuery, args *[]any) (where, order string) {
	order = `id COLLATE "C"`
	if q.Order == content.AcceptedAtDesc {
		order = catalogTimeSQL + ` DESC,id COLLATE "C" DESC`
		if q.AfterID != "" {
			after := pgtype.Timestamptz{InfinityModifier: pgtype.NegativeInfinity, Valid: true}
			if q.AfterAcceptedAt != nil {
				after = pgtype.Timestamptz{Time: *q.AfterAcceptedAt, Valid: true}
			}
			*args = append(*args, after, q.AfterID)
			where += fmt.Sprintf(` AND (%s,id COLLATE "C") < ($%d,$%d)`, catalogTimeSQL, len(*args)-1, len(*args))
		}
	} else {
		*args = append(*args, q.AfterID)
		where += fmt.Sprintf(` AND id > $%d COLLATE "C"`, len(*args))
	}
	return where, order
}

func (s RecordStore) CountRecords(ctx context.Context, org, corpusID string, q content.RecordQuery) (int64, error) {
	args := []any{org, corpusID}
	var count int64
	err := database(ctx, s.Pool).QueryRow(ctx, "SELECT count(*) FROM records WHERE "+catalogRange(q, &args), args...).Scan(&count)
	return count, err
}

// Resolve expands immutable Record-target Relations against canonical
// currentness, baseline availability and authorization. Missing, unready,
// withdrawn, quarantined and inaccessible targets all resolve to "unavailable"
// with no target IDs.
func (s RecordStore) Resolve(ctx context.Context, scope corpus.Scope, relations []content.Relation) ([]content.ResolvedRelation, error) {
	resolved := make([]content.ResolvedRelation, len(relations))
	for i, relation := range relations {
		resolved[i] = content.ResolvedRelation{Source: relation, Status: "unavailable"}
		if !scope.Contains(relation.Target.CorpusID) {
			continue
		}
		var recordID, versionID string
		err := database(ctx, s.Pool).QueryRow(ctx, `SELECT r.id,r.current_version_id FROM records r JOIN record_versions v ON (v.organization,v.id)=(r.organization,r.current_version_id) WHERE r.organization=$1 AND r.corpus_id=$2 AND r.namespace=$3 AND r.record_key=$4 AND `+eligibleVersionSQL+` AND NOT EXISTS(SELECT 1 FROM corpora c WHERE c.organization=r.organization AND c.id=r.corpus_id AND c.archived)`, scope.Organization, relation.Target.CorpusID, relation.Target.Namespace, relation.Target.RecordKey).Scan(&recordID, &versionID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		resolved[i].Status = "available"
		resolved[i].TargetRecordID = recordID
		resolved[i].TargetVersionID = versionID
	}
	return resolved, nil
}
