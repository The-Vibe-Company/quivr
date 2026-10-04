# Merging a pull request

## Before

1. The worker has handed back (`Agent phase` = `ready-to-merge`), and you have the full 40-character head SHA it reported.
2. `gh pr view <n> --json headRefOid,mergeStateStatus` shows that SHA and `CLEAN`.
3. `gh pr checks <n>` shows every check passing (skipped "Mintlify Deployment" is normal).
4. The head contains current `origin/main`: `git merge-base --is-ancestor origin/main <sha>`. If not, either ask the worker to rebase, or test-merge it yourself in a throwaway worktree (`git worktree add --detach <tmp> origin/main`, merge the branch, run `make docs`, `make denylist`, `go build ./...`) when the PR only touches files main did not change. Parallel merges break `make docs` budgets and generated files more often than code.
5. Semantic check: grep `main` for callers of anything the PR deletes or renames.

## Merge

```
gh pr merge <n> --squash --match-head-commit <full-sha>
```

- Leave out `--delete-branch`: it deletes local worktrees that have the branch checked out, including other agents'.
- If GitHub answers with a 5xx or "Something went wrong", check `gh pr view <n> --json state`, then retry every minute; it usually clears within a few minutes.

## After

1. Linear: state Done, remove the `Agent phase` and `Agent runtime` labels.
2. Tell every in-flight worker affected: a newer migration, a Plugin API version taken, regenerated files, a new repository-wide check, code they must now delete or reuse.
3. Launch the tickets this merge unblocked.
4. If the merge changes runtime behaviour, run the checks in [OPERATIONS.md](OPERATIONS.md).
