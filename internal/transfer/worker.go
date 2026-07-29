package transfer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

type CopyRequest struct {
	TransferID  string `json:"transfer_id"`
	Plan        Plan   `json:"plan"`
	Route       Route  `json:"route"`
	StagingPath string `json:"staging_path"`
}

type Progress struct {
	BytesDone int64
	FilesDone int64
}

type VerifyResult struct {
	Revision   string
	TotalBytes int64
	FileCount  int64
}

type Transport interface {
	Copy(ctx context.Context, request CopyRequest, progress func(Progress) error) error
	Verify(ctx context.Context, request CopyRequest) (VerifyResult, error)
	Promote(ctx context.Context, request CopyRequest) error
}

type OperationError struct {
	Code      string
	Retryable bool
	Conflict  bool
	Err       error
}

func (e *OperationError) Error() string { return e.Err.Error() }
func (e *OperationError) Unwrap() error { return e.Err }

type Worker struct {
	Store             store.Store
	Transport         Transport
	Now               func() time.Time
	HeartbeatInterval time.Duration
}

func NewWorker(db store.Store, transport Transport) *Worker {
	return &Worker{Store: db, Transport: transport, Now: time.Now, HeartbeatInterval: 5 * time.Second}
}

// Execute claims a queued job and drives its durable state machine. It is safe
// for multiple workers to call Execute for the same id; only one claim wins.
func (w *Worker) Execute(ctx context.Context, transferID string) error {
	job, claimed, err := w.Store.ClaimTransferJob(ctx, transferID)
	if err != nil {
		return err
	}
	if !claimed {
		current, getErr := w.Store.GetTransferJob(ctx, transferID)
		if getErr != nil {
			return getErr
		}
		if current == nil {
			return fmt.Errorf("transfer %s not found", transferID)
		}
		if oneOf(current.State, store.TransferCompleted, store.TransferCancelled) {
			return nil
		}
		return fmt.Errorf("transfer %s is already being handled in state %s", transferID, current.State)
	}
	return w.executeClaimed(ctx, job, store.TransferPlanning)
}

// Resume continues a non-terminal job after a control-plane restart. Copy uses
// the same transfer-specific staging path, so retry-capable transports resume
// rather than creating a second payload tree.
func (w *Worker) Resume(ctx context.Context, transferID string) error {
	job, err := w.Store.GetTransferJob(ctx, transferID)
	if err != nil || job == nil {
		if err == nil {
			err = fmt.Errorf("transfer %s not found", transferID)
		}
		return err
	}
	switch job.State {
	case store.TransferQueued:
		return w.Execute(ctx, transferID)
	case store.TransferPlanning, store.TransferTransferring, store.TransferVerifying, store.TransferPromoting:
		return w.executeClaimed(ctx, job, job.State)
	case store.TransferCancelling:
		return w.finishCancelled(ctx, job)
	case store.TransferCompleted, store.TransferCancelled:
		return nil
	default:
		return fmt.Errorf("transfer %s cannot resume from state %s", transferID, job.State)
	}
}

func (w *Worker) executeClaimed(ctx context.Context, job *store.TransferJob, resumeState string) error {
	planRecord, err := w.Store.GetTransferPlan(ctx, job.PlanSHA256)
	if err != nil {
		return w.fail(ctx, job, resumeState, "plan_read_failed", true, err)
	}
	plan, err := DecodePlan(planRecord)
	if err != nil {
		return w.fail(ctx, job, resumeState, "plan_invalid", false, err)
	}
	if len(plan.Blockers) > 0 {
		return w.block(ctx, job, resumeState, "plan_blocked", fmt.Errorf("persisted transfer plan contains blockers"))
	}
	request := CopyRequest{TransferID: job.ID, Plan: plan, StagingPath: strings.ReplaceAll(plan.StagingPath, "{transfer_id}", job.ID)}

	if resumeState == store.TransferPlanning || resumeState == store.TransferTransferring {
		if resumeState == store.TransferPlanning {
			if err := w.transition(ctx, job, store.TransferPlanning, store.TransferTransferring); err != nil {
				return err
			}
		}
		if err := w.copyWithFallback(ctx, job, request); err != nil {
			return err
		}
		if err := w.transition(ctx, job, store.TransferTransferring, store.TransferVerifying); err != nil {
			return err
		}
	}

	if job.State == store.TransferVerifying || resumeState == store.TransferVerifying {
		var verification VerifyResult
		err := w.withHeartbeat(ctx, job, store.TransferVerifying, func(operationContext context.Context) error {
			var verifyErr error
			verification, verifyErr = w.Transport.Verify(operationContext, request)
			return verifyErr
		})
		if err != nil {
			return w.handleOperationError(ctx, job, store.TransferVerifying, "verify_failed", err)
		}
		if verification.Revision != plan.Source.Revision {
			return w.fail(ctx, job, store.TransferVerifying, "revision_mismatch", true, fmt.Errorf("destination staging revision %s does not match source %s", verification.Revision, plan.Source.Revision))
		}
		if verification.TotalBytes > 0 {
			job.TotalBytes, job.BytesDone = verification.TotalBytes, verification.TotalBytes
		}
		if verification.FileCount > 0 {
			job.FileCount, job.FilesDone = verification.FileCount, verification.FileCount
		}
		if err := w.transition(ctx, job, store.TransferVerifying, store.TransferPromoting); err != nil {
			return err
		}
	}

	if job.State == store.TransferPromoting || resumeState == store.TransferPromoting {
		if err := w.withHeartbeat(ctx, job, store.TransferPromoting, func(operationContext context.Context) error {
			return w.Transport.Promote(operationContext, request)
		}); err != nil {
			return w.handleOperationError(ctx, job, store.TransferPromoting, "promotion_failed", err)
		}
		if err := w.recordDestinationPlacement(ctx, plan, job); err != nil {
			return w.fail(ctx, job, store.TransferPromoting, "placement_record_failed", true, err)
		}
		now := w.now()
		job.FinishedAt = &now
		job.Retryable = false
		if err := w.transition(ctx, job, store.TransferPromoting, store.TransferCompleted); err != nil {
			current, getErr := w.Store.GetTransferJob(ctx, job.ID)
			if getErr != nil || current == nil || current.State != store.TransferCancelling {
				return err
			}
			// Atomic promotion already won the race with cancellation. Reporting
			// completed is the only truthful terminal state: final now contains
			// the fully verified revision and the source remains untouched.
			current.FinishedAt, current.Retryable = &now, false
			if transitionErr := w.transition(ctx, current, store.TransferCancelling, store.TransferCompleted); transitionErr != nil {
				return transitionErr
			}
		}
	}
	return nil
}

func (w *Worker) recordDestinationPlacement(ctx context.Context, plan Plan, job *store.TransferJob) error {
	if plan.Destination.LogicalURI == "" || plan.Destination.PlacementID == "" {
		return nil
	}
	role := plan.Destination.Role
	if role == "" {
		role = store.PlacementRoleAuthoritative
	}
	placement := &store.PathPlacement{
		ID: plan.Destination.PlacementID, LogicalURI: plan.Destination.LogicalURI,
		ResourceID: plan.Destination.ResourceID, StorageTargetID: plan.Destination.StorageTargetID,
		PhysicalPath: plan.Destination.PhysicalPath, Role: role, DesiredState: store.PlacementDesiredPresent,
	}
	if err := w.Store.SavePathPlacement(ctx, placement); err != nil {
		return err
	}
	now := w.now()
	manifest := ""
	if job.FileCount != 1 {
		manifest = plan.Source.Revision
	}
	updated, err := w.Store.UpdatePathPlacementObservation(ctx, placement.ID, store.PlacementObservation{
		State: store.PlacementObservedPresent, Revision: plan.Source.Revision, ManifestSHA256: manifest,
		BytesPresent: job.BytesDone, Source: "transfer_verify", ObservedAt: &now, CheckedAt: now,
	})
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("destination placement observation was superseded")
	}
	return nil
}

func (w *Worker) copyWithFallback(ctx context.Context, job *store.TransferJob, request CopyRequest) error {
	routes := []Route{{Initiator: request.Plan.Initiator, CommandResourceID: request.Plan.CommandResourceID, Status: store.StorageStatusHealthy}}
	routes = append(routes, request.Plan.Fallback...)
	attempts, err := w.Store.ListTransferAttempts(ctx, job.ID)
	if err != nil {
		return err
	}
	for index := range attempts {
		if attempts[index].State != store.TransferTransferring {
			continue
		}
		now := w.now()
		attempts[index].State = store.TransferFailed
		attempts[index].ErrorCode = "worker_interrupted"
		attempts[index].LastError = "control-plane worker stopped before the attempt reached a durable terminal state"
		attempts[index].FinishedAt = &now
		if err := w.Store.SaveTransferAttempt(ctx, &attempts[index]); err != nil {
			return err
		}
	}
	number := len(attempts)
	var lastErr error
	for _, route := range routes {
		number++
		request.Route = route
		attempt := &store.TransferAttempt{ID: fmt.Sprintf("attempt_%s_%d", job.ID, number), TransferID: job.ID, Number: number, Initiator: route.Initiator, State: store.TransferTransferring, StartedAt: w.now()}
		if err := w.Store.SaveTransferAttempt(ctx, attempt); err != nil {
			return err
		}
		job.Initiator, job.CommandResourceID = route.Initiator, route.CommandResourceID
		copyErr := w.withHeartbeat(ctx, job, store.TransferTransferring, func(operationContext context.Context) error {
			return w.Transport.Copy(operationContext, request, func(progress Progress) error {
				// A fallback rsync can report progress relative to the bytes remaining
				// in the shared staging tree. Clamp it to the durable high-water mark
				// so the public ledger stays monotonic across initiator changes.
				if progress.BytesDone < job.BytesDone {
					progress.BytesDone = job.BytesDone
				}
				if progress.FilesDone < job.FilesDone {
					progress.FilesDone = job.FilesDone
				}
				// Directory plans can know the byte size before the exact entry count.
				// Discover totals monotonically during copy; verification replaces
				// them with the exact destination inventory before promotion.
				if progress.BytesDone > job.TotalBytes {
					job.TotalBytes = progress.BytesDone
				}
				if progress.FilesDone > job.FileCount {
					job.FileCount = progress.FilesDone
				}
				job.BytesDone, job.FilesDone = progress.BytesDone, progress.FilesDone
				now := w.now()
				job.HeartbeatAt = &now
				updated, err := w.Store.UpdateTransferJobIfState(ctx, job, store.TransferTransferring)
				if err != nil {
					return err
				}
				if !updated {
					return fmt.Errorf("transfer state changed while copying")
				}
				return nil
			})
		})
		now := w.now()
		attempt.FinishedAt = &now
		if copyErr == nil {
			attempt.State = store.TransferCompleted
			if err := w.Store.SaveTransferAttempt(ctx, attempt); err != nil {
				return err
			}
			return nil
		}
		attempt.State = store.TransferFailed
		attempt.LastError = copyErr.Error()
		var operation *OperationError
		if errors.As(copyErr, &operation) {
			attempt.ErrorCode = operation.Code
		}
		if err := w.Store.SaveTransferAttempt(ctx, attempt); err != nil {
			return err
		}
		lastErr = copyErr
		if operation != nil && !operation.Retryable {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("transfer plan has no executable route")
	}
	return w.handleOperationError(ctx, job, store.TransferTransferring, "copy_failed", lastErr)
}

func (w *Worker) withHeartbeat(ctx context.Context, job *store.TransferJob, state string, operation func(context.Context) error) error {
	interval := w.HeartbeatInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	operationContext, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := w.touchHeartbeat(operationContext, job, state); err != nil {
		return err
	}
	heartbeatDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-operationContext.Done():
				heartbeatDone <- nil
				return
			case <-ticker.C:
				if err := w.touchHeartbeat(operationContext, job, state); err != nil {
					heartbeatDone <- err
					cancel()
					return
				}
			}
		}
	}()
	operationErr := operation(operationContext)
	cancel()
	heartbeatErr := <-heartbeatDone
	if operationErr != nil {
		return operationErr
	}
	if heartbeatErr != nil {
		return heartbeatErr
	}
	return nil
}

func (w *Worker) touchHeartbeat(ctx context.Context, job *store.TransferJob, state string) error {
	now := w.now()
	updated, err := w.Store.TouchTransferJobHeartbeat(ctx, job.ID, state, now)
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("transfer %s lost its %s worker lease", job.ID, state)
	}
	job.HeartbeatAt = &now
	return nil
}

func (w *Worker) transition(ctx context.Context, job *store.TransferJob, expected, next string) error {
	job.State, job.Stage = next, next
	now := w.now()
	job.UpdatedAt, job.HeartbeatAt = now, &now
	updated, err := w.Store.UpdateTransferJobIfState(ctx, job, expected)
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("transfer %s state changed before %s", job.ID, next)
	}
	return nil
}

func (w *Worker) handleOperationError(ctx context.Context, job *store.TransferJob, expected, fallbackCode string, err error) error {
	var operation *OperationError
	if errors.As(err, &operation) {
		if operation.Conflict {
			return w.block(ctx, job, expected, operation.Code, err)
		}
		return w.fail(ctx, job, expected, operation.Code, operation.Retryable, err)
	}
	return w.fail(ctx, job, expected, fallbackCode, true, err)
}

func (w *Worker) fail(ctx context.Context, job *store.TransferJob, expected, code string, retryable bool, cause error) error {
	now := w.now()
	job.State, job.Stage = store.TransferFailed, expected
	job.ErrorCode, job.LastError, job.Retryable, job.FinishedAt = code, cause.Error(), retryable, &now
	updated, err := w.Store.UpdateTransferJobIfState(ctx, job, expected)
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("transfer %s failure raced with another state update: %w", job.ID, cause)
	}
	return cause
}

func (w *Worker) block(ctx context.Context, job *store.TransferJob, expected, code string, cause error) error {
	now := w.now()
	job.State, job.Stage = store.TransferBlocked, expected
	job.ErrorCode, job.LastError, job.Retryable, job.FinishedAt = code, cause.Error(), false, &now
	updated, err := w.Store.UpdateTransferJobIfState(ctx, job, expected)
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("transfer %s blocker raced with another state update: %w", job.ID, cause)
	}
	return cause
}

func (w *Worker) finishCancelled(ctx context.Context, job *store.TransferJob) error {
	now := w.now()
	job.State, job.Stage = store.TransferCancelled, store.TransferCancelled
	job.FinishedAt, job.Retryable = &now, false
	updated, err := w.Store.UpdateTransferJobIfState(ctx, job, store.TransferCancelling)
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("transfer %s cancellation state changed", job.ID)
	}
	return nil
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}
