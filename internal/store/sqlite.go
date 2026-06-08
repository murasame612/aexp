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
	addColumn("resources", "socks_proxy", "TEXT", "''")
	addColumn("resources", "proxy_command", "TEXT", "''")
	addColumn("resources", "os_type", "TEXT", "''")
	addColumn("resources", "conda_base", "TEXT", "''")
	addColumn("resources", "conda_init", "TEXT", "''")

	return nil
}

func (s *SQLite) Close() error {
	return s.db.Close()
}

// --- Resources ---

const resourceColumns = "id, name, type, host, os_type, port, user, auth_ref, socks_proxy, proxy_command, root_dir, conda_base, conda_init, conda_env, gpu_indices, tags, status, created_at, updated_at"

func (s *SQLite) CreateResource(ctx context.Context, r *Resource) error {
	r.CreatedAt = time.Now()
	r.UpdatedAt = time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO resources (`+resourceColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, r.Type, r.Host, r.OSType, r.Port, r.User, r.AuthRef, r.SocksProxy, r.ProxyCommand, r.RootDir, r.CondaBase, r.CondaInit, r.CondaEnv, r.GPUIndices, r.Tags, r.Status, r.CreatedAt, r.UpdatedAt,
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
		`UPDATE resources SET name=?, type=?, host=?, os_type=?, port=?, user=?, auth_ref=?, socks_proxy=?, proxy_command=?, root_dir=?, conda_base=?, conda_init=?, conda_env=?, gpu_indices=?, tags=?, status=?, updated_at=? WHERE id=?`,
		r.Name, r.Type, r.Host, r.OSType, r.Port, r.User, r.AuthRef, r.SocksProxy, r.ProxyCommand, r.RootDir, r.CondaBase, r.CondaInit, r.CondaEnv, r.GPUIndices, r.Tags, r.Status, r.UpdatedAt, r.ID,
	)
	return err
}

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanResource(r *Resource) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&r.ID, &r.Name, &r.Type, &r.Host, &r.OSType, &r.Port, &r.User, &r.AuthRef, &r.SocksProxy, &r.ProxyCommand, &r.RootDir, &r.CondaBase, &r.CondaInit, &r.CondaEnv, &r.GPUIndices, &r.Tags, &r.Status, &r.CreatedAt, &r.UpdatedAt)
	}
}

func (s *SQLite) DeleteResource(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM resources WHERE id = ?`, id)
	return err
}

// --- Runs ---

func (s *SQLite) CreateRun(ctx context.Context, r *Run) error {
	r.CreatedAt = time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO runs (id, resource_id, name, status, kind, gpu_index, cwd, command, program, args_json, conda_env, project_env, resolved_env, resolved_python, resolved_cwd, env_json, log_paths_json, artifact_paths_json, metric_paths_json, tmux_session, remote_run_dir, exit_code, created_by, created_at, started_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ResourceID, r.Name, r.Status, r.Kind, r.GPUIndex, r.Cwd, r.Command, r.Program, r.ArgsJSON, r.CondaEnv, r.ProjectEnv, r.ResolvedEnv, r.ResolvedPython, r.ResolvedCwd, r.EnvJSON, r.LogPathsJSON, r.ArtifactPathsJSON, r.MetricPathsJSON, r.TmuxSession, r.RemoteRunDir, r.ExitCode, r.CreatedBy, r.CreatedAt, r.StartedAt, r.FinishedAt,
	)
	return err
}

func (s *SQLite) GetRun(ctx context.Context, id string) (*Run, error) {
	r := &Run{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, resource_id, name, status, kind, gpu_index, cwd, command, program, args_json, conda_env, project_env, resolved_env, resolved_python, resolved_cwd, env_json, log_paths_json, artifact_paths_json, metric_paths_json, tmux_session, remote_run_dir, exit_code, created_by, created_at, started_at, finished_at FROM runs WHERE id = ?`, id).
		Scan(&r.ID, &r.ResourceID, &r.Name, &r.Status, &r.Kind, &r.GPUIndex, &r.Cwd, &r.Command, &r.Program, &r.ArgsJSON, &r.CondaEnv, &r.ProjectEnv, &r.ResolvedEnv, &r.ResolvedPython, &r.ResolvedCwd, &r.EnvJSON, &r.LogPathsJSON, &r.ArtifactPathsJSON, &r.MetricPathsJSON, &r.TmuxSession, &r.RemoteRunDir, &r.ExitCode, &r.CreatedBy, &r.CreatedAt, &r.StartedAt, &r.FinishedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

func (s *SQLite) ListRuns(ctx context.Context, filter RunFilter) ([]Run, error) {
	query := `SELECT id, resource_id, name, status, kind, gpu_index, cwd, command, program, args_json, conda_env, project_env, resolved_env, resolved_python, resolved_cwd, env_json, log_paths_json, artifact_paths_json, metric_paths_json, tmux_session, remote_run_dir, exit_code, created_by, created_at, started_at, finished_at FROM runs WHERE 1=1`
	var args []interface{}

	if filter.ResourceID != "" {
		query += " AND resource_id = ?"
		args = append(args, filter.ResourceID)
	}
	if filter.Status != "" {
		query += " AND status = ?"
		args = append(args, filter.Status)
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

	runs := make([]Run, 0)
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.ResourceID, &r.Name, &r.Status, &r.Kind, &r.GPUIndex, &r.Cwd, &r.Command, &r.Program, &r.ArgsJSON, &r.CondaEnv, &r.ProjectEnv, &r.ResolvedEnv, &r.ResolvedPython, &r.ResolvedCwd, &r.EnvJSON, &r.LogPathsJSON, &r.ArtifactPathsJSON, &r.MetricPathsJSON, &r.TmuxSession, &r.RemoteRunDir, &r.ExitCode, &r.CreatedBy, &r.CreatedAt, &r.StartedAt, &r.FinishedAt); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (s *SQLite) UpdateRun(ctx context.Context, r *Run) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET name=?, status=?, kind=?, gpu_index=?, cwd=?, command=?, program=?, args_json=?, conda_env=?, project_env=?, resolved_env=?, resolved_python=?, resolved_cwd=?, env_json=?, log_paths_json=?, artifact_paths_json=?, metric_paths_json=?, tmux_session=?, remote_run_dir=?, exit_code=?, created_by=?, started_at=?, finished_at=? WHERE id=?`,
		r.Name, r.Status, r.Kind, r.GPUIndex, r.Cwd, r.Command, r.Program, r.ArgsJSON, r.CondaEnv, r.ProjectEnv, r.ResolvedEnv, r.ResolvedPython, r.ResolvedCwd, r.EnvJSON, r.LogPathsJSON, r.ArtifactPathsJSON, r.MetricPathsJSON, r.TmuxSession, r.RemoteRunDir, r.ExitCode, r.CreatedBy, r.StartedAt, r.FinishedAt, r.ID,
	)
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

const runMarkColumns = "id, run_id, actor, kind, title, reason, evidence, created_at"

func scanRunMark(m *RunMark) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&m.ID, &m.RunID, &m.Actor, &m.Kind, &m.Title, &m.Reason, &m.Evidence, &m.CreatedAt)
	}
}

func (s *SQLite) SaveRunMark(ctx context.Context, m *RunMark) error {
	m.CreatedAt = time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO run_marks (id, run_id, actor, kind, title, reason, evidence, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.RunID, m.Actor, m.Kind, m.Title, m.Reason, m.Evidence, m.CreatedAt,
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
		marks = append(marks, m)
	}
	return marks, rows.Err()
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
	query := `SELECT ` + execEventColumns + ` FROM exec_events WHERE 1=1`
	var args []interface{}

	if filter.ResourceID != "" {
		query += " AND resource_id = ?"
		args = append(args, filter.ResourceID)
	}
	if filter.Actor != "" {
		query += " AND actor = ?"
		args = append(args, filter.Actor)
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

// --- helpers ---

// stringsJoin is a helper for building comma-separated strings.
func stringsJoin(elems []string, sep string) string {
	return strings.Join(elems, sep)
}
