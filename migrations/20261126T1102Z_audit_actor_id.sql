-- Existing entries use their API key identifier as the effective actor ID.
-- Keep the column nullable so previous writers can still append entries.
ALTER TABLE audit_events ADD COLUMN actor_id text;
