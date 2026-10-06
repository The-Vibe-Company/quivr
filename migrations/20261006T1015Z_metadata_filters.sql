ALTER TABLE projection_generations ADD COLUMN metadata_projected boolean NOT NULL DEFAULT false;

-- New generations opt in; existing immutable anchors require a rebuild.
CREATE TABLE projection_metadata (
    organization text NOT NULL,
    version_id text NOT NULL,
    generation_id text NOT NULL,
    data jsonb NOT NULL,
    PRIMARY KEY (organization, version_id, generation_id)
);
CREATE INDEX projection_metadata_values ON projection_metadata USING gin(data jsonb_path_ops);
