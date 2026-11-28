package weaviate

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
	"golang.org/x/sync/errgroup"
)

const maxTenantSearches = 4

// logRecoveredPanic records the panic type and stack without logging raw panic
// values, which could contain tenant text, queries, or credentials.
func logRecoveredPanic(ctx context.Context, path string, recovered any) {
	slog.ErrorContext(ctx, "recovered weaviate panic",
		"component", "weaviate",
		"path", path,
		"panic_type", fmt.Sprintf("%T", recovered),
		"stack", string(debug.Stack()),
	)
}

// goRecover launches fn on group and converts any panic into
// retrieval.ErrUnavailable so sibling legs are cancelled via the group's
// context instead of crashing the process.
func goRecover(ctx context.Context, group *errgroup.Group, path string, fn func() error) {
	group.Go(func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				logRecoveredPanic(ctx, path, r)
				err = retrieval.ErrUnavailable
			}
		}()
		return fn()
	})
}

func (s *Store) UsesRouteRankFusion(routes []retrieval.Route) bool {
	return len(routes) > 1 && slices.ContainsFunc(routes, func(r retrieval.Route) bool {
		return r.Generation.Collection == InitialCollection || r.Generation.Collection != routes[0].Generation.Collection
	})
}

func (s *Store) Search(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	if len(routes) == 0 || len(routes) > 16 {
		return nil, errors.New("invalid projection route count")
	}
	principals, enforced := visibility.Principals(ctx)
	if !enforced {
		return s.searchRouted(ctx, routes, scope, q)
	}
	if s.Visibility == nil {
		return nil, retrieval.ErrUnavailable
	}
	q.RankAcrossRoutes = q.RankAcrossRoutes || s.UsesRouteRankFusion(routes)
	for range 2 {
		allowed, filters, revisions, err := s.resolveVisibility(ctx, routes, scope, principals)
		if err != nil {
			return nil, err
		}
		rows := []content.Candidate{}
		if len(allowed) > 0 {
			rows, err = s.searchRouted(context.WithValue(ctx, routeVisibilityKey{}, filters), allowed, scope, q)
			if err != nil {
				return nil, err
			}
		}
		current, err := s.Visibility.Current(ctx, scope.Organization, revisions)
		if err != nil {
			return nil, err
		}
		if current {
			return rows, nil
		}
		// A concurrent change can admit or remove namespaces while the
		// index ranks. Discard the entire candidate set and resolve again.
	}
	return nil, retrieval.ErrUnavailable
}

func (s *Store) searchRouted(ctx context.Context, routes []retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
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
		goRecover(work, group, "search_route", func() error {
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
	// Tenant-local scores only rank their own candidate lists. Use the
	// same cross-corpus merge in every mode, including semantic search.
	return fuseRouteRanks(results, cmp.Or(q.K, retrieval.CandidateLimit)), nil
}

func (s *Store) searchRoute(ctx context.Context, route retrieval.Route, scope corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	ctx, err := tenantContext(ctx, route.Generation.Collection, scope.Organization, route.CorpusID, route.Generation.ID)
	if err != nil {
		return nil, err
	}
	return s.searchCollection(ctx, []retrieval.Route{route}, scope, q)
}

func fuseRouteRanks(results [][]content.Candidate, limit int) []content.Candidate {
	total := 0
	for _, rows := range results {
		total += len(rows)
	}
	fused := make(map[candidateID]content.Candidate, total)
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
