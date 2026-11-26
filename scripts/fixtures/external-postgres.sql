-- Run as the DBA in the disposable database. Quivr bootstraps as quivr_engine.
CREATE ROLE quivr_engine LOGIN PASSWORD 'external-adapter-test' NOSUPERUSER NOCREATEDB NOCREATEROLE;
GRANT CONNECT ON DATABASE quivr TO quivr_engine;
GRANT USAGE, CREATE ON SCHEMA public TO quivr_engine;
GRANT USAGE ON LANGUAGE plpgsql TO quivr_engine;
ALTER ROLE quivr_engine IN DATABASE quivr SET search_path = public;

-- A login that can reach the database but has no Quivr object privileges.
CREATE ROLE quivr_unprovisioned LOGIN PASSWORD 'external-adapter-test' NOSUPERUSER NOCREATEDB NOCREATEROLE;
