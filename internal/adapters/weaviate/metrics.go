package weaviate

import (
	"net/http"
	"net/url"
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
	base     http.RoundTripper
	store    *Store
	basePath string
}

func (t metricsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if m := t.store.metrics.Load(); m != nil {
		m.Record(telemetry.DependencyWeaviate, telemetry.ProtocolHTTP, classifyOperation(req.Method, relativeAPIPath(t.basePath, req.URL.Path)))
	}
	return t.base.RoundTrip(req)
}

func (t metricsTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t metricsTransport) Unwrap() http.RoundTripper { return t.base }

func (s *Store) countRequests(base http.RoundTripper) http.RoundTripper {
	return metricsTransport{base: base, store: s, basePath: endpointBasePath(s.Endpoint)}
}

func endpointBasePath(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return strings.TrimRight(u.Path, "/")
}

func relativeAPIPath(basePath, reqPath string) string {
	if basePath == "" {
		return reqPath
	}
	if reqPath == basePath {
		return "/"
	}
	if strings.HasPrefix(reqPath, basePath+"/") {
		return reqPath[len(basePath):]
	}
	return reqPath
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
