-- Durable output of one external normalizer invocation per accepted Record
-- Version (Plugin Platform v0). Written once, before publication; publication,
-- projection rebuilds and retrieval generations read the stored Manifest
-- object and never invoke the plugin again. The Version identity is still the
-- digest of the submitted input, reserved at acceptance.
CREATE TABLE normalizations (
 organization text NOT NULL,
 version_id text NOT NULL,
 idempotency_key text NOT NULL,
 invocation_id text NOT NULL,
 plugin_id text NOT NULL,
 plugin_version text NOT NULL,
 plugin_api text NOT NULL,
 contribution text NOT NULL,
 input_sha256 text NOT NULL,
 manifest_key text NOT NULL,
 manifest_sha256 text NOT NULL,
 manifest_size bigint NOT NULL CHECK(manifest_size>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organization,version_id)
);
