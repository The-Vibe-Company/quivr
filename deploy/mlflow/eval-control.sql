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
-- Supervisor state is separate from canonical measurement leases/results.
CREATE TABLE IF NOT EXISTS eval_control.campaign_runs (
  campaign text PRIMARY KEY REFERENCES eval_control.campaigns(name),
  spec jsonb NOT NULL,
  git_sha text NOT NULL,
  scorer_digest text NOT NULL,
  owner text,
  expires_at timestamptz,
  generation integer NOT NULL DEFAULT 0,
  state jsonb NOT NULL DEFAULT '{"resources":{},"trials":{}}'
);
-- Cross-tier invariants are separate from the immutable exploration policy.
-- Different finalists share these invariants, budgets and the campaign counter.
CREATE TABLE IF NOT EXISTS eval_control.confirmation_policies (
  campaign text PRIMARY KEY REFERENCES eval_control.campaigns(name),
  policy jsonb NOT NULL
);
-- One protected input opening per fenced owner, even on duplicate delivery.
-- Failed/uncertain openings remain counted when a new owner retries.
CREATE TABLE IF NOT EXISTS eval_control.confirmation_reads (
  campaign text NOT NULL REFERENCES eval_control.campaigns(name),
  key text NOT NULL,
  owner text NOT NULL,
  read_ordinal integer NOT NULL CHECK (read_ordinal BETWEEN 1 AND 10),
  PRIMARY KEY (campaign, key, owner),
  UNIQUE (campaign, read_ordinal)
);
-- Standalone operator command persists an app intent before remote creation.
-- Campaign supervisors use their existing resource registry instead.
CREATE TABLE IF NOT EXISTS eval_control.confirmation_apps (
  campaign text NOT NULL REFERENCES eval_control.campaigns(name),
  intent text NOT NULL,
  owner text NOT NULL,
  app_id text,
  PRIMARY KEY (campaign, intent, owner)
);
GRANT USAGE ON SCHEMA eval_control TO quivr_eval_control;
GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA eval_control TO quivr_eval_control;
COMMIT;
