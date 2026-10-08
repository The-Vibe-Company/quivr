package httpapi

import (
	"context"
	"net/http"

	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

func (a *API) CreateConnector(ctx context.Context, in transport.CreateConnectorRequestObject) (transport.CreateConnectorResponseObject, error) {
	return transport.CreateConnectorResponseFunc(func(w http.ResponseWriter) { a.handleCreateConnector(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) ListConnectors(ctx context.Context, in transport.ListConnectorsRequestObject) (transport.ListConnectorsResponseObject, error) {
	return transport.ListConnectorsResponseFunc(func(w http.ResponseWriter) { a.listConnectors(w, in.HTTPRequest, requestScope(ctx)) }), nil
}

func (a *API) CreateConnectorToken(ctx context.Context, in transport.CreateConnectorTokenRequestObject) (transport.CreateConnectorTokenResponseObject, error) {
	return transport.CreateConnectorTokenResponseFunc(func(w http.ResponseWriter) {
		a.handleCreateConnectorToken(w, in.HTTPRequest, requestScope(ctx), in.ConnectorId)
	}), nil
}

func (a *API) ListConnectorTokens(ctx context.Context, in transport.ListConnectorTokensRequestObject) (transport.ListConnectorTokensResponseObject, error) {
	return transport.ListConnectorTokensResponseFunc(func(w http.ResponseWriter) {
		a.handleListConnectorTokens(w, in.HTTPRequest, requestScope(ctx), in.ConnectorId)
	}), nil
}

func (a *API) RotateConnectorToken(ctx context.Context, in transport.RotateConnectorTokenRequestObject) (transport.RotateConnectorTokenResponseObject, error) {
	return transport.RotateConnectorTokenResponseFunc(func(w http.ResponseWriter) {
		a.handleRotateConnectorToken(w, in.HTTPRequest, requestScope(ctx), in.ConnectorId, in.TokenId)
	}), nil
}

func (a *API) RevokeConnectorToken(ctx context.Context, in transport.RevokeConnectorTokenRequestObject) (transport.RevokeConnectorTokenResponseObject, error) {
	return transport.RevokeConnectorTokenResponseFunc(func(w http.ResponseWriter) {
		a.handleRevokeConnectorToken(w, in.HTTPRequest, requestScope(ctx), in.ConnectorId, in.TokenId)
	}), nil
}

func (a *API) PushConnectorAPI(ctx context.Context, in transport.PushConnectorAPIRequestObject) (transport.PushConnectorAPIResponseObject, error) {
	return transport.PushConnectorAPIResponseFunc(func(w http.ResponseWriter) { a.handleConnectorAPI(w, in.HTTPRequest, in.ConnectorId, in.Path) }), nil
}

func (a *API) ChallengeConnectorAPI(ctx context.Context, in transport.ChallengeConnectorAPIRequestObject) (transport.ChallengeConnectorAPIResponseObject, error) {
	return transport.ChallengeConnectorAPIResponseFunc(func(w http.ResponseWriter) { a.handleConnectorAPI(w, in.HTTPRequest, in.ConnectorId, in.Path) }), nil
}

func (a *API) GetConnector(ctx context.Context, in transport.GetConnectorRequestObject) (transport.GetConnectorResponseObject, error) {
	return transport.GetConnectorResponseFunc(func(w http.ResponseWriter) {
		a.handleGetConnector(w, in.HTTPRequest, requestScope(ctx), in.ConnectorId)
	}), nil
}

func (a *API) DisableConnector(ctx context.Context, in transport.DisableConnectorRequestObject) (transport.DisableConnectorResponseObject, error) {
	return transport.DisableConnectorResponseFunc(func(w http.ResponseWriter) {
		a.handleDisableConnector(w, in.HTTPRequest, requestScope(ctx), in.ConnectorId)
	}), nil
}

func (a *API) PauseConnector(ctx context.Context, in transport.PauseConnectorRequestObject) (transport.PauseConnectorResponseObject, error) {
	return transport.PauseConnectorResponseFunc(func(w http.ResponseWriter) {
		a.handleConnectorPause(w, in.HTTPRequest, requestScope(ctx), in.ConnectorId, true)
	}), nil
}

func (a *API) ResumeConnector(ctx context.Context, in transport.ResumeConnectorRequestObject) (transport.ResumeConnectorResponseObject, error) {
	return transport.ResumeConnectorResponseFunc(func(w http.ResponseWriter) {
		a.handleConnectorPause(w, in.HTTPRequest, requestScope(ctx), in.ConnectorId, false)
	}), nil
}

func (a *API) ReplaceConnectorCredential(ctx context.Context, in transport.ReplaceConnectorCredentialRequestObject) (transport.ReplaceConnectorCredentialResponseObject, error) {
	return transport.ReplaceConnectorCredentialResponseFunc(func(w http.ResponseWriter) {
		a.handleReplaceConnectorCredential(w, in.HTTPRequest, requestScope(ctx), in.ConnectorId)
	}), nil
}

func (a *API) ChangeConnectorSchedule(ctx context.Context, in transport.ChangeConnectorScheduleRequestObject) (transport.ChangeConnectorScheduleResponseObject, error) {
	return transport.ChangeConnectorScheduleResponseFunc(func(w http.ResponseWriter) {
		a.handleChangeConnectorSchedule(w, in.HTTPRequest, requestScope(ctx), in.ConnectorId)
	}), nil
}

func (a *API) RequestConnectorRun(ctx context.Context, in transport.RequestConnectorRunRequestObject) (transport.RequestConnectorRunResponseObject, error) {
	return transport.RequestConnectorRunResponseFunc(func(w http.ResponseWriter) {
		a.handleRequestConnectorRun(w, in.HTTPRequest, requestScope(ctx), in.ConnectorId)
	}), nil
}

func (a *API) ListConnectorKinds(ctx context.Context, in transport.ListConnectorKindsRequestObject) (transport.ListConnectorKindsResponseObject, error) {
	return transport.ListConnectorKindsResponseFunc(func(w http.ResponseWriter) { a.connectorKinds(w, in.HTTPRequest, requestScope(ctx)) }), nil
}
