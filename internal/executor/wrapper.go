package executor

// WrapperScript is deployed to remote resources at ~/.aexp/wrapper.sh.
// Usage: wrapper.sh <run_dir>
// It executes <run_dir>/command.sh and captures exit codes, timestamps, stdout/stderr.
const WrapperScript = `#!/usr/bin/env bash
set -o pipefail

RUN_DIR="$1"

mkdir -p "$RUN_DIR/logs"

echo $$ > "$RUN_DIR/pid"
date +%s > "$RUN_DIR/started_at"
echo "running" > "$RUN_DIR/status"

{
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

} > >(tee -a "$RUN_DIR/logs/stdout.log") \
  2> >(tee -a "$RUN_DIR/logs/stderr.log" >&2)
`
