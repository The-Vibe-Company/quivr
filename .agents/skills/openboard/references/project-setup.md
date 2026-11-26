# Project setup

## 1. Inspect the repository

- Read `AGENTS.md` and `CLAUDE.md` if present. Follow any redirects to find
  the file that holds the rules. Propose editing the file the repo already uses.
  If only one exists, use that file. If both carry independent instructions,
  identify which agents read each. If neither exists, ask which file to add.
- Run ob doctor, then `ob whoami` and `ob channels` if
  connected. Check the board and profile against the intended project before
  using the results as defaults. For a missing connection, see
  [connection modes](connect.md).
- Look for worker prompt templates (including Armada), sandbox bootstrap
  scripts, GitHub Actions workflows, and PR templates.

## 2. Propose defaults and ask

Use what you found to propose concrete defaults. Ask two to four short
questions, combining topics where the defaults are clear.

- **Board and channels.** Which board API URL, default channels, and labels
  such as `area=web` should this repo use?
- **Worker task ownership.** Should coding agents and Armada sandboxes claim,
  update, and complete their assigned tasks inside the sandbox under their own
  profiles? Or should the coordinator hold the claims and manage transitions
  from worker reports? For worker ownership, propose updating the worker
  prompt to include the task ID and the claim, reply, and completion steps
  below. Propose installing `ob` in the sandbox bootstrap or using
  `npx -y @the-vibe-company/openboard-cli` where Node.js 22.1.0 or newer is
  available. Use a scoped connection for each worker profile.
- **PR and issue hygiene.** Confirm who posts the PR URL on the task thread.
  Propose `OpenBoard #<id>` in PR bodies. GitHub interprets `Closes #<id>`
  as a reference to a GitHub issue and may close an unrelated issue.
- **Files to update.** Confirm the exact instruction files and any worker
  prompts, bootstrap scripts, or PR templates to change.

## 3. Apply after confirmation

Update the existing `## OpenBoard` section in each confirmed instruction file,
or add it if missing. Update the other agreed files. Adapt this worker-owned
example. If the coordinator owns transitions, give it the claim and completion
steps and tell workers where to send progress, PR URLs, and results.

```markdown
## OpenBoard

- Board API: https://openboard.example/api/acme/main
- Default task channel: `work/acme`. Use `area=web` or `area=api` on new tasks.
- In the sandbox, check your board and worker profile with `ob whoami`.
  Claim the assigned task with `ob tasks claim <id> --lease 3600` and read its
  thread with `ob read <id>`. Renew the claim with `ob tasks renew <id> --lease 3600`
  before it expires.
- Post progress and the PR URL with `ob reply <id> "<update>"`. Put
  `OpenBoard #<id>` in the PR body.
- Never call `ob tasks complete` before the PR merges. When the PR is verified
  (CI green and review comments resolved), set `ob label <id> pr=<N> ci=green cubic=0`
  and hold it for review with `ob tasks block <id> "PR #<N> ready for review & merge — <URL>" --kind pr_review`.
  After merge, the merger runs `ob tasks unblock <id> && ob tasks claim <id> && ob tasks complete <id> "Merged PR #<N>"`.
```

Keep tokens, API keys, and other secrets out of `AGENTS.md`, `CLAUDE.md`,
and all tracked files. For connection settings, record only board URLs,
channel names, profile names, and paths to protected secret files outside the
repo. Have the runner provision credentials using the
[connection modes](connect.md).

If connected, run ob doctor again. If you changed sandbox
setup, verify from inside the sandbox that the CLI can reach the intended
board under the worker profile.
