package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// WithOperations enables scoped rebuild initiation and Operation reads.
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
	return out
}

func (a *API) operationRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
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
