-- Per-UTC-day source read usage and kind-defined diagnostics of a Connector
-- Instance. usage_day stays NULL for kinds that never report reads; the
-- counters roll over when a page is committed on a later UTC day.
ALTER TABLE connector_instances
 ADD COLUMN usage_day date,
 ADD COLUMN usage_items bigint NOT NULL DEFAULT 0 CHECK (usage_items >= 0),
 ADD COLUMN usage_previous_items bigint NOT NULL DEFAULT 0 CHECK (usage_previous_items >= 0),
 ADD COLUMN diagnostics jsonb;
