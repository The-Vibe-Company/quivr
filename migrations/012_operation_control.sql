-- Operation control (THE-659). A rerun is a new Operation linked to its
-- terminal source. Its request identity is Organization + source + key, kept
-- apart from the originating command's route identity so keys never collide.
ALTER TABLE operations DROP CONSTRAINT operations_organization_kind_corpus_id_request_key_key;
CREATE UNIQUE INDEX operations_request_identity ON operations(organization,kind,corpus_id,request_key) WHERE previous_operation_id IS NULL;
CREATE UNIQUE INDEX operations_rerun_identity ON operations(organization,previous_operation_id,request_key) WHERE previous_operation_id IS NOT NULL;
ALTER TABLE operations ADD CONSTRAINT operations_previous_operation FOREIGN KEY(organization,previous_operation_id) REFERENCES operations(organization,id);
