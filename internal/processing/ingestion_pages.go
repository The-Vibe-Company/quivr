package processing

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
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
		return nil, err
	}
	inputKey := content.Hash(raw)
	// Keep consecutive Parts together for the owner's normal packing recipe.
	// A single text Part retains its existing paged segmentation. Size limits
	// partition work only: oversized items and size refusals still page in full.
	if wholeItemFits(v) {
		_, committed, err := store.IngestionPage(ctx, org, v.ID, d.descriptor.Recipe, inputKey, 0)
		if err != nil {
			return nil, err
		}
		if !committed {
			segments, err := d.Plugin.SegmentAndEmbed(ctx, org, corpusID, v, spaces)
			if err == nil {
				return segments, nil
			}
			var refusal *plugins.PluginError
			if !errors.Is(err, content.ErrIngestionRefused) || !errors.As(err, &refusal) || refusal.Retryable ||
				(refusal.Code != "segmentation_limit" && refusal.Code != "input_size") {
				return nil, err
			}
		}
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

func wholeItemFits(v content.Version) bool {
	if len(v.Manifest.Parts) > 64 {
		return false
	}
	textParts, bytes := 0, 0
	for _, part := range v.Manifest.Parts {
		if part.Content.Kind == "text" {
			textParts++
			bytes += len(part.Content.Text)
			if bytes > 256<<10 {
				return false
			}
		}
	}
	return textParts > 1
}
