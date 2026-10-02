package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

func tokenToTransport(token connectors.TokenInfo) transport.ConnectorToken {
	return transport.ConnectorToken{TokenId: token.ID, Prefix: token.Prefix, CreatedAt: token.CreatedAt.UTC(), RotatedAt: token.RotatedAt, RevokedAt: token.RevokedAt, ValidUntil: token.ValidUntil}
}

func (a *API) connectorTokenRoutes(w http.ResponseWriter, r *http.Request, scope corpus.Scope) bool {
	if !strings.HasPrefix(r.URL.Path, "/v0/connectors/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v0/connectors/"), "/")
	if len(parts) < 2 || parts[1] != "tokens" {
		return false
	}
	prepare := func() error {
		if parts[0] == "" {
			writeError(w, publicerr.NotFound, nil)
			return errResponseWritten
		}
		w.Header().Set("Cache-Control", "no-store")
		return nil
	}
	ctx := r.Context()
	switch {
	case len(parts) == 2 && r.Method == "GET":
		var limit int
		var ok bool
		tokens, err := a.Connectors.ListTokens(ctx, scope, "", "", 0, func() (string, string, int, error) {
			if err := prepare(); err != nil {
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

			return parts[0], q.Get("after"), limit + 1, nil
		})
		if errors.Is(err, errResponseWritten) {
			return true
		}
		if err != nil {
			writeError(w, err, publicerr.ConnectorsUnavailable)
			return true
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
	case len(parts) == 2 && r.Method == "POST":
		issued, err := a.Connectors.CreateToken(ctx, scope, "", func() (string, error) {
			if err := prepare(); err != nil {
				return "", err
			}
			return parts[0], nil
		})
		if errors.Is(err, errResponseWritten) {
			return true
		}
		if err != nil {
			writeError(w, err, publicerr.ConnectorsUnavailable)
			return true
		}
		w.Header().Set("Location", r.URL.Path+"/"+issued.Token.ID)
		send(w, 201, transport.ConnectorTokenCreated{Token: tokenToTransport(issued.Token), Secret: issued.Secret})
	case len(parts) == 4 && parts[2] != "" && parts[3] == "rotate" && r.Method == "POST":
		issued, err := a.Connectors.RotateToken(ctx, scope, "", "", func() (string, string, error) {
			if err := prepare(); err != nil {
				return "", "", err
			}
			return parts[0], parts[2], nil
		})
		if errors.Is(err, errResponseWritten) {
			return true
		}
		if err != nil {
			writeError(w, err, publicerr.ConnectorsUnavailable)
			return true
		}
		w.Header().Set("Location", "/v0/connectors/"+parts[0]+"/tokens/"+issued.Token.ID)
		send(w, 201, transport.ConnectorTokenCreated{Token: tokenToTransport(issued.Token), Secret: issued.Secret})
	case len(parts) == 3 && parts[2] != "" && r.Method == "DELETE":
		token, err := a.Connectors.RevokeToken(ctx, scope, "", "", func() (string, string, error) {
			if err := prepare(); err != nil {
				return "", "", err
			}
			return parts[0], parts[2], nil
		})
		if errors.Is(err, errResponseWritten) {
			return true
		}
		if err != nil {
			writeError(w, err, publicerr.ConnectorsUnavailable)
			return true
		}
		send(w, 200, tokenToTransport(token))
	default:
		_, err := a.Connectors.CreateToken(ctx, scope, "", func() (string, error) {
			if err := prepare(); err != nil {
				return "", err
			}
			if len(parts) == 2 || (len(parts) == 3 && parts[2] != "") || (len(parts) == 4 && parts[2] != "" && parts[3] == "rotate") {
				writeError(w, publicerr.MethodNotAllowed, nil)
			} else {
				writeError(w, publicerr.NotFound, nil)
			}
			return "", errResponseWritten
		})
		if !errors.Is(err, errResponseWritten) && err != nil {
			writeError(w, err, publicerr.ConnectorsUnavailable)
		}
	}
	return true
}
