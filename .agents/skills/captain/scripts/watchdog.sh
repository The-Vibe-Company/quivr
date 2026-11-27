#!/usr/bin/env bash
# Lightweight, zero-Python, PID+cmdline-guarded wakeup watchdog for Captain.
# Emits a compact live heartbeat (OpenBoard counts + GitHub open PRs + base CI)
# to <state-dir>/orch.log so Monitor-capable hosts wake every 2m/5m cleanly.
#
# Usage:
#   watchdog.sh start  --config .captain/config.json --state-dir .captain [--interval 120]
#   watchdog.sh status --state-dir .captain
#   watchdog.sh stop   --state-dir .captain

set -euo pipefail

export PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"

ACTION="${1:-}"
shift || true

CONFIG=""
STATE_DIR=""
INTERVAL="120"
REPO=""
CHANNEL=""
BASE_BRANCH="main"

require_arg() {
  if [ "$#" -lt 2 ]; then
    echo "error: missing value for option $1" >&2
    exit 2
  fi
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --config) require_arg "$@"; CONFIG="$2"; shift 2 ;;
    --state-dir) require_arg "$@"; STATE_DIR="$2"; shift 2 ;;
    --interval) require_arg "$@"; INTERVAL="$2"; shift 2 ;;
    --repo) require_arg "$@"; REPO="$2"; shift 2 ;;
    --channel) require_arg "$@"; CHANNEL="$2"; shift 2 ;;
    --base-branch) require_arg "$@"; BASE_BRANCH="$2"; shift 2 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

if [ -z "$STATE_DIR" ]; then
  echo "error: --state-dir is required" >&2
  exit 2
fi

if ! [[ "$INTERVAL" =~ ^[1-9][0-9]*$ ]]; then
  echo "error: --interval must be a positive integer in seconds (got '$INTERVAL'; use 120 instead of 2m)" >&2
  exit 2
fi

mkdir -p "$STATE_DIR"
STATE_DIR="$(cd "$STATE_DIR" && pwd)"

# Extract repo/channel/base_branch from config.json if not overridden (pure jq or awk/sed, zero Python)
if [ -n "$CONFIG" ] && [ -f "$CONFIG" ]; then
  CONFIG="$(cd "$(dirname "$CONFIG")" && pwd)/$(basename "$CONFIG")"
  if command -v jq >/dev/null 2>&1; then
    [ -z "$REPO" ] && REPO="$(jq -r 'if (.repo | type) == "string" then .repo else (.repo.slug // "") end' "$CONFIG" 2>/dev/null || true)"
    [ -z "$CHANNEL" ] && CHANNEL="$(jq -r '.openboard.channel // ""' "$CONFIG" 2>/dev/null || true)"
    cfg_base="$(jq -r '.base_branch // .repo.base_branch // ""' "$CONFIG" 2>/dev/null || true)"
    [ -n "$cfg_base" ] && BASE_BRANCH="$cfg_base"
  fi
fi

PID_FILE="$STATE_DIR/watchdog.pid"
LOG_FILE="$STATE_DIR/orch.log"
LOCK_DIR="$STATE_DIR/watchdog.lock"
TAG="captain-watchdog:$STATE_DIR"

is_running() {
  if [ -f "$PID_FILE" ]; then
    local pid cmdline
    pid="$(cat "$PID_FILE" 2>/dev/null || true)"
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      cmdline="$(ps -p "$pid" -o args= 2>/dev/null || true)"
      if echo "$cmdline" | grep -Fq "$TAG"; then
        return 0
      fi
    fi
  fi
  return 1
}

case "$ACTION" in
  status)
    if is_running; then
      echo "running pid=$(cat "$PID_FILE") log=$LOG_FILE"
      exit 0
    else
      echo "stopped"
      exit 1
    fi
    ;;

  stop)
    if is_running; then
      pid="$(cat "$PID_FILE")"
      kill -TERM "$pid" 2>/dev/null || true
      pkill -TERM -P "$pid" 2>/dev/null || true
      rm -f "$PID_FILE"
      echo "stopped pid=$pid"
    else
      rm -f "$PID_FILE"
      echo "already stopped"
    fi
    ;;

  start)
    if ! mkdir "$LOCK_DIR" 2>/dev/null; then
      echo "error: another watchdog start is in progress ($LOCK_DIR)" >&2
      exit 1
    fi
    trap 'rmdir "$LOCK_DIR" 2>/dev/null || true' EXIT

    if is_running; then
      echo "already running pid=$(cat "$PID_FILE") (refusing duplicate loop)"
      exit 0
    fi

    nohup bash -c '
      set -u
      PID_FILE="$1"
      LOG_FILE="$2"
      INTERVAL="$3"
      REPO="$4"
      CHANNEL="$5"
      BASE_BRANCH="$6"
      STATE_DIR="$7"
      MAX_LOG_BYTES=5242880

      echo "$$" > "$PID_FILE"
      trap "pkill -TERM -P \$\$ 2>/dev/null || true; rm -f \"$PID_FILE\"; exit 0" INT TERM

      while true; do
        if [ -f "$LOG_FILE" ]; then
          sz="$(wc -c < "$LOG_FILE" 2>/dev/null | tr -d " " || echo 0)"
          if [ "${sz:-0}" -ge "$MAX_LOG_BYTES" ]; then
            mv -f "$LOG_FILE" "${LOG_FILE}.1" 2>/dev/null || true
          fi
        fi

        ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
        ob_summary="ob=n/a"
        if [ -n "$CHANNEL" ] && command -v ob >/dev/null 2>&1; then
          raw_ob="$(ob tasks counts "$CHANNEL" --json 2>/dev/null || true)"
          if [ -n "$raw_ob" ] && command -v jq >/dev/null 2>&1; then
            ob_summary="$(printf "%s" "$raw_ob" | jq -r "\"ob[r=\" + ((.counts.ready//0)|tostring) + \" w=\" + ((.counts.waiting//0)|tostring) + \" c=\" + ((.counts.claimed//0)|tostring) + \" b=\" + ((.counts.blocked//0)|tostring) + \" d=\" + ((.counts.completed//0)|tostring) + \"]\"" 2>/dev/null || echo "ob=err")"
          fi
        fi

        pr_summary="prs=n/a"
        base_summary="base=n/a"
        if [ -n "$REPO" ] && command -v gh >/dev/null 2>&1; then
          pr_nums="$(gh pr list --repo "$REPO" --state open --limit 50 --json number -q "[.[].number] | join(\",\")" 2>/dev/null || echo "err")"
          pr_summary="open_prs=[${pr_nums}]"
          base_concl="$(gh run list --repo "$REPO" --branch "$BASE_BRANCH" --limit 1 --json status,conclusion -q ".[0] | (.conclusion // .status // \"none\")" 2>/dev/null || echo "err")"
          base_summary="base(${BASE_BRANCH})=${base_concl}"
        fi

        paused_flag=""
        if [ -f "$STATE_DIR/rules.md" ] && grep -q "PAUSED" "$STATE_DIR/rules.md" 2>/dev/null; then
          paused_flag=" | PAUSED"
        fi

        printf "%s Heartbeat: %s | %s | %s%s\n" "$ts" "$ob_summary" "$pr_summary" "$base_summary" "$paused_flag" >> "$LOG_FILE"

        sleep "$INTERVAL" &
        wait $! || true
      done
    ' "$TAG" "$PID_FILE" "$LOG_FILE" "$INTERVAL" "$REPO" "$CHANNEL" "$BASE_BRANCH" "$STATE_DIR" >/dev/null 2>&1 &

    sleep 1
    if is_running; then
      echo "started pid=$(cat "$PID_FILE") interval=${INTERVAL}s log=$LOG_FILE"
    else
      echo "error: watchdog failed to stay alive; inspect $LOG_FILE" >&2
      exit 1
    fi
    ;;

  *)
    echo "usage: watchdog.sh <start|status|stop> --state-dir <dir> [--config <file>] [--interval 120]" >&2
    exit 2
    ;;
esac
