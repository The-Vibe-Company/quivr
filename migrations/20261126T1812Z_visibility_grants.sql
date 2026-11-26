CREATE TABLE visibility_grants (
    organization text NOT NULL,
    corpus_id text NOT NULL,
    source_namespace text NOT NULL,
    visibility text NOT NULL CHECK (visibility IN ('public', 'restricted')),
    allowed_principals text[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (organization, corpus_id, source_namespace)
);

ALTER TABLE records ADD COLUMN record_acl text[];

-- A monotonic hint lets empty source policies short-circuit without scanning
-- the records of Corpora that have never ingested a record ACL.
CREATE TABLE visibility_acl_corpora (
    organization text NOT NULL,
    corpus_id text NOT NULL,
    PRIMARY KEY (organization, corpus_id)
);

-- First acceptance of a namespace advances the Corpus revision before its
-- projection can publish. Searches can detect a changed source universe.
CREATE TABLE visibility_source_inventory (
    organization text NOT NULL,
    corpus_id text NOT NULL,
    source_namespace text NOT NULL,
    PRIMARY KEY (organization, corpus_id, source_namespace)
);

CREATE TABLE visibility_revisions (
    organization text NOT NULL,
    corpus_id text NOT NULL,
    revision bigint NOT NULL,
    PRIMARY KEY (organization, corpus_id)
);
