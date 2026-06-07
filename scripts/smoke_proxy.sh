#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
USER_HOME="${HOME}"

AEXP_HOST="${AEXP_HOST:-a20049811505541120898398}"
AEXP_USER="${AEXP_USER:-root}"
AEXP_PORT="${AEXP_PORT:-22}"
AEXP_REMOTE_ROOT="${AEXP_REMOTE_ROOT:-/root}"
AEXP_GPU_INDEX="${AEXP_GPU_INDEX:-0}"
AEXP_KEY="${AEXP_KEY:-${USER_HOME}/.ssh/id_ed25519}"
AEXP_PROXY_COMMAND="${AEXP_PROXY_COMMAND:-nc -X 5 -x member.aicloud.szu.edu.cn:30027 %h %p}"
AEXP_BIN="${AEXP_BIN:-/tmp/aexp-smoke-bin}"
AEXP_TMP_HOME="${AEXP_TMP_HOME:-$(mktemp -d /tmp/aexp-smoke-home-XXXXXX)}"
AEXP_RESOURCE="smoke-a200-$(date +%H%M%S)"

log() {
  printf '\n[%s] %s\n' "$(date +%H:%M:%S)" "$*"
}

require() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

run_status_field() {
  local run_id="$1"
  HOME="$AEXP_TMP_HOME" "$AEXP_BIN" run status "$run_id" | tee "/tmp/aexp-smoke-status-${run_id}.txt"
}

run_logs() {
  local run_id="$1"
  HOME="$AEXP_TMP_HOME" "$AEXP_BIN" run logs "$run_id" --last 120 | tee "/tmp/aexp-smoke-logs-${run_id}.txt"
}

submit_run() {
  HOME="$AEXP_TMP_HOME" "$AEXP_BIN" run submit "$@"
}

extract_run_id() {
  awk '/Submitted run/{print $3}'
}

require go
require ssh
require nc

mkdir -p "$AEXP_TMP_HOME/.aexp"

log "building aexp -> $AEXP_BIN"
(cd "$ROOT_DIR" && go build -o "$AEXP_BIN" ./cmd/aexp)

log "preflight ssh via ProxyCommand"
ssh \
  -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile=/dev/null \
  -o ConnectTimeout=20 \
  -o ProxyCommand="$AEXP_PROXY_COMMAND" \
  -i "$AEXP_KEY" \
  "${AEXP_USER}@${AEXP_HOST}" \
  'echo AEXP_SSH_OK && hostname && command -v tmux && nvidia-smi -L 2>/dev/null || true'

log "registering resource $AEXP_RESOURCE"
HOME="$AEXP_TMP_HOME" "$AEXP_BIN" resource add \
  --name "$AEXP_RESOURCE" \
  --host "$AEXP_HOST" \
  --port "$AEXP_PORT" \
  --user "$AEXP_USER" \
  --root-dir "$AEXP_REMOTE_ROOT" \
  --gpu-indices "$AEXP_GPU_INDEX" \
  --auth-ref "$AEXP_KEY" \
  --proxy-command "$AEXP_PROXY_COMMAND"

log "argv-mode smoke"
ARGV_OUT="$(submit_run \
  --resource "$AEXP_RESOURCE" \
  --name argv-smoke \
  --kind smoke \
  --gpu-index "$AEXP_GPU_INDEX" \
  --cwd "$AEXP_REMOTE_ROOT" \
  -- printf 'ARGV_OK=<%s>\n' "space quote ' semi; safe")"
echo "$ARGV_OUT"
ARGV_RUN_ID="$(printf '%s\n' "$ARGV_OUT" | extract_run_id)"
sleep 1
run_status_field "$ARGV_RUN_ID"
ARGV_LOGS="$(run_logs "$ARGV_RUN_ID")"
grep -F "ARGV_OK=<space quote ' semi; safe>" <<<"$ARGV_LOGS" >/dev/null

log "shell-mode smoke"
SHELL_OUT="$(submit_run \
  --resource "$AEXP_RESOURCE" \
  --name shell-smoke \
  --kind smoke \
  --gpu-index "$AEXP_GPU_INDEX" \
  --cwd "$AEXP_REMOTE_ROOT" \
  --shell -- 'echo SHELL_OK; echo CUDA=$CUDA_VISIBLE_DEVICES')"
echo "$SHELL_OUT"
SHELL_RUN_ID="$(printf '%s\n' "$SHELL_OUT" | extract_run_id)"
sleep 1
run_status_field "$SHELL_RUN_ID"
SHELL_LOGS="$(run_logs "$SHELL_RUN_ID")"
grep -F "SHELL_OK" <<<"$SHELL_LOGS" >/dev/null
grep -F "CUDA=$AEXP_GPU_INDEX" <<<"$SHELL_LOGS" >/dev/null

log "cancel smoke"
CANCEL_OUT="$(submit_run \
  --resource "$AEXP_RESOURCE" \
  --name cancel-smoke \
  --kind smoke \
  --gpu-index "$AEXP_GPU_INDEX" \
  --cwd "$AEXP_REMOTE_ROOT" \
  --shell -- 'echo CANCEL_START; sleep 60; echo SHOULD_NOT_PRINT')"
echo "$CANCEL_OUT"
CANCEL_RUN_ID="$(printf '%s\n' "$CANCEL_OUT" | extract_run_id)"
sleep 1
HOME="$AEXP_TMP_HOME" "$AEXP_BIN" run cancel "$CANCEL_RUN_ID"
run_status_field "$CANCEL_RUN_ID"
CANCEL_LOGS="$(run_logs "$CANCEL_RUN_ID")"
grep -F "CANCEL_START" <<<"$CANCEL_LOGS" >/dev/null
if grep -F "SHOULD_NOT_PRINT" <<<"$CANCEL_LOGS" >/dev/null; then
  echo "cancel smoke failed: command continued after cancel" >&2
  exit 1
fi

log "agent event audit"
sqlite3 "$AEXP_TMP_HOME/.aexp/aexp.db" \
  "select run_id, tool_name, output_json from agent_events order by id;"

log "remote wrapper version"
ssh \
  -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile=/dev/null \
  -o ProxyCommand="$AEXP_PROXY_COMMAND" \
  -i "$AEXP_KEY" \
  "${AEXP_USER}@${AEXP_HOST}" \
  'cat ~/.aexp/wrapper.version; echo'

cat <<EOF

SMOKE OK
tmp_home: $AEXP_TMP_HOME
resource: $AEXP_RESOURCE
argv_run: $ARGV_RUN_ID
shell_run: $SHELL_RUN_ID
cancel_run: $CANCEL_RUN_ID
EOF
