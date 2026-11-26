package devhost

import (
	"context"
	"net/http"
	"sync/atomic"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

var configuredClient atomic.Pointer[http.Client]

// SetTransport installs deployment trust for all plugin endpoints, including
// plugins registered after startup and work pinned to earlier plans. Call once
// before serving. A nil transport restores the default. Redirects stay refused.
func SetTransport(transport http.RoundTripper) {
	if transport == nil {
		configuredClient.Store(nil)
		return
	}
	configuredClient.Store(&http.Client{Transport: telemetry.Transport(transport, "plugin.call"), CheckRedirect: client.CheckRedirect})
}

type responseCheckKey struct{}

// WithResponseCheck checks a Contribution POST's headers before reading or
// validating its body.
func WithResponseCheck(ctx context.Context, check func(status int, header http.Header) error) context.Context {
	return context.WithValue(ctx, responseCheckKey{}, check)
}

func httpClient() *http.Client {
	if configured := configuredClient.Load(); configured != nil {
		return configured
	}
	return client
}
