package content

import (
	"context"
	"time"

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
	// Fast lets the reader answer from a stored snapshot or a uniform sample
	// when exact counting would be slow; FacetCounts says which it used.
	Fast bool
	// Declared holds, per routed Corpus, every filter field of its routed
	// generation with its type: what a snapshot of that Corpus counts.
	Declared map[string][]FacetField
}

// FacetCounts are counted fields and how they were counted. Both markers
// are zero for an exact live count.
type FacetCounts struct {
	Items []Facet
	// AsOf is when the stored snapshot that answered was taken (the oldest,
	// across Corpora). Changes accepted since are not counted.
	AsOf *time.Time
	// SampleFraction, between 0 and 1, is the share of the Corpora's Records
	// counted when counts are estimates scaled from a uniform sample.
	SampleFraction float64
}
type FacetReader interface {
	CountFacets(context.Context, string, FacetQuery) ([]Facet, error)
}

// FastFacetReader answers FacetQuery.Fast requests within a few seconds.
type FastFacetReader interface {
	CountFacetsFast(context.Context, string, FacetQuery) (FacetCounts, error)
}

// CountFacets authorizes all selected Corpora before preparing projection
// routes. A Corpus excluded by a missing field still needs authorization.
// A fast request falls back to exact counting when the reader has no fast path.
func (s Service) CountFacets(ctx context.Context, scope corpus.Scope, q FacetQuery, prepare func() (FacetQuery, error)) (FacetCounts, error) {
	if len(q.Records.CorpusIDs) == 0 || len(q.Records.CorpusIDs) > 16 {
		return FacetCounts{}, ErrInvalid
	}
	authorized := append([]string(nil), q.Records.CorpusIDs...)
	seen := map[string]bool{}
	for _, id := range authorized {
		if id == "" || seen[id] {
			return FacetCounts{}, ErrInvalid
		}
		seen[id] = true
		if err := s.authorizeCatalog(ctx, scope, id); err != nil {
			return FacetCounts{}, err
		}
	}
	if prepare != nil {
		var err error
		q, err = prepare()
		if err != nil {
			return FacetCounts{}, err
		}
	}
	q.Records.CorpusIDs = authorized
	if s.Facets == nil {
		return FacetCounts{}, publicerr.ContentUnavailable
	}
	if fast, ok := s.Facets.(FastFacetReader); ok && q.Fast {
		return fast.CountFacetsFast(ctx, scope.Organization, q)
	}
	items, err := s.Facets.CountFacets(ctx, scope.Organization, q)
	return FacetCounts{Items: items}, err
}
