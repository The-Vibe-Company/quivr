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
	// SegmentAndEmbed cuts a Version's text Parts and embeds each segment in
	// the given spaces. A terminal refusal or an answer the engine refuses is
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

// Owns reports whether the pinned plugin owns a space.
func (d PluginDeriver) Owns(space string) bool { return d.Plugin != nil && d.Plugin.Owns(space) }

// Derive returns a Version's plugin segmentation and its vectors in every
// space of the generation the plugin owns, the served one included.
func (d PluginDeriver) Derive(ctx context.Context, org, corpusID string, v content.Version, g content.Generation) (content.Segmentation, []content.EmbeddingData, error) {
	var spaces []string
	for _, key := range g.VectorSpaces() {
		if d.Plugin.Owns(key) {
			spaces = append(spaces, key)
		}
	}
	if !d.Owns(g.SpaceID) || len(spaces) == 0 {
		return content.Segmentation{}, nil, fmt.Errorf("the pinned ingestion plugin does not own space %s", g.SpaceID)
	}
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
	inputs := make([]content.SegmentInput, len(segments))
	for i, s := range segments {
		inputs[i] = s.SegmentInput
	}
	seg, err = content.PluginSegmentation(org, v, d.Plugin.Recipe(), d.Plugin.Provenance(), inputs)
	if err != nil {
		return seg, nil, fmt.Errorf("%w: segments outside the Version's text Parts", content.ErrIngestionRefused)
	}
	if err = d.Content.SaveSegmentation(ctx, org, v, seg); err != nil {
		if errors.Is(err, content.ErrConflict) || errors.Is(err, content.ErrInvalid) {
			// Another answer was stored for the same Version: the plugin is
			// not deterministic, and rebuilds would change what search serves.
			return seg, nil, fmt.Errorf("%w: the segments differ from the stored segmentation", content.ErrIngestionRefused)
		}
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
