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

const ProfileVersion = "balanced.e5-token-windows.v1"

// DefaultProfile answers a search that names no profile. The built-in path
// has no other; a pinned retrieval plugin declares it beside its own.
const DefaultProfile = plugins.DefaultProfile

// LegacyProfile is the former name of the default profile, accepted as an
// alias of DefaultProfile for one release.
const LegacyProfile = "balanced"

// MaxLimit is the largest page a search may request.
const MaxLimit = 50

// CandidateLimit bounds the candidates fetched from the projection per search.
// An enriched segment has a lexical object and an enriched object, and briefly
// a second enriched object while its embedding is replaced: never more than
// three. Three times MaxLimit candidates therefore hold MaxLimit distinct
// segments when that many match, so deduplication cannot shrink a page.
const CandidateLimit = 3 * MaxLimit

var ErrUnsupported = errors.New("unsupported_search")

// ErrUnsupportedProfile reports a profile neither the built-in path nor the
// pinned retrieval plugin declares.
var ErrUnsupportedProfile = errors.New("unsupported_profile")
var ErrUnavailable = errors.New("search_unavailable")

// ErrSourceFilterUnavailable reports a source filter on a Corpus whose routed
// generation predates projected Source Namespaces; a rebuild enables it.
var ErrSourceFilterUnavailable = errors.New("source_filter_unavailable")

// ErrSpaceUnavailable reports a request for a named vector space that a
// routed generation does not carry: it was built before named spaces, or
// without that space. A rebuild enables it.
var ErrSpaceUnavailable = errors.New("space_unavailable")

// ErrRouteChanged reports that routing moved to a generation of other spaces
// while vectors were derived for the prior one; the caller retries.
var ErrRouteChanged = errors.New("projection route changed")

// MaxQueryCodepoints bounds a query sent to an ingestion plugin's space.
const MaxQueryCodepoints = 8192

// MaxSourceNamespaces bounds the Source Namespaces one search may filter on.
const MaxSourceNamespaces = 50

// ErrProjectionMissing reports that a segment has no projected object in the
// generation, so an embedding cannot be attached to it.
var ErrProjectionMissing = errors.New("projection missing")

type Request struct {
	Query     string
	CorpusIDs []string
	// SourceNamespaces, when set, keeps only Records from these Source
	// Namespaces. The projection applies it before ranking.
	SourceNamespaces []string
	Mode, Profile    string
	Limit            int
	Vector           []float32
	// Space names the vector space to search, which every routed generation
	// must carry as a named space; empty searches the generations' served
	// space. The projection receives the resolved space.
	Space string
	// K bounds the projection's candidates; 0 means CandidateLimit.
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
	// ProfileVersion identifies what ranked: the immutable profile shared by
	// every routed generation, or the retrieval plugin and its profile.
	ProfileVersion string
	// Usage is reported when a retrieval plugin ranked.
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
type QueryNormalizer interface {
	NormalizeQuery(context.Context, string) (string, error)
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
	// Embedder encodes queries into the built-in space, after QueryNormalizer.
	Embedder        QueryEmbedder
	QueryNormalizer QueryNormalizer
	// Spaces encodes queries into plugin-owned spaces; nil when no ingestion
	// plugin is pinned.
	Spaces QueryEncoder
	// Ranker answers searches when a retrieval plugin is pinned; nil keeps
	// the built-in path.
	Ranker Ranker
	// Registry describes the vector spaces a ranker may request.
	Registry   SpaceRegistry
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
func (s Service) Search(ctx context.Context, scope corpus.Scope, q Request) (Result, error) {
	out := Result{Hits: []Hit{}}
	if !scope.Allows("content:read") || !scope.Allows("search:query") {
		return out, corpus.ErrForbidden
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
	routed := map[string]string{}
	named := q.Space != ""
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
		switch {
		case named && (!g.SpacesProjected || !g.Carries(q.Space)):
			return out, ErrSpaceUnavailable
		case !named && q.Space == "":
			q.Space = g.SpaceID
		case !named && q.Space != g.SpaceID:
			// One query ranks every Corpus in one space.
			return out, ErrUnsupported
		}
		routes = append(routes, Route{CorpusID: id, Generation: g})
		routed[g.ID] = id
	}
	if s.Ranker != nil {
		return s.rank(ctx, scope, q, routes, out)
	}
	builtin := q.Space == s.Embedder.Space().ID
	plugin := !builtin && s.Spaces != nil && s.Spaces.Owns(q.Space)
	if !builtin && !plugin && (named || q.Mode != "lexical") {
		// No pinned owner can encode a query into this space.
		return out, ErrUnsupported
	}
	var normalized string
	var err error
	if builtin {
		normalized, err = s.QueryNormalizer.NormalizeQuery(ctx, q.Query)
	} else {
		normalized, err = normalizeQuery(q.Query)
	}
	if errors.Is(err, content.ErrInvalid) {
		return out, ErrUnsupported
	}
	if err != nil {
		return out, ErrUnavailable
	}
	q.Query = normalized
	out.ProfileVersion = ProfileVersion
	if q.Mode != "lexical" {
		if builtin {
			q.Vector, err = s.Embedder.Embed(ctx, "query: "+q.Query)
		} else {
			q.Vector, err = s.Spaces.EncodeQuery(ctx, scope.Organization, q.Space, q.Query)
			if errors.Is(err, content.ErrInvalid) {
				// The owner refuses this query: it can never be encoded.
				return out, ErrUnsupported
			}
		}
		if err != nil {
			return out, ErrUnavailable
		}
	}
	candidates, err := s.Projection.Search(ctx, routes, scope, q)
	if err != nil {
		return out, ErrUnavailable
	}
	// Narrow hydration to the requested scope as well as the caller's grants.
	scope.Corpora = q.CorpusIDs
	segments := map[string]bool{}
	for _, c := range candidates {
		if routed[c.GenerationID] == "" || segments[c.SegmentID] {
			continue
		}
		h, err := s.Content.Hydrate(ctx, scope, c)
		if errors.Is(err, corpus.ErrNotFound) {
			continue
		}
		if err != nil {
			return out, ErrUnavailable
		}
		if q.Mode == "semantic" && h.EmbeddingID == "" {
			continue
		}
		segments[c.SegmentID] = true
		out.Hits = append(out.Hits, Hit{Hydrated: h})
		if len(out.Hits) == q.Limit {
			break
		}
	}
	return out, nil
}

// normalizeQuery prepares a query for a plugin-owned space: valid text of at
// most MaxQueryCodepoints, line breaks as LF, trimmed, not empty. The plugin
// applies its own template and length limits.
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
