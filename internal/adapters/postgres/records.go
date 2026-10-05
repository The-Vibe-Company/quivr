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
	err := s.Pool.QueryRow(ctx, "SELECT id,corpus_id,namespace,record_key,withdrawn,coalesce(current_version_id,'') FROM records WHERE organization=$1 AND id=$2", org, id).Scan(&r.ID, &r.Source.CorpusID, &r.Source.Namespace, &r.Source.RecordKey, &r.Withdrawn, &r.CurrentVersionID)
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
	where := "organization=$1 AND corpus_id=$2"
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
	args := []any{org, corpusID}
	where := catalogRange(q, &args)
	order := `id COLLATE "C"`
	if q.Order == content.AcceptedAtDesc {
		order = catalogTimeSQL + ` DESC,id COLLATE "C" DESC`
		if q.AfterID != "" {
			after := pgtype.Timestamptz{InfinityModifier: pgtype.NegativeInfinity, Valid: true}
			if q.AfterAcceptedAt != nil {
				after = pgtype.Timestamptz{Time: *q.AfterAcceptedAt, Valid: true}
			}
			args = append(args, after, q.AfterID)
			where += fmt.Sprintf(` AND (%s,id COLLATE "C") < ($%d,$%d)`, catalogTimeSQL, len(args)-1, len(args))
		}
	} else {
		args = append(args, q.AfterID)
		where += fmt.Sprintf(` AND id > $%d COLLATE "C"`, len(args))
	}
	args = append(args, q.Limit)
	rows, err := s.Pool.Query(ctx, `SELECT id,corpus_id,namespace,record_key,withdrawn,coalesce(current_version_id,''),current_accepted_at FROM records WHERE `+where+` ORDER BY `+order+fmt.Sprintf(" LIMIT $%d", len(args)), args...)
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

func (s RecordStore) CountRecords(ctx context.Context, org, corpusID string, q content.RecordQuery) (int64, error) {
	args := []any{org, corpusID}
	var count int64
	err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM records WHERE "+catalogRange(q, &args), args...).Scan(&count)
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
		err := s.Pool.QueryRow(ctx, `SELECT r.id,r.current_version_id FROM records r JOIN record_versions v ON (v.organization,v.id)=(r.organization,r.current_version_id) WHERE r.organization=$1 AND r.corpus_id=$2 AND r.namespace=$3 AND r.record_key=$4 AND `+eligibleVersionSQL, scope.Organization, relation.Target.CorpusID, relation.Target.Namespace, relation.Target.RecordKey).Scan(&recordID, &versionID)
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
