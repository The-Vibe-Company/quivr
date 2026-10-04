package uploads

import (
	"context"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// CreateScoped authorizes before loading the upload request. Worker attachment
// transfers use the organization-bound Grant path instead.
func (s Service) CreateScoped(ctx context.Context, scope corpus.Scope, req Request, prepare ...func() (Request, error)) (Session, error) {
	if err := scope.Require(corpus.ActionUploadCreate); err != nil {
		return Session{}, err
	}
	for _, load := range prepare {
		var err error
		req, err = load()
		if err != nil {
			return Session{}, err
		}
	}
	return s.Create(ctx, scope.Organization, req)
}
func (s Service) GetScoped(ctx context.Context, scope corpus.Scope, id string) (Session, error) {
	if err := scope.Require(corpus.ActionUploadRead); err != nil {
		return Session{}, err
	}
	return s.Get(ctx, scope.Organization, id)
}
func (s Service) ConfirmScoped(ctx context.Context, scope corpus.Scope, id string) (Session, error) {
	if err := scope.Require(corpus.ActionUploadConfirm); err != nil {
		return Session{}, err
	}
	return s.Confirm(ctx, scope.Organization, id)
}
func (s Service) BlobScoped(ctx context.Context, scope corpus.Scope, id string) (BlobInfo, error) {
	if err := scope.Require(corpus.ActionBlobRead); err != nil {
		return BlobInfo{}, err
	}
	return s.Blob(ctx, scope.Organization, id)
}
