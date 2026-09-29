-- projection_order_and_purge (THE-698)
-- Activation order. Every Operation records when it was accepted: the value is
-- drawn inside the accepting transaction, which holds the Organization journal
-- lock, so within an Organization it follows acceptance commit order. A route
-- records the acceptance of the Operation that installed it; an Operation can
-- only replace a route installed by an earlier-accepted Operation of the same
-- retrieval configuration version (a newer configuration version still wins).
CREATE SEQUENCE operation_acceptance_seq;
ALTER TABLE operations ADD COLUMN acceptance_seq bigint;
UPDATE operations o SET acceptance_seq=s.n
FROM (SELECT organization,id,row_number() OVER (ORDER BY created_at,id) AS n FROM operations) s
WHERE (o.organization,o.id)=(s.organization,s.id);
SELECT setval('operation_acceptance_seq',COALESCE((SELECT max(acceptance_seq) FROM operations),0)+1,false);
ALTER TABLE operations ALTER COLUMN acceptance_seq SET DEFAULT nextval('operation_acceptance_seq');
ALTER TABLE operations ALTER COLUMN acceptance_seq SET NOT NULL;
ALTER SEQUENCE operation_acceptance_seq OWNED BY operations.acceptance_seq;
ALTER TABLE corpus_projection_routes ADD COLUMN acceptance_seq bigint NOT NULL DEFAULT 0;
UPDATE corpus_projection_routes r SET acceptance_seq=o.acceptance_seq
FROM operations o WHERE o.organization=r.organization AND o.target_generation_id=r.generation_id;
-- Physical purge of projection objects nothing can serve again: abandoned
-- (Organization, Corpus, generation) triples and dead Record Versions. A row is
-- noticed first and purged after the configured grace period; purged_at and
-- objects_deleted record the outcome. Canonical rows, coverage and Embedding
-- Artifacts are never deleted.
CREATE TABLE projection_purges (
 organization text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('generation','version')),
 corpus_id text NOT NULL DEFAULT '',
 generation_id text NOT NULL DEFAULT '',
 version_id text NOT NULL DEFAULT '',
 noticed_at timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz NOT NULL DEFAULT '-infinity',
 purged_at timestamptz,
 objects_deleted bigint NOT NULL DEFAULT 0,
 PRIMARY KEY(organization,kind,corpus_id,generation_id,version_id)
);
CREATE INDEX projection_purges_due ON projection_purges(noticed_at) WHERE purged_at IS NULL;
