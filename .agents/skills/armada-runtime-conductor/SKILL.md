---
name: armada-runtime-conductor
description: Runtime guide for running Armada workers on Conductor Cloud. Use when an Armada coordinator must launch a worker on a ticket, send it a message, check whether a silent worker is still alive, or stop and archive it. Four fixed sections, each with the exact conductor command and how to read its output.
---

Armada never calls a runtime. This guide tells the coordinator how to launch, message, check and stop a worker with the `conductor` command line tool, and what to record in Armada afterwards. It was checked against `conductor` 0.89.x (the desktop app's CLI, macOS) and 0.1.x (the CLI inside a Conductor Cloud workspace, Linux); every command below works in both. `conductor --version` prints yours; if a flag is refused, compare with `conductor <command> --help`.

- On a Mac, the app ships the CLI at `/Applications/Conductor.app/Contents/Resources/bin/conductor`, which is not on PATH. `armada doctor` looks for it and prints the fix: a link from a directory already on PATH (`ln -s "/Applications/Conductor.app/Contents/Resources/bin/conductor" ~/.local/bin/conductor`), or a PATH line for your shell profile.
- Always pass `--json` and read fields with `jq`. Exit codes: 0 ok, 1 runtime error, 2 usage error, 3 authentication, 4 server error.
- On exit code 3, `conductor auth whoami` checks the token the CLI uses (it exits 0 when the token works). Do not rely on `conductor auth status`: it only looks for a macOS Keychain entry and fails on Linux ("Keychain storage is only supported on macOS"). In a Conductor Cloud workspace the CLI reads `CONDUCTOR_API_KEY` from the environment and needs no login; on a Mac, `conductor auth login` stores a token in the Keychain.
- A worker is one workspace with one session. Its Armada handle is `<workspaceId>/<sessionId>`; the worker's claim comment carries it, so `armada status` and the ticket always lead back to the session.
- A worker needs no key in its workspace: the prompt of `armada brief` carries a one-time launch token, and the worker's first command exchanges it for a session limited to its ticket, through which Armada gives each command its keys. The token works once, within the hour, so a copy left in a transcript is useless once used.
- Never type a secret value into a command, a file or a message: name the variable and let your shell expand it. The expanded value is still in the `conductor` process's arguments while it runs, so launch from a machine only you use.

## Launch

The brief starts `armada heartbeat --every 5m --parent "$PPID" --background` immediately after claim. It creates a detached process group (like `nohup` + `setsid`) with closed terminal streams and a PID file under `~/.config/armada/watch/`. Plain `&` and `nohup` alone can be killed by command-tool cleanup on Cloud; detached startup survives between turns and while waiting for a message. `$PPID` in the agent's command shell must be the persistent agent process, never `$$` (the shell). The heartbeat stops with that parent or when Armada ends the current session (release, merge, revoke). If startup fails or the runtime cannot retain any background process, fall back to manual reports at least every 15 minutes and record that fallback on the ticket. Reports remain progress dots, not liveness.

1. Pick a ready ticket from `armada status` that does not collide with work in flight.
2. Read the ticket and parent, then choose its profile. A matching label rule wins; otherwise match the profiles' `when` rules by most files/work. Mixed front-end/back-end work: choose the larger part and say why with `--profile <name> --reason "<why>"`. An optional `armada brief ABC-12` or `--json` preview helps resolve the choice and warnings, but is read-only: neither creates a launch token nor marks a worker in flight. If the choice is still needed, `--prompt` refuses before minting anything.
3. **Make one brief call for the launch:** `--prompt --profile-line` writes only the worker prompt to stdout and the selected profile (agent, model, effort, fast mode, runtime and reason) to stderr. Read that line to configure Conductor; no second brief is needed for the profile. Keep any `--profile` and `--reason` flags on this call. Resolve warnings before launching. Never cut a launch message from the human view or `--json`: they carry no token. Write the prompt to a file only you can read, then add what only you know (the boundary with a parallel worker, a decision not yet on the ticket). The token works once, within the hour; if you cancel or the runtime launch fails, use `armada launch revoke ABC-12`. Generate a fresh prompt if its token has expired.

```sh
(umask 077; armada brief ABC-12 --prompt --profile-line > /tmp/abc-12-brief.md)
```

4. Create the workspace with every value from the profile. Never leave the agent, model or effort to Conductor's defaults. Add `--fast-mode` when the profile says fast mode. Run the whole block as one command: shell state does not carry over between separate calls.

```sh
conductor --json workspace create \
  --repo-url https://github.com/<owner>/<name> \
  --branch main \
  --name "ABC-12 <short title>" \
  --agent claude --model opus-5-5-1m --effort high \
  --message-file - \
  --env ARMADA_TICKET=ABC-12 \
  < /tmp/abc-12-brief.md > /tmp/abc-12-launch.json
rm -f /tmp/abc-12-brief.md
jq -r '"\(.workspaceId)/\(.sessionId)"' /tmp/abc-12-launch.json
```

If the prompt has no launch token, its stderr warning says why. Not signed in: `armada login` (a headless coordinator sets `ARMADA_API_KEY`), then generate a new prompt; `armada doctor` checks the sign-in. Only on an Armada that keeps no keys yet (no accounts or no vault), pass the keys the read-only brief's environment table marks `required`, from your shell or from Armada's credentials file (`in credentials file`: the `set -a` line loads it without printing it; drop it when every variable is set in your shell). The subshell keeps the keys out of the rest of your session. Without a launch token the worker is not signed in to Armada: its claims and reports reach Linear only, with a warning. When Conductor's organization environment still holds the keys, every workspace already has them: use the first block.

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
    < /tmp/abc-12-brief.md > /tmp/abc-12-launch.json
)
rm -f /tmp/abc-12-brief.md
```

- `--branch` is the base branch (the default branch). Conductor creates a branch named `conductor/<slug of --name>`; the brief tells the worker to rename it to the ticket's branch.
- The output has `workspaceId`, `sessionId`, `deepLink` and `initialMessage` (`messageId`, `state: "queued"`). Keep the handle the `jq` line prints, and give the owner the `deepLink` when they want to watch.
- Conductor sets `CONDUCTOR_WORKSPACE_ID` and `CONDUCTOR_SESSION_ID` inside the workspace, and signs `gh` in for the worker's pushes. The brief's first commands install your Armada version (`npm install -g`), sign in with the launch token (`armada login --launch-token`) and claim the ticket. `--env` values are not shown back by `workspace get`.
- Keys in Conductor's organization environment reach every workspace, workers included: once Armada keeps the organization's keys, they belong on Armada's Keys page, not there.
- An unknown agent, model or effort fails the command. Check the ids with `conductor model` (each agent's models, efforts and defaults) and fix the profile in `armada.toml`.

5. **Check the claim.** Within a few minutes, `armada status` lists the ticket in flight, phase `planning`, runtime `Conductor`, and the ticket's claim comment reads `session: <workspaceId>/<sessionId>` and `profile: <name>`. No claim after `[policy] not_started_minutes` (ten by default): `armada watch` and `armada inbox` show a `not-started` entry; status lists it under "Pending launches" with `armada launch revoke ABC-12`. It says whether the worker never used its launch token (it never reached its login line) or signed in and stopped before its claim. An unused token expired more than an hour ago produces one `not started (token expired)` inbox notice, then clears. Exchanged launches without a claim stop being followed after 24 hours and remain revocable. Read the transcript (Status section). A worker whose launch token was refused (already used, or more than an hour old) needs a new one: `armada brief ABC-12 --prompt --profile-line` again, and message it only the `armada login --launch-token …` line of the new `--prompt` output (Message section). A worker cut off from Armada was revoked (Organization > Workers says by whom) or left idle for three days: ask the owner before you give a revoked worker a new token. If the worker cannot claim for another reason, fix the cause and message it; as a last resort record the handle yourself with `armada claim ABC-12 --runtime conductor --handle <workspaceId>/<sessionId> --profile <name>`, adding the brief's `--reason` for an override.

## Message

```sh
printf '%s\n' "Plan approved. Go on." | conductor --json message create --session <sessionId> --message-file -
conductor --json message create --session <sessionId> --message-file - < /tmp/abc-12-answer.md
```

The output is `{"messageId": …, "state": "sent"}`. Exit code 0 means Conductor accepted the message, not that the worker read it: check that the session turns `working`, then read its reply in the transcript. Use it to deliver an answer, a plan approval, or a heads-up that the default branch moved. Then record it in Armada: `armada answer <item> "<answer>"` for a question from `armada inbox`, `armada answer --note ABC-12 "<message>"` for a message the worker did not ask for.

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
conductor --json session message <sessionId> --after <lastEventId> --limit 100
```

The second line prints whether more pages exist and the id to continue from. `.content.rawPayload` is the agent's own event format, so the filters depend on the worker's agent (the `agent` of its profile in `armada.toml`).

A `claude` worker: each turn's final reply, then the commands and tools it ran.

```sh
jq -r '.data[] | select(.content.rawPayload.type == "result") | .content.rawPayload | "error=\(.is_error)\n\(.result)"' /tmp/abc-12-events.json
jq -r '.data[] | select(.content.rawPayload.type == "assistant") | .content.rawPayload.message.content[] | select(.type == "tool_use") | .input.command // .name' /tmp/abc-12-events.json
```

A `codex` worker: its events are `.content.rawPayload.event`, each item once as `item.started` and once as `item.completed`; read the completed ones. Its messages (`phase` is `commentary` along the way, `final_answer` at the end of a turn), then each command with its exit code, and its tool calls (Linear and other MCP servers).

```sh
jq -r '.data[] | .content.rawPayload.event | select(.type == "item.completed") | .item | select(.type == "agentMessage") | "[\(.phase)] \(.text)"' /tmp/abc-12-events.json
jq -r '.data[] | .content.rawPayload.event | select(.type == "item.completed") | .item | select(.type == "commandExecution") | "exit=\(.exitCode) \(.command)"' /tmp/abc-12-events.json
jq -r '.data[] | .content.rawPayload.event | select(.type == "item.completed") | .item | select(.type == "mcpToolCall") | "\(.server) \(.tool)"' /tmp/abc-12-events.json
```

Another agent, or a filter that prints nothing: count the event types, look at one event of the type you need, and adapt the filter.

```sh
jq -r '.data[].content.rawPayload | .type // .event.type // "(no payload)"' /tmp/abc-12-events.json | sort | uniq -c
```

## Stop and archive

```sh
conductor --json session cancel <sessionId>
conductor --json workspace archive <workspaceId>
```

- `session cancel` stops the running turn: `{"status", "canceledQueuedMessages"}`, and the session is `idle` within seconds. The workspace stays; a message starts a new turn. Use it on a worker that runs the wrong thing.
- `workspace archive` answers `{"status": "archived"}`. Archive a worker's workspace after its pull request is merged, or after `armada release --ticket ABC-12 --reason "<why>"` for a worker that stops without handing back. The branch and the pull request stay on GitHub.
- **Wait for `idle` before archiving.** A hand-back often arrives while the worker's session is still `working`: `armada report ready-to-merge` runs inside its last turn, which then writes its final reply. Archive only once `session status` answers `idle`. Poll it every 15 seconds; if it is still `working` after 10 minutes, cancel the turn and archive (the transcript keeps what it was doing). Run the whole block as one command; it can take up to 10 minutes, so run it in the background or give it a longer command timeout:

```sh
for i in $(seq 40); do
  [ "$(conductor --json session status <sessionId> | jq -r .status)" = working ] || break
  sleep 15
done
[ "$(conductor --json session status <sessionId> | jq -r .status)" = working ] && conductor --json session cancel <sessionId>
conductor --json workspace archive <workspaceId>
```
- After an archive, `session status` still answers `idle`; `workspace status` says `archived`.
