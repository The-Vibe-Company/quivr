-- W3C trace context belongs to durable work, never canonical request payloads.
-- Empty defaults preserve compatibility with older API and worker versions.
ALTER TABLE ingestion_outbox ADD COLUMN trace_context text NOT NULL DEFAULT '';
ALTER TABLE operation_outbox ADD COLUMN trace_context text NOT NULL DEFAULT '';
ALTER TABLE change_events ADD COLUMN trace_context text NOT NULL DEFAULT '';
ALTER TABLE delivery_outbox ADD COLUMN trace_context text NOT NULL DEFAULT '';
ALTER TABLE evaluation_intents ADD COLUMN trace_context text NOT NULL DEFAULT '';
