// Package routing owns durable deployment routing changes. Counting and
// preparation advance in bounded steps; only the final pointer switch is fenced.
package routing

import (
	"context"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
)

const (
	KindPromotion  = "vector_space_promotion"
	KindActivation = "plugin_activation"
	KindRollback   = "plugin_rollback"
)

func IsKind(kind string) bool {
	return kind == KindPromotion || kind == KindActivation || kind == KindRollback
}

type Command struct {
	Kind       string `json:"kind"`
	Target     string `json:"target,omitempty"`
	Key        string `json:"idempotency_key,omitempty"`
	Force      bool   `json:"force,omitempty"`
	PinnedWork string `json:"pinned_work,omitempty"`
}

type Progress struct {
	Done bool
	Wait time.Duration
}

type Store interface {
	AcceptRouting(context.Context, string, Command) (operations.Operation, error)
	StepRouting(context.Context, string, string) (Progress, error)
}

type Service struct{ Store Store }

func (s Service) Request(ctx context.Context, scope corpus.Scope, command Command, prepare ...func() (Command, error)) (operations.Operation, error) {
	action := corpus.ActionPluginActivate
	switch command.Kind {
	case KindPromotion:
		action = corpus.ActionBackfillPromote
	case KindRollback:
		action = corpus.ActionPluginRollback
	case KindActivation:
	default:
		return operations.Operation{}, operations.ErrUnsupportedKind
	}
	if err := scope.Require(action); err != nil {
		return operations.Operation{}, err
	}
	for _, load := range prepare {
		var err error
		command, err = load()
		if err != nil {
			return operations.Operation{}, err
		}
	}
	return s.Store.AcceptRouting(ctx, scope.Organization, command)
}

func (s Service) Step(ctx context.Context, org, id string) (Progress, error) {
	return s.Store.StepRouting(ctx, org, id)
}
