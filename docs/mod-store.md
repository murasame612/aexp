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

> 本文展示核心字段，不再复制完整建表语句作为迁移权威来源。实际 schema
> 以 `internal/store/schema.sql` 和 `migrateColumns` 为准。

### runs

```sql
CREATE TABLE runs (
    id                  TEXT PRIMARY KEY,       -- run_ + nanoid
    resource_id         TEXT NOT NULL REFERENCES resources(id),
    project_id          TEXT DEFAULT '',
    target_id           TEXT DEFAULT '',
    recipe_name         TEXT DEFAULT '',
    name                TEXT DEFAULT '',        -- 人可读名: ECL-iTransformer-run1
    status              TEXT NOT NULL DEFAULT 'created', -- created|queued|preflighting|starting|running|succeeded|failed|cancelled|lost
    status_source       TEXT DEFAULT 'local_cache',
    status_observed_at  DATETIME,
    status_checked_at   DATETIME,
    status_check_error  TEXT DEFAULT '',
    kind                TEXT NOT NULL DEFAULT 'formal', -- legacy compatibility
    task_role           TEXT NOT NULL DEFAULT 'other',
    evidence_grade      TEXT NOT NULL DEFAULT 'formal',
    experiment_role     TEXT NOT NULL DEFAULT 'unspecified',
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

`run_changes` 由 `runs` 的 insert/update/delete trigger 写入，作为跨进程 SSE 与
增量查询的 durable cursor；`run_launch_jobs` 保存 API 异步提交请求及 queued / launching /
succeeded / failed 状态，使“先显示 Run、后做 SSH 预检”在控制面重启后仍可恢复。

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
    path        TEXT NOT NULL,            -- 远端规范绝对路径
    relative_path TEXT DEFAULT '',        -- 相对于 resolved cwd
    source_uri  TEXT DEFAULT '',
    type        TEXT NOT NULL DEFAULT 'file',  -- file | dir
    role        TEXT DEFAULT '',
    mime        TEXT DEFAULT '',
    size        INTEGER DEFAULT 0,
    sha256      TEXT DEFAULT '',
    collection_state TEXT NOT NULL DEFAULT 'indexed',
    collection_error TEXT DEFAULT '',
    discovered_at DATETIME,
    modified_at DATETIME
);
```

`artifact_paths_json` 是声明，不是 inventory。`artifact_collections` 记录发现流程的
`declared/discovering/indexed/partial/failed` 状态；`run_manifests` 保存 draft→final
的版本化 JSON 和 SHA-256，final 后禁止静默覆盖。

### project_definitions / project_targets

`project_definitions` 保存 repo/recipe 的权威身份；`project_targets` 保存 Project ×
Resource 的 desired execution binding。旧 `project_profiles(resource_id,cwd)` 继续作为
实际环境 observation cache，不能当成 Target，也不能据此猜测历史 Project 归属。

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
