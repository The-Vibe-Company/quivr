package content

import (
	"context"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

type RecordOrder string

const (
	RecordIDOrder  RecordOrder = "record_id"
	AcceptedAtDesc RecordOrder = "accepted_at_desc"
)

// RecordQuery selects current-Version acceptance times. Undated Records remain
// in unbounded catalogs, after dated Records in acceptance order. AfterID and
// AfterAcceptedAt form the exclusive keyset position; nil means an undated key.
type CatalogFilterRoute struct {
	CorpusID     string
	GenerationID string
	Filters      []corpus.TypedFilter
}

type RecordQuery struct {
	CorpusIDs       []string
	Metadata        []corpus.MetadataFilter
	FilterRoutes    []CatalogFilterRoute
	Order           RecordOrder
	AcceptedAfter   *time.Time
	AcceptedBefore  *time.Time
	AfterID         string
	AfterAcceptedAt *time.Time
	Limit           int
}

func (s Service) authorizeCatalog(ctx context.Context, scope corpus.Scope, corpusID string) error {
	if err := scope.Require(corpus.ActionContentRecords); err != nil {
		return err
	}
	if !scope.Contains(corpusID) {
		return corpus.ErrNotFound
	}
	if s.Corpora != nil {
		_, err := s.Corpora.Read(ctx, scope.Organization, corpusID)
		return err
	}
	return nil
}

// Records authorizes before preparing a query, so an invalid or differently
// scoped cursor cannot reveal a Corpus the caller cannot read.
func (s Service) Records(ctx context.Context, scope corpus.Scope, corpusID string, q RecordQuery, prepare ...func() (RecordQuery, error)) ([]Record, error) {
	if err := s.authorizeCatalog(ctx, scope, corpusID); err != nil {
		return nil, err
	}
	for _, load := range prepare {
		var err error
		q, err = load()
		if err != nil {
			return nil, err
		}
	}
	q.CorpusIDs = []string{corpusID}
	return s.Catalog.Records(ctx, scope.Organization, corpusID, q)
}

// CountRecords is an exact independent read with the catalog's authorization,
// time bounds and withdrawn semantics. It does not traverse pages.
func (s Service) CountRecords(ctx context.Context, scope corpus.Scope, corpusID string, q RecordQuery) (int64, error) {
	if err := s.authorizeCatalog(ctx, scope, corpusID); err != nil {
		return 0, err
	}
	q.CorpusIDs = []string{corpusID}
	return s.Catalog.CountRecords(ctx, scope.Organization, corpusID, q)
}

// RecordsAcross authorizes every requested Corpus, including those whose
// mappings exclude it, before evaluating query/cursor details.
func (s Service) RecordsAcross(ctx context.Context, scope corpus.Scope, ids []string, q RecordQuery, prepare func() (RecordQuery, error)) ([]Record, error) {
	if len(ids) == 0 || len(ids) > 16 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			return nil, ErrInvalid
		}
		seen[id] = true
		if err := s.authorizeCatalog(ctx, scope, id); err != nil {
			return nil, err
		}
	}
	var err error
	if prepare != nil {
		q, err = prepare()
		if err != nil {
			return nil, err
		}
	}
	q.CorpusIDs = ids
	return s.Catalog.Records(ctx, scope.Organization, ids[0], q)
}
