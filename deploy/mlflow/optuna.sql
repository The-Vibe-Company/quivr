-- Run as the database owner. Optuna owns tables only in this evaluation schema.
-- Grant the NOLOGIN role to a dedicated login via the operator secret manager.
BEGIN;
DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'quivr_eval_optuna') THEN
    CREATE ROLE quivr_eval_optuna NOLOGIN;
  END IF;
END $$;
CREATE SCHEMA IF NOT EXISTS eval_optuna;
GRANT USAGE, CREATE ON SCHEMA eval_optuna TO quivr_eval_optuna;
COMMIT;
