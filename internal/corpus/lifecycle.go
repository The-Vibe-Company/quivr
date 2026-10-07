package corpus

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/internal/publicerr"
)

// LifecycleStore changes the administrative state without changing content.
type LifecycleStore interface {
	Archive(context.Context, string, string, bool) (Corpus, error)
	Rename(context.Context, string, string, string) (Corpus, error)
	ListArchived(context.Context, Scope, string, int, bool) ([]Corpus, error)
}

func (s Service) Archive(ctx context.Context, scope Scope, id string, archived bool) (Corpus, error) {
	if err := scope.Require(ActionCorpusArchive); err != nil {
		return Corpus{}, err
	}
	if !scope.Contains(id) {
		return Corpus{}, ErrNotFound
	}
	store, ok := s.Store.(LifecycleStore)
	if !ok {
		return Corpus{}, publicerr.StorageUnavailable
	}
	return store.Archive(ctx, scope.Organization, id, archived)
}
func (s Service) Rename(ctx context.Context, scope Scope, id, name string) (Corpus, error) {
	if err := scope.Require(ActionCorpusRename); err != nil {
		return Corpus{}, err
	}
	if !scope.Contains(id) {
		return Corpus{}, ErrNotFound
	}
	if strings.TrimSpace(name) == "" || strings.ContainsRune(name, 0) || utf8.RuneCountInString(name) > 256 {
		return Corpus{}, publicerr.InvalidInput
	}
	store, ok := s.Store.(LifecycleStore)
	if !ok {
		return Corpus{}, publicerr.StorageUnavailable
	}
	return store.Rename(ctx, scope.Organization, id, name)
}
func (s Service) ListArchived(ctx context.Context, scope Scope, after string, limit int, include bool) ([]Corpus, error) {
	if err := scope.Require(ActionCorpusList); err != nil {
		return nil, err
	}
	if !include {
		return s.Store.List(ctx, scope, after, limit)
	}
	store, ok := s.Store.(LifecycleStore)
	if !ok {
		return nil, publicerr.StorageUnavailable
	}
	return store.ListArchived(ctx, scope, after, limit, include)
}
