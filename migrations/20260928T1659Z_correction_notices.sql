-- Monitoring intents also carry withdrawal notification work: a withdrawal
-- intent consumes a committed record.withdrawn event for one Subscription with
-- a Match on the Record, names that Record's latest matched Version and runs
-- no evaluator.
ALTER TABLE evaluation_intents ADD COLUMN kind text NOT NULL DEFAULT 'evaluation'
 CHECK(kind IN ('evaluation','withdrawal'));
-- Journal position of each notice's event. A later correction notice for the
-- same Subscription and Record supersedes an undelivered earlier positive
-- notice; the position keeps that order independent of change-event retention.
-- Every existing notice is a match.created committed with its Match.
ALTER TABLE monitoring_notices ADD COLUMN position bigint;
UPDATE monitoring_notices n SET position=m.position FROM matches m
 WHERE (m.organization,m.id)=(n.organization,n.match_id);
ALTER TABLE monitoring_notices ALTER COLUMN position SET NOT NULL;
CREATE INDEX monitoring_notices_by_record ON monitoring_notices(organization,subscription_id,record_id,position);
-- Prior positive Match lookup for corrections and withdrawal dispatch.
CREATE INDEX matches_by_record ON matches(organization,record_id,subscription_id,position);
