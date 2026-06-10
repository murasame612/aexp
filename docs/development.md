# Development Notes

This document keeps the implementation-oriented material out of the public
README. The root README should help a new user understand and use `aexp`; this
file is for contributors who want to understand how the pieces fit together.

## Architecture

`aexp` runs on the local control machine. It talks to registered resources over
SSH and stores control-plane state in local SQLite.

```text
Agent / CLI / Web UI
        |
        v
  aexp server
  - REST API
  - WebSocket logs
  - embedded dashboard
        |
        v
  SSH executor
  - connection pool
  - one-shot exec
  - tmux-backed runs
        |
        v
  Remote resources
  - bash
  - tmux
  - experiment runtime
```

## Core Concepts

| Concept | Description |
|---|---|
| Resource | A compute target, currently implemented for SSH resources. |
| Run | A tracked execution bound to a resource and remote run directory. |
| Snapshot | Point-in-time resource telemetry such as CPU, memory, and GPU state. |
| Agent event | Audit entry for agent or CLI actions. |
| Run mark | Human or agent interpretation attached to a run after inspecting evidence. |

See [concepts.md](concepts.md) for the user-facing definitions.

## Module Map

| Path | Responsibility |
|---|---|
| `cmd/aexp/` | Cobra CLI, project config commands, sync commands, event helpers. |
| `internal/api/` | HTTP API, WebSocket routes, embedded dashboard. |
| `internal/executor/` | SSH pool, one-shot exec, project detection, tmux run lifecycle, wrapper script. |
| `internal/explore/` | Remote environment discovery. |
| `internal/monitor/` | Resource telemetry polling. |
| `internal/store/` | SQLite schema, migrations, repository methods. |
| `scripts/` | Smoke-test helpers. |
| `examples/` | Minimal project examples. |

## Run Lifecycle

Long-running commands go through `aexp run submit`:

1. Validate resource, cwd, command, run kind, and GPU slot.
2. Optionally detect the project runtime profile.
3. Create a local SQLite run record.
4. Deploy or update the remote wrapper script.
5. Create `<root-dir>/.aexp/runs/<run_id>/`.
6. Write `command.sh`.
7. Launch `tmux new-session -d -s aexp_<run_id>`.
8. Update status, timestamps, logs, and agent events.

The remote wrapper writes:

```text
logs/stdout.log
logs/stderr.log
logs/terminal.log
status
exit_code
started_at
finished_at
```

Status refresh checks the remote status files and tmux session rather than
guessing from SSH connection state.

## User-Facing Docs Policy

Keep the root README focused on:

- what problem `aexp` solves
- installation
- quick start
- common workflows
- screenshots
- safety boundaries
- links to deeper docs

Avoid putting module internals, roadmap checklists, schema details, or
implementation order in the root README. Put those in `docs/`.

## Testing

```bash
go test ./...
go build -o aexp ./cmd/aexp
```

For remote smoke tests, see [testing.md](testing.md). Smoke tests are only
connectivity and workflow checks; never report them as formal experiment
results.

## Current Scope

Implemented:

- SSH resource registration
- one-shot remote exec
- tmux-backed run submission
- live logs
- resource monitoring
- embedded Web dashboard
- SQLite persistence
- project config, project doctor, and rsync-based sync helpers
- structured UI events for progress and metrics
- MCP stdio server wrapping the local aexp binary

Not yet the main path:

- automatic container creation
- Slurm or Kubernetes backends
- multi-user authentication and authorization
- MLflow or external tracker synchronization
