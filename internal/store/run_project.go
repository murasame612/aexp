package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// RunProjectAssignmentWarning describes durable records that intentionally
// remain unchanged when a Run's current organizational ownership changes.
type RunProjectAssignmentWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   int    `json:"count,omitempty"`
}

// RunProjectAssignmentResult is the auditable result of explicitly assigning
// an existing Run to a canonical Project. Immutable launch and evidence
// provenance is never rewritten by this operation.
type RunProjectAssignmentResult struct {
	Run                 *Run                          `json:"run"`
	RunID               string                        `json:"run_id"`
	PreviousProjectID   string                        `json:"previous_project_id,omitempty"`
	ProjectID           string                        `json:"project_id"`
	Actor               string                        `json:"actor"`
	Reason              string                        `json:"reason,omitempty"`
	Changed             bool                          `json:"changed"`
	ChangedAt           time.Time                     `json:"changed_at"`
	ProvenanceUnchanged bool                          `json:"provenance_unchanged"`
	Warnings            []RunProjectAssignmentWarning `json:"warnings"`
}

// RunProjectAssignmentConflict prevents concurrent actors from silently
// overwriting a newer Project assignment.
type RunProjectAssignmentConflict struct {
	RunID             string `json:"run_id"`
	ExpectedProjectID string `json:"expected_project_id,omitempty"`
	CurrentProjectID  string `json:"current_project_id,omitempty"`
}

func (e *RunProjectAssignmentConflict) Error() string {
	return fmt.Sprintf("run %s Project assignment changed: expected %q, current %q", e.RunID, e.ExpectedProjectID, e.CurrentProjectID)
}

func assignmentWarningCount(ctx context.Context, tx *sql.Tx, query string, runID string) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, query, runID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func appendAssignmentWarning(warnings []RunProjectAssignmentWarning, code, message string, count int) []RunProjectAssignmentWarning {
	if count == 0 {
		return warnings
	}
	return append(warnings, RunProjectAssignmentWarning{Code: code, Message: message, Count: count})
}

// AssignRunProject changes only the Run's current organizational ownership.
// It deliberately leaves RunManifest, snapshots, releases, journal references,
// Evidence Maps, and freezes immutable. The legacy Project Run Card projection
// is kept aligned so project-scoped list views do not disagree.
func (s *SQLite) AssignRunProject(ctx context.Context, runID, projectID, expectedProjectID, actor, reason string) (*RunProjectAssignmentResult, error) {
	runID = strings.TrimSpace(runID)
	projectID = strings.TrimSpace(projectID)
	expectedProjectID = strings.TrimSpace(expectedProjectID)
	actor = strings.TrimSpace(actor)
	reason = strings.TrimSpace(reason)
	if runID == "" {
		return nil, &EvidenceGraphValidationError{Code: "RUN_ID_REQUIRED", Message: "run id is required"}
	}
	if projectID == "" {
		return nil, &EvidenceGraphValidationError{Code: "PROJECT_ID_REQUIRED", Message: "target Project is required; Runs cannot be unassigned"}
	}
	if actor == "" {
		actor = "unknown"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var projectName string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM project_definitions WHERE id=?`, projectID).Scan(&projectName); err == sql.ErrNoRows {
		return nil, &EvidenceGraphValidationError{Code: "PROJECT_NOT_FOUND", Message: fmt.Sprintf("project %q is not registered", projectID)}
	} else if err != nil {
		return nil, err
	}

	var currentProjectID, status string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(project_id, ''), status FROM runs WHERE id=?`, runID).Scan(&currentProjectID, &status); err == sql.ErrNoRows {
		return nil, &EvidenceGraphValidationError{Code: "RUN_NOT_FOUND", Message: fmt.Sprintf("run %q does not exist", runID)}
	} else if err != nil {
		return nil, err
	}
	if currentProjectID != expectedProjectID {
		return nil, &RunProjectAssignmentConflict{
			RunID: runID, ExpectedProjectID: expectedProjectID, CurrentProjectID: currentProjectID,
		}
	}

	now := time.Now().UTC()
	result := &RunProjectAssignmentResult{
		RunID: runID, PreviousProjectID: currentProjectID, ProjectID: projectID,
		Actor: actor, Reason: reason, ChangedAt: now, Warnings: []RunProjectAssignmentWarning{},
	}
	if currentProjectID == projectID {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		result.Run, err = s.GetRun(ctx, runID)
		return result, err
	}
	if IsRunActiveLifecycleStatus(status) {
		return nil, &EvidenceGraphValidationError{
			Code:    "RUN_ACTIVE",
			Message: fmt.Sprintf("run %q is %s; wait for a terminal state before changing its Project", runID, status),
		}
	}

	warningQueries := []struct {
		code, message, query string
	}{
		{"RUN_MANIFEST_UNCHANGED", "immutable RunManifest keeps its launch-time Project provenance", `SELECT COUNT(*) FROM run_manifests WHERE run_id=?`},
		{"EVIDENCE_SNAPSHOTS_UNCHANGED", "immutable Evidence Snapshots and Releases remain in their original Project", `SELECT COUNT(*) FROM evidence_snapshots WHERE run_id=?`},
		{"JOURNAL_REFERENCES_UNCHANGED", "existing Project Journal references remain historical and are not moved", `SELECT COUNT(*) FROM project_journal_run_refs WHERE run_id=?`},
		{"EVIDENCE_MAP_REFERENCES_UNCHANGED", "existing Evidence Map nodes remain historical and are not moved", `SELECT COUNT(*) FROM evidence_chain_nodes WHERE run_id=?`},
		{"FREEZES_UNCHANGED", "existing freezes keep their original provenance", `SELECT COUNT(*) FROM run_freezes WHERE run_id=?`},
	}
	for _, warning := range warningQueries {
		count, countErr := assignmentWarningCount(ctx, tx, warning.query, runID)
		if countErr != nil {
			return nil, countErr
		}
		result.Warnings = appendAssignmentWarning(result.Warnings, warning.code, warning.message, count)
	}

	update, err := tx.ExecContext(ctx,
		`UPDATE runs SET project_id=? WHERE id=? AND COALESCE(project_id, '')=?`,
		projectID, runID, expectedProjectID,
	)
	if err != nil {
		return nil, err
	}
	affected, err := update.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		var latest string
		_ = tx.QueryRowContext(ctx, `SELECT COALESCE(project_id, '') FROM runs WHERE id=?`, runID).Scan(&latest)
		return nil, &RunProjectAssignmentConflict{RunID: runID, ExpectedProjectID: expectedProjectID, CurrentProjectID: latest}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE project_run_cards SET project_id=?, project_name=?, updated_at=? WHERE run_id=?`,
		projectID, projectName, now, runID,
	); err != nil {
		return nil, err
	}

	inputJSON, _ := json.Marshal(map[string]string{
		"expected_project_id": expectedProjectID,
		"project_id":          projectID,
		"reason":              reason,
	})
	outputJSON, _ := json.Marshal(map[string]interface{}{
		"previous_project_id":  currentProjectID,
		"project_id":           projectID,
		"provenance_unchanged": true,
		"warnings":             result.Warnings,
	})
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_events (run_id, actor, tool_name, input_json, output_json, timestamp) VALUES (?, ?, ?, ?, ?, ?)`,
		runID, actor, "assign_run_project", string(inputJSON), string(outputJSON), now,
	); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	result.Changed = true
	result.ProvenanceUnchanged = true
	result.Run, err = s.GetRun(ctx, runID)
	return result, err
}
