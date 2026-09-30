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
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// ErrDeadline reports a search that outran its profile's max_latency_ms.
var ErrDeadline = errors.New("search_deadline_exceeded")

// ErrPluginInvalid reports a retrieval plugin answer the engine refuses: a
// ranking with a candidate it never served, too many rounds or requests, or
// any other contract violation.
var ErrPluginInvalid = errors.New("retrieval_plugin_invalid")

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
}

// Profile is one search profile a deployment answers.
type Profile struct {
	Name, Description string
	// MaxLatencyMS and MaxCostCents are the plugin's declared budgets.
	MaxLatencyMS int
	MaxCostCents float64
	// PluginID and PluginVersion name the plugin that answers it.
	PluginID, PluginVersion string
}

// Profiles lists the profiles this deployment answers, default first.
func (s Service) Profiles() []Profile {
	if s.Ranker == nil {
		return []Profile{}
	}
	m := s.Ranker.Manifest()
	r := m.Contributions.Retrieval
	out := []Profile{}
	for _, name := range r.ProfileNames() {
		p := r.Profiles[name]
		out = append(out, Profile{Name: name, Description: p.Description, MaxLatencyMS: p.MaxLatencyMS, MaxCostCents: p.MaxCostCents, PluginID: m.ID, PluginVersion: m.Version})
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
	if s.Ranker == nil {
		return false
	}
	_, ok := s.Ranker.Manifest().Contributions.Retrieval.Profiles[profile]
	return ok
}

func invocationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "search-" + hex.EncodeToString(b[:])
}

// rank answers a search through the pinned retrieval plugin: each round the
// plugin asks for candidates, which the engine serves after authorization,
// withdrawal fences and generation routing, or returns the ranking, which may
// hold only served candidates. The whole search runs under the profile's
// max_latency_ms.
func (s Service) rank(ctx context.Context, scope corpus.Scope, q Request, routes []Route, out Result) (Result, error) {
	started := time.Now()
	m := s.Ranker.Manifest()
	profile := m.Contributions.Retrieval.Profiles[q.Profile]
	ctx, cancel := context.WithTimeout(ctx, time.Duration(profile.MaxLatencyMS)*time.Millisecond)
	defer cancel()
	out.ProfileVersion = "plugin:" + m.ID + "@" + m.Version + "/" + q.Profile
	query, err := normalizeQuery(q.Query)
	if err != nil {
		return out, ErrUnsupported
	}
	spaces, err := s.searchSpaces(ctx, scope.Organization, routes)
	if err != nil {
		return out, s.unserved(ctx, m.ID, ErrUnavailable)
	}
	session := plugins.NewRetrievalSession(m, plugins.SearchRequest{
		InvocationID: invocationID(), OrganizationID: scope.Organization, Configuration: s.Ranker.Configuration(),
		Profile: q.Profile, Query: plugins.SearchQuery{Text: query, Mode: q.Mode}, Limit: q.Limit,
		Scope: plugins.SearchScope{CorpusIDs: q.CorpusIDs, SourceNamespaces: q.SourceNamespaces}, Spaces: spaces,
	})
	// Narrow hydration to the requested scope as well as the caller's grants.
	scope.Corpora = q.CorpusIDs
	sv := server{s: s, scope: scope, routes: routes, spaces: spaces, hydrated: map[string]content.Hydrated{}, vectors: map[string][]float32{}}
	for {
		body, err := s.Ranker.Round(ctx, session.Request())
		switch {
		case err == nil:
		case errors.Is(err, content.ErrInvalid):
			// The plugin refuses this query: it can never be answered.
			return out, ErrUnsupported
		case errors.Is(err, ErrPluginInvalid):
			return out, err
		default:
			return out, s.deadline(ctx, ErrUnavailable)
		}
		answer, issues := session.Judge(body)
		if len(issues) > 0 {
			slog.Warn("retrieval plugin answer refused", "component", "search", "plugin", m.ID, "round", session.Round(), "code", issues[0].Code, "path", issues[0].Path, "detail", issues[0].Message)
			return out, fmt.Errorf("%w: %s %s", ErrPluginInvalid, issues[0].Code, issues[0].Message)
		}
		if answer.Ranking != nil {
			for _, h := range answer.Ranking.Hits {
				out.Hits = append(out.Hits, Hit{Hydrated: sv.hydrated[h.SegmentID], Explanation: h.Explanation})
			}
			usage := session.Usage()
			out.Usage = &Usage{Rounds: session.Round(), Elapsed: time.Since(started), PaidCalls: usage.PaidCalls, CostCents: usage.CostCents}
			if issue := session.Budget(); issue != nil {
				slog.Warn("retrieval plugin over budget", "component", "search", "plugin", m.ID, "profile", q.Profile, "detail", issue.Message)
			}
			return out, nil
		}
		served := make([][]plugins.Candidate, len(answer.Requests))
		for i, request := range answer.Requests {
			if served[i], err = sv.serve(ctx, q, request); err != nil {
				return out, s.unserved(ctx, m.ID, err)
			}
		}
		session.Serve(invocationID(), answer.Requests, served)
	}
}

// deadline turns a failure of the plugin's round caused by the profile's
// deadline into ErrDeadline: the plugin outran its budget.
func (s Service) deadline(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrDeadline
	}
	return err
}

// unserved reports a failure of the engine to serve candidates as it is,
// even when the profile's deadline passed meanwhile: then a dependency of the
// engine (the space owner encoding the query, the index, canonical storage)
// did not answer in time, as when the embedding service is down, so the
// search is unavailable (retryable), not a plugin that outran its budget.
func (s Service) unserved(ctx context.Context, plugin string, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		slog.Warn("search candidates not served within the profile's deadline", "component", "search", "plugin", plugin, "error", err.Error())
	}
	return err
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

// hydrateParallelism bounds concurrent canonical hydrations of one request.
const hydrateParallelism = 8

// server serves the candidate requests of one search.
type server struct {
	s        Service
	scope    corpus.Scope
	routes   []Route
	spaces   []plugins.SearchSpace
	mu       sync.Mutex
	hydrated map[string]content.Hydrated
	// vectors caches query encodings by space and text.
	vectors map[string][]float32
}

// serve runs one candidate request through the projection and hydrates what
// it finds: only authorized, current segments of the routed generations
// reach the plugin, best first, once each.
func (sv *server) serve(ctx context.Context, q Request, c plugins.CandidateRequest) ([]plugins.Candidate, error) {
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
	found, err := sv.s.Projection.Search(ctx, sv.routes, sv.scope, pq)
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
	// Hydrate in index order, a wave at a time, until k candidates are kept:
	// a candidate further down is never read from storage.
	for next := 0; next < len(unique) && len(out) < c.K; {
		wave := unique[next:min(len(unique), next+min(hydrateParallelism, c.K-len(out)))]
		next += len(wave)
		hydrated, err := sv.hydrate(ctx, wave)
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
			if c.Primitive == plugins.PrimitiveNearVector && c.Space == routed[f.GenerationID].Generation.SpaceID && h.EmbeddingID == "" {
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

// hydrate rechecks and reads each candidate from canonical storage, a few at
// a time, reusing segments hydrated earlier in the search. A candidate the
// caller may not read, or that is no longer current, is absent.
func (sv *server) hydrate(ctx context.Context, found []content.Candidate) (map[int]content.Hydrated, error) {
	out := map[int]content.Hydrated{}
	var mu sync.Mutex
	var failure error
	var wg sync.WaitGroup
	slots := make(chan struct{}, hydrateParallelism)
	for i, f := range found {
		sv.mu.Lock()
		h, cached := sv.hydrated[f.SegmentID]
		sv.mu.Unlock()
		if cached {
			out[i] = h
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func(i int, f content.Candidate) {
			defer func() { <-slots; wg.Done() }()
			h, err := sv.s.Content.Hydrate(ctx, sv.scope, f)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, corpus.ErrNotFound):
			case err != nil:
				failure = ErrUnavailable
			default:
				out[i] = h
				sv.mu.Lock()
				sv.hydrated[f.SegmentID] = h
				sv.mu.Unlock()
			}
		}(i, f)
	}
	wg.Wait()
	return out, failure
}

// encode encodes a query with the owner of a space: the engine's E5 encoder
// for the legacy space that Corpora not yet rebuilt since THE-777 serve, the
// pinned ingestion plugin for its own.
func (sv *server) encode(ctx context.Context, space, text string) ([]float32, error) {
	key := space + "\x00" + text
	if v, ok := sv.vectors[key]; ok {
		return v, nil
	}
	var vector []float32
	var err error
	switch {
	case sv.s.Embedder != nil && space == sv.s.Embedder.Space().ID:
		vector, err = sv.s.Embedder.Embed(ctx, "query: "+text)
	case sv.s.Spaces != nil && sv.s.Spaces.Owns(space):
		vector, err = sv.s.Spaces.EncodeQuery(ctx, sv.scope.Organization, space, text)
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
	sv.vectors[key] = vector
	return vector, nil
}
