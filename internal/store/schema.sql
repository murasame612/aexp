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
    remote_path   TEXT DEFAULT '',
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
	project_id          TEXT DEFAULT '',
	target_id           TEXT DEFAULT '',
	recipe_name         TEXT DEFAULT '',
	project_config_sha256 TEXT DEFAULT '',
	datasets_json       TEXT DEFAULT '[]',
	seeds_json          TEXT DEFAULT '[]',
	 split_protocol      TEXT DEFAULT '',
	 evaluation_protocol TEXT DEFAULT '',
	 data_finalization_state TEXT NOT NULL DEFAULT 'pending',
	 data_finalization_error TEXT DEFAULT '',
	 data_finalization_updated_at DATETIME,
	    name                TEXT DEFAULT '',
	    status              TEXT NOT NULL DEFAULT 'created',
	    status_source       TEXT DEFAULT 'local_cache',
	    status_observed_at  DATETIME,
	    status_checked_at   DATETIME,
	    status_check_error  TEXT DEFAULT '',
	    kind                TEXT NOT NULL DEFAULT 'formal',
	    task_role           TEXT NOT NULL DEFAULT 'other',
	    evidence_grade      TEXT NOT NULL DEFAULT 'formal',
	    experiment_role     TEXT NOT NULL DEFAULT 'unspecified',
    gpu_index           INTEGER NOT NULL DEFAULT -1,
    cwd                 TEXT DEFAULT '',
    command             TEXT NOT NULL,
    program             TEXT DEFAULT '',
    args_json           TEXT DEFAULT '[]',
    conda_env           TEXT DEFAULT '',
    project_env         TEXT DEFAULT '',
    target_env          TEXT DEFAULT '',
    force_reason        TEXT DEFAULT '',
    preempt_run_id      TEXT DEFAULT '',
    preempt_save        BOOLEAN NOT NULL DEFAULT 0,
    git_repo_root       TEXT DEFAULT '',
    git_remote_url      TEXT DEFAULT '',
    git_branch          TEXT DEFAULT '',
    git_commit          TEXT DEFAULT '',
    git_dirty           BOOLEAN NOT NULL DEFAULT 0,
    git_status          TEXT DEFAULT '',
    git_diff_hash       TEXT DEFAULT '',
    git_diff_path       TEXT DEFAULT '',
    git_allow_dirty     BOOLEAN NOT NULL DEFAULT 0,
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
    failure_kind        TEXT DEFAULT '',
    failure_reason      TEXT DEFAULT '',
    created_by          TEXT DEFAULT '',
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at          DATETIME,
    finished_at         DATETIME,
    archived_at         DATETIME,
    deleted_at          DATETIME
);

-- Durable change feed: captures writes from the server, reconciler, CLI and
-- MCP processes that share this SQLite database.
CREATE TABLE IF NOT EXISTS run_changes (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      TEXT NOT NULL,
    operation   TEXT NOT NULL,
    changed_at  DATETIME NOT NULL DEFAULT (STRFTIME('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_run_changes_changed ON run_changes(changed_at, seq);

-- Local POS receipt notifications. The singleton settings row owns the
-- lifecycle-event cursor so service restarts never replay historical runs.
CREATE TABLE IF NOT EXISTS printer_settings (
    id                  INTEGER PRIMARY KEY CHECK (id = 1),
    enabled             BOOLEAN NOT NULL DEFAULT 0,
    queue               TEXT NOT NULL DEFAULT 'Printer_POS_80',
    last_event_seq      INTEGER NOT NULL DEFAULT 0,
    enabled_from_event_seq INTEGER NOT NULL DEFAULT 0,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS printer_run_events (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      TEXT NOT NULL,
    operation   TEXT NOT NULL,
    status      TEXT NOT NULL,
    kind        TEXT NOT NULL DEFAULT 'formal',
    started_at  DATETIME,
    finished_at DATETIME,
    changed_at  DATETIME NOT NULL DEFAULT (STRFTIME('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_printer_run_events_changed ON printer_run_events(changed_at, seq);
INSERT OR IGNORE INTO printer_settings(id, last_event_seq)
VALUES (1, (SELECT COALESCE(MAX(seq), 0) FROM printer_run_events));

CREATE TABLE IF NOT EXISTS run_print_jobs (
    ordinal      INTEGER PRIMARY KEY AUTOINCREMENT,
    id           TEXT NOT NULL UNIQUE,
    run_id       TEXT DEFAULT '',
    phase        TEXT NOT NULL,
    event_seq    INTEGER NOT NULL DEFAULT 0,
    state        TEXT NOT NULL DEFAULT 'queued',
    queue        TEXT NOT NULL,
    title        TEXT NOT NULL,
    receipt_text TEXT NOT NULL,
    cups_job_id  TEXT DEFAULT '',
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT DEFAULT '',
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at   DATETIME,
    finished_at  DATETIME
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_run_print_jobs_lifecycle
    ON run_print_jobs(run_id, phase)
    WHERE run_id <> '' AND phase IN ('start', 'end');
CREATE INDEX IF NOT EXISTS idx_run_print_jobs_queue
    ON run_print_jobs(state, ordinal);
DROP TRIGGER IF EXISTS trg_printer_runs_insert;
DROP TRIGGER IF EXISTS trg_printer_runs_update;
DROP TRIGGER IF EXISTS trg_printer_runs_delete;
CREATE TRIGGER IF NOT EXISTS trg_printer_runs_insert AFTER INSERT ON runs
BEGIN
    INSERT INTO printer_run_events(run_id, operation, status, kind, started_at, finished_at, changed_at)
    VALUES (NEW.id, 'insert', NEW.status, NEW.kind, NEW.started_at, NEW.finished_at, STRFTIME('%Y-%m-%dT%H:%M:%fZ', 'now'));
END;
CREATE TRIGGER IF NOT EXISTS trg_printer_runs_update AFTER UPDATE OF status, kind, started_at, finished_at ON runs
WHEN OLD.status IS NOT NEW.status
  OR OLD.kind IS NOT NEW.kind
  OR OLD.started_at IS NOT NEW.started_at
  OR OLD.finished_at IS NOT NEW.finished_at
BEGIN
    INSERT INTO printer_run_events(run_id, operation, status, kind, started_at, finished_at, changed_at)
    VALUES (NEW.id, 'update', NEW.status, NEW.kind, NEW.started_at, NEW.finished_at, STRFTIME('%Y-%m-%dT%H:%M:%fZ', 'now'));
END;
CREATE TRIGGER IF NOT EXISTS trg_printer_runs_delete AFTER DELETE ON runs
BEGIN
    INSERT INTO printer_run_events(run_id, operation, status, kind, started_at, finished_at, changed_at)
    VALUES (OLD.id, 'delete', OLD.status, OLD.kind, OLD.started_at, OLD.finished_at, STRFTIME('%Y-%m-%dT%H:%M:%fZ', 'now'));
END;
DROP TRIGGER IF EXISTS trg_runs_change_insert;
DROP TRIGGER IF EXISTS trg_runs_change_update;
DROP TRIGGER IF EXISTS trg_runs_change_delete;
CREATE TRIGGER IF NOT EXISTS trg_runs_change_insert AFTER INSERT ON runs
BEGIN
    INSERT INTO run_changes(run_id, operation, changed_at) VALUES (NEW.id, 'upsert', STRFTIME('%Y-%m-%dT%H:%M:%fZ', 'now'));
END;
CREATE TRIGGER IF NOT EXISTS trg_runs_change_update AFTER UPDATE ON runs
BEGIN
    INSERT INTO run_changes(run_id, operation, changed_at) VALUES (NEW.id, 'upsert', STRFTIME('%Y-%m-%dT%H:%M:%fZ', 'now'));
END;
CREATE TRIGGER IF NOT EXISTS trg_runs_change_delete AFTER DELETE ON runs
BEGIN
    INSERT INTO run_changes(run_id, operation, changed_at) VALUES (OLD.id, 'delete', STRFTIME('%Y-%m-%dT%H:%M:%fZ', 'now'));
END;

CREATE TABLE IF NOT EXISTS run_launch_jobs (
    run_id       TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    request_json TEXT NOT NULL,
    state        TEXT NOT NULL DEFAULT 'queued',
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT DEFAULT '',
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_run_launch_jobs_pending ON run_launch_jobs(state, updated_at);

CREATE TABLE IF NOT EXISTS run_input_bindings (
    id                       TEXT PRIMARY KEY,
    run_id                   TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    ordinal                  INTEGER NOT NULL,
    logical_uri              TEXT NOT NULL,
    target_path              TEXT NOT NULL,
    revision                 TEXT DEFAULT '',
    mode                     TEXT NOT NULL DEFAULT 'copy',
    source_placement_id      TEXT DEFAULT '',
    destination_placement_id TEXT DEFAULT '',
    transfer_id              TEXT DEFAULT '',
    state                    TEXT NOT NULL DEFAULT 'pending',
    error_code               TEXT DEFAULT '',
    last_error               TEXT DEFAULT '',
    created_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    verified_at              DATETIME,
    UNIQUE(run_id, ordinal)
);
CREATE INDEX IF NOT EXISTS idx_run_input_bindings_run ON run_input_bindings(run_id, ordinal);
CREATE INDEX IF NOT EXISTS idx_run_input_bindings_transfer ON run_input_bindings(transfer_id);

CREATE TABLE IF NOT EXISTS run_output_bindings (
    id                       TEXT PRIMARY KEY,
    run_id                   TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    ordinal                  INTEGER NOT NULL,
    source_pattern           TEXT NOT NULL,
    logical_uri              TEXT NOT NULL,
    role                     TEXT DEFAULT '',
    required                 BOOLEAN NOT NULL DEFAULT 0,
    source_placement_id      TEXT DEFAULT '',
    destination_placement_id TEXT DEFAULT '',
    revision                 TEXT DEFAULT '',
    transfer_id              TEXT DEFAULT '',
    state                    TEXT NOT NULL DEFAULT 'pending',
    error_code               TEXT DEFAULT '',
    last_error               TEXT DEFAULT '',
    created_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at             DATETIME,
    UNIQUE(run_id, ordinal)
);
CREATE INDEX IF NOT EXISTS idx_run_output_bindings_run ON run_output_bindings(run_id, ordinal);
CREATE INDEX IF NOT EXISTS idx_run_output_bindings_transfer ON run_output_bindings(transfer_id);

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

CREATE TABLE IF NOT EXISTS project_definitions (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    description       TEXT DEFAULT '',
    local_root        TEXT DEFAULT '',
    config_path       TEXT DEFAULT '',
    config_hash       TEXT DEFAULT '',
    source_repo       TEXT DEFAULT '',
    default_recipe    TEXT DEFAULT '',
    vault             TEXT DEFAULT '',
    run_card_index    TEXT DEFAULT '',
    proposal_dir      TEXT DEFAULT '',
    promotion_default TEXT DEFAULT '',
    aggregate_command TEXT DEFAULT '',
    gate_command      TEXT DEFAULT '',
    zotero_collection_key      TEXT DEFAULT '',
    literature_service_profile TEXT DEFAULT '',
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS project_targets (
    id                        TEXT PRIMARY KEY,
    project_id                TEXT NOT NULL REFERENCES project_definitions(id) ON DELETE CASCADE,
    name                      TEXT NOT NULL,
    resource_id               TEXT NOT NULL REFERENCES resources(id),
    cwd                       TEXT NOT NULL,
    env_strategy              TEXT NOT NULL DEFAULT 'auto',
    conda_env                 TEXT DEFAULT '',
    desired_env               TEXT DEFAULT '',
    default_gpu               INTEGER NOT NULL DEFAULT -1,
    ui_events_path            TEXT DEFAULT '',
    env_json                  TEXT DEFAULT '{}',
    sync_source               TEXT DEFAULT '',
    sync_target               TEXT DEFAULT '',
    sync_profile              TEXT DEFAULT '',
    prepare_command           TEXT DEFAULT '',
    readiness                 TEXT NOT NULL DEFAULT 'unknown',
    readiness_observed_at     DATETIME,
    readiness_error           TEXT DEFAULT '',
    last_prepare_run_id       TEXT DEFAULT '',
    last_prepared_at          DATETIME,
    observed_config_hash      TEXT DEFAULT '',
    observed_environment_hash TEXT DEFAULT '',
    created_at                DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, name)
);

CREATE INDEX IF NOT EXISTS idx_project_targets_project ON project_targets(project_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_project_targets_resource ON project_targets(resource_id, updated_at DESC);

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
CREATE INDEX IF NOT EXISTS idx_runs_created ON runs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_status_created ON runs(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_resource_created ON runs(resource_id, created_at DESC);

CREATE TABLE IF NOT EXISTS artifacts (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL REFERENCES runs(id),
    path        TEXT NOT NULL,
    relative_path TEXT DEFAULT '',
    source_uri  TEXT DEFAULT '',
    type        TEXT NOT NULL DEFAULT 'file',
    role        TEXT DEFAULT '',
    mime        TEXT DEFAULT '',
    size        INTEGER DEFAULT 0,
    sha256      TEXT DEFAULT '',
    collection_state TEXT NOT NULL DEFAULT 'indexed',
    collection_error TEXT DEFAULT '',
    discovered_at DATETIME,
    modified_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_artifacts_run ON artifacts(run_id);

CREATE TABLE IF NOT EXISTS artifact_collections (
    run_id       TEXT PRIMARY KEY REFERENCES runs(id),
    state        TEXT NOT NULL DEFAULT 'declared',
    error        TEXT DEFAULT '',
    file_count   INTEGER NOT NULL DEFAULT 0,
    total_bytes  INTEGER NOT NULL DEFAULT 0,
    started_at   DATETIME,
    finished_at  DATETIME,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS run_manifests (
    run_id          TEXT PRIMARY KEY REFERENCES runs(id),
    schema_version  INTEGER NOT NULL,
    state           TEXT NOT NULL,
    manifest_json   TEXT NOT NULL,
    sha256          TEXT NOT NULL,
    completeness    TEXT NOT NULL DEFAULT 'current',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finalized_at    DATETIME
);

-- Evidence snapshots are immutable references to already published run
-- outputs. They intentionally contain no transfer, staging, or hook state.
CREATE TABLE IF NOT EXISTS evidence_snapshots (
    id                  TEXT PRIMARY KEY,
    run_id              TEXT NOT NULL REFERENCES runs(id),
    project_id          TEXT NOT NULL REFERENCES project_definitions(id),
    run_manifest_sha256 TEXT NOT NULL,
    output_set_sha256   TEXT NOT NULL,
    manifest_json       TEXT NOT NULL,
    manifest_sha256     TEXT NOT NULL,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(run_id, output_set_sha256)
);
CREATE INDEX IF NOT EXISTS idx_evidence_snapshots_run ON evidence_snapshots(run_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_evidence_snapshots_project ON evidence_snapshots(project_id, created_at DESC);

CREATE TABLE IF NOT EXISTS evidence_releases (
    id                    TEXT PRIMARY KEY,
    snapshot_id           TEXT NOT NULL REFERENCES evidence_snapshots(id),
    project_id            TEXT NOT NULL REFERENCES project_definitions(id),
    sequence              INTEGER NOT NULL,
    state                 TEXT NOT NULL,
    aggregate_result_json TEXT NOT NULL DEFAULT '{}',
    gate_result_json      TEXT NOT NULL DEFAULT '{}',
    error_code            TEXT DEFAULT '',
    last_error            TEXT DEFAULT '',
    created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(snapshot_id, sequence)
);
CREATE INDEX IF NOT EXISTS idx_evidence_releases_snapshot ON evidence_releases(snapshot_id, sequence DESC);

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
    statement   TEXT DEFAULT '',
    body_md     TEXT DEFAULT '',
    reason      TEXT DEFAULT '',
    evidence    TEXT DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_run_marks_run ON run_marks(run_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_run_marks_kind ON run_marks(kind, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_run_marks_actor ON run_marks(actor, created_at DESC);

CREATE TABLE IF NOT EXISTS run_mark_attachments (
    id          TEXT PRIMARY KEY,
    mark_id     TEXT NOT NULL REFERENCES run_marks(id) ON DELETE CASCADE,
    filename    TEXT NOT NULL DEFAULT '',
    local_path  TEXT NOT NULL DEFAULT '',
    mime        TEXT DEFAULT '',
    caption     TEXT DEFAULT '',
    size        INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_run_mark_attachments_mark ON run_mark_attachments(mark_id, created_at ASC);

CREATE TABLE IF NOT EXISTS project_journal_entries (
    id                 TEXT PRIMARY KEY,
    project_id         TEXT NOT NULL REFERENCES project_definitions(id) ON DELETE CASCADE,
    actor              TEXT NOT NULL DEFAULT 'agent',
    title              TEXT NOT NULL,
    body_md            TEXT DEFAULT '',
    literature_refs_json TEXT NOT NULL DEFAULT '[]',
    next_action        TEXT DEFAULT '',
    next_action_status TEXT NOT NULL DEFAULT 'none'
        CHECK(next_action_status IN ('none', 'open', 'done')),
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_project_journal_project
ON project_journal_entries(project_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_project_journal_next_action
ON project_journal_entries(project_id, next_action_status, created_at DESC);

CREATE TABLE IF NOT EXISTS project_journal_run_refs (
    entry_id   TEXT NOT NULL REFERENCES project_journal_entries(id) ON DELETE CASCADE,
    run_id     TEXT NOT NULL REFERENCES runs(id),
    relation   TEXT NOT NULL DEFAULT 'related',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(entry_id, run_id)
);

CREATE INDEX IF NOT EXISTS idx_project_journal_run_refs_run
ON project_journal_run_refs(run_id, created_at DESC);

CREATE TABLE IF NOT EXISTS run_bookmarks (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL UNIQUE REFERENCES runs(id),
    note        TEXT DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_run_bookmarks_updated ON run_bookmarks(updated_at DESC);

CREATE TABLE IF NOT EXISTS project_run_cards (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL,
    project_name    TEXT DEFAULT '',
    run_id          TEXT NOT NULL UNIQUE REFERENCES runs(id),
    question        TEXT DEFAULT '',
    verdict         TEXT DEFAULT '',
    evidence_level  TEXT DEFAULT 'C',
    key_metrics     TEXT DEFAULT '',
    artifact_paths  TEXT DEFAULT '',
    supports_claim  TEXT DEFAULT '',
    weakens_claim   TEXT DEFAULT '',
    next_action     TEXT DEFAULT '',
    important       INTEGER NOT NULL DEFAULT 0,
    should_promote  INTEGER NOT NULL DEFAULT 0,
    proposal_reason TEXT DEFAULT '',
    graph_routing_reason TEXT DEFAULT '',
    related_runs    TEXT DEFAULT '',
    graph_patch_json TEXT DEFAULT '',
    graph_status    TEXT NOT NULL DEFAULT 'none',
    proposal_hash   TEXT DEFAULT '',
    base_graph_revision INTEGER NOT NULL DEFAULT 0,
    reviewed_at     DATETIME,
    no_graph_impact INTEGER NOT NULL DEFAULT 0,
    graph_impact_reason TEXT DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_project_run_cards_project ON project_run_cards(project_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_project_run_cards_important ON project_run_cards(project_id, important, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_project_run_cards_level ON project_run_cards(project_id, evidence_level, updated_at DESC);

CREATE TABLE IF NOT EXISTS manual_project_categories (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_manual_project_categories_name ON manual_project_categories(name);

CREATE TABLE IF NOT EXISTS manual_run_project_assignments (
    run_id      TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    category_id TEXT NOT NULL REFERENCES manual_project_categories(id) ON DELETE CASCADE,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_manual_run_project_assignments_category ON manual_run_project_assignments(category_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS experiment_matrices (
    id                  TEXT PRIMARY KEY,
    title               TEXT NOT NULL,
    description         TEXT DEFAULT '',
    source_kind         TEXT DEFAULT '',
    source_id           TEXT DEFAULT '',
    source_name         TEXT DEFAULT '',
    default_metric_key  TEXT DEFAULT '',
    default_metric_goal TEXT DEFAULT '',
    data_json           TEXT DEFAULT '{}',
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_experiment_matrices_updated ON experiment_matrices(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_experiment_matrices_source ON experiment_matrices(source_kind, source_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS experiment_matrix_rows (
    id         TEXT PRIMARY KEY,
    matrix_id  TEXT NOT NULL REFERENCES experiment_matrices(id) ON DELETE CASCADE,
    label      TEXT NOT NULL,
    position   INTEGER NOT NULL DEFAULT 0,
    data_json  TEXT DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_experiment_matrix_rows_matrix ON experiment_matrix_rows(matrix_id, position);

CREATE TABLE IF NOT EXISTS experiment_matrix_columns (
    id         TEXT PRIMARY KEY,
    matrix_id  TEXT NOT NULL REFERENCES experiment_matrices(id) ON DELETE CASCADE,
    label      TEXT NOT NULL,
    position   INTEGER NOT NULL DEFAULT 0,
    data_json  TEXT DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_experiment_matrix_columns_matrix ON experiment_matrix_columns(matrix_id, position);

CREATE TABLE IF NOT EXISTS experiment_matrix_cells (
    id              TEXT PRIMARY KEY,
    matrix_id       TEXT NOT NULL REFERENCES experiment_matrices(id) ON DELETE CASCADE,
    row_id          TEXT NOT NULL REFERENCES experiment_matrix_rows(id) ON DELETE CASCADE,
    column_id       TEXT NOT NULL REFERENCES experiment_matrix_columns(id) ON DELETE CASCADE,
    run_id          TEXT DEFAULT '',
    project_card_id TEXT DEFAULT '',
    title           TEXT DEFAULT '',
    statement       TEXT DEFAULT '',
    metric_key      TEXT DEFAULT '',
    metric_value    TEXT DEFAULT '',
    note            TEXT DEFAULT '',
    data_json       TEXT DEFAULT '{}',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(matrix_id, row_id, column_id, run_id, project_card_id)
);

CREATE INDEX IF NOT EXISTS idx_experiment_matrix_cells_matrix ON experiment_matrix_cells(matrix_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_experiment_matrix_cells_run ON experiment_matrix_cells(run_id);
CREATE INDEX IF NOT EXISTS idx_experiment_matrix_cells_card ON experiment_matrix_cells(project_card_id);

CREATE TABLE IF NOT EXISTS evidence_chains (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    description TEXT DEFAULT '',
    routing_hints_json TEXT NOT NULL DEFAULT '{}',
    project_id  TEXT DEFAULT '',
    role        TEXT NOT NULL DEFAULT 'secondary',
    status      TEXT NOT NULL DEFAULT 'active',
    revision    INTEGER NOT NULL DEFAULT 0,
    graph_hash  TEXT DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_evidence_chains_updated ON evidence_chains(updated_at DESC);

CREATE TABLE IF NOT EXISTS evidence_chain_nodes (
    id              TEXT PRIMARY KEY,
    chain_id        TEXT NOT NULL REFERENCES evidence_chains(id) ON DELETE CASCADE,
    type            TEXT NOT NULL,
    title           TEXT DEFAULT '',
    body            TEXT DEFAULT '',
    run_id          TEXT DEFAULT '',
    project_card_id TEXT DEFAULT '',
    x               REAL NOT NULL DEFAULT 0,
    y               REAL NOT NULL DEFAULT 0,
    width           REAL NOT NULL DEFAULT 260,
    height          REAL NOT NULL DEFAULT 140,
    pinned          INTEGER NOT NULL DEFAULT 0,
    occurred_at     DATETIME,
    data_json       TEXT DEFAULT '{}',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_evidence_chain_nodes_chain ON evidence_chain_nodes(chain_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_evidence_chain_nodes_run ON evidence_chain_nodes(run_id);

CREATE TABLE IF NOT EXISTS evidence_chain_edges (
    id              TEXT PRIMARY KEY,
    chain_id        TEXT NOT NULL REFERENCES evidence_chains(id) ON DELETE CASCADE,
    source_node_id  TEXT NOT NULL,
    target_node_id  TEXT NOT NULL,
    type            TEXT NOT NULL,
    label           TEXT DEFAULT '',
    rationale       TEXT DEFAULT '',
    data_json       TEXT DEFAULT '{}',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(source_node_id) REFERENCES evidence_chain_nodes(id) ON DELETE CASCADE,
    FOREIGN KEY(target_node_id) REFERENCES evidence_chain_nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_evidence_chain_edges_chain ON evidence_chain_edges(chain_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_evidence_chain_edges_source ON evidence_chain_edges(source_node_id);
CREATE INDEX IF NOT EXISTS idx_evidence_chain_edges_target ON evidence_chain_edges(target_node_id);

CREATE TABLE IF NOT EXISTS evidence_chain_revisions (
    id          TEXT PRIMARY KEY,
    chain_id    TEXT NOT NULL REFERENCES evidence_chains(id) ON DELETE CASCADE,
    revision    INTEGER NOT NULL,
    graph_hash  TEXT NOT NULL,
    graph_json  TEXT NOT NULL,
    actor       TEXT DEFAULT '',
    source_kind TEXT DEFAULT '',
    source_id   TEXT DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(chain_id, revision)
);

CREATE INDEX IF NOT EXISTS idx_evidence_chain_revisions_chain
ON evidence_chain_revisions(chain_id, revision DESC);

CREATE TABLE IF NOT EXISTS evidence_proposals (
    id                       TEXT PRIMARY KEY,
    project_id               TEXT NOT NULL REFERENCES project_definitions(id),
    target_chain_id          TEXT REFERENCES evidence_chains(id),
    base_graph_revision      INTEGER NOT NULL DEFAULT 0,
    actor                    TEXT DEFAULT '',
    summary                  TEXT DEFAULT '',
    routing_reason           TEXT DEFAULT '',
    project_level_impact     INTEGER NOT NULL DEFAULT 0,
    source_run_ids_json      TEXT NOT NULL DEFAULT '[]',
    source_snapshot_ids_json TEXT NOT NULL DEFAULT '[]',
    patch_json               TEXT NOT NULL DEFAULT '{}',
    status                   TEXT NOT NULL DEFAULT 'draft',
    proposal_hash            TEXT DEFAULT '',
    reviewed_by              TEXT DEFAULT '',
    reviewed_at              DATETIME,
    source_kind              TEXT DEFAULT '',
    source_id                TEXT DEFAULT '',
    created_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_evidence_proposals_project
ON evidence_proposals(project_id, status, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_evidence_proposals_target
ON evidence_proposals(target_chain_id, status, updated_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_evidence_proposals_hash
ON evidence_proposals(proposal_hash)
WHERE proposal_hash <> '';

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
CREATE INDEX IF NOT EXISTS idx_exec_events_created ON exec_events(created_at DESC);

CREATE TABLE IF NOT EXISTS storage_targets (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    kind            TEXT NOT NULL DEFAULT 'ssh_rsync',
    resource_id     TEXT NOT NULL REFERENCES resources(id),
    root_path       TEXT NOT NULL,
    config_json     TEXT NOT NULL DEFAULT '{}',
    status          TEXT NOT NULL DEFAULT 'unknown',
    last_error      TEXT DEFAULT '',
    last_checked_at DATETIME,
    health_json     TEXT NOT NULL DEFAULT '{}',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS logical_roots (
    id                TEXT PRIMARY KEY,
    workspace         TEXT NOT NULL,
    prefix            TEXT NOT NULL,
    storage_target_id TEXT NOT NULL REFERENCES storage_targets(id),
    physical_root     TEXT NOT NULL,
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(workspace, prefix)
);

CREATE INDEX IF NOT EXISTS idx_logical_roots_workspace ON logical_roots(workspace, prefix);

CREATE TABLE IF NOT EXISTS path_placements (
    id                  TEXT PRIMARY KEY,
    logical_uri         TEXT NOT NULL,
    resource_id         TEXT NOT NULL REFERENCES resources(id),
    storage_target_id   TEXT REFERENCES storage_targets(id),
    physical_path       TEXT NOT NULL,
    role                TEXT NOT NULL DEFAULT 'cache',
    desired_state       TEXT NOT NULL DEFAULT 'present',
    observed_state      TEXT NOT NULL DEFAULT 'unknown',
    revision            TEXT DEFAULT '',
    manifest_sha256     TEXT DEFAULT '',
    bytes_present       INTEGER NOT NULL DEFAULT 0,
    observation_source  TEXT DEFAULT '',
    observed_at         DATETIME,
    checked_at          DATETIME,
    observation_error   TEXT DEFAULT '',
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(logical_uri, resource_id, physical_path)
);

CREATE INDEX IF NOT EXISTS idx_path_placements_uri ON path_placements(logical_uri, role, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_path_placements_resource ON path_placements(resource_id, observed_state, updated_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_path_placements_authoritative
    ON path_placements(logical_uri)
    WHERE role='authoritative' AND desired_state='present';

CREATE TABLE IF NOT EXISTS transfer_plans (
    plan_sha256              TEXT PRIMARY KEY,
    workspace                TEXT DEFAULT '',
    source_uri               TEXT NOT NULL,
    destination_uri          TEXT NOT NULL,
    source_placement_id      TEXT REFERENCES path_placements(id),
    destination_placement_id TEXT REFERENCES path_placements(id),
    source_revision          TEXT DEFAULT '',
    plan_json                TEXT NOT NULL DEFAULT '{}',
    expires_at               DATETIME NOT NULL,
    created_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS transfer_jobs (
    id                  TEXT PRIMARY KEY,
    plan_sha256         TEXT NOT NULL UNIQUE REFERENCES transfer_plans(plan_sha256),
    state               TEXT NOT NULL DEFAULT 'queued',
    stage               TEXT NOT NULL DEFAULT 'queued',
    attempt             INTEGER NOT NULL DEFAULT 0,
    bytes_done          INTEGER NOT NULL DEFAULT 0,
    total_bytes         INTEGER NOT NULL DEFAULT 0,
    files_done          INTEGER NOT NULL DEFAULT 0,
    file_count          INTEGER NOT NULL DEFAULT 0,
    initiator           TEXT DEFAULT '',
    command_resource_id TEXT DEFAULT '',
    heartbeat_at        DATETIME,
    error_code          TEXT DEFAULT '',
    last_error          TEXT DEFAULT '',
    retryable           BOOLEAN NOT NULL DEFAULT 0,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at          DATETIME,
    finished_at         DATETIME
);

CREATE INDEX IF NOT EXISTS idx_transfer_jobs_state ON transfer_jobs(state, updated_at);

CREATE TABLE IF NOT EXISTS transfer_attempts (
    id          TEXT PRIMARY KEY,
    transfer_id TEXT NOT NULL REFERENCES transfer_jobs(id),
    number      INTEGER NOT NULL,
    initiator   TEXT NOT NULL,
    state       TEXT NOT NULL,
    error_code  TEXT DEFAULT '',
    last_error  TEXT DEFAULT '',
    started_at  DATETIME NOT NULL,
    finished_at DATETIME,
    UNIQUE(transfer_id, number)
);

CREATE INDEX IF NOT EXISTS idx_transfer_attempts_transfer ON transfer_attempts(transfer_id, number);

CREATE TABLE IF NOT EXISTS dataset_versions (
    id                TEXT PRIMARY KEY,
    dataset_id        TEXT NOT NULL,
    version           TEXT NOT NULL,
    storage_target_id TEXT NOT NULL REFERENCES storage_targets(id),
    storage_path      TEXT NOT NULL,
    logical_uri       TEXT DEFAULT '',
    revision          TEXT DEFAULT '',
    manifest_sha256   TEXT DEFAULT '',
    archive_sha256    TEXT DEFAULT '',
    format            TEXT DEFAULT '',
    file_count        INTEGER NOT NULL DEFAULT 0,
    total_bytes       INTEGER NOT NULL DEFAULT 0,
    state             TEXT NOT NULL DEFAULT 'registered',
    manifest_json     TEXT DEFAULT '{}',
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(dataset_id, version)
);

CREATE INDEX IF NOT EXISTS idx_dataset_versions_storage ON dataset_versions(storage_target_id, state, updated_at DESC);

CREATE TABLE IF NOT EXISTS dataset_materializations (
    id                 TEXT PRIMARY KEY,
    dataset_version_id TEXT NOT NULL REFERENCES dataset_versions(id),
    resource_id        TEXT NOT NULL REFERENCES resources(id),
    local_path         TEXT NOT NULL,
    state              TEXT NOT NULL DEFAULT 'planned',
    bytes_present      INTEGER NOT NULL DEFAULT 0,
    verified_sha256    TEXT DEFAULT '',
    transfer_id        TEXT REFERENCES transfer_jobs(id),
    last_error         TEXT DEFAULT '',
    started_at         DATETIME,
    finished_at        DATETIME,
    verified_at        DATETIME,
    last_accessed_at   DATETIME,
    updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(dataset_version_id, resource_id)
);

CREATE INDEX IF NOT EXISTS idx_dataset_materializations_resource ON dataset_materializations(resource_id, state, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_dataset_materializations_dataset ON dataset_materializations(dataset_version_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS run_freezes (
    id                      TEXT PRIMARY KEY,
    run_id                  TEXT NOT NULL REFERENCES runs(id),
    profile                 TEXT NOT NULL,
    profile_sha256          TEXT NOT NULL,
    plan_sha256             TEXT NOT NULL,
    destination_uri         TEXT NOT NULL,
    workspace_path          TEXT DEFAULT '',
    state                   TEXT NOT NULL DEFAULT 'queued',
    stage                   TEXT NOT NULL DEFAULT 'queued',
    error_code              TEXT DEFAULT '',
    last_error              TEXT DEFAULT '',
    run_manifest_sha256     TEXT NOT NULL,
    provenance_json         TEXT NOT NULL DEFAULT '{}',
    blockers_json           TEXT NOT NULL DEFAULT '[]',
    raw_manifest_sha256     TEXT DEFAULT '',
    release_manifest_sha256 TEXT DEFAULT '',
    manifest_uri            TEXT DEFAULT '',
    raw_transfer_id         TEXT REFERENCES transfer_jobs(id),
    workspace_transfer_id   TEXT REFERENCES transfer_jobs(id),
    file_count              INTEGER NOT NULL DEFAULT 0,
    total_bytes             INTEGER NOT NULL DEFAULT 0,
    files_done              INTEGER NOT NULL DEFAULT 0,
    bytes_done              INTEGER NOT NULL DEFAULT 0,
    aggregate_result_json   TEXT DEFAULT '{}',
    gate_result_json        TEXT DEFAULT '{}',
    created_at              DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at              DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    frozen_at               DATETIME,
    released_at             DATETIME
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_run_freezes_plan ON run_freezes(run_id, profile, destination_uri, plan_sha256);
CREATE INDEX IF NOT EXISTS idx_run_freezes_run ON run_freezes(run_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_run_freezes_state ON run_freezes(state, updated_at DESC);

CREATE TABLE IF NOT EXISTS run_freeze_files (
    id                 TEXT PRIMARY KEY,
    freeze_id          TEXT NOT NULL REFERENCES run_freezes(id),
    kind               TEXT NOT NULL,
    role               TEXT NOT NULL,
    relative_path      TEXT NOT NULL,
    source_uri         TEXT NOT NULL,
    frozen_uri         TEXT DEFAULT '',
    sha256             TEXT NOT NULL,
    size               INTEGER NOT NULL DEFAULT 0,
    required           BOOLEAN NOT NULL DEFAULT 0,
    source_artifact_id TEXT DEFAULT '',
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(freeze_id, kind, relative_path)
);

CREATE INDEX IF NOT EXISTS idx_run_freeze_files_freeze ON run_freeze_files(freeze_id, kind, role);
