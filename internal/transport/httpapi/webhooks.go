package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/connectors"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// WithRelay enables the public webhook routes of push Connector Instances.
func WithRelay(relay connectors.Relay) Option {
	return func(a *API) { a.Relay = &relay }
}

// relayTimeout bounds one relayed delivery: the plugin's answer (capped at 8
// s) and the ingestion of its items, within the server's write timeout.
const relayTimeout = 9 * time.Second

// hopByHop headers describe one connection, not the delivery, and Cookie is
// never relayed.
var hopByHop = map[string]bool{"connection": true, "keep-alive": true, "proxy-authenticate": true, "proxy-authorization": true,
	"proxy-connection": true, "te": true, "trailer": true, "transfer-encoding": true, "upgrade": true, "cookie": true}

var relayedHeaderName = regexp.MustCompile("^[a-z0-9!#$%&'*+.^_`|~-]{1,128}$")

// isWebhookRoute reports whether the path is a public webhook route, which
// is served without an API key.
func isWebhookRoute(path string) bool { return strings.HasPrefix(path, connectors.WebhookPath) }

// relayDelivery serves a public webhook route: the source authenticates to
// the connector plugin (a signature, a challenge), never with an API key.
// The request is bounded before any lookup, an unknown route is 404 before
// any plugin call, and the source gets the plugin's answer.
func (a *API) relayDelivery(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, connectors.WebhookPath)
	if a.Relay == nil || id == "" || strings.Contains(id, "/") {
		failure(w, 404, "not_found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		failure(w, 405, "method_not_allowed")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, plugins.MaxRelayBodyBytes+1))
	if err != nil || len(body) > plugins.MaxRelayBodyBytes || len(r.URL.RawQuery) > 8192 {
		// The receive contract bounds the relayed body and query.
		failure(w, 413, "request_too_large")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), relayTimeout)
	defer cancel()
	answer, err := a.Relay.Deliver(ctx, id, connectors.Relayed{ClientIP: a.pushClientIP(r), IdempotencyKeys: r.Header.Values("Idempotency-Key"), Method: r.Method, Query: r.URL.RawQuery, Headers: relayedHeaders(r.Header), Body: body})
	if errors.Is(err, connectors.ErrNoWebhook) {
		failure(w, 404, "not_found")
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, connectors.ErrInvalidSignature):
			failure(w, 401, "invalid_signature")
		case errors.Is(err, connectors.ErrReplay):
			failure(w, 409, "push_replayed")
		case errors.Is(err, connectors.ErrInvalidIdempotencyKey):
			failure(w, 400, "invalid_idempotency_key")
		case errors.Is(err, connectors.ErrInvalidAPIBody):
			failure(w, 400, "invalid_json")
		case errors.Is(err, connectors.ErrInvalidAPIRequest):
			invalid(w, "invalid_schema", "/body")
		case errors.Is(err, connectors.ErrPushItemRejected):
			failure(w, 422, "item_rejected")
		default:
			failure(w, 503, "connectors_unavailable")
		}
		return
	}
	if answer.DeclaredAPI && answer.ErrorCode != "" {
		if answer.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(answer.RetryAfter/time.Second)))
		}
		failure(w, answer.Status, answer.ErrorCode)
		return
	}
	if answer.Receipts != nil {
		receipts := make([]transport.Receipt, 0, len(answer.Receipts))
		for _, receipt := range answer.Receipts {
			receipts = append(receipts, receiptToTransport(receipt))
		}
		send(w, 202, transport.ConnectorPushReceipts{Receipts: receipts})
		return
	}
	if answer.Allow != "" {
		w.Header().Set("Allow", answer.Allow)
		failure(w, 405, "method_not_allowed")
		return
	}
	if answer.DeclaredAPI {
		w.Header().Set("Quivr-Response-Origin", "plugin")
	}
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

// relayedHeaders lowercases header names and keeps the bounded set the
// receive contract admits: no hop-by-hop header or Cookie, at most
// MaxRelayHeaders names of MaxRelayHeaderValues values within
// MaxRelayHeaderBytes in total.
func relayedHeaders(h http.Header) map[string][]string {
	out := map[string][]string{}
	total := 0
	for name, values := range h {
		lower := strings.ToLower(name)
		if hopByHop[lower] || !relayedHeaderName.MatchString(lower) || len(out) >= plugins.MaxRelayHeaders {
			continue
		}
		for _, v := range values {
			if len(out[lower]) >= plugins.MaxRelayHeaderValues || len(v) > 8192 || total+len(lower)+len(v) > plugins.MaxRelayHeaderBytes {
				break
			}
			total += len(lower) + len(v)
			out[lower] = append(out[lower], v)
		}
		if len(out[lower]) == 0 {
			delete(out, lower)
		}
	}
	return out
}
