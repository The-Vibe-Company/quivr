-- Only SHA-256 fingerprints and random reservation tokens are persisted.
-- Expired entries are removed lazily on the next instance reservation.
CREATE TABLE connector_signature_replays (
 organization text NOT NULL,
 connector_id text NOT NULL,
 fingerprint text NOT NULL,
 reservation text NOT NULL,
 expires_at timestamptz NOT NULL,
 PRIMARY KEY (organization,connector_id,fingerprint),
 FOREIGN KEY (organization,connector_id) REFERENCES connector_instances(organization,id) ON DELETE CASCADE
);
CREATE INDEX connector_signature_replays_reservation ON connector_signature_replays(organization,connector_id,reservation);
