// Package retrieval owns projection routing, candidate retrieval and canonical hydration.
package retrieval

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

const ProfileVersion = "balanced.lexical-e5-token-windows.v1"

var ErrUnsupported = errors.New("unsupported_search")
var ErrUnavailable = errors.New("search_unavailable")

type Request struct {
	Query         string
	CorpusIDs     []string
	Mode, Profile string
	Limit         int
}
type Result struct {
	Hits       []content.Hydrated
	Generation content.Generation
}
type Routing interface {
	Authorize(context.Context, corpus.Scope, []string) error
	ActiveGeneration(context.Context) (content.Generation, error)
}
type Projection interface {
	Publish(context.Context, content.Generation, string, string, content.Version, content.Segmentation) error
	Search(context.Context, content.Generation, corpus.Scope, Request) ([]content.Candidate, error)
}
type QueryNormalizer interface {
	NormalizeQuery(context.Context, string) (string, error)
}
type Service struct {
	QueryNormalizer QueryNormalizer
	Routing         Routing
	Projection      Projection
	Content         content.Service
}

func (s Service) Index(ctx context.Context, org string, v content.Version, seg content.Segmentation) error {
	g, err := s.Routing.ActiveGeneration(ctx)
	if err != nil {
		return err
	}
	r, err := s.Content.Record(ctx, corpus.Scope{Organization: org, Actions: []string{"content:read"}, Corpora: []string{"*"}}, v.RecordID)
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
	if q.Mode != "lexical" || q.Profile != "balanced" || q.Limit < 1 || q.Limit > 50 || len(q.CorpusIDs) == 0 || len(q.CorpusIDs) > 16 {
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
	g, err := s.Routing.ActiveGeneration(ctx)
	if err != nil {
		return out, ErrUnavailable
	}
	if g.ProfileVersion != ProfileVersion {
		return out, ErrUnsupported
	}
	out.Generation = g
	candidates, err := s.Projection.Search(ctx, g, scope, q)
	if err != nil {
		return out, ErrUnavailable
	}
	// Narrow hydration to the requested scope as well as the caller's grants.
	scope.Corpora = q.CorpusIDs
	segments := map[string]bool{}
	for _, c := range candidates {
		if c.GenerationID != g.ID || segments[c.SegmentID] {
			continue
		}
		h, err := s.Content.Hydrate(ctx, scope, c)
		if errors.Is(err, corpus.ErrNotFound) {
			continue
		}
		if err != nil {
			return out, ErrUnavailable
		}
		segments[c.SegmentID] = true
		out.Hits = append(out.Hits, h)
		if len(out.Hits) == q.Limit {
			break
		}
	}
	return out, nil
}
