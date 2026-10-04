-- Register, check and activate plugins without restarting (THE-781, Spec 5
-- slice 2). A registration keeps the exact manifest and the settings it is
-- installed with, so api and worker resolve plugins from the active plan
-- alone; its identity now includes those settings. The Contract Runner's
-- report is stored on it, and a lease lets a check a restart interrupted run
-- again.
ALTER TABLE plugin_registrations
 ADD COLUMN manifest bytea,
 ADD COLUMN settings jsonb,
 ADD COLUMN check_report jsonb,
 ADD COLUMN check_lease_until timestamptz NOT NULL DEFAULT '-infinity';
DO $$
DECLARE c text;
BEGIN
 FOR c IN SELECT conname FROM pg_constraint WHERE conrelid='plugin_registrations'::regclass AND contype='u' LOOP
  EXECUTE format('ALTER TABLE plugin_registrations DROP CONSTRAINT %I', c);
 END LOOP;
END $$;
CREATE INDEX plugin_registrations_awaiting_check ON plugin_registrations(created_at) WHERE state='registered';

-- Idempotency keys of registration requests.
CREATE TABLE plugin_registration_requests (
 request_key text PRIMARY KEY,
 registration_id text NOT NULL REFERENCES plugin_registrations(id),
 created_at timestamptz NOT NULL DEFAULT now()
);

-- What recorded each plan: the startup configuration or an operator activation.
ALTER TABLE pipeline_plans ADD COLUMN source text NOT NULL DEFAULT 'configuration' CHECK (source IN ('configuration','activation'));

-- The roles the startup configuration last applied (role -> registration id),
-- so a later start applies only the roles the configuration changed since.
CREATE TABLE pipeline_configuration (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 roles jsonb NOT NULL,
 applied_at timestamptz NOT NULL DEFAULT now()
);
