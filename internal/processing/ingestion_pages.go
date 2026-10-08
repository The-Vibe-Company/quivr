package processing

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

func (d PluginDeriver) segmentAndEmbed(ctx context.Context, org, corpusID string, v content.Version, spaces []string) ([]PluginSegment, error) {
	if !d.descriptor.Paged {
		return d.Plugin.SegmentAndEmbed(ctx, org, corpusID, v, spaces)
	}
	plugin, ok := d.Plugin.(PagedIngestionPlugin)
	if !ok {
		return nil, content.Refused("the ingestion owner declared pages without implementing them")
	}
	store, ok := d.Content.Baseline.(content.IngestionPageStore)
	if !ok {
		return nil, errors.New("durable ingestion page storage unavailable")
	}
	inputKey, err := ingestionPageInputKey(v, spaces)
	if err != nil {
		return nil, err
	}
	var out []PluginSegment
	var cursor json.RawMessage
	seen := map[string]bool{}
	for number := 0; ; number++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if seen[string(cursor)] {
			return nil, content.Refused("ingestion continuation did not advance")
		}
		seen[string(cursor)] = true
		page, found, err := store.IngestionPage(ctx, org, v.ID, d.descriptor.Recipe, inputKey, number)
		if err != nil {
			return nil, err
		}
		if !found {
			result, err := plugin.SegmentAndEmbedPage(ctx, org, corpusID, v, spaces, cursor)
			if err != nil {
				return nil, err
			}
			if result.Segments == nil {
				result.Segments = []PluginSegment{}
			}
			encoded, err := json.Marshal(result.Segments)
			if err != nil {
				return nil, err
			}
			page, err = store.SaveIngestionPage(ctx, org, v.ID, d.descriptor.Recipe, inputKey, number, content.IngestionPage{Segments: encoded, Next: result.Next})
			if err != nil {
				return nil, err
			}
		}
		var segments []PluginSegment
		if err = json.Unmarshal(page.Segments, &segments); err != nil {
			return nil, content.ErrArtifactCorrupt
		}
		out = append(out, segments...)
		cursor = page.Next
		if len(cursor) == 0 || string(cursor) == "null" {
			break
		}
	}
	return out, nil
}

// completePages runs only after canonical segmentation and every requested
// vector are durable, including retries that reuse already saved artifacts.
func (d PluginDeriver) completePages(ctx context.Context, org string, v content.Version, spaces []string) error {
	if !d.descriptor.Paged {
		return nil
	}
	store, ok := d.Content.Baseline.(content.IngestionPageStore)
	if !ok {
		return errors.New("durable ingestion page storage unavailable")
	}
	key, err := ingestionPageInputKey(v, spaces)
	if err != nil {
		return err
	}
	return store.DeleteIngestionPages(ctx, org, v.ID, d.descriptor.Recipe, key)
}

func ingestionPageInputKey(v content.Version, spaces []string) (string, error) {
	keys := slices.Clone(spaces)
	slices.Sort(keys)
	// A normalization restart can replace Parts under the same Version ID
	// before a complete segmentation exists. Bind the durable namespace to
	// the exact source Manifest as well as spaces, so old in-flight calls and
	// committed pages cannot supply vectors for replacement content.
	raw, err := json.Marshal(struct {
		Spaces []string         `json:"spaces"`
		Source content.Manifest `json:"source"`
	}{keys, v.Manifest})
	if err != nil {
		return "", err
	}
	return content.Hash(raw), nil
}
