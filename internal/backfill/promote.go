package backfill

import (
	"context"
	"errors"
	"fmt"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
)

var (
	// ErrCoverageIncomplete refuses to promote a space that some Corpus's
	// routed generation does not carry, or carries without a vector for
	// every current segment, unless the promotion is forced.
	ErrCoverageIncomplete = errors.New("coverage_incomplete")
	// ErrNotEvaluation refuses to promote a space the registry has retired.
	ErrNotEvaluation = errors.New("not_evaluation_space")
)

// Promotion is the outcome of promoting a vector space to served.
type Promotion struct {
	// Served is the space search now uses; Previous the one it replaced,
	// now an evaluation space whose vectors stay.
	Served, Previous string
	// GenerationsSwitched counts the generations that now serve Served.
	GenerationsSwitched int64
	// CorporaIncomplete and SegmentsMissing measure what the space does not
	// cover: Corpora whose routed generation lacks it or a vector in it, and
	// the current segments without one.
	CorporaIncomplete, SegmentsMissing int64
}

// IncompleteError is ErrCoverageIncomplete with its measure.
type IncompleteError struct{ Promotion Promotion }

func (e *IncompleteError) Error() string {
	return fmt.Sprintf("vector space %s lacks a vector for %d current segments in %d Corpora; backfill them, or force the promotion", e.Promotion.Served, e.Promotion.SegmentsMissing, e.Promotion.CorporaIncomplete)
}

func (e *IncompleteError) Unwrap() error { return ErrCoverageIncomplete }

// PromotionStore swaps the served space of the deployment.
type PromotionStore interface {
	// PromoteSpace makes a registered evaluation space the served one, in
	// the registry and in every generation that carries it, and keeps the
	// choice across later space registrations. Unless force, it refuses
	// with IncompleteError while coverage is incomplete. Promoting the
	// served space changes nothing.
	PromoteSpace(ctx context.Context, space string, force bool) (Promotion, error)
}

// Promotions promotes vector spaces for operators (plugins:admin).
type Promotions struct{ Store PromotionStore }

// Promote makes space the one search uses; promoting the former one back
// is the same call.
func (p Promotions) Promote(ctx context.Context, scope corpus.Scope, space string, force bool) (Promotion, error) {
	if !scope.Allows(operations.BackfillPermission) {
		return Promotion{}, corpus.ErrForbidden
	}
	return p.Store.PromoteSpace(ctx, space, force)
}
