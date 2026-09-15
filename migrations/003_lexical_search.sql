ALTER TABLE record_versions ADD COLUMN baseline_ready boolean NOT NULL DEFAULT false;
ALTER TABLE record_versions ADD COLUMN quarantined boolean NOT NULL DEFAULT false;
ALTER TABLE record_versions ADD COLUMN processing text NOT NULL DEFAULT 'queued';
ALTER TABLE record_versions ADD COLUMN error_code text NOT NULL DEFAULT '';
CREATE TABLE tombstones (
 organization text NOT NULL, record_id text NOT NULL,
 PRIMARY KEY(organization,record_id),
 FOREIGN KEY(organization,record_id) REFERENCES records(organization,id)
);
CREATE TABLE projection_generations (
 id text PRIMARY KEY, collection text NOT NULL UNIQUE,
 profile_version text NOT NULL, active boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX one_active_projection ON projection_generations(active) WHERE active;
CREATE TABLE segmentations (
 organization text NOT NULL, id text NOT NULL, version_id text NOT NULL,
 recipe text NOT NULL, digest text NOT NULL,
 PRIMARY KEY(organization,id), UNIQUE(organization,version_id,recipe),
 FOREIGN KEY(organization,version_id) REFERENCES record_versions(organization,id)
);
CREATE TABLE segments (
 organization text NOT NULL, id text NOT NULL, segmentation_id text NOT NULL,
 version_id text NOT NULL, part_key text NOT NULL,
 start_offset integer NOT NULL CHECK(start_offset>=0),
 end_offset integer NOT NULL CHECK(end_offset>=start_offset), text_sha256 text NOT NULL,
 PRIMARY KEY(organization,id),
 FOREIGN KEY(organization,segmentation_id) REFERENCES segmentations(organization,id),
 FOREIGN KEY(organization,version_id,part_key) REFERENCES version_parts(organization,version_id,part_key)
);
CREATE TABLE projection_coverage (
 organization text NOT NULL, version_id text NOT NULL,
 generation_id text NOT NULL REFERENCES projection_generations(id),
 segmentation_id text NOT NULL,
 PRIMARY KEY(organization,version_id,generation_id),
 FOREIGN KEY(organization,version_id) REFERENCES record_versions(organization,id),
 FOREIGN KEY(organization,segmentation_id) REFERENCES segmentations(organization,id)
);
-- New workflow identity resumes previously materialized receipts for baseline indexing.
-- Restart API/workers after this evaluation migration.
UPDATE ingestion_outbox SET dispatched=false,lease_until='-infinity';
