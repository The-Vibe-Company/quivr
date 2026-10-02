package retrieval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/publicerr"
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

// Serves reports whether the deployment answers profile, by its name or by
// LegacyProfile for the default: what a Saved Query may record.
func (s Service) Serves(profile string) bool {
	if profile == LegacyProfile {
		profile = DefaultProfile
	}
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

// rank answers a search through the pinned retrieval plugin: each round the
// plugin asks for candidates, which the engine serves after authorization,
// withdrawal fences and generation routing, or returns the ranking, which may
// hold only served candidates. The profile's max_latency_ms is its latency
// objective: a search over it still answers and is reported with the time
// each phase took. ctx carries the profile's hard bound, and started is when
// Search began authorizing and routing (Search).
func (s Service) rank(ctx context.Context, scope corpus.Scope, q Request, routes []Route, out Result, started time.Time) (Result, error) {
	return s.rankProfile(ctx, scope, q, routes, out, started, &searchChain{enforce: s.Ranker.Manifest().SupportsSearchBudget()}, nil)
}

func (s Service) rankProfile(ctx context.Context, scope corpus.Scope, q Request, routes []Route, out Result, started time.Time, chain *searchChain, ancestors []*profileSpend) (Result, error) {
	m := s.Ranker.Manifest()
	profile := m.Contributions.Retrieval.Profiles[q.Profile]
	timing := &Phases{Routing: time.Since(started)}
	out.ProfileVersion = "plugin:" + m.ID + "@" + m.Version + "/" + q.Profile
	spend := &profileSpend{max: profile.MaxCostCents, usage: ProfileUsage{Profile: m.ID + "/" + q.Profile, PluginVersion: m.Version}}
	chain.profiles = append(chain.profiles, spend)
	frames := append(slices.Clone(ancestors), spend)
	query, err := normalizeQuery(q.Query)
	if err != nil {
		return out, ErrUnsupported
	}
	phase := time.Now()
	spaces, err := s.searchSpaces(ctx, scope.Organization, routes)
	timing.Coverage += time.Since(phase)
	if err != nil {
		return out, s.unserved(ctx, m.ID, ErrUnavailable, timing)
	}
	session := plugins.NewRetrievalSession(m, plugins.SearchRequest{
		InvocationID: invocationID(), OrganizationID: scope.Organization, Configuration: s.Ranker.Configuration(),
		Profile: q.Profile, Query: plugins.SearchQuery{Text: query, Mode: q.Mode}, Limit: q.Limit,
		Scope: plugins.SearchScope{CorpusIDs: q.CorpusIDs, SourceNamespaces: q.SourceNamespaces}, Spaces: spaces,
	})
	// Narrow hydration to the requested scope as well as the caller's grants.
	scope.Corpora = q.CorpusIDs
	sv := server{s: s, scope: scope, routes: routes, spaces: spaces, hydrated: map[string]content.Hydrated{}, vectors: map[string][]float32{}, timing: timing, chain: chain, frames: frames}
	for {
		if ctx.Err() != nil {
			return out, s.deadline(ctx, m.ID, ErrUnavailable, timing)
		}
		phase := time.Now()
		request := session.Request()
		if m.SupportsSearchBudget() {
			deadline, _ := ctx.Deadline()
			request.Budget = &plugins.SearchBudget{RemainingTimeMS: int(max(0, time.Until(deadline).Milliseconds())), RemainingCostCents: remainingCost(frames)}
		}
		body, err := s.Ranker.Round(ctx, request)
		timing.PluginRounds += time.Since(phase)
		switch {
		case err == nil:
		case errors.Is(err, content.ErrInvalid):
			// The plugin refuses this query: it can never be answered.
			return out, ErrUnsupported
		case errors.Is(err, ErrPluginInvalid):
			return out, err
		default:
			return out, s.deadline(ctx, m.ID, ErrUnavailable, timing)
		}
		answer, issues := session.Judge(body)
		if len(issues) > 0 {
			slog.Warn("retrieval plugin answer refused", "component", "search", "plugin", m.ID, "round", session.Round(), "code", issues[0].Code, "path", issues[0].Path, "detail", issues[0].Message)
			return out, fmt.Errorf("%w: %s %s", ErrPluginInvalid, issues[0].Code, issues[0].Message)
		}
		spend.usage.Rounds = session.Round()
		if err := chain.record(frames, answer.Usage); err != nil {
			return out, err
		}
		if ctx.Err() != nil {
			return out, s.deadline(ctx, m.ID, ErrUnavailable, timing)
		}
		if answer.Ranking != nil {
			for _, h := range answer.Ranking.Hits {
				out.Hits = append(out.Hits, Hit{Hydrated: sv.hydrated[h.SegmentID], Explanation: h.Explanation, Score: h.Score})
			}
			out.Usage = &Usage{Rounds: session.Round(), Elapsed: time.Since(started), PaidCalls: chain.paidCalls, CostCents: chain.costCents, Phases: *timing}
			for _, f := range chain.profiles {
				out.Usage.Profiles = append(out.Usage.Profiles, f.usage)
			}
			if out.Usage.OverObjective = out.Usage.Elapsed > profile.Objective(); out.Usage.OverObjective {
				slog.Warn("search over its latency objective", append([]any{"component", "search", "plugin", m.ID, "profile", q.Profile, "mode", q.Mode,
					"elapsed_ms", out.Usage.Elapsed.Milliseconds(), "objective_ms", profile.MaxLatencyMS}, timing.attrs()...)...)
			}
			if issue := session.Budget(); issue != nil {
				slog.Warn("retrieval plugin over budget", "component", "search", "plugin", m.ID, "profile", q.Profile, "detail", issue.Message)
			}
			return out, nil
		}
		served := make([][]plugins.Candidate, len(answer.Requests))
		for i, request := range answer.Requests {
			if served[i], err = sv.serve(ctx, q, request); err != nil {
				return out, s.unserved(ctx, m.ID, err, timing)
			}
		}
		session.Serve(invocationID(), answer.Requests, served)
	}
}

// deadline turns a failure of the plugin's round caused by the profile's
// hard bound into ErrDeadline: the plugin outran it.
func (s Service) deadline(ctx context.Context, plugin string, err error, timing *Phases) error {
	if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		slog.Warn("retrieval plugin outran the profile's hard bound", append([]any{"component", "search", "plugin", plugin}, timing.attrs()...)...)
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
		slog.Warn("search candidates not served within the profile's hard bound", append([]any{"component", "search", "plugin", plugin, "error", err.Error()}, timing.attrs()...)...)
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

// searchSpaces lists the vector spaces every routed generation carries, as
// the registry describes them, with their coverage summed over the Corpora.
func (s Service) searchSpaces(ctx context.Context, org string, routes []Route) ([]plugins.SearchSpace, error) {
	if s.Registry == nil {
		return []plugins.SearchSpace{}, nil
	}
	var out []plugins.SearchSpace
	for i, r := range routes {
		spaces, total, err := s.Coverage.spaces(ctx, s.Registry, org, r)
		if err != nil {
			return nil, err
		}
		byID := map[string]content.SpaceCoverage{}
		for _, c := range spaces {
			if r.Generation.Carries(c.ID) {
				byID[c.ID] = c
			}
		}
		if i == 0 {
			for _, c := range spaces {
				if _, ok := byID[c.ID]; !ok {
					continue
				}
				owner := plugins.SpaceOwner{Kind: "engine"}
				if c.OwnerPluginID != "" {
					owner = plugins.SpaceOwner{Kind: "plugin", PluginID: c.OwnerPluginID, PluginVersion: c.OwnerPluginVersion}
				}
				out = append(out, plugins.SearchSpace{ID: c.ID, Owner: owner, Model: c.Model, Dimensions: c.VectorSpace.Dimensions, Metric: c.Metric,
					Indexes: nonNil(c.Indexes), QueryModalities: nonNil(c.QueryModalities), Role: c.GenerationRole, Coverage: plugins.SpaceCoverage{Segments: c.Segments, Total: total}})
			}
			continue
		}
		// One query ranks every Corpus in one space: keep the spaces all carry.
		out = slices.DeleteFunc(out, func(sp plugins.SearchSpace) bool {
			_, ok := byID[sp.ID]
			return !ok
		})
		for j := range out {
			if c, ok := byID[out[j].ID]; ok {
				out[j].Coverage.Segments += c.Segments
				out[j].Coverage.Total += total
			}
		}
	}
	if out == nil {
		out = []plugins.SearchSpace{}
	}
	return out, nil
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// maxHydrationBatches bounds the hydration batches of one candidate request.
const maxHydrationBatches = 3

// server serves the candidate requests of one search, one at a time.
type server struct {
	s      Service
	scope  corpus.Scope
	routes []Route
	spaces []plugins.SearchSpace
	// hydrated caches the segments hydrated earlier in the search.
	hydrated map[string]content.Hydrated
	// vectors caches query encodings by space and text.
	vectors map[string][]float32
	timing  *Phases
	chain   *searchChain
	frames  []*profileSpend
}

// serve runs one candidate request through the projection and hydrates what
// it finds: only authorized, current segments of the routed generations
// reach the plugin, best first, once each.
func (sv *server) serve(ctx context.Context, q Request, c plugins.CandidateRequest) ([]plugins.Candidate, error) {
	if c.Primitive == plugins.PrimitiveProfile {
		return sv.serveProfile(ctx, q, c)
	}
	text := c.QueryText
	if text != "" {
		normalized, err := normalizeQuery(text)
		if err != nil {
			return nil, ErrUnsupported
		}
		text = normalized
	}
	pq := Request{Query: text, CorpusIDs: q.CorpusIDs, SourceNamespaces: q.SourceNamespaces, Space: c.Space, Field: c.EffectiveField(), K: fetch(c.K)}
	if c.Filter != nil && len(c.Filter.SourceNamespaces) > 0 {
		pq.SourceNamespaces = c.Filter.SourceNamespaces
		for _, r := range sv.routes {
			if !r.Generation.SourceNamespaceProjected {
				return nil, ErrSourceFilterUnavailable
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
	if pq.Mode != "lexical" {
		if c.Vector != nil {
			pq.Vector = plugins.Float32s(c.Vector)
		} else {
			vector, err := sv.encode(ctx, c.Space, text)
			if err != nil {
				return nil, err
			}
			pq.Vector = vector
		}
	}
	phase := time.Now()
	found, err := sv.s.Projection.Search(ctx, sv.routes, sv.scope, pq)
	sv.timing.IndexQuery += time.Since(phase)
	if err != nil {
		return nil, ErrUnavailable
	}
	routed := map[string]Route{}
	for _, r := range sv.routes {
		routed[r.Generation.ID] = r
	}
	// Keep the first object of each segment: the index returns the best first.
	var unique []content.Candidate
	seen := map[string]bool{}
	for _, f := range found {
		if _, ok := routed[f.GenerationID]; !ok || seen[f.SegmentID] {
			continue
		}
		seen[f.SegmentID] = true
		unique = append(unique, f)
	}
	out := []plugins.Candidate{}
	records := map[string]bool{}
	// Hydrate in index order, a batch at a time, until k candidates are kept.
	// The first batch is the k best: when hydration keeps them all, as it
	// usually does, a candidate further down is never read. A further batch
	// is sized from the share of candidates kept so far, and the last allowed
	// takes every candidate left, so a page costs at most maxHydrationBatches.
	for batch, next := 1, 0; next < len(unique) && len(out) < c.K; batch++ {
		size := c.K - len(out)
		if next > 0 {
			if len(out) == 0 || batch == maxHydrationBatches {
				size = len(unique)
			} else {
				size = max(size, (size*next+len(out)-1)/len(out))
			}
		}
		wave := unique[next:min(len(unique), next+size)]
		next += len(wave)
		phase := time.Now()
		hydrated, err := sv.hydrate(ctx, wave)
		sv.timing.Hydration += time.Since(phase)
		if err != nil {
			return nil, err
		}
		for i, f := range wave {
			h, ok := hydrated[i]
			if !ok {
				continue
			}
			// A vector hit on the served space counts only with its embedding
			// coverage, as in a semantic search.
			if c.Primitive == plugins.PrimitiveNearVector && routed[f.GenerationID].Generation.Serves(c.Space) && h.EmbeddingID == "" {
				continue
			}
			if c.GroupBy == plugins.GroupByRecord {
				if records[h.RecordID] {
					continue
				}
				records[h.RecordID] = true
			}
			out = append(out, plugins.Candidate{SegmentID: h.Segment.ID, RecordID: h.RecordID, VersionID: h.VersionID, PartKey: h.Segment.PartKey,
				Text: h.Segment.Text, Start: h.Segment.Start, End: h.Segment.End, Score: f.Score})
			if len(out) == c.K {
				break
			}
		}
	}
	return out, nil
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
	var batch []content.Candidate
	var at []int
	for i, f := range found {
		if h, cached := sv.hydrated[f.SegmentID]; cached {
			out[i] = h
			continue
		}
		batch, at = append(batch, f), append(at, i)
	}
	if len(batch) == 0 {
		return out, nil
	}
	hydrated, err := sv.s.Content.Hydrate(ctx, sv.scope, batch)
	if err != nil {
		return nil, ErrUnavailable
	}
	for j, h := range hydrated {
		out[at[j]] = h
		sv.hydrated[h.Segment.ID] = h
	}
	return out, nil
}

// encode encodes a query with the owner of a space: the engine's E5 encoder
// for the legacy space that Corpora not yet rebuilt since THE-777 serve, the
// pinned ingestion plugin for its own.
func (sv *server) encode(ctx context.Context, space, text string) ([]float32, error) {
	key := space + "\x00" + text
	if v, ok := sv.vectors[key]; ok {
		return v, nil
	}
	started := time.Now()
	defer func() { sv.timing.QueryEncoding += time.Since(started) }()
	vector, err := sv.s.EncodeQuery(ctx, sv.scope.Organization, space, text)
	if err != nil {
		return nil, err
	}
	sv.vectors[key] = vector
	return vector, nil
}

func (s Service) EncodeQuery(ctx context.Context, org, space, text string) ([]float32, error) {
	var vector []float32
	var err error
	switch {
	case s.Embedder != nil && space == s.Embedder.Space().ID:
		vector, err = s.Embedder.Embed(ctx, "query: "+text)
	case s.Spaces != nil && s.Spaces.Owns(space):
		vector, err = s.Spaces.EncodeQuery(ctx, org, space, text)
		if errors.Is(err, ErrQueryTooLong) {
			return nil, err
		}
		if errors.Is(err, content.ErrInvalid) {
			return nil, ErrUnsupported
		}
	default:
		// No pinned owner can encode a query into this space.
		return nil, ErrUnsupported
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	return vector, nil
}
