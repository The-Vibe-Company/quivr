package retrieval

import (
	"context"
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
