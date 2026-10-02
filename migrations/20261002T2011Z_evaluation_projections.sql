-- Each ingestion owner projects its own segmentation without replacing the
-- serving owner's coverage. Existing rows retain their served role.
ALTER TABLE projection_coverage ADD COLUMN plugin_id text NOT NULL DEFAULT '';
ALTER TABLE projection_coverage ADD COLUMN role text NOT NULL DEFAULT 'served' CHECK(role IN ('served','evaluation'));
UPDATE projection_coverage pc SET plugin_id=CASE WHEN s.recipe LIKE 'plugin:%' THEN split_part(substring(s.recipe from 8),'@',1) ELSE '' END
FROM segmentations s WHERE (s.organization,s.id)=(pc.organization,pc.segmentation_id);
ALTER TABLE projection_coverage DROP CONSTRAINT projection_coverage_pkey;
ALTER TABLE projection_coverage ADD PRIMARY KEY(organization,version_id,generation_id,plugin_id);
CREATE UNIQUE INDEX projection_coverage_served ON projection_coverage(organization,version_id,generation_id) WHERE role='served';

CREATE FUNCTION projection_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 SELECT CASE WHEN recipe LIKE 'plugin:%' THEN split_part(substring(recipe from 8),'@',1) ELSE '' END INTO NEW.plugin_id
 FROM segmentations WHERE organization=NEW.organization AND id=NEW.segmentation_id;
 RETURN NEW;
END $$;
CREATE TRIGGER projection_coverage_owner BEFORE INSERT OR UPDATE OF segmentation_id ON projection_coverage FOR EACH ROW EXECUTE FUNCTION projection_owner();

-- Routing travels with a generation; activation may change it only after
-- validating coverage. Legacy generations are described when evaluation starts.
ALTER TABLE projection_generations ADD COLUMN ingestion_routing jsonb;

ALTER TABLE pipeline_plan_work DROP CONSTRAINT pipeline_plan_work_kind_check;
ALTER TABLE pipeline_plan_work ADD CONSTRAINT pipeline_plan_work_kind_check CHECK(kind IN ('ingestion','connector_run','operation','evaluation'));
CREATE TABLE ingestion_evaluations (
 organization text NOT NULL, id text NOT NULL, record_id text NOT NULL, version_id text NOT NULL,
 generation_id text NOT NULL REFERENCES projection_generations(id),
 plugin_id text NOT NULL, registration_id text NOT NULL REFERENCES plugin_registrations(id),
 plan_id text NOT NULL REFERENCES pipeline_plans(id), spaces text[] NOT NULL,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','succeeded','failed','skipped')),
 diagnostic jsonb, dispatched boolean NOT NULL DEFAULT false, lease_until timestamptz NOT NULL DEFAULT '-infinity',
 created_at timestamptz NOT NULL DEFAULT now(), completed_at timestamptz,
 PRIMARY KEY(organization,id), UNIQUE(organization,version_id,generation_id,registration_id),
 FOREIGN KEY(organization,version_id) REFERENCES record_versions(organization,id)
);
CREATE INDEX ingestion_evaluations_pending ON ingestion_evaluations(lease_until) WHERE NOT dispatched AND state='queued';
