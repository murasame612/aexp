package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const projectJournalColumns = `id, project_id, actor, title, body_md, next_action, next_action_status, created_at, updated_at`

func scanProjectJournalEntry(row rowScanner, entry *ProjectJournalEntry) error {
	return row.Scan(
		&entry.ID,
		&entry.ProjectID,
		&entry.Actor,
		&entry.Title,
		&entry.BodyMD,
		&entry.NextAction,
		&entry.NextActionStatus,
		&entry.CreatedAt,
		&entry.UpdatedAt,
	)
}

func normalizeProjectJournalEntry(entry *ProjectJournalEntry) error {
	if entry == nil {
		return graphValidationError("JOURNAL_ENTRY_REQUIRED", "journal entry is required")
	}
	entry.ID = strings.TrimSpace(entry.ID)
	entry.ProjectID = strings.TrimSpace(entry.ProjectID)
	entry.Actor = strings.TrimSpace(entry.Actor)
	entry.Title = strings.TrimSpace(entry.Title)
	entry.BodyMD = strings.TrimSpace(entry.BodyMD)
	entry.NextAction = strings.TrimSpace(entry.NextAction)
	if entry.ID == "" {
		return graphValidationError("JOURNAL_ID_REQUIRED", "journal entry id is required")
	}
	if entry.ProjectID == "" {
		return graphValidationError("PROJECT_ID_REQUIRED", "project id is required")
	}
	if entry.Title == "" {
		return graphValidationError("JOURNAL_TITLE_REQUIRED", "journal title is required")
	}
	if entry.Actor == "" {
		entry.Actor = "agent"
	}
	if entry.NextAction == "" {
		entry.NextActionStatus = JournalNextActionNone
	} else if entry.NextActionStatus == "" || entry.NextActionStatus == JournalNextActionNone {
		entry.NextActionStatus = JournalNextActionOpen
	}
	switch entry.NextActionStatus {
	case JournalNextActionNone, JournalNextActionOpen, JournalNextActionDone:
	default:
		return graphValidationError("JOURNAL_NEXT_ACTION_STATUS_INVALID", fmt.Sprintf("invalid next action status %q", entry.NextActionStatus))
	}
	entry.RunIDs = uniqueNonEmptyStrings(entry.RunIDs)
	return nil
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *SQLite) CreateProjectJournalEntry(ctx context.Context, entry *ProjectJournalEntry) error {
	if err := normalizeProjectJournalEntry(entry); err != nil {
		return err
	}
	now := time.Now().UTC()
	entry.CreatedAt = now
	entry.UpdatedAt = now

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var projectExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM project_definitions WHERE id=?`, entry.ProjectID).Scan(&projectExists); err == sql.ErrNoRows {
		return graphValidationError("PROJECT_NOT_FOUND", fmt.Sprintf("project %q does not exist", entry.ProjectID))
	} else if err != nil {
		return err
	}
	for _, runID := range entry.RunIDs {
		var projectID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM runs WHERE id=?`, runID).Scan(&projectID); err == sql.ErrNoRows {
			return graphValidationError("RUN_NOT_FOUND", fmt.Sprintf("run %q does not exist", runID))
		} else if err != nil {
			return err
		}
		if projectID != entry.ProjectID {
			return graphValidationError(
				"RUN_PROJECT_MISMATCH",
				fmt.Sprintf("run %q belongs to project %q, not %q", runID, projectID, entry.ProjectID),
			)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_journal_entries (
		id, project_id, actor, title, body_md, next_action, next_action_status, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID,
		entry.ProjectID,
		entry.Actor,
		entry.Title,
		entry.BodyMD,
		entry.NextAction,
		entry.NextActionStatus,
		entry.CreatedAt,
		entry.UpdatedAt,
	); err != nil {
		return err
	}
	for _, runID := range entry.RunIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO project_journal_run_refs (entry_id, run_id, relation, created_at) VALUES (?, ?, 'related', ?)`,
			entry.ID,
			runID,
			now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) GetProjectJournalEntry(ctx context.Context, id string) (*ProjectJournalEntry, error) {
	var entry ProjectJournalEntry
	if err := scanProjectJournalEntry(
		s.db.QueryRowContext(ctx, `SELECT `+projectJournalColumns+` FROM project_journal_entries WHERE id=?`, id),
		&entry,
	); err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	runIDs, err := s.listProjectJournalRunIDs(ctx, entry.ID)
	if err != nil {
		return nil, err
	}
	entry.RunIDs = runIDs
	return &entry, nil
}

func (s *SQLite) ListProjectJournalEntries(ctx context.Context, filter ProjectJournalFilter) ([]ProjectJournalEntry, error) {
	query := `SELECT ` + projectJournalColumns + ` FROM project_journal_entries entry WHERE 1=1`
	args := make([]interface{}, 0, 8)
	if filter.ProjectID != "" {
		query += ` AND entry.project_id=?`
		args = append(args, filter.ProjectID)
	}
	if filter.RunID != "" {
		query += ` AND EXISTS (
			SELECT 1 FROM project_journal_run_refs ref
			WHERE ref.entry_id=entry.id AND ref.run_id=?
		)`
		args = append(args, filter.RunID)
	}
	if filter.NextActionStatus != "" {
		query += ` AND entry.next_action_status=?`
		args = append(args, filter.NextActionStatus)
	}
	if value := strings.TrimSpace(filter.Query); value != "" {
		pattern := "%" + strings.ToLower(value) + "%"
		query += ` AND (lower(entry.title) LIKE ? OR lower(entry.body_md) LIKE ? OR lower(entry.next_action) LIKE ?)`
		args = append(args, pattern, pattern, pattern)
	}
	query += ` ORDER BY entry.created_at DESC, entry.id DESC`
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		if filter.Limit <= 0 {
			query += ` LIMIT -1`
		}
		query += ` OFFSET ?`
		args = append(args, filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]ProjectJournalEntry, 0)
	for rows.Next() {
		var entry ProjectJournalEntry
		if err := scanProjectJournalEntry(rows, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range entries {
		runIDs, err := s.listProjectJournalRunIDs(ctx, entries[index].ID)
		if err != nil {
			return nil, err
		}
		entries[index].RunIDs = runIDs
	}
	return entries, nil
}

func (s *SQLite) listProjectJournalRunIDs(ctx context.Context, entryID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT run_id FROM project_journal_run_refs WHERE entry_id=? ORDER BY created_at, run_id`,
		entryID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runIDs := make([]string, 0)
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return nil, err
		}
		runIDs = append(runIDs, runID)
	}
	return runIDs, rows.Err()
}

func (s *SQLite) UpdateProjectJournalNextActionStatus(ctx context.Context, id, status string) (*ProjectJournalEntry, error) {
	status = strings.TrimSpace(status)
	if status != JournalNextActionOpen && status != JournalNextActionDone {
		return nil, graphValidationError(
			"JOURNAL_NEXT_ACTION_STATUS_INVALID",
			"next action status must be open or done",
		)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE project_journal_entries
		SET next_action_status=?, updated_at=?
		WHERE id=? AND trim(next_action)<>''`,
		status,
		time.Now().UTC(),
		id,
	)
	if err != nil {
		return nil, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rows == 0 {
		entry, getErr := s.GetProjectJournalEntry(ctx, id)
		if getErr != nil {
			return nil, getErr
		}
		if entry == nil {
			return nil, nil
		}
		return nil, graphValidationError("JOURNAL_NEXT_ACTION_MISSING", "journal entry has no next action")
	}
	return s.GetProjectJournalEntry(ctx, id)
}
