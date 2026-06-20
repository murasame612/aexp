package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLite implements Store using SQLite.
type SQLite struct {
	db *sql.DB
}

// NewSQLite opens (or creates) a SQLite database and runs migrations.
func NewSQLite(dbPath string) (*SQLite, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// Enable WAL mode for better concurrent reads.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	s := &SQLite{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return s, nil
}

func (s *SQLite) migrate() error {
	schema, err := Migrations.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	if _, err := s.db.Exec(string(schema)); err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}

	// Add missing columns for upgrades from older schema versions
	return s.migrateColumns()
}

// migrateColumns adds columns that may not exist in older databases.
func (s *SQLite) migrateColumns() error {
	addColumn := func(table, column, colType, defaultValue string) {
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s DEFAULT %s", table, column, colType, defaultValue)
		s.db.Exec(stmt) // ignore error (column already exists)
	}

	addColumn("runs", "kind", "TEXT NOT NULL", "'formal'")
	addColumn("runs", "gpu_index", "INTEGER NOT NULL", "-1")
	addColumn("runs", "program", "TEXT", "''")
	addColumn("runs", "args_json", "TEXT", "'[]'")
	addColumn("runs", "project_env", "TEXT", "''")
	addColumn("runs", "resolved_env", "TEXT", "''")
	addColumn("runs", "resolved_python", "TEXT", "''")
	addColumn("runs", "resolved_cwd", "TEXT", "''")
	addColumn("runs", "ui_events_path", "TEXT", "''")
	addColumn("runs", "archived_at", "DATETIME", "NULL")
	addColumn("runs", "deleted_at", "DATETIME", "NULL")
	addColumn("resources", "socks_proxy", "TEXT", "''")
	addColumn("resources", "proxy_command", "TEXT", "''")
	addColumn("resources", "os_type", "TEXT", "''")
	addColumn("resources", "remote_path", "TEXT", "''")
	addColumn("resources", "conda_base", "TEXT", "''")
	addColumn("resources", "conda_init", "TEXT", "''")
	addColumn("resources", "ssh_status", "TEXT NOT NULL", "'unknown'")
	addColumn("resources", "last_doctor_error", "TEXT", "''")
	addColumn("resources", "last_checked_at", "DATETIME", "NULL")
	addColumn("resources", "last_success_at", "DATETIME", "NULL")
	addColumn("project_run_cards", "project_name", "TEXT", "''")
	addColumn("project_run_cards", "should_promote", "INTEGER NOT NULL", "0")
	addColumn("project_run_cards", "proposal_reason", "TEXT", "''")
	addColumn("project_run_cards", "related_runs", "TEXT", "''")
	addColumn("run_marks", "statement", "TEXT", "''")
	addColumn("run_marks", "body_md", "TEXT", "''")

	return nil
}

func (s *SQLite) Close() error {
	return s.db.Close()
}

// --- Resources ---

const resourceColumns = "id, name, type, host, os_type, port, user, auth_ref, socks_proxy, proxy_command, root_dir, remote_path, conda_base, conda_init, conda_env, gpu_indices, tags, status, ssh_status, last_doctor_error, last_checked_at, last_success_at, created_at, updated_at"

func (s *SQLite) CreateResource(ctx context.Context, r *Resource) error {
	r.CreatedAt = time.Now()
	r.UpdatedAt = time.Now()
	if r.SSHStatus == "" {
		r.SSHStatus = ResourceSSHStatusUnknown
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO resources (`+resourceColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, r.Type, r.Host, r.OSType, r.Port, r.User, r.AuthRef, r.SocksProxy, r.ProxyCommand, r.RootDir, r.RemotePath, r.CondaBase, r.CondaInit, r.CondaEnv, r.GPUIndices, r.Tags, r.Status, r.SSHStatus, r.LastDoctorError, r.LastCheckedAt, r.LastSuccessAt, r.CreatedAt, r.UpdatedAt,
	)
	return err
}

func (s *SQLite) GetResource(ctx context.Context, id string) (*Resource, error) {
	r := &Resource{}
	err := scanResource(r)(s.db.QueryRowContext(ctx, `SELECT `+resourceColumns+` FROM resources WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

func (s *SQLite) GetResourceByName(ctx context.Context, name string) (*Resource, error) {
	r := &Resource{}
	err := scanResource(r)(s.db.QueryRowContext(ctx, `SELECT `+resourceColumns+` FROM resources WHERE name = ?`, name))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

func (s *SQLite) ListResources(ctx context.Context) ([]Resource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+resourceColumns+` FROM resources ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	resources := make([]Resource, 0)
	for rows.Next() {
		var r Resource
		if err := scanResource(&r)(rows); err != nil {
			return nil, err
		}
		resources = append(resources, r)
	}
	return resources, rows.Err()
}

func (s *SQLite) UpdateResource(ctx context.Context, r *Resource) error {
	r.UpdatedAt = time.Now()
	_, err := s.db.ExecContext(ctx,
		`UPDATE resources SET name=?, type=?, host=?, os_type=?, port=?, user=?, auth_ref=?, socks_proxy=?, proxy_command=?, root_dir=?, remote_path=?, conda_base=?, conda_init=?, conda_env=?, gpu_indices=?, tags=?, status=?, ssh_status=?, last_doctor_error=?, last_checked_at=?, last_success_at=?, updated_at=? WHERE id=?`,
		r.Name, r.Type, r.Host, r.OSType, r.Port, r.User, r.AuthRef, r.SocksProxy, r.ProxyCommand, r.RootDir, r.RemotePath, r.CondaBase, r.CondaInit, r.CondaEnv, r.GPUIndices, r.Tags, r.Status, r.SSHStatus, r.LastDoctorError, r.LastCheckedAt, r.LastSuccessAt, r.UpdatedAt, r.ID,
	)
	return err
}

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanResource(r *Resource) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&r.ID, &r.Name, &r.Type, &r.Host, &r.OSType, &r.Port, &r.User, &r.AuthRef, &r.SocksProxy, &r.ProxyCommand, &r.RootDir, &r.RemotePath, &r.CondaBase, &r.CondaInit, &r.CondaEnv, &r.GPUIndices, &r.Tags, &r.Status, &r.SSHStatus, &r.LastDoctorError, &r.LastCheckedAt, &r.LastSuccessAt, &r.CreatedAt, &r.UpdatedAt)
	}
}

func (s *SQLite) DeleteResource(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM resources WHERE id = ?`, id)
	return err
}

// --- Runs ---

const runColumns = "id, resource_id, name, status, kind, gpu_index, cwd, command, program, args_json, conda_env, project_env, resolved_env, resolved_python, resolved_cwd, env_json, log_paths_json, artifact_paths_json, metric_paths_json, ui_events_path, tmux_session, remote_run_dir, exit_code, created_by, created_at, started_at, finished_at, archived_at, deleted_at"

func scanRun(r *Run) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&r.ID, &r.ResourceID, &r.Name, &r.Status, &r.Kind, &r.GPUIndex, &r.Cwd, &r.Command, &r.Program, &r.ArgsJSON, &r.CondaEnv, &r.ProjectEnv, &r.ResolvedEnv, &r.ResolvedPython, &r.ResolvedCwd, &r.EnvJSON, &r.LogPathsJSON, &r.ArtifactPathsJSON, &r.MetricPathsJSON, &r.UIEventsPath, &r.TmuxSession, &r.RemoteRunDir, &r.ExitCode, &r.CreatedBy, &r.CreatedAt, &r.StartedAt, &r.FinishedAt, &r.ArchivedAt, &r.DeletedAt)
	}
}

func (s *SQLite) CreateRun(ctx context.Context, r *Run) error {
	r.CreatedAt = time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO runs (`+runColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ResourceID, r.Name, r.Status, r.Kind, r.GPUIndex, r.Cwd, r.Command, r.Program, r.ArgsJSON, r.CondaEnv, r.ProjectEnv, r.ResolvedEnv, r.ResolvedPython, r.ResolvedCwd, r.EnvJSON, r.LogPathsJSON, r.ArtifactPathsJSON, r.MetricPathsJSON, r.UIEventsPath, r.TmuxSession, r.RemoteRunDir, r.ExitCode, r.CreatedBy, r.CreatedAt, r.StartedAt, r.FinishedAt, r.ArchivedAt, r.DeletedAt,
	)
	return err
}

func (s *SQLite) GetRun(ctx context.Context, id string) (*Run, error) {
	r := &Run{}
	err := scanRun(r)(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

func (s *SQLite) ListRuns(ctx context.Context, filter RunFilter) ([]Run, error) {
	where, args := runFilterWhere(filter)
	query := `SELECT ` + runColumns + ` FROM runs` + where

	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	runs := make([]Run, 0)
	for rows.Next() {
		var r Run
		if err := scanRun(&r)(rows); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (s *SQLite) CountRuns(ctx context.Context, filter RunFilter) (int, error) {
	where, args := runFilterWhere(filter)
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs`+where, args...).Scan(&count)
	return count, err
}

func runFilterWhere(filter RunFilter) (string, []interface{}) {
	query := ` WHERE 1=1`
	var args []interface{}

	if filter.ResourceID != "" {
		query += " AND resource_id = ?"
		args = append(args, filter.ResourceID)
	}
	if filter.Status != "" {
		query += " AND status = ?"
		args = append(args, filter.Status)
	}
	if filter.Deleted {
		query += " AND deleted_at IS NOT NULL"
	} else {
		query += " AND deleted_at IS NULL"
		if filter.Trash {
			query += " AND archived_at IS NOT NULL"
		} else {
			query += " AND archived_at IS NULL"
		}
	}

	return query, args
}

func (s *SQLite) UpdateRun(ctx context.Context, r *Run) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET name=?, status=?, kind=?, gpu_index=?, cwd=?, command=?, program=?, args_json=?, conda_env=?, project_env=?, resolved_env=?, resolved_python=?, resolved_cwd=?, env_json=?, log_paths_json=?, artifact_paths_json=?, metric_paths_json=?, ui_events_path=?, tmux_session=?, remote_run_dir=?, exit_code=?, created_by=?, started_at=?, finished_at=? WHERE id=?`,
		r.Name, r.Status, r.Kind, r.GPUIndex, r.Cwd, r.Command, r.Program, r.ArgsJSON, r.CondaEnv, r.ProjectEnv, r.ResolvedEnv, r.ResolvedPython, r.ResolvedCwd, r.EnvJSON, r.LogPathsJSON, r.ArtifactPathsJSON, r.MetricPathsJSON, r.UIEventsPath, r.TmuxSession, r.RemoteRunDir, r.ExitCode, r.CreatedBy, r.StartedAt, r.FinishedAt, r.ID,
	)
	return err
}

func (s *SQLite) ArchiveRun(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET archived_at = COALESCE(archived_at, ?), deleted_at = NULL WHERE id = ?`, time.Now(), id)
	return err
}

func (s *SQLite) RestoreRun(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET archived_at = NULL, deleted_at = NULL WHERE id = ?`, id)
	return err
}

func (s *SQLite) DeleteRunLogically(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET deleted_at = COALESCE(deleted_at, ?), archived_at = COALESCE(archived_at, ?) WHERE id = ?`, time.Now(), time.Now(), id)
	return err
}

// --- Project Profiles ---

func (s *SQLite) SaveProjectProfile(ctx context.Context, p *ProjectProfile) error {
	p.UpdatedAt = time.Now()
	entrypointsJSON, _ := json.Marshal(p.Entrypoints)
	metricsJSON, _ := json.Marshal(p.Metrics)
	logsJSON, _ := json.Marshal(p.Logs)
	warningsJSON, _ := json.Marshal(p.Warnings)
	_, err := s.db.ExecContext(ctx, `INSERT INTO project_profiles (
		resource_id, resource_name, cwd, env_strategy, resolved_env, env_name, python, resolved_cwd, command_prefix,
		python_ok, torch_ok, cuda, cuda_ok, entrypoints_json, metrics_json, logs_json, warnings_json, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(resource_id, cwd) DO UPDATE SET
		resource_name=excluded.resource_name,
		env_strategy=excluded.env_strategy,
		resolved_env=excluded.resolved_env,
		env_name=excluded.env_name,
		python=excluded.python,
		resolved_cwd=excluded.resolved_cwd,
		command_prefix=excluded.command_prefix,
		python_ok=excluded.python_ok,
		torch_ok=excluded.torch_ok,
		cuda=excluded.cuda,
		cuda_ok=excluded.cuda_ok,
		entrypoints_json=excluded.entrypoints_json,
		metrics_json=excluded.metrics_json,
		logs_json=excluded.logs_json,
		warnings_json=excluded.warnings_json,
		updated_at=excluded.updated_at`,
		p.ResourceID, p.ResourceName, p.Cwd, p.EnvStrategy, p.ResolvedEnv, p.EnvName, p.Python, p.ResolvedCwd, p.CommandPrefix,
		boolInt(p.PythonOK), boolInt(p.TorchOK), p.CUDA, boolInt(p.CUDAOK), string(entrypointsJSON), string(metricsJSON), string(logsJSON), string(warningsJSON), p.UpdatedAt,
	)
	return err
}

func (s *SQLite) GetProjectProfile(ctx context.Context, resourceID string, cwd string) (*ProjectProfile, error) {
	var p ProjectProfile
	var pythonOK, torchOK, cudaOK int
	var entrypointsJSON, metricsJSON, logsJSON, warningsJSON string
	err := s.db.QueryRowContext(ctx, `SELECT resource_id, resource_name, cwd, env_strategy, resolved_env, env_name, python, resolved_cwd, command_prefix, python_ok, torch_ok, cuda, cuda_ok, entrypoints_json, metrics_json, logs_json, warnings_json, updated_at
		FROM project_profiles WHERE resource_id = ? AND cwd = ?`, resourceID, cwd).
		Scan(&p.ResourceID, &p.ResourceName, &p.Cwd, &p.EnvStrategy, &p.ResolvedEnv, &p.EnvName, &p.Python, &p.ResolvedCwd, &p.CommandPrefix, &pythonOK, &torchOK, &p.CUDA, &cudaOK, &entrypointsJSON, &metricsJSON, &logsJSON, &warningsJSON, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.PythonOK = pythonOK != 0
	p.TorchOK = torchOK != 0
	p.CUDAOK = cudaOK != 0
	_ = json.Unmarshal([]byte(entrypointsJSON), &p.Entrypoints)
	_ = json.Unmarshal([]byte(metricsJSON), &p.Metrics)
	_ = json.Unmarshal([]byte(logsJSON), &p.Logs)
	_ = json.Unmarshal([]byte(warningsJSON), &p.Warnings)
	return &p, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// --- Snapshots ---

func (s *SQLite) SaveSnapshot(ctx context.Context, snap *Snapshot) error {
	snap.Timestamp = time.Now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO resource_snapshots (resource_id, run_id, timestamp, cpu_percent, mem_used_mb, mem_total_mb, gpu_json, disk_json, load_1m, load_5m, load_15m)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snap.ResourceID, snap.RunID, snap.Timestamp, snap.CPUPercent, snap.MemUsedMB, snap.MemTotalMB, snap.GPUJSON, snap.DiskJSON, snap.Load1m, snap.Load5m, snap.Load15m,
	)
	if err != nil {
		return err
	}
	snap.ID, _ = res.LastInsertId()
	return nil
}

func (s *SQLite) GetLatestSnapshot(ctx context.Context, resourceID string) (*Snapshot, error) {
	snap := &Snapshot{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, resource_id, run_id, timestamp, cpu_percent, mem_used_mb, mem_total_mb, gpu_json, disk_json, load_1m, load_5m, load_15m FROM resource_snapshots WHERE resource_id = ? ORDER BY timestamp DESC LIMIT 1`, resourceID).
		Scan(&snap.ID, &snap.ResourceID, &snap.RunID, &snap.Timestamp, &snap.CPUPercent, &snap.MemUsedMB, &snap.MemTotalMB, &snap.GPUJSON, &snap.DiskJSON, &snap.Load1m, &snap.Load5m, &snap.Load15m)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return snap, err
}

func (s *SQLite) ListSnapshots(ctx context.Context, resourceID string, limit int) ([]Snapshot, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, resource_id, run_id, timestamp, cpu_percent, mem_used_mb, mem_total_mb, gpu_json, disk_json, load_1m, load_5m, load_15m FROM resource_snapshots WHERE resource_id = ? ORDER BY timestamp DESC LIMIT ?`, resourceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snapshots []Snapshot
	for rows.Next() {
		var snap Snapshot
		if err := rows.Scan(&snap.ID, &snap.ResourceID, &snap.RunID, &snap.Timestamp, &snap.CPUPercent, &snap.MemUsedMB, &snap.MemTotalMB, &snap.GPUJSON, &snap.DiskJSON, &snap.Load1m, &snap.Load5m, &snap.Load15m); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snap)
	}
	return snapshots, rows.Err()
}

// --- Logs ---

func (s *SQLite) AppendLogLines(ctx context.Context, runID string, lines []LogLine) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO log_lines (run_id, source, line_no, content, timestamp) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, l := range lines {
		if l.Timestamp.IsZero() {
			l.Timestamp = time.Now()
		}
		if _, err := stmt.ExecContext(ctx, runID, l.Source, l.LineNo, l.Content, l.Timestamp); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) GetLogLines(ctx context.Context, runID string, source string, offset, limit int) ([]LogLine, error) {
	if limit <= 0 {
		limit = 200
	}
	query := `SELECT id, run_id, source, line_no, content, timestamp FROM log_lines WHERE run_id = ?`
	args := []interface{}{runID}

	if source != "" {
		query += " AND source = ?"
		args = append(args, source)
	}
	query += " ORDER BY line_no ASC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lines []LogLine
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.ID, &l.RunID, &l.Source, &l.LineNo, &l.Content, &l.Timestamp); err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

func (s *SQLite) CountLogLines(ctx context.Context, runID string, source string) (int, error) {
	query := `SELECT COUNT(*) FROM log_lines WHERE run_id = ?`
	args := []interface{}{runID}
	if source != "" {
		query += " AND source = ?"
		args = append(args, source)
	}
	var count int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}

// --- Artifacts ---

func (s *SQLite) SaveArtifacts(ctx context.Context, runID string, artifacts []Artifact) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO artifacts (id, run_id, path, type, size, modified_at) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, a := range artifacts {
		if _, err := stmt.ExecContext(ctx, a.ID, runID, a.Path, a.Type, a.Size, a.ModifiedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) ListArtifacts(ctx context.Context, runID string) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, path, type, size, modified_at FROM artifacts WHERE run_id = ? ORDER BY path`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var artifacts []Artifact
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.RunID, &a.Path, &a.Type, &a.Size, &a.ModifiedAt); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, a)
	}
	return artifacts, rows.Err()
}

// --- Agent Events ---

func (s *SQLite) SaveAgentEvent(ctx context.Context, e *AgentEvent) error {
	e.Timestamp = time.Now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_events (run_id, actor, tool_name, input_json, output_json, timestamp) VALUES (?, ?, ?, ?, ?, ?)`,
		e.RunID, e.Actor, e.ToolName, e.InputJSON, e.OutputJSON, e.Timestamp,
	)
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	return nil
}

func (s *SQLite) ListAgentEvents(ctx context.Context, runID string) ([]AgentEvent, error) {
	var rows *sql.Rows
	var err error

	if runID != "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, run_id, actor, tool_name, input_json, output_json, timestamp FROM agent_events WHERE run_id = ? ORDER BY timestamp`, runID)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, run_id, actor, tool_name, input_json, output_json, timestamp FROM agent_events ORDER BY timestamp DESC LIMIT 100`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []AgentEvent
	for rows.Next() {
		var e AgentEvent
		if err := rows.Scan(&e.ID, &e.RunID, &e.Actor, &e.ToolName, &e.InputJSON, &e.OutputJSON, &e.Timestamp); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// --- Run Marks ---

const runMarkColumns = "id, run_id, actor, kind, title, statement, body_md, reason, evidence, created_at"

func scanRunMark(m *RunMark) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&m.ID, &m.RunID, &m.Actor, &m.Kind, &m.Title, &m.Statement, &m.BodyMD, &m.Reason, &m.Evidence, &m.CreatedAt)
	}
}

func (s *SQLite) SaveRunMark(ctx context.Context, m *RunMark) error {
	m.CreatedAt = time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO run_marks (id, run_id, actor, kind, title, statement, body_md, reason, evidence, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.RunID, m.Actor, m.Kind, m.Title, m.Statement, m.BodyMD, m.Reason, m.Evidence, m.CreatedAt,
	)
	return err
}

func (s *SQLite) GetRunMark(ctx context.Context, id string) (*RunMark, error) {
	m := &RunMark{}
	err := scanRunMark(m)(s.db.QueryRowContext(ctx,
		`SELECT `+runMarkColumns+` FROM run_marks WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.Attachments, err = s.ListRunMarkAttachments(ctx, m.ID)
	return m, err
}

func (s *SQLite) ListRunMarks(ctx context.Context, filter RunMarkFilter) ([]RunMark, error) {
	query := `SELECT ` + runMarkColumns + ` FROM run_marks WHERE 1=1`
	var args []interface{}

	if filter.RunID != "" {
		query += " AND run_id = ?"
		args = append(args, filter.RunID)
	}
	if filter.Actor != "" {
		query += " AND actor = ?"
		args = append(args, filter.Actor)
	}
	if filter.Kind != "" {
		query += " AND kind = ?"
		args = append(args, filter.Kind)
	}

	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	marks := make([]RunMark, 0)
	for rows.Next() {
		var m RunMark
		if err := scanRunMark(&m)(rows); err != nil {
			return nil, err
		}
		attachments, err := s.ListRunMarkAttachments(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		m.Attachments = attachments
		marks = append(marks, m)
	}
	return marks, rows.Err()
}

const runMarkAttachmentColumns = "id, mark_id, filename, local_path, mime, caption, size, created_at"

func scanRunMarkAttachment(a *RunMarkAttachment) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&a.ID, &a.MarkID, &a.Filename, &a.LocalPath, &a.Mime, &a.Caption, &a.Size, &a.CreatedAt)
	}
}

func (s *SQLite) SaveRunMarkAttachments(ctx context.Context, markID string, attachments []RunMarkAttachment) error {
	if len(attachments) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO run_mark_attachments (`+runMarkAttachmentColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now()
	for _, a := range attachments {
		if a.CreatedAt.IsZero() {
			a.CreatedAt = now
		}
		if a.MarkID == "" {
			a.MarkID = markID
		}
		if _, err := stmt.ExecContext(ctx, a.ID, a.MarkID, a.Filename, a.LocalPath, a.Mime, a.Caption, a.Size, a.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) GetRunMarkAttachment(ctx context.Context, markID string, attachmentID string) (*RunMarkAttachment, error) {
	a := &RunMarkAttachment{}
	err := scanRunMarkAttachment(a)(s.db.QueryRowContext(ctx,
		`SELECT `+runMarkAttachmentColumns+` FROM run_mark_attachments WHERE mark_id = ? AND id = ?`, markID, attachmentID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return a, err
}

func (s *SQLite) ListRunMarkAttachments(ctx context.Context, markID string) ([]RunMarkAttachment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+runMarkAttachmentColumns+` FROM run_mark_attachments WHERE mark_id = ? ORDER BY created_at ASC`, markID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	attachments := make([]RunMarkAttachment, 0)
	for rows.Next() {
		var a RunMarkAttachment
		if err := scanRunMarkAttachment(&a)(rows); err != nil {
			return nil, err
		}
		attachments = append(attachments, a)
	}
	return attachments, rows.Err()
}

// --- Run Bookmarks ---

const runBookmarkColumns = "id, run_id, note, created_at, updated_at"

func scanRunBookmark(b *RunBookmark) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&b.ID, &b.RunID, &b.Note, &b.CreatedAt, &b.UpdatedAt)
	}
}

func (s *SQLite) SaveRunBookmark(ctx context.Context, b *RunBookmark) error {
	now := time.Now()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO run_bookmarks (id, run_id, note, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET note=excluded.note, updated_at=excluded.updated_at`,
		b.ID, b.RunID, b.Note, b.CreatedAt, b.UpdatedAt,
	)
	return err
}

func (s *SQLite) GetRunBookmark(ctx context.Context, runID string) (*RunBookmark, error) {
	b := &RunBookmark{}
	err := scanRunBookmark(b)(s.db.QueryRowContext(ctx,
		`SELECT `+runBookmarkColumns+` FROM run_bookmarks WHERE run_id = ?`, runID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return b, err
}

func (s *SQLite) ListRunBookmarks(ctx context.Context, filter RunBookmarkFilter) ([]RunBookmark, error) {
	query := `SELECT ` + runBookmarkColumns + ` FROM run_bookmarks ORDER BY updated_at DESC`
	var args []interface{}

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bookmarks := make([]RunBookmark, 0)
	for rows.Next() {
		var b RunBookmark
		if err := scanRunBookmark(&b)(rows); err != nil {
			return nil, err
		}
		bookmarks = append(bookmarks, b)
	}
	return bookmarks, rows.Err()
}

func (s *SQLite) DeleteRunBookmark(ctx context.Context, runID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM run_bookmarks WHERE run_id = ?`, runID)
	return err
}

// --- Project Run Cards ---

const projectRunCardColumns = "id, project_id, project_name, run_id, question, verdict, evidence_level, key_metrics, artifact_paths, supports_claim, weakens_claim, next_action, important, should_promote, proposal_reason, related_runs, created_at, updated_at"

func scanProjectRunCard(c *ProjectRunCard) func(rowScanner) error {
	return func(row rowScanner) error {
		var important, shouldPromote int
		if err := row.Scan(
			&c.ID, &c.ProjectID, &c.ProjectName, &c.RunID, &c.Question, &c.Verdict, &c.EvidenceLevel,
			&c.KeyMetrics, &c.ArtifactPaths, &c.SupportsClaim, &c.WeakensClaim, &c.NextAction,
			&important, &shouldPromote, &c.ProposalReason, &c.RelatedRuns, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return err
		}
		c.Important = important != 0
		c.ShouldPromote = shouldPromote != 0
		return nil
	}
}

func (s *SQLite) SaveProjectRunCard(ctx context.Context, c *ProjectRunCard) error {
	now := time.Now()
	if c.ID == "" {
		c.ID = "card_" + strings.TrimPrefix(c.RunID, "run_")
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO project_run_cards (`+projectRunCardColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET
			project_id=excluded.project_id,
			project_name=excluded.project_name,
			question=excluded.question,
			verdict=excluded.verdict,
			evidence_level=excluded.evidence_level,
			key_metrics=excluded.key_metrics,
			artifact_paths=excluded.artifact_paths,
			supports_claim=excluded.supports_claim,
			weakens_claim=excluded.weakens_claim,
			next_action=excluded.next_action,
			important=excluded.important,
			should_promote=excluded.should_promote,
			proposal_reason=excluded.proposal_reason,
			related_runs=excluded.related_runs,
			updated_at=excluded.updated_at`,
		c.ID, c.ProjectID, c.ProjectName, c.RunID, c.Question, c.Verdict, c.EvidenceLevel,
		c.KeyMetrics, c.ArtifactPaths, c.SupportsClaim, c.WeakensClaim, c.NextAction,
		boolInt(c.Important), boolInt(c.ShouldPromote), c.ProposalReason, c.RelatedRuns, c.CreatedAt, c.UpdatedAt,
	)
	return err
}

func (s *SQLite) GetProjectRunCard(ctx context.Context, runID string) (*ProjectRunCard, error) {
	c := &ProjectRunCard{}
	err := scanProjectRunCard(c)(s.db.QueryRowContext(ctx,
		`SELECT `+projectRunCardColumns+` FROM project_run_cards WHERE run_id = ?`, runID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

func (s *SQLite) ListProjectRunCards(ctx context.Context, filter ProjectRunCardFilter) ([]ProjectRunCard, error) {
	query := `SELECT ` + projectRunCardColumns + ` FROM project_run_cards WHERE 1=1`
	var args []interface{}
	if filter.ProjectID != "" {
		query += " AND project_id = ?"
		args = append(args, filter.ProjectID)
	}
	if filter.RunID != "" {
		query += " AND run_id = ?"
		args = append(args, filter.RunID)
	}
	if filter.ImportantOnly {
		query += " AND important = 1"
	}
	query += " ORDER BY updated_at DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cards := make([]ProjectRunCard, 0)
	for rows.Next() {
		var c ProjectRunCard
		if err := scanProjectRunCard(&c)(rows); err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	return cards, rows.Err()
}

// --- Manual Project Categories ---

const manualProjectCategoryColumns = "id, name, description, created_at, updated_at"

func scanManualProjectCategory(c *ManualProjectCategory, includeRunCount bool) func(rowScanner) error {
	return func(row rowScanner) error {
		dest := []interface{}{&c.ID, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt}
		if includeRunCount {
			dest = append(dest, &c.RunCount)
		}
		return row.Scan(dest...)
	}
}

func (s *SQLite) CreateManualProjectCategory(ctx context.Context, c *ManualProjectCategory) error {
	now := time.Now()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO manual_project_categories (`+manualProjectCategoryColumns+`)
		 VALUES (?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.Description, c.CreatedAt, c.UpdatedAt,
	)
	return err
}

func (s *SQLite) GetManualProjectCategory(ctx context.Context, id string) (*ManualProjectCategory, error) {
	c := &ManualProjectCategory{}
	err := scanManualProjectCategory(c, true)(s.db.QueryRowContext(ctx,
		`SELECT c.`+strings.ReplaceAll(manualProjectCategoryColumns, ", ", ", c.")+`, COUNT(a.run_id) AS run_count
		 FROM manual_project_categories c
		 LEFT JOIN manual_run_project_assignments a ON a.category_id = c.id
		 WHERE c.id = ?
		 GROUP BY c.id`,
		id,
	))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

func (s *SQLite) ListManualProjectCategories(ctx context.Context) ([]ManualProjectCategory, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.`+strings.ReplaceAll(manualProjectCategoryColumns, ", ", ", c.")+`, COUNT(a.run_id) AS run_count
		 FROM manual_project_categories c
		 LEFT JOIN manual_run_project_assignments a ON a.category_id = c.id
		 GROUP BY c.id
		 ORDER BY lower(c.name), c.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	categories := make([]ManualProjectCategory, 0)
	for rows.Next() {
		var c ManualProjectCategory
		if err := scanManualProjectCategory(&c, true)(rows); err != nil {
			return nil, err
		}
		categories = append(categories, c)
	}
	return categories, rows.Err()
}

func (s *SQLite) AssignRunToManualProjectCategory(ctx context.Context, runID string, categoryID string) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO manual_run_project_assignments (run_id, category_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET
			category_id=excluded.category_id,
			updated_at=excluded.updated_at`,
		runID, categoryID, now, now,
	)
	return err
}

func scanRunProjectAssignment(a *RunProjectAssignment) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&a.RunID, &a.CategoryID, &a.CategoryName, &a.CreatedAt, &a.UpdatedAt)
	}
}

func (s *SQLite) GetRunProjectAssignment(ctx context.Context, runID string) (*RunProjectAssignment, error) {
	a := &RunProjectAssignment{}
	err := scanRunProjectAssignment(a)(s.db.QueryRowContext(ctx,
		`SELECT a.run_id, a.category_id, c.name, a.created_at, a.updated_at
		 FROM manual_run_project_assignments a
		 JOIN manual_project_categories c ON c.id = a.category_id
		 WHERE a.run_id = ?`,
		runID,
	))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return a, err
}

func (s *SQLite) ListRunProjectAssignments(ctx context.Context) ([]RunProjectAssignment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.run_id, a.category_id, c.name, a.created_at, a.updated_at
		 FROM manual_run_project_assignments a
		 JOIN manual_project_categories c ON c.id = a.category_id
		 ORDER BY a.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assignments := make([]RunProjectAssignment, 0)
	for rows.Next() {
		var a RunProjectAssignment
		if err := scanRunProjectAssignment(&a)(rows); err != nil {
			return nil, err
		}
		assignments = append(assignments, a)
	}
	return assignments, rows.Err()
}

func (s *SQLite) UnassignRunFromManualProjectCategory(ctx context.Context, runID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM manual_run_project_assignments WHERE run_id = ?`, runID)
	return err
}

// --- Experiment Matrices ---

const experimentMatrixColumns = "id, title, description, source_kind, source_id, source_name, default_metric_key, default_metric_goal, data_json, created_at, updated_at"

func scanExperimentMatrix(m *ExperimentMatrix) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&m.ID, &m.Title, &m.Description, &m.SourceKind, &m.SourceID, &m.SourceName, &m.DefaultMetricKey, &m.DefaultMetricGoal, &m.DataJSON, &m.CreatedAt, &m.UpdatedAt)
	}
}

func (s *SQLite) CreateExperimentMatrix(ctx context.Context, m *ExperimentMatrix) error {
	now := time.Now()
	if m.ID == "" {
		m.ID = fmt.Sprintf("matrix_%d", now.UnixNano())
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	if strings.TrimSpace(m.DataJSON) == "" {
		m.DataJSON = "{}"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO experiment_matrices (`+experimentMatrixColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Title, m.Description, m.SourceKind, m.SourceID, m.SourceName, m.DefaultMetricKey, m.DefaultMetricGoal, m.DataJSON, m.CreatedAt, m.UpdatedAt,
	)
	return err
}

func (s *SQLite) GetExperimentMatrix(ctx context.Context, id string) (*ExperimentMatrix, error) {
	m := &ExperimentMatrix{}
	err := scanExperimentMatrix(m)(s.db.QueryRowContext(ctx, `SELECT `+experimentMatrixColumns+` FROM experiment_matrices WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return m, err
}

func (s *SQLite) ListExperimentMatrices(ctx context.Context, filter ExperimentMatrixFilter) ([]ExperimentMatrix, error) {
	query := `SELECT ` + experimentMatrixColumns + ` FROM experiment_matrices WHERE 1=1`
	var args []interface{}
	if q := strings.TrimSpace(filter.Query); q != "" {
		like := "%" + strings.ToLower(q) + "%"
		query += " AND (LOWER(title) LIKE ? OR LOWER(description) LIKE ? OR LOWER(source_name) LIKE ? OR LOWER(id) LIKE ?)"
		args = append(args, like, like, like, like)
	}
	if filter.SourceKind != "" {
		query += " AND source_kind = ?"
		args = append(args, filter.SourceKind)
	}
	if filter.SourceID != "" {
		query += " AND source_id = ?"
		args = append(args, filter.SourceID)
	}
	query += " ORDER BY updated_at DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	matrices := make([]ExperimentMatrix, 0)
	for rows.Next() {
		var m ExperimentMatrix
		if err := scanExperimentMatrix(&m)(rows); err != nil {
			return nil, err
		}
		matrices = append(matrices, m)
	}
	return matrices, rows.Err()
}

func (s *SQLite) UpdateExperimentMatrix(ctx context.Context, m *ExperimentMatrix) error {
	m.UpdatedAt = time.Now()
	if strings.TrimSpace(m.DataJSON) == "" {
		m.DataJSON = "{}"
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE experiment_matrices SET title = ?, description = ?, source_kind = ?, source_id = ?, source_name = ?, default_metric_key = ?, default_metric_goal = ?, data_json = ?, updated_at = ? WHERE id = ?`,
		m.Title, m.Description, m.SourceKind, m.SourceID, m.SourceName, m.DefaultMetricKey, m.DefaultMetricGoal, m.DataJSON, m.UpdatedAt, m.ID,
	)
	return err
}

func (s *SQLite) DeleteExperimentMatrix(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM experiment_matrix_cells WHERE matrix_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM experiment_matrix_columns WHERE matrix_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM experiment_matrix_rows WHERE matrix_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM experiment_matrices WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

const experimentMatrixRowColumns = "id, matrix_id, label, position, data_json, created_at, updated_at"
const experimentMatrixColumnColumns = "id, matrix_id, label, position, data_json, created_at, updated_at"
const experimentMatrixCellColumns = "id, matrix_id, row_id, column_id, run_id, project_card_id, title, statement, metric_key, metric_value, note, data_json, created_at, updated_at"

func scanExperimentMatrixRow(r *ExperimentMatrixRow) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&r.ID, &r.MatrixID, &r.Label, &r.Position, &r.DataJSON, &r.CreatedAt, &r.UpdatedAt)
	}
}

func scanExperimentMatrixColumn(c *ExperimentMatrixColumn) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&c.ID, &c.MatrixID, &c.Label, &c.Position, &c.DataJSON, &c.CreatedAt, &c.UpdatedAt)
	}
}

func scanExperimentMatrixCell(c *ExperimentMatrixCell) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&c.ID, &c.MatrixID, &c.RowID, &c.ColumnID, &c.RunID, &c.ProjectCardID, &c.Title, &c.Statement, &c.MetricKey, &c.MetricValue, &c.Note, &c.DataJSON, &c.CreatedAt, &c.UpdatedAt)
	}
}

func (s *SQLite) GetExperimentMatrixGrid(ctx context.Context, matrixID string) (*ExperimentMatrixGrid, error) {
	rows, err := s.listExperimentMatrixRows(ctx, matrixID)
	if err != nil {
		return nil, err
	}
	columns, err := s.listExperimentMatrixColumns(ctx, matrixID)
	if err != nil {
		return nil, err
	}
	cells, err := s.listExperimentMatrixCells(ctx, matrixID)
	if err != nil {
		return nil, err
	}
	return &ExperimentMatrixGrid{Rows: rows, Columns: columns, Cells: cells}, nil
}

func (s *SQLite) listExperimentMatrixRows(ctx context.Context, matrixID string) ([]ExperimentMatrixRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+experimentMatrixRowColumns+` FROM experiment_matrix_rows WHERE matrix_id = ? ORDER BY position, updated_at DESC`, matrixID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ExperimentMatrixRow, 0)
	for rows.Next() {
		var row ExperimentMatrixRow
		if err := scanExperimentMatrixRow(&row)(rows); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *SQLite) listExperimentMatrixColumns(ctx context.Context, matrixID string) ([]ExperimentMatrixColumn, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+experimentMatrixColumnColumns+` FROM experiment_matrix_columns WHERE matrix_id = ? ORDER BY position, updated_at DESC`, matrixID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ExperimentMatrixColumn, 0)
	for rows.Next() {
		var column ExperimentMatrixColumn
		if err := scanExperimentMatrixColumn(&column)(rows); err != nil {
			return nil, err
		}
		out = append(out, column)
	}
	return out, rows.Err()
}

func (s *SQLite) listExperimentMatrixCells(ctx context.Context, matrixID string) ([]ExperimentMatrixCell, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+experimentMatrixCellColumns+` FROM experiment_matrix_cells WHERE matrix_id = ? ORDER BY updated_at DESC`, matrixID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ExperimentMatrixCell, 0)
	for rows.Next() {
		var cell ExperimentMatrixCell
		if err := scanExperimentMatrixCell(&cell)(rows); err != nil {
			return nil, err
		}
		out = append(out, cell)
	}
	return out, rows.Err()
}

func (s *SQLite) SaveExperimentMatrixGrid(ctx context.Context, matrixID string, grid ExperimentMatrixGrid) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now()
	if _, err := tx.ExecContext(ctx, `DELETE FROM experiment_matrix_cells WHERE matrix_id = ?`, matrixID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM experiment_matrix_columns WHERE matrix_id = ?`, matrixID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM experiment_matrix_rows WHERE matrix_id = ?`, matrixID); err != nil {
		return err
	}
	for _, row := range grid.Rows {
		if row.CreatedAt.IsZero() {
			row.CreatedAt = now
		}
		row.UpdatedAt = now
		if strings.TrimSpace(row.DataJSON) == "" {
			row.DataJSON = "{}"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO experiment_matrix_rows (`+experimentMatrixRowColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			row.ID, matrixID, row.Label, row.Position, row.DataJSON, row.CreatedAt, row.UpdatedAt,
		); err != nil {
			return err
		}
	}
	for _, column := range grid.Columns {
		if column.CreatedAt.IsZero() {
			column.CreatedAt = now
		}
		column.UpdatedAt = now
		if strings.TrimSpace(column.DataJSON) == "" {
			column.DataJSON = "{}"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO experiment_matrix_columns (`+experimentMatrixColumnColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			column.ID, matrixID, column.Label, column.Position, column.DataJSON, column.CreatedAt, column.UpdatedAt,
		); err != nil {
			return err
		}
	}
	for _, cell := range grid.Cells {
		if cell.CreatedAt.IsZero() {
			cell.CreatedAt = now
		}
		cell.UpdatedAt = now
		if strings.TrimSpace(cell.DataJSON) == "" {
			cell.DataJSON = "{}"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO experiment_matrix_cells (`+experimentMatrixCellColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cell.ID, matrixID, cell.RowID, cell.ColumnID, cell.RunID, cell.ProjectCardID, cell.Title, cell.Statement, cell.MetricKey, cell.MetricValue, cell.Note, cell.DataJSON, cell.CreatedAt, cell.UpdatedAt,
		); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE experiment_matrices SET updated_at = ? WHERE id = ?`, now, matrixID); err != nil {
		return err
	}
	return tx.Commit()
}

// --- Evidence Chains ---

const evidenceChainColumns = "id, title, description, created_at, updated_at"

func scanEvidenceChain(c *EvidenceChain) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&c.ID, &c.Title, &c.Description, &c.CreatedAt, &c.UpdatedAt)
	}
}

func (s *SQLite) CreateEvidenceChain(ctx context.Context, c *EvidenceChain) error {
	now := time.Now()
	if c.ID == "" {
		c.ID = fmt.Sprintf("chain_%d", now.UnixNano())
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO evidence_chains (`+evidenceChainColumns+`) VALUES (?, ?, ?, ?, ?)`,
		c.ID, c.Title, c.Description, c.CreatedAt, c.UpdatedAt,
	)
	return err
}

func (s *SQLite) GetEvidenceChain(ctx context.Context, id string) (*EvidenceChain, error) {
	c := &EvidenceChain{}
	err := scanEvidenceChain(c)(s.db.QueryRowContext(ctx, `SELECT `+evidenceChainColumns+` FROM evidence_chains WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

func (s *SQLite) ListEvidenceChains(ctx context.Context, filter EvidenceChainFilter) ([]EvidenceChain, error) {
	query := `SELECT ` + evidenceChainColumns + ` FROM evidence_chains WHERE 1=1`
	var args []interface{}
	if q := strings.TrimSpace(filter.Query); q != "" {
		like := "%" + strings.ToLower(q) + "%"
		query += " AND (LOWER(title) LIKE ? OR LOWER(description) LIKE ? OR LOWER(id) LIKE ?)"
		args = append(args, like, like, like)
	}
	query += " ORDER BY updated_at DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chains := make([]EvidenceChain, 0)
	for rows.Next() {
		var c EvidenceChain
		if err := scanEvidenceChain(&c)(rows); err != nil {
			return nil, err
		}
		chains = append(chains, c)
	}
	return chains, rows.Err()
}

func (s *SQLite) UpdateEvidenceChain(ctx context.Context, c *EvidenceChain) error {
	c.UpdatedAt = time.Now()
	_, err := s.db.ExecContext(ctx,
		`UPDATE evidence_chains SET title = ?, description = ?, updated_at = ? WHERE id = ?`,
		c.Title, c.Description, c.UpdatedAt, c.ID,
	)
	return err
}

func (s *SQLite) DeleteEvidenceChain(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM evidence_chain_edges WHERE chain_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM evidence_chain_nodes WHERE chain_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM evidence_chains WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

const evidenceChainNodeColumns = "id, chain_id, type, title, body, run_id, project_card_id, x, y, width, height, data_json, created_at, updated_at"

func scanEvidenceChainNode(n *EvidenceChainNode) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&n.ID, &n.ChainID, &n.Type, &n.Title, &n.Body, &n.RunID, &n.ProjectCardID, &n.X, &n.Y, &n.Width, &n.Height, &n.DataJSON, &n.CreatedAt, &n.UpdatedAt)
	}
}

const evidenceChainEdgeColumns = "id, chain_id, source_node_id, target_node_id, type, label, rationale, data_json, created_at, updated_at"

func scanEvidenceChainEdge(e *EvidenceChainEdge) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&e.ID, &e.ChainID, &e.SourceNodeID, &e.TargetNodeID, &e.Type, &e.Label, &e.Rationale, &e.DataJSON, &e.CreatedAt, &e.UpdatedAt)
	}
}

func (s *SQLite) GetEvidenceChainGraph(ctx context.Context, chainID string) (*EvidenceChainGraph, error) {
	nodes, err := s.listEvidenceChainNodes(ctx, chainID)
	if err != nil {
		return nil, err
	}
	edges, err := s.listEvidenceChainEdges(ctx, chainID)
	if err != nil {
		return nil, err
	}
	return &EvidenceChainGraph{Nodes: nodes, Edges: edges}, nil
}

func (s *SQLite) listEvidenceChainNodes(ctx context.Context, chainID string) ([]EvidenceChainNode, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+evidenceChainNodeColumns+` FROM evidence_chain_nodes WHERE chain_id = ? ORDER BY updated_at DESC`, chainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := make([]EvidenceChainNode, 0)
	for rows.Next() {
		var n EvidenceChainNode
		if err := scanEvidenceChainNode(&n)(rows); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func (s *SQLite) listEvidenceChainEdges(ctx context.Context, chainID string) ([]EvidenceChainEdge, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+evidenceChainEdgeColumns+` FROM evidence_chain_edges WHERE chain_id = ? ORDER BY updated_at DESC`, chainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	edges := make([]EvidenceChainEdge, 0)
	for rows.Next() {
		var e EvidenceChainEdge
		if err := scanEvidenceChainEdge(&e)(rows); err != nil {
			return nil, err
		}
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

func (s *SQLite) SaveEvidenceChainGraph(ctx context.Context, chainID string, graph EvidenceChainGraph) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now()
	if _, err := tx.ExecContext(ctx, `DELETE FROM evidence_chain_edges WHERE chain_id = ?`, chainID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM evidence_chain_nodes WHERE chain_id = ?`, chainID); err != nil {
		return err
	}
	for _, n := range graph.Nodes {
		if n.CreatedAt.IsZero() {
			n.CreatedAt = now
		}
		n.UpdatedAt = now
		if n.Width <= 0 {
			n.Width = 260
		}
		if n.Height <= 0 {
			n.Height = 140
		}
		if strings.TrimSpace(n.DataJSON) == "" {
			n.DataJSON = "{}"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO evidence_chain_nodes (`+evidenceChainNodeColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			n.ID, chainID, n.Type, n.Title, n.Body, n.RunID, n.ProjectCardID, n.X, n.Y, n.Width, n.Height, n.DataJSON, n.CreatedAt, n.UpdatedAt,
		); err != nil {
			return err
		}
	}
	for _, e := range graph.Edges {
		if e.CreatedAt.IsZero() {
			e.CreatedAt = now
		}
		e.UpdatedAt = now
		if strings.TrimSpace(e.DataJSON) == "" {
			e.DataJSON = "{}"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO evidence_chain_edges (`+evidenceChainEdgeColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, chainID, e.SourceNodeID, e.TargetNodeID, e.Type, e.Label, e.Rationale, e.DataJSON, e.CreatedAt, e.UpdatedAt,
		); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE evidence_chains SET updated_at = ? WHERE id = ?`, now, chainID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) ListEvidenceRunCandidates(ctx context.Context, filter EvidenceRunCandidateFilter) ([]EvidenceChainRunCandidate, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 80
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	out := make([]EvidenceChainRunCandidate, 0, limit)
	seenRuns := map[string]bool{}

	cards, err := s.ListProjectRunCards(ctx, ProjectRunCardFilter{Limit: limit * 4})
	if err != nil {
		return nil, err
	}
	for _, card := range cards {
		if len(out) >= limit {
			break
		}
		run, _ := s.GetRun(ctx, card.RunID)
		candidate := EvidenceChainRunCandidate{
			ID:             "card:" + card.ID,
			Kind:           "project_card",
			RunID:          card.RunID,
			ProjectCardID:  card.ID,
			ProjectID:      card.ProjectID,
			ProjectName:    card.ProjectName,
			Question:       card.Question,
			Verdict:        card.Verdict,
			EvidenceLevel:  card.EvidenceLevel,
			KeyMetrics:     card.KeyMetrics,
			NextAction:     card.NextAction,
			Run:            run,
			ProjectRunCard: &card,
		}
		if evidenceCandidateMatches(candidate, query) {
			out = append(out, candidate)
			if card.RunID != "" {
				seenRuns[card.RunID] = true
			}
		}
	}

	if len(out) >= limit {
		return out, nil
	}
	runs, err := s.ListRuns(ctx, RunFilter{Limit: limit * 4})
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		if len(out) >= limit {
			break
		}
		if seenRuns[run.ID] {
			continue
		}
		runCopy := run
		candidate := EvidenceChainRunCandidate{
			ID:    "run:" + run.ID,
			Kind:  "run",
			RunID: run.ID,
			Run:   &runCopy,
		}
		if evidenceCandidateMatches(candidate, query) {
			out = append(out, candidate)
		}
	}
	return out, nil
}

func evidenceCandidateMatches(candidate EvidenceChainRunCandidate, query string) bool {
	if query == "" {
		return true
	}
	parts := []string{
		candidate.ID,
		candidate.Kind,
		candidate.RunID,
		candidate.ProjectCardID,
		candidate.ProjectID,
		candidate.ProjectName,
		candidate.Question,
		candidate.Verdict,
		candidate.EvidenceLevel,
		candidate.KeyMetrics,
		candidate.NextAction,
	}
	if candidate.Run != nil {
		parts = append(parts, candidate.Run.ID, candidate.Run.Name, candidate.Run.Kind, candidate.Run.Status, candidate.Run.Command, candidate.Run.Cwd, candidate.Run.ResolvedCwd)
	}
	return textPartsContain(parts, query)
}

func textPartsContain(parts []string, query string) bool {
	for _, part := range parts {
		if strings.Contains(strings.ToLower(part), query) {
			return true
		}
	}
	return false
}

// --- Exec Events ---

const execEventColumns = "id, resource_id, actor, command, cwd, exit_code, started_at, finished_at, duration_ms, stdout_tail, stderr_tail, created_at"

func scanExecEvent(e *ExecEvent) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&e.ID, &e.ResourceID, &e.Actor, &e.Command, &e.Cwd,
			&e.ExitCode, &e.StartedAt, &e.FinishedAt, &e.DurationMs,
			&e.StdoutTail, &e.StderrTail, &e.CreatedAt)
	}
}

func (s *SQLite) SaveExecEvent(ctx context.Context, e *ExecEvent) error {
	e.CreatedAt = time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO exec_events (id, resource_id, actor, command, cwd, exit_code, started_at, finished_at, duration_ms, stdout_tail, stderr_tail, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.ResourceID, e.Actor, e.Command, e.Cwd, e.ExitCode,
		e.StartedAt, e.FinishedAt, e.DurationMs, e.StdoutTail, e.StderrTail, e.CreatedAt,
	)
	return err
}

func (s *SQLite) GetExecEvent(ctx context.Context, id string) (*ExecEvent, error) {
	e := &ExecEvent{}
	err := scanExecEvent(e)(s.db.QueryRowContext(ctx,
		`SELECT `+execEventColumns+` FROM exec_events WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return e, err
}

func (s *SQLite) ListExecEvents(ctx context.Context, filter ExecEventFilter) ([]ExecEvent, error) {
	where, args := execEventFilterWhere(filter)
	query := `SELECT ` + execEventColumns + ` FROM exec_events` + where

	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]ExecEvent, 0)
	for rows.Next() {
		var e ExecEvent
		if err := scanExecEvent(&e)(rows); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *SQLite) CountExecEvents(ctx context.Context, filter ExecEventFilter) (int, error) {
	where, args := execEventFilterWhere(filter)
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM exec_events`+where, args...).Scan(&count)
	return count, err
}

func execEventFilterWhere(filter ExecEventFilter) (string, []interface{}) {
	query := ` WHERE 1=1`
	var args []interface{}

	if filter.ResourceID != "" {
		query += " AND resource_id = ?"
		args = append(args, filter.ResourceID)
	}
	if filter.Actor != "" {
		query += " AND actor = ?"
		args = append(args, filter.Actor)
	}

	return query, args
}

// --- helpers ---

// stringsJoin is a helper for building comma-separated strings.
func stringsJoin(elems []string, sep string) string {
	return strings.Join(elems, sep)
}
