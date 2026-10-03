---
name: armada-runtime-herdr
description: Runtime guide for persistent Armada workers on this machine through herdr. Four sections cover launch, message, status, and stop with safe worktree archival.
---

Herdr keeps each worker in its own worktree and terminal across coordinator restarts. Run these commands on the machine that launched the worker, against the same herdr server/session. The claim stores JSON `{workspace,pane,agent}`: recover it from `armada status --json` or the ticket's claim comment instead of relying on your old coordinator session. Requires herdr 0.9.1+, a supported harness already signed in, and an Armada coordinator sign-in. Never handle harness keys or sign-ins.

## Launch

```sh
armada launch ABC-12 --runtime herdr --profile backend --reason "back end work" --json
```

The profile in `[herdr.profiles]` selects the harness, model and effort. Label routing wins; use a reason for semantic selection or an override. An optional `--harness claude|codex|opencode|deepseek` checks the selected profile. DeepSeek profiles run OpenCode with the exact configured DeepSeek model; Armada reports their runtime agent as `opencode` while preserving the DeepSeek profile label. Launch checks tools first, creates a worktree, starts the harness, and sends the scoped worker brief. Its JSON gives the `handle` and worktree path, without the token. The worker signs in with its one-time launch token, claims the stored handle and starts its heartbeat. No claim after the configured timeout: read the pane, fix the cause, or revoke the unused launch with `armada launch revoke ABC-12`. Failed launch retains the worktree for inspection.

## Message

```sh
armada answer <inbox-id> "Plan approved. Go on."
armada answer ABC-12 "<answer to a live harness approval or question>"
armada answer --note ABC-12 --message-file /tmp/abc-12-message.md
```

For herdr, Armada verifies the saved pane, delivers literal text plus Enter and then records the answer on the ticket and in the inbox. Read an approval UI before deciding what input it expects; the answer may be a short choice such as `y`. Delivery errors leave the question open and write no successful answer. Inspect the pane before retrying after a timeout: the terminal may already have received the input. There is no separate delivery step. Successful delivery confirms submission, not that the worker has completed a turn. For other runtimes, follow their guide and deliver before recording with Armada.

## Status

```sh
armada status --json
armada inbox
herdr agent get <pane>
herdr agent read <pane> --source recent-unwrapped --lines 120
herdr agent wait <pane> --until idle --until done --until blocked --timeout 30000
```

`status`, `inbox` and each `watch` poll discover workers from persisted claims and publish local observations to Armada. The dashboard shows that stored live state beside the last report; it never probes a local runtime. Observations expire after `policy.silence_minutes`.

Herdr's `.result.agent` carries `workspace_id`, `pane_id`, `name` and `agent_status`. Verify all three IDs against the claim. `working` is a running turn, `blocked` is an approval or question UI, and `idle`/`done` are ready for input. A live `runtime-blocked` inbox entry can appear before a worker report; read its pane and answer by ticket. It clears after an answer until the next blocked transition. Herdr's state-change counter preserves approvals that arrive between polls, even when both readings are blocked. `unknown` falls back to Armada reports; it is not proof of completion. DeepSeek and other harnesses also report their phase to herdr via `pane report-agent --source armada` on report, ask and heartbeat. Reporting failures warn and do not fail the worker command.

Terminal reads print text directly, not JSON. Do not paste credentials or complete transcripts into tickets. A coordinator restart changes no worker handle: read `armada status` and continue from those claims.

## Stop and archive

```sh
# For unfinished work, release the ticket with its reason first.
armada release --ticket ABC-12 --reason "<why the worker is being stopped>"
armada stop ABC-12
```

After merge, run `armada stop ABC-12` directly; the released claim is still discoverable for cleanup. Stop verifies the linked worktree belongs to this repository and the claim's branch. It refuses uncommitted files (including untracked files), a missing remote upstream, an unreachable upstream, or commits not on that upstream, and explains what remains. Push or preserve that work before retrying; never force removal. It fetches the configured upstream, interrupts an active turn and waits up to five seconds for idle or done, then rechecks the checkout before `herdr worktree remove --workspace <workspace>` without `--force`. Herdr ends the workspace terminals and removes the checkout; the branch remains. The exact saved claim is marked ended. If the worker does not settle, the checkout stays intact; inspect the pane and retry. Failed removal leaves the claim and checkout for inspection; inspect `herdr worktree list` before retrying a timeout.
