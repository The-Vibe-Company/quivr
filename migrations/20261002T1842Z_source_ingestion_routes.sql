-- Source format is acceptance metadata, independent of normalized text and
-- of the canonical command/digest (receipt replays keep their identity).
ALTER TABLE accepted_revisions ADD COLUMN source_media_type text NOT NULL DEFAULT 'text/plain';
UPDATE accepted_revisions a SET source_media_type=COALESCE(
 NULLIF(a.command->'content'->>'media_type',''),
 (SELECT b.media_type FROM verified_blobs b
  WHERE b.organization=a.organization AND b.blob_id=a.command->'provenance'->'source_blob_ids'->>0),
 'text/plain');

-- A receipt executes one routed ingestion plugin. Its plan still also pins
-- normalization; drain counts and failure budgets distinguish ingestion owners.
ALTER TABLE pipeline_plan_work
 ADD COLUMN ingestion_registration_id text REFERENCES plugin_registrations(id),
 ADD COLUMN plugin_unavailable_attempts jsonb NOT NULL DEFAULT '{}';

-- A generation preserves the served/evaluation role and owner of each space.
-- Existing generations keep the one space they actually served.
UPDATE projection_generations g SET spaces=(
 SELECT jsonb_agg(e || jsonb_build_object(
  'role',CASE WHEN e->>'id'=g.space_id THEN 'served' ELSE 'evaluation' END,
  'owner_plugin_id',COALESCE(vs.owner_plugin_id,'')) ORDER BY n)
 FROM jsonb_array_elements(g.spaces) WITH ORDINALITY AS s(e,n)
 LEFT JOIN vector_spaces vs ON vs.id=e->>'id'
) WHERE jsonb_array_length(g.spaces)>0;

-- Promoting one owner's space must not replace another owner's served space.
ALTER TABLE vector_space_promotions ADD COLUMN owner_plugin_id text NOT NULL DEFAULT '';
UPDATE vector_space_promotions p SET owner_plugin_id=vs.owner_plugin_id
 FROM vector_spaces vs WHERE vs.id=p.served_space_id;
ALTER TABLE vector_space_promotions DROP COLUMN singleton;
ALTER TABLE vector_space_promotions ADD PRIMARY KEY(owner_plugin_id);
