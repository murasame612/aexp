package store

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"strings"
	"time"
)

const logicalRootColumns = "id, workspace, prefix, storage_target_id, physical_root, created_at, updated_at"

func scanLogicalRoot(root *LogicalRoot) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&root.ID, &root.Workspace, &root.Prefix, &root.StorageTargetID, &root.PhysicalRoot, &root.CreatedAt, &root.UpdatedAt)
	}
}

func (s *SQLite) SaveLogicalRoot(ctx context.Context, root *LogicalRoot) error {
	if root == nil || root.ID == "" || root.Workspace == "" {
		return fmt.Errorf("logical root id and workspace are required")
	}
	if !safeLogicalRootPrefix(root.Prefix) || !safeRelativePath(root.PhysicalRoot) {
		return fmt.Errorf("logical root prefix and physical root must be safe relative paths")
	}
	root.Prefix = cleanRelativePath(root.Prefix)
	root.PhysicalRoot = cleanRelativePath(root.PhysicalRoot)
	if root.StorageTargetID == "" {
		return fmt.Errorf("logical root storage target is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var targetCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM storage_targets WHERE id=?`, root.StorageTargetID).Scan(&targetCount); err != nil {
		return err
	}
	if targetCount != 1 {
		return fmt.Errorf("storage target %s not found", root.StorageTargetID)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, prefix FROM logical_roots WHERE workspace=? AND id<>?`, root.Workspace, root.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, prefix string
		if err := rows.Scan(&id, &prefix); err != nil {
			rows.Close()
			return err
		}
		// A workspace root is the fallback for logical paths that do not belong
		// to a more specific child root (for example datasets). ResolveRoot uses
		// longest-prefix matching, so this one intentional overlap is safe.
		// Non-empty roots remain non-overlapping to avoid ambiguous ownership.
		if root.Prefix == prefix || (root.Prefix != "" && prefix != "" && pathsOverlap(root.Prefix, prefix)) {
			rows.Close()
			return fmt.Errorf("logical root %s overlaps root %s at %s", root.Prefix, id, prefix)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := time.Now()
	if root.CreatedAt.IsZero() {
		root.CreatedAt = now
	}
	root.UpdatedAt = now
	_, err = tx.ExecContext(ctx, `INSERT INTO logical_roots (`+logicalRootColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET workspace=excluded.workspace, prefix=excluded.prefix, storage_target_id=excluded.storage_target_id, physical_root=excluded.physical_root, updated_at=excluded.updated_at`,
		root.ID, root.Workspace, root.Prefix, root.StorageTargetID, root.PhysicalRoot, root.CreatedAt, root.UpdatedAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) GetLogicalRoot(ctx context.Context, id string) (*LogicalRoot, error) {
	root := &LogicalRoot{}
	err := scanLogicalRoot(root)(s.db.QueryRowContext(ctx, `SELECT `+logicalRootColumns+` FROM logical_roots WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return root, err
}

func (s *SQLite) ListLogicalRoots(ctx context.Context, workspace string) ([]LogicalRoot, error) {
	query := `SELECT ` + logicalRootColumns + ` FROM logical_roots`
	var args []any
	if workspace != "" {
		query += ` WHERE workspace=?`
		args = append(args, workspace)
	}
	query += ` ORDER BY workspace, length(prefix) DESC, prefix`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roots := make([]LogicalRoot, 0)
	for rows.Next() {
		var root LogicalRoot
		if err := scanLogicalRoot(&root)(rows); err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, rows.Err()
}

func (s *SQLite) DeleteLogicalRoot(ctx context.Context, id string) error {
	root, err := s.GetLogicalRoot(ctx, id)
	if err != nil || root == nil {
		if err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	uriPrefix := "aexp://" + root.Workspace + "/" + root.Prefix
	var placements int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM path_placements WHERE logical_uri=? OR logical_uri LIKE ?`, uriPrefix, uriPrefix+"/%").Scan(&placements); err != nil {
		return err
	}
	if placements > 0 {
		return fmt.Errorf("logical root is referenced by %d placement(s)", placements)
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM logical_roots WHERE id=?`, id)
	return err
}

const pathPlacementColumns = "id, logical_uri, resource_id, storage_target_id, physical_path, role, desired_state, observed_state, revision, manifest_sha256, bytes_present, observation_source, observed_at, checked_at, observation_error, created_at, updated_at"

func scanPathPlacement(placement *PathPlacement) func(rowScanner) error {
	return func(row rowScanner) error {
		var storageTargetID sql.NullString
		if err := row.Scan(&placement.ID, &placement.LogicalURI, &placement.ResourceID, &storageTargetID, &placement.PhysicalPath, &placement.Role, &placement.DesiredState, &placement.ObservedState, &placement.Revision, &placement.ManifestSHA256, &placement.BytesPresent, &placement.ObservationSource, &placement.ObservedAt, &placement.CheckedAt, &placement.ObservationError, &placement.CreatedAt, &placement.UpdatedAt); err != nil {
			return err
		}
		placement.StorageTargetID = storageTargetID.String
		return nil
	}
}

func (s *SQLite) SavePathPlacement(ctx context.Context, placement *PathPlacement) error {
	if err := s.preparePathPlacement(ctx, placement); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO path_placements (`+pathPlacementColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET logical_uri=excluded.logical_uri, resource_id=excluded.resource_id, storage_target_id=excluded.storage_target_id, physical_path=excluded.physical_path, role=excluded.role, desired_state=excluded.desired_state, updated_at=excluded.updated_at`,
		placement.ID, placement.LogicalURI, placement.ResourceID, nullableString(placement.StorageTargetID), placement.PhysicalPath, placement.Role, placement.DesiredState, placement.ObservedState, placement.Revision, placement.ManifestSHA256, placement.BytesPresent, placement.ObservationSource, placement.ObservedAt, placement.CheckedAt, placement.ObservationError, placement.CreatedAt, placement.UpdatedAt)
	return err
}

func (s *SQLite) preparePathPlacement(ctx context.Context, placement *PathPlacement) error {
	if placement == nil || placement.ID == "" || placement.LogicalURI == "" || placement.ResourceID == "" || placement.PhysicalPath == "" {
		return fmt.Errorf("placement id, logical URI, resource, and physical path are required")
	}
	if !validLogicalURIShape(placement.LogicalURI) || hasUnsafePathText(placement.PhysicalPath) {
		return fmt.Errorf("placement contains an unsafe logical URI or physical path")
	}
	if placement.Role == "" {
		placement.Role = PlacementRoleCache
	}
	if !oneOf(placement.Role, PlacementRoleAuthoritative, PlacementRoleReplica, PlacementRoleCache, PlacementRoleProjection) {
		return fmt.Errorf("invalid placement role %q", placement.Role)
	}
	if placement.DesiredState == "" {
		placement.DesiredState = PlacementDesiredPresent
	}
	if !oneOf(placement.DesiredState, PlacementDesiredPresent, PlacementDesiredAbsent) {
		return fmt.Errorf("invalid placement desired state %q", placement.DesiredState)
	}
	if placement.ObservedState == "" {
		placement.ObservedState = PlacementObservedUnknown
	}
	if !validObservedState(placement.ObservedState) {
		return fmt.Errorf("invalid placement observed state %q", placement.ObservedState)
	}
	if resource, err := s.GetResource(ctx, placement.ResourceID); err != nil {
		return err
	} else if resource == nil {
		return fmt.Errorf("resource %s not found", placement.ResourceID)
	}
	if placement.StorageTargetID != "" {
		target, err := s.GetStorageTarget(ctx, placement.StorageTargetID)
		if err != nil {
			return err
		}
		if target == nil {
			return fmt.Errorf("storage target %s not found", placement.StorageTargetID)
		}
		if target.ResourceID != placement.ResourceID {
			return fmt.Errorf("storage target %s belongs to resource %s, not %s", target.ID, target.ResourceID, placement.ResourceID)
		}
	}
	now := time.Now()
	if placement.CreatedAt.IsZero() {
		placement.CreatedAt = now
	}
	placement.UpdatedAt = now
	return nil
}

func (s *SQLite) GetPathPlacement(ctx context.Context, id string) (*PathPlacement, error) {
	placement := &PathPlacement{}
	err := scanPathPlacement(placement)(s.db.QueryRowContext(ctx, `SELECT `+pathPlacementColumns+` FROM path_placements WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return placement, err
}

func (s *SQLite) ListPathPlacements(ctx context.Context, logicalURI string) ([]PathPlacement, error) {
	query := `SELECT ` + pathPlacementColumns + ` FROM path_placements`
	var args []any
	if logicalURI != "" {
		query += ` WHERE logical_uri=?`
		args = append(args, logicalURI)
	}
	query += ` ORDER BY CASE role WHEN 'authoritative' THEN 0 WHEN 'replica' THEN 1 WHEN 'cache' THEN 2 ELSE 3 END, updated_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	placements := make([]PathPlacement, 0)
	for rows.Next() {
		var placement PathPlacement
		if err := scanPathPlacement(&placement)(rows); err != nil {
			return nil, err
		}
		placements = append(placements, placement)
	}
	return placements, rows.Err()
}

// UpdatePathPlacementObservation uses checked_at as a compare-and-swap token:
// a late remote probe can never overwrite a newer observation.
func (s *SQLite) UpdatePathPlacementObservation(ctx context.Context, id string, observation PlacementObservation) (bool, error) {
	if observation.CheckedAt.IsZero() || !validObservedState(observation.State) {
		return false, fmt.Errorf("valid observed state and checked_at are required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE path_placements SET observed_state=?, revision=?, manifest_sha256=?, bytes_present=?, observation_source=?, observed_at=?, checked_at=?, observation_error=?, updated_at=? WHERE id=? AND (checked_at IS NULL OR checked_at < ?)`,
		observation.State, observation.Revision, observation.ManifestSHA256, observation.BytesPresent, observation.Source, observation.ObservedAt, observation.CheckedAt, observation.Error, observation.CheckedAt, id, observation.CheckedAt)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

const transferPlanColumns = "plan_sha256, workspace, source_uri, destination_uri, source_placement_id, destination_placement_id, source_revision, plan_json, expires_at, created_at"
const transferJobColumns = "id, plan_sha256, state, stage, attempt, bytes_done, total_bytes, files_done, file_count, initiator, command_resource_id, heartbeat_at, error_code, last_error, retryable, created_at, updated_at, started_at, finished_at"

func scanTransferPlan(plan *TransferPlan) func(rowScanner) error {
	return func(row rowScanner) error {
		var sourcePlacementID, destinationPlacementID sql.NullString
		if err := row.Scan(&plan.PlanSHA256, &plan.Workspace, &plan.SourceURI, &plan.DestinationURI, &sourcePlacementID, &destinationPlacementID, &plan.SourceRevision, &plan.PlanJSON, &plan.ExpiresAt, &plan.CreatedAt); err != nil {
			return err
		}
		plan.SourcePlacementID = sourcePlacementID.String
		plan.DestinationPlacementID = destinationPlacementID.String
		return nil
	}
}

func scanTransferJob(job *TransferJob) func(rowScanner) error {
	return func(row rowScanner) error {
		return row.Scan(&job.ID, &job.PlanSHA256, &job.State, &job.Stage, &job.Attempt, &job.BytesDone, &job.TotalBytes, &job.FilesDone, &job.FileCount, &job.Initiator, &job.CommandResourceID, &job.HeartbeatAt, &job.ErrorCode, &job.LastError, &job.Retryable, &job.CreatedAt, &job.UpdatedAt, &job.StartedAt, &job.FinishedAt)
	}
}

func (s *SQLite) CreateTransferJobWithPlan(ctx context.Context, plan *TransferPlan, job *TransferJob, placements ...*PathPlacement) (*TransferJob, bool, error) {
	if plan == nil || job == nil || plan.PlanSHA256 == "" || job.ID == "" {
		return nil, false, fmt.Errorf("transfer plan hash and job id are required")
	}
	if job.PlanSHA256 == "" {
		job.PlanSHA256 = plan.PlanSHA256
	}
	if job.PlanSHA256 != plan.PlanSHA256 {
		return nil, false, fmt.Errorf("job plan hash does not match transfer plan")
	}
	if plan.SourceURI == "" || plan.DestinationURI == "" || plan.ExpiresAt.IsZero() {
		return nil, false, fmt.Errorf("transfer plan source, destination, and expiry are required")
	}
	if plan.PlanJSON == "" {
		plan.PlanJSON = "{}"
	}
	now := time.Now()
	if plan.CreatedAt.IsZero() {
		plan.CreatedAt = now
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	if job.State == "" {
		job.State = TransferQueued
	}
	if job.Stage == "" {
		job.Stage = job.State
	}
	for _, placement := range placements {
		if err := s.preparePathPlacement(ctx, placement); err != nil {
			return nil, false, err
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	existing := &TransferJob{}
	err = scanTransferJob(existing)(tx.QueryRowContext(ctx, `SELECT `+transferJobColumns+` FROM transfer_jobs WHERE plan_sha256=?`, plan.PlanSHA256))
	if err == nil {
		return existing, false, nil
	}
	if err != sql.ErrNoRows {
		return nil, false, err
	}
	for _, placement := range placements {
		if _, err := tx.ExecContext(ctx, `INSERT INTO path_placements (`+pathPlacementColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET logical_uri=excluded.logical_uri, resource_id=excluded.resource_id, storage_target_id=excluded.storage_target_id, physical_path=excluded.physical_path, role=excluded.role, desired_state=excluded.desired_state, updated_at=excluded.updated_at`,
			placement.ID, placement.LogicalURI, placement.ResourceID, nullableString(placement.StorageTargetID), placement.PhysicalPath, placement.Role, placement.DesiredState, placement.ObservedState, placement.Revision, placement.ManifestSHA256, placement.BytesPresent, placement.ObservationSource, placement.ObservedAt, placement.CheckedAt, placement.ObservationError, placement.CreatedAt, placement.UpdatedAt); err != nil {
			return nil, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO transfer_plans (`+transferPlanColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		plan.PlanSHA256, plan.Workspace, plan.SourceURI, plan.DestinationURI, nullableString(plan.SourcePlacementID), nullableString(plan.DestinationPlacementID), plan.SourceRevision, plan.PlanJSON, plan.ExpiresAt, plan.CreatedAt); err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO transfer_jobs (`+transferJobColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.PlanSHA256, job.State, job.Stage, job.Attempt, job.BytesDone, job.TotalBytes, job.FilesDone, job.FileCount, job.Initiator, job.CommandResourceID, job.HeartbeatAt, job.ErrorCode, job.LastError, job.Retryable, job.CreatedAt, job.UpdatedAt, job.StartedAt, job.FinishedAt); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	copy := *job
	return &copy, true, nil
}

func (s *SQLite) GetTransferPlan(ctx context.Context, planSHA256 string) (*TransferPlan, error) {
	plan := &TransferPlan{}
	err := scanTransferPlan(plan)(s.db.QueryRowContext(ctx, `SELECT `+transferPlanColumns+` FROM transfer_plans WHERE plan_sha256=?`, planSHA256))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return plan, err
}

func (s *SQLite) GetTransferJob(ctx context.Context, id string) (*TransferJob, error) {
	job := &TransferJob{}
	err := scanTransferJob(job)(s.db.QueryRowContext(ctx, `SELECT `+transferJobColumns+` FROM transfer_jobs WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return job, err
}

func (s *SQLite) ListTransferJobs(ctx context.Context, state string, limit int) ([]TransferJob, error) {
	return s.ListTransferJobsPage(ctx, state, "", nil, limit, 0)
}

func (s *SQLite) ListTransferJobsPage(ctx context.Context, state, workspace string, updatedSince *time.Time, limit, offset int) ([]TransferJob, error) {
	query := `SELECT ` + transferJobColumns + ` FROM transfer_jobs`
	var args []any
	conditions := make([]string, 0, 3)
	if state != "" {
		conditions = append(conditions, `state=?`)
		args = append(args, state)
	}
	if workspace != "" {
		conditions = append(conditions, `plan_sha256 IN (SELECT plan_sha256 FROM transfer_plans WHERE workspace=?)`)
		args = append(args, workspace)
	}
	if updatedSince != nil {
		conditions = append(conditions, `updated_at>=?`)
		args = append(args, updatedSince.UTC())
	}
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	query += ` ORDER BY updated_at DESC`
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query += ` LIMIT ?`
	args = append(args, limit)
	if offset > 0 {
		query += ` OFFSET ?`
		args = append(args, offset)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]TransferJob, 0)
	for rows.Next() {
		var job TransferJob
		if err := scanTransferJob(&job)(rows); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *SQLite) ClaimTransferJob(ctx context.Context, id string) (*TransferJob, bool, error) {
	now := time.Now()
	result, err := s.db.ExecContext(ctx, `UPDATE transfer_jobs SET state=?, stage=?, attempt=attempt+1, heartbeat_at=?, started_at=COALESCE(started_at, ?), updated_at=? WHERE id=? AND state=?`, TransferPlanning, TransferPlanning, now, now, now, id, TransferQueued)
	if err != nil {
		return nil, false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return nil, false, err
	}
	job, err := s.GetTransferJob(ctx, id)
	return job, err == nil, err
}

// RequeueCompletedTransferJob repairs a verified placement that disappeared
// after an earlier successful transfer. The plan and transfer identity remain
// stable, while the append-only attempt ledger records the new materialization.
func (s *SQLite) RequeueCompletedTransferJob(ctx context.Context, id, planSHA256 string, totalBytes, fileCount int64) (*TransferJob, bool, error) {
	if id == "" || planSHA256 == "" || totalBytes < 0 || fileCount < 0 {
		return nil, false, fmt.Errorf("valid transfer id, plan hash, and totals are required")
	}
	now := time.Now()
	result, err := s.db.ExecContext(ctx, `UPDATE transfer_jobs SET state=?, stage=?, bytes_done=0, total_bytes=?, files_done=0, file_count=?, initiator='', command_resource_id='', heartbeat_at=NULL, error_code='', last_error='', retryable=1, updated_at=?, started_at=NULL, finished_at=NULL WHERE id=? AND plan_sha256=? AND state=?`,
		TransferQueued, TransferQueued, totalBytes, fileCount, now, id, planSHA256, TransferCompleted)
	if err != nil {
		return nil, false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	job, getErr := s.GetTransferJob(ctx, id)
	return job, count == 1, getErr
}

func (s *SQLite) TouchTransferJobHeartbeat(ctx context.Context, id, expectedState string, at time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE transfer_jobs SET heartbeat_at=? WHERE id=? AND state=?`, at.UTC(), id, expectedState)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *SQLite) UpdateTransferJobIfState(ctx context.Context, job *TransferJob, expectedState string) (bool, error) {
	if job == nil || job.ID == "" || !validTransferTransition(expectedState, job.State) {
		return false, fmt.Errorf("invalid transfer state transition %s -> %s", expectedState, stateOf(job))
	}
	if job.BytesDone < 0 || job.FilesDone < 0 || job.TotalBytes < job.BytesDone || job.FileCount < job.FilesDone {
		return false, fmt.Errorf("invalid transfer progress")
	}
	job.UpdatedAt = time.Now()
	result, err := s.db.ExecContext(ctx, `UPDATE transfer_jobs SET state=?, stage=?, attempt=?, bytes_done=?, total_bytes=?, files_done=?, file_count=?, initiator=?, command_resource_id=?, heartbeat_at=?, error_code=?, last_error=?, retryable=?, updated_at=?, started_at=?, finished_at=? WHERE id=? AND state=? AND bytes_done<=? AND files_done<=?`,
		job.State, job.Stage, job.Attempt, job.BytesDone, job.TotalBytes, job.FilesDone, job.FileCount, job.Initiator, job.CommandResourceID, job.HeartbeatAt, job.ErrorCode, job.LastError, job.Retryable, job.UpdatedAt, job.StartedAt, job.FinishedAt, job.ID, expectedState, job.BytesDone, job.FilesDone)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

const transferAttemptColumns = "id, transfer_id, number, initiator, state, error_code, last_error, started_at, finished_at"

func (s *SQLite) SaveTransferAttempt(ctx context.Context, attempt *TransferAttempt) error {
	if attempt == nil || attempt.ID == "" || attempt.TransferID == "" || attempt.Number <= 0 || attempt.Initiator == "" || attempt.State == "" {
		return fmt.Errorf("complete transfer attempt identity is required")
	}
	if attempt.StartedAt.IsZero() {
		attempt.StartedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO transfer_attempts (`+transferAttemptColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(transfer_id, number) DO UPDATE SET state=excluded.state, error_code=excluded.error_code, last_error=excluded.last_error, finished_at=excluded.finished_at WHERE transfer_attempts.finished_at IS NULL`,
		attempt.ID, attempt.TransferID, attempt.Number, attempt.Initiator, attempt.State, attempt.ErrorCode, attempt.LastError, attempt.StartedAt, attempt.FinishedAt)
	return err
}

func (s *SQLite) ListTransferAttempts(ctx context.Context, transferID string) ([]TransferAttempt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+transferAttemptColumns+` FROM transfer_attempts WHERE transfer_id=? ORDER BY number`, transferID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attempts := make([]TransferAttempt, 0)
	for rows.Next() {
		var attempt TransferAttempt
		if err := rows.Scan(&attempt.ID, &attempt.TransferID, &attempt.Number, &attempt.Initiator, &attempt.State, &attempt.ErrorCode, &attempt.LastError, &attempt.StartedAt, &attempt.FinishedAt); err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

func cleanRelativePath(value string) string {
	return strings.Trim(path.Clean("/"+value), "/")
}

func safeRelativePath(value string) bool {
	if value == "" || value == "." || strings.HasPrefix(value, "/") || hasUnsafePathText(value) || strings.Contains(value, `\`) {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func safeLogicalRootPrefix(value string) bool {
	if value == "" {
		return true
	}
	return safeRelativePath(value)
}

func pathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func hasUnsafePathText(value string) bool {
	return strings.ContainsAny(value, "\x00\r\n")
}

func validLogicalURIShape(value string) bool {
	if !strings.HasPrefix(value, "aexp://") || hasUnsafePathText(value) || strings.Contains(value, `\`) {
		return false
	}
	rest := strings.TrimPrefix(value, "aexp://")
	parts := strings.SplitN(rest, "/", 2)
	return len(parts) == 2 && parts[0] != "" && safeRelativePath(parts[1])
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func validObservedState(state string) bool {
	return oneOf(state, PlacementObservedPresent, PlacementObservedMissing, PlacementObservedUnknown, PlacementObservedUnreachable, PlacementObservedConflict)
}

func validTransferTransition(from, to string) bool {
	if from == to {
		return !oneOf(from, TransferCompleted, TransferCancelled)
	}
	allowed := map[string][]string{
		TransferQueued:       {TransferPlanning, TransferCancelling, TransferCancelled},
		TransferPlanning:     {TransferTransferring, TransferBlocked, TransferFailed, TransferCancelling},
		TransferTransferring: {TransferVerifying, TransferFailed, TransferCancelling},
		TransferVerifying:    {TransferPromoting, TransferBlocked, TransferFailed, TransferCancelling},
		TransferPromoting:    {TransferCompleted, TransferBlocked, TransferFailed, TransferCancelling},
		TransferCancelling:   {TransferCancelled, TransferCompleted, TransferFailed},
		TransferFailed:       {TransferQueued},
		TransferBlocked:      {TransferQueued},
	}
	return oneOf(to, allowed[from]...)
}

func stateOf(job *TransferJob) string {
	if job == nil {
		return "<nil>"
	}
	return job.State
}
