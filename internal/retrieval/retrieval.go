// Package retrieval owns projection routing, candidate retrieval and canonical hydration.
package retrieval

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

const ProfileVersion = "balanced.e5-token-windows.v1"

var ErrUnsupported = errors.New("unsupported_search")
var ErrUnavailable = errors.New("search_unavailable")

type Request struct {
	Query         string
	CorpusIDs     []string
	Mode, Profile string
	Limit         int
	Vector        []float32
}
type Result struct {
	Hits []content.Hydrated
	// ProfileVersion is the immutable profile shared by every routed generation.
	ProfileVersion string
}

// Route pairs a Corpus with the logical generation PostgreSQL currently routes it to.
type Route struct {
	CorpusID   string
	Generation content.Generation
}

// Routing resolves canonical per-Corpus generation routing. It never consults
// physical engine aliases.
type Routing interface {
	Authorize(context.Context, corpus.Scope, []string) error
	Generation(ctx context.Context, org, corpusID string) (content.Generation, error)
}
type Projection interface {
	Publish(context.Context, content.Generation, string, string, content.Version, content.Segmentation) error
	PublishEmbeddings(context.Context, content.Generation, string, []content.EmbeddingData) error
	Search(context.Context, []Route, corpus.Scope, Request) ([]content.Candidate, error)
}
type QueryNormalizer interface {
	NormalizeQuery(context.Context, string) (string, error)
}
type QueryEmbedder interface {
	Embed(context.Context, string) ([]float32, error)
	Space() content.VectorSpace
}
type Service struct {
	Embedder        QueryEmbedder
	QueryNormalizer QueryNormalizer
	Routing         Routing
	Projection      Projection
	Content         content.Service
}

func (s Service) Index(ctx context.Context, org string, v content.Version, seg content.Segmentation) error {
	r, err := s.Content.Record(ctx, corpus.Scope{Organization: org, Actions: []string{"content:read"}, Corpora: []string{"*"}}, v.RecordID)
	if err != nil {
		return err
	}
	g, err := s.Routing.Generation(ctx, org, r.Source.CorpusID)
	if err != nil {
		return err
	}
	if err = s.Projection.Publish(ctx, g, org, r.Source.CorpusID, v, seg); err != nil {
		return err
	}
	return s.Content.Promote(ctx, org, seg, g)
}
func (s Service) Search(ctx context.Context, scope corpus.Scope, q Request) (Result, error) {
	out := Result{Hits: []content.Hydrated{}}
	if !scope.Allows("content:read") || !scope.Allows("search:query") {
		return out, corpus.ErrForbidden
	}
	if q.Mode == "" {
		q.Mode = "hybrid"
	}
	if q.Profile == "" {
		q.Profile = "balanced"
	}
	if q.Limit == 0 {
		q.Limit = 10
	}
	if (q.Mode != "lexical" && q.Mode != "semantic" && q.Mode != "hybrid") || q.Profile != "balanced" || q.Limit < 1 || q.Limit > 50 || len(q.CorpusIDs) == 0 || len(q.CorpusIDs) > 16 {
		return out, ErrUnsupported
	}
	seen := map[string]bool{}
	for _, id := range q.CorpusIDs {
		if id == "" || seen[id] {
			return out, ErrUnsupported
		}
		seen[id] = true
		if !scope.Contains(id) {
			return out, corpus.ErrForbidden
		}
	}
	if err := s.Routing.Authorize(ctx, scope, q.CorpusIDs); err != nil {
		return out, err
	}
	normalized, err := s.QueryNormalizer.NormalizeQuery(ctx, q.Query)
	if errors.Is(err, content.ErrInvalid) {
		return out, ErrUnsupported
	}
	if err != nil {
		return out, ErrUnavailable
	}
	q.Query = normalized
	routes := make([]Route, 0, len(q.CorpusIDs))
	routed := map[string]string{}
	for _, id := range q.CorpusIDs {
		g, err := s.Routing.Generation(ctx, scope.Organization, id)
		if err != nil {
			return out, ErrUnavailable
		}
		if g.ProfileVersion != ProfileVersion || g.SpaceID != s.Embedder.Space().ID {
			return out, ErrUnsupported
		}
		routes = append(routes, Route{CorpusID: id, Generation: g})
		routed[g.ID] = id
	}
	out.ProfileVersion = ProfileVersion
	if q.Mode != "lexical" {
		q.Vector, err = s.Embedder.Embed(ctx, "query: "+q.Query)
		if err != nil {
			return out, ErrUnavailable
		}
	}
	candidates, err := s.Projection.Search(ctx, routes, scope, q)
	if err != nil {
		return out, ErrUnavailable
	}
	// Narrow hydration to the requested scope as well as the caller's grants.
	scope.Corpora = q.CorpusIDs
	segments := map[string]bool{}
	for _, c := range candidates {
		if routed[c.GenerationID] == "" || segments[c.SegmentID] {
			continue
		}
		h, err := s.Content.Hydrate(ctx, scope, c)
		if errors.Is(err, corpus.ErrNotFound) {
			continue
		}
		if err != nil {
			return out, ErrUnavailable
		}
		if q.Mode == "semantic" && h.EmbeddingID == "" {
			continue
		}
		segments[c.SegmentID] = true
		out.Hits = append(out.Hits, h)
		if len(out.Hits) == q.Limit {
			break
		}
	}
	return out, nil
}

func (s Service) IndexEmbeddings(ctx context.Context, org string, v content.Version, seg content.Segmentation, data []content.EmbeddingData) error {
	r, err := s.Content.Record(ctx, corpus.Scope{Organization: org, Actions: []string{"content:read"}, Corpora: []string{"*"}}, v.RecordID)
	if err != nil {
		return err
	}
	g, err := s.Routing.Generation(ctx, org, r.Source.CorpusID)
	if err != nil {
		return err
	}
	if g.SpaceID != s.Embedder.Space().ID {
		return ErrUnsupported
	}
	if err = s.Projection.PublishEmbeddings(ctx, g, org, data); err != nil {
		return err
	}
	return s.Content.CommitEnrichment(ctx, org, seg, g, data)
}
