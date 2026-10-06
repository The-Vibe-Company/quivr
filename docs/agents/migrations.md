# Add a migration that preserves application rollback

Contributors keep the previous Quivr binary usable on the expanded schema.
Operators apply expansions before rolling the API and worker, and apply
contracts only after retiring the previous binary. There are no down migrations.

## Write the expansion

Create a timestamped file with `make migration name=<slug>`; migrations already
merged are frozen. Add new tables or nullable columns, or columns with non-null
constant defaults so previous writers can omit them. Both versions must read and
write successfully. Keep later expansions independent of any deferred contract.

`make check` parses PostgreSQL SQL with the pinned `pglast` dependency in
`contracts/http/v0/checks/requirements.txt`. Its conservative allowlist accepts
new tables without foreign keys into existing tables, sequences, enum types,
indexes on tables created in the same file,
and added columns without new checks, uniqueness or references. Mixed ALTER
commands are checked separately. Row rewrites, renames, type changes, procedural
SQL, constraints and indexes on existing tables require a separate contract
release. The lint is a guard, not a proof of every application's semantics.

For a rename, add the new field in release N, keep reading the old field and
write both forms while older binaries run. Backfill with resumable application
work, then switch reads in a later release. Never remove the old field during
that expansion. Review default expressions and cross-version write behaviour.

## Defer the contract to another release

Release N+1 may stop supporting N only after an operator explicitly opts in.
Put the destructive step in its own timestamped file. Its first two lines are,
for example (replace the filename with the already released expansion):

```sql
-- quivr:contract
-- quivr:expand 20261001T0000Z_expand_example.sql
```

The expansion reference must exist in the previous version's migration set.
Until release artifacts are wired into this check, CI uses the merge-base commit
as that version; reviewers still verify that the expansion shipped in an earlier
release. The filename reference records that dependency without rewriting SQL.
Tags use physical LF or CRLF lines; other separators are rejected.
Contract SQL cannot manage transactions or change session settings. Ordinary
`quivr migrate`, API readiness and worker startup skip tagged contracts.
`quivr migrate --contract` applies all pending expansions and contracts in
filename order, with transactional bookkeeping and an advisory lock.

All migration runs bound lock acquisition to two seconds and each SQL batch to
five seconds. A timeout rolls back the whole migration transaction. Split large
work into resumable application operations; retry migration after the conflicting
transaction finishes. These bounds limit interference; they do not guarantee
that database writes never wait briefly for a DDL lock.

## Check compatibility

Run `make migration-compatibility` on Linux x86_64 with Docker and Python 3.12.
It builds the merge-base API and worker, applies candidate expansions while they
run, and reuses that commit's inline-ingestion, authorization and lexical-search
acceptance cases. It restarts the previous binary on the expanded schema and
runs the cases with a fresh Organization to prove new writes still work.
CI requires this job alongside the existing verification jobs. Evidence records
the previous commit, candidate commit, selected cases and elapsed time.

## Existing migrations

`migrations/legacy.json` classifies every SQL file present when this policy was
introduced, pins its SHA-256 and lists detected risks. `legacy-risk` flags drops,
constraint changes, row updates/deletes, procedural SQL and other statements
outside the additive allowlist. These files still run in the historical order on
fresh installations. Their classification does not make old upgrades compatible;
never re-tag or rewrite them. The inventory and every merged migration are frozen
by the lint. New migrations cannot use this exception list.

Operator steps and the rollback window are in the public
[upgrade guide](https://docs.quivr.thevibecompany.co/run-quivr/upgrade-quivr).
