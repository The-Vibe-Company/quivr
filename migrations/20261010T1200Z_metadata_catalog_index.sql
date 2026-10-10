-- Performance indexes, synchronization and historical population are resumable
-- maintenance after startup. This expansion only installs the empty relation.
ALTER TABLE projection_metadata ADD COLUMN filter_indexed boolean NOT NULL DEFAULT false;

CREATE TABLE projection_metadata_filter_values (
    organization text NOT NULL,
    corpus_id text NOT NULL,
    generation_id text NOT NULL,
    version_id text NOT NULL,
    field text NOT NULL,
    value jsonb NOT NULL,
    date_epoch_ms bigint
);
