CREATE TABLE organization_journals (
 organization text PRIMARY KEY,
 last_sequence bigint NOT NULL DEFAULT 0
);
CREATE TABLE change_events (
 organization text NOT NULL,
 sequence bigint NOT NULL,
 event_id text NOT NULL UNIQUE,
 corpus_id text NOT NULL,
 event_type text NOT NULL,
 resource_type text NOT NULL,
 resource_id text NOT NULL,
 occurred_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organization,sequence)
);
CREATE TABLE records (
 organization text NOT NULL,
 id text NOT NULL,
 corpus_id text NOT NULL,
 namespace text NOT NULL,
 record_key text NOT NULL,
 acceptance_order bigint NOT NULL DEFAULT 0,
 desired_order bigint NOT NULL DEFAULT 0,
 desired_version_id text,
 desired_position text NOT NULL DEFAULT '',
 withdrawn boolean NOT NULL DEFAULT false,
 current_version_id text,
 PRIMARY KEY(organization,id),
 UNIQUE(organization,corpus_id,namespace,record_key),
 FOREIGN KEY(organization,corpus_id) REFERENCES corpora(organization,id)
);
-- An accepted source revision reserves a content identity; it is not a published Version.
CREATE TABLE accepted_revisions (
 organization text NOT NULL,
 record_id text NOT NULL,
 slot text NOT NULL,
 digest text NOT NULL,
 version_id text NOT NULL,
 acceptance_order bigint NOT NULL,
 source_position text NOT NULL,
 predecessor_id text,
 command jsonb NOT NULL,
 PRIMARY KEY(organization,record_id,slot),
 FOREIGN KEY(organization,record_id) REFERENCES records(organization,id)
);
CREATE TABLE ingestion_receipts (
 organization text NOT NULL,
 id text NOT NULL,
 request_key text NOT NULL,
 canonical_request bytea NOT NULL,
 command jsonb NOT NULL,
 corpus_id text NOT NULL,
 record_id text NOT NULL,
 acceptance_order bigint NOT NULL,
 slot text NOT NULL,
 digest text NOT NULL,
 version_id text,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','resolved')),
 outcome text CHECK(outcome IN ('created','duplicate','withdrawal_applied','conflict')),
 processing text NOT NULL DEFAULT 'queued',
 error_code text NOT NULL DEFAULT '',
 PRIMARY KEY(organization,id),
 UNIQUE(organization,request_key),
 UNIQUE(organization,record_id,acceptance_order),
 FOREIGN KEY(organization,record_id) REFERENCES records(organization,id),
 CHECK((state='pending' AND outcome IS NULL) OR (state='resolved' AND outcome IS NOT NULL))
);
CREATE TABLE ingestion_outbox (
 organization text NOT NULL,
 receipt_id text NOT NULL,
 dispatched boolean NOT NULL DEFAULT false,
 lease_until timestamptz NOT NULL DEFAULT '-infinity',
 PRIMARY KEY(organization,receipt_id),
 FOREIGN KEY(organization,receipt_id) REFERENCES ingestion_receipts(organization,id)
);
CREATE TABLE content_blobs (
 organization text NOT NULL,
 blob_id text NOT NULL,
 object_key text NOT NULL,
 sha256 text NOT NULL,
 byte_length bigint NOT NULL,
 PRIMARY KEY(organization,blob_id),
 UNIQUE(organization,sha256,byte_length)
);
CREATE TABLE record_versions (
 organization text NOT NULL,
 id text NOT NULL,
 record_id text NOT NULL,
 slot text NOT NULL,
 digest text NOT NULL,
 acceptance_order bigint NOT NULL,
 source_position text NOT NULL,
 predecessor_id text,
 text_blob_id text NOT NULL,
 manifest_blob_id text NOT NULL,
 provenance jsonb NOT NULL,
 PRIMARY KEY(organization,id),
 UNIQUE(organization,record_id,slot),
 FOREIGN KEY(organization,record_id) REFERENCES records(organization,id),
 FOREIGN KEY(organization,text_blob_id) REFERENCES content_blobs(organization,blob_id),
 FOREIGN KEY(organization,manifest_blob_id) REFERENCES content_blobs(organization,blob_id)
);
CREATE TABLE version_parts (
 organization text NOT NULL,
 version_id text NOT NULL,
 part_key text NOT NULL,
 role text NOT NULL,
 blob_id text NOT NULL,
 PRIMARY KEY(organization,version_id,part_key),
 FOREIGN KEY(organization,version_id) REFERENCES record_versions(organization,id),
 FOREIGN KEY(organization,blob_id) REFERENCES content_blobs(organization,blob_id)
);
