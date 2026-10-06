package content

import (
	"context"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
)

// FacetField selects bounded values of one typed projected metadata field.
// Datetime fields require a UTC calendar interval: day, month or year.
type FacetField struct {
	Field    string `json:"field"`
	Limit    int    `json:"limit,omitempty"`
	Interval string `json:"interval,omitempty"`
	Type     string `json:"-"`
}
type FacetBucket struct {
	Value any   `json:"value"`
	Count int64 `json:"count"`
}
type Facet struct {
	Field   string        `json:"field"`
	Buckets []FacetBucket `json:"buckets"`
}
type FacetQuery struct {
	Records          RecordQuery
	Fields           []FacetField
	SourceNamespaces []string
}
type FacetReader interface {
	CountFacets(context.Context, string, FacetQuery) ([]Facet, error)
}

// CountFacets authorizes all selected Corpora before preparing projection
// routes. A Corpus excluded by a missing field still needs authorization.
func (s Service) CountFacets(ctx context.Context, scope corpus.Scope, q FacetQuery, prepare func() (FacetQuery, error)) ([]Facet, error) {
	if len(q.Records.CorpusIDs) == 0 || len(q.Records.CorpusIDs) > 16 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range q.Records.CorpusIDs {
		if id == "" || seen[id] {
			return nil, ErrInvalid
		}
		seen[id] = true
		if err := s.authorizeCatalog(ctx, scope, id); err != nil {
			return nil, err
		}
	}
	if prepare != nil {
		var err error
		q, err = prepare()
		if err != nil {
			return nil, err
		}
	}
	if s.Facets == nil {
		return nil, publicerr.ContentUnavailable
	}
	return s.Facets.CountFacets(ctx, scope.Organization, q)
}
