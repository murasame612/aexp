# mod-store — 数据层

## 数据库

SQLite via `modernc.org/sqlite`（纯 Go，无 CGO）。

文件位置：`~/.aexp/aexp.db`（默认）或 `--db` 参数。

## 表结构

### resources

```sql
CREATE TABLE resources (
    id          TEXT PRIMARY KEY,       -- rsrc_ + nanoid
    name        TEXT NOT NULL UNIQUE,   -- 人可读名: mu-tslib
    type        TEXT NOT NULL DEFAULT 'ssh',  -- ssh | docker | local | slurm | k8s
    host        TEXT NOT NULL,          -- SSH host
    port        INTEGER NOT NULL DEFAULT 22,
    user        TEXT NOT NULL DEFAULT 'root',
    auth_ref    TEXT DEFAULT '',        -- SSH key path，空则用默认
    root_dir    TEXT NOT NULL,          -- 工作空间根目录
    conda_env   TEXT DEFAULT '',        -- 默认 conda 环境
    gpu_indices TEXT DEFAULT '',        -- 可见 GPU: 0,1,2,3
    tags        TEXT DEFAULT '',        -- 逗号分隔标签
    status      TEXT NOT NULL DEFAULT 'unknown', -- idle|busy|error|unreachable
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### runs

```sql
CREATE TABLE runs (
    id                  TEXT PRIMARY KEY,       -- run_ + nanoid
    resource_id         TEXT NOT NULL REFERENCES resources(id),
    name                TEXT DEFAULT '',        -- 人可读名: ECL-iTransformer-run1
    status              TEXT NOT NULL DEFAULT 'created', -- created|queued|starting|running|succeeded|failed|cancelled|lost
    cwd                 TEXT DEFAULT '',        -- 工作目录
    command             TEXT NOT NULL,          -- 完整命令
    conda_env           TEXT DEFAULT '',        -- 覆盖 resource 默认
    env_json            TEXT DEFAULT '{}',      -- 额外环境变量 JSON
    log_paths_json      TEXT DEFAULT '[]',      -- 日志路径 glob JSON
    artifact_paths_json TEXT DEFAULT '[]',      -- 产物路径 glob JSON
    metric_paths_json   TEXT DEFAULT '[]',      -- 指标路径 glob JSON
    tmux_session        TEXT DEFAULT '',        -- tmux session 名
    remote_run_dir      TEXT DEFAULT '',        -- 远程 run 目录
    exit_code           INTEGER,                -- 退出码
    created_by          TEXT DEFAULT '',        -- agent_thread_id 或 user
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at          DATETIME,
    finished_at         DATETIME
);
```

### resource_snapshots

```sql
CREATE TABLE resource_snapshots (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_id     TEXT NOT NULL REFERENCES resources(id),
    run_id          TEXT DEFAULT '',           -- 关联当前 run（可选）
    timestamp       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    cpu_percent     REAL DEFAULT 0,
    mem_used_mb     REAL DEFAULT 0,
    mem_total_mb    REAL DEFAULT 0,
    gpu_json        TEXT DEFAULT '[]',         -- [{index,name,util,mem_used,mem_total}]
    disk_json       TEXT DEFAULT '{}',         -- {used_gb,total_gb}
    load_1m         REAL DEFAULT 0,
    load_5m         REAL DEFAULT 0,
    load_15m        REAL DEFAULT 0
);

CREATE INDEX idx_snapshots_resource ON resource_snapshots(resource_id, timestamp DESC);
```

### log_lines

```sql
CREATE TABLE log_lines (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      TEXT NOT NULL REFERENCES runs(id),
    source      TEXT NOT NULL DEFAULT 'stdout',  -- stdout | stderr
    line_no     INTEGER NOT NULL,
    content     TEXT NOT NULL,
    timestamp   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_log_run ON log_lines(run_id, source, line_no);
```

### artifacts

```sql
CREATE TABLE artifacts (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL REFERENCES runs(id),
    path        TEXT NOT NULL,            -- 相对于 root_dir
    type        TEXT NOT NULL DEFAULT 'file',  -- file | dir
    size        INTEGER DEFAULT 0,
    modified_at DATETIME
);
```

### agent_events

```sql
CREATE TABLE agent_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      TEXT DEFAULT '',           -- 可选，有些操作不关联 run
    actor       TEXT NOT NULL,             -- agent_thread_id 或 user
    tool_name   TEXT NOT NULL,             -- create_run / stop_run / ...
    input_json  TEXT DEFAULT '{}',
    output_json TEXT DEFAULT '{}',
    timestamp   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_agent_events_run ON agent_events(run_id);
```

## Go Interface

```go
type Store interface {
    // Resources
    CreateResource(ctx context.Context, r *Resource) error
    GetResource(ctx context.Context, id string) (*Resource, error)
    GetResourceByName(ctx context.Context, name string) (*Resource, error)
    ListResources(ctx context.Context) ([]Resource, error)
    UpdateResource(ctx context.Context, r *Resource) error
    DeleteResource(ctx context.Context, id string) error

    // Runs
    CreateRun(ctx context.Context, r *Run) error
    GetRun(ctx context.Context, id string) (*Run, error)
    ListRuns(ctx context.Context, filter RunFilter) ([]Run, error)
    UpdateRun(ctx context.Context, r *Run) error

    // Snapshots
    SaveSnapshot(ctx context.Context, s *Snapshot) error
    GetLatestSnapshot(ctx context.Context, resourceID string) (*Snapshot, error)
    ListSnapshots(ctx context.Context, resourceID string, limit int) ([]Snapshot, error)

    // Logs
    AppendLogLines(ctx context.Context, runID string, lines []LogLine) error
    GetLogLines(ctx context.Context, runID string, source string, offset, limit int) ([]LogLine, error)
    CountLogLines(ctx context.Context, runID string, source string) (int, error)

    // Artifacts
    SaveArtifacts(ctx context.Context, runID string, artifacts []Artifact) error
    ListArtifacts(ctx context.Context, runID string) ([]Artifact, error)

    // Agent Events
    SaveAgentEvent(ctx context.Context, e *AgentEvent) error
    ListAgentEvents(ctx context.Context, runID string) ([]AgentEvent, error)
}
```

## ID 生成

用 `github.com/matoous/go-nanoid/v2` 生成短 URL-friendly ID：

- Resource: `rsrc_` + 8 chars → `rsrc_muTslib`
- Run: `run_` + 8 chars → `run_Yn7pL2wE`

## 迁移

用 `golang-migrate/migrate/v4` + 嵌入 SQL 文件，版本化 schema。
