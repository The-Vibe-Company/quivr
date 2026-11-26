package weaviate

import (
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

// SetMetrics attaches process-local dependency call counters to this store.
func (s *Store) SetMetrics(m *telemetry.DependencyMetrics) {
	if s != nil {
		s.metrics.Store(m)
	}
}

type metricsTransport struct {
	base  http.RoundTripper
	store *Store
}

func (t metricsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if m := t.store.metrics.Load(); m != nil {
		m.Record(telemetry.DependencyWeaviate, telemetry.ProtocolHTTP, classifyOperation(req.Method, req.URL.Path))
	}
	return t.base.RoundTrip(req)
}

func (t metricsTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (s *Store) countRequests(base http.RoundTripper) http.RoundTripper {
	return metricsTransport{base: base, store: s}
}

func classifyOperation(method, path string) string {
	switch {
	case path == "/v1/graphql":
		return telemetry.OpQuery
	case path == "/v1/.well-known/ready":
		return telemetry.OpReady
	case strings.HasPrefix(path, "/v1/schema"):
		return telemetry.OpSchema
	case strings.HasPrefix(path, "/v1/batch/objects"):
		if method == http.MethodDelete {
			return telemetry.OpDelete
		}
		return telemetry.OpWrite
	case strings.HasPrefix(path, "/v1/objects"):
		switch method {
		case http.MethodGet, http.MethodHead:
			return telemetry.OpRead
		case http.MethodDelete:
			return telemetry.OpDelete
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			return telemetry.OpWrite
		}
	}
	return telemetry.OpOther
}
