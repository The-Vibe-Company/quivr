-- Finish work on the plugin version it started with (THE-782, Spec 5 slice
-- 3). Each unfinished piece of work (the processing of an ingestion receipt,
-- a connector run, an Operation) records the Pipeline Plan it started on;
-- its retries and restarts resolve plugins in that plan. A registration that
-- left the active plan is draining while work pinned to a plan naming it
-- remains, and inactive once none does.
CREATE TABLE pipeline_plan_work (
 kind text NOT NULL CHECK (kind IN ('ingestion','connector_run','operation')),
 organization text NOT NULL,
 work_id text NOT NULL,
 plan_id text NOT NULL REFERENCES pipeline_plans(id),
 -- Attempts that found a plugin of the plan unreachable after it left the
 -- active plan; the work stops at the deployment's budget.
 unavailable_attempts integer NOT NULL DEFAULT 0,
 pinned_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (kind, organization, work_id)
);
CREATE INDEX pipeline_plan_work_plan ON pipeline_plan_work(plan_id);
CREATE INDEX pipeline_plan_roles_registration ON pipeline_plan_roles(registration_id);

-- What recorded a registration first. The first component of every
-- invocation idempotency key depends on it: a
-- registration from the configuration keeps the startup placeholder. Every
-- registration recorded so far keeps it, so work in flight converges.
ALTER TABLE plugin_registrations ADD COLUMN origin text NOT NULL DEFAULT 'configuration' CHECK (origin IN ('configuration','registration'));
