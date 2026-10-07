-- Queue observations and purge candidates are maintained by application writes.
-- Initialization and repair advance through bounded, persisted keyset batches.
CREATE TABLE queue_enrichment_records (
 organization text NOT NULL,
 record_id text NOT NULL,
 version_id text NOT NULL DEFAULT '',
 desired_version_id text NOT NULL DEFAULT '',
 gone boolean NOT NULL DEFAULT false,
 pending boolean NOT NULL DEFAULT false,
 PRIMARY KEY(organization,record_id)
);
CREATE TABLE queue_enrichment_bootstrap (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 organization text NOT NULL DEFAULT '',
 record_id text NOT NULL DEFAULT '',
 initialized boolean NOT NULL DEFAULT false
);
CREATE TABLE projection_purge_candidates (
 organization text NOT NULL,
 version_id text NOT NULL,
 PRIMARY KEY(organization,version_id)
);
CREATE TABLE projection_purge_bootstrap (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 organization text NOT NULL DEFAULT '',
 segmentation_id text NOT NULL DEFAULT '',
 initialized boolean NOT NULL DEFAULT false
);
CREATE TABLE queue_observation_journals (
 organization text PRIMARY KEY,
 position bigint NOT NULL DEFAULT 0
);
