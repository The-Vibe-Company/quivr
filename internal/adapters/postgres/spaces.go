package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/jackc/pgx/v5"
)

// spacesLock serializes registrations of concurrent api, worker and migrate
// processes.
const spacesLock = 7760001

// RegisterSpaces records the deployment's vector spaces in the registry and
// retires every other one. A space keeps its first owner: another owner is
// content.ErrSpaceOwner, and another model, dimensions or metric under the
// same id is content.ErrSpaceChanged. The owner's version and the role follow
// the deployment.
func (s ContentStore) RegisterSpaces(ctx context.Context, spaces []content.RegisteredSpace) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, spacesLock); err != nil {
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
	if _, err = tx.Exec(ctx, `UPDATE vector_spaces SET role='retired' WHERE NOT (id=ANY($1)) AND role<>'retired'`, ids); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
const deploymentSpacesSQL = `(SELECT jsonb_agg(jsonb_build_object('id',vs.id,'metric',vs.metric) ORDER BY vs.role<>'served',vs.id) FROM vector_spaces vs WHERE vs.role IN ('served','evaluation'))`

// servedSpaceSQL is the registry's served space; NULL when none is registered.
const servedSpaceSQL = `(SELECT vs.id FROM vector_spaces vs WHERE vs.role='served' ORDER BY vs.id LIMIT 1)`

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
func (s ContentStore) VectorSpaces(ctx context.Context, org, corpusID string) (content.Generation, []content.SpaceCoverage, int64, error) {
	g, err := s.Generation(ctx, org, corpusID)
	if err != nil {
		return g, nil, 0, notFound(err)
	}
	current := `FROM segments sg JOIN records r ON (r.organization,r.current_version_id)=(sg.organization,sg.version_id) JOIN record_versions v ON (v.organization,v.id)=(sg.organization,sg.version_id)
 JOIN projection_coverage pc ON (pc.organization,pc.version_id,pc.segmentation_id,pc.generation_id)=(sg.organization,sg.version_id,sg.segmentation_id,$3)
 WHERE sg.organization=$1 AND r.corpus_id=$2 AND ` + eligibleVersionSQL
	var total int64
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) `+current, org, corpusID, g.ID).Scan(&total); err != nil {
		return g, nil, 0, err
	}
	out := []content.SpaceCoverage{}
	for _, id := range g.VectorSpaces() {
		var c content.SpaceCoverage
		c.ID = id
		err := s.Pool.QueryRow(ctx, `SELECT manifest,name,version,owner_plugin_id,owner_plugin_version,model,dimensions,metric,indexes,query_modalities,role FROM vector_spaces WHERE id=$1`, id).
			Scan(&c.Manifest, &c.Name, &c.Version, &c.OwnerPluginID, &c.OwnerPluginVersion, &c.Model, &c.VectorSpace.Dimensions, &c.Metric, &c.Indexes, &c.QueryModalities, &c.Role)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return g, nil, 0, err
		}
		c.GenerationRole = content.SpaceEvaluation
		if id == g.SpaceID {
			c.GenerationRole = content.SpaceServed
		}
		if err = s.Pool.QueryRow(ctx, `SELECT count(*) `+current+` AND EXISTS(SELECT 1 FROM embedding_coverage ec WHERE ec.organization=sg.organization AND ec.segment_id=sg.id AND ec.generation_id=$3 AND ec.space_id=$4)`, org, corpusID, g.ID, id).Scan(&c.Segments); err != nil {
			return g, nil, 0, err
		}
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
