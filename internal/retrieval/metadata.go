package retrieval

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

// MetadataProjection publishes search and catalog projections from the same
// pinned mapping. A retry can repeat either write before coverage is committed.
type MetadataProjection struct {
	Projection
	Metadata content.MetadataWriter
}

func (p MetadataProjection) Publish(ctx context.Context, g content.Generation, org, corpusID, namespace string, v content.Version, seg content.Segmentation) error {
	if err := p.Projection.Publish(ctx, g, org, corpusID, namespace, v, seg); err != nil {
		return err
	}
	if !g.MetadataProjected {
		return nil
	}
	return p.Metadata.SaveProjectionMetadata(ctx, org, v.ID, g.ID, content.ProjectionMetadata(v, g.Fields))
}

func (p MetadataProjection) UpdateLexicalText(ctx context.Context, g content.Generation, org, corpusID string, updates []content.SegmentLexicalUpdate) error {
	updater, ok := p.Projection.(LexicalProjection)
	if !ok {
		return errors.New("projection does not support lexical text updates")
	}
	return updater.UpdateLexicalText(ctx, g, org, corpusID, updates)
}

func (p MetadataProjection) UsesRouteRankFusion(routes []Route) bool {
	fusion, ok := p.Projection.(RouteRankFusion)
	return ok && fusion.UsesRouteRankFusion(routes)
}
