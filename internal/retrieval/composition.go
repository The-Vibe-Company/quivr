package retrieval

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

// Keep time for the caller to consume candidates and finish its own round.
const profileReturnMargin = 10 * time.Millisecond

type profileSpend struct {
	max           float64
	cost, ownCost plugins.SearchCost
	usage         ProfileUsage
}

type searchChain struct {
	profiles  []*profileSpend
	paidCalls int
	costCents float64
	enforce   bool
}

func remainingCost(frames []*profileSpend) float64 {
	remaining := math.Inf(1)
	for _, f := range frames {
		remaining = min(remaining, max(0, f.max-f.cost.Cents()))
	}
	return remaining
}

func (c *searchChain) record(frames []*profileSpend, u *plugins.SearchUsage) error {
	if u == nil {
		return nil
	}
	own := frames[len(frames)-1]
	own.usage.PaidCalls += u.PaidCalls
	own.ownCost.Add(u.CostCents)
	own.usage.CostCents = own.ownCost.Cents()
	c.paidCalls += u.PaidCalls
	for _, f := range frames {
		f.cost.Add(u.CostCents)
		if c.enforce && f.cost.Exceeds(f.max) {
			return fmt.Errorf("%w: %s for %s", ErrPluginInvalid, plugins.CodeOverBudget, f.usage.Profile)
		}
	}
	c.costCents = frames[0].cost.Cents()
	return nil
}

func (sv *server) serveProfile(ctx context.Context, q Request, c plugins.CandidateRequest) ([]plugins.Candidate, error) {
	if len(sv.frames) >= plugins.MaxProfileDepth || c.Profile == nil || !sv.s.Ranker.Manifest().RequiresProfile(c.Profile.Name) {
		return nil, fmt.Errorf("%w: %s or depth exceeds %d", ErrPluginInvalid, plugins.CodePluginDependency, plugins.MaxProfileDepth)
	}
	ranker, local, ok := sv.s.resolveProfile(c.Profile.Name)
	if !ok {
		return nil, fmt.Errorf("%w: required profile unavailable", ErrPluginInvalid)
	}
	full := ranker.Manifest().ID + "/" + local
	for _, f := range sv.frames {
		if f.usage.Profile == full {
			return nil, fmt.Errorf("%w: profile cycle", ErrPluginInvalid)
		}
	}
	q.Profile, q.Limit = local, c.Profile.Limit
	if c.Profile.Query != "" {
		q.Query = c.Profile.Query
	}
	if c.Profile.Mode != "" {
		q.Mode = c.Profile.Mode
	}
	if c.Filter != nil && len(c.Filter.SourceNamespaces) > 0 {
		q.SourceNamespaces = c.Filter.SourceNamespaces
	}
	if len(q.SourceNamespaces) > 0 {
		for _, route := range sv.routes {
			if !route.Generation.SourceNamespaceProjected {
				return nil, ErrSourceFilterUnavailable
			}
		}
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= profileReturnMargin {
		return nil, ErrDeadline
	}
	innerDeadline := deadline.Add(-profileReturnMargin)
	if bound := time.Now().Add(ranker.Manifest().Contributions.Retrieval.Profiles[local].Deadline()); bound.Before(innerDeadline) {
		innerDeadline = bound
	}
	innerCtx, cancel := context.WithDeadline(ctx, innerDeadline)
	defer cancel()
	inner := sv.s
	inner.Ranker = ranker
	result, err := inner.rankProfile(innerCtx, sv.scope, q, sv.routes, Result{Hits: []Hit{}, Profile: full}, time.Now(), sv.chain, sv.frames)
	if err != nil {
		return nil, err
	}
	if u := result.Usage; u != nil {
		sv.timing.Coverage += u.Phases.Coverage
		sv.timing.PluginRounds += u.Phases.PluginRounds
		sv.timing.QueryEncoding += u.Phases.QueryEncoding
		sv.timing.IndexQuery += u.Phases.IndexQuery
		sv.timing.Hydration += u.Phases.Hydration
	}
	out := make([]plugins.Candidate, 0, len(result.Hits))
	for _, h := range result.Hits {
		sv.hydrated[h.Segment.ID] = h.Hydrated
		out = append(out, sv.candidate(h.Hydrated, h.Score, h.Explanation))
	}
	return out, nil
}
