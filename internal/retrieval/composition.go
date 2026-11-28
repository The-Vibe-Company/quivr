package retrieval

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
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
	mu                sync.Mutex
	profiles          []*profileSpend
	paidCalls         int
	totalCost         plugins.SearchCost
	costCents         float64
	enforce           bool
	degraded          map[string]bool
	vectors           map[string]bool
	visibleCandidates map[content.Candidate]struct{}
}

func (c *searchChain) setEnforce(enforce bool) {
	c.mu.Lock()
	c.enforce = enforce
	c.mu.Unlock()
}

func (c *searchChain) addProfile(spend *profileSpend) {
	c.mu.Lock()
	c.profiles = append(c.profiles, spend)
	c.mu.Unlock()
}

func (c *searchChain) setRounds(spend *profileSpend, rounds int) {
	c.mu.Lock()
	spend.usage.Rounds = rounds
	c.mu.Unlock()
}

func (c *searchChain) addDegraded(routes []Route) {
	if len(routes) == 0 {
		return
	}
	c.mu.Lock()
	if c.degraded == nil {
		c.degraded = map[string]bool{}
	}
	for _, route := range routes {
		c.degraded[route.CorpusID] = true
	}
	c.mu.Unlock()
}

func (c *searchChain) addVectors(routes []Route) {
	if len(routes) == 0 {
		return
	}
	c.mu.Lock()
	if c.vectors == nil {
		c.vectors = map[string]bool{}
	}
	for _, route := range routes {
		c.vectors[route.CorpusID] = true
	}
	c.mu.Unlock()
}

func (c *searchChain) hasVisibleCandidates() bool {
	c.mu.Lock()
	has := c.visibleCandidates != nil
	c.mu.Unlock()
	return has
}

func (c *searchChain) addVisibleCandidate(candidate content.Candidate) {
	c.mu.Lock()
	if c.visibleCandidates != nil {
		c.visibleCandidates[candidate] = struct{}{}
	}
	c.mu.Unlock()
}

func (c *searchChain) snapshotUsage() (paidCalls int, costCents float64, profiles []ProfileUsage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	profiles = make([]ProfileUsage, 0, len(c.profiles))
	for _, f := range c.profiles {
		profiles = append(profiles, f.usage)
	}
	return c.paidCalls, c.costCents, profiles
}

func (c *searchChain) degradations() []Degradation {
	c.mu.Lock()
	defer c.mu.Unlock()
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

func (c *searchChain) remainingCost(frames []*profileSpend) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
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
	c.mu.Lock()
	defer c.mu.Unlock()
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

func (sv *server) serveProfile(ctx context.Context, q Request, c plugins.CandidateRequest) ([]plugins.Candidate, *Result, Phases, error) {
	if len(sv.frames) >= plugins.MaxProfileDepth || c.Profile == nil || !sv.s.Ranker.Manifest().RequiresProfile(c.Profile.Name) {
		return nil, nil, Phases{}, fmt.Errorf("%w: %s or depth exceeds %d", ErrPluginInvalid, plugins.CodePluginDependency, plugins.MaxProfileDepth)
	}
	ranker, local, ok := sv.s.resolveProfile(c.Profile.Name)
	if !ok {
		return nil, nil, Phases{}, fmt.Errorf("%w: required profile unavailable", ErrPluginInvalid)
	}
	full := ranker.Manifest().ID + "/" + local
	for _, f := range sv.frames {
		if f.usage.Profile == full {
			return nil, nil, Phases{}, fmt.Errorf("%w: profile cycle", ErrPluginInvalid)
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
				return nil, nil, Phases{}, ErrSourceFilterUnavailable
			}
		}
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= profileReturnMargin {
		return nil, nil, Phases{}, ErrDeadline
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
		return nil, nil, Phases{}, err
	}
	var fallback *Result
	defaultRanker, defaultLocal, hasDefault := sv.s.resolveProfile(DefaultProfile)
	sameQuery := c.Profile.Query == "" || c.Profile.Query == origQuery
	sameMode := c.Profile.Mode == "" || c.Profile.Mode == origMode || c.Profile.Mode == "hybrid"
	enoughLimit := c.Profile.Limit >= origLimit
	sameNamespaces := c.Filter == nil || len(c.Filter.SourceNamespaces) == 0 || slices.Equal(c.Filter.SourceNamespaces, origNamespaces)
	if hasDefault && len(sv.frames) == 1 && isDeepProfile(sv.requestedProfile, sv.localProfile) &&
		sameQuery && sameMode && enoughLimit && sameNamespaces &&
		ranker.Manifest().ID == defaultRanker.Manifest().ID && ranker.Manifest().Version == defaultRanker.Manifest().Version && local == defaultLocal {
		saved := result
		saved.Hits = slices.Clone(result.Hits)
		fallback = &saved
	}
	var phases Phases
	if u := result.Usage; u != nil {
		phases = Phases{
			Coverage:      u.Phases.Coverage,
			PluginRounds:  u.Phases.PluginRounds,
			QueryEncoding: u.Phases.QueryEncoding,
			IndexQuery:    u.Phases.IndexQuery,
			Hydration:     u.Phases.Hydration,
		}
	}
	out := make([]plugins.Candidate, 0, len(result.Hits))
	sv.mu.Lock()
	for _, h := range result.Hits {
		sv.hydrated[h.Segment.ID] = h.Hydrated
		out = append(out, sv.candidate(h.Hydrated, h.Score, h.Explanation))
	}
	sv.mu.Unlock()
	return out, fallback, phases, nil
}
