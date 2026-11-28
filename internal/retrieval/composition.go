package retrieval

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
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
	profiles          []*profileSpend
	paidCalls         int
	totalCost         plugins.SearchCost
	costCents         float64
	enforce           bool
	degraded          map[string]bool
	vectors           map[string]bool
	visibleCandidates map[content.Candidate]struct{}
}

func (c *searchChain) degradations() []Degradation {
	if len(c.degraded) == 0 {
		return nil
	}
	ids := make([]string, 0, len(c.degraded))
	for id := range c.degraded {
		if !c.vectors[id] {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	return []Degradation{{Reason: DegradationVectorsUnavailable, CorpusIDs: ids}}
}

func appendDegradation(existing []Degradation, reason string, routes []Route) []Degradation {
	ids := make([]string, 0, len(routes))
	seen := map[string]bool{}
	for _, r := range routes {
		if r.CorpusID == "" || seen[r.CorpusID] {
			continue
		}
		seen[r.CorpusID] = true
		ids = append(ids, r.CorpusID)
	}
	if len(ids) == 0 {
		return existing
	}
	slices.Sort(ids)
	for _, d := range existing {
		if d.Reason == reason {
			return existing
		}
	}
	return append(existing, Degradation{Reason: reason, CorpusIDs: ids})
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
	c.totalCost.Add(u.CostCents)
	for _, f := range frames {
		f.cost.Add(u.CostCents)
		if c.enforce && f.cost.Exceeds(f.max) {
			return fmt.Errorf("%w: %s for %s", ErrPluginInvalid, plugins.CodeOverBudget, f.usage.Profile)
		}
	}
	c.costCents = c.totalCost.Cents()
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
	origQuery := q.Query
	origMode := q.Mode
	origLimit := q.Limit
	origNamespaces := q.SourceNamespaces
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
	result, err := inner.rankProfile(innerCtx, innerCtx, sv.scope, q, sv.routes, Result{Hits: []Hit{}, Profile: full}, time.Now(), sv.chain, sv.frames, nil, false)
	if err != nil {
		return nil, err
	}
	defaultRanker, defaultLocal, hasDefault := sv.s.resolveProfile(DefaultProfile)
	sameQuery := c.Profile.Query == "" || c.Profile.Query == origQuery
	sameMode := c.Profile.Mode == "" || c.Profile.Mode == origMode || c.Profile.Mode == "hybrid"
	enoughLimit := c.Profile.Limit >= origLimit
	sameNamespaces := c.Filter == nil || len(c.Filter.SourceNamespaces) == 0 || slices.Equal(c.Filter.SourceNamespaces, origNamespaces)
	if hasDefault && len(sv.frames) == 1 && isDeepProfile(sv.requestedProfile, sv.localProfile) &&
		sameQuery && sameMode && enoughLimit && sameNamespaces &&
		ranker.Manifest().ID == defaultRanker.Manifest().ID && ranker.Manifest().Version == defaultRanker.Manifest().Version && local == defaultLocal && sv.fallback == nil {
		saved := result
		saved.Hits = slices.Clone(result.Hits)
		sv.fallback = &saved
	}
	if u := result.Usage; u != nil {
		sv.timing.Coverage += u.Phases.Coverage
		sv.timing.PluginRounds += u.Phases.PluginRounds
		sv.timing.QueryEncoding += u.Phases.QueryEncoding
		sv.timing.IndexQuery += u.Phases.IndexQuery
		sv.timing.Hydration += u.Phases.Hydration
	}
	out := make([]plugins.Candidate, 0, len(result.Hits))
	outHydrated := make([]content.Hydrated, 0, len(result.Hits))
	for _, h := range result.Hits {
		sv.hydrated[h.Segment.ID] = h.Hydrated
		out = append(out, sv.candidate(h.Hydrated, h.Score, h.Explanation))
		outHydrated = append(outHydrated, h.Hydrated)
	}
	if c.IncludeLead && sv.s.Ranker.Manifest().SupportsRichRetrievalCandidates() {
		if err := sv.attachLeads(ctx, q, out, outHydrated); err != nil {
			return nil, err
		}
	}
	return out, nil
}
