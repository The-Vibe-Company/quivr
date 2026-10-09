package httpapi

import (
	"errors"
	"net/http"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/routing"
)

func WithRoutingOperations(service routing.Service) Option {
	return func(a *API) { a.RoutingOperations = &service }
}

func (a *API) requestRouting(w http.ResponseWriter, r *http.Request, scope corpus.Scope, command routing.Command, prepare ...func() (routing.Command, error)) {
	if a.RoutingOperations == nil {
		if err := scope.Require(corpus.ActionPluginActivate); err != nil {
			writeError(w, err, nil)
		} else {
			writeError(w, publicerr.NotFound, nil)
		}
		return
	}
	op, err := a.RoutingOperations.Request(r.Context(), scope, command, prepare...)
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.StorageUnavailable)
		return
	}
	w.Header().Set("Location", "/v0/operations/"+op.ID)
	send(w, http.StatusAccepted, operationToTransport(op))
}
