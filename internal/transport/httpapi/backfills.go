package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/backfill"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// WithBackfills enables POST /v0/admin/backfills and
// POST /v0/admin/spaces/{vector_space_id}/promote (plugins:admin).
func WithBackfills(service backfill.Service, promotions backfill.Promotions) Option {
	return func(a *API) { a.Backfills, a.Promotions = &service, &promotions }
}

const (
	backfillsPath = "/v0/admin/backfills"
	spacesPath    = "/v0/admin/spaces/"
)

// backfillRequest is the backfill command.
type backfillRequest struct {
	IdempotencyKey string     `json:"idempotency_key"`
	CorpusID       string     `json:"corpus_id"`
	AcceptedAfter  *time.Time `json:"accepted_after"`
	AcceptedBefore *time.Time `json:"accepted_before"`
	RegistrationID string     `json:"registration_id"`
	Spaces         []string   `json:"spaces"`
	DryRun         bool       `json:"dry_run"`
	ConfirmCost    bool       `json:"confirm_cost"`
}

func (a *API) requestBackfill(w http.ResponseWriter, r *http.Request, scope corpus.Scope) {
	if a.Backfills == nil {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	var in backfillRequest
	estimate, op, err := a.Backfills.Request(r.Context(), scope, backfill.Request{}, func() (backfill.Request, error) {
		if !decodeInto(w, r, a.schemas["BackfillRequest"], &in) {
			return backfill.Request{}, errResponseWritten
		}

		return backfill.Request{Key: in.IdempotencyKey, CorpusID: in.CorpusID, AcceptedAfter: in.AcceptedAfter, AcceptedBefore: in.AcceptedBefore,
			RegistrationID: in.RegistrationID, Spaces: in.Spaces, DryRun: in.DryRun, ConfirmCost: in.ConfirmCost}, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	switch {
	case err != nil:
		writeError(w, err, publicerr.StorageUnavailable)
	case in.DryRun:
		send(w, 200, estimateToTransport(estimate))
	default:
		// The Operation and its dispatch intent are committed before this response.
		w.Header().Set("Location", "/v0/operations/"+op.ID)
		send(w, 202, operationToTransport(op))
	}
}

func estimateToTransport(e operations.BackfillEstimate) transport.BackfillEstimate {
	return transport.BackfillEstimate{RegistrationId: e.RegistrationID, Spaces: nonNil(e.Spaces), Versions: int(e.Versions), Segments: int(e.Segments), InputTokens: int(e.InputTokens),
		EstimatedSeconds: e.EstimatedSeconds, DurationBasis: transport.BackfillEstimateDurationBasis(e.DurationBasis), EstimatedCostUsd: e.EstimatedCostUSD, ConfirmationRequired: e.ConfirmationRequired}
}

func backfillToTransport(b *operations.Backfill) *transport.OperationBackfill {
	if b == nil {
		return nil
	}
	return &transport.OperationBackfill{RegistrationId: b.RegistrationID, Spaces: nonNil(b.Spaces), AcceptedAfter: b.AcceptedAfter, AcceptedBefore: b.AcceptedBefore,
		PlanId: optionalString(b.PlanID), Checkpoint: optionalString(b.Checkpoint), Estimate: estimateToTransport(b.Estimate)}
}

// promotionRequest is the promotion command.
type promotionRequest struct {
	Force bool `json:"force"`
}

func (a *API) promoteSpace(w http.ResponseWriter, r *http.Request, scope corpus.Scope, space string) {
	if a.Promotions == nil {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	if space == "" {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	var in promotionRequest
	p, err := a.Promotions.Promote(r.Context(), scope, "", false, func() (string, bool, error) {
		if !decodeInto(w, r, a.schemas["VectorSpacePromotionRequest"], &in) {
			return "", false, errResponseWritten
		}

		return space, in.Force, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	switch {
	case err != nil:
		writeError(w, err, publicerr.StorageUnavailable)
	default:
		send(w, 200, transport.VectorSpacePromotion{ServedSpaceId: p.Served, PreviousSpaceId: p.Previous, GenerationsSwitched: int(p.GenerationsSwitched),
			CorporaIncomplete: int(p.CorporaIncomplete), SegmentsMissing: int(p.SegmentsMissing)})
	}
}
