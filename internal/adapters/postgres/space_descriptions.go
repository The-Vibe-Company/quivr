package postgres

import (
	"context"
	"errors"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
)

// DescribeVectorSpaces reads only registry metadata and projection presence.
// Coverage counting belongs to a background refresh, never this request.
func (s SpaceStore) DescribeVectorSpaces(ctx context.Context, org, corpusID string) (content.Generation, []content.SpaceCoverage, error) {
	g, err := (ProjectionStore{Pool: s.Pool}).Generation(ctx, org, corpusID)
	if err != nil {
		return g, nil, notFound(err)
	}
	out := []content.SpaceCoverage{}
	for _, id := range g.VectorSpaces() {
		c := content.SpaceCoverage{CoverageUnknown: true}
		c.ID = id
		err = database(ctx, s.Pool).QueryRow(ctx, `SELECT manifest,name,version,owner_plugin_id,owner_plugin_version,model,dimensions,metric,indexes,query_modalities,role FROM vector_spaces WHERE id=$1`, id).
			Scan(&c.Manifest, &c.Name, &c.Version, &c.OwnerPluginID, &c.OwnerPluginVersion, &c.Model, &c.VectorSpace.Dimensions, &c.Metric, &c.Indexes, &c.QueryModalities, &c.Role)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return g, nil, err
		}
		c.GenerationRole = content.SpaceEvaluation
		if g.Serves(id) {
			c.GenerationRole = content.SpaceServed
		}
		// Owner presence preserves independent evaluation routing without a
		// segment count. Legacy engine spaces keep generation routing.
		if c.OwnerPluginID != "" {
			var present bool
			err = database(ctx, s.Pool).QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM projection_coverage pc
 JOIN record_versions v ON (v.organization,v.id)=(pc.organization,pc.version_id)
 JOIN records r ON (r.organization,r.id,r.current_version_id)=(v.organization,v.record_id,v.id)
 WHERE pc.organization=$1 AND pc.generation_id=$3 AND pc.plugin_id=$4 AND pc.role='served'
 AND r.corpus_id=$2 AND `+eligibleVersionSQL+`)`, org, corpusID, g.ID, c.OwnerPluginID).Scan(&present)
			if err != nil {
				return g, nil, err
			}
			presence := int64(0)
			if present {
				presence = 1
			}
			c.ServingSegments = &presence
		}
		out = append(out, c)
	}
	return g, out, nil
}
