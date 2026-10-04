package httpapi

import (
	"context"
	"net/http"

	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

func (a *API) GetOperation(ctx context.Context, in transport.GetOperationRequestObject) (transport.GetOperationResponseObject, error) {
	return transport.GetOperationResponseFunc(func(w http.ResponseWriter) {
		a.handleGetOperation(w, in.HTTPRequest, requestScope(ctx), in.OperationId)
	}), nil
}

func (a *API) ConfigureRetrieval(ctx context.Context, in transport.ConfigureRetrievalRequestObject) (transport.ConfigureRetrievalResponseObject, error) {
	return transport.ConfigureRetrievalResponseFunc(func(w http.ResponseWriter) { a.configureRetrieval(w, in.HTTPRequest, requestScope(ctx), in.CorpusId) }), nil
}

func (a *API) CancelOperation(ctx context.Context, in transport.CancelOperationRequestObject) (transport.CancelOperationResponseObject, error) {
	return transport.CancelOperationResponseFunc(func(w http.ResponseWriter) {
		a.operationAction(w, in.HTTPRequest, requestScope(ctx), in.OperationId, "cancel")
	}), nil
}

func (a *API) RerunOperation(ctx context.Context, in transport.RerunOperationRequestObject) (transport.RerunOperationResponseObject, error) {
	return transport.RerunOperationResponseFunc(func(w http.ResponseWriter) {
		a.operationAction(w, in.HTTPRequest, requestScope(ctx), in.OperationId, "rerun")
	}), nil
}

func (a *API) PauseOperation(ctx context.Context, in transport.PauseOperationRequestObject) (transport.PauseOperationResponseObject, error) {
	return transport.PauseOperationResponseFunc(func(w http.ResponseWriter) {
		a.operationAction(w, in.HTTPRequest, requestScope(ctx), in.OperationId, "pause")
	}), nil
}

func (a *API) ResumeOperation(ctx context.Context, in transport.ResumeOperationRequestObject) (transport.ResumeOperationResponseObject, error) {
	return transport.ResumeOperationResponseFunc(func(w http.ResponseWriter) {
		a.operationAction(w, in.HTTPRequest, requestScope(ctx), in.OperationId, "resume")
	}), nil
}

func (a *API) RebuildCorpusProjection(ctx context.Context, in transport.RebuildCorpusProjectionRequestObject) (transport.RebuildCorpusProjectionResponseObject, error) {
	return transport.RebuildCorpusProjectionResponseFunc(func(w http.ResponseWriter) {
		a.handleRebuildCorpusProjection(w, in.HTTPRequest, requestScope(ctx), in.CorpusId)
	}), nil
}
