// Package retrieval owns projection routing, candidate retrieval and canonical hydration.
package retrieval

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// ProfileVersion identifies the projection recipe every generation is built
// with; a routed generation of another recipe cannot be searched.
const ProfileVersion = "balanced.e5-token-windows.v1"

// DefaultProfile answers a search that names no profile. Every retrieval
// plugin declares it beside its own.
const DefaultProfile = plugins.DefaultProfile

// LegacyProfile is the former name of the default profile, accepted as an
// alias of DefaultProfile for one release.
const LegacyProfile = "balanced"

// MaxLimit is the largest page a search may request.
const MaxLimit = 50

// CandidateLimit is the fewest projected objects a candidate request fetches.
// An enriched segment has a lexical object and an enriched object, and briefly
// a second enriched object while its embedding is replaced: never more than
// three. Three times MaxLimit objects therefore hold MaxLimit distinct
// segments when that many match, so deduplication cannot shrink a page.
const CandidateLimit = 3 * MaxLimit

var ErrUnsupported = errors.New("unsupported_search")

// ErrQueryTooLong reports a query over the length the profile or the owner of
// the searched vector space accepts. It carries a publicerr detail naming the
// limit, which the API returns as the message.
var ErrQueryTooLong = errors.New("query_too_long")

// ErrUnsupportedProfile reports a profile the pinned retrieval plugin does
// not declare.
var ErrUnsupportedProfile = errors.New("unsupported_profile")
var ErrUnavailable = errors.New("search_unavailable")

// ErrSourceFilterUnavailable reports a source filter on a Corpus whose routed
// generation predates projected Source Namespaces; a rebuild enables it.
var ErrSourceFilterUnavailable = errors.New("source_filter_unavailable")

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
	// SourceNamespaces, when set, keeps only Records from these Source
	// Namespaces. The projection applies it before ranking.
	SourceNamespaces []string
	Mode, Profile    string
	Limit            int
	Vector           []float32
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
}

type Result struct {
	Hits []Hit
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
	// Registry describes the vector spaces a ranker may request.
	Registry SpaceRegistry
	// Coverage, when set, keeps the Registry's answers for a short time.
	Coverage   *CoverageCache
	Routing    Routing
	Projection Projection
	Content    content.Service
}

func (s Service) Index(ctx context.Context, org string, v content.Version, seg content.Segmentation) error {
	r, err := s.Content.Record(ctx, corpus.Scope{Organization: org, Actions: []string{"content:read"}, Corpora: []string{"*"}}, v.RecordID)
	if err != nil {
		return err
	}
	g, err := s.Routing.Generation(ctx, org, r.Source.CorpusID)
	if err != nil {
		return err
	}
	if err = s.Projection.Publish(ctx, g, org, r.Source.CorpusID, r.Source.Namespace, v, seg); err != nil {
		return err
	}
	return s.Content.Promote(ctx, org, seg, g)
}

// Search validates and authorizes a search, routes each Corpus to its
// generation, and lets the pinned retrieval plugin rank it from the
// candidates the engine serves (rank).
func (s Service) Search(ctx context.Context, scope corpus.Scope, q Request) (Result, error) {
	out := Result{Hits: []Hit{}}
	if !scope.Allows("content:read") || !scope.Allows("search:query") {
		return out, corpus.ErrForbidden
	}
	if s.Ranker == nil {
		// The api refuses to start without a retrieval plugin.
		return out, ErrUnavailable
	}
	if q.Mode == "" {
		q.Mode = "hybrid"
	}
	if q.Profile == "" || q.Profile == LegacyProfile {
		q.Profile = DefaultProfile
	}
	if q.Limit == 0 {
		q.Limit = 10
	}
	if !s.declares(q.Profile) {
		return out, ErrUnsupportedProfile
	}
	out.Profile = q.Profile
	if (q.Mode != "lexical" && q.Mode != "semantic" && q.Mode != "hybrid") || q.Limit < 1 || q.Limit > MaxLimit || len(q.CorpusIDs) == 0 || len(q.CorpusIDs) > 16 {
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
		// Objects of an older generation carry no Source Namespace, so a
		// filter would silently drop them: refuse instead.
		if len(q.SourceNamespaces) > 0 && !g.SourceNamespaceProjected {
			return out, ErrSourceFilterUnavailable
		}
		// One query ranks every Corpus in one space.
		if served != "" && g.SpaceID != served {
			return out, ErrUnsupported
		}
		served = g.SpaceID
		routes = append(routes, Route{CorpusID: id, Generation: g})
	}
	return s.rank(ctx, scope, q, routes, out)
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
	if err != nil || !eligible {
		return err
	}
	r, err := s.Content.Record(ctx, corpus.Scope{Organization: org, Actions: []string{"content:read"}, Corpora: []string{"*"}}, v.RecordID)
	if err != nil {
		return err
	}
	g, err := s.Routing.Generation(ctx, org, r.Source.CorpusID)
	if err != nil {
		return err
	}
	if content.CheckCoverage(seg, g, data) != nil {
		// Derived for a generation of other spaces: routing moved meanwhile.
		return ErrRouteChanged
	}
	if err = s.Projection.PublishEmbeddings(ctx, g, org, data); err != nil {
		if errors.Is(err, ErrProjectionMissing) {
			// Withdrawn or superseded since the check above: end, else retry.
			if eligible, recheck := s.Content.EnrichmentEligible(ctx, org, v.ID); recheck == nil && !eligible {
				return nil
			}
		}
		return err
	}
	return s.Content.CommitEnrichment(ctx, org, seg, g, data)
}
