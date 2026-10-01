-- evaluation_retirements
CREATE TABLE evaluation_retirements (
    organization text NOT NULL,
    id text NOT NULL,
    request_key text NOT NULL,
    canonical_request bytea NOT NULL,
    result jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization, id),
    UNIQUE (organization, request_key)
);
