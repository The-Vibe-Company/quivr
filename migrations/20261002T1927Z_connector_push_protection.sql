-- Engine-owned ingress protection; shared across API replicas.
ALTER TABLE connector_instances ADD COLUMN push_policy jsonb;
CREATE TABLE connector_push_buckets (
 organization text NOT NULL,
 connector_id text NOT NULL,
 tokens double precision NOT NULL,
 updated_at timestamptz NOT NULL,
 PRIMARY KEY (organization,connector_id),
 FOREIGN KEY (organization,connector_id) REFERENCES connector_instances(organization,id) ON DELETE CASCADE
);
CREATE TABLE connector_push_answers (
 organization text NOT NULL,
 connector_id text NOT NULL,
 key_hash text NOT NULL,
 answer jsonb,
 error_code text NOT NULL DEFAULT '',
 owner_token text NOT NULL DEFAULT '',
 expires_at timestamptz NOT NULL,
 PRIMARY KEY (organization,connector_id,key_hash),
 FOREIGN KEY (organization,connector_id) REFERENCES connector_instances(organization,id) ON DELETE CASCADE
);
CREATE INDEX connector_push_answers_expiry ON connector_push_answers(expires_at);
