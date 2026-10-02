package httpapi

import (
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
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
	if !scope.Allows(connectors.ActionConnectorAdmin) {
		failure(w, 403, "forbidden")
		return true
	}
	if a.Connectors.Store == nil || parts[0] == "" {
		failure(w, 404, "not_found")
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	ctx := r.Context()
	switch {
	case len(parts) == 2 && r.Method == "GET":
		q := r.URL.Query()
		for key, values := range q {
			if (key != "limit" && key != "after") || len(values) != 1 || (key == "after" && len(values[0]) > 128) {
				failure(w, 422, "invalid_query")
				return true
			}
		}
		limit, ok := pageLimit(w, q, 100, 100)
		if !ok {
			return true
		}
		tokens, err := a.Connectors.ListTokens(ctx, scope, parts[0], q.Get("after"), limit+1)
		if err != nil {
			connectorFailure(w, err)
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
		issued, err := a.Connectors.CreateToken(ctx, scope, parts[0])
		if err != nil {
			connectorFailure(w, err)
			return true
		}
		w.Header().Set("Location", r.URL.Path+"/"+issued.Token.ID)
		send(w, 201, transport.ConnectorTokenCreated{Token: tokenToTransport(issued.Token), Secret: issued.Secret})
	case len(parts) == 4 && parts[2] != "" && parts[3] == "rotate" && r.Method == "POST":
		issued, err := a.Connectors.RotateToken(ctx, scope, parts[0], parts[2])
		if err != nil {
			connectorFailure(w, err)
			return true
		}
		w.Header().Set("Location", "/v0/connectors/"+parts[0]+"/tokens/"+issued.Token.ID)
		send(w, 201, transport.ConnectorTokenCreated{Token: tokenToTransport(issued.Token), Secret: issued.Secret})
	case len(parts) == 3 && parts[2] != "" && r.Method == "DELETE":
		token, err := a.Connectors.RevokeToken(ctx, scope, parts[0], parts[2])
		if err != nil {
			connectorFailure(w, err)
			return true
		}
		send(w, 200, tokenToTransport(token))
	case len(parts) == 2, len(parts) == 3 && parts[2] != "", len(parts) == 4 && parts[2] != "" && parts[3] == "rotate":
		failure(w, 405, "method_not_allowed")
	default:
		failure(w, 404, "not_found")
	}
	return true
}
