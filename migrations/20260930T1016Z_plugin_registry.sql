-- The plugin registry (THE-780, Spec 5 slice 1): every plugin version the
-- operator runs at an address and Quivr records, and the immutable Pipeline
-- Plans mapping each role of the deployment to one registration. Startup seeds
-- them from the QUIVR_CONFIG pins when the registry is empty; the engine still
-- resolves plugins from the configuration in this slice.
CREATE TABLE plugin_registrations (
 id text PRIMARY KEY,
 plugin_id text NOT NULL,
 version text NOT NULL,
 endpoint text NOT NULL,
 manifest_digest text NOT NULL,
 -- The artifact (for example OCI image) digest the plugin reports, recorded
 -- as information; Quivr never deploys it.
 artifact_digest text,
 contributions text[] NOT NULL,
 -- Roles the manifest declares it can serve; the plan says which it serves.
 roles text[] NOT NULL,
 state text NOT NULL CHECK (state IN ('registered','validated','active','draining','inactive','rejected')),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (plugin_id, version, manifest_digest, endpoint)
);

CREATE TABLE pipeline_plans (
 id text PRIMARY KEY,
 created_at timestamptz NOT NULL DEFAULT now()
);

-- A plan's roles never change once written.
CREATE TABLE pipeline_plan_roles (
 plan_id text NOT NULL REFERENCES pipeline_plans(id),
 role text NOT NULL,
 registration_id text NOT NULL REFERENCES plugin_registrations(id),
 PRIMARY KEY (plan_id, role)
);

-- One active plan per deployment.
CREATE TABLE active_pipeline_plan (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 plan_id text NOT NULL REFERENCES pipeline_plans(id),
 activated_at timestamptz NOT NULL DEFAULT now()
);
