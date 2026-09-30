---
name: armada-runtime-conductor
description: Runtime guide for running Armada workers on Conductor Cloud. Use when an Armada coordinator must launch a worker on a ticket, send it a message, check whether a silent worker is still alive, or stop and archive it. Four fixed sections, each with the exact conductor command and how to read its output.
---

Armada never calls a runtime. This guide tells the coordinator how to launch, message, check and stop a worker with the `conductor` command line tool, and what to record in Armada afterwards. It was checked against `conductor` 0.89.2; if a flag is refused, compare with `conductor <command> --help`.

- Always pass `--json` and read fields with `jq`. Exit codes: 0 ok, 1 runtime error, 2 usage error, 3 authentication (`conductor auth status`), 4 server error.
- A worker is one workspace with one session. Its Armada handle is `<workspaceId>/<sessionId>`; the worker's claim comment carries it, so `armada status` and the ticket always lead back to the session.
- Never type a secret value into a command, a file or a message: name the variable and let your shell expand it. The expanded value is still in the `conductor` process's arguments while it runs, so launch from a machine only you use.

## Launch

1. Pick a ready ticket from `armada status` that does not collide with work in flight.
2. Run `armada brief ABC-12`. Read the profile (agent, model, effort, fast mode), the environment table and the warnings. Resolve every warning first: an open blocker, a ticket already in flight. Export each required variable the table marks `NOT set in this shell`. One marked `in credentials file` is in Armada's machine store: keep the `set -a` line of the launch command below, which loads that file without printing it.
3. Write the prompt to a file, and add what only you know (the boundary with a parallel worker, a decision not yet on the ticket):

```sh
armada brief ABC-12 --prompt > /tmp/abc-12-brief.md
```

4. Create the workspace with every value from the profile. Never leave the agent, model or effort to Conductor's defaults. Add `--fast-mode` when the profile says fast mode; drop an optional `--env` line whose variable you do not have. Run the whole block as one command: shell state does not carry over between separate calls, and the subshell keeps the keys out of the rest of your session. Drop the `set -a` line when every variable is set in your shell.

```sh
(
  set -a; . "${XDG_CONFIG_HOME:-$HOME/.config}/armada/credentials"; set +a
  [ -n "$LINEAR_API_KEY" ] || { echo "LINEAR_API_KEY is missing" >&2; exit 1; }
  conductor --json workspace create \
    --repo-url https://github.com/<owner>/<name> \
    --branch main \
    --name "ABC-12 <short title>" \
    --agent claude --model opus-5-5-1m --effort high \
    --message-file - \
    --env ARMADA_TICKET=ABC-12 \
    --env LINEAR_API_KEY="$LINEAR_API_KEY" \
    --env ARMADA_TURSO_URL="$ARMADA_TURSO_URL" \
    --env ARMADA_TURSO_TOKEN="$ARMADA_TURSO_TOKEN" \
    < /tmp/abc-12-brief.md > /tmp/abc-12-launch.json
)
jq -r '"\(.workspaceId)/\(.sessionId)"' /tmp/abc-12-launch.json
```

- `--branch` is the base branch (the default branch). Conductor creates a branch named `conductor/<slug of --name>`; the brief tells the worker to rename it to the ticket's branch.
- The output has `workspaceId`, `sessionId`, `deepLink` and `initialMessage` (`messageId`, `state: "queued"`). Keep the handle the `jq` line prints, and give the owner the `deepLink` when they want to watch.
- Conductor sets `CONDUCTOR_WORKSPACE_ID` and `CONDUCTOR_SESSION_ID` inside the workspace. The brief's first commands install your Armada version (`npm install -g`) and claim the ticket with them. `--env` values are not shown back by `workspace get`.
- An unknown agent, model or effort fails the command. Check the ids with `conductor model` (each agent's models, efforts and defaults) and fix the profile in `armada.toml`.

5. **Check the claim.** Within a few minutes, `armada status` lists the ticket in flight, phase `planning`, runtime `Conductor`, and the ticket's claim comment reads `session: <workspaceId>/<sessionId>`. No claim after ten minutes: read the transcript (Status section). If the worker cannot claim (for example a missing key), fix the cause and message it; as a last resort record the handle yourself with `armada claim ABC-12 --runtime conductor --handle <workspaceId>/<sessionId>`.

## Message

```sh
printf '%s\n' "Plan approved. Go on." | conductor --json message create --session <sessionId> --message-file -
conductor --json message create --session <sessionId> --message-file - < /tmp/abc-12-answer.md
```

The output is `{"messageId": …, "state": "sent"}`. Exit code 0 means Conductor accepted the message, not that the worker read it: check that the session turns `working`, then read its reply in the transcript. Use it to deliver an answer, a plan approval, or a heads-up that the default branch moved. After delivering an answer, record it with `armada answer` when your Armada version has it.

## Status

```sh
conductor --json session status <sessionId>
```

The output is `{"workspaceId", "sessionId", "status", "updatedAt"}`. For a worker:

- `working`: a turn is running. A silent worker that is `working` is busy (a long build or test run): leave it, and read the transcript if it stays silent past twice `policy.silence_minutes`.
- `idle`: no turn is running. The worker finished its turn: it handed back, it waits for an answer, or it stopped without finishing. If `armada status` does not show it `ready-to-merge`, `awaiting-approval` or `blocked`, it has stopped: read its last reply, then message it to go on, or release and relaunch the ticket.
- `error`: the last turn failed (agent or provider failure). Read the transcript and message it to resume; if it fails again, cancel, archive, release and relaunch.
- Right after launch the session is `idle` for a few seconds while the workspace is initializing and the first message is queued; it turns `working` when the agent starts.

The workspace itself: `conductor --json workspace status <workspaceId>` gives `status` `initializing`, `ready` or `archived`.

**Read the transcript** to see what a worker did or said. Events come oldest first, 100 per page; `hasMore` tells when to fetch the next page with `--after` the last event id. Keep the last id you read and poll from it.

```sh
conductor --json session message <sessionId> --limit 100 > /tmp/abc-12-events.json
jq -r '.hasMore, .data[-1].id' /tmp/abc-12-events.json
jq -r '.data[] | select(.content.rawPayload.type == "result") | .content.rawPayload | "error=\(.is_error)\n\(.result)"' /tmp/abc-12-events.json
jq -r '.data[] | select(.content.rawPayload.type == "assistant") | .content.rawPayload.message.content[] | select(.type == "tool_use") | .input.command // .name' /tmp/abc-12-events.json
conductor --json session message <sessionId> --after <lastEventId> --limit 100
```

The second line prints whether more pages exist and the id to continue from; the third, each turn's final reply; the fourth, the commands and tools the worker ran. `.content.rawPayload` is the agent's own event format: these filters read the `claude` agent; for another agent, look at one event and adapt the filter.

## Stop and archive

```sh
conductor --json session cancel <sessionId>
conductor --json workspace archive <workspaceId>
```

- `session cancel` stops the running turn: `{"status", "canceledQueuedMessages"}`, and the session is `idle` within seconds. The workspace stays; a message starts a new turn. Use it on a worker that runs the wrong thing.
- `workspace archive` answers `{"status": "archived"}`. Archive a worker's workspace after its pull request is merged, or after `armada release --ticket ABC-12 --reason "<why>"` for a worker that stops without handing back. The branch and the pull request stay on GitHub.
- After an archive, `session status` still answers `idle`; `workspace status` says `archived`.
