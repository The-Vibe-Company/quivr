// Package retrieval owns projection routing, candidate retrieval and canonical hydration.
package retrieval

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/processing"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
)

// ProfileVersion identifies the projection recipe every generation is built
// with; a routed generation of another recipe cannot be searched. This persisted
// index identifier stays stable across search-profile renames.
const ProfileVersion = "balanced.e5-token-windows.v1"

// DefaultProfile answers a search that names no profile. Every retrieval
// plugin declares it beside its own.
const DefaultProfile = plugins.DefaultProfile

// MaxLimit is the largest page a search may request.
const MaxLimit = 50

// CandidateLimit is the fewest projected objects a candidate request fetches.
// An enriched segment has a lexical object and an enriched object, and briefly
// a second enriched object while its embedding is replaced: never more than
// three. Three times MaxLimit objects therefore hold MaxLimit distinct
// segments when that many match, so deduplication cannot shrink a page.
const CandidateLimit = 3 * MaxLimit

var ErrUnsupported = publicerr.UnsupportedSearch

// ErrQueryTooLong reports a query over the length the profile or the owner of
// the searched vector space accepts. It carries a publicerr detail naming the
// limit, which the API returns as the message.
var ErrQueryTooLong = publicerr.QueryTooLong

// ErrUnsupportedProfile reports a profile the pinned retrieval plugin does
// not declare.
var ErrUnsupportedProfile = publicerr.UnsupportedProfile
var ErrUnavailable = publicerr.SearchUnavailable

// ErrMetadataFilterUnavailable requires rebuilding a generation that predates
// typed metadata projection.
var ErrMetadataFilterUnavailable = publicerr.MetadataFilterUnavailable

// ErrSourceFilterUnavailable reports a source filter on a Corpus whose routed
// generation predates projected Source Namespaces; a rebuild enables it.
var ErrSourceFilterUnavailable = publicerr.SourceFilterUnavailable

// ErrRouteChanged reports that routing moved to a generation of other spaces
// while vectors were derived for the prior one; the caller retries.
var ErrRouteChanged = errors.New("projection route changed")

// MaxQueryCodepoints bounds a query and every query text a retrieval plugin
// asks candidates for.
const MaxQueryCodepoints = 8192

// MaxSourceNamespaces bounds the Source Namespaces one search may filter on.
const MaxSourceNamespaces = 50

// ErrProjectionMissing reports that a segment has no projected object in the
// generation, so an embedding cannot be attached to it.
var ErrProjectionMissing = errors.New("projection missing")

// Request is a search, and each candidate request's projection query.
type Request struct {
	Query     string
	CorpusIDs []string
	// EvaluationPlugin selects one ingestion plugin's projected vectors and
	// anchors for an evaluation query. An empty value follows the generation's
	// normal served routing.
	EvaluationPlugin string
	// SourceNamespaces, when set, keeps only Records from these Source
	// Namespaces. The projection applies it before ranking.
	SourceNamespaces      []string
	Metadata              []corpus.MetadataFilter
	RecordIDs, VersionIDs []string
	GroupBy               string
	Mode, Profile         string
	Limit                 int
	Vector                []float32
	// Space names the vector space a projection query ranks in; empty is the
	// routed generations' served space.
	Space string
	// K bounds the projection's objects; 0 means CandidateLimit.
	K int
	// Field is the keyword field: FieldSource (title and body, the default)
	// or FieldLexical (an ingestion plugin's lexical text).
	Field string
	// Hybrid overrides the hybrid weight and fusion; nil keeps alpha 0.5 and
	// relative score fusion.
	Hybrid *HybridOptions
}

// Keyword fields and hybrid fusions a projection query may use.
const (
	FieldSource         = plugins.FieldSource
	FieldLexical        = plugins.FieldLexical
	FusionRelativeScore = plugins.FusionRelativeScore
	FusionRanked        = plugins.FusionRanked
)

// HybridOptions weight and fuse the two sides of a hybrid query.
type HybridOptions struct {
	Alpha  float64
	Fusion string
}

// Hit is one ranked, hydrated segment and the ranking plugin's explanation.
type Hit struct {
	content.Hydrated
	Explanation string
	Score       float64
}

type Result struct {
	Hits            []Hit
	ExcludedCorpora []corpus.CorpusExclusion
	// Profile is the resolved profile name.
	Profile string
	// ProfileVersion identifies what ranked: the retrieval plugin, its
	// version and the profile.
	ProfileVersion string
	// Usage is what the search spent.
	Usage *Usage
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
	// Publish projects a Version's segments into a generation, tagged with
	// their Organization, Corpus and Source Namespace.
	Publish(ctx context.Context, g content.Generation, org, corpusID, namespace string, v content.Version, seg content.Segmentation) error
	PublishEmbeddings(context.Context, content.Generation, string, []content.EmbeddingData) error
	Search(context.Context, []Route, corpus.Scope, Request) ([]content.Candidate, error)
}
type QueryEmbedder interface {
	Embed(context.Context, string) ([]float32, error)
	Space() content.VectorSpace
}

// QueryEncoder encodes queries into the vector spaces a pinned ingestion
// plugin owns, so a query vector comes from the model that made the document
// vectors. A refusal of the query itself is content.ErrInvalid.
type QueryEncoder interface {
	Owns(space string) bool
	EncodeQuery(ctx context.Context, org, space, query string) ([]float32, error)
}
type Service struct {
	// Embedder encodes queries into the legacy E5 space that generations
	// built before the core.ingest plugin (THE-777) still serve, until their
	// Corpus is rebuilt.
	Embedder QueryEmbedder
	// Spaces encodes queries into plugin-owned spaces; nil when no ingestion
	// plugin is pinned.
	Spaces QueryEncoder
	// Ranker is the pinned retrieval plugin, normally core.retrieve: it
	// answers every search, from the candidates the engine serves it.
	Ranker Ranker
	// ProfilesRouter resolves deployment names to individual retrieval plugins.
	ProfilesRouter ProfileRouter
	// Registry describes the vector spaces a ranker may request.
	Registry SpaceRegistry
	// Coverage, when set, keeps the Registry's answers for a short time.
	Coverage   *CoverageCache
	Routing    Routing
	Projection Projection
	Content    content.Service
}

func (s Service) Index(ctx context.Context, org string, v content.Version, seg content.Segmentation) error {
	r, g, err := processing.VersionRoute(ctx, org, v, s.Content, s.Routing)
	if err != nil {
		return err
	}
	if err = s.Projection.Publish(ctx, g, org, r.Source.CorpusID, r.Source.Namespace, v, seg); err != nil {
		return err
	}
	return s.Content.Promote(ctx, org, seg, g)
}

// PrepareIndex writes the external keyword projection before the canonical
// publication transaction. Readers still require canonical eligibility.
func (s Service) PrepareIndex(ctx context.Context, org string, v content.Version, seg content.Segmentation) (content.Generation, error) {
	r, g, err := processing.VersionRoute(ctx, org, v, s.Content, s.Routing)
	if err != nil {
		return g, err
	}
	err = s.Projection.Publish(ctx, g, org, r.Source.CorpusID, r.Source.Namespace, v, seg)
	return g, err
}

// Search validates and authorizes a search, routes each Corpus to its
// generation, and lets the pinned retrieval plugin rank it from the
// candidates the engine serves (rank).
func (s Service) Search(ctx context.Context, scope corpus.Scope, q Request) (Result, error) {
	out := Result{Hits: []Hit{}}
	if err := scope.Require(corpus.ActionRetrievalSearch); err != nil {
		return out, err
	}
	if s.Ranker == nil && s.ProfilesRouter == nil {
		// The api refuses to start without a retrieval plugin.
		return out, ErrUnavailable
	}
	if q.Mode == "" {
		q.Mode = "hybrid"
	}
	if q.Profile == "" {
		q.Profile = DefaultProfile
	}
	if q.Limit == 0 {
		q.Limit = 10
	}
	if router, ok := s.ProfilesRouter.(SnapshotRouter); ok {
		s.ProfilesRouter = router.Snapshot()
	}
	ranker, local, supported := s.resolveProfile(q.Profile)
	if !supported {
		return out, ErrUnsupportedProfile
	}
	out.Profile = q.Profile
	// Hold the concrete plugin for the entire search, including its budgets.
	s.Ranker, q.Profile = ranker, local
	// The whole search, authorization and routing included, runs under the
	// profile's hard bound.
	ctx, cancel := context.WithTimeout(ctx, s.Ranker.Manifest().Contributions.Retrieval.Profiles[q.Profile].Deadline())
	defer cancel()
	if (q.Mode != "lexical" && q.Mode != "semantic" && q.Mode != "hybrid") || q.Limit < 1 || q.Limit > MaxLimit || len(q.CorpusIDs) == 0 || len(q.CorpusIDs) > 16 {
		return out, ErrUnsupported
	}
	if (q.EvaluationPlugin == "") != (q.Space == "") {
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
	if len(q.SourceNamespaces) > MaxSourceNamespaces {
		return out, ErrUnsupported
	}
	namespaces := map[string]bool{}
	for _, ns := range q.SourceNamespaces {
		if ns == "" || namespaces[ns] {
			return out, ErrUnsupported
		}
		namespaces[ns] = true
	}
	for _, ids := range [][]string{q.RecordIDs, q.VersionIDs} {
		if ids != nil && len(ids) == 0 || len(ids) > 50 {
			return out, publicerr.InvalidQuery
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if id == "" || utf8.RuneCountInString(id) > 200 || seen[id] {
				return out, publicerr.InvalidQuery
			}
			seen[id] = true
		}
	}
	if err := corpus.ValidateFilters(q.Metadata); err != nil {
		return out, err
	}
	started := time.Now()
	if err := s.Routing.Authorize(ctx, scope, q.CorpusIDs); err != nil {
		return out, err
	}
	routes := make([]Route, 0, len(q.CorpusIDs))
	served := ""
	for _, id := range q.CorpusIDs {
		g, err := s.Routing.Generation(ctx, scope.Organization, id)
		if err != nil {
			return out, ErrUnavailable
		}
		if g.ProfileVersion != ProfileVersion {
			return out, ErrUnsupported
		}
		if len(q.RecordIDs) > 0 && !g.ItemKeywordsProjected {
			return out, ErrMetadataFilterUnavailable
		}
		if len(q.Metadata) > 0 {
			_, missing, resolveErr := corpus.ResolveFilters(q.Metadata, g.Fields)
			if resolveErr != nil {
				return out, resolveErr
			}
			if len(missing) > 0 {
				out.ExcludedCorpora = append(out.ExcludedCorpora, corpus.CorpusExclusion{CorpusID: id, Fields: missing})
				continue
			}
			if !g.MetadataProjected {
				return out, publicerr.MetadataFilterUnavailable
			}
		}
		// Objects of an older generation carry no Source Namespace, so a
		// filter would silently drop them: refuse instead.
		if len(q.SourceNamespaces) > 0 && !g.SourceNamespaceProjected {
			return out, ErrSourceFilterUnavailable
		}
		// One query ranks every Corpus in one space.
		if q.EvaluationPlugin == "" && served != "" && g.SpaceID != served {
			return out, ErrUnsupported
		}
		served = g.SpaceID
		routes = append(routes, Route{CorpusID: id, Generation: g})
	}
	q.CorpusIDs = nil
	for _, route := range routes {
		q.CorpusIDs = append(q.CorpusIDs, route.CorpusID)
	}
	if len(routes) == 0 {
		if _, err := normalizeQuery(q.Query); err != nil {
			return out, ErrUnsupported
		}
		m := s.Ranker.Manifest()
		out.ProfileVersion = "plugin:" + m.ID + "@" + m.Version + "/" + q.Profile
		return out, nil
	}
	return s.rank(ctx, scope, q, routes, out, started)
}

// normalizeQuery prepares a query: valid text of at most MaxQueryCodepoints,
// line breaks as LF, trimmed, not empty. The space's owner applies its own
// template and length limits.
func normalizeQuery(q string) (string, error) {
	if !content.ValidText(q) || utf8.RuneCountInString(q) > MaxQueryCodepoints {
		return "", content.ErrInvalid
	}
	q = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(q, "\r\n", "\n"), "\r", "\n"))
	if q == "" {
		return "", content.ErrInvalid
	}
	return q, nil
}

// IndexEmbeddings attaches a Version's vectors to its routed generation. A
// Version that is no longer current and eligible has nothing to serve, so its
// enrichment ends here instead of retrying against objects a rebuild never
// projected.
func (s Service) IndexEmbeddings(ctx context.Context, org string, v content.Version, seg content.Segmentation, data []content.EmbeddingData) error {
	eligible, err := s.Content.EnrichmentEligible(ctx, org, v.ID)
	if err != nil {
		return err
	}
	if !eligible {
		return s.Content.EnrichmentProgress(ctx, org, v.ID, "idle", "")
	}
	_, g, err := processing.VersionRoute(ctx, org, v, s.Content, s.Routing)
	if err != nil {
		return err
	}
	if content.CheckCoverage(seg, g, data) != nil {
		// Derived for a generation of other spaces: routing moved meanwhile.
		return ErrRouteChanged
	}
	publish, err := s.Content.ReconcileServingEnrichment(ctx, org, seg, g)
	if err != nil || !publish {
		return err
	}
	if err = s.Projection.PublishEmbeddings(ctx, g, org, data); err != nil {
		if errors.Is(err, ErrProjectionMissing) {
			// Withdrawn or superseded since the check above: end, else retry.
			if eligible, recheck := s.Content.EnrichmentEligible(ctx, org, v.ID); recheck == nil && !eligible {
				return s.Content.EnrichmentProgress(ctx, org, v.ID, "idle", "")
			}
		}
		return err
	}
	return s.Content.CommitEnrichment(ctx, org, seg, g, data)
}
