# aexp — Agent Experiment Control Plane

## What is this?

A lightweight middleware between AI research agents and GPU compute containers.
Agent no longer SSHes into containers and runs commands blindly. Instead it goes through `aexp`:

```
Agent (MCP/CLI)
  -> aexp Control Plane (Go)
    -> SSH into container
      -> tmux session / nohup
        -> experiment runs
      -> log tail + resource monitor
    -> Web Dashboard (real-time)
```

## Why not just SSH?

| Problem | SSH alone | aexp |
|---|---|---|
| Agent loses track of which container is doing what | Yes | No — all runs registered |
| No structured log access | tail manually | auto-tail, websocket stream |
| Can't see GPU/memory from outside | nvidia-smi inside only | centralized resource view |
| Run history lost | yes | SQLite persisted |
| Agent can't resume after context window | yes | run_id is the handle |
| No reproducibility | command buried in shell history | command + env + cwd recorded |

## Architecture Overview

```
┌─────────────────────────────────────────────────────┐
│                    aexp server                       │
│                                                     │
│  ┌──────────┐  ┌──────────┐  ┌──────────────────┐  │
│  │ REST API │  │ WebSocket│  │  Static Web UI    │  │
│  │ /api/v1/*│  │ /ws/*    │  │  /                │  │
│  └────┬─────┘  └────┬─────┘  └──────────────────┘  │
│       │              │                               │
│  ┌────┴──────────────┴───────┐                       │
│  │       Run Controller      │                       │
│  │  submit / cancel / tail   │                       │
│  └────────────┬──────────────┘                       │
│               │                                      │
│  ┌────────────┴──────────────┐  ┌────────────────┐  │
│  │     SSH Executor          │  │  Resource       │  │
│  │  connect / exec / tmux    │  │  Poller         │  │
│  └────────────┬──────────────┘  │  cpu/gpu/mem    │  │
│               │                 └────────────────┘  │
│  ┌────────────┴──────────────┐                      │
│  │     Store (SQLite)        │                      │
│  │  containers / runs / logs │                      │
│  └───────────────────────────┘                      │
└──────────────────────┬──────────────────────────────┘
                       │ SSH
          ┌────────────┼────────────┐
          ▼            ▼            ▼
    ┌──────────┐ ┌──────────┐ ┌──────────┐
    │Container1│ │Container2│ │Container3│
    │ mu:dam-  │ │ szu:a200-│ │ mu:llm-  │
    │ tslib-0  │ │ exp-0    │ │ 4ts-0    │
    └──────────┘ └──────────┘ └──────────┘
```

## Core Concepts

### Container
A registered compute environment. Not necessarily a Docker container — can be any SSH-accessible host with a workspace.
```
id, name, host, port, user, workspace_dir,
conda_env, tags, gpu_indices, status
```

### Run
A single experiment execution inside a container.
```
id, container_id, command, cwd, conda_env,
log_paths, status, pid, tmux_session,
started_at, ended_at, exit_code, notes
```

### Resource Snapshot
Periodic CPU/GPU/memory stats for each container.
```
container_id, timestamp,
cpu_percent, mem_used_mb, mem_total_mb,
gpu_util_percent, gpu_mem_used_mb, gpu_mem_total_mb
```

## MVP Scope (Phase 1)

Only what's needed to replace "SSH in and run things manually":

- [ ] `aexp container add` — register a container
- [ ] `aexp container list` — show all containers + resource status
- [ ] `aexp container status <name>` — detailed resource view
- [ ] `aexp run submit <container> -- <command>` — run experiment via tmux
- [ ] `aexp run list` — show all runs across containers
- [ ] `aexp run logs <run_id>` — tail logs in terminal
- [ ] `aexp run cancel <run_id>` — kill run
- [ ] Web dashboard — runs list, single run detail, live log tail, resource gauges
- [ ] Resource polling — CPU/GPU/memory every 10s

**NOT in MVP**: MCP server, auto container creation, MLflow integration, scheduling, multi-user auth.

## CLI Design

```bash
# Container management
aexp container add \
  --name dam-tslib-0 \
  --host mu \
  --port 22 \
  --user root \
  --workspace /workspace \
  --conda-env tslib \
  --tags "dam,timeseries,4090"

aexp container list
aexp container status dam-tslib-0
aexp container remove dam-tslib-0

# Run experiments
aexp run submit \
  --container dam-tslib-0 \
  --cwd /workspace/Time-Series-Library \
  --log-paths "logs/*.log,results/*.json" \
  --name "ECL-iTransformer-run1" \
  -- python train.py --data ECL --model iTransformer

aexp run list
aexp run list --container dam-tslib-0
aexp run status <run_id>
aexp run logs <run_id>             # tail -f style
aexp run logs <run_id> --last 200  # last N lines
aexp run cancel <run_id>

# Server
aexp serve --port 8080 --db ./data/aexp.db
```

## Tech Stack

| Component | Choice | Why |
|---|---|---|
| Language | Go 1.22+ | SSH client, concurrency, single binary |
| HTTP | chi or net/http | lightweight, stdlib-first |
| WebSocket | gorilla/websocket | mature, log streaming |
| Database | SQLite (modernc.org/sqlite) | zero-deploy, single file |
| SSH | golang.org/x/crypto/ssh | standard Go SSH client |
| CLI | cobra | standard Go CLI framework |
| Web UI | vanilla HTML/JS + Tailwind | no build step, embed in binary |
| TUI (optional) | bubbletea | fancy terminal UI for `aexp tui` |

## File Structure

```
aexp/
├── docs/                    # this directory
│   ├── blueprint.md
│   ├── mod-*.md
│   └── ...
├── cmd/
│   └── aexp/
│       └── main.go
├── internal/
│   ├── api/                 # HTTP handlers
│   ├── container/           # container management
│   ├── executor/            # SSH + tmux execution
│   ├── monitor/             # resource polling
│   ├── run/                 # run lifecycle
│   └── store/               # SQLite persistence
├── web/                     # embedded web UI
│   └── index.html
├── go.mod
└── go.sum
```

## Next Steps

1. Read all `mod-*.md` docs for detailed design of each module
2. Start with `store` (data layer)
3. Then `container` (register + SSH connect)
4. Then `executor` (tmux run)
5. Then `api` (HTTP + WebSocket)
6. Then `web` (dashboard)
7. Then `monitor` (resource polling)
8. Finally wire `cmd/aexp/main.go`
