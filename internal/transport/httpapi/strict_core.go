package httpapi

import (
	"context"
	"net/http"

	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

func (a *API) ListCorpora(ctx context.Context, in transport.ListCorporaRequestObject) (transport.ListCorporaResponseObject, error) {
	return transport.ListCorporaResponseFunc(func(w http.ResponseWriter) { a.list(w, in.HTTPRequest, requestScope(ctx)) }), nil
}
func (a *API) CreateCorpus(ctx context.Context, in transport.CreateCorpusRequestObject) (transport.CreateCorpusResponseObject, error) {
	return transport.CreateCorpusResponseFunc(func(w http.ResponseWriter) { a.create(w, in.HTTPRequest, requestScope(ctx)) }), nil
}
func (a *API) GetCorpus(ctx context.Context, in transport.GetCorpusRequestObject) (transport.GetCorpusResponseObject, error) {
	return transport.GetCorpusResponseFunc(func(w http.ResponseWriter) {
		c, err := a.Service.Read(ctx, requestScope(ctx), in.CorpusId)
		if err != nil {
			writeError(w, err, publicerr.StorageUnavailable)
		} else {
			send(w, 200, c)
		}
	}), nil
}
func (a *API) SearchRecords(ctx context.Context, in transport.SearchRecordsRequestObject) (transport.SearchRecordsResponseObject, error) {
	return transport.SearchRecordsResponseFunc(func(w http.ResponseWriter) { a.search(w, in.HTTPRequest, requestScope(ctx)) }), nil
}
func (a *API) ListSearchProfiles(ctx context.Context, in transport.ListSearchProfilesRequestObject) (transport.ListSearchProfilesResponseObject, error) {
	return transport.ListSearchProfilesResponseFunc(func(w http.ResponseWriter) { a.searchProfiles(w, requestScope(ctx)) }), nil
}
func (a *API) PollChanges(ctx context.Context, in transport.PollChangesRequestObject) (transport.PollChangesResponseObject, error) {
	return transport.PollChangesResponseFunc(func(w http.ResponseWriter) { a.pollChanges(w, in.HTTPRequest, requestScope(ctx)) }), nil
}
func (a *API) StreamChanges(ctx context.Context, in transport.StreamChangesRequestObject) (transport.StreamChangesResponseObject, error) {
	return transport.StreamChangesResponseFunc(func(w http.ResponseWriter) { a.streamChanges(w, in.HTTPRequest, requestScope(ctx)) }), nil
}
func (a *API) ListVectorSpaces(ctx context.Context, in transport.ListVectorSpacesRequestObject) (transport.ListVectorSpacesResponseObject, error) {
	return transport.ListVectorSpacesResponseFunc(func(w http.ResponseWriter) {
		if in.CorpusId == "" {
			writeError(w, publicerr.NotFound, nil)
			return
		}
		a.listVectorSpaces(w, in.HTTPRequest, requestScope(ctx), in.CorpusId)
	}), nil
}
func (a *API) RelayConnectorChallenge(ctx context.Context, in transport.RelayConnectorChallengeRequestObject) (transport.RelayConnectorChallengeResponseObject, error) {
	return transport.RelayConnectorChallengeResponseFunc(func(w http.ResponseWriter) { a.relayDelivery(w, in.HTTPRequest) }), nil
}
func (a *API) RelayConnectorDelivery(ctx context.Context, in transport.RelayConnectorDeliveryRequestObject) (transport.RelayConnectorDeliveryResponseObject, error) {
	return transport.RelayConnectorDeliveryResponseFunc(func(w http.ResponseWriter) { a.relayDelivery(w, in.HTTPRequest) }), nil
}
