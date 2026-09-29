-- monitoring_edit_delete (THE-724)
-- Logical deletion. A deleted Saved Query or Subscription keeps its Versions,
-- Matches and Deliveries readable; it is never undeleted. A deleted
-- Subscription is also disabled, so every disable guarantee applies to it.
ALTER TABLE saved_queries ADD COLUMN deleted boolean NOT NULL DEFAULT false;
ALTER TABLE subscriptions ADD COLUMN deleted boolean NOT NULL DEFAULT false;
-- Journal position of the Subscription's deletion; NULL while it exists.
ALTER TABLE subscriptions ADD COLUMN deleted_position bigint;
-- Every later Subscription Version records its own activation_position, the
-- journal position of its commit: a trigger is judged by the Version whose
-- activation_position is the latest one below the trigger's position.
CREATE INDEX subscription_versions_effective ON subscription_versions(organization,subscription_id,activation_position);
