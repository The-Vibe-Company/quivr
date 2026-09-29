-- subscription_enable (THE-696)
-- Journal position of the latest re-enable. Evaluation resumes after it: a
-- trigger at or before it is never evaluated, so re-enabling does not backfill
-- the pause. NULL until the first re-enable.
ALTER TABLE subscriptions ADD COLUMN enabled_position bigint;
-- Start of a Delivery's window when it is not its creation. A notice committed
-- while its Subscription is disabled (a withdrawal) is 'infinity' until the
-- re-enable opens its window. NULL: the window starts at created_at.
ALTER TABLE deliveries ADD COLUMN window_start timestamptz;
