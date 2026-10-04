-- Canonical per-Corpus Projection Generation routing. A Corpus without a route
-- is served by the default (active) generation; a validated rebuild installs a
-- route atomically with its Operation outcome.
-- Logical generations now share a physical collection.
ALTER TABLE projection_generations DROP CONSTRAINT projection_generations_collection_key;
ALTER TABLE projection_generations ADD COLUMN organization text;
ALTER TABLE projection_generations ADD COLUMN corpus_id text;
CREATE TABLE corpus_projection_routes (
 organization text NOT NULL, corpus_id text NOT NULL,
 generation_id text NOT NULL REFERENCES projection_generations(id),
 PRIMARY KEY(organization,corpus_id),
 FOREIGN KEY(organization,corpus_id) REFERENCES corpora(organization,id)
);
-- Durable administrative execution. Retries keep identity; request identity is
-- Organization + kind (route) + Corpus + idempotency key.
CREATE TABLE operations (
 organization text NOT NULL, id text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('projection_rebuild')),
 corpus_id text NOT NULL,
 request_key text NOT NULL, canonical_request bytea NOT NULL,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','succeeded','failed','cancel_requested','canceled')),
 target_generation_id text NOT NULL REFERENCES projection_generations(id),
 previous_operation_id text,
 counters jsonb NOT NULL DEFAULT '{}',
 errors jsonb NOT NULL DEFAULT '[]',
 result jsonb,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organization,id),
 UNIQUE(organization,kind,corpus_id,request_key),
 FOREIGN KEY(organization,corpus_id) REFERENCES corpora(organization,id),
 CHECK(state<>'succeeded' OR result IS NOT NULL)
);
CREATE TABLE operation_outbox (
 organization text NOT NULL, operation_id text NOT NULL,
 dispatched boolean NOT NULL DEFAULT false,
 lease_until timestamptz NOT NULL DEFAULT '-infinity',
 PRIMARY KEY(organization,operation_id),
 FOREIGN KEY(organization,operation_id) REFERENCES operations(organization,id)
);
-- Evaluation cutover to the generation-tagged QuivrTextV4 collection. Restart the
-- API and workers after migrating; every accepted Receipt replays through a new
-- workflow identity. Enrichment reuses stored checksummed Embedding Artifacts,
-- so replay does not call inference for already-embedded segments.
UPDATE projection_generations SET active=false;
UPDATE record_versions SET baseline_ready=false,processing='queued',error_code='' WHERE NOT quarantined;
UPDATE ingestion_outbox SET dispatched=false,lease_until='-infinity';
