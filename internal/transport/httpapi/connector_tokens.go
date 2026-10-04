package httpapi

import (
	"errors"
	"net/http"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

func tokenToTransport(token connectors.TokenInfo) transport.ConnectorToken {
	return transport.ConnectorToken{TokenId: token.ID, Prefix: token.Prefix, CreatedAt: token.CreatedAt.UTC(), RotatedAt: token.RotatedAt, RevokedAt: token.RevokedAt, ValidUntil: token.ValidUntil}
}

func prepareConnectorToken(w http.ResponseWriter, connectorID string) error {
	if connectorID == "" {
		writeError(w, publicerr.NotFound, nil)
		return errResponseWritten
	}
	w.Header().Set("Cache-Control", "no-store")
	return nil
}

func (a *API) handleListConnectorTokens(w http.ResponseWriter, r *http.Request, scope corpus.Scope, connectorID string) {
	var limit int
	var ok bool
	tokens, err := a.Connectors.ListTokens(r.Context(), scope, "", "", 0, func() (string, string, int, error) {
		if err := prepareConnectorToken(w, connectorID); err != nil {
			return "", "", 0, err
		}
		q := r.URL.Query()
		for key, values := range q {
			if (key != "limit" && key != "after") || len(values) != 1 || (key == "after" && len(values[0]) > 128) {
				writeError(w, publicerr.InvalidQuery, nil)
				return "", "", 0, errResponseWritten
			}
		}
		limit, ok = pageLimit(w, q, 100, 100)
		if !ok {
			return "", "", 0, errResponseWritten
		}

		return connectorID, q.Get("after"), limit + 1, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.ConnectorsUnavailable)
		return
	}
	page := transport.ConnectorTokenList{Items: []transport.ConnectorToken{}}
	for i, token := range tokens {
		if i == limit {
			after := tokens[i-1].ID
			page.NextAfter = &after
			break
		}
		page.Items = append(page.Items, tokenToTransport(token))
	}
	send(w, 200, page)
}

func (a *API) handleCreateConnectorToken(w http.ResponseWriter, r *http.Request, scope corpus.Scope, connectorID string) {
	issued, err := a.Connectors.CreateToken(r.Context(), scope, "", func() (string, error) {
		if err := prepareConnectorToken(w, connectorID); err != nil {
			return "", err
		}
		return connectorID, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.ConnectorsUnavailable)
		return
	}
	w.Header().Set("Location", "/v0/connectors/"+connectorID+"/tokens/"+issued.Token.ID)
	send(w, 201, transport.ConnectorTokenCreated{Token: tokenToTransport(issued.Token), Secret: issued.Secret})
}

func (a *API) handleRotateConnectorToken(w http.ResponseWriter, r *http.Request, scope corpus.Scope, connectorID, tokenID string) {
	if tokenID == "" {
		a.connectorTokenFallback(w, r, scope, connectorID, "")
		return
	}
	issued, err := a.Connectors.RotateToken(r.Context(), scope, "", "", func() (string, string, error) {
		if err := prepareConnectorToken(w, connectorID); err != nil {
			return "", "", err
		}
		return connectorID, tokenID, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.ConnectorsUnavailable)
		return
	}
	w.Header().Set("Location", "/v0/connectors/"+connectorID+"/tokens/"+issued.Token.ID)
	send(w, 201, transport.ConnectorTokenCreated{Token: tokenToTransport(issued.Token), Secret: issued.Secret})
}

func (a *API) handleRevokeConnectorToken(w http.ResponseWriter, r *http.Request, scope corpus.Scope, connectorID, tokenID string) {
	if tokenID == "" {
		a.connectorTokenFallback(w, r, scope, connectorID, "")
		return
	}
	token, err := a.Connectors.RevokeToken(r.Context(), scope, "", "", func() (string, string, error) {
		if err := prepareConnectorToken(w, connectorID); err != nil {
			return "", "", err
		}
		return connectorID, tokenID, nil
	})
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		writeError(w, err, publicerr.ConnectorsUnavailable)
		return
	}
	send(w, 200, tokenToTransport(token))
}

// connectorTokenFallback keeps the legacy service boundary for paths under
// /v0/connectors/{connector_id}/tokens that the strict operation set cannot
// dispatch. shape is "collection", "token", or "rotate" for known resource
// shapes with a wrong method; every other shape is a not-found path.
func (a *API) connectorTokenFallback(w http.ResponseWriter, r *http.Request, scope corpus.Scope, connectorID, shape string) {
	_, err := a.Connectors.CreateToken(r.Context(), scope, "", func() (string, error) {
		if err := prepareConnectorToken(w, connectorID); err != nil {
			return "", err
		}
		switch shape {
		case "collection", "token", "rotate":
			writeError(w, publicerr.MethodNotAllowed, nil)
		default:
			writeError(w, publicerr.NotFound, nil)
		}
		return "", errResponseWritten
	})
	if !errors.Is(err, errResponseWritten) && err != nil {
		writeError(w, err, publicerr.ConnectorsUnavailable)
	}
}
