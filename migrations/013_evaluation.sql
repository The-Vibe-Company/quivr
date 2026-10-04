-- Internal: the Record Version a trigger event concerns. Never exposed by the
-- public feed. Trigger events committed before this migration have no Version
-- and are skipped by evaluation dispatch.
ALTER TABLE change_events ADD COLUMN record_version_id text;
-- Per-Organization evaluation dispatch checkpoint. position is the last journal
-- position fully dispatched; subscription_after pages the Subscriptions of the
-- next trigger event.
CREATE TABLE monitoring_checkpoints (
 organization text PRIMARY KEY,
 position bigint NOT NULL,
 subscription_after text NOT NULL DEFAULT ''
);
-- Durable evaluation work: one per Subscription Version and trigger event.
CREATE TABLE evaluation_intents (
 organization text NOT NULL,
 subscription_version_id text NOT NULL,
 sequence bigint NOT NULL,
 subscription_id text NOT NULL,
 corpus_id text NOT NULL,
 record_id text NOT NULL,
 record_version_id text NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','done')),
 outcome text NOT NULL DEFAULT '',
 attempts integer NOT NULL DEFAULT 0,
 error_code text NOT NULL DEFAULT '',
 available_at timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz NOT NULL DEFAULT '-infinity',
 PRIMARY KEY(organization,subscription_version_id,sequence),
 FOREIGN KEY(organization,subscription_version_id) REFERENCES subscription_versions(organization,id)
);
CREATE INDEX evaluation_intents_due ON evaluation_intents(available_at) WHERE state='pending';
-- Evaluation reads a Version's enrichment coverage through its segments.
CREATE INDEX segments_by_version ON segments(organization,version_id);
-- Immutable positive facts: unique per Subscription Version and Record Version.
CREATE TABLE matches (
 organization text NOT NULL,
 id text NOT NULL,
 subscription_id text NOT NULL,
 subscription_version_id text NOT NULL,
 saved_query_id text NOT NULL,
 saved_query_version_id text NOT NULL,
 corpus_id text NOT NULL,
 record_id text NOT NULL,
 record_version_id text NOT NULL,
 previous_match_id text,
 evidence jsonb NOT NULL,
 position bigint NOT NULL,
 PRIMARY KEY(organization,id),
 UNIQUE(organization,subscription_version_id,record_version_id),
 FOREIGN KEY(organization,subscription_version_id) REFERENCES subscription_versions(organization,id),
 FOREIGN KEY(organization,record_version_id) REFERENCES record_versions(organization,id)
);
CREATE INDEX matches_by_subscription ON matches(organization,subscription_id,position);
-- Logical notification of one notice to one destination.
CREATE TABLE deliveries (
 organization text NOT NULL,
 id text NOT NULL,
 match_id text NOT NULL,
 destination_id text NOT NULL,
 event_kind text NOT NULL,
 event_id text NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','delivering','delivered','exhausted')),
 attempt_count integer NOT NULL DEFAULT 0,
 PRIMARY KEY(organization,id),
 UNIQUE(organization,match_id,destination_id,event_kind),
 FOREIGN KEY(organization,match_id) REFERENCES matches(organization,id)
);
-- Immutable reference-only notices. body is the exact webhook body; the feed
-- event with the same event_id carries these references.
CREATE TABLE monitoring_notices (
 organization text NOT NULL,
 event_id text NOT NULL,
 kind text NOT NULL,
 match_id text NOT NULL,
 record_id text NOT NULL,
 record_version_id text NOT NULL,
 subscription_id text NOT NULL,
 subscription_version_id text NOT NULL,
 delivery_id text NOT NULL,
 previous_match_id text,
 body bytea NOT NULL,
 PRIMARY KEY(organization,event_id),
 FOREIGN KEY(organization,delivery_id) REFERENCES deliveries(organization,id)
);
-- Durable delivery work for the network delivery worker.
CREATE TABLE delivery_outbox (
 organization text NOT NULL,
 delivery_id text NOT NULL,
 available_at timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz NOT NULL DEFAULT '-infinity',
 PRIMARY KEY(organization,delivery_id),
 FOREIGN KEY(organization,delivery_id) REFERENCES deliveries(organization,id)
);
