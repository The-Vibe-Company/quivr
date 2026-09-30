-- Reprocess past articles with an ingestion plugin (THE-784, Spec 5 slice 5).
-- A backfill is an Operation that fills vector spaces of a Corpus's routed
-- generation for the Versions it already serves, paced below live ingestion.
-- It can be paused, which is a new Operation state.
ALTER TABLE operations DROP CONSTRAINT operations_kind_check;
ALTER TABLE operations ADD CONSTRAINT operations_kind_check CHECK(kind IN ('projection_rebuild','retrieval_configuration','backfill'));
ALTER TABLE operations DROP CONSTRAINT operations_state_check;
ALTER TABLE operations ADD CONSTRAINT operations_state_check CHECK(state IN ('queued','running','paused','succeeded','failed','cancel_requested','canceled'));

-- A dry run records its estimate under the request's key. A backfill is
-- accepted only with the key and scope of a recorded dry run, so the operator
-- has seen the volume, duration and cost of what starts.
CREATE TABLE backfill_estimates (
 organization text NOT NULL,
 corpus_id text NOT NULL,
 request_key text NOT NULL,
 canonical_request bytea NOT NULL,
 estimate jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (organization, corpus_id, request_key),
 FOREIGN KEY (organization, corpus_id) REFERENCES corpora(organization, id)
);

-- What a backfill Operation fills and how far it got. The checkpoint is the
-- last Version id it finished, in Version id order; plan_id is the Pipeline
-- Plan its first step was pinned to.
CREATE TABLE backfills (
 organization text NOT NULL,
 operation_id text NOT NULL,
 registration_id text NOT NULL REFERENCES plugin_registrations(id),
 spaces text[] NOT NULL,
 accepted_after timestamptz,
 accepted_before timestamptz,
 plan_id text REFERENCES pipeline_plans(id),
 checkpoint text NOT NULL DEFAULT '',
 estimate jsonb NOT NULL,
 PRIMARY KEY (organization, operation_id),
 FOREIGN KEY (organization, operation_id) REFERENCES operations(organization, id)
);

-- The served space an operator promoted. Registering the deployment's spaces
-- (startup, activation, rollback) keeps it served while the ingestion plugin
-- still enables it and the space it replaced; otherwise the pin decides again
-- and the row is removed.
CREATE TABLE vector_space_promotions (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 served_space_id text NOT NULL,
 previous_space_id text NOT NULL,
 promoted_at timestamptz NOT NULL DEFAULT now()
);
