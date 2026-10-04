CREATE TABLE uploads (
 organization text NOT NULL,
 id text NOT NULL,
 request_key text NOT NULL,
 canonical_request bytea NOT NULL,
 sha256 text NOT NULL,
 byte_length bigint NOT NULL,
 media_type text NOT NULL,
 state text NOT NULL DEFAULT 'awaiting_upload' CHECK(state IN ('awaiting_upload','verifying','verified','rejected','expired')),
 object_key text NOT NULL,
 blob_id text,
 error_code text NOT NULL DEFAULT '',
 expires_at timestamptz NOT NULL,
 PRIMARY KEY(organization,id),
 UNIQUE(organization,request_key)
);
-- A verified Blob is an immutable, Organization-scoped identity. Its digest and
-- byte length are stable; the object key is a temporary transfer capability.
CREATE TABLE verified_blobs (
 organization text NOT NULL,
 blob_id text NOT NULL,
 object_key text NOT NULL,
 sha256 text NOT NULL,
 byte_length bigint NOT NULL,
 media_type text NOT NULL,
  PRIMARY KEY(organization,blob_id),
  UNIQUE(organization,sha256,byte_length,media_type)
);
