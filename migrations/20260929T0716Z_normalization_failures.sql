-- Failure handling of external normalization (Plugin Platform v0).
--
-- A normalizations row is now the durable outcome of one Record Version's
-- normalization, written once:
--   normalized  the plugin's validated Manifest is published;
--   fallback    an optional route failed and the built-in text path's Manifest
--               is published; failure_* names the failed invocation;
--   failed      nothing from the plugin is published; the Version is published
--               quarantined with its submitted input Manifest and failure_*.
-- The row keeps the input checksum, the invocation and the reason, which is
-- what a later reprocessing (Spec 5) needs. A divergent output for the same
-- idempotency key is recorded once in conflict_* and never overwrites it.
ALTER TABLE normalizations
 ADD COLUMN outcome text NOT NULL DEFAULT 'normalized' CHECK(outcome IN ('normalized','fallback','failed')),
 ADD COLUMN failure_code text NOT NULL DEFAULT '',
 ADD COLUMN failure_message text NOT NULL DEFAULT '',
 ADD COLUMN failure_retryable boolean NOT NULL DEFAULT false,
 ADD COLUMN input_blob_id text NOT NULL DEFAULT '',
 ADD COLUMN conflict_invocation_id text,
 ADD COLUMN conflict_manifest_sha256 text,
 ALTER COLUMN manifest_key DROP NOT NULL,
 ALTER COLUMN manifest_sha256 DROP NOT NULL,
 ALTER COLUMN manifest_size DROP NOT NULL,
 ADD CONSTRAINT normalizations_outcome_manifest CHECK((outcome='failed')=(manifest_key IS NULL)),
 ADD CONSTRAINT normalizations_outcome_failure CHECK((outcome='normalized')=(failure_code=''));

-- Invocations that count against a Version's retry budget: plugin-declared
-- retryable errors and timeouts. Unavailability never counts.
CREATE TABLE normalization_attempts (
 organization text NOT NULL,
 version_id text NOT NULL,
 attempts integer NOT NULL CHECK(attempts>0),
 last_code text NOT NULL,
 last_invocation_id text NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organization,version_id)
);

-- The structured reason a Version was published quarantined, as exposed on
-- the Version read: {code, message, retryable, plugin, contribution,
-- invocation_id}. Older quarantines keep only error_code.
ALTER TABLE record_versions ADD COLUMN quarantine jsonb;
