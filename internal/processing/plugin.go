package processing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// IngestionPlugin is the pinned ingestion plugin as the engine calls it. Its
// spaces are named by key ("<space id>@<space version>").
type IngestionPlugin interface {
	// Recipe names the plugin's segmentation: its id and version.
	Recipe() string
	// Producer names the producer of its Embedding Artifacts.
	Producer() string
	// Provenance is recorded with each Segmentation it makes.
	Provenance() json.RawMessage
	// Owns reports whether a space key is one of the plugin's spaces.
	Owns(space string) bool
	// VectorSpace describes one of its spaces, for its Embedding Artifacts.
	VectorSpace(space string) (content.VectorSpace, bool)
	// Spaces are the space keys the deployment enables, the served one first.
	Spaces() []string
	// SegmentsOnly reports whether the plugin answers a request with no
	// space (Plugin API 0.7): its segments alone, without vectors.
	SegmentsOnly() bool
	// SegmentAndEmbed cuts a Version's text Parts and embeds each segment in
	// the given spaces (none: the segments alone, when SegmentsOnly). A
	// terminal refusal or an answer the engine refuses is
	// content.ErrIngestionRefused; any other error is retried.
	SegmentAndEmbed(ctx context.Context, org, corpusID string, v content.Version, spaces []string) ([]PluginSegment, error)
}

// PluginSegment is one segment a plugin returned, with a vector per space key.
type PluginSegment struct {
	content.SegmentInput
	Vectors map[string][]float32
}

// PluginDeriver derives Versions through the pinned ingestion plugin for
// generations whose served space it owns. It stores the Segmentation and an
// Embedding Artifact per segment and space, so enrichment and rebuilds reuse
// them and call the plugin again only for what is missing.
type PluginDeriver struct {
	Content content.Service
	Plugin  IngestionPlugin
}

// Following is an ingestion plugin that follows the active Pipeline Plan
// (Spec 5). A derivation resolves it once, so every call of one derivation
// reaches the same plugin version even when the plan changes meanwhile.
type Following interface {
	Current() IngestionPlugin
}

// resolved is the deriver with the plugin the plan names now.
func (d PluginDeriver) resolved() PluginDeriver {
	if f, ok := d.Plugin.(Following); ok {
		d.Plugin = f.Current()
	}
	return d
}

// Owns reports whether the pinned plugin owns a space.
func (d PluginDeriver) Owns(space string) bool { return d.Plugin != nil && d.Plugin.Owns(space) }

// Segment returns a Version's plugin segmentation for its baseline: the
// stored one, or else the plugin's segments alone, stored, so the Version is
// searchable by keyword without waiting for any embedding backend. A plugin
// limited to Plugin API 0.6 answers segments and vectors in one call; both
// are stored, the vectors in the generation's spaces it owns or, for a
// generation it does not serve, in the spaces the deployment enables.
func (d PluginDeriver) Segment(ctx context.Context, org, corpusID string, v content.Version, g content.Generation) (content.Segmentation, error) {
	d = d.resolved()
	seg, err := d.Content.PluginSegmentationOf(ctx, org, v, d.Plugin.Recipe())
	if !errors.Is(err, corpus.ErrNotFound) {
		return seg, err
	}
	if !d.Plugin.SegmentsOnly() {
		spaces := d.owned(g)
		if len(spaces) == 0 {
			spaces = d.Plugin.Spaces()
		}
		seg, _, err = d.derive(ctx, org, corpusID, v, spaces)
		return seg, err
	}
	segments, err := d.Plugin.SegmentAndEmbed(ctx, org, corpusID, v, nil)
	if err != nil {
		return seg, err
	}
	return d.save(ctx, org, v, segments)
}

// owned lists the generation's spaces the pinned plugin owns.
func (d PluginDeriver) owned(g content.Generation) []string {
	var spaces []string
	for _, key := range g.VectorSpaces() {
		if d.Plugin.Owns(key) {
			spaces = append(spaces, key)
		}
	}
	return spaces
}

// Derive returns a Version's plugin segmentation and its vectors in every
// space of the generation the plugin owns, the served one included.
func (d PluginDeriver) Derive(ctx context.Context, org, corpusID string, v content.Version, g content.Generation) (content.Segmentation, []content.EmbeddingData, error) {
	d = d.resolved()
	spaces := d.owned(g)
	if !d.Owns(g.SpaceID) || len(spaces) == 0 {
		return content.Segmentation{}, nil, fmt.Errorf("the pinned ingestion plugin does not own space %s", g.SpaceID)
	}
	return d.derive(ctx, org, corpusID, v, spaces)
}

// derive reuses the stored segmentation and artifacts and calls the plugin
// only when a vector is missing. The plugin's segments must then be the
// stored ones: a plugin that answers other segments is refused.
func (d PluginDeriver) derive(ctx context.Context, org, corpusID string, v content.Version, spaces []string) (content.Segmentation, []content.EmbeddingData, error) {
	seg, err := d.Content.PluginSegmentationOf(ctx, org, v, d.Plugin.Recipe())
	switch {
	case err == nil:
		data, complete, err := d.stored(ctx, org, corpusID, v, seg, spaces)
		if err != nil || complete {
			return seg, data, err
		}
	case !errors.Is(err, corpus.ErrNotFound):
		return seg, nil, err
	}
	segments, err := d.Plugin.SegmentAndEmbed(ctx, org, corpusID, v, spaces)
	if err != nil {
		return seg, nil, err
	}
	seg, err = d.save(ctx, org, v, segments)
	if err != nil {
		return seg, nil, err
	}
	data := make([]content.EmbeddingData, 0, len(seg.Segments)*len(spaces))
	for i, p := range seg.Segments {
		for _, key := range spaces {
			space, _ := d.Plugin.VectorSpace(key)
			vector := segments[i].Vectors[key]
			input := content.EmbeddingInput(org, corpusID, v, seg, p, space, d.Plugin.Producer())
			artifact, err := d.Content.SaveEmbedding(ctx, input, space, vector)
			if errors.Is(err, content.ErrConflict) || errors.Is(err, content.ErrInvalid) {
				return seg, nil, fmt.Errorf("%w: a vector differs from the stored artifact", content.ErrIngestionRefused)
			}
			if err != nil {
				return seg, nil, err
			}
			data = append(data, content.EmbeddingData{Artifact: artifact, Vector: vector})
		}
	}
	return seg, data, nil
}

// save stores the plugin's segments as the Version's segmentation. Another
// answer already stored for the Version is a refusal: the plugin is not
// deterministic, and search would change under a rebuild.
func (d PluginDeriver) save(ctx context.Context, org string, v content.Version, segments []PluginSegment) (content.Segmentation, error) {
	inputs := make([]content.SegmentInput, len(segments))
	for i, s := range segments {
		inputs[i] = s.SegmentInput
	}
	seg, err := content.PluginSegmentation(org, v, d.Plugin.Recipe(), d.Plugin.Provenance(), inputs)
	if err != nil {
		return seg, fmt.Errorf("%w: segments outside the Version's text Parts", content.ErrIngestionRefused)
	}
	if err = d.Content.SaveSegmentation(ctx, org, v, seg); err != nil {
		if errors.Is(err, content.ErrConflict) || errors.Is(err, content.ErrInvalid) {
			return seg, fmt.Errorf("%w: the segments differ from the segmentation stored for this Version", content.ErrIngestionRefused)
		}
		return seg, err
	}
	return seg, nil
}

// stored loads the verified artifact of every segment in every space; it is
// incomplete when one is missing.
func (d PluginDeriver) stored(ctx context.Context, org, corpusID string, v content.Version, seg content.Segmentation, spaces []string) ([]content.EmbeddingData, bool, error) {
	data := make([]content.EmbeddingData, 0, len(seg.Segments)*len(spaces))
	for _, p := range seg.Segments {
		for _, key := range spaces {
			space, _ := d.Plugin.VectorSpace(key)
			input := content.EmbeddingInput(org, corpusID, v, seg, p, space, d.Plugin.Producer())
			artifact, vector, err := d.Content.LoadEmbedding(ctx, org, input.DerivationID)
			switch {
			case errors.Is(err, corpus.ErrNotFound), errors.Is(err, content.ErrArtifactMissing):
				return nil, false, nil
			case err != nil:
				return nil, false, err
			}
			data = append(data, content.EmbeddingData{Artifact: artifact, Vector: vector})
		}
	}
	return data, true, nil
}
