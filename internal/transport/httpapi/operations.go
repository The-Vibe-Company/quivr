package httpapi

import (
	"errors"
	"net/http"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
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

func (a *API) handleGetOperation(w http.ResponseWriter, r *http.Request, scope corpus.Scope, operationID string) {
	if operationID == "" {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	op, err := a.Operations.Read(r.Context(), scope, operationID)
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
		return
	}
	send(w, 200, operationToTransport(op))
}

func (a *API) handleRebuildCorpusProjection(w http.ResponseWriter, r *http.Request, scope corpus.Scope, corpusID string) {
	if corpusID == "" {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	op, err := a.Operations.RequestRebuild(r.Context(), scope, corpusID, "", func() (string, string, error) {
		raw, ok := decodeRequest(w, r, a.schemas["ActionRequest"])
		if !ok {
			return "", "", errResponseWritten
		}
		key, _ := raw.(map[string]any)["idempotency_key"].(string)

		return corpusID, key, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	switch {
	case err != nil:
		writeError(w, err, publicerr.StorageUnavailable)
	default:
		// The Operation and its dispatch intent are committed before this response.
		w.Header().Set("Location", "/v0/operations/"+op.ID)
		send(w, 202, operationToTransport(op))
	}
}

// operationAction serves cancel, rerun, pause and resume. All are 202 with
// the resulting Operation: cancel, pause and resume return the current state
// (terminal outcomes unchanged), rerun the new linked Operation.
func (a *API) operationAction(w http.ResponseWriter, r *http.Request, scope corpus.Scope, id, action string) {
	if id == "" {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	load := func() (string, error) {
		raw, ok := decodeRequest(w, r, a.schemas["ActionRequest"])
		if !ok {
			return "", errResponseWritten
		}
		key, _ := raw.(map[string]any)["idempotency_key"].(string)
		return key, nil
	}
	prepare := func() (string, string, error) { key, err := load(); return id, key, err }
	prepareID := func() (string, error) { _, err := load(); return id, err }
	var op operations.Operation
	var err error
	switch action {
	case "cancel":
		op, err = a.Operations.Cancel(r.Context(), scope, "", "", prepare)
	case "pause":
		op, err = a.Operations.Pause(r.Context(), scope, "", prepareID)
	case "resume":
		op, err = a.Operations.Resume(r.Context(), scope, "", prepareID)
	default:
		op, err = a.Operations.Rerun(r.Context(), scope, "", "", prepare)
	}
	if errors.Is(err, errResponseWritten) {
		return
	}
	switch {
	case err != nil:
		writeError(w, err, publicerr.StorageUnavailable)
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
	if corpusID == "" {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	op, err := a.Operations.ConfigureRetrieval(r.Context(), scope, corpusID, "", corpus.Retrieval{}, func() (string, string, corpus.Retrieval, error) {
		raw, ok := decodeRequest(w, r, a.schemas["ConfigUpdate"])
		if !ok {
			return "", "", corpus.Retrieval{}, errResponseWritten
		}
		data := raw.(map[string]any)
		key, _ := data["idempotency_key"].(string)
		requested, _ := data["retrieval"].(map[string]any)
		cfg, err := a.Service.Resolve(requested)
		if err != nil {
			writeError(w, err, publicerr.InvalidMapping)
			return "", "", corpus.Retrieval{}, errResponseWritten
		}

		return corpusID, key, cfg, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	switch {
	case err != nil:
		writeError(w, err, publicerr.StorageUnavailable)
	default:
		// The Operation, its pinned configuration and dispatch intent are committed.
		w.Header().Set("Location", "/v0/operations/"+op.ID)
		send(w, 202, operationToTransport(op))
	}
}
