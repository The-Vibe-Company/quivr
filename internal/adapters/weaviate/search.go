package weaviate

import (
	"context"
	"errors"
	"slices"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"golang.org/x/sync/errgroup"
)

const maxTenantSearches = 4

func (s *Store) UsesRouteRankFusion(routes []retrieval.Route) bool {
	return len(routes) > 1 && slices.ContainsFunc(routes, func(r retrieval.Route) bool {
		return r.Generation.Collection == InitialCollection || r.Generation.Collection != routes[0].Generation.Collection
	})
}

func (s *Store) Search(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	if len(routes) == 0 || len(routes) > 16 {
		return nil, errors.New("invalid projection route count")
	}
	if len(routes) == 1 {
		rows, err := s.searchRoute(ctx, routes[0], scope, q)
		if err == nil && q.RankAcrossRoutes {
			rows = retrieval.NormalizeCandidateRanks(rows)
		}
		return rows, err
	}
	// Preserve the shared index ranking for requests that can still use it.
	if !q.RankAcrossRoutes && !s.UsesRouteRankFusion(routes) {
		return s.searchCollection(ctx, routes, scope, q)
	}
	group, work := errgroup.WithContext(ctx)
	group.SetLimit(maxTenantSearches)
	results := make([][]content.Candidate, len(routes))
	for i, route := range routes {
		if work.Err() != nil {
			break
		}
		group.Go(func() error {
			if err := work.Err(); err != nil {
				return err
			}
			rows, err := s.searchRoute(work, route, scope, q)
			results[i] = rows
			return err
		})
	}
	err := group.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return fuseRouteRanks(results, q.K), nil
}

func (s *Store) searchRoute(ctx context.Context, route retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	ctx, err := tenantContext(ctx, route.Generation.Collection, scope.Organization, route.CorpusID, route.Generation.ID)
	if err != nil {
		return nil, err
	}
	return s.searchCollection(ctx, []retrieval.Route{route}, scope, q)
}

func fuseRouteRanks(results [][]content.Candidate, limit int) []content.Candidate {
	fused := map[string]content.Candidate{}
	for _, rows := range results {
		for _, row := range retrieval.NormalizeCandidateRanks(rows) {
			key := candidateKey(row, false)
			row.Score += fused[key].Score
			fused[key] = row
		}
	}
	out := ordered(fused)
	if limit > 0 && len(out) > limit {
		return out[:limit]
	}
	return out
}
