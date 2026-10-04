-- evaluation_intents_by_subscription_version (THE-725)
-- A Subscription Version decides a Record Version once: enrichment does not
-- ask again a Subscription Version that decided it, an intent waits while
-- another intent of the same pair is being evaluated, and a later intent of
-- a decided pair completes as a duplicate. Those checks look intents up by
-- (Subscription Version, Record Version).
CREATE INDEX evaluation_intents_by_subscription_version ON evaluation_intents(organization,subscription_version_id,record_version_id);
