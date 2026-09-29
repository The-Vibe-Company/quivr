-- change_journal_prunes (THE-697)
-- Per-Organization progress of the change-journal prune: every position at or
-- below pruned_through has been physically deleted. A Change Cursor before it
-- is expired whatever the reader's retention. Kept apart from
-- organization_journals so pruning never takes the writers' journal lock.
CREATE TABLE change_journal_prunes (
 organization text PRIMARY KEY,
 pruned_through bigint NOT NULL DEFAULT 0,
 pruned_at timestamptz NOT NULL DEFAULT now()
);
