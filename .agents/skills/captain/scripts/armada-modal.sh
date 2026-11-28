#!/usr/bin/env bash
# Zero-Python Armada >= 14.x Modal helper for Captain.
# Absorbs real Modal CLI traps:
#   1. `snapshot new --wait` can return while state=running (phase=secret_scan).
#   2. `snapshot show --build <jobId>` rejects `--org` (invalid_config).
#   3. `armada new --provider modal` fails with 409 if defaultForLaunch MCPs are needs_reconnect.
#   4. `armada exec` returns not_found while a fresh sandbox is still `starting`.
#   5. Modal secrets/variables require Vault + Launch Templates (`armada template launch`).
#
# Usage:
#   armada-modal.sh preflight-mcp --org <ORG>
#   armada-modal.sh wait-build <JOB_ID> [--timeout 600] [--interval 5]
#   armada-modal.sh wait-ready <SANDBOX_ID> --org <ORG> [--timeout 180]
#   armada-modal.sh launch-template <TEMPLATE_ID_OR_NAME> --org <ORG> --request-key <KEY> \
#     (--prompt <TEXT> | --prompt-file <PATH>) [--ttl 4h] [--revision N] [--wait-ready] [--timeout 180]
#   armada-modal.sh launch-raw <SNAPSHOT> --org <ORG> --agent <AGENT> [--model <MODEL>] \
#     [--reasoning-effort <LEVEL>] [--credential-id <UUID>] [--ttl 4h] \
#     [--connections <ID,...>] [--prompt <TEXT> | --prompt-file <PATH>] [--wait-ready] [--timeout 180]

set -euo pipefail

export PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"

die() {
  echo "error: $*" >&2
  exit 2
}

require_jq() {
  command -v jq >/dev/null 2>&1 || die "jq is required (install via brew install jq / apt-get install jq)"
}

require_arg() {
  if [ "$#" -lt 2 ]; then
    die "missing value for option $1"
  fi
}

# Resolve Armada >= 14.x CLI into ARMADA_CMD array.
resolve_armada_cli() {
  if [ -n "${ARMADA_CLI:-}" ]; then
    # shellcheck disable=SC2206
    ARMADA_CMD=( $ARMADA_CLI )
  elif command -v armada >/dev/null 2>&1; then
    local ver major
    ver="$(armada --version 2>/dev/null | head -n 1 | tr -d '[:space:]' || true)"
    major="${ver%%.*}"
    if [[ "$major" =~ ^[0-9]+$ ]] && [ "$major" -ge 14 ]; then
      ARMADA_CMD=( armada )
    else
      ARMADA_CMD=( npx -y @the-vibe-company/armada@latest )
    fi
  else
    ARMADA_CMD=( npx -y @the-vibe-company/armada@latest )
  fi
}

run_armada() {
  "${ARMADA_CMD[@]}" "$@"
}

cmd_preflight_mcp() {
  require_jq
  local org=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --org) require_arg "$@"; org="$2"; shift 2 ;;
      *) die "unknown option for preflight-mcp: $1" ;;
    esac
  done
  [ -n "$org" ] || die "preflight-mcp requires --org <ORG>"

  local body raw
  body="$(jq -cn --arg org "$org" '{org: $org}')"
  raw="$(run_armada api mcp/list "$body")"

  printf "%s\n" "$raw" | jq '{
    total: ((.connections // .items // .) | length),
    default_blocking: [
      (.connections // .items // .)[]
      | select((.defaultForLaunch // false) == true and (.status // "") != "connected")
      | {id, name, status}
    ],
    connected_ids: [
      (.connections // .items // .)[]
      | select((.status // "") == "connected")
      | .id
    ],
    recommended_launch_flag: "--no-connections"
  }'

  local blocking_count
  blocking_count="$(printf "%s\n" "$raw" | jq '[(.connections // .items // .)[] | select((.defaultForLaunch // false) == true and (.status // "") != "connected")] | length')"
  if [ "$blocking_count" -gt 0 ]; then
    echo "warning: $blocking_count defaultForLaunch MCP connection(s) are not connected (would 409 on bare 'armada new'); use Launch Templates with connections:[] or pass --no-connections." >&2
  fi
}

cmd_wait_build() {
  require_jq
  local job_id="" timeout="600" interval="5"
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --timeout) require_arg "$@"; timeout="$2"; shift 2 ;;
      --interval) require_arg "$@"; interval="$2"; shift 2 ;;
      -*) die "unknown option for wait-build: $1" ;;
      *)
        if [ -z "$job_id" ]; then
          job_id="$1"; shift
        else
          die "unexpected argument for wait-build: $1"
        fi
        ;;
    esac
  done
  [ -n "$job_id" ] || die "wait-build requires <JOB_ID>"
  [[ "$timeout" =~ ^[1-9][0-9]*$ ]] || die "--timeout must be a positive integer"
  [[ "$interval" =~ ^[1-9][0-9]*$ ]] || die "--interval must be a positive integer"

  local start_ts now elapsed raw state phase
  start_ts="$(date +%s)"
  while true; do
    # IMPORTANT: `armada snapshot show --build <jobId>` MUST NOT receive `--org` (refused as invalid_config).
    raw="$(run_armada snapshot show --build "$job_id" --json)"
    state="$(printf "%s\n" "$raw" | jq -r '.state // ""')"
    phase="$(printf "%s\n" "$raw" | jq -r '.phase // ""')"

    case "$state" in
      succeeded)
        printf "%s\n" "$raw"
        return 0
        ;;
      failed|recovery_required)
        printf "%s\n" "$raw" >&2
        return 1
        ;;
      queued|running)
        now="$(date +%s)"
        elapsed=$((now - start_ts))
        if [ "$elapsed" -ge "$timeout" ]; then
          echo "error: build $job_id still $state (phase=$phase) after ${timeout}s; inspect later with 'armada snapshot show --build $job_id --json' (do NOT resubmit)" >&2
          printf "%s\n" "$raw"
          return 124
        fi
        echo "build $job_id: state=$state phase=$phase (${elapsed}s/${timeout}s)" >&2
        sleep "$interval"
        ;;
      *)
        echo "error: unexpected build state '$state' for $job_id" >&2
        printf "%s\n" "$raw" >&2
        return 1
        ;;
    esac
  done
}

cmd_wait_ready() {
  require_jq
  local sandbox_id="" org="" timeout="180"
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --org) require_arg "$@"; org="$2"; shift 2 ;;
      --timeout) require_arg "$@"; timeout="$2"; shift 2 ;;
      -*) die "unknown option for wait-ready: $1" ;;
      *)
        if [ -z "$sandbox_id" ]; then
          sandbox_id="$1"; shift
        else
          die "unexpected argument for wait-ready: $1"
        fi
        ;;
    esac
  done
  [ -n "$sandbox_id" ] || die "wait-ready requires <SANDBOX_ID>"
  [ -n "$org" ] || die "wait-ready requires --org <ORG>"
  [[ "$timeout" =~ ^[1-9][0-9]*$ ]] || die "--timeout must be a positive integer"

  local start_ts now elapsed remaining chunk raw status
  start_ts="$(date +%s)"
  while true; do
    now="$(date +%s)"
    elapsed=$((now - start_ts))
    remaining=$((timeout - elapsed))
    if [ "$remaining" -le 0 ]; then
      echo "error: sandbox $sandbox_id still starting after ${timeout}s" >&2
      return 124
    fi
    chunk="$remaining"
    [ "$chunk" -gt 60 ] && chunk=60

    raw="$(run_armada info "$sandbox_id" --wait --timeout "$chunk" --org "$org" --json)"
    status="$(printf "%s\n" "$raw" | jq -r '.sandbox.status // .status // ""')"

    case "$status" in
      starting|queued|creating|provisioning)
        echo "sandbox $sandbox_id: status=$status (${elapsed}s/${timeout}s)" >&2
        sleep 2
        ;;
      failed|stopped|deleted|expired)
        printf "%s\n" "$raw" >&2
        return 1
        ;;
      "")
        echo "error: could not parse sandbox status for $sandbox_id" >&2
        printf "%s\n" "$raw" >&2
        return 1
        ;;
      *)
        printf "%s\n" "$raw"
        return 0
        ;;
    esac
  done
}

cmd_launch_template() {
  require_jq
  local template="" org="" request_key="" prompt="" prompt_file="" ttl="4h" revision="" wait_ready="0" timeout="180"
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --org) require_arg "$@"; org="$2"; shift 2 ;;
      --request-key) require_arg "$@"; request_key="$2"; shift 2 ;;
      --prompt) require_arg "$@"; prompt="$2"; shift 2 ;;
      --prompt-file) require_arg "$@"; prompt_file="$2"; shift 2 ;;
      --ttl) require_arg "$@"; ttl="$2"; shift 2 ;;
      --revision) require_arg "$@"; revision="$2"; shift 2 ;;
      --wait-ready) wait_ready="1"; shift ;;
      --timeout) require_arg "$@"; timeout="$2"; shift 2 ;;
      -*) die "unknown option for launch-template: $1" ;;
      *)
        if [ -z "$template" ]; then
          template="$1"; shift
        else
          die "unexpected argument for launch-template: $1"
        fi
        ;;
    esac
  done
  [ -n "$template" ] || die "launch-template requires <TEMPLATE_ID_OR_NAME>"
  [ -n "$org" ] || die "launch-template requires --org <ORG>"
  [ -n "$request_key" ] || die "launch-template requires --request-key <KEY>"
  if [ -n "$prompt_file" ]; then
    [ -f "$prompt_file" ] || die "prompt file not found: $prompt_file"
    prompt="$(cat "$prompt_file")"
  fi

  local args=( template launch "$template" --request-key "$request_key" --org "$org" --ttl "$ttl" --json )
  [ -n "$revision" ] && args+=( --revision "$revision" )
  [ -n "$prompt" ] && args+=( --prompt "$prompt" )

  local raw sid
  raw="$(run_armada "${args[@]}")"
  sid="$(printf "%s\n" "$raw" | jq -r '.sandbox.id // .id // ""')"

  if [ "$wait_ready" -eq 1 ] && [ -n "$sid" ]; then
    cmd_wait_ready "$sid" --org "$org" --timeout "$timeout" >/dev/null
  fi
  printf "%s\n" "$raw"
}

cmd_launch_raw() {
  require_jq
  local snapshot="" org="" agent="" model="" effort="" cred_id="" ttl="4h" connections="" prompt="" prompt_file="" wait_ready="0" timeout="180"
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --org) require_arg "$@"; org="$2"; shift 2 ;;
      --agent) require_arg "$@"; agent="$2"; shift 2 ;;
      --model) require_arg "$@"; model="$2"; shift 2 ;;
      --reasoning-effort) require_arg "$@"; effort="$2"; shift 2 ;;
      --credential-id) require_arg "$@"; cred_id="$2"; shift 2 ;;
      --ttl) require_arg "$@"; ttl="$2"; shift 2 ;;
      --connections) require_arg "$@"; connections="$2"; shift 2 ;;
      --prompt) require_arg "$@"; prompt="$2"; shift 2 ;;
      --prompt-file) require_arg "$@"; prompt_file="$2"; shift 2 ;;
      --wait-ready) wait_ready="1"; shift ;;
      --timeout) require_arg "$@"; timeout="$2"; shift 2 ;;
      -*) die "unknown option for launch-raw: $1" ;;
      *)
        if [ -z "$snapshot" ]; then
          snapshot="$1"; shift
        else
          die "unexpected argument for launch-raw: $1"
        fi
        ;;
    esac
  done
  [ -n "$snapshot" ] || die "launch-raw requires <SNAPSHOT>"
  [ -n "$org" ] || die "launch-raw requires --org <ORG>"
  [ -n "$agent" ] || die "launch-raw requires --agent <AGENT>"
  if [ -n "$prompt_file" ]; then
    [ -f "$prompt_file" ] || die "prompt file not found: $prompt_file"
    prompt="$(cat "$prompt_file")"
  fi

  local args=( new "$snapshot" --provider modal --agent "$agent" --org "$org" --ttl "$ttl" --json )
  [ -n "$model" ] && args+=( --model "$model" )
  [ -n "$effort" ] && args+=( --reasoning-effort "$effort" )
  [ -n "$cred_id" ] && args+=( --credential-id "$cred_id" )
  # Default to --no-connections so defaultForLaunch MCPs in needs_reconnect never cause 409 conflicts.
  if [ -n "$connections" ]; then
    args+=( --connections "$connections" )
  else
    args+=( --no-connections )
  fi
  [ -n "$prompt" ] && args+=( "$prompt" )

  local raw sid
  raw="$(run_armada "${args[@]}")"
  sid="$(printf "%s\n" "$raw" | jq -r '.sandbox.id // .id // ""')"

  if [ "$wait_ready" -eq 1 ] && [ -n "$sid" ]; then
    cmd_wait_ready "$sid" --org "$org" --timeout "$timeout" >/dev/null
  fi
  printf "%s\n" "$raw"
}

ACTION="${1:-}"
shift || true
resolve_armada_cli

case "$ACTION" in
  preflight-mcp) cmd_preflight_mcp "$@" ;;
  wait-build) cmd_wait_build "$@" ;;
  wait-ready) cmd_wait_ready "$@" ;;
  launch-template) cmd_launch_template "$@" ;;
  launch-raw) cmd_launch_raw "$@" ;;
  *)
    echo "usage: armada-modal.sh <preflight-mcp|wait-build|wait-ready|launch-template|launch-raw> [options]" >&2
    exit 2
    ;;
esac
