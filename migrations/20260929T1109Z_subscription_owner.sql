-- subscription_owner (THE-727)
-- The Subscription Owner is an opaque, client-defined end-user reference set
-- at creation and never changed; NULL marks a global, organization-wide
-- Subscription. Every Version, Match and notice of the Subscription shares it.
ALTER TABLE subscriptions ADD COLUMN owner text;
-- The owner listing pages active Subscriptions of one owner (or the global
-- ones) by ID.
CREATE INDEX subscriptions_active_by_owner ON subscriptions(organization,owner,id) WHERE enabled AND NOT deleted;
-- Pinned Corpora by Subscription: every Subscription read aggregates them and
-- the owner listing checks them against the key's grant.
CREATE INDEX subscription_corpora_by_subscription ON subscription_corpora(organization,subscription_id);
