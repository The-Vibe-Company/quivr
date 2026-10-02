package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr-v2/internal/transport/generated"
)

// SpaceRegistry lists the vector spaces of a Corpus's routed generation with
// their coverage (postgres.ContentStore).
type SpaceRegistry interface {
	VectorSpaces(ctx context.Context, org, corpusID string) (content.Generation, []content.SpaceCoverage, int64, error)
}

// WithVectorSpaces serves GET /v0/corpora/{corpus_id}/vector-spaces.
func WithVectorSpaces(registry SpaceRegistry) Option {
	return func(a *API) { a.Spaces = registry }
}

// vectorSpaceRoute reports whether a request is GET /v0/corpora/{id}/vector-spaces
// and returns the Corpus id.
func vectorSpaceRoute(r *http.Request) (string, bool) {
	id, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/v0/corpora/"), "/vector-spaces")
	return id, ok && r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v0/corpora/") && id != "" && !strings.Contains(id, "/")
}

func (a *API) listVectorSpaces(w http.ResponseWriter, r *http.Request, scope corpus.Scope, id string) {
	if !scope.Allows("corpora:read") {
		writeError(w, publicerr.Forbidden, nil)
		return
	}
	if a.Spaces == nil || !scope.Contains(id) {
		writeError(w, publicerr.NotFound, nil)
		return
	}
	if _, err := a.Service.Read(r.Context(), scope, id); err != nil {
		writeError(w, err, publicerr.DependencyUnavailable)
		return
	}
	g, spaces, total, err := a.Spaces.VectorSpaces(r.Context(), scope.Organization, id)
	if err != nil {
		writeError(w, err, publicerr.DependencyUnavailable)
		return
	}
	out := transport.VectorSpaceList{ProjectionGenerationId: g.ID, Segments: int(total), Items: []transport.VectorSpace{}}
	for _, s := range spaces {
		item := transport.VectorSpace{VectorSpaceId: s.ID, Name: s.Name, Version: s.Version, Model: s.Model, Dimensions: s.VectorSpace.Dimensions,
			Metric: transport.VectorSpaceMetric(s.Metric), Indexes: orEmpty(s.Indexes), QueryModalities: orEmpty(s.QueryModalities), Role: transport.VectorSpaceRole(s.GenerationRole)}
		item.Owner.Kind = transport.VectorSpaceOwnerKindEngine
		if s.OwnerPluginID != "" {
			item.Owner.Kind = transport.VectorSpaceOwnerKindPlugin
			item.Owner.PluginId, item.Owner.PluginVersion = optionalString(s.OwnerPluginID), optionalString(s.OwnerPluginVersion)
		}
		if item.Name == "" {
			// A space registered before the registry described it.
			item.Name = s.ID
		}
		if item.Metric == "" {
			item.Metric = transport.Cosine
		}
		item.Coverage.Segments = int(s.Segments)
		out.Items = append(out.Items, item)
	}
	send(w, 200, out)
}

func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
