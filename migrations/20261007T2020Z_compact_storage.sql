-- Expand only. Compact writes are activated separately after older writers drain.
CREATE TABLE storage_state (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 compact boolean NOT NULL DEFAULT false,
 activated_at timestamptz
);
ALTER TABLE ingestion_receipts ADD COLUMN request_digest bytea;
ALTER TABLE normalizations ADD COLUMN outcome_key text;
ALTER TABLE normalizations ADD COLUMN outcome_sha256 text;
ALTER TABLE normalizations ADD COLUMN outcome_size bigint;

CREATE TABLE storage_organizations (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 organization text NOT NULL UNIQUE
);
CREATE TABLE storage_spaces (
 id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 space_id text NOT NULL UNIQUE
);
CREATE TABLE storage_segments (
 organization_id bigint NOT NULL,
 segment_id text NOT NULL,
 id bigint GENERATED ALWAYS AS IDENTITY,
 PRIMARY KEY(organization_id,segment_id),
 UNIQUE(organization_id,id)
);
CREATE TABLE embedding_files (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 organization_id bigint NOT NULL,
 version_id text NOT NULL,
 segmentation_id text NOT NULL,
 space_id integer NOT NULL,
 corpus_id text NOT NULL,
 recipe text NOT NULL,
 producer text NOT NULL,
 object_key text NOT NULL,
 sha256 bytea NOT NULL,
 byte_length bigint NOT NULL,
 dimensions integer NOT NULL,
 row_count integer NOT NULL,
 presence bytea NOT NULL,
 UNIQUE(organization_id,segmentation_id,space_id)
);
CREATE TABLE compact_embeddings (
 organization_id bigint NOT NULL,
 segment_id bigint NOT NULL,
 space_id integer NOT NULL,
 file_id bigint NOT NULL,
 ordinal integer NOT NULL,
 vector_sha256 bytea NOT NULL,
 artifact_sha256 bytea NOT NULL,
 PRIMARY KEY(organization_id,segment_id,space_id)
);
CREATE INDEX compact_embeddings_file ON compact_embeddings(file_id,ordinal);
CREATE TABLE compact_embedding_coverage (
 organization_id bigint NOT NULL,
 file_id bigint NOT NULL,
 generation_id text NOT NULL,
 covered bytea NOT NULL,
 PRIMARY KEY(organization_id,file_id,generation_id)
);
CREATE TABLE storage_compactions (
 id text PRIMARY KEY,
 phase text NOT NULL DEFAULT 'vectors',
 checkpoint text NOT NULL DEFAULT '',
 retire_audit_detail boolean NOT NULL DEFAULT false,
 groups_done bigint NOT NULL DEFAULT 0,
 receipts_done bigint NOT NULL DEFAULT 0,
 normalizations_done bigint NOT NULL DEFAULT 0
);
