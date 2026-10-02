package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

func (a *API) connectorAPIRoute(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/v0/connectors/") {
		return false
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/v0/connectors/"), "/", 3)
	if len(parts) < 2 || parts[1] != "api" {
		return false
	}
	if a.Relay == nil || len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		failure(w, 404, "not_found")
		return true
	}
	bearer := r.Header.Get("Authorization")
	if !strings.HasPrefix(bearer, "Bearer ") {
		failure(w, 401, "invalid_api_key")
		return true
	}
	credential := strings.TrimPrefix(bearer, "Bearer ")
	auth := connectors.APIAuth{}
	if scope, ok := a.Keys[credential]; ok {
		auth.Scope = &scope
		if !scope.Allows(corpus.ActionConnectorPush) {
			failure(w, 403, "forbidden")
			return true
		}
	} else if strings.HasPrefix(credential, "qit_") {
		auth.InstanceToken = credential
	} else {
		failure(w, 401, "invalid_api_key")
		return true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, plugins.MaxRelayBodyBytes+1))
	if err != nil || len(body) > plugins.MaxRelayBodyBytes || len(r.URL.RawQuery) > 8192 || len(parts[2]) > 8192 {
		failure(w, 413, "request_too_large")
		return true
	}
	headers := relayedHeaders(r.Header)
	delete(headers, "authorization")
	ctx, cancel := context.WithTimeout(r.Context(), relayTimeout)
	defer cancel()
	answer, err := a.Relay.DeliverAPIWithAuth(ctx, auth, parts[0], parts[2], connectors.Relayed{Method: r.Method, Query: r.URL.RawQuery, Headers: headers, Body: body})
	switch {
	case errors.Is(err, connectors.ErrInvalidInstanceToken):
		failure(w, 401, "invalid_instance_token")
	case errors.Is(err, corpus.ErrForbidden):
		failure(w, 403, "forbidden")
	case errors.Is(err, corpus.ErrNotFound):
		failure(w, 404, "not_found")
	case errors.Is(err, connectors.ErrInvalidAPIBody):
		failure(w, 400, "invalid_json")
	case errors.Is(err, connectors.ErrInvalidAPIRequest):
		invalid(w, "invalid_schema", "/body")
	case errors.Is(err, connectors.ErrPushItemRejected):
		failure(w, 422, "item_rejected")
	case err != nil:
		failure(w, 503, "connectors_unavailable")
	case answer.ErrorCode != "":
		if answer.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(answer.RetryAfter/time.Second)))
		}
		failure(w, answer.Status, answer.ErrorCode)
	case answer.Allow != "":
		w.Header().Set("Allow", answer.Allow)
		failure(w, 405, "method_not_allowed")
	case answer.Receipts != nil:
		receipts := make([]transport.Receipt, 0, len(answer.Receipts))
		for _, receipt := range answer.Receipts {
			receipts = append(receipts, receiptToTransport(receipt))
		}
		send(w, 202, transport.ConnectorPushReceipts{Receipts: receipts})
	default:
		w.Header().Set("Quivr-Response-Origin", "plugin")
		if answer.ContentType != "" {
			w.Header().Set("Content-Type", answer.ContentType)
		} else if answer.Body != "" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		if answer.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(answer.RetryAfter/time.Second)))
		}
		w.WriteHeader(answer.Status)
		_, _ = io.WriteString(w, answer.Body)
	}
	return true
}
