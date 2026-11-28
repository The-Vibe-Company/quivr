package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
)

// DescribeVectorSpaces reads only registry metadata and projection presence.
// Coverage counting belongs to a background refresh, never this request.
// The routed generation, every space's registry row and owner presence, and
// the Corpus eligibility probe come from one statement: one round trip and
// one snapshot per Corpus.
func (s SpaceStore) DescribeVectorSpaces(ctx context.Context, org, corpusID string) (content.Generation, []content.SpaceCoverage, error) {
	args := []any{org, corpusID}
	sql := describeVectorSpacesSQL(ctx, &args)
	rows, err := database(ctx, s.Pool).Query(ctx, sql, args...)
	if err != nil {
		return content.Generation{}, nil, err
	}
	var row generationRow
	var eligible *bool
	routed := false
	described := map[string]describedSpace{}
	for rows.Next() {
		var d describedSpace
		var id *string
		targets := append(row.targets(), &id)
		targets = append(targets, spaceMetadataTargets(&d.coverage)...)
		if err = rows.Scan(append(targets, &d.present, &eligible)...); err != nil {
			rows.Close()
			return content.Generation{}, nil, err
		}
		routed = true
		if id != nil {
			d.coverage.ID = *id
			described[*id] = d
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return content.Generation{}, nil, err
	}
	if !routed {
		return content.Generation{}, nil, notFound(pgx.ErrNoRows)
	}
	g, err := row.decode()
	if err != nil {
		return g, nil, notFound(err)
	}
	out := []content.SpaceCoverage{}
	for _, id := range g.VectorSpaces() {
		d, ok := described[id]
		if !ok {
			// PostgreSQL lists the spaces from canonical JSON keys; a
			// generation whose stored keys only Go's decoder matches is
			// described space by space instead.
			if d, err = s.describeSpace(ctx, org, corpusID, g, id); err != nil {
				return g, nil, err
			}
		}
		c := d.coverage
		c.CoverageUnknown = true
		c.GenerationRole = content.SpaceEvaluation
		if g.Serves(id) {
			c.GenerationRole = content.SpaceServed
		}
		// Owner presence preserves independent evaluation routing without a
		// segment count. Legacy engine spaces keep generation routing.
		if c.OwnerPluginID != "" {
			presence := int64(0)
			if d.present != nil && *d.present {
				c.GenerationRole = content.SpaceServed
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
		if eligible == nil {
			// The statement evaluated eligibility from PostgreSQL's reading
			// of the generation roles; read it now if Go's reading differs.
			present := false
			args := []any{org, corpusID}
			sql := presenceVisibility(ctx, corpusEligibilityPresenceSQL, &args)
			if err = database(ctx, s.Pool).QueryRow(ctx, sql, args...).Scan(&present); err != nil {
				return g, nil, err
			}
			eligible = &present
		}
		for i := range out {
			out[i].CorpusEmpty = !*eligible
		}
		break
	}
	return g, out, nil
}

type describedSpace struct {
	coverage content.SpaceCoverage
	present  *bool
}

// describeSpace reads one space's registry row and owner presence. Presence
// selects the owner's served space from g as Go decoded it: its spaces and
// ingestion routing are passed re-encoded with canonical keys, which the
// stored generation's JSON selectors would otherwise miss.
func (s SpaceStore) describeSpace(ctx context.Context, org, corpusID string, g content.Generation, id string) (describedSpace, error) {
	var d describedSpace
	d.coverage.ID = id
	err := database(ctx, s.Pool).QueryRow(ctx, `SELECT `+spaceMetadataSQL+` FROM vector_spaces vs WHERE vs.id=$1`, id).
		Scan(spaceMetadataTargets(&d.coverage)...)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return d, err
	}
	if d.coverage.OwnerPluginID == "" {
		return d, nil
	}
	spaces, err := json.Marshal(append([]content.GenerationSpace{}, g.Spaces...))
	if err != nil {
		return d, err
	}
	routing, err := json.Marshal(g.IngestionRouting)
	if err != nil {
		return d, err
	}
	present := false
	args := []any{org, corpusID, g.ID, d.coverage.OwnerPluginID, id, spaces, routing}
	decoded := `(SELECT eg.id,eg.space_id,eg.coverage_routed,$7::jsonb AS ingestion_routing,$6::jsonb AS spaces FROM ` + effectiveGenerationsSQL + ` eg)`
	sql := presenceVisibility(ctx, spaceOwnerPresence(decoded, "$3", "$4", "$5"), &args)
	err = database(ctx, s.Pool).QueryRow(ctx, sql, args...).Scan(&present)
	d.present = &present
	return d, err
}

// describeVectorSpacesSQL reads the generation routed for Corpus $2 of
// Organization $1, then one row per space it carries (content.Generation's
// VectorSpaces, from the same JSON): the registry row, whether
// the generation serves the space (content.Generation's Serves), and owner
// presence. Every row carries Corpus eligibility, evaluated once and only
// when a served owner is absent. An unregistered space keeps zero metadata.
func describeVectorSpacesSQL(ctx context.Context, args *[]any) string {
	const spaces = `(CASE WHEN jsonb_typeof(routed.spaces)='array' THEN routed.spaces ELSE '[]' END)`
	const projected = `(routed.spaces_projected AND jsonb_array_length(` + spaces + `)>0)`
	presence := presenceVisibility(ctx, spaceOwnerPresence(effectiveGenerationsSQL, "routed.id", "vs.owner_plugin_id", "listed.id"), args)
	eligibility := presenceVisibility(ctx, corpusEligibilityPresenceSQL, args)
	return `WITH routed AS MATERIALIZED (` + routedGenerationQuerySQL + `
), listed AS MATERIALIZED (
 SELECT l.id FROM routed CROSS JOIN LATERAL (
  SELECT COALESCE(e.sp->>'id','') AS id FROM jsonb_array_elements(` + spaces + `) e(sp) WHERE ` + projected + `
  UNION ALL SELECT routed.space_id WHERE NOT ` + projected + `
 ) l
), described AS MATERIALIZED (
 SELECT listed.id,` + spaceMetadataSQL + `,
 COALESCE((SELECT e.sp->>'role'='served' FROM jsonb_array_elements(` + spaces + `) WITH ORDINALITY e(sp,ord)
  WHERE COALESCE(e.sp->>'id','')=listed.id AND COALESCE(e.sp->>'role','')<>'' ORDER BY e.ord LIMIT 1),listed.id=routed.space_id) AS served,
 CASE WHEN vs.owner_plugin_id<>'' THEN (` + presence + `) END AS present
 FROM (SELECT DISTINCT id FROM listed) listed CROSS JOIN routed LEFT JOIN vector_spaces vs ON vs.id=listed.id
)
SELECT ` + routedColumnsSQL + `,d.id,` + spaceMetadataColumns("d") + `,d.present,
 CASE WHEN EXISTS(SELECT 1 FROM described absent WHERE absent.served AND absent.present IS FALSE) THEN (` + eligibility + `) END
FROM routed LEFT JOIN described d ON true`
}

// spaceMetadataSQL selects registry row vs, which a left join may leave
// absent, as spaceMetadataTargets scans it: zero values for a missing row.
const spaceMetadataSQL = `vs.manifest,COALESCE(vs.name,'') AS name,COALESCE(vs.version,'') AS version,COALESCE(vs.owner_plugin_id,'') AS owner,COALESCE(vs.owner_plugin_version,'') AS owner_version,COALESCE(vs.model,'') AS model,COALESCE(vs.dimensions,0) AS dimensions,COALESCE(vs.metric,'') AS metric,vs.indexes,vs.query_modalities,COALESCE(vs.role,'') AS role`

// spaceMetadataColumns lists spaceMetadataSQL's columns from relation alias.
func spaceMetadataColumns(alias string) string {
	columns := []string{"manifest", "name", "version", "owner", "owner_version", "model", "dimensions", "metric", "indexes", "query_modalities", "role"}
	for i, column := range columns {
		columns[i] = alias + "." + column
	}
	return strings.Join(columns, ",")
}

func spaceMetadataTargets(c *content.SpaceCoverage) []any {
	return []any{&c.Manifest, &c.Name, &c.Version, &c.OwnerPluginID, &c.OwnerPluginVersion, &c.Model, &c.VectorSpace.Dimensions, &c.Metric, &c.Indexes, &c.QueryModalities, &c.Role}
}

// spaceOwnerPresence reports whether the owner's served projection in the
// generation selects the space for a current eligible Record of Corpus $2;
// generations is the relation the generation's routing settings come from.
// Start at this Corpus's Records; correlated point reads keep an absent owner
// from scanning another Corpus's coverage, even with stale statistics.
func spaceOwnerPresence(generations, generation, owner, space string) string {
	return `SELECT EXISTS(
 SELECT 1 FROM records r
 JOIN LATERAL (
  SELECT v.* FROM record_versions v
  WHERE (v.organization,v.id)=(r.organization,r.current_version_id) OFFSET 0
 ) v ON v.record_id=r.id
 JOIN LATERAL (
  SELECT 1 FROM projection_coverage pc
  JOIN ` + generations + ` selected_g ON selected_g.id=pc.generation_id
  WHERE (pc.organization,pc.version_id,pc.generation_id,pc.plugin_id)=(v.organization,v.id,` + generation + `,` + owner + `)
  AND ` + effectiveServingSQL("pc", "selected_g") + `
  AND ` + space + `=` + selectedVectorSpaceSQL("pc", "selected_g") + ` OFFSET 0
 ) pc ON true
 WHERE r.organization=$1 AND r.corpus_id=$2 AND ` + eligibleVersionPointSQL + `)`
}

var spaceOwnerPresenceSQL = spaceOwnerPresence(effectiveGenerationsSQL, "$3", "$4", "$5")

const corpusEligibilityPresenceSQL = `SELECT EXISTS(
 SELECT 1 FROM records r JOIN LATERAL (
  SELECT v.* FROM record_versions v
  WHERE (v.organization,v.id)=(r.organization,r.current_version_id) OFFSET 0
 ) v ON true
 WHERE r.organization=$1 AND r.corpus_id=$2 AND ` + eligibleVersionPointSQL + `)`

// Both presence queries end in the closing parenthesis of their EXISTS.
func presenceVisibility(ctx context.Context, sql string, args *[]any) string {
	return strings.TrimSuffix(sql, ")") + visibilityPredicate(ctx, args, "r") + ")"
}
