-- Deployment routing commands have no invented Corpus or target generation.
-- Separate additive storage preserves the constraints of existing Operations.
ALTER TABLE projection_generations ADD COLUMN coverage_routed boolean NOT NULL DEFAULT false;
CREATE TABLE routing_operations (
 organization text NOT NULL, id text NOT NULL,
 kind text NOT NULL, request_key text NOT NULL, request jsonb NOT NULL,
 state text NOT NULL DEFAULT 'queued', phase text NOT NULL DEFAULT 'prepare',
 observing boolean NOT NULL DEFAULT false,
 previous_epoch text NOT NULL DEFAULT '', previous_plan text NOT NULL DEFAULT '',
 target_plan text NOT NULL DEFAULT '', settings jsonb NOT NULL DEFAULT '{}',
 cursor_organization text NOT NULL DEFAULT '', cursor_record text NOT NULL DEFAULT '',
 cursor_generation text NOT NULL DEFAULT '',
 counters jsonb NOT NULL DEFAULT '{}', errors jsonb NOT NULL DEFAULT '[]', result jsonb,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organization,id), UNIQUE(organization,kind,request_key)
);
CREATE INDEX routing_operations_pending ON routing_operations(id) WHERE observing;
CREATE TABLE routing_operation_outbox (
 organization text NOT NULL, operation_id text NOT NULL,
 dispatched boolean NOT NULL DEFAULT false,
 lease_until timestamptz NOT NULL DEFAULT '-infinity', trace_context text NOT NULL DEFAULT '',
 PRIMARY KEY(organization,operation_id)
);
CREATE INDEX routing_operation_outbox_pending ON routing_operation_outbox(lease_until,operation_id) WHERE NOT dispatched;
CREATE TABLE routing_switch_state (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), epoch text NOT NULL DEFAULT ''
);
CREATE TABLE routing_generation_settings (
 epoch text NOT NULL, generation_id text NOT NULL,
 space_id text NOT NULL, spaces jsonb NOT NULL, ingestion_routing jsonb, coverage_routed boolean NOT NULL DEFAULT false,
 PRIMARY KEY(epoch,generation_id)
);
CREATE TABLE routing_dirty_records (
 epoch text NOT NULL, organization text NOT NULL, record_id text NOT NULL,
 revision bigint NOT NULL DEFAULT 1,
 PRIMARY KEY(epoch,organization,record_id)
);
CREATE TABLE routing_dirty_corpora (
 epoch text NOT NULL, organization text NOT NULL, corpus_id text NOT NULL,
 cursor_record text NOT NULL DEFAULT '', revision bigint NOT NULL DEFAULT 1,
 PRIMARY KEY(epoch,organization,corpus_id)
);
CREATE TABLE routing_dirty_generations (
 epoch text NOT NULL, generation_id text NOT NULL,
 PRIMARY KEY(epoch,generation_id)
);
CREATE TABLE routing_coverage_gaps (
 epoch text NOT NULL, organization text NOT NULL, record_id text NOT NULL,
 corpus_id text NOT NULL, version_id text NOT NULL,
 owner_plugin_id text NOT NULL DEFAULT '', space_id text NOT NULL DEFAULT '',
 missing_segments bigint NOT NULL DEFAULT 0, cutover_required boolean NOT NULL DEFAULT true,
 fallback_owner text NOT NULL DEFAULT '', fallback_space text NOT NULL DEFAULT '',
 PRIMARY KEY(epoch,organization,record_id)
);
-- A stop is published as bounded role metadata, never a sweep of pinned work.
CREATE TABLE routing_work_fences (
 registration_id text NOT NULL, epoch text NOT NULL,
 stopped_before timestamptz NOT NULL DEFAULT now(), next_routing jsonb,
 PRIMARY KEY(registration_id,epoch)
);
