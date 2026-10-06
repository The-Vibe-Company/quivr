package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// spacesLock serializes registrations of concurrent api, worker and migrate
// processes.
const spacesLock = 7760001

// RegisterSpaces records the deployment's vector spaces in the registry and
// retires every other one. A space keeps its first owner: another owner is
// content.ErrSpaceOwner, and another model, dimensions or metric under the
// same id is content.ErrSpaceChanged. The owner's version and the role follow
// the deployment.
func (s SpaceStore) RegisterSpaces(ctx context.Context, spaces []content.RegisteredSpace) error {
	tx, err := database(ctx, s.Pool).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = registerSpaces(ctx, tx, spaces); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// registerSpaces is RegisterSpaces inside tx, which a plugin activation
// commits with its plan.
func registerSpaces(ctx context.Context, tx pgx.Tx, spaces []content.RegisteredSpace) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, spacesLock); err != nil {
		return err
	}
	spaces, err := keepPromotion(ctx, tx, spaces)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(spaces))
	for _, sp := range spaces {
		ids = append(ids, sp.ID)
		var owner, model, metric string
		var dimensions int
		err := tx.QueryRow(ctx, `SELECT owner_plugin_id,model,dimensions,metric FROM vector_spaces WHERE id=$1 FOR UPDATE`, sp.ID).Scan(&owner, &model, &dimensions, &metric)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if _, err = tx.Exec(ctx, `INSERT INTO vector_spaces(id,manifest,name,version,owner_plugin_id,owner_plugin_version,model,dimensions,metric,indexes,query_modalities,role) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
				sp.ID, sp.Manifest, sp.Name, sp.Version, sp.OwnerPluginID, sp.OwnerPluginVersion, sp.Model, sp.Dimensions, sp.Metric, sp.Indexes, sp.QueryModalities, sp.Role); err != nil {
				return err
			}
			continue
		case err != nil:
			return err
		}
		// A row written before the registry (the built-in space, saved with
		// its first vector) has no description yet: it takes this one.
		described := dimensions > 0
		if described && owner != sp.OwnerPluginID {
			return &content.SpaceError{Kind: content.ErrSpaceOwner, Space: sp.ID, Detail: fmt.Sprintf("it belongs to %s; a space has exactly one owner, so declare a space of your own", ownerName(owner))}
		}
		if described && (model != sp.Model || dimensions != sp.Dimensions || metric != sp.Metric) {
			return &content.SpaceError{Kind: content.ErrSpaceChanged, Space: sp.ID, Detail: fmt.Sprintf("it was registered as %s, %d dimensions, %s; bump the space version to register %s, %d dimensions, %s", model, dimensions, metric, sp.Model, sp.Dimensions, sp.Metric)}
		}
		var same bool
		if err = tx.QueryRow(ctx, `SELECT manifest=$2::jsonb FROM vector_spaces WHERE id=$1`, sp.ID, sp.Manifest).Scan(&same); err != nil {
			return err
		}
		if !same {
			return &content.SpaceError{Kind: content.ErrSpaceChanged, Space: sp.ID, Detail: "its description differs from the registered one; bump the space version"}
		}
		if _, err = tx.Exec(ctx, `UPDATE vector_spaces SET name=$2,version=$3,owner_plugin_id=$4,owner_plugin_version=$5,model=$6,dimensions=$7,metric=$8,indexes=$9,query_modalities=$10,role=$11 WHERE id=$1`,
			sp.ID, sp.Name, sp.Version, sp.OwnerPluginID, sp.OwnerPluginVersion, sp.Model, sp.Dimensions, sp.Metric, sp.Indexes, sp.QueryModalities, sp.Role); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE vector_spaces SET role='retired' WHERE NOT (id=ANY($1)) AND role<>'retired'`, ids)
	return err
}

func ownerName(owner string) string {
	if owner == "" {
		return "the engine"
	}
	return "plugin " + owner
}

// deploymentSpacesSQL is the jsonb list of the registry's served and
// evaluation spaces, served first, that a new generation is built with; NULL
// when nothing is registered.
const deploymentSpacesSQL = `(SELECT jsonb_agg(jsonb_build_object('id',vs.id,'metric',vs.metric,'role',vs.role,'owner_plugin_id',vs.owner_plugin_id) ORDER BY vs.role<>'served',vs.id) FROM vector_spaces vs WHERE vs.role IN ('served','evaluation'))`

// servedSpaceSQL is the registry's served space; NULL when none is registered.
const servedSpaceSQL = `(SELECT vs.id FROM vector_spaces vs WHERE vs.role='served' ORDER BY vs.owner_plugin_id IS DISTINCT FROM (SELECT pr.plugin_id FROM active_pipeline_plan ap JOIN pipeline_plan_roles r ON r.plan_id=ap.plan_id AND r.role IN('ingestion','ingestion-default') JOIN plugin_registrations pr ON pr.id=r.registration_id),vs.id LIMIT 1)`

// scanSpaces decodes a generation's spaces.
func scanSpaces(raw []byte) ([]content.GenerationSpace, error) {
	var spaces []content.GenerationSpace
	if len(raw) == 0 {
		return nil, nil
	}
	err := json.Unmarshal(raw, &spaces)
	return spaces, err
}

// VectorSpaces lists the spaces the Corpus's routed generation carries, as
// the registry describes them, with the current segments that hold a vector
// in each, and the total of current segments the generation projects.
func (s SpaceStore) VectorSpaces(ctx context.Context, org, corpusID string) (content.Generation, []content.SpaceCoverage, int64, error) {
	g, err := (ProjectionStore{Pool: s.Pool}).Generation(ctx, org, corpusID)
	if err != nil {
		return g, nil, 0, notFound(err)
	}
	// Bound current-version and cut lookups by each eligible Record even when freshly indexed
	// data has stale statistics. Aggregate coverage by owner/space before
	// joining totals so an underestimated Version count cannot cause a
	// quadratic join. Every count still comes from one database snapshot.
	rows, err := database(ctx, s.Pool).Query(ctx, `WITH current AS MATERIALIZED (
 SELECT cut.segment_id,v.id AS version_id,cut.plugin_id,cut.role,
 count(*) OVER (PARTITION BY cut.plugin_id,v.id) AS version_segments
 FROM records r JOIN LATERAL (
  SELECT v.* FROM record_versions v
  WHERE (v.organization,v.id)=(r.organization,r.current_version_id)
  OFFSET 0
 ) v ON v.record_id=r.id
 JOIN LATERAL (
  SELECT sg.id AS segment_id,pc.plugin_id,pc.role FROM projection_coverage pc
  JOIN segments sg ON (sg.organization,sg.version_id,sg.segmentation_id)=(pc.organization,pc.version_id,pc.segmentation_id)
  WHERE pc.organization=r.organization AND pc.version_id=v.id AND pc.generation_id=$3
  OFFSET 0
 ) cut ON true
 WHERE r.organization=$1 AND r.corpus_id=$2 AND `+eligibleVersionSQL+`
), carried AS (
 SELECT sp.id,COALESCE(vs.owner_plugin_id,'') AS owner FROM unnest($4::text[]) sp(id) LEFT JOIN vector_spaces vs ON vs.id=sp.id
), per_version AS (
 SELECT plugin_id,version_id,role,count(*) AS segments FROM current GROUP BY plugin_id,version_id,role
), owners AS (
 SELECT plugin_id,sum(segments)::bigint AS total,
 COALESCE(sum(segments) FILTER(WHERE role='served'),0)::bigint AS serving
 FROM per_version GROUP BY plugin_id
), vectors AS (
 SELECT cur.plugin_id,cur.version_id,ec.space_id,count(*) AS covered,max(cur.version_segments) AS total
 FROM current cur JOIN LATERAL (
  SELECT space_id FROM embedding_coverage ec
  WHERE ec.organization=$1 AND ec.segment_id=cur.segment_id AND ec.generation_id=$3 AND ec.space_id=ANY($4::text[])
  OFFSET 0
 ) ec ON true
 GROUP BY cur.plugin_id,cur.version_id,ec.space_id
), coverage AS (
 SELECT plugin_id,space_id,sum(covered)::bigint AS covered,
 count(*) FILTER(WHERE covered=total) AS versions FROM vectors GROUP BY plugin_id,space_id
)
SELECT sp.id,COALESCE(vec.covered,0)::bigint,COALESCE(p.total,0)::bigint,
 COALESCE(p.serving,0)::bigint,COALESCE(vec.versions,0)::bigint,
 (SELECT COALESCE(sum(segments),0)::bigint FROM per_version WHERE role='served')
FROM carried sp LEFT JOIN owners p ON p.plugin_id=sp.owner
 LEFT JOIN coverage vec ON vec.plugin_id=sp.owner AND vec.space_id=sp.id`, org, corpusID, g.ID, g.VectorSpaces())
	if err != nil {
		return g, nil, 0, err
	}
	type counts struct{ covered, total, serving, versions int64 }
	bySpace := make(map[string]counts)
	var total int64
	for rows.Next() {
		var id string
		var c counts
		if err = rows.Scan(&id, &c.covered, &c.total, &c.serving, &c.versions, &total); err != nil {
			rows.Close()
			return g, nil, 0, err
		}
		bySpace[id] = c
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return g, nil, 0, err
	}
	out := []content.SpaceCoverage{}
	for _, id := range g.VectorSpaces() {
		var c content.SpaceCoverage
		c.ID = id
		err := database(ctx, s.Pool).QueryRow(ctx, `SELECT manifest,name,version,owner_plugin_id,owner_plugin_version,model,dimensions,metric,indexes,query_modalities,role FROM vector_spaces WHERE id=$1`, id).
			Scan(&c.Manifest, &c.Name, &c.Version, &c.OwnerPluginID, &c.OwnerPluginVersion, &c.Model, &c.VectorSpace.Dimensions, &c.Metric, &c.Indexes, &c.QueryModalities, &c.Role)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return g, nil, 0, err
		}
		c.GenerationRole = content.SpaceEvaluation
		if g.Serves(id) {
			c.GenerationRole = content.SpaceServed
		}
		counts := bySpace[id]
		c.Segments, c.VersionsCovered = counts.covered, counts.versions
		c.TotalSegments = &counts.total
		if c.OwnerPluginID == "" {
			counts.serving = total
		}
		c.ServingSegments = &counts.serving
		out = append(out, c)
	}
	slices.SortStableFunc(out, func(a, b content.SpaceCoverage) int {
		if a.GenerationRole == b.GenerationRole {
			return 0
		}
		if a.GenerationRole == content.SpaceServed {
			return -1
		}
		return 1
	})
	return g, out, total, nil
}

// RegisteredSpaces lists the vector space registry.
func (s SpaceStore) RegisteredSpaces(ctx context.Context) ([]content.RegisteredSpace, error) {
	rows, err := database(ctx, s.Pool).Query(ctx, `SELECT id,manifest,name,version,owner_plugin_id,owner_plugin_version,model,dimensions,metric,indexes,query_modalities,role FROM vector_spaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []content.RegisteredSpace{}
	for rows.Next() {
		var sp content.RegisteredSpace
		if err = rows.Scan(&sp.ID, &sp.Manifest, &sp.Name, &sp.Version, &sp.OwnerPluginID, &sp.OwnerPluginVersion, &sp.Model, &sp.Dimensions, &sp.Metric, &sp.Indexes, &sp.QueryModalities, &sp.Role); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// SpaceStore persists vector space registrations and coverage.
type SpaceStore struct{ Pool *pgxpool.Pool }
