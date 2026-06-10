package executor

import (
	"crypto/sha256"
	"encoding/hex"
)

// WrapperScript is deployed to remote resources at ~/.aexp/wrapper.sh.
// Usage: wrapper.sh <run_dir>
// It executes <run_dir>/command.sh and captures exit codes, timestamps, stdout/stderr.
const WrapperScript = `#!/usr/bin/env bash
set -o pipefail

RUN_DIR="$1"

mkdir -p "$RUN_DIR/logs"
: > "$RUN_DIR/logs/terminal.log"
STDOUT_PIPE="$RUN_DIR/logs/stdout.pipe.$$"
STDERR_PIPE="$RUN_DIR/logs/stderr.pipe.$$"
rm -f "$STDOUT_PIPE" "$STDERR_PIPE"
mkfifo "$STDOUT_PIPE" "$STDERR_PIPE"
cleanup() {
  rm -f "$STDOUT_PIPE" "$STDERR_PIPE"
}
trap cleanup EXIT

{
  while IFS= read -r line || [ -n "$line" ]; do
    printf '%s\n' "$line" | tee -a "$RUN_DIR/logs/stdout.log"
    printf '[stdout] %s\n' "$line" >> "$RUN_DIR/logs/terminal.log"
  done < "$STDOUT_PIPE"
} &
STDOUT_LOGGER=$!

{
  while IFS= read -r line || [ -n "$line" ]; do
    printf '%s\n' "$line" | tee -a "$RUN_DIR/logs/stderr.log" >&2
    printf '[stderr] %s\n' "$line" >> "$RUN_DIR/logs/terminal.log"
  done < "$STDERR_PIPE"
} &
STDERR_LOGGER=$!

echo $$ > "$RUN_DIR/pid"
date +%s > "$RUN_DIR/started_at"
echo "running" > "$RUN_DIR/status"

(
  echo "[aexp] ========================================"
  echo "[aexp] Run started at $(date)"
  echo "[aexp] Script: $RUN_DIR/command.sh"
  echo "[aexp] PID: $$"
  echo "[aexp] ========================================"
  echo ""

  bash "$RUN_DIR/command.sh"

  EXIT_CODE=$?
  echo ""
  echo "[aexp] ========================================"
  echo "[aexp] Finished at $(date) with exit code $EXIT_CODE"
  echo "[aexp] ========================================"

  echo "$EXIT_CODE" > "$RUN_DIR/exit_code"
  date +%s > "$RUN_DIR/finished_at"

  if [ "$EXIT_CODE" -eq 0 ]; then
    echo "succeeded" > "$RUN_DIR/status"
  else
    echo "failed" > "$RUN_DIR/status"
  fi

  exit "$EXIT_CODE"

) > "$STDOUT_PIPE" 2> "$STDERR_PIPE"
WRAPPER_EXIT=$?
wait "$STDOUT_LOGGER" 2>/dev/null || true
wait "$STDERR_LOGGER" 2>/dev/null || true
exit "$WRAPPER_EXIT"
`

// WrapperHash is the sha256 hex of WrapperScript, used to detect stale wrappers.
var WrapperHash string

func init() {
	h := sha256.Sum256([]byte(WrapperScript))
	WrapperHash = hex.EncodeToString(h[:8])
}
