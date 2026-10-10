-- Expand only: historical records are visited by the resumable initializer,
-- outside migration locks and outside request transactions.
ALTER TABLE accepted_revisions ADD COLUMN connector_instance_id text NOT NULL DEFAULT 'unknown';

CREATE TABLE corpus_record_observations (
 organization text NOT NULL,
 record_id text NOT NULL,
 corpus_id text NOT NULL DEFAULT '',
 namespace text NOT NULL DEFAULT '',
 connector_id text NOT NULL DEFAULT 'unknown',
 shard smallint NOT NULL DEFAULT 0 CHECK (shard BETWEEN 0 AND 15),
 hour timestamptz,
 catalog boolean NOT NULL DEFAULT false,
 eligible boolean NOT NULL DEFAULT false,
 previous jsonb,
 PRIMARY KEY (organization,record_id)
);
CREATE TABLE corpus_record_totals (
 organization text NOT NULL,
 corpus_id text NOT NULL,
 shard smallint NOT NULL,
 eligible bigint NOT NULL DEFAULT 0,
 catalog bigint NOT NULL DEFAULT 0,
 undated bigint NOT NULL DEFAULT 0,
 catalog_undated bigint NOT NULL DEFAULT 0,
 PRIMARY KEY (organization,corpus_id,shard)
);
CREATE TABLE corpus_record_hours (
 organization text NOT NULL,
 corpus_id text NOT NULL,
 hour timestamptz NOT NULL,
 shard smallint NOT NULL,
 eligible bigint NOT NULL DEFAULT 0,
 catalog bigint NOT NULL DEFAULT 0,
 PRIMARY KEY (organization,corpus_id,hour,shard)
);
CREATE TABLE corpus_record_sources (
 organization text NOT NULL,
 corpus_id text NOT NULL,
 namespace text NOT NULL COLLATE "C",
 connector_id text NOT NULL COLLATE "C",
 shard smallint NOT NULL,
 eligible bigint NOT NULL DEFAULT 0,
 catalog bigint NOT NULL DEFAULT 0,
 PRIMARY KEY (organization,corpus_id,namespace,connector_id,shard)
);
CREATE TABLE corpus_record_bootstrap (
 organization text NOT NULL,
 corpus_id text NOT NULL,
 after_id text NOT NULL COLLATE "C" DEFAULT '',
 complete boolean NOT NULL DEFAULT false,
 PRIMARY KEY (organization,corpus_id)
);
