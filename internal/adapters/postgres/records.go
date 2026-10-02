package postgres

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/jackc/pgx/v5"
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

// Records reads one keyset page of a Corpus catalog in a single statement.
func (s RecordStore) Records(ctx context.Context, org, corpusID, after string, limit int) ([]content.Record, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,corpus_id,namespace,record_key,withdrawn,coalesce(current_version_id,'') FROM records WHERE organization=$1 AND corpus_id=$2 AND id > $3 COLLATE "C" ORDER BY id COLLATE "C" LIMIT $4`, org, corpusID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []content.Record{}
	for rows.Next() {
		var r content.Record
		if err = rows.Scan(&r.ID, &r.Source.CorpusID, &r.Source.Namespace, &r.Source.RecordKey, &r.Withdrawn, &r.CurrentVersionID); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
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
