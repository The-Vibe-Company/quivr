package content

import (
	"context"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// Submitter is a command path bound to the authenticated request's scope.
// It keeps a batch's authorization outside the per-entry acceptance loop.
type Submitter struct {
	service Service
	scope   corpus.Scope
}

func (s Submitter) Accept(ctx context.Context, c Command) (Receipt, error) {
	return s.service.accept(ctx, s.scope, c, s.scope.Allows("content:read"))
}

// Submit authorizes before the transport loads and validates the command.
func (s Service) Submit(scope corpus.Scope, command func(Submitter) error) error {
	if err := scope.Require(corpus.ActionContentAccept); err != nil {
		return err
	}
	return command(Submitter{service: s, scope: scope})
}

// Batch authorizes the envelope once. Each entry retains independent input
// validation, Corpus visibility and idempotent acceptance.
func (s Service) Batch(scope corpus.Scope, commands func(Submitter) error) error {
	if err := scope.Require(corpus.ActionContentBatch); err != nil {
		return err
	}
	return commands(Submitter{service: s, scope: scope})
}
