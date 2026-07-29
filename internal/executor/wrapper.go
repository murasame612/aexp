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
: > "$RUN_DIR/logs/stdout.log"
: > "$RUN_DIR/logs/stderr.log"

echo $$ > "$RUN_DIR/pid"
date +%s > "$RUN_DIR/started_at"
echo "running" > "$RUN_DIR/status"

aexp_prefix_stream() {
  local tag="$1"
  local output="$2"
  if command -v perl >/dev/null 2>&1; then
    perl -e '
      use Fcntl qw(O_WRONLY O_CREAT O_APPEND);
      my ($tag, $path) = @ARGV;
      sysopen(my $out, $path, O_WRONLY | O_CREAT | O_APPEND, 0666) or die $!;
      my ($at_start, $previous_cr) = (1, 0);
      while (1) {
        my $read = sysread(STDIN, my $chunk, 8192);
        die $! unless defined $read;
        last unless $read;
        my $encoded = "";
        for my $char (split //, $chunk) {
          if ($char eq "\n" && $previous_cr) {
            $previous_cr = 0;
            next;
          }
          $encoded .= "[$tag] " if $at_start;
          if ($char eq "\r" || $char eq "\n") {
            $encoded .= "\n";
            $at_start = 1;
            $previous_cr = ($char eq "\r");
          } else {
            $encoded .= $char;
            $at_start = 0;
            $previous_cr = 0;
          }
        }
        for (my $offset = 0; $offset < length($encoded);) {
          my $written = syswrite($out, $encoded, length($encoded) - $offset, $offset);
          die $! unless defined $written;
          $offset += $written;
        }
      }
    ' "$tag" "$output"
  elif command -v python3 >/dev/null 2>&1 || command -v python >/dev/null 2>&1; then
    local python_bin
    python_bin="$(command -v python3 || command -v python)"
    "$python_bin" -u -c '
import os, sys
tag, path = sys.argv[1], sys.argv[2]
fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o666)
at_start, previous_cr = True, False
try:
    while True:
        chunk = os.read(0, 8192)
        if not chunk:
            break
        output = bytearray()
        for char in chunk:
            if char == 10 and previous_cr:
                previous_cr = False
                continue
            if at_start:
                output.extend(("[" + tag + "] ").encode())
            if char in (10, 13):
                output.append(10)
                at_start = True
                previous_cr = char == 13
            else:
                output.append(char)
                at_start = False
                previous_cr = False
        view = memoryview(output)
        while view:
            view = view[os.write(fd, view):]
finally:
    os.close(fd)
' "$tag" "$output"
  else
    awk -v tag="$tag" '{ print "[" tag "] " $0; fflush() }' >> "$output"
  fi
}

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

) > >(tee -a "$RUN_DIR/logs/stdout.log" >(aexp_prefix_stream stdout "$RUN_DIR/logs/terminal.log")) \
  2> >(tee -a "$RUN_DIR/logs/stderr.log" >(aexp_prefix_stream stderr "$RUN_DIR/logs/terminal.log") >&2)
WRAPPER_EXIT=$?
exit "$WRAPPER_EXIT"
`

// WrapperHash is the sha256 hex of WrapperScript, used to detect stale wrappers.
var WrapperHash string

func init() {
	h := sha256.Sum256([]byte(WrapperScript))
	WrapperHash = hex.EncodeToString(h[:8])
}
