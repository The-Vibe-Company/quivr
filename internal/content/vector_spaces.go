package content

import (
	"context"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

type VectorSpaceStore interface {
	VectorSpaces(context.Context, string, string) (Generation, []SpaceCoverage, int64, error)
}

// VectorSpaces owns the permission and Corpus visibility of the space catalog.
type VectorSpaces struct {
	Store   VectorSpaceStore
	Corpora corpus.Store
}

func (s VectorSpaces) List(ctx context.Context, scope corpus.Scope, id string) (Generation, []SpaceCoverage, int64, error) {
	if err := scope.Require(corpus.ActionVectorSpacesRead); err != nil {
		return Generation{}, nil, 0, err
	}
	if s.Store == nil || !scope.Contains(id) {
		return Generation{}, nil, 0, corpus.ErrNotFound
	}
	if _, err := s.Corpora.Read(ctx, scope.Organization, id); err != nil {
		return Generation{}, nil, 0, err
	}
	return s.Store.VectorSpaces(ctx, scope.Organization, id)
}
