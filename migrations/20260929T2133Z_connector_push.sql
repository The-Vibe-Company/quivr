-- Push health of Connector Instances whose kind declares the push mode
-- (THE-718). push_state is the kind's latest report on its push channel, from
-- a pull run: active, pending or failed (with push_setup_class and code).
-- push_error_* is the core's latest delivery failure, or missed deliveries,
-- cleared by the next accepted delivery. While push is active without an
-- error, pull runs at most every push_poll_interval_seconds.
ALTER TABLE connector_instances
 ADD COLUMN push_state text CHECK (push_state IN ('active','pending','failed')),
 ADD COLUMN push_setup_class text CHECK (push_setup_class IN ('access','transient','source')),
 ADD COLUMN push_setup_code text,
 ADD COLUMN push_setup_at timestamptz,
 ADD COLUMN push_poll_interval_seconds integer CHECK (push_poll_interval_seconds > 0),
 ADD COLUMN push_error_class text CHECK (push_error_class IN ('access','transient','source')),
 ADD COLUMN push_error_code text,
 ADD COLUMN push_error_at timestamptz,
 ADD COLUMN push_last_delivery_at timestamptz;
-- The public webhook route names an instance by id alone.
CREATE INDEX connector_instances_id ON connector_instances(id);
