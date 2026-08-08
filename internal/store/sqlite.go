package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	separator := "?"
	if strings.Contains(dbPath, "?") {
		separator = "&"
	}
	// _pragma applies to every connection opened by database/sql, unlike a
	// one-off PRAGMA executed on whichever pooled connection happens to be used.
	dsn := dbPath + separator + "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
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

	// Add missing columns before creating indexes that reference columns absent
	// from older databases. SQLite evaluates CREATE INDEX even when the table
	// already exists, so those indexes cannot live in the base schema.
	if err := s.migrateColumns(); err != nil {
		return err
	}
	if err := s.reconcileMissingResourceReferences(); err != nil {
		return err
	}
	if err := s.reconcileLegacyProjects(); err != nil {
		return err
	}
	return s.migrateIndexes()
}

// reconcileLegacyProjects promotes the old evidence-card project labels into
// canonical Project records without deleting or rewriting the legacy records.
// Historical runs created before runs.project_id existed inherit the project
// only when their run card supplies one unambiguous project identity.
func (s *SQLite) reconcileLegacyProjects() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin legacy project reconciliation: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
INSERT OR IGNORE INTO project_definitions (
	id, name, description, local_root, config_path, config_hash, source_repo, default_recipe,
	vault, run_card_index, proposal_dir, promotion_default, aggregate_command, gate_command,
	zotero_collection_key, literature_service_profile,
	created_at, updated_at
)
SELECT project_id,
       COALESCE(NULLIF(MAX(project_name), ''), project_id),
       'Imported from the legacy evidence-card project index.',
       '', '', '', '', '', '', '', '', '', '', '', '', '',
       MIN(created_at), MAX(updated_at)
FROM project_run_cards
WHERE TRIM(project_id) <> '' AND project_id <> 'unassigned'
GROUP BY project_id`); err != nil {
		return fmt.Errorf("import legacy project definitions: %w", err)
	}

	if _, err := tx.Exec(`
UPDATE runs
SET project_id = (
	SELECT MIN(project_id)
	FROM project_run_cards
	WHERE project_run_cards.run_id = runs.id
	  AND TRIM(project_run_cards.project_id) <> ''
	  AND project_run_cards.project_id <> 'unassigned'
)
WHERE TRIM(COALESCE(project_id, '')) = ''
  AND (
	SELECT COUNT(DISTINCT project_id)
	FROM project_run_cards
	WHERE project_run_cards.run_id = runs.id
	  AND TRIM(project_run_cards.project_id) <> ''
	  AND project_run_cards.project_id <> 'unassigned'
  ) = 1`); err != nil {
		return fmt.Errorf("link legacy runs to canonical projects: %w", err)
	}

	rows, err := tx.Query(`SELECT ` + projectDefinitionColumns + ` FROM project_definitions ORDER BY id`)
	if err != nil {
		return fmt.Errorf("list projects for primary map reconciliation: %w", err)
	}
	projects := make([]ProjectDefinition, 0)
	for rows.Next() {
		var project ProjectDefinition
		if err := scanProjectDefinition(rows, &project); err != nil {
			rows.Close()
			return fmt.Errorf("scan project for primary map reconciliation: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close project reconciliation rows: %w", err)
	}
	for index := range projects {
		chain := primaryEvidenceChainForProject(&projects[index], projects[index].CreatedAt)
		routingHints, _ := json.Marshal(chain.RoutingHints)
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO evidence_chains (`+evidenceChainColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			chain.ID, chain.Title, chain.Description, string(routingHints), chain.ProjectID, chain.Role, chain.Status,
			chain.Revision, chain.GraphHash, chain.CreatedAt, chain.UpdatedAt,
		); err != nil {
			return fmt.Errorf("create primary evidence map for imported project %q: %w", projects[index].ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy project reconciliation: %w", err)
	}
	return nil
}

func (s *SQLite) reconcileMissingResourceReferences() error {
	const statement = `
INSERT OR IGNORE INTO resources (id, name, type, host, port, user, root_dir, status, ssh_status, last_doctor_error)
SELECT missing.resource_id,
       'deleted-' || missing.resource_id,
       'tombstone',
       'deleted.invalid',
       22,
       'deleted',
       '/',
       'deleted',
       'unknown',
       'historical resource metadata was deleted; this inert tombstone preserves provenance'
FROM (
    SELECT resource_id FROM runs
    UNION SELECT resource_id FROM resource_snapshots
    UNION SELECT resource_id FROM project_profiles
    UNION SELECT resource_id FROM project_targets
    UNION SELECT resource_id FROM exec_events
    UNION SELECT resource_id FROM storage_targets
    UNION SELECT resource_id FROM path_placements
    UNION SELECT resource_id FROM dataset_materializations
) AS missing
LEFT JOIN resources ON resources.id = missing.resource_id
WHERE missing.resource_id <> '' AND resources.id IS NULL`
	if _, err := s.db.Exec(statement); err != nil {
		return fmt.Errorf("reconcile deleted historical resources: %w", err)
	}
	return nil
}

func (s *SQLite) migrateIndexes() error {
	const indexes = `
CREATE INDEX IF NOT EXISTS idx_runs_project_created ON runs(project_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_target_created ON runs(target_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_archive_delete_created ON runs(archived_at, deleted_at, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_evidence_chains_primary_project
ON evidence_chains(project_id)
WHERE project_id <> '' AND role = 'primary' AND status = 'active';`
	if _, err := s.db.Exec(indexes); err != nil {
		return fmt.Errorf("create post-column indexes: %w", err)
	}
	return nil
}

// migrateColumns adds columns that may not exist in older databases.
func (s *SQLite) migrateColumns() error {
	columnExists := func(table, column string) bool {
		rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
		if err != nil {
			return false
		}
		defer rows.Close()
		for rows.Next() {
			var cid int
			var name, colType string
			var notNull, primaryKey int
			var defaultValue interface{}
			if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &primaryKey); err == nil && name == column {
				return true
			}
		}
		return false
	}
	addColumn := func(table, column, colType, defaultValue string) {
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s DEFAULT %s", table, column, colType, defaultValue)
		s.db.Exec(stmt) // ignore error (column already exists)
	}

	printerEnabledEpochExisted := columnExists("printer_settings", "enabled_from_event_seq")
	addColumn("runs", "kind", "TEXT NOT NULL", "'formal'")
	addColumn("printer_settings", "enabled_from_event_seq", "INTEGER NOT NULL", "0")
	if !printerEnabledEpochExisted {
		_, _ = s.db.Exec(`UPDATE printer_settings SET enabled_from_event_seq=last_event_seq WHERE enabled=1`)
	}
	addColumn("runs", "task_role", "TEXT NOT NULL", "'other'")
	addColumn("runs", "evidence_grade", "TEXT NOT NULL", "'formal'")
	addColumn("runs", "experiment_role", "TEXT NOT NULL", "'unspecified'")
	// Backfill legacy rows only while all three newly added columns still carry
	// their migration defaults. Never overwrite explicitly authored semantics.
	_, _ = s.db.Exec(`UPDATE runs SET task_role='prepare', evidence_grade='none', experiment_role='unspecified' WHERE kind='setup' AND task_role='other' AND evidence_grade='formal' AND experiment_role='unspecified'`)
	_, _ = s.db.Exec(`UPDATE runs SET evidence_grade='smoke' WHERE kind='smoke' AND task_role='other' AND evidence_grade='formal' AND experiment_role='unspecified'`)
	_, _ = s.db.Exec(`UPDATE runs SET evidence_grade='pilot' WHERE kind='pilot' AND task_role='other' AND evidence_grade='formal' AND experiment_role='unspecified'`)
	_, _ = s.db.Exec(`UPDATE runs SET experiment_role='ablation' WHERE kind='ablation' AND task_role='other' AND evidence_grade='formal' AND experiment_role='unspecified'`)
	addColumn("runs", "project_id", "TEXT", "''")
	addColumn("runs", "target_id", "TEXT", "''")
	addColumn("runs", "recipe_name", "TEXT", "''")
	addColumn("runs", "project_config_sha256", "TEXT", "''")
	addColumn("storage_targets", "health_json", "TEXT NOT NULL", "'{}'")
	addColumn("runs", "datasets_json", "TEXT", "'[]'")
	addColumn("runs", "seeds_json", "TEXT", "'[]'")
	addColumn("runs", "split_protocol", "TEXT", "''")
	addColumn("runs", "evaluation_protocol", "TEXT", "''")
	addColumn("runs", "data_finalization_state", "TEXT NOT NULL", "'pending'")
	addColumn("runs", "data_finalization_error", "TEXT", "''")
	addColumn("runs", "data_finalization_updated_at", "DATETIME", "NULL")
	addColumn("runs", "status_source", "TEXT", "'local_cache'")
	addColumn("runs", "status_observed_at", "DATETIME", "NULL")
	addColumn("runs", "status_checked_at", "DATETIME", "NULL")
	addColumn("runs", "status_check_error", "TEXT", "''")
	addColumn("runs", "gpu_index", "INTEGER NOT NULL", "-1")
	addColumn("runs", "program", "TEXT", "''")
	addColumn("runs", "args_json", "TEXT", "'[]'")
	addColumn("runs", "project_env", "TEXT", "''")
	addColumn("runs", "target_env", "TEXT", "''")
	addColumn("runs", "force_reason", "TEXT", "''")
	addColumn("runs", "preempt_run_id", "TEXT", "''")
	addColumn("runs", "preempt_save", "BOOLEAN NOT NULL", "0")
	addColumn("runs", "git_repo_root", "TEXT", "''")
	addColumn("runs", "git_remote_url", "TEXT", "''")
	addColumn("runs", "git_branch", "TEXT", "''")
	addColumn("runs", "git_commit", "TEXT", "''")
	addColumn("runs", "git_dirty", "BOOLEAN NOT NULL", "0")
	addColumn("runs", "git_status", "TEXT", "''")
	addColumn("runs", "git_diff_hash", "TEXT", "''")
	addColumn("runs", "git_diff_path", "TEXT", "''")
	addColumn("runs", "git_allow_dirty", "BOOLEAN NOT NULL", "0")
	addColumn("runs", "resolved_env", "TEXT", "''")
	addColumn("runs", "resolved_python", "TEXT", "''")
	addColumn("runs", "resolved_cwd", "TEXT", "''")
	addColumn("runs", "ui_events_path", "TEXT", "''")
	addColumn("runs", "failure_kind", "TEXT", "''")
	addColumn("runs", "failure_reason", "TEXT", "''")
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
	addColumn("artifacts", "relative_path", "TEXT", "''")
	addColumn("artifacts", "source_uri", "TEXT", "''")
	addColumn("artifacts", "role", "TEXT", "''")
	addColumn("artifacts", "mime", "TEXT", "''")
	addColumn("artifacts", "sha256", "TEXT", "''")
	addColumn("artifacts", "collection_state", "TEXT NOT NULL", "'indexed'")
	addColumn("artifacts", "collection_error", "TEXT", "''")
	addColumn("artifacts", "discovered_at", "DATETIME", "NULL")
	addColumn("project_run_cards", "project_name", "TEXT", "''")
	addColumn("project_run_cards", "should_promote", "INTEGER NOT NULL", "0")
	addColumn("project_run_cards", "proposal_reason", "TEXT", "''")
	addColumn("project_run_cards", "graph_routing_reason", "TEXT", "''")
	addColumn("project_run_cards", "related_runs", "TEXT", "''")
	addColumn("project_run_cards", "graph_patch_json", "TEXT", "''")
	addColumn("project_run_cards", "graph_status", "TEXT NOT NULL", "'none'")
	addColumn("project_run_cards", "proposal_hash", "TEXT", "''")
	addColumn("project_run_cards", "base_graph_revision", "INTEGER NOT NULL", "0")
	addColumn("project_run_cards", "reviewed_at", "DATETIME", "NULL")
	addColumn("project_run_cards", "no_graph_impact", "INTEGER NOT NULL", "0")
	addColumn("project_run_cards", "graph_impact_reason", "TEXT", "''")
	addColumn("evidence_chains", "project_id", "TEXT", "''")
	addColumn("evidence_chains", "routing_hints_json", "TEXT NOT NULL", "'{}'")
	addColumn("evidence_chains", "role", "TEXT NOT NULL", "'secondary'")
	addColumn("evidence_chains", "status", "TEXT NOT NULL", "'active'")
	addColumn("evidence_chains", "revision", "INTEGER NOT NULL", "0")
	addColumn("evidence_chains", "graph_hash", "TEXT", "''")
	addColumn("evidence_chain_nodes", "pinned", "INTEGER NOT NULL", "0")
	addColumn("evidence_chain_nodes", "occurred_at", "DATETIME", "NULL")
	addColumn("run_marks", "statement", "TEXT", "''")
	addColumn("run_marks", "body_md", "TEXT", "''")
	addColumn("dataset_versions", "logical_uri", "TEXT", "''")
	addColumn("dataset_versions", "revision", "TEXT", "''")
	addColumn("dataset_materializations", "transfer_id", "TEXT", "''")
	addColumn("run_freezes", "raw_transfer_id", "TEXT", "''")
	addColumn("run_freezes", "workspace_transfer_id", "TEXT", "''")
	addColumn("project_definitions", "aggregate_command", "TEXT", "''")
	addColumn("project_definitions", "gate_command", "TEXT", "''")
	addColumn("project_definitions", "zotero_collection_key", "TEXT", "''")
	addColumn("project_definitions", "literature_service_profile", "TEXT", "''")
	addColumn("project_journal_entries", "literature_refs_json", "TEXT NOT NULL", "'[]'")

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
	rows, err := s.db.QueryContext(ctx, `SELECT `+resourceColumns+` FROM resources WHERE type<>? ORDER BY name`, ResourceTypeTombstone)
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
	var storageTargets int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM storage_targets WHERE resource_id=?`, id).Scan(&storageTargets); err != nil {
		return err
	}
	if storageTargets > 0 {
		return fmt.Errorf("resource hosts %d storage target(s); delete the storage target metadata first", storageTargets)
	}
	var historicalReferences int
	if err := s.db.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM runs WHERE resource_id=?) +
  (SELECT COUNT(*) FROM resource_snapshots WHERE resource_id=?) +
  (SELECT COUNT(*) FROM project_profiles WHERE resource_id=?) +
  (SELECT COUNT(*) FROM project_targets WHERE resource_id=?) +
  (SELECT COUNT(*) FROM exec_events WHERE resource_id=?) +
  (SELECT COUNT(*) FROM path_placements WHERE resource_id=?) +
  (SELECT COUNT(*) FROM dataset_materializations WHERE resource_id=?)`, id, id, id, id, id, id, id).Scan(&historicalReferences); err != nil {
		return err
	}
	if historicalReferences > 0 {
		_, err := s.db.ExecContext(ctx, `UPDATE resources SET name=?, type=?, host='deleted.invalid', user='deleted', auth_ref='', socks_proxy='', proxy_command='', root_dir='/', remote_path='', conda_base='', conda_init='', conda_env='', gpu_indices='', tags='', status=?, ssh_status=?, last_doctor_error=?, updated_at=? WHERE id=?`,
			"deleted-"+id, ResourceTypeTombstone, ResourceStatusDeleted, ResourceSSHStatusUnknown, "resource metadata deleted; inert tombstone retained for historical provenance", time.Now(), id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM resources WHERE id = ?`, id)
	return err
}

// --- Runs ---

const runColumns = "id, resource_id, project_id, target_id, recipe_name, project_config_sha256, datasets_json, seeds_json, split_protocol, evaluation_protocol, data_finalization_state, data_finalization_error, data_finalization_updated_at, name, status, status_source, status_observed_at, status_checked_at, status_check_error, kind, task_role, evidence_grade, experiment_role, gpu_index, cwd, command, program, args_json, conda_env, project_env, target_env, force_reason, preempt_run_id, preempt_save, git_repo_root, git_remote_url, git_branch, git_commit, git_dirty, git_status, git_diff_hash, git_diff_path, git_allow_dirty, resolved_env, resolved_python, resolved_cwd, env_json, log_paths_json, artifact_paths_json, metric_paths_json, ui_events_path, tmux_session, remote_run_dir, exit_code, failure_kind, failure_reason, created_by, created_at, started_at, finished_at, archived_at, deleted_at"

func scanRun(r *Run) func(rowScanner) error {
	return func(row rowScanner) error {
		err := row.Scan(&r.ID, &r.ResourceID, &r.ProjectID, &r.TargetID, &r.RecipeName, &r.ProjectConfigSHA256, &r.DatasetsJSON, &r.SeedsJSON, &r.SplitProtocol, &r.EvaluationProtocol, &r.DataFinalizationState, &r.DataFinalizationError, &r.DataFinalizationUpdatedAt, &r.Name, &r.Status, &r.StatusSource, &r.StatusObservedAt, &r.StatusCheckedAt, &r.StatusCheckError, &r.Kind, &r.TaskRole, &r.EvidenceGrade, &r.ExperimentRole, &r.GPUIndex, &r.Cwd, &r.Command, &r.Program, &r.ArgsJSON, &r.CondaEnv, &r.ProjectEnv, &r.TargetEnv, &r.ForceReason, &r.PreemptRunID, &r.PreemptSave, &r.GitRepoRoot, &r.GitRemoteURL, &r.GitBranch, &r.GitCommit, &r.GitDirty, &r.GitStatus, &r.GitDiffHash, &r.GitDiffPath, &r.GitAllowDirty, &r.ResolvedEnv, &r.ResolvedPython, &r.ResolvedCwd, &r.EnvJSON, &r.LogPathsJSON, &r.ArtifactPathsJSON, &r.MetricPathsJSON, &r.UIEventsPath, &r.TmuxSession, &r.RemoteRunDir, &r.ExitCode, &r.FailureKind, &r.FailureReason, &r.CreatedBy, &r.CreatedAt, &r.StartedAt, &r.FinishedAt, &r.ArchivedAt, &r.DeletedAt)
		if err == nil {
			RefreshRunStatusFreshness(r, time.Now())
		}
		return err
	}
}

type contextExecer interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
}

func prepareRunCreate(r *Run) error {
	r.CreatedAt = time.Now()
	if r.DataFinalizationState == "" {
		r.DataFinalizationState = RunDataFinalizationPending
	}
	kind, taskRole, evidenceGrade, experimentRole, err := NormalizeRunSemantics(r.Kind, r.TaskRole, r.EvidenceGrade, r.ExperimentRole)
	if err != nil {
		return err
	}
	r.Kind, r.TaskRole, r.EvidenceGrade, r.ExperimentRole = kind, taskRole, evidenceGrade, experimentRole
	if r.StatusSource == "" {
		r.StatusSource = RunStatusSourceLocalCache
	}
	RefreshRunStatusFreshness(r, r.CreatedAt)
	return nil
}

func insertRun(ctx context.Context, exec contextExecer, r *Run) error {
	_, err := exec.ExecContext(ctx,
		`INSERT INTO runs (`+runColumns+`)
			 VALUES (`+strings.TrimSuffix(strings.Repeat("?, ", 62), ", ")+`)`,
		r.ID, r.ResourceID, r.ProjectID, r.TargetID, r.RecipeName, r.ProjectConfigSHA256, r.DatasetsJSON, r.SeedsJSON, r.SplitProtocol, r.EvaluationProtocol, r.DataFinalizationState, r.DataFinalizationError, r.DataFinalizationUpdatedAt, r.Name, r.Status, r.StatusSource, r.StatusObservedAt, r.StatusCheckedAt, r.StatusCheckError, r.Kind, r.TaskRole, r.EvidenceGrade, r.ExperimentRole, r.GPUIndex, r.Cwd, r.Command, r.Program, r.ArgsJSON, r.CondaEnv, r.ProjectEnv, r.TargetEnv, r.ForceReason, r.PreemptRunID, r.PreemptSave, r.GitRepoRoot, r.GitRemoteURL, r.GitBranch, r.GitCommit, r.GitDirty, r.GitStatus, r.GitDiffHash, r.GitDiffPath, r.GitAllowDirty, r.ResolvedEnv, r.ResolvedPython, r.ResolvedCwd, r.EnvJSON, r.LogPathsJSON, r.ArtifactPathsJSON, r.MetricPathsJSON, r.UIEventsPath, r.TmuxSession, r.RemoteRunDir, r.ExitCode, r.FailureKind, r.FailureReason, r.CreatedBy, r.CreatedAt, r.StartedAt, r.FinishedAt, r.ArchivedAt, r.DeletedAt,
	)
	return err
}

func (s *SQLite) CreateRun(ctx context.Context, r *Run) error {
	if err := prepareRunCreate(r); err != nil {
		return err
	}
	return insertRun(ctx, s.db, r)
}

func (s *SQLite) CreateRunWithBindings(ctx context.Context, r *Run, bindings RunBindings) error {
	if err := prepareRunCreate(r); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertRun(ctx, tx, r); err != nil {
		return err
	}
	if err := insertRunBindings(ctx, tx, r.ID, bindings); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) CreateRunWithLaunchJob(ctx context.Context, r *Run, job *RunLaunchJob, bindings ...RunBindings) error {
	if r == nil || job == nil || strings.TrimSpace(job.RunID) == "" || job.RunID != r.ID || strings.TrimSpace(job.RequestJSON) == "" {
		return fmt.Errorf("atomic run launch requires matching run_id and request_json")
	}
	if job.State == "" {
		job.State = RunLaunchQueued
	}
	if job.State != RunLaunchQueued {
		return fmt.Errorf("new run launch job must be queued")
	}
	if err := prepareRunCreate(r); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertRun(ctx, tx, r); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_launch_jobs(run_id, request_json, state, attempts, last_error) VALUES (?, ?, ?, 0, '')`, job.RunID, job.RequestJSON, job.State); err != nil {
		return err
	}
	if len(bindings) > 0 {
		if err := insertRunBindings(ctx, tx, r.ID, bindings[0]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insertRunBindings(ctx context.Context, exec contextExecer, runID string, bindings RunBindings) error {
	now := time.Now()
	for index := range bindings.Inputs {
		binding := &bindings.Inputs[index]
		binding.RunID, binding.Ordinal = runID, index
		if binding.ID == "" {
			binding.ID = fmt.Sprintf("%s_input_%d", runID, index)
		}
		if binding.Mode == "" {
			binding.Mode = "copy"
		}
		if binding.State == "" {
			binding.State = RunBindingPending
		}
		binding.CreatedAt, binding.UpdatedAt = now, now
		if _, err := exec.ExecContext(ctx, `INSERT INTO run_input_bindings
			(id, run_id, ordinal, logical_uri, target_path, revision, mode, source_placement_id, destination_placement_id, transfer_id, state, error_code, last_error, created_at, updated_at, verified_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, binding.ID, binding.RunID, binding.Ordinal, binding.LogicalURI, binding.TargetPath, binding.Revision, binding.Mode, binding.SourcePlacementID, binding.DestinationPlacementID, binding.TransferID, binding.State, binding.ErrorCode, binding.LastError, binding.CreatedAt, binding.UpdatedAt, binding.VerifiedAt); err != nil {
			return err
		}
	}
	for index := range bindings.Outputs {
		binding := &bindings.Outputs[index]
		binding.RunID, binding.Ordinal = runID, index
		if binding.ID == "" {
			binding.ID = fmt.Sprintf("%s_output_%d", runID, index)
		}
		if binding.State == "" {
			binding.State = RunBindingPending
		}
		binding.CreatedAt, binding.UpdatedAt = now, now
		if _, err := exec.ExecContext(ctx, `INSERT INTO run_output_bindings
			(id, run_id, ordinal, source_pattern, logical_uri, role, required, source_placement_id, destination_placement_id, revision, transfer_id, state, error_code, last_error, created_at, updated_at, published_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, binding.ID, binding.RunID, binding.Ordinal, binding.SourcePattern, binding.LogicalURI, binding.Role, binding.Required, binding.SourcePlacementID, binding.DestinationPlacementID, binding.Revision, binding.TransferID, binding.State, binding.ErrorCode, binding.LastError, binding.CreatedAt, binding.UpdatedAt, binding.PublishedAt); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLite) ListRunInputBindings(ctx context.Context, runID string) ([]RunInputBinding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, run_id, ordinal, logical_uri, target_path, revision, mode, source_placement_id, destination_placement_id, transfer_id, state, error_code, last_error, created_at, updated_at, verified_at FROM run_input_bindings WHERE run_id=? ORDER BY ordinal`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RunInputBinding, 0)
	for rows.Next() {
		var binding RunInputBinding
		if err := rows.Scan(&binding.ID, &binding.RunID, &binding.Ordinal, &binding.LogicalURI, &binding.TargetPath, &binding.Revision, &binding.Mode, &binding.SourcePlacementID, &binding.DestinationPlacementID, &binding.TransferID, &binding.State, &binding.ErrorCode, &binding.LastError, &binding.CreatedAt, &binding.UpdatedAt, &binding.VerifiedAt); err != nil {
			return nil, err
		}
		result = append(result, binding)
	}
	return result, rows.Err()
}

func (s *SQLite) UpdateRunInputBinding(ctx context.Context, binding *RunInputBinding) error {
	binding.UpdatedAt = time.Now()
	_, err := s.db.ExecContext(ctx, `UPDATE run_input_bindings SET source_placement_id=?, destination_placement_id=?, transfer_id=?, state=?, error_code=?, last_error=?, updated_at=?, verified_at=? WHERE id=?`, binding.SourcePlacementID, binding.DestinationPlacementID, binding.TransferID, binding.State, binding.ErrorCode, binding.LastError, binding.UpdatedAt, binding.VerifiedAt, binding.ID)
	return err
}

func (s *SQLite) ListRunOutputBindings(ctx context.Context, runID string) ([]RunOutputBinding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, run_id, ordinal, source_pattern, logical_uri, role, required, source_placement_id, destination_placement_id, revision, transfer_id, state, error_code, last_error, created_at, updated_at, published_at FROM run_output_bindings WHERE run_id=? ORDER BY ordinal`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RunOutputBinding, 0)
	for rows.Next() {
		var binding RunOutputBinding
		if err := rows.Scan(&binding.ID, &binding.RunID, &binding.Ordinal, &binding.SourcePattern, &binding.LogicalURI, &binding.Role, &binding.Required, &binding.SourcePlacementID, &binding.DestinationPlacementID, &binding.Revision, &binding.TransferID, &binding.State, &binding.ErrorCode, &binding.LastError, &binding.CreatedAt, &binding.UpdatedAt, &binding.PublishedAt); err != nil {
			return nil, err
		}
		result = append(result, binding)
	}
	return result, rows.Err()
}

func (s *SQLite) UpdateRunOutputBinding(ctx context.Context, binding *RunOutputBinding) error {
	binding.UpdatedAt = time.Now()
	_, err := s.db.ExecContext(ctx, `UPDATE run_output_bindings SET source_placement_id=?, destination_placement_id=?, revision=?, transfer_id=?, state=?, error_code=?, last_error=?, updated_at=?, published_at=? WHERE id=?`, binding.SourcePlacementID, binding.DestinationPlacementID, binding.Revision, binding.TransferID, binding.State, binding.ErrorCode, binding.LastError, binding.UpdatedAt, binding.PublishedAt, binding.ID)
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

const runSummaryColumns = `id, resource_id, project_id, name, status,
	status_source, status_observed_at, status_checked_at, status_check_error,
	data_finalization_state, data_finalization_error, data_finalization_updated_at,
	kind, task_role, evidence_grade, experiment_role, gpu_index,
	cwd, ui_events_path, substr(command, 1, 240), created_at, started_at, finished_at, archived_at, deleted_at`

func scanRunSummary(summary *RunSummary) func(rowScanner) error {
	return func(row rowScanner) error {
		if err := row.Scan(&summary.ID, &summary.ResourceID, &summary.ProjectID, &summary.Name, &summary.Status,
			&summary.StatusSource, &summary.StatusObservedAt, &summary.StatusCheckedAt, &summary.StatusCheckError,
			&summary.DataFinalizationState, &summary.DataFinalizationError, &summary.DataFinalizationUpdatedAt,
			&summary.Kind, &summary.TaskRole, &summary.EvidenceGrade, &summary.ExperimentRole, &summary.GPUIndex,
			&summary.Cwd, &summary.UIEventsPath, &summary.CommandPreview, &summary.CreatedAt, &summary.StartedAt, &summary.FinishedAt, &summary.ArchivedAt, &summary.DeletedAt); err != nil {
			return err
		}
		summary.ObservationState = RunObservationState(summary.StatusSource, summary.StatusCheckError)
		summary.LifecycleStatus = summary.Status
		summary.ObservationError = RunObservationErrorFrom(summary.StatusCheckError)
		probe := &Run{Status: summary.Status, StatusObservedAt: summary.StatusObservedAt, StatusCheckError: summary.StatusCheckError}
		RefreshRunStatusFreshness(probe, time.Now())
		summary.LifecycleStatus = probe.LifecycleStatus
		summary.ObservationState = probe.ObservationState
		summary.ObservationError = probe.ObservationError
		summary.StatusFreshness = probe.StatusFreshness
		return nil
	}
}

func (s *SQLite) GetRunSummary(ctx context.Context, id string) (*RunSummary, error) {
	summary := &RunSummary{}
	err := scanRunSummary(summary)(s.db.QueryRowContext(ctx, `SELECT `+runSummaryColumns+` FROM runs WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return summary, err
}

func (s *SQLite) ListRunSummaries(ctx context.Context, filter RunFilter) ([]RunSummary, error) {
	where, args := runFilterWhere(filter)
	query := `SELECT ` + runSummaryColumns + ` FROM runs` + where + ` ORDER BY created_at DESC`
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		query += ` OFFSET ?`
		args = append(args, filter.Offset)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RunSummary, 0)
	for rows.Next() {
		var summary RunSummary
		if err := scanRunSummary(&summary)(rows); err != nil {
			return nil, err
		}
		result = append(result, summary)
	}
	return result, rows.Err()
}

func (s *SQLite) ListRunChanges(ctx context.Context, afterSeq int64, updatedSince *time.Time, limit int) ([]RunChange, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	query := `SELECT seq, run_id, operation, changed_at FROM run_changes WHERE seq > ?`
	args := []interface{}{afterSeq}
	// Sequence is the authoritative no-gap cursor. updated_since is a fallback
	// for clients that do not yet have a sequence checkpoint; combining both as
	// an AND predicate can lose same-timestamp writes.
	if updatedSince != nil && afterSeq == 0 {
		// changed_at is persisted as RFC3339 text by current triggers, while
		// older databases may contain SQLite's "YYYY-MM-DD HH:MM:SS" form.
		// Compare parsed instants so driver-specific time bindings and mixed
		// legacy formats cannot make the fallback cursor return older rows.
		query += ` AND julianday(changed_at) >= julianday(?)`
		args = append(args, updatedSince.UTC().Format(time.RFC3339Nano))
	}
	query += ` ORDER BY seq LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	changes := make([]RunChange, 0)
	for rows.Next() {
		var change RunChange
		if err := rows.Scan(&change.Seq, &change.RunID, &change.Operation, &change.ChangedAt); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

func (s *SQLite) LatestRunChangeSeq(ctx context.Context) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM run_changes`).Scan(&seq)
	return seq, err
}

func (s *SQLite) SaveRunLaunchJob(ctx context.Context, job *RunLaunchJob) error {
	if job == nil || strings.TrimSpace(job.RunID) == "" || strings.TrimSpace(job.RequestJSON) == "" {
		return fmt.Errorf("run launch job requires run_id and request_json")
	}
	if job.State == "" {
		job.State = RunLaunchQueued
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO run_launch_jobs(run_id, request_json, state, attempts, last_error)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(run_id) DO NOTHING`, job.RunID, job.RequestJSON, job.State, job.Attempts, job.LastError)
	return err
}

func scanRunLaunchJob(row rowScanner, job *RunLaunchJob) error {
	return row.Scan(&job.RunID, &job.RequestJSON, &job.State, &job.Attempts, &job.LastError, &job.CreatedAt, &job.UpdatedAt)
}

func (s *SQLite) ClaimRunLaunchJob(ctx context.Context, runID string) (*RunLaunchJob, bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE run_launch_jobs SET state=?, attempts=attempts+1, updated_at=CURRENT_TIMESTAMP
		WHERE run_id=? AND state=?`, RunLaunchLaunching, runID, RunLaunchQueued)
	if err != nil {
		return nil, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows == 0 {
		return nil, false, err
	}
	job := &RunLaunchJob{}
	if err := scanRunLaunchJob(s.db.QueryRowContext(ctx, `SELECT run_id, request_json, state, attempts, last_error, created_at, updated_at FROM run_launch_jobs WHERE run_id=?`, runID), job); err != nil {
		return nil, false, err
	}
	return job, true, nil
}

func (s *SQLite) ListPendingRunLaunchJobs(ctx context.Context) ([]RunLaunchJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, request_json, state, attempts, last_error, created_at, updated_at
		FROM run_launch_jobs WHERE state=? ORDER BY created_at`, RunLaunchQueued)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]RunLaunchJob, 0)
	for rows.Next() {
		var job RunLaunchJob
		if err := scanRunLaunchJob(rows, &job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *SQLite) RequeueInterruptedRunLaunchJobs(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE run_launch_jobs SET state=?, last_error='control plane restarted during launch', updated_at=CURRENT_TIMESTAMP WHERE state=?`, RunLaunchQueued, RunLaunchLaunching)
	return err
}

func (s *SQLite) CompleteRunLaunchJob(ctx context.Context, runID, state, lastError string) error {
	if state != RunLaunchSucceeded && state != RunLaunchBlocked && state != RunLaunchFailed {
		return fmt.Errorf("invalid terminal launch state %q", state)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE run_launch_jobs SET state=?, last_error=?, updated_at=CURRENT_TIMESTAMP WHERE run_id=?`, state, lastError, runID)
	return err
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
	if filter.ProjectID != "" {
		query += " AND project_id = ?"
		args = append(args, filter.ProjectID)
	}
	if filter.ProjectScopeID != "" {
		if filter.ProjectScopeID == "__unassigned__" {
			query += ` AND NOT EXISTS (
				SELECT 1 FROM project_run_cards project_card
				WHERE project_card.run_id = runs.id AND trim(project_card.project_id) <> ''
			)`
		} else {
			query += ` AND (
				runs.project_id = ?
				OR EXISTS (
					SELECT 1 FROM project_run_cards project_card
					WHERE project_card.run_id = runs.id AND project_card.project_id = ?
				)
			)`
			args = append(args,
				filter.ProjectScopeID,
				filter.ProjectScopeID,
			)
		}
	}
	if filter.Status != "" {
		query += " AND status = ?"
		args = append(args, filter.Status)
	}
	if value := strings.TrimSpace(filter.Query); value != "" {
		pattern := "%" + strings.ToLower(value) + "%"
		query += ` AND (
			lower(id) LIKE ?
			OR lower(name) LIKE ?
			OR lower(command) LIKE ?
			OR lower(cwd) LIKE ?
			OR lower(resource_id) LIKE ?
			OR lower(project_id) LIKE ?
		)`
		for range 6 {
			args = append(args, pattern)
		}
	}
	switch strings.ToLower(strings.TrimSpace(filter.KindGroup)) {
	case "experiments":
		query += " AND lower(COALESCE(kind, 'formal')) NOT IN ('setup', 'smoke')"
	case "tools":
		query += " AND lower(COALESCE(kind, 'formal')) IN ('setup', 'smoke')"
	}
	if filter.Active {
		query += " AND status IN (?, ?, ?, ?, ?, ?)"
		args = append(args, RunStatusCreated, RunStatusQueued, RunStatusPreflighting, RunStatusStarting, RunStatusRunning, RunStatusSSHUnreachable)
	}
	if filter.ImportantOnly {
		query += " AND EXISTS (SELECT 1 FROM project_run_cards important_card WHERE important_card.run_id = runs.id AND important_card.important = 1)"
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
	args := append(runUpdateArgs(r), r.ID)
	_, err := s.db.ExecContext(ctx, runUpdateSQL+` WHERE id=?`, args...)
	return err
}

const runUpdateSQL = `UPDATE runs SET project_id=?, target_id=?, recipe_name=?, project_config_sha256=?, datasets_json=?, seeds_json=?, split_protocol=?, evaluation_protocol=?, data_finalization_state=?, data_finalization_error=?, data_finalization_updated_at=?, name=?, status=?, status_source=?, status_observed_at=?, status_checked_at=?, status_check_error=?, kind=?, task_role=?, evidence_grade=?, experiment_role=?, gpu_index=?, cwd=?, command=?, program=?, args_json=?, conda_env=?, project_env=?, target_env=?, force_reason=?, preempt_run_id=?, preempt_save=?, git_repo_root=?, git_remote_url=?, git_branch=?, git_commit=?, git_dirty=?, git_status=?, git_diff_hash=?, git_diff_path=?, git_allow_dirty=?, resolved_env=?, resolved_python=?, resolved_cwd=?, env_json=?, log_paths_json=?, artifact_paths_json=?, metric_paths_json=?, ui_events_path=?, tmux_session=?, remote_run_dir=?, exit_code=?, failure_kind=?, failure_reason=?, created_by=?, started_at=?, finished_at=?`

func runUpdateArgs(r *Run) []interface{} {
	return []interface{}{r.ProjectID, r.TargetID, r.RecipeName, r.ProjectConfigSHA256, r.DatasetsJSON, r.SeedsJSON, r.SplitProtocol, r.EvaluationProtocol, r.DataFinalizationState, r.DataFinalizationError, r.DataFinalizationUpdatedAt, r.Name, r.Status, r.StatusSource, r.StatusObservedAt, r.StatusCheckedAt, r.StatusCheckError, r.Kind, r.TaskRole, r.EvidenceGrade, r.ExperimentRole, r.GPUIndex, r.Cwd, r.Command, r.Program, r.ArgsJSON, r.CondaEnv, r.ProjectEnv, r.TargetEnv, r.ForceReason, r.PreemptRunID, r.PreemptSave, r.GitRepoRoot, r.GitRemoteURL, r.GitBranch, r.GitCommit, r.GitDirty, r.GitStatus, r.GitDiffHash, r.GitDiffPath, r.GitAllowDirty, r.ResolvedEnv, r.ResolvedPython, r.ResolvedCwd, r.EnvJSON, r.LogPathsJSON, r.ArtifactPathsJSON, r.MetricPathsJSON, r.UIEventsPath, r.TmuxSession, r.RemoteRunDir, r.ExitCode, r.FailureKind, r.FailureReason, r.CreatedBy, r.StartedAt, r.FinishedAt}
}

func (s *SQLite) UpdateRunIfStatus(ctx context.Context, r *Run, expectedStatus string) (bool, error) {
	args := append(runUpdateArgs(r), r.ID, expectedStatus)
	result, err := s.db.ExecContext(ctx, runUpdateSQL+` WHERE id=? AND status=?`, args...)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

func (s *SQLite) UpdateRunStatusObservation(ctx context.Context, id, expectedStatus string, observation RunStatusObservation) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET status_source=?, status_observed_at=COALESCE(?, status_observed_at), status_checked_at=?, status_check_error=? WHERE id=? AND status=?`, observation.Source, observation.ObservedAt, observation.CheckedAt, observation.Error, id, expectedStatus)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

func (s *SQLite) UpdateRunFailureMetadata(ctx context.Context, id, expectedStatus, failureKind, failureReason, statusSource, statusCheckError string) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE runs
		 SET failure_kind=?, failure_reason=?, status_source=?, status_check_error=?
		 WHERE id=? AND status=?`,
		failureKind, failureReason, statusSource, statusCheckError, id, expectedStatus,
	)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

func (s *SQLite) UpdateRunDataFinalization(ctx context.Context, id, state, lastError string, updatedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET data_finalization_state=?, data_finalization_error=?, data_finalization_updated_at=? WHERE id=?`, state, lastError, updatedAt, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("run %s not found", id)
	}
	return nil
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

// --- Executable Projects and Targets ---

func (s *SQLite) CreateProjectDefinition(ctx context.Context, p *ProjectDefinition) error {
	if p == nil || strings.TrimSpace(p.ID) == "" {
		return graphValidationError("PROJECT_ID_REQUIRED", "project id is required")
	}
	if strings.TrimSpace(p.Name) == "" {
		return graphValidationError("PROJECT_NAME_REQUIRED", "project name is required")
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	chain := primaryEvidenceChainForProject(p, now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_definitions (
		id, name, description, local_root, config_path, config_hash, source_repo, default_recipe,
		vault, run_card_index, proposal_dir, promotion_default, aggregate_command, gate_command,
		zotero_collection_key, literature_service_profile, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Description, p.LocalRoot, p.ConfigPath, p.ConfigHash, p.SourceRepo, p.DefaultRecipe,
		p.Vault, p.RunCardIndex, p.ProposalDir, p.PromotionDefault, p.AggregateCommand, p.GateCommand,
		p.ZoteroCollectionKey, p.LiteratureServiceProfile, p.CreatedAt, p.UpdatedAt); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return graphValidationError("PROJECT_EXISTS", fmt.Sprintf("project %q already exists", p.ID))
		}
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO evidence_chains (`+evidenceChainColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chain.ID, chain.Title, chain.Description, "{}", chain.ProjectID, chain.Role, chain.Status,
		chain.Revision, chain.GraphHash, chain.CreatedAt, chain.UpdatedAt,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) SaveProjectDefinition(ctx context.Context, p *ProjectDefinition) error {
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	p.UpdatedAt = time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO project_definitions (
		id, name, description, local_root, config_path, config_hash, source_repo, default_recipe,
		vault, run_card_index, proposal_dir, promotion_default, aggregate_command, gate_command,
		zotero_collection_key, literature_service_profile, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name=excluded.name, description=excluded.description, local_root=excluded.local_root,
		config_path=excluded.config_path, config_hash=excluded.config_hash, source_repo=excluded.source_repo,
		default_recipe=excluded.default_recipe, vault=excluded.vault, run_card_index=excluded.run_card_index,
		proposal_dir=excluded.proposal_dir, promotion_default=excluded.promotion_default,
		aggregate_command=excluded.aggregate_command, gate_command=excluded.gate_command,
		zotero_collection_key=excluded.zotero_collection_key,
		literature_service_profile=excluded.literature_service_profile, updated_at=excluded.updated_at`,
		p.ID, p.Name, p.Description, p.LocalRoot, p.ConfigPath, p.ConfigHash, p.SourceRepo, p.DefaultRecipe,
		p.Vault, p.RunCardIndex, p.ProposalDir, p.PromotionDefault, p.AggregateCommand, p.GateCommand,
		p.ZoteroCollectionKey, p.LiteratureServiceProfile, p.CreatedAt, p.UpdatedAt)
	return err
}

const projectDefinitionColumns = `id, name, description, local_root, config_path, config_hash, source_repo, default_recipe, vault, run_card_index, proposal_dir, promotion_default, aggregate_command, gate_command, zotero_collection_key, literature_service_profile, created_at, updated_at`

func scanProjectDefinition(row rowScanner, p *ProjectDefinition) error {
	return row.Scan(&p.ID, &p.Name, &p.Description, &p.LocalRoot, &p.ConfigPath, &p.ConfigHash, &p.SourceRepo, &p.DefaultRecipe, &p.Vault, &p.RunCardIndex, &p.ProposalDir, &p.PromotionDefault, &p.AggregateCommand, &p.GateCommand, &p.ZoteroCollectionKey, &p.LiteratureServiceProfile, &p.CreatedAt, &p.UpdatedAt)
}

func (s *SQLite) GetProjectDefinition(ctx context.Context, id string) (*ProjectDefinition, error) {
	var p ProjectDefinition
	err := scanProjectDefinition(s.db.QueryRowContext(ctx, `SELECT `+projectDefinitionColumns+` FROM project_definitions WHERE id=?`, id), &p)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}

func (s *SQLite) ListProjectDefinitions(ctx context.Context) ([]ProjectDefinition, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+projectDefinitionColumns+` FROM project_definitions ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []ProjectDefinition
	for rows.Next() {
		var p ProjectDefinition
		if err := scanProjectDefinition(rows, &p); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

type ProjectInUseError struct {
	ProjectID string
	Counts    map[string]int
}

func (e *ProjectInUseError) Error() string {
	parts := make([]string, 0, len(e.Counts))
	for name, count := range e.Counts {
		if count > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", name, count))
		}
	}
	sort.Strings(parts)
	return fmt.Sprintf("project %q is still referenced (%s)", e.ProjectID, strings.Join(parts, ", "))
}

func (s *SQLite) DeleteProjectDefinition(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	references := map[string]string{
		"runs":      `SELECT COUNT(*) FROM runs WHERE project_id=?`,
		"cards":     `SELECT COUNT(*) FROM project_run_cards WHERE project_id=?`,
		"maps":      `SELECT COUNT(*) FROM evidence_chains WHERE project_id=?`,
		"snapshots": `SELECT COUNT(*) FROM evidence_snapshots WHERE project_id=?`,
		"releases":  `SELECT COUNT(*) FROM evidence_releases WHERE project_id=?`,
		"proposals": `SELECT COUNT(*) FROM evidence_proposals WHERE project_id=?`,
		"journals":  `SELECT COUNT(*) FROM project_journal_entries WHERE project_id=?`,
	}
	counts := make(map[string]int, len(references))
	inUse := false
	for name, query := range references {
		var count int
		if err := tx.QueryRowContext(ctx, query, id).Scan(&count); err != nil {
			return err
		}
		counts[name] = count
		if count > 0 {
			inUse = true
		}
	}
	if inUse {
		return &ProjectInUseError{ProjectID: id, Counts: counts}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_targets WHERE project_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_definitions WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) SaveProjectTarget(ctx context.Context, target *ProjectTarget) error {
	if target.CreatedAt.IsZero() {
		target.CreatedAt = time.Now()
	}
	if target.EnvStrategy == "" {
		target.EnvStrategy = "auto"
	}
	if target.Readiness == "" {
		target.Readiness = TargetReadinessUnknown
	}
	if target.EnvJSON == "" {
		target.EnvJSON = "{}"
	}
	target.UpdatedAt = time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO project_targets (
		id, project_id, name, resource_id, cwd, env_strategy, conda_env, desired_env, default_gpu,
		ui_events_path, env_json, sync_source, sync_target, sync_profile, prepare_command, readiness,
		readiness_observed_at, readiness_error, last_prepare_run_id, last_prepared_at,
		observed_config_hash, observed_environment_hash, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		project_id=excluded.project_id, name=excluded.name, resource_id=excluded.resource_id, cwd=excluded.cwd,
		env_strategy=excluded.env_strategy, conda_env=excluded.conda_env, desired_env=excluded.desired_env,
		default_gpu=excluded.default_gpu, ui_events_path=excluded.ui_events_path, env_json=excluded.env_json,
		sync_source=excluded.sync_source, sync_target=excluded.sync_target, sync_profile=excluded.sync_profile,
		prepare_command=excluded.prepare_command, readiness=excluded.readiness,
		readiness_observed_at=excluded.readiness_observed_at, readiness_error=excluded.readiness_error,
		last_prepare_run_id=excluded.last_prepare_run_id, last_prepared_at=excluded.last_prepared_at,
		observed_config_hash=excluded.observed_config_hash, observed_environment_hash=excluded.observed_environment_hash,
		updated_at=excluded.updated_at`,
		target.ID, target.ProjectID, target.Name, target.ResourceID, target.Cwd, target.EnvStrategy, target.CondaEnv,
		target.DesiredEnv, target.DefaultGPU, target.UIEventsPath, target.EnvJSON, target.SyncSource, target.SyncTarget,
		target.SyncProfile, target.PrepareCommand, target.Readiness, target.ReadinessObservedAt, target.ReadinessError,
		target.LastPrepareRunID, target.LastPreparedAt, target.ObservedConfigHash, target.ObservedEnvironmentHash,
		target.CreatedAt, target.UpdatedAt)
	return err
}

func (s *SQLite) BeginProjectTargetPrepare(ctx context.Context, id string, observedAt time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE project_targets SET readiness=?, readiness_observed_at=?, readiness_error='', updated_at=? WHERE id=? AND readiness!=?`, TargetReadinessChecking, observedAt, observedAt, id, TargetReadinessChecking)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

const projectTargetColumns = `id, project_id, name, resource_id, cwd, env_strategy, conda_env, desired_env, default_gpu, ui_events_path, env_json, sync_source, sync_target, sync_profile, prepare_command, readiness, readiness_observed_at, readiness_error, last_prepare_run_id, last_prepared_at, observed_config_hash, observed_environment_hash, created_at, updated_at`

func scanProjectTarget(row rowScanner, target *ProjectTarget) error {
	return row.Scan(&target.ID, &target.ProjectID, &target.Name, &target.ResourceID, &target.Cwd, &target.EnvStrategy, &target.CondaEnv, &target.DesiredEnv, &target.DefaultGPU, &target.UIEventsPath, &target.EnvJSON, &target.SyncSource, &target.SyncTarget, &target.SyncProfile, &target.PrepareCommand, &target.Readiness, &target.ReadinessObservedAt, &target.ReadinessError, &target.LastPrepareRunID, &target.LastPreparedAt, &target.ObservedConfigHash, &target.ObservedEnvironmentHash, &target.CreatedAt, &target.UpdatedAt)
}

func (s *SQLite) GetProjectTarget(ctx context.Context, id string) (*ProjectTarget, error) {
	var target ProjectTarget
	err := scanProjectTarget(s.db.QueryRowContext(ctx, `SELECT `+projectTargetColumns+` FROM project_targets WHERE id=?`, id), &target)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &target, err
}

func (s *SQLite) ListProjectTargets(ctx context.Context, projectID string) ([]ProjectTarget, error) {
	query := `SELECT ` + projectTargetColumns + ` FROM project_targets`
	args := []interface{}{}
	if projectID != "" {
		query += ` WHERE project_id=?`
		args = append(args, projectID)
	}
	query += ` ORDER BY name, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []ProjectTarget
	for rows.Next() {
		var target ProjectTarget
		if err := scanProjectTarget(rows, &target); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func (s *SQLite) DeleteProjectTarget(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM project_targets WHERE id=?`, id)
	return err
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

	if _, err := tx.ExecContext(ctx, `DELETE FROM artifacts WHERE run_id=?`, runID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO artifacts (id, run_id, path, relative_path, source_uri, type, role, mime, size, sha256, collection_state, collection_error, discovered_at, modified_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, a := range artifacts {
		if _, err := stmt.ExecContext(ctx, a.ID, runID, a.Path, a.RelativePath, a.SourceURI, a.Type, a.Role, a.Mime, a.Size, a.SHA256, a.CollectionState, a.CollectionError, a.DiscoveredAt, a.ModifiedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) ListArtifacts(ctx context.Context, runID string) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, path, relative_path, source_uri, type, role, mime, size, sha256, collection_state, collection_error, discovered_at, modified_at FROM artifacts WHERE run_id = ? ORDER BY path`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var artifacts []Artifact
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.RunID, &a.Path, &a.RelativePath, &a.SourceURI, &a.Type, &a.Role, &a.Mime, &a.Size, &a.SHA256, &a.CollectionState, &a.CollectionError, &a.DiscoveredAt, &a.ModifiedAt); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, a)
	}
	return artifacts, rows.Err()
}

func (s *SQLite) SaveArtifactCollection(ctx context.Context, collection *ArtifactCollection) error {
	collection.UpdatedAt = time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO artifact_collections (run_id, state, error, file_count, total_bytes, started_at, finished_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET state=excluded.state, error=excluded.error, file_count=excluded.file_count,
		total_bytes=excluded.total_bytes, started_at=excluded.started_at, finished_at=excluded.finished_at, updated_at=excluded.updated_at`,
		collection.RunID, collection.State, collection.Error, collection.FileCount, collection.TotalBytes, collection.StartedAt, collection.FinishedAt, collection.UpdatedAt)
	return err
}

func (s *SQLite) GetArtifactCollection(ctx context.Context, runID string) (*ArtifactCollection, error) {
	var collection ArtifactCollection
	err := s.db.QueryRowContext(ctx, `SELECT run_id, state, error, file_count, total_bytes, started_at, finished_at, updated_at FROM artifact_collections WHERE run_id=?`, runID).
		Scan(&collection.RunID, &collection.State, &collection.Error, &collection.FileCount, &collection.TotalBytes, &collection.StartedAt, &collection.FinishedAt, &collection.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &collection, err
}

func (s *SQLite) SaveRunManifest(ctx context.Context, manifest *RunManifest) error {
	if manifest.CreatedAt.IsZero() {
		manifest.CreatedAt = time.Now()
	}
	var existingState, existingHash string
	err := s.db.QueryRowContext(ctx, `SELECT state, sha256 FROM run_manifests WHERE run_id=?`, manifest.RunID).Scan(&existingState, &existingHash)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if existingState == RunManifestFinal {
		if existingHash == manifest.SHA256 && manifest.State == RunManifestFinal {
			return nil
		}
		return fmt.Errorf("run manifest %s is final and immutable", manifest.RunID)
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO run_manifests (run_id, schema_version, state, manifest_json, sha256, completeness, created_at, finalized_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET schema_version=excluded.schema_version, state=excluded.state,
		manifest_json=excluded.manifest_json, sha256=excluded.sha256, completeness=excluded.completeness, finalized_at=excluded.finalized_at
		WHERE run_manifests.state != 'final'`,
		manifest.RunID, manifest.SchemaVersion, manifest.State, manifest.ManifestJSON, manifest.SHA256, manifest.Completeness, manifest.CreatedAt, manifest.FinalizedAt)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		var finalHash string
		if err := s.db.QueryRowContext(ctx, `SELECT sha256 FROM run_manifests WHERE run_id=? AND state='final'`, manifest.RunID).Scan(&finalHash); err != nil {
			return err
		}
		if finalHash != manifest.SHA256 || manifest.State != RunManifestFinal {
			return fmt.Errorf("run manifest %s is final and immutable", manifest.RunID)
		}
	}
	return nil
}

func (s *SQLite) GetRunManifest(ctx context.Context, runID string) (*RunManifest, error) {
	var manifest RunManifest
	err := s.db.QueryRowContext(ctx, `SELECT run_id, schema_version, state, manifest_json, sha256, completeness, created_at, finalized_at FROM run_manifests WHERE run_id=?`, runID).
		Scan(&manifest.RunID, &manifest.SchemaVersion, &manifest.State, &manifest.ManifestJSON, &manifest.SHA256, &manifest.Completeness, &manifest.CreatedAt, &manifest.FinalizedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &manifest, err
}

func (s *SQLite) CreateEvidenceSnapshot(ctx context.Context, runID string) (*EvidenceSnapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	var status, kind, evidenceGrade, projectID, finalizationState string
	err = tx.QueryRowContext(ctx, `SELECT status, kind, evidence_grade, project_id, data_finalization_state FROM runs WHERE id=?`, runID).
		Scan(&status, &kind, &evidenceGrade, &projectID, &finalizationState)
	if err == sql.ErrNoRows {
		return nil, false, &EvidenceSnapshotBlockedError{Blockers: []EvidenceSnapshotBlocker{{Code: "run_not_found", Message: "run does not exist"}}}
	}
	if err != nil {
		return nil, false, err
	}

	blockers := make([]EvidenceSnapshotBlocker, 0)
	if status != RunStatusSucceeded {
		blockers = append(blockers, EvidenceSnapshotBlocker{Code: "run_not_succeeded", Message: "run must succeed before creating a snapshot"})
	}
	if evidenceGrade != RunEvidenceGradeFormal && kind != RunKindFormal && kind != RunKindAblation {
		blockers = append(blockers, EvidenceSnapshotBlocker{Code: "run_not_formal", Message: "only formal or ablation runs can create an evidence snapshot"})
	}
	if strings.TrimSpace(projectID) == "" {
		blockers = append(blockers, EvidenceSnapshotBlocker{Code: "project_missing", Message: "run is not bound to a Project"})
	} else {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM project_definitions WHERE id=?`, projectID).Scan(&exists); err == sql.ErrNoRows {
			blockers = append(blockers, EvidenceSnapshotBlocker{Code: "project_unregistered", Message: "run Project is not registered"})
		} else if err != nil {
			return nil, false, err
		}
	}
	if finalizationState != RunDataFinalizationCompleted {
		blockers = append(blockers, EvidenceSnapshotBlocker{Code: "outputs_not_verified", Message: "run outputs have not completed verified publication"})
	}

	var manifest RunManifest
	err = tx.QueryRowContext(ctx, `SELECT run_id, schema_version, state, manifest_json, sha256, completeness, created_at, finalized_at FROM run_manifests WHERE run_id=?`, runID).
		Scan(&manifest.RunID, &manifest.SchemaVersion, &manifest.State, &manifest.ManifestJSON, &manifest.SHA256, &manifest.Completeness, &manifest.CreatedAt, &manifest.FinalizedAt)
	if err == sql.ErrNoRows {
		blockers = append(blockers, EvidenceSnapshotBlocker{Code: "manifest_missing", Message: "final RunManifest is missing"})
	} else if err != nil {
		return nil, false, err
	} else {
		if manifest.State != RunManifestFinal {
			blockers = append(blockers, EvidenceSnapshotBlocker{Code: "manifest_not_final", Message: "RunManifest is not final"})
		}
		if manifest.Completeness != RunManifestCompletenessCurrent {
			blockers = append(blockers, EvidenceSnapshotBlocker{Code: "manifest_legacy_partial", Message: "legacy partial RunManifest cannot create a snapshot"})
		}
		if strings.TrimSpace(manifest.SHA256) == "" {
			blockers = append(blockers, EvidenceSnapshotBlocker{Code: "manifest_hash_missing", Message: "RunManifest SHA-256 is missing"})
		}
	}

	rows, err := tx.QueryContext(ctx, `SELECT id, ordinal, logical_uri, role, revision, required, state FROM run_output_bindings WHERE run_id=? ORDER BY ordinal, id`, runID)
	if err != nil {
		return nil, false, err
	}
	outputs := make([]EvidenceSnapshotOutput, 0)
	for rows.Next() {
		var output EvidenceSnapshotOutput
		var state string
		if err := rows.Scan(&output.BindingID, &output.Ordinal, &output.LogicalURI, &output.Role, &output.Revision, &output.Required, &state); err != nil {
			rows.Close()
			return nil, false, err
		}
		if state != RunBindingPublished || strings.TrimSpace(output.Revision) == "" {
			if output.Required {
				blockers = append(blockers, EvidenceSnapshotBlocker{Code: "required_output_unpublished", Message: fmt.Sprintf("required output %s is not published with a revision", output.BindingID)})
			}
			continue
		}
		outputs = append(outputs, output)
	}
	if err := rows.Close(); err != nil {
		return nil, false, err
	}
	if len(outputs) == 0 {
		blockers = append(blockers, EvidenceSnapshotBlocker{Code: "outputs_empty", Message: "no published output revisions are available"})
	}
	if len(blockers) > 0 {
		return nil, false, &EvidenceSnapshotBlockedError{Blockers: blockers}
	}

	sort.Slice(outputs, func(i, j int) bool {
		if outputs[i].Ordinal != outputs[j].Ordinal {
			return outputs[i].Ordinal < outputs[j].Ordinal
		}
		return outputs[i].BindingID < outputs[j].BindingID
	})
	outputJSON, err := json.Marshal(outputs)
	if err != nil {
		return nil, false, err
	}
	outputDigest := sha256.Sum256(outputJSON)
	outputSetSHA256 := "sha256:" + hex.EncodeToString(outputDigest[:])
	snapshotManifest := struct {
		SchemaVersion     int                      `json:"schema_version"`
		RunID             string                   `json:"run_id"`
		ProjectID         string                   `json:"project_id"`
		RunManifestSHA256 string                   `json:"run_manifest_sha256"`
		OutputSetSHA256   string                   `json:"output_set_sha256"`
		Outputs           []EvidenceSnapshotOutput `json:"outputs"`
	}{
		SchemaVersion: 1, RunID: runID, ProjectID: projectID, RunManifestSHA256: manifest.SHA256,
		OutputSetSHA256: outputSetSHA256, Outputs: outputs,
	}
	manifestJSON, err := json.Marshal(snapshotManifest)
	if err != nil {
		return nil, false, err
	}
	manifestDigest := sha256.Sum256(manifestJSON)
	manifestSHA256 := "sha256:" + hex.EncodeToString(manifestDigest[:])
	idDigest := sha256.Sum256([]byte(runID + "\x00" + outputSetSHA256))
	snapshotID := "snap_" + hex.EncodeToString(idDigest[:8])
	now := time.Now()
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO evidence_snapshots
		(id, run_id, project_id, run_manifest_sha256, output_set_sha256, manifest_json, manifest_sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		snapshotID, runID, projectID, manifest.SHA256, outputSetSHA256, string(manifestJSON), manifestSHA256, now)
	if err != nil {
		return nil, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	var snapshot EvidenceSnapshot
	if err := tx.QueryRowContext(ctx, `SELECT id, run_id, project_id, run_manifest_sha256, output_set_sha256, manifest_json, manifest_sha256, created_at FROM evidence_snapshots WHERE run_id=? AND output_set_sha256=?`, runID, outputSetSHA256).
		Scan(&snapshot.ID, &snapshot.RunID, &snapshot.ProjectID, &snapshot.RunManifestSHA256, &snapshot.OutputSetSHA256, &snapshot.ManifestJSON, &snapshot.ManifestSHA256, &snapshot.CreatedAt); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return &snapshot, affected > 0, nil
}

func (s *SQLite) GetEvidenceSnapshot(ctx context.Context, id string) (*EvidenceSnapshot, error) {
	var snapshot EvidenceSnapshot
	err := s.db.QueryRowContext(ctx, `SELECT id, run_id, project_id, run_manifest_sha256, output_set_sha256, manifest_json, manifest_sha256, created_at FROM evidence_snapshots WHERE id=?`, id).
		Scan(&snapshot.ID, &snapshot.RunID, &snapshot.ProjectID, &snapshot.RunManifestSHA256, &snapshot.OutputSetSHA256, &snapshot.ManifestJSON, &snapshot.ManifestSHA256, &snapshot.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &snapshot, err
}

func (s *SQLite) ListEvidenceSnapshots(ctx context.Context, runID string) ([]EvidenceSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, run_id, project_id, run_manifest_sha256, output_set_sha256, manifest_json, manifest_sha256, created_at FROM evidence_snapshots WHERE run_id=? ORDER BY created_at DESC, id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	snapshots := make([]EvidenceSnapshot, 0)
	for rows.Next() {
		var snapshot EvidenceSnapshot
		if err := rows.Scan(&snapshot.ID, &snapshot.RunID, &snapshot.ProjectID, &snapshot.RunManifestSHA256, &snapshot.OutputSetSHA256, &snapshot.ManifestJSON, &snapshot.ManifestSHA256, &snapshot.CreatedAt); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
}

func (s *SQLite) AppendEvidenceRelease(ctx context.Context, release *EvidenceRelease) error {
	if release == nil || strings.TrimSpace(release.SnapshotID) == "" {
		return fmt.Errorf("evidence release requires snapshot_id")
	}
	if release.State != EvidenceReleaseReleased && release.State != EvidenceReleaseBlocked && release.State != EvidenceReleaseFailed {
		return fmt.Errorf("invalid evidence release state %q", release.State)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var projectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM evidence_snapshots WHERE id=?`, release.SnapshotID).Scan(&projectID); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM evidence_releases WHERE snapshot_id=?`, release.SnapshotID).Scan(&release.Sequence); err != nil {
		return err
	}
	release.ProjectID = projectID
	if release.CreatedAt.IsZero() {
		release.CreatedAt = time.Now().UTC()
	}
	idDigest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", release.SnapshotID, release.Sequence)))
	release.ID = "release_" + hex.EncodeToString(idDigest[:8])
	if strings.TrimSpace(release.AggregateResultJSON) == "" {
		release.AggregateResultJSON = "{}"
	}
	if strings.TrimSpace(release.GateResultJSON) == "" {
		release.GateResultJSON = "{}"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO evidence_releases
		(id, snapshot_id, project_id, sequence, state, aggregate_result_json, gate_result_json, error_code, last_error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		release.ID, release.SnapshotID, release.ProjectID, release.Sequence, release.State,
		release.AggregateResultJSON, release.GateResultJSON, release.ErrorCode, release.LastError, release.CreatedAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) GetEvidenceRelease(ctx context.Context, id string) (*EvidenceRelease, error) {
	var release EvidenceRelease
	err := s.db.QueryRowContext(ctx, `SELECT id, snapshot_id, project_id, sequence, state, aggregate_result_json, gate_result_json, error_code, last_error, created_at FROM evidence_releases WHERE id=?`, id).
		Scan(&release.ID, &release.SnapshotID, &release.ProjectID, &release.Sequence, &release.State,
			&release.AggregateResultJSON, &release.GateResultJSON, &release.ErrorCode, &release.LastError, &release.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &release, err
}

func (s *SQLite) ListEvidenceReleases(ctx context.Context, snapshotID string) ([]EvidenceRelease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, snapshot_id, project_id, sequence, state, aggregate_result_json, gate_result_json, error_code, last_error, created_at FROM evidence_releases WHERE snapshot_id=? ORDER BY sequence DESC`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	releases := make([]EvidenceRelease, 0)
	for rows.Next() {
		var release EvidenceRelease
		if err := rows.Scan(&release.ID, &release.SnapshotID, &release.ProjectID, &release.Sequence, &release.State,
			&release.AggregateResultJSON, &release.GateResultJSON, &release.ErrorCode, &release.LastError, &release.CreatedAt); err != nil {
			return nil, err
		}
		releases = append(releases, release)
	}
	return releases, rows.Err()
}

func (s *SQLite) ListProjectAssets(ctx context.Context, projectID string, limit, offset int) ([]ProjectAsset, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	const projectAssetsCTE = `
	WITH project_assets AS (
		SELECT output.id AS id, runs.project_id AS project_id, output.run_id AS run_id,
		       output.logical_uri AS logical_uri, output.revision AS revision,
		       output.role AS role, output.state AS state, output.published_at AS published_at
	FROM run_output_bindings output
	JOIN runs ON runs.id=output.run_id
	WHERE runs.project_id=? AND output.state=? AND trim(output.revision)<>''
	UNION ALL
	SELECT input.id AS id, runs.project_id AS project_id, input.run_id AS run_id,
	       input.logical_uri AS logical_uri, input.revision AS revision,
	       'input' AS role, input.state AS state, input.verified_at AS published_at
		FROM run_input_bindings input
		JOIN runs ON runs.id=input.run_id
		WHERE runs.project_id=? AND input.state=? AND trim(input.revision)<>''
		UNION ALL
		SELECT dataset.id AS id, ? AS project_id, '' AS run_id,
		       dataset.logical_uri AS logical_uri, dataset.revision AS revision,
		       'dataset' AS role, dataset.state AS state, dataset.updated_at AS published_at
		FROM dataset_versions dataset
		WHERE dataset.state=? AND trim(dataset.revision)<>''
		  AND instr(dataset.logical_uri, 'aexp://' || ? || '/')=1
	)`
	var total int
	if err := s.db.QueryRowContext(ctx, projectAssetsCTE+` SELECT COUNT(*) FROM project_assets`,
		projectID, RunBindingPublished, projectID, RunBindingReady,
		projectID, DatasetStateVerified, projectID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, projectAssetsCTE+`
		SELECT id, project_id, run_id, logical_uri, revision, role, state, published_at
		FROM project_assets
			ORDER BY published_at DESC, id
			LIMIT ? OFFSET ?`,
		projectID, RunBindingPublished, projectID, RunBindingReady,
		projectID, DatasetStateVerified, projectID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	assets := make([]ProjectAsset, 0)
	for rows.Next() {
		var asset ProjectAsset
		if err := rows.Scan(&asset.ID, &asset.ProjectID, &asset.RunID, &asset.LogicalURI,
			&asset.Revision, &asset.Role, &asset.State, &asset.PublishedAt); err != nil {
			return nil, 0, err
		}
		assets = append(assets, asset)
	}
	return assets, total, rows.Err()
}

// --- Data center ---

const storageTargetColumns = "id, name, kind, resource_id, root_path, config_json, status, last_error, last_checked_at, health_json, created_at, updated_at"

func scanStorageTarget(target *StorageTarget) func(rowScanner) error {
	return func(row rowScanner) error {
		var healthJSON string
		if err := row.Scan(&target.ID, &target.Name, &target.Kind, &target.ResourceID, &target.RootPath, &target.ConfigJSON, &target.Status, &target.LastError, &target.LastCheckedAt, &healthJSON, &target.CreatedAt, &target.UpdatedAt); err != nil {
			return err
		}
		if strings.TrimSpace(healthJSON) != "" && healthJSON != "{}" {
			var health StorageTargetHealth
			if json.Unmarshal([]byte(healthJSON), &health) == nil {
				normalizeStorageTargetHealth(&health)
				target.Health = &health
			}
		}
		return nil
	}
}

func normalizeStorageTargetHealth(health *StorageTargetHealth) {
	if health == nil {
		return
	}
	if health.Checks == nil {
		health.Checks = map[string]StorageHealthCheck{}
	}
	if health.DataPlane == nil {
		health.DataPlane = []StorageDataPlaneHealth{}
	}
}

func (s *SQLite) SaveStorageTarget(ctx context.Context, target *StorageTarget) error {
	if target == nil {
		return fmt.Errorf("storage target is required")
	}
	if resource, err := s.GetResource(ctx, target.ResourceID); err != nil {
		return err
	} else if resource == nil {
		return fmt.Errorf("storage resource %s not found", target.ResourceID)
	}
	now := time.Now()
	if target.CreatedAt.IsZero() {
		target.CreatedAt = now
	}
	target.UpdatedAt = now
	if target.Kind == "" {
		target.Kind = StorageKindSSHRsync
	}
	if target.ConfigJSON == "" {
		target.ConfigJSON = "{}"
	}
	if target.Status == "" {
		target.Status = StorageStatusUnknown
	}
	healthJSON := "{}"
	if target.Health != nil {
		if data, marshalErr := json.Marshal(target.Health); marshalErr == nil {
			healthJSON = string(data)
		}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO storage_targets (`+storageTargetColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, kind=excluded.kind, resource_id=excluded.resource_id, root_path=excluded.root_path, config_json=excluded.config_json, status=excluded.status, last_error=excluded.last_error, last_checked_at=excluded.last_checked_at, health_json=excluded.health_json, updated_at=excluded.updated_at`,
		target.ID, target.Name, target.Kind, target.ResourceID, target.RootPath, target.ConfigJSON, target.Status, target.LastError, target.LastCheckedAt, healthJSON, target.CreatedAt, target.UpdatedAt)
	return err
}

func (s *SQLite) GetStorageTarget(ctx context.Context, id string) (*StorageTarget, error) {
	target := &StorageTarget{}
	err := scanStorageTarget(target)(s.db.QueryRowContext(ctx, `SELECT `+storageTargetColumns+` FROM storage_targets WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return target, err
}

func (s *SQLite) GetStorageTargetByName(ctx context.Context, name string) (*StorageTarget, error) {
	target := &StorageTarget{}
	err := scanStorageTarget(target)(s.db.QueryRowContext(ctx, `SELECT `+storageTargetColumns+` FROM storage_targets WHERE name=?`, name))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return target, err
}

func (s *SQLite) ListStorageTargets(ctx context.Context) ([]StorageTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+storageTargetColumns+` FROM storage_targets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := make([]StorageTarget, 0)
	for rows.Next() {
		var target StorageTarget
		if err := scanStorageTarget(&target)(rows); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func (s *SQLite) GetStorageTargetUsage(ctx context.Context, id string) (StorageTargetUsage, error) {
	var usage StorageTargetUsage
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dataset_versions WHERE storage_target_id=?`, id).Scan(&usage.DatasetVersions); err != nil {
		return usage, err
	}
	target, err := s.GetStorageTarget(ctx, id)
	if err != nil || target == nil {
		return usage, err
	}
	prefix := "storage://" + target.Name + "/%"
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_freezes WHERE destination_uri LIKE ?`, prefix).Scan(&usage.RunFreezes); err != nil {
		return usage, err
	}
	return usage, nil
}

func (s *SQLite) DeleteStorageTarget(ctx context.Context, id string) error {
	usage, err := s.GetStorageTargetUsage(ctx, id)
	if err != nil {
		return err
	}
	if usage.DatasetVersions > 0 || usage.RunFreezes > 0 {
		return fmt.Errorf("storage target is in use by %d dataset version(s) and %d run freeze(s)", usage.DatasetVersions, usage.RunFreezes)
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM storage_targets WHERE id=?`, id)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

const datasetVersionColumns = "id, dataset_id, version, storage_target_id, storage_path, logical_uri, revision, manifest_sha256, archive_sha256, format, file_count, total_bytes, state, manifest_json, created_at, updated_at"

func scanDatasetVersion(dataset *DatasetVersion) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&dataset.ID, &dataset.DatasetID, &dataset.Version, &dataset.StorageTargetID, &dataset.StoragePath, &dataset.LogicalURI, &dataset.Revision, &dataset.ManifestSHA256, &dataset.ArchiveSHA256, &dataset.Format, &dataset.FileCount, &dataset.TotalBytes, &dataset.State, &dataset.ManifestJSON, &dataset.CreatedAt, &dataset.UpdatedAt)
	}
}

func (s *SQLite) SaveDatasetVersion(ctx context.Context, dataset *DatasetVersion) error {
	if dataset == nil {
		return fmt.Errorf("dataset version is required")
	}
	if target, err := s.GetStorageTarget(ctx, dataset.StorageTargetID); err != nil {
		return err
	} else if target == nil {
		return fmt.Errorf("storage target %s not found", dataset.StorageTargetID)
	}
	now := time.Now()
	if dataset.CreatedAt.IsZero() {
		dataset.CreatedAt = now
	}
	dataset.UpdatedAt = now
	if dataset.State == "" {
		dataset.State = DatasetStateRegistered
	}
	if dataset.ManifestJSON == "" {
		dataset.ManifestJSON = "{}"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO dataset_versions (`+datasetVersionColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET dataset_id=excluded.dataset_id, version=excluded.version, storage_target_id=excluded.storage_target_id, storage_path=excluded.storage_path, logical_uri=excluded.logical_uri, revision=excluded.revision, manifest_sha256=excluded.manifest_sha256, archive_sha256=excluded.archive_sha256, format=excluded.format, file_count=excluded.file_count, total_bytes=excluded.total_bytes, state=excluded.state, manifest_json=excluded.manifest_json, updated_at=excluded.updated_at`,
		dataset.ID, dataset.DatasetID, dataset.Version, dataset.StorageTargetID, dataset.StoragePath, dataset.LogicalURI, dataset.Revision, dataset.ManifestSHA256, dataset.ArchiveSHA256, dataset.Format, dataset.FileCount, dataset.TotalBytes, dataset.State, dataset.ManifestJSON, dataset.CreatedAt, dataset.UpdatedAt)
	return err
}

func (s *SQLite) CreateDatasetVersionImmutable(ctx context.Context, dataset *DatasetVersion) (*DatasetVersion, bool, error) {
	if dataset == nil || dataset.ID == "" || dataset.DatasetID == "" || dataset.Version == "" || dataset.LogicalURI == "" || dataset.Revision == "" {
		return nil, false, fmt.Errorf("immutable dataset id, ref, logical URI, and revision are required")
	}
	if target, err := s.GetStorageTarget(ctx, dataset.StorageTargetID); err != nil {
		return nil, false, err
	} else if target == nil {
		return nil, false, fmt.Errorf("storage target %s not found", dataset.StorageTargetID)
	}
	now := time.Now()
	if dataset.CreatedAt.IsZero() {
		dataset.CreatedAt = now
	}
	dataset.UpdatedAt = now
	if dataset.State == "" {
		dataset.State = DatasetStateVerified
	}
	if dataset.ManifestJSON == "" {
		dataset.ManifestJSON = "{}"
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO dataset_versions (`+datasetVersionColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(dataset_id, version) DO NOTHING`,
		dataset.ID, dataset.DatasetID, dataset.Version, dataset.StorageTargetID, dataset.StoragePath, dataset.LogicalURI, dataset.Revision, dataset.ManifestSHA256, dataset.ArchiveSHA256, dataset.Format, dataset.FileCount, dataset.TotalBytes, dataset.State, dataset.ManifestJSON, dataset.CreatedAt, dataset.UpdatedAt)
	if err != nil {
		return nil, false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	existing, err := s.GetDatasetVersionByRef(ctx, dataset.DatasetID, dataset.Version)
	if err != nil || existing == nil {
		return nil, false, err
	}
	if existing.LogicalURI != dataset.LogicalURI || existing.Revision != dataset.Revision {
		return nil, false, &DatasetVersionConflictError{
			DatasetID: dataset.DatasetID, Version: dataset.Version,
			ExistingURI: existing.LogicalURI, ExistingRevision: existing.Revision,
			RequestedURI: dataset.LogicalURI, RequestedRevision: dataset.Revision,
		}
	}
	// A metadata-only registration is not evidence. A later managed ingest may
	// promote that exact immutable identity after transfer verification proves
	// the bytes. URI and revision were matched above, so this never retags or
	// overwrites a different dataset version.
	if count == 0 && existing.State == DatasetStateRegistered && dataset.State == DatasetStateVerified {
		result, err := s.db.ExecContext(ctx, `UPDATE dataset_versions
			SET manifest_sha256=?, archive_sha256=?, format=?, file_count=?, total_bytes=?, state=?, manifest_json=?, updated_at=?
			WHERE id=? AND state=? AND logical_uri=? AND revision=?`,
			dataset.ManifestSHA256, dataset.ArchiveSHA256, dataset.Format, dataset.FileCount, dataset.TotalBytes,
			DatasetStateVerified, dataset.ManifestJSON, now,
			existing.ID, DatasetStateRegistered, existing.LogicalURI, existing.Revision,
		)
		if err != nil {
			return nil, false, err
		}
		if _, err := result.RowsAffected(); err != nil {
			return nil, false, err
		}
		existing, err = s.GetDatasetVersion(ctx, existing.ID)
		if err != nil {
			return nil, false, err
		}
	}
	return existing, count == 1, nil
}

func (s *SQLite) GetDatasetVersion(ctx context.Context, id string) (*DatasetVersion, error) {
	dataset := &DatasetVersion{}
	err := scanDatasetVersion(dataset)(s.db.QueryRowContext(ctx, `SELECT `+datasetVersionColumns+` FROM dataset_versions WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return dataset, err
}

func (s *SQLite) GetDatasetVersionByRef(ctx context.Context, datasetID, version string) (*DatasetVersion, error) {
	dataset := &DatasetVersion{}
	err := scanDatasetVersion(dataset)(s.db.QueryRowContext(ctx, `SELECT `+datasetVersionColumns+` FROM dataset_versions WHERE dataset_id=? AND version=?`, datasetID, version))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return dataset, err
}

func (s *SQLite) ListDatasetVersions(ctx context.Context) ([]DatasetVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+datasetVersionColumns+` FROM dataset_versions ORDER BY dataset_id, version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	datasets := make([]DatasetVersion, 0)
	for rows.Next() {
		var dataset DatasetVersion
		if err := scanDatasetVersion(&dataset)(rows); err != nil {
			return nil, err
		}
		datasets = append(datasets, dataset)
	}
	return datasets, rows.Err()
}

const datasetMaterializationColumns = "id, dataset_version_id, resource_id, local_path, state, bytes_present, verified_sha256, transfer_id, last_error, started_at, finished_at, verified_at, last_accessed_at, updated_at"

func scanDatasetMaterialization(m *DatasetMaterialization) func(rowScanner) error {
	return func(row rowScanner) error {
		var transferID sql.NullString
		if err := row.Scan(&m.ID, &m.DatasetVersionID, &m.ResourceID, &m.LocalPath, &m.State, &m.BytesPresent, &m.VerifiedSHA256, &transferID, &m.LastError, &m.StartedAt, &m.FinishedAt, &m.VerifiedAt, &m.LastAccessedAt, &m.UpdatedAt); err != nil {
			return err
		}
		m.TransferID = transferID.String
		return nil
	}
}

func (s *SQLite) SaveDatasetMaterialization(ctx context.Context, m *DatasetMaterialization) error {
	if m == nil {
		return fmt.Errorf("dataset materialization is required")
	}
	if dataset, err := s.GetDatasetVersion(ctx, m.DatasetVersionID); err != nil {
		return err
	} else if dataset == nil {
		return fmt.Errorf("dataset version %s not found", m.DatasetVersionID)
	}
	if resource, err := s.GetResource(ctx, m.ResourceID); err != nil {
		return err
	} else if resource == nil {
		return fmt.Errorf("resource %s not found", m.ResourceID)
	}
	m.UpdatedAt = time.Now()
	if m.State == "" {
		m.State = MaterializationPlanned
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO dataset_materializations (`+datasetMaterializationColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(dataset_version_id, resource_id) DO UPDATE SET id=excluded.id, local_path=excluded.local_path, state=excluded.state, bytes_present=excluded.bytes_present, verified_sha256=excluded.verified_sha256, transfer_id=excluded.transfer_id, last_error=excluded.last_error, started_at=excluded.started_at, finished_at=excluded.finished_at, verified_at=excluded.verified_at, last_accessed_at=excluded.last_accessed_at, updated_at=excluded.updated_at`,
		m.ID, m.DatasetVersionID, m.ResourceID, m.LocalPath, m.State, m.BytesPresent, m.VerifiedSHA256, nullableString(m.TransferID), m.LastError, m.StartedAt, m.FinishedAt, m.VerifiedAt, m.LastAccessedAt, m.UpdatedAt)
	return err
}

func (s *SQLite) GetDatasetMaterialization(ctx context.Context, datasetVersionID, resourceID string) (*DatasetMaterialization, error) {
	m := &DatasetMaterialization{}
	err := scanDatasetMaterialization(m)(s.db.QueryRowContext(ctx, `SELECT `+datasetMaterializationColumns+` FROM dataset_materializations WHERE dataset_version_id=? AND resource_id=?`, datasetVersionID, resourceID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return m, err
}

func (s *SQLite) ListDatasetMaterializations(ctx context.Context, datasetVersionID string) ([]DatasetMaterialization, error) {
	query := `SELECT ` + datasetMaterializationColumns + ` FROM dataset_materializations`
	var args []interface{}
	if datasetVersionID != "" {
		query += ` WHERE dataset_version_id=?`
		args = append(args, datasetVersionID)
	}
	query += ` ORDER BY updated_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DatasetMaterialization, 0)
	for rows.Next() {
		var m DatasetMaterialization
		if err := scanDatasetMaterialization(&m)(rows); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQLite) UpdateDatasetMaterializationIfState(ctx context.Context, m *DatasetMaterialization, expectedState string) (bool, error) {
	m.UpdatedAt = time.Now()
	result, err := s.db.ExecContext(ctx, `UPDATE dataset_materializations SET local_path=?, state=?, bytes_present=?, verified_sha256=?, transfer_id=?, last_error=?, started_at=?, finished_at=?, verified_at=?, last_accessed_at=?, updated_at=? WHERE dataset_version_id=? AND resource_id=? AND state=?`, m.LocalPath, m.State, m.BytesPresent, m.VerifiedSHA256, nullableString(m.TransferID), m.LastError, m.StartedAt, m.FinishedAt, m.VerifiedAt, m.LastAccessedAt, m.UpdatedAt, m.DatasetVersionID, m.ResourceID, expectedState)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

const runFreezeColumns = "id, run_id, profile, profile_sha256, plan_sha256, destination_uri, workspace_path, state, stage, error_code, last_error, run_manifest_sha256, provenance_json, blockers_json, raw_manifest_sha256, release_manifest_sha256, manifest_uri, raw_transfer_id, workspace_transfer_id, file_count, total_bytes, files_done, bytes_done, aggregate_result_json, gate_result_json, created_at, updated_at, frozen_at, released_at"

func scanRunFreeze(f *RunFreeze) func(rowScanner) error {
	return func(row rowScanner) error {
		var rawTransferID, workspaceTransferID sql.NullString
		if err := row.Scan(&f.ID, &f.RunID, &f.Profile, &f.ProfileSHA256, &f.PlanSHA256, &f.DestinationURI, &f.WorkspacePath, &f.State, &f.Stage, &f.ErrorCode, &f.LastError, &f.RunManifestSHA256, &f.ProvenanceJSON, &f.BlockersJSON, &f.RawManifestSHA256, &f.ReleaseManifestSHA256, &f.ManifestURI, &rawTransferID, &workspaceTransferID, &f.FileCount, &f.TotalBytes, &f.FilesDone, &f.BytesDone, &f.AggregateResultJSON, &f.GateResultJSON, &f.CreatedAt, &f.UpdatedAt, &f.FrozenAt, &f.ReleasedAt); err != nil {
			return err
		}
		f.RawTransferID, f.WorkspaceTransferID = rawTransferID.String, workspaceTransferID.String
		return nil
	}
}

func (s *SQLite) CreateRunFreeze(ctx context.Context, f *RunFreeze) error {
	if f == nil {
		return fmt.Errorf("run freeze is required")
	}
	if run, err := s.GetRun(ctx, f.RunID); err != nil {
		return err
	} else if run == nil {
		return fmt.Errorf("run %s not found", f.RunID)
	}
	now := time.Now()
	if f.CreatedAt.IsZero() {
		f.CreatedAt = now
	}
	f.UpdatedAt = now
	if f.State == "" {
		f.State = RunFreezeQueued
	}
	if f.Stage == "" {
		f.Stage = f.State
	}
	if f.ProvenanceJSON == "" {
		f.ProvenanceJSON = "{}"
	}
	if f.BlockersJSON == "" {
		f.BlockersJSON = "[]"
	}
	if f.AggregateResultJSON == "" {
		f.AggregateResultJSON = "{}"
	}
	if f.GateResultJSON == "" {
		f.GateResultJSON = "{}"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO run_freezes (`+runFreezeColumns+`) VALUES (`+strings.TrimSuffix(strings.Repeat("?, ", 29), ", ")+`)`, f.ID, f.RunID, f.Profile, f.ProfileSHA256, f.PlanSHA256, f.DestinationURI, f.WorkspacePath, f.State, f.Stage, f.ErrorCode, f.LastError, f.RunManifestSHA256, f.ProvenanceJSON, f.BlockersJSON, f.RawManifestSHA256, f.ReleaseManifestSHA256, f.ManifestURI, nullableString(f.RawTransferID), nullableString(f.WorkspaceTransferID), f.FileCount, f.TotalBytes, f.FilesDone, f.BytesDone, f.AggregateResultJSON, f.GateResultJSON, f.CreatedAt, f.UpdatedAt, f.FrozenAt, f.ReleasedAt)
	return err
}

func (s *SQLite) GetRunFreeze(ctx context.Context, id string) (*RunFreeze, error) {
	f := &RunFreeze{}
	err := scanRunFreeze(f)(s.db.QueryRowContext(ctx, `SELECT `+runFreezeColumns+` FROM run_freezes WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return f, err
}

func (s *SQLite) ListRunFreezes(ctx context.Context, runID string) ([]RunFreeze, error) {
	query := `SELECT ` + runFreezeColumns + ` FROM run_freezes`
	var args []interface{}
	if runID != "" {
		query += ` WHERE run_id=?`
		args = append(args, runID)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RunFreeze, 0)
	for rows.Next() {
		var f RunFreeze
		if err := scanRunFreeze(&f)(rows); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *SQLite) UpdateRunFreezeIfState(ctx context.Context, f *RunFreeze, expectedState string) (bool, error) {
	if f == nil {
		return false, fmt.Errorf("run freeze is required")
	}
	f.UpdatedAt = time.Now()
	result, err := s.db.ExecContext(ctx, `UPDATE run_freezes SET state=?, stage=?, error_code=?, last_error=?, blockers_json=?, raw_manifest_sha256=?, release_manifest_sha256=?, manifest_uri=?, raw_transfer_id=?, workspace_transfer_id=?, file_count=?, total_bytes=?, files_done=?, bytes_done=?, aggregate_result_json=?, gate_result_json=?, updated_at=?, frozen_at=?, released_at=? WHERE id=? AND state=?`, f.State, f.Stage, f.ErrorCode, f.LastError, f.BlockersJSON, f.RawManifestSHA256, f.ReleaseManifestSHA256, f.ManifestURI, nullableString(f.RawTransferID), nullableString(f.WorkspaceTransferID), f.FileCount, f.TotalBytes, f.FilesDone, f.BytesDone, f.AggregateResultJSON, f.GateResultJSON, f.UpdatedAt, f.FrozenAt, f.ReleasedAt, f.ID, expectedState)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (s *SQLite) ReplaceRunFreezeFiles(ctx context.Context, freezeID string, files []RunFreezeFile) error {
	freeze, err := s.GetRunFreeze(ctx, freezeID)
	if err != nil {
		return err
	}
	if freeze == nil {
		return fmt.Errorf("run freeze %s not found", freezeID)
	}
	if freeze.State == RunFreezeReleased || freeze.State == RunFreezeBlocked {
		return fmt.Errorf("run freeze %s is immutable", freezeID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_freeze_files WHERE freeze_id=?`, freezeID); err != nil {
		return err
	}
	for _, file := range files {
		if file.SHA256 == "" {
			return fmt.Errorf("freeze file %s is missing sha256", file.RelativePath)
		}
		if file.CreatedAt.IsZero() {
			file.CreatedAt = time.Now()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_freeze_files (id, freeze_id, kind, role, relative_path, source_uri, frozen_uri, sha256, size, required, source_artifact_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, file.ID, freezeID, file.Kind, file.Role, file.RelativePath, file.SourceURI, file.FrozenURI, file.SHA256, file.Size, file.Required, file.SourceArtifactID, file.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) ListRunFreezeFiles(ctx context.Context, freezeID string) ([]RunFreezeFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, freeze_id, kind, role, relative_path, source_uri, frozen_uri, sha256, size, required, source_artifact_id, created_at FROM run_freeze_files WHERE freeze_id=? ORDER BY kind, role, relative_path`, freezeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RunFreezeFile, 0)
	for rows.Next() {
		var file RunFreezeFile
		if err := rows.Scan(&file.ID, &file.FreezeID, &file.Kind, &file.Role, &file.RelativePath, &file.SourceURI, &file.FrozenURI, &file.SHA256, &file.Size, &file.Required, &file.SourceArtifactID, &file.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, file)
	}
	return out, rows.Err()
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
	} else if len(filter.RunIDs) > 0 {
		placeholders := make([]string, 0, len(filter.RunIDs))
		for _, runID := range filter.RunIDs {
			if strings.TrimSpace(runID) == "" {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, runID)
		}
		if len(placeholders) > 0 {
			query += " AND run_id IN (" + strings.Join(placeholders, ",") + ")"
		}
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

const projectRunCardColumns = "id, project_id, project_name, run_id, question, verdict, evidence_level, key_metrics, artifact_paths, supports_claim, weakens_claim, next_action, important, should_promote, proposal_reason, graph_routing_reason, related_runs, graph_patch_json, graph_status, proposal_hash, base_graph_revision, reviewed_at, no_graph_impact, graph_impact_reason, created_at, updated_at"

type ProjectRunCardRevisionConflict struct {
	RunID    string    `json:"run_id"`
	Expected time.Time `json:"expected_updated_at"`
	Current  time.Time `json:"current_updated_at"`
}

func (e *ProjectRunCardRevisionConflict) Error() string {
	return fmt.Sprintf("project run card %s revision conflict: expected %s, current %s", e.RunID, e.Expected.Format(time.RFC3339Nano), e.Current.Format(time.RFC3339Nano))
}

func scanProjectRunCard(c *ProjectRunCard) func(rowScanner) error {
	return func(row rowScanner) error {
		var important, shouldPromote, noGraphImpact int
		var reviewedAt sql.NullTime
		if err := row.Scan(
			&c.ID, &c.ProjectID, &c.ProjectName, &c.RunID, &c.Question, &c.Verdict, &c.EvidenceLevel,
			&c.KeyMetrics, &c.ArtifactPaths, &c.SupportsClaim, &c.WeakensClaim, &c.NextAction,
			&important, &shouldPromote, &c.ProposalReason, &c.GraphRoutingReason, &c.RelatedRuns, &c.GraphPatchJSON,
			&c.GraphStatus, &c.ProposalHash, &c.BaseGraphRevision, &reviewedAt, &noGraphImpact,
			&c.GraphImpactReason, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return err
		}
		c.Important = important != 0
		c.ShouldPromote = shouldPromote != 0
		c.NoGraphImpact = noGraphImpact != 0
		if reviewedAt.Valid {
			value := reviewedAt.Time
			c.ReviewedAt = &value
		}
		return nil
	}
}

func (s *SQLite) SaveProjectRunCard(ctx context.Context, c *ProjectRunCard) error {
	now := time.Now()
	expectedUpdatedAt := c.UpdatedAt
	currentUpdatedAt := time.Time{}
	currentUpdatedAtRaw := ""
	err := s.db.QueryRowContext(ctx,
		`SELECT updated_at, CAST(updated_at AS TEXT) FROM project_run_cards WHERE run_id=?`,
		c.RunID,
	).Scan(&currentUpdatedAt, &currentUpdatedAtRaw)
	switch {
	case err == nil:
		if expectedUpdatedAt.IsZero() || !currentUpdatedAt.Equal(expectedUpdatedAt) {
			return &ProjectRunCardRevisionConflict{RunID: c.RunID, Expected: expectedUpdatedAt, Current: currentUpdatedAt}
		}
	case err == sql.ErrNoRows:
		if err := s.requireProjectRunCardProject(ctx, c.ProjectID); err != nil {
			return err
		}
		currentUpdatedAtRaw = ""
	case err != nil:
		return err
	}
	if c.ID == "" {
		c.ID = "card_" + strings.TrimPrefix(c.RunID, "run_")
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if strings.TrimSpace(c.GraphStatus) == "" {
		c.GraphStatus = "none"
	}
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO project_run_cards (`+projectRunCardColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET
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
			updated_at=excluded.updated_at
		 WHERE CAST(project_run_cards.updated_at AS TEXT) = ?`,
		c.ID, c.ProjectID, c.ProjectName, c.RunID, c.Question, c.Verdict, c.EvidenceLevel,
		c.KeyMetrics, c.ArtifactPaths, c.SupportsClaim, c.WeakensClaim, c.NextAction,
		boolInt(c.Important), boolInt(c.ShouldPromote), c.ProposalReason, c.GraphRoutingReason, c.RelatedRuns,
		c.GraphPatchJSON, c.GraphStatus, c.ProposalHash, c.BaseGraphRevision, c.ReviewedAt,
		boolInt(c.NoGraphImpact), c.GraphImpactReason, c.CreatedAt, now,
		currentUpdatedAtRaw,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		current, getErr := s.GetProjectRunCard(ctx, c.RunID)
		if getErr != nil {
			return getErr
		}
		if current == nil {
			return sql.ErrNoRows
		}
		return &ProjectRunCardRevisionConflict{RunID: c.RunID, Expected: expectedUpdatedAt, Current: current.UpdatedAt}
	}
	c.UpdatedAt = now
	return nil
}

func (s *SQLite) requireProjectRunCardProject(ctx context.Context, projectID string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return &EvidenceGraphValidationError{Code: "PROJECT_ID_REQUIRED", Message: "project run card requires a registered project"}
	}
	project, err := s.GetProjectDefinition(ctx, projectID)
	if err != nil {
		return err
	}
	if project == nil {
		return &EvidenceGraphValidationError{Code: "PROJECT_NOT_FOUND", Message: fmt.Sprintf("project %q is not registered", projectID)}
	}
	return nil
}

func (s *SQLite) ReassignProjectRunCard(ctx context.Context, runID, projectID, projectName string, expectedUpdatedAt time.Time) (*ProjectRunCard, error) {
	runID = strings.TrimSpace(runID)
	projectID = strings.TrimSpace(projectID)
	if runID == "" || projectID == "" {
		return nil, fmt.Errorf("run id and project id are required")
	}
	project, err := s.GetProjectDefinition(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, &EvidenceGraphValidationError{Code: "PROJECT_NOT_FOUND", Message: fmt.Sprintf("project %q is not registered", projectID)}
	}
	if strings.TrimSpace(projectName) == "" {
		projectName = project.Name
	}
	var currentUpdatedAt time.Time
	var currentUpdatedAtRaw string
	err = s.db.QueryRowContext(ctx,
		`SELECT updated_at, CAST(updated_at AS TEXT) FROM project_run_cards WHERE run_id=?`,
		runID,
	).Scan(&currentUpdatedAt, &currentUpdatedAtRaw)
	if err != nil {
		return nil, err
	}
	if expectedUpdatedAt.IsZero() || !currentUpdatedAt.Equal(expectedUpdatedAt) {
		return nil, &ProjectRunCardRevisionConflict{RunID: runID, Expected: expectedUpdatedAt, Current: currentUpdatedAt}
	}
	now := time.Now()
	result, err := s.db.ExecContext(ctx,
		`UPDATE project_run_cards
		 SET project_id=?, project_name=?, updated_at=?
		 WHERE run_id=? AND CAST(updated_at AS TEXT)=?`,
		projectID, projectName, now, runID, currentUpdatedAtRaw,
	)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		current, getErr := s.GetProjectRunCard(ctx, runID)
		if getErr != nil {
			return nil, getErr
		}
		if current == nil {
			return nil, sql.ErrNoRows
		}
		return nil, &ProjectRunCardRevisionConflict{RunID: runID, Expected: expectedUpdatedAt, Current: current.UpdatedAt}
	}
	return s.GetProjectRunCard(ctx, runID)
}

// saveProjectRunGraphProposal owns only the graph-proposal half of a run card.
// Interpretation writes and proposal writes intentionally never replace each
// other's columns.
func (s *SQLite) saveProjectRunGraphProposal(ctx context.Context, c *ProjectRunCard) error {
	now := time.Now()
	var existing int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM project_run_cards WHERE run_id=?`, c.RunID).Scan(&existing)
	if err == sql.ErrNoRows {
		if err := s.requireProjectRunCardProject(ctx, c.ProjectID); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if c.ID == "" {
		c.ID = "card_" + strings.TrimPrefix(c.RunID, "run_")
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if strings.TrimSpace(c.GraphStatus) == "" {
		c.GraphStatus = GraphProposalNone
	}
	c.UpdatedAt = now
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO project_run_cards (`+projectRunCardColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET
			graph_routing_reason=excluded.graph_routing_reason,
			graph_patch_json=excluded.graph_patch_json,
			graph_status=excluded.graph_status,
			proposal_hash=excluded.proposal_hash,
			base_graph_revision=excluded.base_graph_revision,
			reviewed_at=excluded.reviewed_at,
			no_graph_impact=excluded.no_graph_impact,
			graph_impact_reason=excluded.graph_impact_reason,
			updated_at=excluded.updated_at`,
		c.ID, c.ProjectID, c.ProjectName, c.RunID, c.Question, c.Verdict, c.EvidenceLevel,
		c.KeyMetrics, c.ArtifactPaths, c.SupportsClaim, c.WeakensClaim, c.NextAction,
		boolInt(c.Important), boolInt(c.ShouldPromote), c.ProposalReason, c.GraphRoutingReason, c.RelatedRuns,
		c.GraphPatchJSON, c.GraphStatus, c.ProposalHash, c.BaseGraphRevision, c.ReviewedAt,
		boolInt(c.NoGraphImpact), c.GraphImpactReason, c.CreatedAt, c.UpdatedAt,
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

const evidenceChainColumns = "id, title, description, routing_hints_json, project_id, role, status, revision, graph_hash, created_at, updated_at"

func scanEvidenceChain(c *EvidenceChain) func(rowScanner) error {
	return func(row rowScanner) error {
		var routingHintsJSON string
		if err := row.Scan(&c.ID, &c.Title, &c.Description, &routingHintsJSON, &c.ProjectID, &c.Role, &c.Status, &c.Revision, &c.GraphHash, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return err
		}
		c.RoutingHints = EvidenceGraphRoutingHints{}
		if strings.TrimSpace(routingHintsJSON) == "" {
			return nil
		}
		return json.Unmarshal([]byte(routingHintsJSON), &c.RoutingHints)
	}
}

func normalizeEvidenceGraphRoutingHints(hints EvidenceGraphRoutingHints) EvidenceGraphRoutingHints {
	normalize := func(values []string) []string {
		seen := make(map[string]bool)
		out := make([]string, 0, len(values))
		for _, value := range values {
			value = strings.TrimSpace(value)
			key := strings.ToLower(value)
			if value == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, value)
		}
		return out
	}
	return EvidenceGraphRoutingHints{
		Recipes:  normalize(hints.Recipes),
		Keywords: normalize(hints.Keywords),
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
	if strings.TrimSpace(c.Role) == "" {
		c.Role = "secondary"
	}
	if strings.TrimSpace(c.Status) == "" {
		c.Status = "active"
	}
	if err := s.validateEvidenceChainOwnership(ctx, c); err != nil {
		return err
	}
	if c.Revision == 0 && strings.TrimSpace(c.GraphHash) == "" {
		_, emptyHash, err := CanonicalEvidenceGraph(EvidenceChainGraph{})
		if err != nil {
			return err
		}
		c.GraphHash = emptyHash
	}
	c.RoutingHints = normalizeEvidenceGraphRoutingHints(c.RoutingHints)
	routingHintsJSON, err := json.Marshal(c.RoutingHints)
	if err != nil {
		return err
	}
	c.UpdatedAt = now
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO evidence_chains (`+evidenceChainColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Title, c.Description, string(routingHintsJSON), c.ProjectID, c.Role, c.Status, c.Revision, c.GraphHash, c.CreatedAt, c.UpdatedAt,
	)
	return err
}

func (s *SQLite) validateEvidenceChainOwnership(ctx context.Context, c *EvidenceChain) error {
	c.ProjectID = strings.TrimSpace(c.ProjectID)
	c.Role = strings.TrimSpace(c.Role)
	c.Status = strings.TrimSpace(c.Status)
	if c.Role != "primary" && c.Role != "secondary" && c.Role != "archive" {
		return graphValidationError("INVALID_ROLE", "role must be primary, secondary, or archive")
	}
	if c.Status != "active" && c.Status != "archived" {
		return graphValidationError("INVALID_STATUS", "status must be active or archived")
	}
	if c.Status == "active" && c.ProjectID == "" {
		return graphValidationError("PROJECT_ID_REQUIRED", "active evidence maps must belong to a registered project")
	}
	if c.ProjectID != "" {
		project, err := s.GetProjectDefinition(ctx, c.ProjectID)
		if err != nil {
			return err
		}
		if project == nil {
			return graphValidationError("PROJECT_NOT_REGISTERED", fmt.Sprintf("project %q is not registered", c.ProjectID))
		}
	}
	return nil
}

func (s *SQLite) GetEvidenceChain(ctx context.Context, id string) (*EvidenceChain, error) {
	c := &EvidenceChain{}
	err := scanEvidenceChain(c)(s.db.QueryRowContext(ctx, `SELECT `+evidenceChainColumns+` FROM evidence_chains WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

func (s *SQLite) GetActivePrimaryEvidenceChain(ctx context.Context, projectID string) (*EvidenceChain, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, nil
	}
	c := &EvidenceChain{}
	err := scanEvidenceChain(c)(s.db.QueryRowContext(ctx,
		`SELECT `+evidenceChainColumns+` FROM evidence_chains
		 WHERE project_id = ? AND role = 'primary' AND status = 'active'`,
		projectID,
	))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

func (s *SQLite) EnsureProjectPrimaryEvidenceChain(ctx context.Context, projectID string) (*EvidenceChain, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, graphValidationError("PROJECT_ID_REQUIRED", "project id is required")
	}
	project, err := s.GetProjectDefinition(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, graphValidationError("PROJECT_NOT_REGISTERED", fmt.Sprintf("project %q is not registered", projectID))
	}
	if existing, err := s.GetActivePrimaryEvidenceChain(ctx, projectID); err != nil || existing != nil {
		return existing, err
	}
	chain := primaryEvidenceChainForProject(project, time.Now().UTC())
	if err := s.CreateEvidenceChain(ctx, chain); err != nil {
		// A concurrent caller may have won the partial unique-index race.
		if existing, getErr := s.GetActivePrimaryEvidenceChain(ctx, projectID); getErr == nil && existing != nil {
			return existing, nil
		}
		return nil, err
	}
	return chain, nil
}

func primaryEvidenceChainForProject(project *ProjectDefinition, now time.Time) *EvidenceChain {
	sum := sha256.Sum256([]byte(strings.TrimSpace(project.ID)))
	return &EvidenceChain{
		ID:        "chain_primary_" + hex.EncodeToString(sum[:8]),
		Title:     strings.TrimSpace(project.Name) + " Research Graph",
		ProjectID: strings.TrimSpace(project.ID),
		Role:      "primary",
		Status:    "active",
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func (s *SQLite) ListEvidenceChains(ctx context.Context, filter EvidenceChainFilter) ([]EvidenceChain, error) {
	query := `SELECT ` + evidenceChainColumns + ` FROM evidence_chains WHERE 1=1`
	var args []interface{}
	if q := strings.TrimSpace(filter.Query); q != "" {
		like := "%" + strings.ToLower(q) + "%"
		query += " AND (LOWER(title) LIKE ? OR LOWER(description) LIKE ? OR LOWER(routing_hints_json) LIKE ? OR LOWER(id) LIKE ?)"
		args = append(args, like, like, like, like)
	}
	if projectID := strings.TrimSpace(filter.ProjectID); projectID != "" {
		query += " AND project_id = ?"
		args = append(args, projectID)
	}
	if role := strings.TrimSpace(filter.Role); role != "" {
		query += " AND role = ?"
		args = append(args, role)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		query += " AND status = ?"
		args = append(args, status)
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
	if err := s.validateEvidenceChainOwnership(ctx, c); err != nil {
		return err
	}
	c.UpdatedAt = time.Now()
	c.RoutingHints = normalizeEvidenceGraphRoutingHints(c.RoutingHints)
	routingHintsJSON, err := json.Marshal(c.RoutingHints)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE evidence_chains SET title = ?, description = ?, routing_hints_json = ?, project_id = ?, role = ?, status = ?, updated_at = ? WHERE id = ?`,
		c.Title, c.Description, string(routingHintsJSON), c.ProjectID, c.Role, c.Status, c.UpdatedAt, c.ID,
	)
	return err
}

func (s *SQLite) DeleteEvidenceChain(ctx context.Context, id string) error {
	// The legacy API names this operation "delete", but Evidence Workspace
	// history is append-only. Archive the Map in place so accepted revisions,
	// nodes, edges, and Primary MapReferences remain readable.
	_, err := s.db.ExecContext(ctx, `UPDATE evidence_chains
		SET role = 'archive', status = 'archived', updated_at = ?
		WHERE id = ?`, time.Now(), id)
	return err
}

// PurgeEvidenceChain permanently removes a secondary Map after proving that
// doing so cannot silently break the Primary Map or an in-flight proposal.
// Empty, never-saved Topics may be removed directly; Topics with history must
// first be archived so permanent deletion is always an explicit second step.
func (s *SQLite) PurgeEvidenceChain(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var role, status string
	var revision int64
	if err := tx.QueryRowContext(ctx,
		`SELECT role, status, revision FROM evidence_chains WHERE id = ?`, id,
	).Scan(&role, &status, &revision); err != nil {
		return err
	}
	if role == "primary" {
		return graphValidationError("PRIMARY_MAP_REQUIRED", "the Project Primary Map cannot be deleted")
	}

	var nodeCount, proposalCount int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM evidence_chain_nodes WHERE chain_id = ?`, id,
	).Scan(&nodeCount); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM evidence_proposals WHERE target_chain_id = ?`, id,
	).Scan(&proposalCount); err != nil {
		return err
	}
	if proposalCount > 0 {
		return graphValidationError("MAP_HAS_PROPOSALS", "the Topic Map still has proposals and cannot be permanently deleted")
	}
	if status != "archived" && (revision > 0 || nodeCount > 0) {
		return graphValidationError("ARCHIVE_REQUIRED", "a Topic Map with evidence history must be archived before permanent deletion")
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT data_json FROM evidence_chain_nodes WHERE type = ?`, EvidenceNodeMapRef,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var dataJSON string
		if err := rows.Scan(&dataJSON); err != nil {
			return err
		}
		var reference EvidenceMapReference
		if json.Unmarshal([]byte(dataJSON), &reference) == nil && reference.TargetMapID == id {
			return graphValidationError("MAP_STILL_REFERENCED", "the Topic Map is still referenced by another Evidence Map")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM evidence_chains WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

const evidenceChainNodeColumns = "id, chain_id, type, title, body, run_id, project_card_id, x, y, width, height, pinned, occurred_at, data_json, created_at, updated_at"

func scanEvidenceChainNode(n *EvidenceChainNode) func(rowScanner) error {
	return func(row rowScanner) error {
		var pinned int
		var occurredAt sql.NullTime
		if err := row.Scan(&n.ID, &n.ChainID, &n.Type, &n.Title, &n.Body, &n.RunID, &n.ProjectCardID, &n.X, &n.Y, &n.Width, &n.Height, &pinned, &occurredAt, &n.DataJSON, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return err
		}
		n.Pinned = pinned != 0
		if occurredAt.Valid {
			value := occurredAt.Time
			n.OccurredAt = &value
		}
		normalized, err := normalizeEvidenceNodeProvenance(*n)
		if err != nil {
			return err
		}
		*n = normalized
		return nil
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
	_, err := s.SaveEvidenceChainGraphCAS(ctx, chainID, graph, EvidenceGraphSaveOptions{
		ExpectedRevision: -1,
		Actor:            "legacy-client",
		SourceKind:       "legacy-client",
	})
	return err
}

func (s *SQLite) SaveEvidenceChainGraphCAS(ctx context.Context, chainID string, graph EvidenceChainGraph, opts EvidenceGraphSaveOptions) (*EvidenceChain, error) {
	chain, err := s.GetEvidenceChain(ctx, chainID)
	if err != nil {
		return nil, err
	}
	if chain == nil {
		return nil, sql.ErrNoRows
	}
	if chain.Status == "archived" {
		return nil, graphValidationError("GRAPH_ARCHIVED", "archived evidence graph is read-only")
	}
	for i := range graph.Nodes {
		graph.Nodes[i].ChainID = chainID
	}
	for i := range graph.Edges {
		graph.Edges[i].ChainID = chainID
	}
	graphJSON, graphHash, err := CanonicalEvidenceGraph(graph)
	if err != nil {
		return nil, err
	}
	layoutOnly := opts.SourceKind == "replace_graph" && graphHash == chain.GraphHash
	if opts.SourceKind == "replace_graph" {
		if opts.ExpectedRevision < 0 {
			return nil, graphValidationError("EXPECTED_REVISION_REQUIRED", "layout save requires expected_revision")
		}
		if !layoutOnly {
			return nil, graphValidationError("SEMANTIC_WRITE_REQUIRES_PROPOSAL", "direct graph writes may only update layout; semantic changes require an evidence proposal")
		}
	}
	strict := opts.SourceKind != "migration" && !layoutOnly
	if err := validateEvidenceChainGraph(&graph, strict); err != nil {
		return nil, err
	}
	for _, node := range graph.Nodes {
		if node.Type != EvidenceNodeRun {
			continue
		}
		run, err := s.GetRun(ctx, node.RunID)
		if err != nil {
			return nil, err
		}
		if run == nil {
			return nil, graphValidationError("RUN_NOT_FOUND", fmt.Sprintf("run %q does not exist", node.RunID))
		}
		if chain.ProjectID != "" && run.ProjectID != "" && chain.ProjectID != run.ProjectID {
			return nil, graphValidationError("CROSS_PROJECT_RUN", fmt.Sprintf("run %q belongs to project %q, graph belongs to %q", run.ID, run.ProjectID, chain.ProjectID))
		}
		if node.ProjectCardID != "" {
			card, err := s.GetProjectRunCard(ctx, node.RunID)
			if err != nil {
				return nil, err
			}
			if card == nil || card.ID != node.ProjectCardID {
				return nil, graphValidationError("PROJECT_CARD_NOT_FOUND", fmt.Sprintf("project card %q does not match run %q", node.ProjectCardID, node.RunID))
			}
		}
	}
	if err := s.ValidateEvidenceMapReferences(ctx, chain, &graph); err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	saved, err := replaceEvidenceGraphTx(ctx, tx, chainID, graph, opts, graphJSON, graphHash)
	if err != nil {
		var conflict *EvidenceGraphRevisionConflict
		if errors.As(err, &conflict) {
			_ = tx.Rollback()
			if latest, getErr := s.GetEvidenceChain(ctx, chainID); getErr == nil && latest != nil {
				conflict.Current = latest.Revision
				conflict.CurrentHash = latest.GraphHash
			}
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}

func replaceEvidenceGraphTx(ctx context.Context, tx *sql.Tx, chainID string, graph EvidenceChainGraph, opts EvidenceGraphSaveOptions, graphJSON []byte, graphHash string) (*EvidenceChain, error) {
	current := &EvidenceChain{}
	if err := scanEvidenceChain(current)(tx.QueryRowContext(ctx, `SELECT `+evidenceChainColumns+` FROM evidence_chains WHERE id = ?`, chainID)); err != nil {
		return nil, err
	}
	if current.Status == "archived" {
		return nil, graphValidationError("GRAPH_ARCHIVED", "archived evidence graph is read-only")
	}
	if opts.ExpectedRevision >= 0 && current.Revision != opts.ExpectedRevision {
		return nil, &EvidenceGraphRevisionConflict{
			Expected:    opts.ExpectedRevision,
			Current:     current.Revision,
			CurrentHash: current.GraphHash,
		}
	}
	existingNodes, err := listEvidenceChainNodesWith(ctx, tx, chainID)
	if err != nil {
		return nil, err
	}
	existingEdges, err := listEvidenceChainEdgesWith(ctx, tx, chainID)
	if err != nil {
		return nil, err
	}
	nodeCreatedAt := make(map[string]time.Time, len(existingNodes))
	for _, node := range existingNodes {
		nodeCreatedAt[node.ID] = node.CreatedAt
	}
	edgeCreatedAt := make(map[string]time.Time, len(existingEdges))
	for _, edge := range existingEdges {
		edgeCreatedAt[edge.ID] = edge.CreatedAt
	}

	now := time.Now()
	semanticChanged := current.GraphHash != graphHash
	nextRevision := current.Revision
	if semanticChanged {
		nextRevision++
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE evidence_chains SET revision = ?, graph_hash = ?, updated_at = ? WHERE id = ? AND revision = ?`,
		nextRevision, graphHash, now, chainID, current.Revision,
	)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, &EvidenceGraphRevisionConflict{Expected: current.Revision, Current: current.Revision, CurrentHash: current.GraphHash}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM evidence_chain_edges WHERE chain_id = ?`, chainID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM evidence_chain_nodes WHERE chain_id = ?`, chainID); err != nil {
		return nil, err
	}
	for _, n := range graph.Nodes {
		normalized, err := normalizeEvidenceNodeProvenance(n)
		if err != nil {
			return nil, err
		}
		n = normalized
		if createdAt, ok := nodeCreatedAt[n.ID]; ok {
			n.CreatedAt = createdAt
		} else if n.CreatedAt.IsZero() {
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
			`INSERT INTO evidence_chain_nodes (`+evidenceChainNodeColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			n.ID, chainID, n.Type, n.Title, n.Body, n.RunID, n.ProjectCardID, n.X, n.Y, n.Width, n.Height,
			boolInt(n.Pinned), n.OccurredAt, n.DataJSON, n.CreatedAt, n.UpdatedAt,
		); err != nil {
			return nil, err
		}
	}
	for _, e := range graph.Edges {
		if createdAt, ok := edgeCreatedAt[e.ID]; ok {
			e.CreatedAt = createdAt
		} else if e.CreatedAt.IsZero() {
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
			return nil, err
		}
	}
	if semanticChanged {
		revisionID := fmt.Sprintf("egr_%s_%d", chainID, nextRevision)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO evidence_chain_revisions (id, chain_id, revision, graph_hash, graph_json, actor, source_kind, source_id, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			revisionID, chainID, nextRevision, graphHash, string(graphJSON), strings.TrimSpace(opts.Actor),
			strings.TrimSpace(opts.SourceKind), strings.TrimSpace(opts.SourceID), now,
		); err != nil {
			return nil, err
		}
	}
	current.Revision = nextRevision
	current.GraphHash = graphHash
	current.UpdatedAt = now
	return current, nil
}

type evidenceChainQuery interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}

func listEvidenceChainNodesWith(ctx context.Context, queryer evidenceChainQuery, chainID string) ([]EvidenceChainNode, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT `+evidenceChainNodeColumns+` FROM evidence_chain_nodes WHERE chain_id = ? ORDER BY updated_at DESC`, chainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := make([]EvidenceChainNode, 0)
	for rows.Next() {
		var node EvidenceChainNode
		if err := scanEvidenceChainNode(&node)(rows); err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

func listEvidenceChainEdgesWith(ctx context.Context, queryer evidenceChainQuery, chainID string) ([]EvidenceChainEdge, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT `+evidenceChainEdgeColumns+` FROM evidence_chain_edges WHERE chain_id = ? ORDER BY updated_at DESC`, chainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	edges := make([]EvidenceChainEdge, 0)
	for rows.Next() {
		var edge EvidenceChainEdge
		if err := scanEvidenceChainEdge(&edge)(rows); err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}

func (s *SQLite) ListEvidenceChainRevisions(ctx context.Context, chainID string, limit int) ([]EvidenceChainRevision, error) {
	query := `SELECT id, chain_id, revision, graph_hash, graph_json, actor, source_kind, source_id, created_at
		FROM evidence_chain_revisions WHERE chain_id = ? ORDER BY revision DESC`
	args := []interface{}{chainID}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	revisions := make([]EvidenceChainRevision, 0)
	for rows.Next() {
		var revision EvidenceChainRevision
		if err := rows.Scan(&revision.ID, &revision.ChainID, &revision.Revision, &revision.GraphHash, &revision.GraphJSON,
			&revision.Actor, &revision.SourceKind, &revision.SourceID, &revision.CreatedAt); err != nil {
			return nil, err
		}
		revisions = append(revisions, revision)
	}
	return revisions, rows.Err()
}

func (s *SQLite) GetEvidenceChainRevision(ctx context.Context, chainID string, revisionNumber int64) (*EvidenceChainRevision, error) {
	revision := &EvidenceChainRevision{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, chain_id, revision, graph_hash, graph_json, actor, source_kind, source_id, created_at
		 FROM evidence_chain_revisions WHERE chain_id = ? AND revision = ?`,
		chainID, revisionNumber,
	).Scan(&revision.ID, &revision.ChainID, &revision.Revision, &revision.GraphHash, &revision.GraphJSON,
		&revision.Actor, &revision.SourceKind, &revision.SourceID, &revision.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return revision, err
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
