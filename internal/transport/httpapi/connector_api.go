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
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
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
		writeError(w, publicerr.NotFound, nil)
		return true
	}
	auth := connectors.APIAuth{}
	bearer := r.Header.Get("Authorization")
	if bearer != "" {
		if !strings.HasPrefix(bearer, "Bearer ") {
			writeError(w, publicerr.InvalidApiKey, nil)
			return true
		}
		credential := strings.TrimPrefix(bearer, "Bearer ")
		if scope, ok := a.Keys[credential]; ok {
			auth.Scope = &scope
		} else if strings.HasPrefix(credential, "qit_") {
			auth.InstanceToken = credential
		} else {
			writeError(w, publicerr.InvalidApiKey, nil)
			return true
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), relayTimeout)
	defer cancel()
	answer, err := a.Relay.DeliverAPIWithAuth(ctx, auth, parts[0], parts[2], connectors.Relayed{}, func() (connectors.Relayed, error) {
		body, err := io.ReadAll(io.LimitReader(r.Body, plugins.MaxRelayBodyBytes+1))
		if err != nil || len(body) > plugins.MaxRelayBodyBytes || len(r.URL.RawQuery) > 8192 || len(parts[2]) > 8192 {
			writeError(w, publicerr.RequestTooLarge, nil)
			return connectors.Relayed{}, errResponseWritten
		}
		headers := relayedHeaders(r.Header)
		delete(headers, "authorization")

		return connectors.Relayed{ClientIP: a.pushClientIP(r), IdempotencyKeys: r.Header.Values("Idempotency-Key"), Method: r.Method, Query: r.URL.RawQuery, Headers: headers, Body: body}, nil
	})
	if errors.Is(err, errResponseWritten) {
		return true
	}
	switch {
	case err != nil:
		writeError(w, err, publicerr.ConnectorsUnavailable)
	case answer.ErrorCode != "":
		if answer.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(answer.RetryAfter/time.Second)))
		}
		writeError(w, answer.PublicError(), nil)
	case answer.Allow != "":
		w.Header().Set("Allow", answer.Allow)
		writeError(w, publicerr.MethodNotAllowed, nil)
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
