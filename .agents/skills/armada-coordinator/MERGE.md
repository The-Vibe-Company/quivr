# Merging a pull request

Run `armada merge <pr>` (add `--dry-run` to see the checklist only, `--wait` when main keeps moving, `--no-ticket` for a pull request no ticket owns). It checks everything below, takes the project's merge lock, merges pinned to the handed-back SHA, confirms the merge on GitHub and closes the ticket. Its output lists the workers in flight to tell and the worker session to archive. When a refusal names a rule, fix the cause (usually: ask the worker to bring the default branch in and report again) rather than merging by hand. When you must merge by hand, follow the same steps.

## The owner's merge rule: `--reason` and `--ask-owner`

`[policy] merge_approval` in `armada.toml` says in plain words which merges the owner wants to approve first, for example "merge on your own, except front-end changes: send me a link to check them first". Without it you merge everything on your own. With it, `armada merge` prints the rule and you judge each pull request: look at its files and at what users will see, then record why either way.

- It may merge on your own: `armada merge <pr> --reason "<why>"`, for example `--reason "CLI only"`. The ticket's merged comment and the dashboard say `merged on its own (rule: …): CLI only`.
- The rule keeps it for the owner: `armada merge <pr> --ask-owner --reason "<why>"`, for example `--reason "touches components/timeline"`. It merges nothing: it records the pull request as GitHub shows it now (title, files with +/−, CI, the preview deployment of its head) with the ticket's screenshots on the owner's Validations page, posts the approval link (`https://<dashboard>/approve/<id>`) on the ticket and prints it. Send it to the owner. Their decision arrives in your inbox as a `decision` item (it wakes `armada watch`): approved, run `armada merge <pr>`, which records `approved by <owner> at <time>`; changes requested, relay them to the worker.
- Once the owner was asked about a pull request, `armada merge` refuses it until they approved that exact head; a head that is it with only main merged in still counts. Any other new head needs `--ask-owner` again.

## Main kept moving: `--wait`

With several workers in flight, main often moves between a green hand-back and its merge. Instead of asking the worker to bring main in, run `armada merge <pr> --wait [--timeout <min>]` (30 minutes by default), in the background where your runtime allows it (Claude Code: `run_in_background`):

- A head behind main that GitHub says merges cleanly is updated with GitHub's "update branch": a merge commit on the branch, no force-push. Squash merges make that merge commit harmless. When main does not require heads to be up to date and `[gates] local_commands` is set, it is test-merged instead, as without `--wait`, so a busy main does not restart CI each time.
- It waits for every required check on the new head, then merges pinned to that head.
- It holds the project's merge lock only while merging, not while waiting, so two waits never block each other. Signed in with Armada down, it refuses before touching the branch.
- It stops at once, naming the cause, on a red check, a conflict, any other refused rule, or when GitHub refuses the update (a branch protection rule or ruleset can forbid it) or accepts it without the head moving within 3 minutes: ask the worker to fix it. A refusal after an update says so: the worker pulls the updated branch before pushing again.
- The updated head still counts as the worker's hand-back, because the only change is main coming in: `armada merge` accepts a head that is the handed-back SHA followed only by merge commits, each bringing in a commit of main, with the tree of a clean merge (checked with git). That holds without `--wait` too, for example after "Update branch" on GitHub. The ticket's merged comment names both heads: `head <new>, the handed-back <old> updated with main`.

## No ticket: `--no-ticket`

`armada merge <pr> --no-ticket` merges a pull request no ticket owns: the `armada init` pull request (its output names the command) or the release pull request below. There is no hand-back and nothing is written to Linear; every other check is unchanged, and the merge is pinned to the head it checked. A branch that names a ticket of the program is refused unless you add `--reason "<why the ticket stays open>"`. Use that override for partial or configuration work that must land before the ticket is done: it requires the configured CI checks, posts the reason on the PR before merging, and leaves the ticket and its worker unchanged. A failed reason comment refuses the merge; `--dry-run` posts nothing.

When none of the required checks ran on the head (a release pull request opened with the workflow's own token gets no CI run), it passes with a note, on GitHub's own state (`UNSTABLE` included, when nothing failed) and the checks that did run, once the head is a minute old; a younger head, or a head this run updated (its update starts CI), is waited for (`--wait`) or refused.

## Before

1. The worker has handed back: `Agent phase` is `ready-to-merge` and you have the full 40-character head SHA it reported.
2. `gh pr view <n> --json headRefOid,mergeStateStatus,isDraft,title` shows that SHA (or that SHA with only main merged in), `CLEAN` (or `HAS_HOOKS`), not a draft, and a Commitizen title.
3. `gh pr checks <n>` shows every required check passing.
4. The head contains the current default branch: `git merge-base --is-ancestor origin/<default> <sha>`. If not, update it (`--wait`) or ask the worker to bring the default branch in (rebase, or merge it into their branch; never force-push a branch someone else pushed to).
5. Semantic check: search the default branch for callers of anything the pull request deletes or renames.
6. A dashboard pull request (one that changes Armada's own dashboard, `packages/dashboard`): compare with /agents and /design. Open each page it changes on its preview or the demo next to `/agents` and `/design`. The header bar, the toolbar, the sections and the rows must match them. A page with a title of its own, another font or its own list style goes back to the worker.

## Merge

```sh
gh pr merge <n> --squash --match-head-commit <full-sha>
```

- One merge at a time: `armada merge` holds the project's merge lock. If it refuses because Armada is down, use `--no-lock` only when you are sure no other coordinator merges in the project.
- Leave out `--delete-branch`: it deletes local worktrees that have the branch checked out, including other agents'. Delete the branch once its worktree is gone.
- If GitHub answers with a 5xx, check `gh pr view <n> --json state` before retrying.

## After

1. The ticket is Done, its `Agent phase` and `Agent runtime` labels removed, the pull request linked.
2. Tell every in-flight worker what the merge changes for them: a shared file, a migration, a new check, code they must now reuse or delete.
3. Archive the merged worker's workspace with the "Stop and archive" section of its runtime guide.
4. Launch the tickets this merge unblocked.

## The release pull request

Some repositories publish through release-please: every merge to the default branch opens or updates one release pull request, and merging it tags the version and publishes it. When the repository's rules (`AGENTS.md`) say to merge it after each merge, do it right after the ticket's merge, without a hand-back: no worker owns it.

- **What it looks like.** Title `chore(main): release <version>`, opened by `github-actions`, label `autorelease: pending`, and a diff that only touches the changelog, the manifest and version fields. It is opened with the workflow's own token, so **no CI check runs on it** (only checks from apps such as Vercel, if any) and `mergeStateStatus` is often `UNSTABLE` (`armada merge --no-ticket` accepts it when none of the required checks ran and nothing failed). Both are expected: the publish job runs the repository's checks again before publishing. Anything else in the diff, or a `DIRTY` state, is not expected: stop and tell the owner.
- **Find it and its head:**

```sh
gh pr list --state open --label "autorelease: pending" --json number,title,headRefOid,mergeStateStatus
```

- **Merge it** with `armada merge <n> --no-ticket`: no ticket owns it, and no required check runs on it (see "No ticket" above). By hand, pin it to that head, like any merge, and without `--delete-branch`:

```sh
gh pr merge <n> --squash --match-head-commit <full-sha>
```

- **Check the publish.** After a minute or two the new version is on the registry, for example `npm view <package> version` for an npm package (the package is named in `AGENTS.md` or the release workflow). If it is not, open the Release run (`gh run list --workflow release.yml --limit 3`), fix the cause and re-run its failed jobs: a later push does not publish a version already tagged.
- Release-please updates the same pull request on each merge to the default branch; one release pull request can carry several merges.
