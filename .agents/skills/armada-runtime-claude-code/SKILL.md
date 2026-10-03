---
name: armada-runtime-claude-code
description: Runtime guide for running Armada workers as local subagents of a Claude Code coordinator. Use when an Armada coordinator running in Claude Code must launch a worker on a ticket with the Agent tool, send it a message, check whether a silent worker is still alive, or stop it and clean up its worktree. Four fixed sections, each with the exact tool call and how to read its result.
---

Armada records Conductor and Claude Code runtime actions separately; persistent herdr workers use integrated commands. This guide tells a coordinator running inside Claude Code how to launch, message, check and stop a worker as one of its own subagents (the Agent tool), and what to record in Armada afterwards. It was checked against Claude Code's Agent, SendMessage, TaskStop and EnterWorktree tools as of October 2026; if a parameter below is refused, read the tool's own description and adapt.

- **A subagent lives inside your session.** It dies when your session ends: closing the terminal, a crash or the end of a cloud machine stops every worker you launched this way, mid-turn. Use this runtime for short tickets you will see through in one sitting. Long runs, and anything that must survive you, go to Conductor (the `armada-runtime-conductor` skill).
- **Always in its own worktree.** A subagent launched without `isolation: "worktree"` works in your own checkout: it edits your files and switches your branch under you, and two such workers overwrite each other. Never launch one without it.
- A worker is one background subagent. Its Armada handle is its name: the ticket id in lowercase (`abc-12` for ABC-12), which the brief's claim line already uses. The worker's claim comment carries it, so `armada status` and the ticket lead back to the subagent.
- **It shares your machine and environment**: your `gh` sign-in for its pushes, your installed `armada`, and every environment variable of your session. A worker needs no key: the prompt of `armada brief` carries a one-time launch token, and its first command exchanges it for a session limited to its ticket, kept in the machine's credentials file next to your own sign-in. Because it inherits your environment, an `ARMADA_TICKET` of yours is not its ticket: the brief tells it to pass `--ticket` to every command.
- Run `armada watch` in the background while a worker is in flight, as the coordinator skill says: a worker's question, plan or hand-back reaches your inbox, not your conversation. The task notification Claude Code sends when a subagent stops is a hint to look, not the record.
- `armada inbox` and `armada status` run fine while a watch runs: never stop it to read the inbox or the fleet. To stop this project's watch, use `armada watch --stop`, never `pkill` or `killall` patterns: those can kill other projects' watches on the same machine.

## Launch

The brief starts a heartbeat immediately after claim. Run the heartbeat command in the subagent's **background Bash** (`run_in_background: true`), with `--parent "$PPID"` and its claim's `--handle`; do not use `--background`, `nohup` or `setsid` here. A subagent's background Bash dies with the subagent, which is the intended behaviour: detaching it could falsely keep a finished subagent alive while the coordinator's process still runs. It also stops when Armada ends its session (release, merge, revoke). If a runtime cannot keep any background Bash, report the fallback on the ticket and keep today's manual reports at least every 15 minutes. Report actual progress at meaningful steps even when heartbeats run.

1. Pick a ready ticket from `armada status` that does not collide with work in flight.
2. Read the ticket and parent, then choose its profile. A matching label rule wins; otherwise match the profiles' `when` rules by most files/work and pass `--profile <name> --reason "<why>"`. Mixed front-end/back-end work: choose the larger part and say why. An optional `armada brief ABC-12` or `--json` preview is read-only: neither mints a token nor marks a worker in flight. The profile must have `runtime = "claude-code"` in `armada.toml`. The profile's effort is not applied by the Agent tool; a worker that needs a set effort goes to Conductor.
3. **Make one brief call for the launch:** `armada brief ABC-12 --prompt --profile-line`, keeping the chosen `--profile` and `--reason` flags. Stdout is the exact worker prompt; stderr names the profile, agent, model, effort, runtime and reason, so no second brief is needed for the model. Resolve warnings before launching. If the choice is still needed, `--prompt` refuses without minting anything. **The launch message is that `--prompt` output, and only it**: the human view and `--json` carry no token. The token works once, within the hour; generate a fresh prompt if it has expired. If you cancel or launching the subagent fails, use `armada launch revoke ABC-12`. Add what only you know (the boundary with a parallel worker, a decision not yet on the ticket) at the end. Without a launch token, stderr says why: usually `armada login`, then generate a new prompt.
4. Launch with the Agent tool, every value set:

```json
{
  "description": "ABC-12 <short title>",
  "name": "abc-12",
  "prompt": "<the whole armada brief --prompt text, then your notes>",
  "subagent_type": "general-purpose",
  "model": "<the profile's model, e.g. opus>",
  "isolation": "worktree",
  "run_in_background": true
}
```

- `isolation: "worktree"` gives the worker a git worktree of its own under `.claude/worktrees/` (for example `.claude/worktrees/agent-<id>`), on a new branch (`worktree-agent-<id>`) from the default branch; the brief tells the worker to rename it to the ticket's branch. Never leave it out.
- `run_in_background: true` returns at once, so you keep coordinating. Without it your turn waits for the whole ticket.
- `name` is the handle in the brief's claim line. When your Agent tool has no `name` parameter, the result's `agentId` is the only way to reach the worker: record it at once with `armada answer --note ABC-12 "subagent <agentId>"`, so the ticket leads back to it after your context is compacted or a new coordinator takes over.
- The result gives the `agentId` and an `output_file`: the worker's transcript (JSONL). Keep both. Never read that file whole: it is the entire conversation (Status section).
- `.claude/worktrees/` appears as untracked in your own checkout: never commit it (add it to `.gitignore`).

5. **Check the claim.** Within a few minutes, `armada status` lists the ticket in flight, phase `planning`, runtime `Claude Code`, and the claim comment reads `session: abc-12` and `profile: <name>`. No claim after `[policy] not_started_minutes` (ten by default): `armada watch` and `armada inbox` show a `not-started` entry saying whether the worker never used its token or signed in and stopped. Read the transcript (Status section). A worker whose launch token was refused needs a new one: brief again and message it only the `armada login --launch-token …` line of the new `--prompt` output. A worker cut off from Armada was revoked on the dashboard or left idle for three days: ask the owner before you give it a new token.

## Message

```json
{ "to": "abc-12", "summary": "ABC-12 plan approved", "message": "Plan approved. Go on." }
```

SendMessage reaches the worker by its name, or by its `agentId` when it has none. A worker that is running gets the message at its next tool call; a worker that finished its turn (it handed back, asks a question, or stopped) is resumed from its transcript, in its worktree, and answers in a new turn. Success means the message was delivered, not that the worker acted on it: check its status, then read its reply. Make the first line a sentence that says what the message is. Use it to deliver an answer, a plan approval, or a heads-up that the default branch moved. Then record it in Armada: `armada answer <item> "<answer>"` for a question from `armada inbox`, `armada answer --note ABC-12 "<message>"` for a message the worker did not ask for.

## Status

Is the worker alive, and what did it do last? Three readings, cheapest first:

- **ListAgents** lists your subagents that are running: `abc-12 · general-purpose · running · started 4m ago`. A worker not in the list has finished its turn: it handed back, waits for an answer, or stopped. If `armada status` does not show it `ready-to-merge`, `awaiting-approval` or `blocked`, it has stopped without finishing: read its last reply, then message it to go on, or stop it and release the ticket.
- **The task notification** Claude Code sends you when a subagent stops: `status` is `completed` (its turn ended) or `killed` (stopped), and `result` is its final reply. A worker that stops on an error says so there: read the transcript and message it to resume.
- **Its last activity**: the time its transcript was last written, and the last event in it. A `running` worker whose transcript has not moved for twice `policy.silence_minutes` is stuck: message it, then stop it if it does not answer.

```sh
f=<output_file>
date -u -r "$f" +%FT%TZ
tail -n 1 "$f" | jq -r '[.timestamp, .type, (.message.content[0].type // "")] | join(" ")'
```

Its final reply and the commands it ran, without reading the whole file:

```sh
tail -n 200 "$f" | jq -r 'select(.type == "assistant") | .message.content[] | select(.type == "text") | .text' | tail -n 40
tail -n 200 "$f" | jq -r 'select(.type == "assistant") | .message.content[] | select(.type == "tool_use") | .input.command // .name'
```

A filter that prints nothing: count the event types of the last lines, look at one event of the type you need, and adapt.

```sh
tail -n 200 "$f" | jq -r '.type' | sort | uniq -c
```

## Stop and archive

A subagent has no workspace to archive: archiving it means stopping it and removing its worktree.

1. Stop a worker that runs the wrong thing, or one you release: TaskStop with its name (or `agentId`).

```json
{ "task_id": "abc-12" }
```

The notification that follows says `killed` and names the worktree it leaves: `worktreePath` and `worktreeBranch`. A stopped worker keeps its worktree and its changes; a message would resume it there.

2. Record why when it does not hand back: `armada release --ticket ABC-12 --reason "<why>"`.
3. Remove its worktree once its pull request is merged, or once you released the ticket and nothing in the worktree is worth keeping. Wait until the worker is no longer running (ListAgents), then, from your own checkout:

```sh
git worktree list                               # find the worker's path and branch
git -C .claude/worktrees/<dir> status --short   # what would be lost
git worktree remove --force .claude/worktrees/<dir>
git branch -D <the worker's local branch>       # the ticket's branch; the pull request and the remote branch stay on GitHub
```

A worktree in which the worker changed nothing is removed by Claude Code when the subagent ends; `git worktree list` no longer shows it.

## A worker without a worktree

A subagent launched without `isolation: "worktree"` runs in your checkout. The brief's first section makes the worker check `git rev-parse --show-toplevel` before anything else and, when it is not under `.claude/worktrees/`, call the `EnterWorktree` tool with its name to move into a worktree of its own before it touches a file. Claude Code may refuse that from a subagent (it would change the working folder of your whole session); the worker then changes nothing, says so in its reply and ends its turn. Then:

1. Check your own checkout: `git status --short` and `git branch --show-current`. Anything the worker changed there is not yours: move it to a worktree before you go on (`git stash push -- <its files>`, then `git stash pop` inside the new worker's worktree once it runs), and switch back to your branch if it moved.
2. Stop it (TaskStop), and launch it again under the same name with `isolation: "worktree"`. A worker that had already claimed claims again from the same handle, which keeps its claim.
