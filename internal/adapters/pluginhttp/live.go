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

// LiveRetriever is the retrieval plugin of the active Pipeline Plan. Search
// uses it only when the process started with a retrieval plugin; an
// activation never adds or removes the retrieval role (registry). Should a
// configuration applied by another process's restart remove it, the process
// keeps Started until it restarts too.
type LiveRetriever struct {
	Live    *plugins.Live
	Started *plugins.Pin
}

var _ retrieval.Ranker = LiveRetriever{}

func (l LiveRetriever) now() Retriever {
	if pin := l.Live.Set().Retrieval(); pin != nil {
		return Retriever{Pin: pin}
	}
	return Retriever{Pin: l.Started}
}

func (l LiveRetriever) Manifest() *plugins.Manifest    { return l.now().Manifest() }
func (l LiveRetriever) Configuration() json.RawMessage { return l.now().Configuration() }
func (l LiveRetriever) Round(ctx context.Context, request plugins.SearchRequest) ([]byte, error) {
	return l.now().Round(ctx, request)
}
