package retrieval

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/internal/visibility"
)

// ErrDeadline reports a search that outran its profile's hard bound
// (plugins.RetrievalProfile.Deadline).
var ErrDeadline = publicerr.SearchDeadlineExceeded

// ErrPluginInvalid reports a retrieval plugin answer the engine refuses: a
// ranking with a candidate it never served, too many rounds or requests, or
// any other contract violation.
var ErrPluginInvalid = publicerr.RetrievalPluginInvalid

// Ranker is the pinned retrieval plugin as search calls it.
type Ranker interface {
	// Manifest is the plugin's validated manifest: profiles and limits.
	Manifest() *plugins.Manifest
	// Configuration is the plugin's validated installer configuration.
	Configuration() json.RawMessage
	// Round posts one round and returns the 200 answer unjudged. A terminal
	// refusal of the query is content.ErrInvalid; an answer over the size
	// bound is ErrPluginInvalid; anything else is unavailability.
	Round(ctx context.Context, request plugins.SearchRequest) ([]byte, error)
}

// ProfileRouter selects one immutable ranker and its plugin-local profile.
// Resolve is called once per search, so all rounds use the same registration.
type ProfileRouter interface {
	Profiles() []Profile
	Resolve(profile string) (Ranker, string, bool)
}

// SnapshotRouter binds dependency routing to the same plan as the outer search.
type SnapshotRouter interface{ Snapshot() ProfileRouter }

// SpaceRegistry describes the vector spaces of a Corpus's routed generation.
type SpaceRegistry interface {
	VectorSpaces(ctx context.Context, org, corpusID string) (content.Generation, []content.SpaceCoverage, int64, error)
}

// Usage is what one plugin-ranked search spent.
type Usage struct {
	Rounds    int
	Elapsed   time.Duration
	PaidCalls int
	CostCents float64
	// OverObjective reports a search that took longer than its profile's
	// latency objective, max_latency_ms.
	OverObjective bool
	// Objective is the resolved profile's declared latency objective.
	Objective time.Duration
	// DefaultProfile reports whether the resolved ranker/profile is the
	// deployment's default search profile (even when requested by full name or alias).
	DefaultProfile bool
	// Phases is the time the search spent in each of its phases.
	Phases   Phases
	Profiles []ProfileUsage
}

// ProfileUsage reports this plugin's own paid calls, without double counting dependencies.
type ProfileUsage struct {
	Profile, PluginVersion string
	Rounds, PaidCalls      int
	CostCents              float64
}

// Profile is one search profile a deployment answers.
type Profile struct {
	Name, FullName, Description string
	Aliases                     []string
	// MaxLatencyMS and MaxCostCents are the plugin's declared budgets.
	MaxLatencyMS int
	MaxCostCents float64
	// PluginID and PluginVersion name the plugin that answers it.
	PluginID, PluginVersion string
}

// Profiles lists the profiles this deployment answers, default first.
func (s Service) Profiles() []Profile {
	if s.ProfilesRouter != nil {
		return s.ProfilesRouter.Profiles()
	}
	if s.Ranker == nil {
		return []Profile{}
	}
	m := s.Ranker.Manifest()
	r := m.Contributions.Retrieval
	out := []Profile{}
	for _, name := range r.ProfileNames() {
		p := r.Profiles[name]
		out = append(out, Profile{Name: name, FullName: m.ID + "/" + name, Aliases: []string{name}, Description: p.Description, MaxLatencyMS: p.MaxLatencyMS, MaxCostCents: p.MaxCostCents, PluginID: m.ID, PluginVersion: m.Version})
	}
	return out
}

// Serves reports whether the deployment answers a profile a Saved Query may record.
func (s Service) Serves(profile string) bool {
	return s.declares(profile)
}

func (s Service) declares(profile string) bool {
	_, _, ok := s.resolveProfile(profile)
	return ok
}

func (s Service) resolveProfile(profile string) (Ranker, string, bool) {
	if s.ProfilesRouter != nil {
		return s.ProfilesRouter.Resolve(profile)
	}
	if s.Ranker == nil {
		return nil, "", false
	}
	m := s.Ranker.Manifest()
	local := profile
	if prefix := m.ID + "/"; strings.HasPrefix(local, prefix) {
		local = strings.TrimPrefix(local, prefix)
	}
	_, ok := m.Contributions.Retrieval.Profiles[local]
	return s.Ranker, local, ok
}

func invocationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "search-" + hex.EncodeToString(b[:])
}

func isDeepProfile(requested, local string) bool {
	if requested == DefaultProfile {
		return false
	}
	return requested == "deep" || local == "deep" || strings.HasSuffix(requested, "/deep")
}

// rank answers a search through the pinned retrieval plugin: each round the
// plugin asks for candidates, which the engine serves after authorization,
// withdrawal fences and generation routing, or returns the ranking, which may
// hold only served candidates. The profile's max_latency_ms is its latency
// objective: a search over it still answers and is reported with the time
// each phase took. ctx carries the profile's hard bound, parentCtx is the
// caller context before applying the profile bound, and started is when Search
// began authorizing and routing.
func (s Service) rank(ctx, parentCtx context.Context, scope corpus.Scope, q Request, routes []Route, out Result, started time.Time, chain *searchChain) (Result, error) {
	defRanker, defLocal, hasDefault := s.resolveProfile(DefaultProfile)
	canFallback := isDeepProfile(out.Profile, q.Profile) && hasDefault &&
		(defRanker.Manifest().ID != s.Ranker.Manifest().ID || defLocal != q.Profile)
	if canFallback && (parentCtx == nil || parentCtx.Err() == nil) {
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < s.minRerankBudget() {
			slog.WarnContext(ctx, "deep search degraded to default before reranking due to tight deadline",
				"component", "search", "profile", out.Profile, "remaining_ms", max(0, time.Until(deadline).Milliseconds()))
			fallbackCtx, cancel := context.WithTimeout(parentCtx, defRanker.Manifest().Contributions.Retrieval.Profiles[defLocal].Deadline())
			defer cancel()
			return s.runDefaultFallback(fallbackCtx, scope, q, routes, out, started, chain, nil, defRanker, defLocal, DegradationRerankerBudgetExceeded)
		}
	}
	chain.setEnforce(s.Ranker.Manifest().SupportsSearchBudget())
	return s.rankProfile(ctx, parentCtx, scope, q, routes, out, started, chain, nil, nil, canFallback)
}

func (s Service) runDefaultFallback(ctx context.Context, scope corpus.Scope, q Request, routes []Route, out Result, started time.Time, chain *searchChain, timing *Phases, defRanker Ranker, defLocal, reason string) (Result, error) {
	fb := s
	fb.Ranker = defRanker
	q.Profile = defLocal
	if chain == nil {
		chain = &searchChain{}
	}
	chain.setEnforce(defRanker.Manifest().SupportsSearchBudget())
	res, err := fb.rankProfile(ctx, ctx, scope, q, routes, out, started, chain, nil, timing, false)
	if err != nil {
		return res, err
	}
	res.Degraded = appendDegradation(res.Degraded, reason, routes)
	return res, nil
}

func (s Service) isDefaultResolved(m *plugins.Manifest, localProfile string) bool {
	if defRanker, defLocal, ok := s.resolveProfile(DefaultProfile); ok && defRanker != nil {
		dm := defRanker.Manifest()
		return m.ID == dm.ID && localProfile == defLocal
	}
	return localProfile == DefaultProfile
}

func (s Service) degradeToSavedFallback(ctx context.Context, out Result, q Request, routes []Route, fallback *Result, reason string, chain *searchChain, timing *Phases, started time.Time, rounds int, profile plugins.RetrievalProfile) Result {
	slog.WarnContext(ctx, "deep search degraded to default hybrid results", "component", "search", "profile", out.Profile, "fallback_version", fallback.ProfileVersion, "reason", reason)
	out.ProfileVersion = fallback.ProfileVersion
	out.Hits = append([]Hit{}, fallback.Hits[:min(len(fallback.Hits), q.Limit)]...)
	out.Degraded = appendDegradation(chain.degradations(), reason, routes)
	paidCalls, costCents, profiles := chain.snapshotUsage()
	out.Usage = &Usage{Rounds: max(rounds, 1), Elapsed: time.Since(started), PaidCalls: paidCalls, CostCents: costCents, Objective: profile.Objective(), DefaultProfile: false, Phases: *timing, Profiles: profiles}
	out.Usage.OverObjective = out.Usage.Elapsed > profile.Objective()
	return out
}

func (s Service) rankProfile(ctx, parentCtx context.Context, scope corpus.Scope, q Request, routes []Route, out Result, started time.Time, chain *searchChain, ancestors []*profileSpend, carriedTiming *Phases, canFallback bool) (Result, error) {
	m := s.Ranker.Manifest()
	profile := m.Contributions.Retrieval.Profiles[q.Profile]
	timing := carriedTiming
	if timing == nil {
		timing = &Phases{Routing: time.Since(started)}
	}
	out.ProfileVersion = "plugin:" + m.ID + "@" + m.Version + "/" + q.Profile
	spend := &profileSpend{max: profile.MaxCostCents, usage: ProfileUsage{Profile: m.ID + "/" + q.Profile, PluginVersion: m.Version}}
	chain.addProfile(spend)
	frames := append(slices.Clone(ancestors), spend)
	query, err := normalizeQuery(q.Query)
	if err != nil {
		return out, ErrUnsupported
	}
	phase := time.Now()
	plan, err := s.searchSpaces(ctx, scope.Organization, routes, q.Mode == "hybrid" && q.EvaluationPlugin == "")
	timing.Coverage += time.Since(phase)
	if err != nil {
		return out, s.unserved(ctx, m.ID, ErrUnavailable, timing)
	}
	spaces := plan.spaces
	if plan.empty && q.EvaluationPlugin == "" {
		return out, nil
	}
	if q.EvaluationPlugin == "" {
		servingOwners := map[string]bool{}
		for _, sp := range spaces {
			if sp.Role == content.SpaceServed {
				servingOwners[sp.Owner.PluginID] = true
			}
		}
		spaces = slices.DeleteFunc(spaces, func(sp plugins.SearchSpace) bool { return !servingOwners[sp.Owner.PluginID] })
	}
	if q.EvaluationPlugin != "" {
		selected := slices.IndexFunc(spaces, func(sp plugins.SearchSpace) bool { return sp.ID == q.Space && sp.Owner.PluginID == q.EvaluationPlugin })
		if selected < 0 {
			return out, ErrUnsupported
		}
		// The ranker sees the requested evaluation model as this query's primary space.
		spaces = []plugins.SearchSpace{spaces[selected]}
		spaces[0].Role = content.SpaceServed
	}
	// Unioning hybrid routes must still fit the existing Plugin API envelope.
	if len(spaces) > 16 {
		return out, ErrUnsupported
	}
	if q.Mode == "hybrid" && q.EvaluationPlugin == "" && len(plan.fallback) > 0 {
		chain.addDegraded(plan.fallback)
		if !slices.ContainsFunc(spaces, func(sp plugins.SearchSpace) bool { return sp.Role == content.SpaceServed }) {
			q.Mode = "lexical"
		}
	}
	if q.Mode == "semantic" && s.Registry != nil && q.EvaluationPlugin == "" && (len(plan.fallback) > 0 || !slices.ContainsFunc(spaces, func(sp plugins.SearchSpace) bool { return sp.Role == content.SpaceServed })) {
		return out, ErrUnsupported
	}
	session := plugins.NewRetrievalSession(m, plugins.SearchRequest{
		InvocationID: invocationID(), OrganizationID: scope.Organization, Configuration: s.Ranker.Configuration(),
		Profile: q.Profile, Query: plugins.SearchQuery{Text: query, Mode: q.Mode}, Limit: q.Limit,
		Scope: plugins.SearchScope{CorpusIDs: q.CorpusIDs, SourceNamespaces: q.SourceNamespaces}, Spaces: spaces,
	})
	// Narrow hydration to the requested scope as well as the caller's grants.
	scope.Corpora = q.CorpusIDs
	sv := server{s: s, scope: scope, routes: routes, spaces: spaces, plan: plan, hydrated: map[string]content.Hydrated{}, vectors: map[string][]float32{}, timing: timing, chain: chain, frames: frames, requestedProfile: out.Profile, localProfile: q.Profile}
	for {
		callerDone := parentCtx != nil && parentCtx.Err() != nil
		if sv.fallback != nil && !callerDone {
			if ctx.Err() != nil {
				return s.degradeToSavedFallback(ctx, out, q, routes, sv.fallback, DegradationRerankerBudgetExceeded, chain, timing, started, session.Round(), profile), nil
			}
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < s.minRerankBudget() {
				return s.degradeToSavedFallback(ctx, out, q, routes, sv.fallback, DegradationRerankerBudgetExceeded, chain, timing, started, session.Round(), profile), nil
			}
		}
		if ctx.Err() != nil {
			return out, s.deadline(ctx, m.ID, ErrUnavailable, timing)
		}
		if sv.chain.hasVisibleCandidates() {
			allowed, err := sv.recheckServed(ctx, q, session.Request().Served)
			if err != nil {
				return out, s.unserved(ctx, m.ID, err, timing)
			}
			if !allowed {
				return out, nil
			}
		}
		phase := time.Now()
		request := session.Request()
		if !m.SupportsCoverageSnapshots() {
			request.Spaces = slices.Clone(request.Spaces)
			for i := range request.Spaces {
				request.Spaces[i].Coverage.Unknown = false
				request.Spaces[i].Coverage.AgeMS = nil
			}
		}
		if m.SupportsSearchBudget() {
			deadline, _ := ctx.Deadline()
			request.Budget = &plugins.SearchBudget{RemainingTimeMS: int(max(0, time.Until(deadline).Milliseconds())), RemainingCostCents: chain.remainingCost(frames)}
		}
		body, err := s.Ranker.Round(ctx, request)
		timing.PluginRounds += time.Since(phase)
		callerDone = parentCtx != nil && parentCtx.Err() != nil
		switch {
		case err == nil:
		case errors.Is(err, content.ErrInvalid):
			// The plugin refuses this query: it can never be answered.
			return out, ErrUnsupported
		case errors.Is(err, ErrPluginInvalid):
			return out, err
		default:
			reason := DegradationRerankerUnavailable
			if errors.Is(context.Cause(ctx), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrDeadline) {
				reason = DegradationRerankerBudgetExceeded
			}
			if sv.fallback != nil && !callerDone {
				return s.degradeToSavedFallback(ctx, out, q, routes, sv.fallback, reason, chain, timing, started, session.Round(), profile), nil
			}
			if canFallback && len(ancestors) == 0 && !callerDone && parentCtx != nil {
				if defRanker, defLocal, ok := s.resolveProfile(DefaultProfile); ok {
					slog.WarnContext(ctx, "deep search degraded to default after reranker failure", "component", "search", "profile", out.Profile, "reason", reason)
					fallbackCtx, cancel := context.WithTimeout(parentCtx, defRanker.Manifest().Contributions.Retrieval.Profiles[defLocal].Deadline())
					defer cancel()
					return s.runDefaultFallback(fallbackCtx, scope, q, routes, out, started, chain, timing, defRanker, defLocal, reason)
				}
			}
			return out, s.deadline(ctx, m.ID, ErrUnavailable, timing)
		}
		answer, issues := session.Judge(body)
		if len(issues) > 0 {
			slog.WarnContext(ctx, "retrieval plugin answer refused", "component", "search", "plugin", m.ID, "round", session.Round(), "code", issues[0].Code, "path", issues[0].Path, "detail", issues[0].Message)
			return out, fmt.Errorf("%w: %s %s", ErrPluginInvalid, issues[0].Code, issues[0].Message)
		}
		chain.setRounds(spend, session.Round())
		if err := chain.record(frames, answer.Usage); err != nil {
			return out, err
		}
		if ctx.Err() != nil {
			callerDone = parentCtx != nil && parentCtx.Err() != nil
			if sv.fallback != nil && !callerDone && errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
				return s.degradeToSavedFallback(ctx, out, q, routes, sv.fallback, DegradationRerankerBudgetExceeded, chain, timing, started, session.Round(), profile), nil
			}
			return out, s.deadline(ctx, m.ID, ErrUnavailable, timing)
		}
		if answer.Ranking != nil {
			out.Degraded = chain.degradations()
			for _, h := range answer.Ranking.Hits {
				out.Hits = append(out.Hits, Hit{Hydrated: sv.hydrated[h.SegmentID], Explanation: h.Explanation, Score: h.Score})
			}
			paidCalls, costCents, profiles := chain.snapshotUsage()
			out.Usage = &Usage{Rounds: session.Round(), Elapsed: time.Since(started), PaidCalls: paidCalls, CostCents: costCents, Objective: profile.Objective(), DefaultProfile: s.isDefaultResolved(m, q.Profile), Phases: *timing, Profiles: profiles}
			if out.Usage.OverObjective = out.Usage.Elapsed > profile.Objective(); out.Usage.OverObjective {
				slog.WarnContext(ctx, "search over its latency objective", append([]any{"component", "search", "plugin", m.ID, "profile", q.Profile, "mode", q.Mode,
					"elapsed_ms", out.Usage.Elapsed.Milliseconds(), "objective_ms", profile.MaxLatencyMS}, timing.attrs()...)...)
			}
			if issue := session.Budget(); issue != nil {
				slog.WarnContext(ctx, "retrieval plugin over budget", "component", "search", "plugin", m.ID, "profile", q.Profile, "detail", issue.Message)
			}
			return out, nil
		}
		served, err := sv.serveRound(ctx, q, answer.Requests)
		if err != nil {
			return out, s.unserved(ctx, m.ID, err, timing)
		}
		session.Serve(invocationID(), answer.Requests, served)
	}
}

// deadline turns a failure of the plugin's round caused by the profile's
// hard bound into ErrDeadline: the plugin outran it.
func (s Service) deadline(ctx context.Context, plugin string, err error, timing *Phases) error {
	if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		slog.WarnContext(ctx, "retrieval plugin outran the profile's hard bound", append([]any{"component", "search", "plugin", plugin}, timing.attrs()...)...)
		return ErrDeadline
	}
	return err
}

// unserved reports a failure of the engine to serve candidates as it is,
// even when the profile's hard bound passed meanwhile: then a dependency of
// the engine (the space owner encoding the query, the index, canonical
// storage) did not answer in time, as when the embedding service is down, so
// the search is unavailable (retryable), not a plugin that outran its bound.
func (s Service) unserved(ctx context.Context, plugin string, err error, timing *Phases) error {
	if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		slog.WarnContext(ctx, "search candidates not served within the profile's hard bound", append([]any{"component", "search", "plugin", plugin, "error", err.Error()}, timing.attrs()...)...)
	}
	return err
}

// Phases is the time one search spent in each of its phases: authorizing and
// routing, reading space coverage, the retrieval plugin's rounds, encoding the
// query with each space's owner, querying the index, and hydrating candidates
// from canonical storage.
type Phases struct {
	Routing, Coverage, PluginRounds, QueryEncoding, IndexQuery, Hydration time.Duration
}

// add accumulates phase durations into p.
func (p *Phases) add(other Phases) {
	p.Routing += other.Routing
	p.Coverage += other.Coverage
	p.PluginRounds += other.PluginRounds
	p.QueryEncoding += other.QueryEncoding
	p.IndexQuery += other.IndexQuery
	p.Hydration += other.Hydration
}

// mergeWavePhases summarizes a concurrent wave of per-leg phases without
// exceeding the wave's wall-clock elapsed time.
func mergeWavePhases(legs []Phases, wall time.Duration) Phases {
	var merged Phases
	for _, leg := range legs {
		merged.Coverage = max(merged.Coverage, leg.Coverage)
		merged.PluginRounds = max(merged.PluginRounds, leg.PluginRounds)
		merged.QueryEncoding = max(merged.QueryEncoding, leg.QueryEncoding)
		merged.IndexQuery = max(merged.IndexQuery, leg.IndexQuery)
		merged.Hydration = max(merged.Hydration, leg.Hydration)
	}
	total := merged.Coverage + merged.PluginRounds + merged.QueryEncoding + merged.IndexQuery + merged.Hydration
	if total <= wall || total <= 0 {
		return merged
	}
	if wall <= 0 {
		return Phases{}
	}
	scale := func(d time.Duration) time.Duration {
		return time.Duration(float64(d) * float64(wall) / float64(total))
	}
	merged.Coverage = scale(merged.Coverage)
	merged.PluginRounds = scale(merged.PluginRounds)
	merged.QueryEncoding = scale(merged.QueryEncoding)
	merged.IndexQuery = scale(merged.IndexQuery)
	merged.Hydration = scale(merged.Hydration)
	return merged
}

// attrs are the phases as log attributes, preceded by the slowest one.
func (p *Phases) attrs() []any {
	named := []struct {
		name string
		d    time.Duration
	}{{"routing", p.Routing}, {"coverage", p.Coverage}, {"plugin_rounds", p.PluginRounds}, {"query_encoding", p.QueryEncoding}, {"index_query", p.IndexQuery}, {"hydration", p.Hydration}}
	slowest := named[0]
	out := []any{}
	for _, n := range named {
		if n.d > slowest.d {
			slowest = n
		}
		out = append(out, n.name+"_ms", n.d.Milliseconds())
	}
	return append([]any{"slowest_phase", slowest.name}, out...)
}

// spacePlan retains per-Corpus routing alongside the plugin's aggregate space
// descriptions. Unknown coverage does not disable a route. An absent served
// projection or current coverage with no vectors uses keywords instead. A
// background snapshot may predate ingestion and cannot disable a current route.
type spacePlan struct {
	spaces       []plugins.SearchSpace
	bySpace      map[string][]Route
	carried      map[string][]Route
	fallback     []Route
	empty        bool
	emptyCorpora map[string]bool
}

func (s Service) searchSpaces(ctx context.Context, org string, routes []Route, hybrid bool) (spacePlan, error) {
	plan := spacePlan{spaces: []plugins.SearchSpace{}, bySpace: map[string][]Route{}, carried: map[string][]Route{}, empty: len(routes) > 0, emptyCorpora: map[string]bool{}}
	if s.Registry == nil {
		plan.empty = false
		return plan, nil
	}
	var out []plugins.SearchSpace
	for i, r := range routes {
		existing := len(out)
		spaces, total, err := s.Coverage.spaces(ctx, s.Registry, org, r)
		if err != nil {
			return plan, err
		}
		byID := map[string]content.SpaceCoverage{}
		corpusEmpty := len(spaces) > 0 && spaces[0].CorpusEmpty
		plan.emptyCorpora[r.CorpusID] = corpusEmpty
		plan.empty = plan.empty && corpusEmpty
		served := false
		for _, c := range spaces {
			if r.Generation.Carries(c.ID) {
				noProjection := c.ServingSegments != nil && *c.ServingSegments == 0
				noVectors := !c.CoverageUnknown && c.CoverageAgeMS == nil && c.TotalSegments != nil && c.Segments == 0
				if noProjection || noVectors {
					c.GenerationRole = content.SpaceEvaluation
				} else {
					plan.carried[c.ID] = append(plan.carried[c.ID], r)
					if c.GenerationRole == content.SpaceServed {
						plan.bySpace[c.ID] = append(plan.bySpace[c.ID], r)
						served = true
					}
				}
				byID[c.ID] = c
			}
		}
		if !served && !corpusEmpty {
			plan.fallback = append(plan.fallback, r)
		}
		if i == 0 || hybrid {
			for _, listed := range spaces {
				c, ok := byID[listed.ID]
				if !ok || slices.ContainsFunc(out, func(sp plugins.SearchSpace) bool { return sp.ID == c.ID }) {
					continue
				}
				owner := plugins.SpaceOwner{Kind: "engine"}
				if c.OwnerPluginID != "" {
					owner = plugins.SpaceOwner{Kind: "plugin", PluginID: c.OwnerPluginID, PluginVersion: c.OwnerPluginVersion}
				}
				out = append(out, plugins.SearchSpace{ID: c.ID, Owner: owner, Model: c.Model, Dimensions: c.VectorSpace.Dimensions, Metric: c.Metric,
					Indexes: nonNil(c.Indexes), QueryModalities: nonNil(c.QueryModalities), Role: c.GenerationRole, Coverage: plugins.SpaceCoverage{Segments: c.Segments, Total: ownerTotal(c, total), Unknown: c.CoverageUnknown, AgeMS: c.CoverageAgeMS}})
			}
			if i == 0 {
				continue
			}
		}
		// Explicit semantic/evaluation selections still require a common space.
		if !hybrid {
			out = slices.DeleteFunc(out, func(sp plugins.SearchSpace) bool {
				_, ok := byID[sp.ID]
				return !ok
			})
		}
		for j := range out {
			if hybrid && j >= existing {
				continue // A newly offered space already includes this Corpus.
			}
			if c, ok := byID[out[j].ID]; ok {
				if c.GenerationRole == content.SpaceServed {
					out[j].Role = content.SpaceServed
				}
				out[j].Coverage.Segments += c.Segments
				out[j].Coverage.Total += ownerTotal(c, total)
				out[j].Coverage.Unknown = out[j].Coverage.Unknown || c.CoverageUnknown
				if c.CoverageAgeMS != nil && (out[j].Coverage.AgeMS == nil || *c.CoverageAgeMS > *out[j].Coverage.AgeMS) {
					out[j].Coverage.AgeMS = c.CoverageAgeMS
				}
			}
		}
	}
	if out == nil {
		out = []plugins.SearchSpace{}
	}
	plan.spaces = out
	return plan, nil
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// maxHydrationBatches bounds the hydration batches of one candidate request.
const maxHydrationBatches = 3

// server serves the candidate requests of one search.
type server struct {
	s      Service
	scope  corpus.Scope
	routes []Route
	spaces []plugins.SearchSpace
	plan   spacePlan
	mu     sync.Mutex
	// hydrated caches the segments hydrated earlier in the search.
	hydrated map[string]content.Hydrated
	// vectors caches query encodings when no pin-aware encoder is configured.
	vectors map[string][]float32
	// keywordCandidates avoids querying the same fallback partition for every
	// healthy space. Keys include the full effective lexical request.
	keywordCandidates map[string][]content.Candidate
	timing            *Phases
	chain             *searchChain
	frames            []*profileSpend
	requestedProfile  string
	localProfile      string
	fallback          *Result
}

type fetchedCandidates struct {
	unique         []content.Candidate
	vectorRoutes   []Route
	fallbackRoutes []Route
	phases         Phases
}

// serveRound executes all candidate requests of one round. When a round issues
// multiple requests, candidate fetching (encoding, index search, composed
// profiles) runs concurrently while preserving deterministic served order and
// lowest-index error precedence, and overlapping segments across requests are
// hydrated together per wave.
func (sv *server) serveRound(ctx context.Context, q Request, requests []plugins.CandidateRequest) ([][]plugins.Candidate, error) {
	if len(requests) == 0 {
		return [][]plugins.Candidate{}, nil
	}
	if len(requests) == 1 {
		c := requests[0]
		if c.Primitive == plugins.PrimitiveProfile {
			cands, fb, phases, err := sv.serveProfile(ctx, q, c)
			if err != nil {
				return nil, err
			}
			if fb != nil && sv.fallback == nil {
				sv.fallback = fb
			}
			sv.timing.add(phases)
			return [][]plugins.Candidate{cands}, nil
		}
		fc, err := sv.fetchCandidates(ctx, q, c)
		sv.timing.add(fc.phases)
		if err != nil {
			return nil, err
		}
		sv.chain.addVectors(fc.vectorRoutes)
		sv.chain.addDegraded(fc.fallbackRoutes)
		served := make([][]plugins.Candidate, 1)
		if err := sv.hydrateRound(ctx, q, requests, []*fetchedCandidates{fc}, served); err != nil {
			return nil, err
		}
		return served, nil
	}

	n := len(requests)
	served := make([][]plugins.Candidate, n)
	fetched := make([]*fetchedCandidates, n)
	fallbacks := make([]*Result, n)
	legPhases := make([]Phases, n)
	errs := make([]error, n)
	ctxs := make([]context.Context, n)
	cancels := make([]context.CancelFunc, n)
	for i := range n {
		ctxs[i], cancels[i] = context.WithCancel(ctx)
	}
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()

	fetchStart := time.Now()
	var wg sync.WaitGroup
	wg.Add(n)
	for i, c := range requests {
		go func(i int, c plugins.CandidateRequest) {
			defer wg.Done()
			var err error
			if c.Primitive == plugins.PrimitiveProfile {
				served[i], fallbacks[i], legPhases[i], err = sv.serveProfile(ctxs[i], q, c)
			} else {
				var fc *fetchedCandidates
				fc, err = sv.fetchCandidates(ctxs[i], q, c)
				fetched[i] = fc
				if fc != nil {
					legPhases[i] = fc.phases
				}
			}
			if err != nil {
				errs[i] = err
				// Cancel only higher-index requests so lower-index error
				// precedence is never poisoned by a sibling cancellation.
				for j := i + 1; j < n; j++ {
					cancels[j]()
				}
			}
		}(i, c)
	}
	wg.Wait()
	sv.timing.add(mergeWavePhases(legPhases, time.Since(fetchStart)))

	for i := range n {
		if errs[i] != nil {
			return nil, errs[i]
		}
	}
	for i := range n {
		if fallbacks[i] != nil && sv.fallback == nil {
			sv.fallback = fallbacks[i]
		}
		if fc := fetched[i]; fc != nil {
			sv.chain.addVectors(fc.vectorRoutes)
			sv.chain.addDegraded(fc.fallbackRoutes)
		}
	}
	if err := sv.hydrateRound(ctx, q, requests, fetched, served); err != nil {
		return nil, err
	}
	return served, nil
}

// fetchCandidates runs one non-profile candidate request through query
// encoding and the projection, returning deduplicated index candidates in best-
// first order without mutating sv.timing or sv.chain.
func (sv *server) fetchCandidates(ctx context.Context, q Request, c plugins.CandidateRequest) (*fetchedCandidates, error) {
	fc := &fetchedCandidates{}
	text := c.QueryText
	if text != "" {
		normalized, err := normalizeQuery(text)
		if err != nil {
			return fc, ErrUnsupported
		}
		text = normalized
	}
	pq := Request{EvaluationPlugin: q.EvaluationPlugin, Query: text, CorpusIDs: q.CorpusIDs, SourceNamespaces: q.SourceNamespaces, Metadata: q.Metadata, RecordIDs: q.RecordIDs, VersionIDs: q.VersionIDs, GroupBy: c.GroupBy, Space: c.Space, Field: c.EffectiveField(), Limit: c.K, K: fetch(c.K)}
	if c.Filter != nil && len(c.Filter.SourceNamespaces) > 0 {
		pq.SourceNamespaces = c.Filter.SourceNamespaces
		for _, r := range sv.routes {
			if !r.Generation.SourceNamespaceProjected {
				return fc, ErrSourceFilterUnavailable
			}
		}
	}
	switch c.Primitive {
	case plugins.PrimitiveBM25:
		pq.Mode, pq.Space = "lexical", ""
	case plugins.PrimitiveNearVector:
		pq.Mode = "semantic"
	case plugins.PrimitiveHybrid:
		pq.Mode = "hybrid"
		pq.Hybrid = &HybridOptions{Alpha: c.EffectiveAlpha(), Fusion: c.EffectiveFusion()}
	}
	routes := sv.routes
	var fallback []Route
	if q.Mode == "hybrid" && q.EvaluationPlugin == "" && pq.Mode != "lexical" && sv.s.Registry != nil {
		routes = sv.plan.bySpace[c.Space]
		// A ranker may explicitly select an offered evaluation space of a
		// serving owner. A globally served space uses only each Corpus's
		// served routes, never another Corpus's evaluation vectors.
		if slices.ContainsFunc(sv.spaces, func(sp plugins.SearchSpace) bool { return sp.ID == c.Space && sp.Role == content.SpaceEvaluation }) {
			routes = sv.plan.carried[c.Space]
		}
		if pq.Mode == "hybrid" {
			for _, route := range sv.routes {
				if !sv.plan.emptyCorpora[route.CorpusID] && !slices.ContainsFunc(routes, func(vectorRoute Route) bool { return vectorRoute.CorpusID == route.CorpusID }) {
					fallback = append(fallback, route)
				}
			}
		}
	}
	if pq.Mode != "lexical" && len(routes) > 0 {
		if c.Vector != nil {
			pq.Vector = plugins.Float32s(c.Vector)
		} else {
			vector, elapsed, err := sv.encode(ctx, c.Space, text)
			fc.phases.QueryEncoding += elapsed
			if err != nil {
				return fc, err
			}
			pq.Vector = vector
		}
	}
	if fusion, ok := sv.s.Projection.(RouteRankFusion); ok && len(fallback) > 0 {
		pq.RankAcrossRoutes = fusion.UsesRouteRankFusion(sv.routes)
	}
	search := func(routes []Route, request Request) ([]content.Candidate, error) {
		phase := time.Now()
		defer func() { fc.phases.IndexQuery += time.Since(phase) }()
		return sv.s.Projection.Search(ctx, routes, sv.scope, request)
	}
	found := []content.Candidate{}
	if len(routes) > 0 {
		var err error
		found, err = search(routes, pq)
		if err != nil {
			return fc, ErrUnavailable
		}
		if q.EvaluationPlugin == "" && pq.Mode != "lexical" {
			fc.vectorRoutes = routes
		}
	}
	if len(fallback) > 0 {
		keywords := pq
		keywords.Mode, keywords.Space, keywords.Vector, keywords.Hybrid = "lexical", "", nil, nil
		keywords.CorpusIDs = nil
		for _, route := range fallback {
			keywords.CorpusIDs = append(keywords.CorpusIDs, route.CorpusID)
		}
		key, err := json.Marshal(keywords)
		if err != nil {
			return fc, ErrUnsupported
		}
		sv.mu.Lock()
		more, cached := sv.keywordCandidates[string(key)]
		sv.mu.Unlock()
		if _, enforced := visibility.Principals(ctx); !cached || enforced {
			more, err = search(fallback, keywords)
			if err != nil {
				return fc, ErrUnavailable
			}
			sv.mu.Lock()
			if sv.keywordCandidates == nil {
				sv.keywordCandidates = map[string][]content.Candidate{}
			}
			sv.keywordCandidates[string(key)] = more
			sv.mu.Unlock()
		}
		if !pq.RankAcrossRoutes {
			found, more = NormalizeCandidateRanks(found), NormalizeCandidateRanks(more)
		}
		found = append(found, more...)
		fc.fallbackRoutes = fallback
		slices.SortStableFunc(found, func(a, b content.Candidate) int {
			return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(a.SegmentID, b.SegmentID))
		})
	}
	routed := map[string]Route{}
	for _, r := range sv.routes {
		routed[r.Generation.ID] = r
	}
	// Keep the first object of each segment: the index returns the best first.
	seen := map[string]bool{}
	for _, f := range found {
		if _, ok := routed[f.GenerationID]; !ok || seen[f.SegmentID] {
			continue
		}
		seen[f.SegmentID] = true
		f.EvaluationPlugin, f.EvaluationSpace = q.EvaluationPlugin, q.Space
		fc.unique = append(fc.unique, f)
	}
	return fc, nil
}

// hydrateRound hydrates fetched candidates across all non-profile requests in
// the round wave-by-wave, coalescing overlapping SegmentIDs into one batch
// lookup per wave so Postgres and blob reads are never duplicated across legs.
func (sv *server) hydrateRound(ctx context.Context, q Request, requests []plugins.CandidateRequest, fetched []*fetchedCandidates, served [][]plugins.Candidate) error {
	type legState struct {
		next    int
		out     []plugins.Candidate
		records map[string]bool
	}
	routed := make(map[string]Route, len(sv.routes))
	for _, r := range sv.routes {
		routed[r.Generation.ID] = r
	}
	states := make([]*legState, len(requests))
	active := false
	for i, fc := range fetched {
		if fc == nil {
			continue
		}
		states[i] = &legState{out: []plugins.Candidate{}, records: map[string]bool{}}
		if len(fc.unique) > 0 && requests[i].K > 0 {
			active = true
		}
	}
	if !active {
		for i, st := range states {
			if st != nil {
				served[i] = st.out
			}
		}
		return nil
	}

	type hydrateKey struct {
		segmentID        string
		generationID     string
		evaluationPlugin string
		evaluationSpace  string
	}
	type legWave struct {
		candidates []content.Candidate
		indices    []int
	}

	for batch := 1; ; batch++ {
		waves := make([]legWave, len(requests))
		var combined []content.Candidate
		dedup := map[hydrateKey]int{}
		for i, st := range states {
			if st == nil {
				continue
			}
			unique := fetched[i].unique
			c := requests[i]
			if st.next >= len(unique) || len(st.out) >= c.K {
				continue
			}
			size := c.K - len(st.out)
			if st.next > 0 {
				if len(st.out) == 0 || batch == maxHydrationBatches {
					size = len(unique)
				} else {
					size = max(size, (size*st.next+len(st.out)-1)/len(st.out))
				}
			}
			wave := unique[st.next:min(len(unique), st.next+size)]
			st.next += len(wave)
			idxs := make([]int, len(wave))
			for j, f := range wave {
				key := hydrateKey{
					segmentID:        f.SegmentID,
					generationID:     f.GenerationID,
					evaluationPlugin: f.EvaluationPlugin,
					evaluationSpace:  f.EvaluationSpace,
				}
				idx, ok := dedup[key]
				if !ok {
					idx = len(combined)
					dedup[key] = idx
					combined = append(combined, f)
				}
				idxs[j] = idx
			}
			waves[i] = legWave{candidates: wave, indices: idxs}
		}
		if len(combined) == 0 {
			break
		}
		phase := time.Now()
		hydrated, err := sv.hydrate(ctx, combined)
		sv.timing.Hydration += time.Since(phase)
		if err != nil {
			return err
		}
		for i, wave := range waves {
			if len(wave.candidates) == 0 {
				continue
			}
			st := states[i]
			c := requests[i]
			for j, f := range wave.candidates {
				h, ok := hydrated[wave.indices[j]]
				if !ok {
					continue
				}
				// A vector hit on the served space counts only with its embedding
				// coverage, as in a semantic search.
				if c.Primitive == plugins.PrimitiveNearVector && (routed[f.GenerationID].Generation.Serves(c.Space) || q.EvaluationPlugin != "") && h.EmbeddingID == "" {
					continue
				}
				if c.GroupBy == plugins.GroupByRecord {
					if st.records[h.RecordID] {
						continue
					}
					st.records[h.RecordID] = true
				}
				st.out = append(st.out, sv.candidate(h, f.Score, ""))
				sv.chain.addVisibleCandidate(visibilityCandidate(h, q))
				if len(st.out) == c.K {
					break
				}
			}
		}
	}
	for i, st := range states {
		if st != nil {
			served[i] = st.out
		}
	}
	return nil
}

// fetch is how many projected objects serving k candidates asks the index
// for: three per candidate, since a segment has up to three objects (its
// lexical anchor and one or, while its embedding is replaced, two enriched
// objects), and never fewer than CandidateLimit, so the index query, and
// therefore its ranking, does not depend on k. Hybrid fusion normalizes each
// side over what it retrieved and the vector index widens its search with the
// limit: a smaller fetch could order the same candidates differently.
func fetch(k int) int {
	return max(3*k, CandidateLimit)
}

// hydrate rechecks and reads candidates from canonical storage in one batch,
// reusing segments hydrated earlier in the search. A candidate the caller may
// not read, or that is no longer current, is absent.
func (sv *server) hydrate(ctx context.Context, found []content.Candidate) (map[int]content.Hydrated, error) {
	out := map[int]content.Hydrated{}
	_, enforced := visibility.Principals(ctx)
	var batch []content.Candidate
	var at []int
	sv.mu.Lock()
	for i, f := range found {
		if h, cached := sv.hydrated[f.SegmentID]; cached && !enforced {
			out[i] = h
			continue
		}
		batch, at = append(batch, f), append(at, i)
	}
	sv.mu.Unlock()
	if len(batch) == 0 {
		return out, nil
	}
	hydrated, err := sv.s.Content.Hydrate(ctx, sv.scope, batch)
	if err != nil {
		return nil, ErrUnavailable
	}
	sv.mu.Lock()
	for j, h := range hydrated {
		out[at[j]] = h
		sv.hydrated[h.Segment.ID] = h
	}
	sv.mu.Unlock()
	return out, nil
}

// encode encodes a query with the owner of a space: the engine's E5 encoder
// for the legacy space that Corpora not yet rebuilt since THE-777 serve, the
// pinned ingestion plugin for its own.
func (sv *server) encode(ctx context.Context, space, text string) ([]float32, time.Duration, error) {
	// The serving registration can change between candidate rounds. Let
	// encodeQuery resolve it before consulting the shared cache.
	_, pinAware := sv.s.Spaces.(pinnedQueryEncoder)
	var key string
	if !pinAware {
		key = queryVectorKey(space, text)
		sv.mu.Lock()
		v, ok := sv.vectors[key]
		sv.mu.Unlock()
		if ok {
			return v, 0, nil
		}
	}
	started := time.Now()
	vector, err := sv.s.encodeQuery(ctx, sv.scope.Organization, space, text)
	elapsed := time.Since(started)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return nil, elapsed, ErrModelUnavailable
		}
		return nil, elapsed, err
	}
	if !pinAware {
		sv.mu.Lock()
		sv.vectors[key] = vector
		sv.mu.Unlock()
	}
	return vector, elapsed, nil
}

func (s Service) EncodeQuery(ctx context.Context, org, space, text string) ([]float32, error) {
	return s.encodeQuery(ctx, org, space, text)
}

// A live encoder must return the actual serving encoder alongside its identity.
// Looking them up separately would allow a plan change to cache a vector under
// the previous pin.
type pinnedQueryEncoder interface {
	ResolveQueryEncoder(context.Context, string) (QueryEncoder, string, error)
}

func (s Service) encodeQuery(ctx context.Context, org, space, text string) ([]float32, error) {
	if ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	switch {
	case s.Embedder != nil && space == s.Embedder.Space().ID:
		// The legacy space ID identifies the engine's pinned model and template.
		return s.QueryVectors.encode(ctx, queryVectorKey("engine", org, space, text), func(ctx context.Context) ([]float32, error) {
			vector, err := s.Embedder.Embed(ctx, "query: "+text)
			if err != nil {
				return nil, ErrUnavailable
			}
			return vector, nil
		})
	case s.Spaces != nil:
		// Pinned work must pass the plugin invocation's per-work rollback guard.
		_, pinnedWork := plugins.WorkOf(ctx)
		if resolver, ok := s.Spaces.(pinnedQueryEncoder); ok && s.QueryVectors != nil && !pinnedWork {
			encoder, pin, err := resolver.ResolveQueryEncoder(ctx, space)
			if err != nil {
				return nil, ErrUnavailable
			}
			if encoder != nil {
				return s.QueryVectors.encode(ctx, queryVectorKey("plugin", org, space, pin, text), func(ctx context.Context) ([]float32, error) {
					return encodePluginQuery(ctx, encoder, org, space, text)
				})
			}
			return nil, ErrUnsupported
		}
		// Preserve the ordinary path for encoders without a stable identity.
		if s.Spaces.Owns(space) {
			return encodePluginQuery(ctx, s.Spaces, org, space, text)
		}
	}
	return nil, ErrUnsupported
}

func encodePluginQuery(ctx context.Context, encoder QueryEncoder, org, space, text string) ([]float32, error) {
	vector, err := encoder.EncodeQuery(ctx, org, space, text)
	switch {
	case errors.Is(err, ErrQueryTooLong):
		return nil, err
	case errors.Is(err, content.ErrInvalid):
		return nil, ErrUnsupported
	case err != nil:
		return nil, ErrUnavailable
	default:
		return vector, nil
	}
}

func ownerTotal(c content.SpaceCoverage, legacy int64) int64 {
	if c.TotalSegments != nil {
		return *c.TotalSegments
	}
	return legacy
}

// candidate preserves the single-Part text/offset contract for older rankers.
func (sv *server) candidate(h content.Hydrated, score float64, explanation string) plugins.Candidate {
	c := plugins.Candidate{SegmentID: h.Segment.ID, RecordID: h.RecordID, VersionID: h.VersionID, PartKey: h.Segment.PartKey,
		Text: h.Segment.AnchorText(), Start: h.Segment.Start, End: h.Segment.End, Score: score, Explanation: explanation}
	if len(h.Segment.Derivation.SourceRanges) > 0 && sv.s.Ranker.Manifest().SupportsMultiPartSegments() {
		c.PassageText = h.Segment.Text
		c.SourceSeparator = h.Segment.Derivation.SourceSeparator
		for _, r := range h.Segment.Derivation.SourceRanges {
			c.SourceRanges = append(c.SourceRanges, plugins.SourceRange{PartKey: r.PartKey, Start: r.Start, End: r.End})
		}
	}
	if h.Segment.Derivation.Region != nil && sv.s.Ranker.Manifest().SupportsMultimodalIngestion() {
		r := h.Segment.Derivation.Region
		c.Region = &plugins.ImageRegion{
			CoordinateSystem: r.CoordinateSystem,
			X:                r.X,
			Y:                r.Y,
			Width:            r.Width,
			Height:           r.Height,
			ImageWidth:       r.ImageWidth,
			ImageHeight:      r.ImageHeight,
			Label:            r.Label,
		}
	}
	return c
}
