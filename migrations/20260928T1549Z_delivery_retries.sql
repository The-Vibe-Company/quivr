-- Delivery window start: automatic retries end exhausted once created_at plus
-- the configured window has passed. Deliveries created before this migration
-- start their window at migration time.
ALTER TABLE deliveries ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
-- Why automatic attempts ended: a permanent receiver failure, or a window that
-- elapsed after a retryable failure or while the work was not admissible.
-- Empty unless the Delivery is exhausted.
ALTER TABLE deliveries ADD COLUMN exhausted_reason text NOT NULL DEFAULT ''
 CHECK(exhausted_reason IN ('','permanent_error','window_elapsed'));
-- Upgrade Deliveries parked by the previous worker, which scheduled no retry:
-- a permanent failure ends exhausted; every other parked Delivery becomes due
-- so the worker rechecks admission (re-parking disabled, withdrawn or
-- unconfigured work) and retries within its window.
UPDATE deliveries SET state='exhausted',exhausted_reason='permanent_error'
 WHERE state='pending' AND last_outcome='permanent_error';
DELETE FROM delivery_outbox o USING deliveries d
 WHERE (d.organization,d.id)=(o.organization,o.delivery_id) AND d.state='exhausted';
UPDATE delivery_outbox SET available_at=now(),lease_until='-infinity' WHERE available_at='infinity';
-- Backlog gauges scan only scheduled (non-parked) work.
CREATE INDEX delivery_outbox_scheduled ON delivery_outbox(available_at) WHERE available_at<'infinity';
