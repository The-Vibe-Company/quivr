package pluginhttp

import (
	"context"
	"encoding/json"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

// LiveIngestor is the ingestion plugin of the Pipeline Plan: the plan the
// work a call's context carries is pinned to (plugins.Live.Pin), or else the
// active one at that moment. A derivation resolves it once for all its calls
// (processing.Following). A process always follows a plan with an ingestion
// plugin: startup refuses one without.
type LiveIngestor struct{ Live *plugins.Live }

var (
	_ processing.IngestionPlugin = LiveIngestor{}
	_ processing.Following       = LiveIngestor{}
	_ retrieval.QueryEncoder     = LiveIngestor{}
)

func (l LiveIngestor) now() Ingestor { return Ingestor{Pin: l.Live.Set().Ingestion()} }

// Current is the ingestion plugin of the plan the work ctx carries is pinned
// to, or else of the active plan now.
func (l LiveIngestor) Current(ctx context.Context) processing.IngestionPlugin {
	return Ingestor{Pin: l.Live.SetFor(ctx).Ingestion()}
}

func (l LiveIngestor) Recipe() string              { return l.now().Recipe() }
func (l LiveIngestor) Producer() string            { return l.now().Producer() }
func (l LiveIngestor) Provenance() json.RawMessage { return l.now().Provenance() }
func (l LiveIngestor) Owns(space string) bool      { return l.now().Owns(space) }
func (l LiveIngestor) Spaces() []string            { return l.now().Spaces() }
func (l LiveIngestor) SegmentsOnly() bool          { return l.now().SegmentsOnly() }
func (l LiveIngestor) VectorSpace(space string) (content.VectorSpace, bool) {
	return l.now().VectorSpace(space)
}
func (l LiveIngestor) SegmentAndEmbed(ctx context.Context, org, corpusID string, v content.Version, spaces []string) ([]processing.PluginSegment, error) {
	return l.Current(ctx).SegmentAndEmbed(ctx, org, corpusID, v, spaces)
}
func (l LiveIngestor) EncodeQuery(ctx context.Context, org, space, query string) ([]float32, error) {
	return l.now().EncodeQuery(ctx, org, space, query)
}

// LiveRetriever routes deployment profile names in one snapshot of the active
// Pipeline Plan. The returned Retriever remains pinned for the whole search.
type LiveRetriever struct {
	Live    *plugins.Live
	Aliases map[string]string
}

var _ retrieval.ProfileRouter = LiveRetriever{}

func (l LiveRetriever) Profiles() []retrieval.Profile {
	profiles, err := l.Live.Set().RetrievalProfiles(l.Aliases)
	if err != nil {
		return []retrieval.Profile{}
	}
	out := make([]retrieval.Profile, 0, len(profiles))
	for _, p := range profiles {
		budget := p.Pin.Manifest.Contributions.Retrieval.Profiles[p.Name]
		out = append(out, retrieval.Profile{Name: p.Name, FullName: p.FullName, Aliases: p.Aliases,
			Description: budget.Description, MaxLatencyMS: budget.MaxLatencyMS, MaxCostCents: budget.MaxCostCents,
			PluginID: p.Pin.Manifest.ID, PluginVersion: p.Pin.Manifest.Version})
	}
	return out
}

func (l LiveRetriever) Resolve(profile string) (retrieval.Ranker, string, bool) {
	p, ok := l.Live.Set().ResolveRetrievalProfile(profile, l.Aliases)
	if !ok {
		return nil, "", false
	}
	return Retriever{Pin: p.Pin}, p.Name, true
}
