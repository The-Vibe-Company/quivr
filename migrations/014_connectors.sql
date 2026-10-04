-- A Connector Instance is bound to one Corpus and one Source Namespace. The row
-- is also its schedule: the worker claims enabled instances whose next_run_at is
-- due under a lease, and run_sequence identifies the one in-flight acquisition.
CREATE TABLE connector_instances (
 organization text NOT NULL,
 id text NOT NULL,
 corpus_id text NOT NULL,
 source_namespace text NOT NULL,
 kind text NOT NULL,
 config jsonb NOT NULL,
 interval_seconds integer NOT NULL CHECK (interval_seconds > 0),
 silent_after_seconds integer NOT NULL CHECK (silent_after_seconds > 0),
 credential_warning_seconds integer NOT NULL CHECK (credential_warning_seconds >= 0),
 enabled boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(),
 disabled_at timestamptz,
 request_key text NOT NULL,
 -- HMAC of the canonical create request: replay detection without storing a secret.
 request_digest bytea NOT NULL,
 next_run_at timestamptz NOT NULL DEFAULT now(),
 run_sequence bigint NOT NULL DEFAULT 0,
 lease_until timestamptz,
 -- Acquisition Checkpoint, advanced only after a page's items were durably accepted.
 checkpoint jsonb,
 last_success_at timestamptz,
 last_item_at timestamptz,
 last_error_code text,
 last_error_class text CHECK (last_error_class IN ('access','transient','source')),
 last_error_at timestamptz,
 -- Set by a run refused access; cleared only by a later successful run.
 access_error_at timestamptz,
 health_state text NOT NULL CHECK (health_state IN ('active','silent','access_error','credential_expiring','disabled')),
 health_revision bigint NOT NULL DEFAULT 0,
 health_evaluated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organization,id),
 UNIQUE(organization,request_key),
 FOREIGN KEY(organization,corpus_id) REFERENCES corpora(organization,id)
);
-- A Source Namespace has at most one enabled acquirer; disabling releases it.
CREATE UNIQUE INDEX connector_instances_enabled_namespace ON connector_instances(organization,corpus_id,source_namespace) WHERE enabled;
CREATE INDEX connector_instances_due ON connector_instances(next_run_at) WHERE enabled;

-- Deposited Credentials are insert-only versions; the highest version is current.
-- Secrets are AES-256-GCM ciphertext under the deployment credential key (key_id).
CREATE TABLE connector_credentials (
 organization text NOT NULL,
 connector_id text NOT NULL,
 version integer NOT NULL CHECK (version > 0),
 key_id text NOT NULL,
 nonce bytea NOT NULL,
 ciphertext bytea NOT NULL,
 expires_at timestamptz,
 deposited_at timestamptz NOT NULL DEFAULT now(),
 request_key text,
 request_digest bytea,
 PRIMARY KEY(organization,connector_id,version),
 UNIQUE(organization,connector_id,request_key),
 FOREIGN KEY(organization,connector_id) REFERENCES connector_instances(organization,id)
);
