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
		// segment count. Start at this Corpus's Records; correlated point reads
		// keep an absent owner from scanning another Corpus's coverage, even
		// with stale statistics. Legacy engine spaces keep generation routing.
		if c.OwnerPluginID != "" {
			var present bool
			err = database(ctx, s.Pool).QueryRow(ctx, spaceOwnerPresenceSQL, org, corpusID, g.ID, c.OwnerPluginID).Scan(&present)
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
	// A missing served owner can mean rebuilding or an empty Corpus. Read
	// current eligibility only in this case; never use old coverage totals.
	for _, c := range out {
		if c.GenerationRole != content.SpaceServed || c.ServingSegments == nil || *c.ServingSegments != 0 {
			continue
		}
		var present bool
		err = database(ctx, s.Pool).QueryRow(ctx, corpusEligibilityPresenceSQL, org, corpusID).Scan(&present)
		if err != nil {
			return g, nil, err
		}
		for i := range out {
			out[i].CorpusEmpty = !present
		}
		break
	}
	return g, out, nil
}

const spaceOwnerPresenceSQL = `SELECT EXISTS(
 SELECT 1 FROM records r
 JOIN LATERAL (
  SELECT v.* FROM record_versions v
  WHERE (v.organization,v.id)=(r.organization,r.current_version_id) OFFSET 0
 ) v ON v.record_id=r.id
 JOIN LATERAL (
  SELECT 1 FROM projection_coverage pc
  WHERE (pc.organization,pc.version_id,pc.generation_id,pc.plugin_id)=(v.organization,v.id,$3,$4)
  AND pc.role='served' OFFSET 0
 ) pc ON true
 WHERE r.organization=$1 AND r.corpus_id=$2 AND ` + eligibleVersionPointSQL + `)`

const corpusEligibilityPresenceSQL = `SELECT EXISTS(
 SELECT 1 FROM records r JOIN LATERAL (
  SELECT v.* FROM record_versions v
  WHERE (v.organization,v.id)=(r.organization,r.current_version_id) OFFSET 0
 ) v ON true
 WHERE r.organization=$1 AND r.corpus_id=$2 AND ` + eligibleVersionPointSQL + `)`
