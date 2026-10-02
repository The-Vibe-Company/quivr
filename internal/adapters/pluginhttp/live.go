package pluginhttp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

// LiveIngestor is the ingestion plugin of the Pipeline Plan: the plan the
// work a call's context carries is pinned to (plugins.Live.Pin), or else the
// active one at that moment. A derivation resolves it once for all its calls
// (processing.Routed). A process always follows a plan with an ingestion
// plugin: startup refuses one without.
type IngestionOwners interface {
	IngestionSpaceOwner(context.Context, string) (*plugins.Pin, error)
}

type LiveIngestor struct {
	Live *plugins.Live
	// Owners retains the registry owners of spaces still carried by older
	// generations after their plugin left the active plan.
	Owners IngestionOwners
}

var (
	_ processing.IngestionPlugin = LiveIngestor{}
	_ processing.Routed          = LiveIngestor{}
	_ retrieval.QueryEncoder     = LiveIngestor{}
)

func (l LiveIngestor) now() Ingestor { return Ingestor{Pin: l.Live.Set().Ingestion()} }

// Ingestor returns one pin bound to the work plan and source route.
func (l LiveIngestor) Ingestor(ctx context.Context, mediaType string) processing.IngestionPlugin {
	if pin := l.Live.Ingestor(ctx, mediaType); pin != nil {
		return Ingestor{Pin: pin}
	}
	return nil
}

// ForSpace resolves the declared owner rather than the deployment default.
func (l LiveIngestor) ForSpace(ctx context.Context, space string) processing.IngestionPlugin {
	if pin := l.Live.SetFor(ctx).SpaceOwner(space); pin != nil {
		return Ingestor{Pin: pin}
	}
	return nil
}

func (l LiveIngestor) Recipe() string              { return l.now().Recipe() }
func (l LiveIngestor) Producer() string            { return l.now().Producer() }
func (l LiveIngestor) Provenance() json.RawMessage { return l.now().Provenance() }
func (l LiveIngestor) Owns(space string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pin, _ := l.queryOwner(ctx, space)
	return pin != nil
}
func (l LiveIngestor) Spaces() []string   { return l.now().Spaces() }
func (l LiveIngestor) SegmentsOnly() bool { return l.now().SegmentsOnly() }
func (l LiveIngestor) VectorSpace(space string) (content.VectorSpace, bool) {
	return l.now().VectorSpace(space)
}
func (l LiveIngestor) SegmentAndEmbed(ctx context.Context, org, corpusID string, v content.Version, spaces []string) ([]processing.PluginSegment, error) {
	if owner := l.Ingestor(ctx, v.SourceMediaType); owner != nil {
		return owner.SegmentAndEmbed(ctx, org, corpusID, v, spaces)
	}
	return nil, ErrUnavailable
}
func (l LiveIngestor) queryOwner(ctx context.Context, space string) (*plugins.Pin, error) {
	if pin := l.Live.SetFor(ctx).SpaceOwner(space); pin != nil {
		return pin, nil
	}
	if l.Owners != nil {
		return l.Owners.IngestionSpaceOwner(ctx, space)
	}
	return nil, nil
}
func (l LiveIngestor) EncodeQuery(ctx context.Context, org, space, query string) ([]float32, error) {
	pin, err := l.queryOwner(ctx, space)
	if err != nil {
		return nil, err
	}
	if pin == nil {
		return nil, ErrUnavailable
	}
	return (Ingestor{Pin: pin}).EncodeQuery(ctx, org, space, query)
}

// LiveRetriever routes deployment profile names in one snapshot of the active
// Pipeline Plan. The returned Retriever remains pinned for the whole search.
type LiveRetriever struct {
	Live    *plugins.Live
	Aliases map[string]string
}

var _ retrieval.ProfileRouter = LiveRetriever{}

func (l LiveRetriever) Profiles() []retrieval.Profile {
	return retrievalProfiles(l.Live.Set(), l.Aliases)
}

func retrievalProfiles(set *plugins.PinSet, aliases map[string]string) []retrieval.Profile {
	profiles, err := set.RetrievalProfiles(aliases)
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
	return l.Snapshot().Resolve(profile)
}

// Snapshot keeps outer and inner rounds on one immutable Pipeline Plan.
func (l LiveRetriever) Snapshot() retrieval.ProfileRouter {
	return snapshotRetriever{set: l.Live.Set(), aliases: l.Aliases}
}

type snapshotRetriever struct {
	set     *plugins.PinSet
	aliases map[string]string
}

func (l snapshotRetriever) Profiles() []retrieval.Profile {
	return retrievalProfiles(l.set, l.aliases)
}

func (l snapshotRetriever) Resolve(profile string) (retrieval.Ranker, string, bool) {
	p, ok := l.set.ResolveRetrievalProfile(profile, l.aliases)
	if !ok {
		return nil, "", false
	}
	return Retriever{Pin: p.Pin}, p.Name, true
}
