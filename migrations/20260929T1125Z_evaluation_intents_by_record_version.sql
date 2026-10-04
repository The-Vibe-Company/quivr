-- evaluation_intents_by_record_version (THE-722)
-- The engine batches the pending evaluation intents of one Record Version
-- pinned to one evaluator into one plugin call: it claims them together by
-- Record Version.
CREATE INDEX evaluation_intents_pending_by_version ON evaluation_intents(organization,record_version_id) WHERE state='pending';
