# mod-cli — CLI Interface

## Tool Name

`aexp` — Agent Experiment

## Command Structure

```
aexp <resource> <action> [flags] [args]
```

## Commands

### Server

```bash
aexp serve [--port 8080] [--db ~/.aexp/aexp.db]
```

Start the control plane server.

### Init

```bash
aexp init
```

First-time setup:
- Create `~/.aexp/` directory
- Generate SSH keypair at `~/.aexp/id_ed25519` (if not exists)
- Create empty SQLite database
- Print the public key for user to deploy

### Container

```bash
# Add
aexp container add \
  --name dam-tslib-0 \
  --host 192.168.1.100 \
  --port 22 \
  --user root \
  --workspace /workspace \
  --conda-env tslib \
  --gpu-indices 0 \
  --tags "dam,timeseries,4090"

# List
aexp container list
# Output:
#   NAME            HOST              GPU   STATUS   CPU    MEM      GPU_MEM
#   dam-tslib-0     192.168.1.100     0     idle     23%    45%      2.1/24G
#   szu-exp-0       192.168.1.200     0     busy     78%    67%      18/24G

# Status (detailed)
aexp container status dam-tslib-0

# Test SSH connection
aexp container test dam-tslib-0

# Update
aexp container update dam-tslib-0 --conda-env llm4ts

# Remove
aexp container remove dam-tslib-0
```

### Run

```bash
# Submit
aexp run submit \
  --container dam-tslib-0 \
  --name "ECL-iTransformer-run1" \
  --cwd /workspace/Time-Series-Library \
  --conda-env tslib \
  --log-paths "logs/*.log,results/*.json" \
  -- python train.py --data ECL --model iTransformer --features M
# Output: Submitted run r_Yn7pL2wE on dam-tslib-0

# List
aexp run list [--status running] [--container dam-tslib-0]
# Output:
#   RUN_ID       NAME                 CONTAINER      STATUS     DURATION
#   r_Yn7pL2wE   ECL-iTransformer..   dam-tslib-0    running    12:34
#   r_Km3qPx2w   Weather-Transformer  szu-exp-0      running    05:21
#   r_Px2wN7mk   ILI-36dim-iTrans..   dam-tslib-0    succeeded  45:12

# Status
aexp run status r_Yn7pL2wE

# Logs (tail -f style)
aexp run logs r_Yn7pL2wE
# Ctrl+C to stop following

# Logs (last N lines, then exit)
aexp run logs r_Yn7pL2wE --last 100

# Cancel
aexp run cancel r_Yn7pL2wE
```

### Quick Submit (shorthand)

```bash
# Skip run submit, directly execute on container
aexp exec dam-tslib-0 -- python train.py --data ECL
# This is submit + auto-tail logs in one step
```

## Output Formats

Default: human-readable table.

JSON output for scripting/agents:

```bash
aexp container list --json
aexp run list --json
aexp run status r_Yn7pL2wE --json
```

## Configuration

`~/.aexp/config.yaml`:

```yaml
server:
  port: 8080
  host: 0.0.0.0

database:
  path: ~/.aexp/aexp.db

ssh:
  key: ~/.aexp/id_ed25519
  timeout: 10s

monitor:
  interval: 10s

defaults:
  conda_init: "source /opt/conda/etc/profile.d/conda.sh"
```

## Dependencies

- `github.com/spf13/cobra` — CLI framework
- `github.com/spf13/viper` — config management
- Table output: simple `fmt.Printf` with padding (no external lib)

## Agent-Friendly Design

Every command's `--json` output is parseable.
Status codes are consistent:
- 0: success
- 1: general error
- 2: not found
- 3: connection error

Exit codes + JSON = agent can script any workflow.
