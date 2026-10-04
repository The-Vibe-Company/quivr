-- A route cutover leaves already-pinned work on its original owner. A
-- separately pinned served job publishes pending Versions on the new owner.
ALTER TABLE pipeline_plan_work DROP CONSTRAINT pipeline_plan_work_kind_check;
ALTER TABLE pipeline_plan_work ADD CONSTRAINT pipeline_plan_work_kind_check CHECK(kind IN ('ingestion','connector_run','operation','evaluation','serving_projection'));
CREATE TABLE serving_projections (
 organization text NOT NULL, id text NOT NULL, record_id text NOT NULL, version_id text NOT NULL,
 generation_id text NOT NULL REFERENCES projection_generations(id),
 plugin_id text NOT NULL, registration_id text NOT NULL REFERENCES plugin_registrations(id),
 plan_id text NOT NULL REFERENCES pipeline_plans(id), spaces text[] NOT NULL,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','succeeded','failed','skipped')),
 diagnostic jsonb, dispatched boolean NOT NULL DEFAULT false, lease_until timestamptz NOT NULL DEFAULT '-infinity',
	deadline_attempts integer NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(), completed_at timestamptz,
 PRIMARY KEY(organization,id),
 FOREIGN KEY(organization,version_id) REFERENCES record_versions(organization,id)
);
CREATE INDEX serving_projections_pending ON serving_projections(lease_until) WHERE NOT dispatched AND state='queued';
