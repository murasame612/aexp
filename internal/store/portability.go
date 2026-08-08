package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// PathPrefixMapping is an explicit old-controller to new-controller path
// mapping. Only exact matches and path-segment descendants are rewritten.
type PathPrefixMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type PortabilityRewriteSummary struct {
	ResourceBindingsCleared int `json:"resource_bindings_cleared"`
	AttachmentPathsUpdated  int `json:"attachment_paths_updated"`
	MappedPathsUpdated      int `json:"mapped_paths_updated"`
}

func (s *SQLite) IntegrityCheck(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("run SQLite integrity check: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("read SQLite integrity check: %w", err)
		}
		if result != "ok" {
			return fmt.Errorf("SQLite integrity check failed: %s", result)
		}
	}
	return rows.Err()
}

// PreparePortabilityCopy mutates only an imported database copy. It clears
// controller-specific SSH bindings, points bundled attachments at their
// restored files, and applies explicit path-prefix mappings to known path
// columns. Opaque JSON and command fields are deliberately not rewritten.
func (s *SQLite) PreparePortabilityCopy(ctx context.Context, attachmentPaths map[string]string, mappings []PathPrefixMapping) (PortabilityRewriteSummary, error) {
	var summary PortabilityRewriteSummary
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return summary, fmt.Errorf("begin portability rewrite: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `UPDATE resources
SET auth_ref='', socks_proxy='', proxy_command='', ssh_status='unknown',
    last_doctor_error='portability import requires resource rebind',
    last_checked_at=NULL, last_success_at=NULL
WHERE type='ssh'`)
	if err != nil {
		return summary, fmt.Errorf("clear imported resource bindings: %w", err)
	}
	if count, countErr := result.RowsAffected(); countErr == nil {
		summary.ResourceBindingsCleared = int(count)
	}

	for attachmentID, restoredPath := range attachmentPaths {
		result, err := tx.ExecContext(ctx, `UPDATE run_mark_attachments SET local_path=? WHERE id=?`, filepath.Clean(restoredPath), attachmentID)
		if err != nil {
			return summary, fmt.Errorf("rewrite attachment %s: %w", attachmentID, err)
		}
		if count, countErr := result.RowsAffected(); countErr == nil {
			summary.AttachmentPathsUpdated += int(count)
		}
	}

	columns := []struct{ table, column string }{
		{"resources", "root_dir"},
		{"resources", "conda_base"},
		{"project_definitions", "local_root"},
		{"project_definitions", "config_path"},
		{"project_definitions", "vault"},
		{"project_definitions", "run_card_index"},
		{"project_definitions", "proposal_dir"},
		{"project_targets", "cwd"},
		{"project_targets", "ui_events_path"},
		{"project_targets", "sync_source"},
		{"project_targets", "sync_target"},
		{"runs", "cwd"},
		{"runs", "git_repo_root"},
		{"runs", "git_diff_path"},
		{"runs", "resolved_python"},
		{"runs", "resolved_cwd"},
		{"runs", "ui_events_path"},
		{"runs", "remote_run_dir"},
		{"artifacts", "path"},
		{"storage_targets", "root_path"},
		{"path_placements", "physical_path"},
		{"dataset_versions", "storage_path"},
		{"dataset_materializations", "local_path"},
		{"run_mark_attachments", "local_path"},
		{"exec_events", "cwd"},
	}
	for _, target := range columns {
		query := fmt.Sprintf(`SELECT rowid, %s FROM %s WHERE %s <> ''`, target.column, target.table, target.column)
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return summary, fmt.Errorf("list %s.%s paths: %w", target.table, target.column, err)
		}
		type rewrite struct {
			rowID int64
			value string
		}
		var rewrites []rewrite
		for rows.Next() {
			var rowID int64
			var value string
			if err := rows.Scan(&rowID, &value); err != nil {
				rows.Close()
				return summary, fmt.Errorf("scan %s.%s path: %w", target.table, target.column, err)
			}
			mapped, changed := mapPathPrefix(value, mappings)
			if changed {
				rewrites = append(rewrites, rewrite{rowID: rowID, value: mapped})
			}
		}
		if err := rows.Close(); err != nil {
			return summary, fmt.Errorf("close %s.%s paths: %w", target.table, target.column, err)
		}
		for _, item := range rewrites {
			update := fmt.Sprintf(`UPDATE %s SET %s=? WHERE rowid=?`, target.table, target.column)
			if _, err := tx.ExecContext(ctx, update, item.value, item.rowID); err != nil {
				return summary, fmt.Errorf("update %s.%s path: %w", target.table, target.column, err)
			}
			summary.MappedPathsUpdated++
		}
	}

	if err := tx.Commit(); err != nil {
		return summary, fmt.Errorf("commit portability rewrite: %w", err)
	}
	return summary, nil
}

func mapPathPrefix(value string, mappings []PathPrefixMapping) (string, bool) {
	for _, mapping := range mappings {
		from := filepath.Clean(strings.TrimSpace(mapping.From))
		to := filepath.Clean(strings.TrimSpace(mapping.To))
		if from == "." || to == "." || !filepath.IsAbs(from) || !filepath.IsAbs(to) {
			continue
		}
		cleaned := filepath.Clean(value)
		if cleaned == from {
			return to, true
		}
		prefix := from + string(filepath.Separator)
		if strings.HasPrefix(cleaned, prefix) {
			return filepath.Join(to, strings.TrimPrefix(cleaned, prefix)), true
		}
	}
	return value, false
}
