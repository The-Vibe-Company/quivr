-- Per-route-family idempotency for monitoring commands.
CREATE TABLE monitoring_requests (
 organization text NOT NULL,
 route_family text NOT NULL,
 request_key text NOT NULL,
 canonical_request bytea NOT NULL,
 resource_id text NOT NULL,
 PRIMARY KEY(organization,route_family,request_key)
);
CREATE TABLE saved_queries (
 organization text NOT NULL,
 id text NOT NULL,
 name text NOT NULL,
 current_version_id text NOT NULL,
 PRIMARY KEY(organization,id)
);
-- Immutable: rows are only inserted.
CREATE TABLE saved_query_versions (
 organization text NOT NULL,
 saved_query_id text NOT NULL,
 id text NOT NULL,
 definition jsonb NOT NULL,
 corpus_ids text[] NOT NULL,
 PRIMARY KEY(organization,id),
 FOREIGN KEY(organization,saved_query_id) REFERENCES saved_queries(organization,id)
);
-- Enabled state is the only mutable Subscription fact; configuration lives in its Versions.
CREATE TABLE subscriptions (
 organization text NOT NULL,
 id text NOT NULL,
 name text NOT NULL,
 current_version_id text NOT NULL,
 enabled boolean NOT NULL DEFAULT true,
 disabled_position bigint,
 PRIMARY KEY(organization,id)
);
-- Immutable: rows are only inserted. activation_position is the Organization
-- journal position of the activation commit; evaluation considers only later positions.
CREATE TABLE subscription_versions (
 organization text NOT NULL,
 subscription_id text NOT NULL,
 id text NOT NULL,
 saved_query_id text NOT NULL,
 saved_query_version_id text NOT NULL,
 evaluator jsonb NOT NULL,
 destination_id text NOT NULL,
 activation_position bigint NOT NULL,
 PRIMARY KEY(organization,id),
 FOREIGN KEY(organization,subscription_id) REFERENCES subscriptions(organization,id),
 FOREIGN KEY(organization,saved_query_version_id) REFERENCES saved_query_versions(organization,id)
);
-- Scoped Subscription lookup for evaluation of a Corpus change.
CREATE TABLE subscription_corpora (
 organization text NOT NULL,
 corpus_id text NOT NULL,
 subscription_id text NOT NULL,
 PRIMARY KEY(organization,corpus_id,subscription_id),
 FOREIGN KEY(organization,subscription_id) REFERENCES subscriptions(organization,id),
 FOREIGN KEY(organization,corpus_id) REFERENCES corpora(organization,id)
);
