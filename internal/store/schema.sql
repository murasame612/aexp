CREATE TABLE IF NOT EXISTS resources (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    type          TEXT NOT NULL DEFAULT 'ssh',
    host          TEXT NOT NULL,
    os_type       TEXT DEFAULT '',
    port          INTEGER NOT NULL DEFAULT 22,
    user          TEXT NOT NULL DEFAULT 'root',
    auth_ref      TEXT DEFAULT '',
    socks_proxy   TEXT DEFAULT '',
    proxy_command TEXT DEFAULT '',
    root_dir      TEXT NOT NULL,
    conda_base    TEXT DEFAULT '',
    conda_init    TEXT DEFAULT '',
    conda_env     TEXT DEFAULT '',
    gpu_indices   TEXT DEFAULT '',
    tags          TEXT DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'unknown',
    ssh_status    TEXT NOT NULL DEFAULT 'unknown',
    last_doctor_error TEXT DEFAULT '',
    last_checked_at DATETIME,
    last_success_at DATETIME,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS runs (
    id                  TEXT PRIMARY KEY,
    resource_id         TEXT NOT NULL REFERENCES resources(id),
    name                TEXT DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'created',
    kind                TEXT NOT NULL DEFAULT 'formal',
    gpu_index           INTEGER NOT NULL DEFAULT -1,
    cwd                 TEXT DEFAULT '',
    command             TEXT NOT NULL,
    program             TEXT DEFAULT '',
    args_json           TEXT DEFAULT '[]',
    conda_env           TEXT DEFAULT '',
    project_env         TEXT DEFAULT '',
    resolved_env        TEXT DEFAULT '',
    resolved_python     TEXT DEFAULT '',
    resolved_cwd        TEXT DEFAULT '',
    env_json            TEXT DEFAULT '{}',
    log_paths_json      TEXT DEFAULT '[]',
    artifact_paths_json TEXT DEFAULT '[]',
    metric_paths_json   TEXT DEFAULT '[]',
    ui_events_path      TEXT DEFAULT '',
    tmux_session        TEXT DEFAULT '',
    remote_run_dir      TEXT DEFAULT '',
    exit_code           INTEGER,
    created_by          TEXT DEFAULT '',
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at          DATETIME,
    finished_at         DATETIME,
    archived_at         DATETIME,
    deleted_at          DATETIME
);

CREATE TABLE IF NOT EXISTS project_profiles (
    resource_id       TEXT NOT NULL REFERENCES resources(id),
    resource_name     TEXT DEFAULT '',
    cwd               TEXT NOT NULL,
    env_strategy      TEXT DEFAULT 'auto',
    resolved_env      TEXT DEFAULT '',
    env_name          TEXT DEFAULT '',
    python            TEXT DEFAULT '',
    resolved_cwd      TEXT DEFAULT '',
    command_prefix    TEXT DEFAULT '',
    python_ok         INTEGER NOT NULL DEFAULT 0,
    torch_ok          INTEGER NOT NULL DEFAULT 0,
    cuda              TEXT DEFAULT 'unknown',
    cuda_ok           INTEGER NOT NULL DEFAULT 0,
    entrypoints_json  TEXT DEFAULT '[]',
    metrics_json      TEXT DEFAULT '[]',
    logs_json         TEXT DEFAULT '[]',
    warnings_json     TEXT DEFAULT '[]',
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (resource_id, cwd)
);

CREATE TABLE IF NOT EXISTS resource_snapshots (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_id     TEXT NOT NULL REFERENCES resources(id),
    run_id          TEXT DEFAULT '',
    timestamp       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    cpu_percent     REAL DEFAULT 0,
    mem_used_mb     REAL DEFAULT 0,
    mem_total_mb    REAL DEFAULT 0,
    gpu_json        TEXT DEFAULT '[]',
    disk_json       TEXT DEFAULT '{}',
    load_1m         REAL DEFAULT 0,
    load_5m         REAL DEFAULT 0,
    load_15m        REAL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_snapshots_resource ON resource_snapshots(resource_id, timestamp DESC);

CREATE TABLE IF NOT EXISTS log_lines (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      TEXT NOT NULL REFERENCES runs(id),
    source      TEXT NOT NULL DEFAULT 'stdout',
    line_no     INTEGER NOT NULL,
    content     TEXT NOT NULL,
    timestamp   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_log_run ON log_lines(run_id, source, line_no);

CREATE TABLE IF NOT EXISTS artifacts (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL REFERENCES runs(id),
    path        TEXT NOT NULL,
    type        TEXT NOT NULL DEFAULT 'file',
    size        INTEGER DEFAULT 0,
    modified_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_artifacts_run ON artifacts(run_id);

CREATE TABLE IF NOT EXISTS agent_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      TEXT DEFAULT '',
    actor       TEXT NOT NULL,
    tool_name   TEXT NOT NULL,
    input_json  TEXT DEFAULT '{}',
    output_json TEXT DEFAULT '{}',
    timestamp   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_agent_events_run ON agent_events(run_id);

CREATE TABLE IF NOT EXISTS run_marks (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL REFERENCES runs(id),
    actor       TEXT NOT NULL,
    kind        TEXT NOT NULL DEFAULT 'key_result',
    title       TEXT DEFAULT '',
    reason      TEXT DEFAULT '',
    evidence    TEXT DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_run_marks_run ON run_marks(run_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_run_marks_kind ON run_marks(kind, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_run_marks_actor ON run_marks(actor, created_at DESC);

CREATE TABLE IF NOT EXISTS run_bookmarks (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL UNIQUE REFERENCES runs(id),
    note        TEXT DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_run_bookmarks_updated ON run_bookmarks(updated_at DESC);

CREATE TABLE IF NOT EXISTS exec_events (
    id            TEXT PRIMARY KEY,
    resource_id   TEXT NOT NULL REFERENCES resources(id),
    actor         TEXT NOT NULL,
    command       TEXT NOT NULL,
    cwd           TEXT DEFAULT '',
    exit_code     INTEGER,
    started_at    DATETIME NOT NULL,
    finished_at   DATETIME,
    duration_ms   INTEGER DEFAULT 0,
    stdout_tail   TEXT DEFAULT '',
    stderr_tail   TEXT DEFAULT '',
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_exec_events_resource ON exec_events(resource_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_exec_events_actor ON exec_events(actor, created_at DESC);
