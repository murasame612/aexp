# mod-executor — Experiment Execution

## How Experiments Run

`aexp` creates a **tmux session** inside the container, runs the command inside it,
and monitors the session. This means:

- If SSH disconnects, the experiment keeps running
- `aexp` can reconnect to the tmux session to tail logs
- The tmux session name is deterministic: `aexp_<run_id>`

## Run Lifecycle

```
submit
  |
  v
[pending] -- SSH connect + create tmux session --> [running]
  |                                                  |
  |                                            tmux session exits
  |                                                  |
  |                                            exit code == 0 --> [succeeded]
  |                                            exit code != 0 --> [failed]
  |                                                  |
  v                                            user cancels
[cancelled] <--- kill tmux session ---------------+
```

## Submit Flow

```go
// executor/executor.go
type Executor struct {
    pool   *SSHPool
    store  Store
}

func (e *Executor) Submit(ctx context.Context, req SubmitRequest) (*Run, error) {
    // 1. Validate container exists and is reachable
    // 2. Create Run record in DB with status=pending
    // 3. SSH into container
    // 4. Build tmux command:
    //    tmux new-session -d -s aexp_<run_id> \
    //      "cd <cwd> && source /opt/conda/etc/profile.d/conda.sh && \
    //       conda activate <env> && <command> 2>&1 | tee <log_file>"
    // 5. Execute via SSH
    // 6. Get PID of the process inside tmux
    // 7. Update Run record: status=running, pid=X, started_at=now
    // 8. Return Run
}
```

## The tmux Command Template

```bash
tmux new-session -d -s 'aexp_${RUN_ID}' \
  'bash -c "
    set -e
    source /opt/conda/etc/profile.d/conda.sh 2>/dev/null || true
    conda activate ${CONDA_ENV} 2>/dev/null || true
    cd ${CWD}
    echo \"[aexp] Run ${RUN_ID} started at $(date)\"
    echo \"[aexp] Command: ${COMMAND}\"
    echo \"[aexp] Container: ${CONTAINER_NAME}\"
    echo \"[aexp] ================================\"
    ${COMMAND}
    EXIT_CODE=\$?
    echo \"[aexp] ================================\"
    echo \"[aexp] Finished at $(date) with exit code \$EXIT_CODE\"
    echo \$EXIT_CODE > /tmp/aexp_${RUN_ID}.exit
  " 2>&1 | tee ${LOG_FILE}'
```

- `LOG_FILE` defaults to `${WORKSPACE}/.aexp/logs/${RUN_ID}.log`
- Exit code is written to a temp file so `aexp` can read it after tmux session ends

## Log Tailing

Two strategies:

### Strategy 1: SSH exec + tail -f (MVP)

```go
func (e *Executor) TailLogs(ctx context.Context, runID string, lastN int) (<-chan LogLine, error) {
    // 1. SSH into container
    // 2. Run: tail -f -n <lastN> <log_file>
    // 3. Stream lines back via channel
    // 4. Cancel when ctx is done
}
```

### Strategy 2: tmux capture-pane (fallback)

If the command doesn't tee to a file:

```bash
tmux capture-pane -t aexp_${RUN_ID} -p -S -200
```

This captures the last 200 lines of the tmux pane buffer.

## Cancel Flow

```go
func (e *Executor) Cancel(ctx context.Context, runID string) error {
    // 1. SSH into container
    // 2. tmux send-keys -t aexp_<run_id> C-c
    // 3. Wait up to 5s for tmux session to end
    // 4. If still running: tmux kill-session -t aexp_<run_id>
    // 5. Update Run: status=cancelled, ended_at=now
}
```

## Status Check (is run still alive?)

```go
func (e *Executor) CheckStatus(ctx context.Context, run *Run) (RunStatus, error) {
    // 1. SSH into container
    // 2. tmux has-session -t aexp_<run_id>
    //    - if no: read /tmp/aexp_<run_id>.exit for exit code
    //    - if yes: still running
    // 3. Also check: is the PID still alive? (ps -p <pid>)
}
```

## Log File Management

Each run gets a dedicated log file:

```
<workspace>/.aexp/logs/<run_id>.log
```

The `.aexp/` directory is created inside the container's workspace on first use.

Log paths specified by the user (e.g. `logs/*.log`) are also tracked as artifacts.
After a run completes, `aexp` collects these files.

## Run Record in DB

After submit, the run record looks like:

```json
{
  "id": "r_Yn7pL2wE",
  "container_id": "c_Ab3xK9mQ",
  "name": "ECL-iTransformer-run1",
  "command": "python train.py --data ECL --model iTransformer",
  "cwd": "/workspace/Time-Series-Library",
  "conda_env": "tslib",
  "log_paths": "logs/*.log,results/*.json",
  "tmux_session": "aexp_r_Yn7pL2wE",
  "status": "running",
  "pid": 12345,
  "started_at": "2026-06-07T14:30:00Z"
}
```
