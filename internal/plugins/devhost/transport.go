package devhost

import (
	"net/http"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"sync/atomic"
)

var configuredClient atomic.Pointer[http.Client]

// SetTransport installs deployment trust for all plugin endpoints, including
// plugins registered after startup and work pinned to earlier plans. Call once
// before serving. CLI callers keep the default transport. Redirects stay refused.
func SetTransport(transport http.RoundTripper) {
	configuredClient.Store(&http.Client{Transport: telemetry.Transport(transport, "plugin.call"), CheckRedirect: client.CheckRedirect})
}

func httpClient() *http.Client {
	if configured := configuredClient.Load(); configured != nil {
		return configured
	}
	return client
}
