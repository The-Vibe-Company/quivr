-- Upgrade an alert-rule plugin without breaking existing alerts (THE-805).
-- A Subscription Version keeps the alert-rule plugin version it recorded until
-- an operator migrates it. The plugin registry counts, for each version, the
-- Subscriptions and pending evaluations that still use it, and a migration
-- lists the Subscriptions pinning a version: both look Subscription Versions
-- up by evaluator plugin id and version.
CREATE INDEX subscription_versions_by_evaluator ON subscription_versions((evaluator->>'plugin_id'),(evaluator->>'version'));
