# mod-store — Data Layer

## Database

SQLite via `modernc.org/sqlite` (pure Go, no CGO).

File location: `~/.aexp/aexp.db` (default) or `--db` flag.

## Tables

### containers

```sql
CREATE TABLE containers (
    id          TEXT PRIMARY KEY,     -- nanoid or uuid
    name        TEXT NOT NULL UNIQUE, -- human name: dam-tslib-0
    host        TEXT NOT NULL,        -- SSH host
    port        INTEGER NOT NULL DEFAULT 22,
    user        TEXT NOT NULL DEFAULT 'root',
    workspace   TEXT NOT NULL,        -- base workspace dir
    conda_env   TEXT DEFAULT '',      -- default conda env name
    tags        TEXT DEFAULT '',      -- comma-separated
    gpu_indices TEXT DEFAULT '',      -- comma-separated: 0,1,2,3
    status      TEXT NOT NULL DEFAULT 'unknown', -- unknown/idle/busy/error
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### runs

```sql
CREATE TABLE runs (
    id              TEXT PRIMARY KEY,     -- nanoid
    container_id    TEXT NOT NULL REFERENCES containers(id),
    name            TEXT DEFAULT '',      -- human label: ECL-iTransformer-run1
    command         TEXT NOT NULL,        -- the full command
    cwd             TEXT DEFAULT '',      -- working dir (relative to workspace or absolute)
    conda_env       TEXT DEFAULT '',      -- override container default
    log_paths       TEXT DEFAULT '',      -- comma-separated globs
    tmux_session    TEXT DEFAULT '',      -- tmux session name
    status          TEXT NOT NULL DEFAULT 'pending', -- pending/running/succeeded/failed/cancelled
    pid             INTEGER DEFAULT 0,
    exit_code       INTEGER DEFAULT 0,
    started_at      DATETIME,
    ended_at        DATETIME,
    notes           TEXT DEFAULT '',      -- agent notes / reason
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### resource_snapshots

```sql
CREATE TABLE resource_snapshots (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    container_id    TEXT NOT NULL REFERENCES containers(id),
    timestamp       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    cpu_percent     REAL DEFAULT 0,
    mem_used_mb     REAL DEFAULT 0,
    mem_total_mb    REAL DEFAULT 0,
    gpu_util        TEXT DEFAULT '',      -- JSON: [{"index":0,"util":45,"mem_used":8000,"mem_total":24000}]
    load_1m         REAL DEFAULT 0,
    load_5m         REAL DEFAULT 0,
    load_15m        REAL DEFAULT 0
);

CREATE INDEX idx_snapshots_container ON resource_snapshots(container_id, timestamp DESC);
```

### log_lines (optional cache)

```sql
CREATE TABLE log_lines (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      TEXT NOT NULL REFERENCES runs(id),
    stream      TEXT NOT NULL DEFAULT 'stdout', -- stdout/stderr
    line_num    INTEGER NOT NULL,
    content     TEXT NOT NULL,
    timestamp   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_log_run ON log_lines(run_id, line_num);
```

> Log lines may also be stored as files on disk (`~/.aexp/logs/<run_id>.log`).
> The DB cache is optional — for search / recent-lines display.

## Go Interface

```go
// store/store.go
type Store interface {
    // Containers
    CreateContainer(ctx context.Context, c *Container) error
    GetContainer(ctx context.Context, id string) (*Container, error)
    GetContainerByName(ctx context.Context, name string) (*Container, error)
    ListContainers(ctx context.Context) ([]Container, error)
    UpdateContainer(ctx context.Context, c *Container) error
    DeleteContainer(ctx context.Context, id string) error

    // Runs
    CreateRun(ctx context.Context, r *Run) error
    GetRun(ctx context.Context, id string) (*Run, error)
    ListRuns(ctx context.Context, filter RunFilter) ([]Run, error)
    UpdateRun(ctx context.Context, r *Run) error

    // Resources
    SaveSnapshot(ctx context.Context, s *ResourceSnapshot) error
    GetLatestSnapshot(ctx context.Context, containerID string) (*ResourceSnapshot, error)

    // Logs (optional)
    AppendLogLines(ctx context.Context, runID string, lines []LogLine) error
    GetLogLines(ctx context.Context, runID string, offset, limit int) ([]LogLine, error)
    CountLogLines(ctx context.Context, runID string) (int, error)
}
```

## ID Generation

Use `github.com/matoous/go-nanoid/v2` for short, URL-friendly IDs:

- Container ID: `c_` prefix + 8 chars → `c_Ab3xK9mQ`
- Run ID: `r_` prefix + 8 chars → `r_Yn7pL2wE`

Human-readable, easy to type in CLI.

## Migrations

Use `github.com/golang-migrate/migrate/v4` with embedded SQL files.
Version the schema so upgrades are safe.
