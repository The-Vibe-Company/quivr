-- Evaluation-only schema. Run as the database owner; safe to apply repeatedly.
-- Grant this NOLOGIN role to a dedicated login via the operator's secret manager.
-- Never grant it access to MLflow tracking/auth tables or engine databases.
BEGIN;
DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'quivr_eval_control') THEN
    CREATE ROLE quivr_eval_control NOLOGIN;
  END IF;
END $$;
CREATE SCHEMA IF NOT EXISTS eval_control;
CREATE TABLE IF NOT EXISTS eval_control.campaigns (
  name text PRIMARY KEY,
  policy jsonb NOT NULL,
  stopped text,
  confirmation_reads integer NOT NULL DEFAULT 0 CHECK (confirmation_reads BETWEEN 0 AND 10)
);
CREATE TABLE IF NOT EXISTS eval_control.days (
  campaign text REFERENCES eval_control.campaigns(name),
  day date NOT NULL,
  kind text NOT NULL CHECK (kind IN ('provider', 'modal')),
  stopped boolean NOT NULL DEFAULT false,
  PRIMARY KEY (campaign, day, kind)
);
CREATE TABLE IF NOT EXISTS eval_control.reservations (
  id text PRIMARY KEY,
  campaign text NOT NULL REFERENCES eval_control.campaigns(name),
  day date NOT NULL,
  kind text NOT NULL CHECK (kind IN ('provider', 'modal')),
  reserved_usd numeric NOT NULL CHECK (reserved_usd >= 0),
  charged_usd numeric NOT NULL CHECK (charged_usd >= 0),
  settled boolean NOT NULL DEFAULT false,
  metadata jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS reservation_day ON eval_control.reservations(campaign, day, kind);
CREATE TABLE IF NOT EXISTS eval_control.leases (
  campaign text REFERENCES eval_control.campaigns(name),
  key text NOT NULL,
  owner text NOT NULL,
  expires_at timestamptz NOT NULL,
  payload jsonb,
  PRIMARY KEY (campaign, key)
);
CREATE TABLE IF NOT EXISTS eval_control.attempts (
  campaign text REFERENCES eval_control.campaigns(name),
  key text NOT NULL,
  owner text NOT NULL,
  status text NOT NULL CHECK (status IN ('capped', 'failed')),
  recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
GRANT USAGE ON SCHEMA eval_control TO quivr_eval_control;
GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA eval_control TO quivr_eval_control;
COMMIT;
