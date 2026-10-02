-- Bearer secrets are shown only when issued. Store only their SHA-256 digest
-- and display metadata; validity is checked on every authentication.
CREATE TABLE connector_instance_tokens (
 organization text NOT NULL,
 connector_id text NOT NULL,
 id text NOT NULL,
 hash bytea NOT NULL CHECK (octet_length(hash) = 32),
 prefix text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 rotated_at timestamptz,
 revoked_at timestamptz,
 valid_until timestamptz,
 PRIMARY KEY (organization, connector_id, id),
 FOREIGN KEY (organization, connector_id) REFERENCES connector_instances(organization, id),
 CHECK ((rotated_at IS NULL) = (valid_until IS NULL))
);
