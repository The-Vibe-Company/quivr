package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
)

type routeSurface int

const (
	surfaceAll routeSurface = iota
	surfaceFunctional
	surfaceAdmin
)

// IsAdminPath reports whether path belongs to the operational /v0/admin/* surface.
func IsAdminPath(path string) bool {
	return path == "/v0/admin" || strings.HasPrefix(path, "/v0/admin/")
}

func (a *API) surfaceBlocked(path string) bool {
	switch a.surface {
	case surfaceFunctional:
		return IsAdminPath(path)
	case surfaceAdmin:
		return !IsAdminPath(path)
	default:
		return false
	}
}

// NewSplit constructs strictly isolated functional and administrative HTTP
// handlers. The functional handler refuses every /v0/admin/* path before
// authentication; the admin handler serves only /v0/admin/* and authenticates
// exclusively against adminKeys, which must be non-empty and disjoint from keys.
func NewSplit(store corpus.Store, contents content.Service, search retrieval.Service, uploadService uploads.Service, keys, adminKeys map[string]corpus.Scope, cursorKey []byte, options ...Option) (public http.Handler, admin http.Handler, err error) {
	if len(adminKeys) == 0 {
		return nil, nil, errors.New("admin listener requires at least one dedicated admin key")
	}
	for token := range adminKeys {
		if _, overlap := keys[token]; overlap {
			return nil, nil, errors.New("admin listener keys must not reuse functional API keys")
		}
	}
	functionalAPI, err := buildAPI(store, contents, search, uploadService, keys, cursorKey, surfaceFunctional, options...)
	if err != nil {
		return nil, nil, err
	}
	adminAPI, err := buildAPI(store, contents, search, uploadService, adminKeys, cursorKey, surfaceAdmin, options...)
	if err != nil {
		return nil, nil, err
	}
	return http.HandlerFunc(functionalAPI.serveAccess), http.HandlerFunc(adminAPI.serveAccess), nil
}

// CombineProbeAndAdmin dispatches /v0/admin/* requests to the authenticated
// admin handler and all other requests (/healthz, /readyz, /metrics) to probes.
func CombineProbeAndAdmin(probes, admin http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsAdminPath(r.URL.Path) {
			admin.ServeHTTP(w, r)
			return
		}
		probes.ServeHTTP(w, r)
	})
}
