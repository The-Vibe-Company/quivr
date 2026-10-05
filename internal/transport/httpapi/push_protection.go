package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/lifecycle"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
)

// WithTrustedPushProxies names the networks whose forwarding headers may be
// used for push allowlists. By default only the socket peer is trusted.
func WithTrustedPushProxies(cidrs []string) Option {
	return func(a *API) { a.pushProxyCIDRs = append([]string(nil), cidrs...) }
}

func trustedPushPeer(ip netip.Addr, cidrs []string) bool {
	for _, raw := range cidrs {
		if p, err := netip.ParsePrefix(raw); err == nil && p.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

// Walk from the socket through trusted proxy hops. A caller-controlled prefix
// never overrides the first untrusted address. Malformed chains fail closed.
func (a *API) pushClientIP(r *http.Request) netip.Addr {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(peer)
	if err != nil {
		return netip.Addr{}
	}
	ip = ip.Unmap()
	if !trustedPushPeer(ip, a.pushProxyCIDRs) {
		return ip
	}
	values := r.Header.Values("X-Forwarded-For")
	if len(values) == 0 {
		return ip
	}
	chain := strings.Split(strings.Join(values, ","), ",")
	if len(chain) > 64 {
		return netip.Addr{}
	}
	for i := len(chain) - 1; i >= 0 && trustedPushPeer(ip, a.pushProxyCIDRs); i-- {
		ip, err = netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil {
			return netip.Addr{}
		}
		ip = ip.Unmap()
	}
	return ip
}

// pushResponse keeps the bounded connector answer until its audit event and
// counters commit. Other APIs, including streaming, bypass this buffer.
type pushResponse struct {
	header    http.Header
	body      bytes.Buffer
	status    int
	errorCode string
}

func (w *pushResponse) SetErrorCode(code string) { w.errorCode = code }

func (w *pushResponse) Header() http.Header { return w.header }
func (w *pushResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *pushResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.body.Write(p)
}

func connectorAPIInstance(path string) string {
	if !strings.HasPrefix(path, "/v0/connectors/") {
		return ""
	}
	parts := strings.SplitN(strings.TrimPrefix(path, "/v0/connectors/"), "/", 3)
	if len(parts) < 2 || parts[1] != "api" {
		return ""
	}
	return parts[0]
}

func (a *API) servePushAudited(w http.ResponseWriter, r *http.Request) {
	id := connectorAPIInstance(r.URL.Path)
	// A signature kind's legacy address is a declared receive alias. Classify
	// it before bounded body validation so refused attempts are audited too.
	if id == "" && isWebhookRoute(r.URL.Path) && a.Relay != nil && a.Relay.Protection != nil {
		aliasID := strings.TrimPrefix(r.URL.Path, connectors.WebhookPath)
		if aliasID != "" && !strings.Contains(aliasID, "/") {
			lookupCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			target, err := a.Relay.Store.LoadDelivery(lookupCtx, aliasID)
			cancel()
			if err != nil && !errors.Is(err, corpus.ErrNotFound) {
				w.Header().Set("Retry-After", "30")
				writeError(w, publicerr.ConnectorsUnavailable, nil)
				return
			}
			if err == nil {
				if connector, ok := a.Relay.Registry.Lookup(target.Kind); ok {
					for _, route := range connector.Descriptor().APIRoutes {
						if route.Auth == "signature" {
							id = aliasID
							break
						}
					}
				}
			}
		}
	}
	if id == "" || a.Relay == nil || a.Relay.Protection == nil {
		a.serve(w, r)
		return
	}
	buffered := &pushResponse{header: make(http.Header)}
	buffered.header.Set("X-Request-ID", w.Header().Get("X-Request-ID"))
	a.serve(buffered, r)
	ctx, cancel := lifecycle.CleanupContext(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.Relay.Protection.RecordPush(ctx, id, buffered.status >= 200 && buffered.status < 300); err != nil && !errors.Is(err, corpus.ErrNotFound) {
		slog.WarnContext(r.Context(), "connector push audit unavailable", "connector_id", id)
		w.Header().Set("X-Request-ID", buffered.header.Get("X-Request-ID"))
		w.Header().Set("Retry-After", "30")
		writeError(w, publicerr.ConnectorsUnavailable, nil)
		return
	}
	for key, values := range buffered.header {
		w.Header()[key] = values
	}
	if coded, ok := w.(interface{ SetErrorCode(string) }); ok {
		coded.SetErrorCode(buffered.errorCode)
	}
	w.WriteHeader(buffered.status)
	_, _ = w.Write(buffered.body.Bytes())
}
