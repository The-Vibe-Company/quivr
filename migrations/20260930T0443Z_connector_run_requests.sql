-- A client may ask for a Connector Instance run now (THE-766). The request
-- pulls next_run_at in, but never before last_run_at plus the deployment's
-- interval floor, nor before retry_until, the end of the Retry-After the
-- source asked for at the last run. Both are written when a run finishes.
ALTER TABLE connector_instances
 ADD COLUMN last_run_at timestamptz,
 ADD COLUMN retry_until timestamptz;
