package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/backfill"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/quarantine"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// WithOperations enables scoped rebuild initiation, Operation reads, cancel and rerun.
func WithOperations(service operations.Service) Option {
	return func(a *API) { a.Operations = service }
}

func operationToTransport(op operations.Operation) transport.Operation {
	out := transport.Operation{OperationId: op.ID, Kind: op.Kind, State: transport.OperationState(op.State), Counters: map[string]int{}, Errors: []transport.Error{}}
	for k, v := range op.Counters {
		out.Counters[k] = v
	}
	for _, e := range op.Errors {
		out.Errors = append(out.Errors, transport.Error{Code: e.Code, Message: e.Message, Retryable: e.Retryable})
	}
	if op.CorpusID != "" {
		out.CorpusId = &op.CorpusID
	}
	if op.PreviousID != "" {
		out.PreviousOperationId = &op.PreviousID
	}
	if op.State == operations.StateSucceeded && op.ResultGenerationID != "" {
		out.Result = &transport.ProjectionRebuildResult{ProjectionGenerationId: op.ResultGenerationID}
	}
	out.Backfill = backfillToTransport(op.Backfill)
	out.QuarantineReprocess = reprocessToTransport(op.Reprocess)
	return out
}

func (a *API) operationRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	if rest, ok := strings.CutPrefix(r.URL.Path, "/v0/operations/"); ok {
		if id, action, ok := strings.Cut(rest, "/"); ok && id != "" && (action == "cancel" || action == "rerun" || action == "pause" || action == "resume") {
			a.operationAction(w, r, scope, id, action)
			return true
		}
	}
	if id, ok := strings.CutPrefix(r.URL.Path, "/v0/operations/"); ok && id != "" && !strings.Contains(id, "/") {
		if r.Method != "GET" {
			failure(w, 405, "method_not_allowed")
			return true
		}
		op, err := a.Operations.Read(r.Context(), scope, id)
		switch {
		case errors.Is(err, corpus.ErrForbidden):
			failure(w, 403, "forbidden")
		case errors.Is(err, corpus.ErrNotFound):
			failure(w, 404, "not_found")
		case err != nil:
			failure(w, 503, "storage_unavailable")
		default:
			send(w, 200, operationToTransport(op))
		}
		return true
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/v0/corpora/")
	if configured, isConfig := strings.CutSuffix(rest, "/retrieval"); ok && isConfig && configured != "" && !strings.Contains(configured, "/") {
		a.configureRetrieval(w, r, scope, configured)
		return true
	}
	corpusID, ok2 := strings.CutSuffix(rest, "/rebuilds")
	if !ok || !ok2 || corpusID == "" || strings.Contains(corpusID, "/") {
		return false
	}
	if r.Method != "POST" {
		failure(w, 405, "method_not_allowed")
		return true
	}
	if !scope.Allows("projections:rebuild") {
		failure(w, 403, "forbidden")
		return true
	}
	if !scope.Contains(corpusID) {
		failure(w, 404, "not_found")
		return true
	}
	raw, ok := decodeRequest(w, r, a.actionSchema)
	if !ok {
		return true
	}
	key, _ := raw.(map[string]any)["idempotency_key"].(string)
	op, err := a.Operations.RequestRebuild(r.Context(), scope, corpusID, key)
	switch {
	case errors.Is(err, corpus.ErrForbidden):
		failure(w, 403, "forbidden")
	case errors.Is(err, corpus.ErrNotFound):
		failure(w, 404, "not_found")
	case errors.Is(err, operations.ErrConflict):
		failure(w, 409, "idempotency_conflict")
	case err != nil:
		failure(w, 503, "storage_unavailable")
	default:
		// The Operation and its dispatch intent are committed before this response.
		w.Header().Set("Location", "/v0/operations/"+op.ID)
		send(w, 202, operationToTransport(op))
	}
	return true
}

// operationAction serves cancel, rerun, pause and resume. All are 202 with
// the resulting Operation: cancel, pause and resume return the current state
// (terminal outcomes unchanged), rerun the new linked Operation.
func (a *API) operationAction(w http.ResponseWriter, r *http.Request, scope corpus.Scope, id, action string) {
	if r.Method != "POST" {
		failure(w, 405, "method_not_allowed")
		return
	}
	if !scope.Allows("operations:write") {
		failure(w, 403, "forbidden")
		return
	}
	raw, ok := decodeRequest(w, r, a.actionSchema)
	if !ok {
		return
	}
	key, _ := raw.(map[string]any)["idempotency_key"].(string)
	var op operations.Operation
	var err error
	switch action {
	case "cancel":
		op, err = a.Operations.Cancel(r.Context(), scope, id, key)
	case "pause":
		op, err = a.Operations.Pause(r.Context(), scope, id)
	case "resume":
		op, err = a.Operations.Resume(r.Context(), scope, id)
	default:
		op, err = a.Operations.Rerun(r.Context(), scope, id, key)
	}
	switch {
	case errors.Is(err, corpus.ErrForbidden):
		failure(w, 403, "forbidden")
	case errors.Is(err, corpus.ErrNotFound):
		failure(w, 404, "not_found")
	case errors.Is(err, operations.ErrNotTerminal):
		failure(w, 409, "operation_not_terminal")
	case errors.Is(err, operations.ErrConflict):
		failure(w, 409, "idempotency_conflict")
	case errors.Is(err, operations.ErrUnsupportedKind):
		failure(w, 422, "unsupported_operation_kind")
	case errors.Is(err, backfill.ErrInProgress):
		// A backfill rerun while another backfill of its Corpus runs.
		failure(w, 409, "backfill_in_progress")
	case errors.Is(err, quarantine.ErrInProgress):
		// A reprocess rerun while another reprocess of its Corpus runs.
		failure(w, 409, "reprocess_in_progress")
	case errors.Is(err, backfill.ErrRegistrationNotActive):
		// A backfill rerun after its ingestion plugin left the active plan.
		failure(w, 409, "registration_not_active")
	case err != nil:
		failure(w, 503, "storage_unavailable")
	default:
		if action == "rerun" {
			w.Header().Set("Location", "/v0/operations/"+op.ID)
		}
		send(w, 202, operationToTransport(op))
	}
}

// configureRetrieval validates a Corpus retrieval configuration and accepts the
// Operation that builds and activates its replacement generation. The Corpus
// keeps serving its prior effective configuration until validated cutover.
func (a *API) configureRetrieval(w http.ResponseWriter, r *http.Request, scope corpus.Scope, corpusID string) {
	if r.Method != "PUT" {
		failure(w, 405, "method_not_allowed")
		return
	}
	if !scope.Allows("corpora:write") || !scope.Allows("operations:write") {
		failure(w, 403, "forbidden")
		return
	}
	if !scope.Contains(corpusID) {
		failure(w, 404, "not_found")
		return
	}
	raw, ok := decodeRequest(w, r, a.configSchema)
	if !ok {
		return
	}
	data := raw.(map[string]any)
	key, _ := data["idempotency_key"].(string)
	requested, _ := data["retrieval"].(map[string]any)
	cfg, err := a.Service.Resolve(requested)
	if err != nil {
		failure(w, 422, publicCode(err, "invalid_mapping"))
		return
	}
	op, err := a.Operations.ConfigureRetrieval(r.Context(), scope, corpusID, key, cfg)
	switch {
	case errors.Is(err, corpus.ErrForbidden):
		failure(w, 403, "forbidden")
	case errors.Is(err, corpus.ErrNotFound):
		failure(w, 404, "not_found")
	case errors.Is(err, operations.ErrConflict):
		failure(w, 409, "idempotency_conflict")
	case err != nil:
		failure(w, 503, "storage_unavailable")
	default:
		// The Operation, its pinned configuration and dispatch intent are committed.
		w.Header().Set("Location", "/v0/operations/"+op.ID)
		send(w, 202, operationToTransport(op))
	}
}
