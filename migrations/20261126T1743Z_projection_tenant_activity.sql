CREATE TABLE projection_tenant_activity (
    organization text NOT NULL,
    corpus_id text NOT NULL,
    due_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization, corpus_id)
);
CREATE INDEX projection_tenant_activity_due ON projection_tenant_activity (due_at, organization, corpus_id);
