-- Preserve the requested restart stage across retries and operation reruns.
-- The request contract validates the value; nullable keeps older writers compatible.
ALTER TABLE quarantine_reprocesses ADD COLUMN from_stage text;
