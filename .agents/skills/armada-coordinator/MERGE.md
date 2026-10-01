# Merging a pull request

Run `armada merge <pr>` (add `--dry-run` to see the checklist only). It checks everything below, takes the project's merge lock, merges pinned to the handed-back SHA, confirms the merge on GitHub and closes the ticket. Its output lists the workers in flight to tell and the worker session to archive. When a refusal names a rule, fix the cause (usually: ask the worker to rebase and report again) rather than merging by hand. When you must merge by hand, follow the same steps.

## Before

1. The worker has handed back: `Agent phase` is `ready-to-merge` and you have the full 40-character head SHA it reported.
2. `gh pr view <n> --json headRefOid,mergeStateStatus,isDraft,title` shows that SHA, `CLEAN`, not a draft, and a Commitizen title.
3. `gh pr checks <n>` shows every required check passing.
4. The head contains the current default branch: `git merge-base --is-ancestor origin/<default> <sha>`. If not, ask the worker to rebase.
5. Semantic check: search the default branch for callers of anything the pull request deletes or renames.

## Merge

```sh
gh pr merge <n> --squash --match-head-commit <full-sha>
```

- One merge at a time: `armada merge` holds the project's merge lock. If it refuses because Turso is down, use `--no-lock` only when you are sure no other coordinator merges in the project.
- Leave out `--delete-branch`: it deletes local worktrees that have the branch checked out, including other agents'. Delete the branch once its worktree is gone.
- If GitHub answers with a 5xx, check `gh pr view <n> --json state` before retrying.

## After

1. The ticket is Done, its `Agent phase` and `Agent runtime` labels removed, the pull request linked.
2. Tell every in-flight worker what the merge changes for them: a shared file, a migration, a new check, code they must now reuse or delete.
3. Archive the merged worker's workspace with the "Stop and archive" section of its runtime guide.
4. Launch the tickets this merge unblocked.

## The release pull request

Some repositories publish through release-please: every merge to the default branch opens or updates one release pull request, and merging it tags the version and publishes it. When the repository's rules (`AGENTS.md`) say to merge it after each merge, do it right after the ticket's merge, without a hand-back: no worker owns it, so `armada merge` does not apply.

- **What it looks like.** Title `chore(main): release <version>`, opened by `github-actions`, label `autorelease: pending`, and a diff that only touches the changelog, the manifest and version fields. It is opened with the workflow's own token, so **no CI check runs on it** (only checks from apps such as Vercel, if any) and `mergeStateStatus` is `UNSTABLE`. Both are expected: the publish job runs the repository's checks again before publishing. Anything else in the diff, or a `DIRTY` state, is not expected: stop and tell the owner.
- **Find it and its head:**

```sh
gh pr list --state open --label "autorelease: pending" --json number,title,headRefOid,mergeStateStatus
```

- **Merge it** pinned to that head, like any merge, and without `--delete-branch`:

```sh
gh pr merge <n> --squash --match-head-commit <full-sha>
```

- **Check the publish.** After a minute or two the new version is on the registry, for example `npm view <package> version` for an npm package (the package is named in `AGENTS.md` or the release workflow). If it is not, open the Release run (`gh run list --workflow release.yml --limit 3`), fix the cause and re-run its failed jobs: a later push does not publish a version already tagged.
- Release-please updates the same pull request on each merge to the default branch; one release pull request can carry several merges.
