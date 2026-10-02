package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/backfill"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
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

// backfillRoutes serves the backfill and promotion commands.
func (a *API) backfillRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	if r.URL.Path == backfillsPath {
		if r.Method != "POST" {
			failure(w, 405, "method_not_allowed")
		} else if a.Backfills == nil {
			failure(w, 404, "not_found")
		} else {
			a.requestBackfill(w, r, scope)
		}
		return true
	}
	rest, ok := strings.CutPrefix(r.URL.Path, spacesPath)
	space, isPromote := strings.CutSuffix(rest, "/promote")
	if !ok || !isPromote || space == "" || strings.Contains(space, "/") {
		return false
	}
	switch {
	case r.Method != "POST":
		failure(w, 405, "method_not_allowed")
	case a.Promotions == nil:
		failure(w, 404, "not_found")
	default:
		a.promoteSpace(w, r, scope, space)
	}
	return true
}

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
	if !scope.Allows(operations.BackfillPermission) {
		failure(w, 403, "forbidden")
		return
	}
	var in backfillRequest
	if !decodeInto(w, r, a.backfillSchema, &in) {
		return
	}
	estimate, op, err := a.Backfills.Request(r.Context(), scope, backfill.Request{Key: in.IdempotencyKey, CorpusID: in.CorpusID, AcceptedAfter: in.AcceptedAfter, AcceptedBefore: in.AcceptedBefore,
		RegistrationID: in.RegistrationID, Spaces: in.Spaces, DryRun: in.DryRun, ConfirmCost: in.ConfirmCost})
	switch {
	case errors.Is(err, corpus.ErrForbidden):
		failure(w, 403, "forbidden")
	case errors.Is(err, corpus.ErrNotFound):
		failure(w, 404, "not_found")
	case errors.Is(err, operations.ErrConflict):
		failure(w, 409, "idempotency_conflict")
	case errors.Is(err, backfill.ErrDryRunRequired), errors.Is(err, backfill.ErrRegistrationNotActive), errors.Is(err, backfill.ErrRebuildRequired), errors.Is(err, backfill.ErrInProgress):
		failure(w, 409, publicBackfillCode(err))
	case errors.Is(err, backfill.ErrCostConfirmationRequired):
		e := apiError(409, "cost_confirmation_required")
		e.Message = "the estimated cost exceeds backfill.max_cost_without_confirmation; repeat the request with confirm_cost"
		send(w, 409, e)
	case errors.Is(err, backfill.ErrInvalid):
		failure(w, 422, "invalid_backfill")
	case err != nil:
		failure(w, 503, "storage_unavailable")
	case in.DryRun:
		send(w, 200, estimateToTransport(estimate))
	default:
		// The Operation and its dispatch intent are committed before this response.
		w.Header().Set("Location", "/v0/operations/"+op.ID)
		send(w, 202, operationToTransport(op))
	}
}

// publicBackfillCode is the code of a coded backfill refusal.
func publicBackfillCode(err error) string {
	for _, known := range []error{backfill.ErrDryRunRequired, backfill.ErrRegistrationNotActive, backfill.ErrRebuildRequired, backfill.ErrInProgress} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "conflict"
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
	if !scope.Allows(operations.BackfillPermission) {
		failure(w, 403, "forbidden")
		return
	}
	var in promotionRequest
	if !decodeInto(w, r, a.promotionSchema, &in) {
		return
	}
	p, err := a.Promotions.Promote(r.Context(), scope, space, in.Force)
	switch {
	case errors.Is(err, corpus.ErrForbidden):
		failure(w, 403, "forbidden")
	case errors.Is(err, corpus.ErrNotFound):
		failure(w, 404, "not_found")
	case errors.Is(err, backfill.ErrCoverageIncomplete):
		e := apiError(409, "coverage_incomplete")
		var incomplete *backfill.IncompleteError
		if errors.As(err, &incomplete) {
			p := incomplete.Promotion
			e.Message = fmt.Sprintf("vector space %s lacks a vector for %d current segments in %d Corpora; backfill them, or force the promotion", boundedPublicText(p.Served, 128), p.SegmentsMissing, p.CorporaIncomplete)
		}
		send(w, 409, e)
	case errors.Is(err, backfill.ErrNotEvaluation):
		failure(w, 422, "not_evaluation_space")
	case err != nil:
		failure(w, 503, "storage_unavailable")
	default:
		send(w, 200, transport.VectorSpacePromotion{ServedSpaceId: p.Served, PreviousSpaceId: p.Previous, GenerationsSwitched: int(p.GenerationsSwitched),
			CorporaIncomplete: int(p.CorporaIncomplete), SegmentsMissing: int(p.SegmentsMissing)})
	}
}
