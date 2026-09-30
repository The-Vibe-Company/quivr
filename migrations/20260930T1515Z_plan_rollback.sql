-- Roll back to the previous plugin in one call (THE-783, Spec 5 slice 4).
-- Every plan names the plan it replaced, so a rollback knows where to return;
-- plans recorded before this take the plan created just before them.
ALTER TABLE pipeline_plans ADD COLUMN previous_plan_id text REFERENCES pipeline_plans(id);
UPDATE pipeline_plans p SET previous_plan_id=(SELECT q.id FROM pipeline_plans q WHERE (q.created_at,q.id)<(p.created_at,p.id) ORDER BY q.created_at DESC,q.id DESC LIMIT 1);
ALTER TABLE pipeline_plans DROP CONSTRAINT pipeline_plans_source_check;
ALTER TABLE pipeline_plans ADD CONSTRAINT pipeline_plans_source_check CHECK (source IN ('configuration','activation','rollback'));

-- Idempotency keys of rollback requests: the request they carried and the
-- plan they recorded.
CREATE TABLE pipeline_plan_requests (
 request_key text PRIMARY KEY,
 request jsonb NOT NULL,
 plan_id text NOT NULL REFERENCES pipeline_plans(id),
 created_at timestamptz NOT NULL DEFAULT now()
);

-- A rollback with pinned_work=stop marks the work pinned to a plan naming a
-- registration it retires: that work never calls the abandoned version again
-- and stops at its next call.
ALTER TABLE pipeline_plan_work ADD COLUMN stopped_at timestamptz;
