package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Generation identity and projection flags remain on the original row. Only
// routing settings are overlaid by a single active epoch. Predicates on id and
// organization push down to the original indexes; no JSON row conversion or
// chain of historical epochs is involved.
const effectiveGenerationsSQL = `(SELECT g.id,
 g.collection,
 g.profile_version,
 g.active,
 COALESCE((SELECT o.space_id FROM routing_generation_settings o WHERE o.generation_id=g.id AND o.epoch=(SELECT epoch FROM routing_switch_state WHERE singleton)),g.space_id) AS space_id,
 g.organization,
 g.corpus_id,
 g.retrieval,
 g.retrieval_version,
 g.source_namespace_projected,
 COALESCE((SELECT o.spaces FROM routing_generation_settings o WHERE o.generation_id=g.id AND o.epoch=(SELECT epoch FROM routing_switch_state WHERE singleton)),g.spaces) AS spaces,
 g.spaces_projected,
 g.default_until,
 COALESCE((SELECT o.ingestion_routing FROM routing_generation_settings o WHERE o.generation_id=g.id AND o.epoch=(SELECT epoch FROM routing_switch_state WHERE singleton)),g.ingestion_routing) AS ingestion_routing,
 COALESCE((SELECT o.coverage_routed FROM routing_generation_settings o WHERE o.generation_id=g.id AND o.epoch=(SELECT epoch FROM routing_switch_state WHERE singleton)),g.coverage_routed) AS coverage_routed,
 g.metadata_projected,
 g.item_keywords_projected FROM projection_generations g)`

const markRoutingRecordsSQL = `INSERT INTO routing_dirty_records(epoch,organization,record_id)
 SELECT o.id,k.organization,k.record_id FROM routing_operations o
 CROSS JOIN (SELECT DISTINCT organization,record_id FROM unnest($1::text[],$2::text[]) k(organization,record_id)) k
 WHERE o.observing ORDER BY o.id,k.organization,k.record_id
 ON CONFLICT(epoch,organization,record_id) DO UPDATE SET revision=routing_dirty_records.revision+1`

func markRoutingVersion(ctx context.Context, tx pgx.Tx, org, id string) error {
	_, err := tx.Exec(ctx, `INSERT INTO routing_dirty_records(epoch,organization,record_id)
 SELECT o.id,v.organization,v.record_id FROM routing_operations o,record_versions v
 WHERE o.observing AND v.organization=$1 AND v.id=$2
 ON CONFLICT(epoch,organization,record_id) DO UPDATE SET revision=routing_dirty_records.revision+1`, org, id)
	return err
}

func markRoutingCorpus(ctx context.Context, tx pgx.Tx, org, id string) error {
	_, err := tx.Exec(ctx, `INSERT INTO routing_dirty_corpora(epoch,organization,corpus_id)
 SELECT id,$1,$2 FROM routing_operations WHERE observing
 ON CONFLICT(epoch,organization,corpus_id) DO UPDATE SET cursor_record='',revision=routing_dirty_corpora.revision+1`, org, id)
	return err
}

func markRoutingGeneration(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `INSERT INTO routing_dirty_generations(epoch,generation_id) SELECT id,$1 FROM routing_operations WHERE observing ON CONFLICT DO NOTHING`, id)
	return err
}

// Before changing one generation, fold its current overlay into its base row.
// This keeps ordinary carry/startup mutations from hiding a prior promotion.
func materializeRoutingGeneration(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE projection_generations g SET space_id=o.space_id,spaces=o.spaces,ingestion_routing=o.ingestion_routing,coverage_routed=o.coverage_routed
 FROM routing_generation_settings o,routing_switch_state st WHERE st.singleton AND o.epoch=st.epoch AND o.generation_id=g.id AND g.id=$1`, id)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM routing_generation_settings o USING routing_switch_state st WHERE st.singleton AND o.epoch=st.epoch AND o.generation_id=$1`, id); err != nil {
		return err
	}
	return markRoutingGeneration(ctx, tx, id)
}

// effectiveServingSQL selects the preferred owner by accepted source format.
// A rollback gap keeps its exact outgoing owner until complete recovery removes
// the gap. Stored roles describe only generations without routing metadata.
func effectiveServingSQL(pc, generation string) string {
	return `(` + generation + `.coverage_routed=false AND ` + pc + `.role='served' OR ` + generation + `.coverage_routed=true AND ` + pc + `.plugin_id=COALESCE(
 (SELECT NULLIF(gap.fallback_owner,'') FROM routing_coverage_gaps gap JOIN routing_switch_state st ON st.singleton AND st.epoch=gap.epoch
 WHERE gap.organization=` + pc + `.organization AND gap.record_id=(SELECT record_id FROM record_versions WHERE organization=` + pc + `.organization AND id=` + pc + `.version_id) AND gap.version_id=` + pc + `.version_id),
 (SELECT COALESCE(` + generation + `.ingestion_routing->'routes'->>COALESCE(NULLIF(ar.source_media_type,''),'text/plain'),` + generation + `.ingestion_routing->>'default','')
 FROM record_versions rv JOIN accepted_revisions ar ON (ar.organization,ar.record_id,ar.slot)=(rv.organization,rv.record_id,rv.slot)
 WHERE rv.organization=` + pc + `.organization AND rv.id=` + pc + `.version_id)))`
}

func effectiveCoverageSQL(pc string) string {
	return `EXISTS(SELECT FROM ` + effectiveGenerationsSQL + ` selected_generation WHERE selected_generation.id=` + pc + `.generation_id AND ` + effectiveServingSQL(pc, "selected_generation") + `)`
}

// A retained rollback projection is tied to its original named vector space,
// even when a later command changes that owner's preferred model.
func selectedVectorSpaceSQL(pc, generation string) string {
	return `COALESCE((SELECT NULLIF(gap.fallback_space,'') FROM routing_coverage_gaps gap JOIN routing_switch_state st ON st.singleton AND st.epoch=gap.epoch
 WHERE gap.organization=` + pc + `.organization AND gap.version_id=` + pc + `.version_id AND gap.record_id=(SELECT record_id FROM record_versions WHERE organization=` + pc + `.organization AND id=` + pc + `.version_id) AND gap.fallback_owner=` + pc + `.plugin_id),
 (SELECT sp->>'id' FROM jsonb_array_elements(` + generation + `.spaces) sp WHERE sp->>'owner_plugin_id'=` + pc + `.plugin_id AND sp->>'role'='served' LIMIT 1),` + generation + `.space_id)`
}
