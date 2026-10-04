-- Enrichment calls of a Record Version that ended at the ingestion plugin's
-- deadline (THE-810). After a bounded number the Version's enrichment stops
-- as blocked with enrichment_timeout; it stays searchable by keyword. An
-- unavailable plugin never counts.
CREATE TABLE enrichment_timeouts (
 organization text NOT NULL,
 version_id text NOT NULL,
 timeouts integer NOT NULL CHECK(timeouts>0),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organization,version_id)
);
