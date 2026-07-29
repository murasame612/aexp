package transfer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ziwu/aexp/internal/store"
)

type PlanHashMismatchError struct {
	Expected string
	Actual   string
}

func (e *PlanHashMismatchError) Error() string {
	return fmt.Sprintf("transfer plan changed: expected %s, actual %s", e.Expected, e.Actual)
}

type PlanBlockedError struct {
	PlanSHA256 string
	Blockers   []Blocker
}

func (e *PlanBlockedError) Error() string {
	return fmt.Sprintf("transfer plan %s has %d blocker(s)", e.PlanSHA256, len(e.Blockers))
}

type JobDetail struct {
	Job      store.TransferJob       `json:"job"`
	Plan     *store.TransferPlan     `json:"plan,omitempty"`
	Attempts []store.TransferAttempt `json:"attempts"`
}

type Service struct {
	Store   store.Store
	Planner *Planner
	Now     func() time.Time
	NewID   func() string
}

func NewService(db store.Store, planner *Planner) *Service {
	return &Service{
		Store: db, Planner: planner, Now: time.Now,
		NewID: func() string { return "transfer_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12] },
	}
}

// Create recomputes the side-effect-free plan and compares its hash before
// atomically persisting the plan and queued job. A dry-run plan is never stored.
func (s *Service) Create(ctx context.Context, request PlanRequest, expectedPlanSHA256 string) (*store.TransferJob, bool, error) {
	if expectedPlanSHA256 == "" {
		return nil, false, fmt.Errorf("expected_plan_sha256 is required")
	}
	plan, err := s.Planner.Build(ctx, request)
	if err != nil {
		return nil, false, err
	}
	if plan.PlanSHA256 != expectedPlanSHA256 {
		return nil, false, &PlanHashMismatchError{Expected: expectedPlanSHA256, Actual: plan.PlanSHA256}
	}
	if len(plan.Blockers) > 0 {
		return nil, false, &PlanBlockedError{PlanSHA256: plan.PlanSHA256, Blockers: plan.Blockers}
	}
	return s.persistPlan(ctx, plan)
}

// CreateCurrent is the high-level Agent copy facade. It discovers and pins the
// current source revision once, then atomically stores that accepted plan and a
// durable asynchronous job. The worker still verifies the staged payload and
// destination promotion remains no-overwrite.
func (s *Service) CreateCurrent(ctx context.Context, request PlanRequest) (*store.TransferJob, bool, Plan, error) {
	plan, err := s.Planner.Build(ctx, request)
	if err != nil {
		return nil, false, Plan{}, err
	}
	if len(plan.Blockers) > 0 {
		return nil, false, plan, &PlanBlockedError{PlanSHA256: plan.PlanSHA256, Blockers: plan.Blockers}
	}
	job, created, err := s.persistPlan(ctx, plan)
	return job, created, plan, err
}

func (s *Service) persistPlan(ctx context.Context, plan Plan) (*store.TransferJob, bool, error) {
	persistent, err := s.Planner.PersistentPlan(plan)
	if err != nil {
		return nil, false, err
	}
	now := s.now()
	job := &store.TransferJob{
		ID: s.NewID(), PlanSHA256: plan.PlanSHA256, State: store.TransferQueued, Stage: store.TransferQueued,
		TotalBytes: plan.TotalBytes, FileCount: plan.FileCount, Retryable: true, CreatedAt: now, UpdatedAt: now,
	}
	if plan.AlreadySatisfied {
		job.State, job.Stage = store.TransferCompleted, store.TransferCompleted
		job.BytesDone, job.FilesDone = job.TotalBytes, job.FileCount
		job.Retryable = false
		job.FinishedAt = &now
	}
	placements := make([]*store.PathPlacement, 0, 1)
	if plan.Destination.LogicalURI != "" && plan.Destination.PlacementID != "" {
		role := plan.Destination.Role
		if role == "" {
			role = store.PlacementRoleAuthoritative
		}
		placements = append(placements, &store.PathPlacement{
			ID: plan.Destination.PlacementID, LogicalURI: plan.Destination.LogicalURI,
			ResourceID: plan.Destination.ResourceID, StorageTargetID: plan.Destination.StorageTargetID,
			PhysicalPath: plan.Destination.PhysicalPath, Role: role, DesiredState: store.PlacementDesiredPresent,
		})
		if plan.AlreadySatisfied {
			placements[0].ObservedState = store.PlacementObservedPresent
			placements[0].Revision = plan.Source.Revision
			placements[0].BytesPresent = plan.Destination.Bytes
			placements[0].ObservationSource = "transfer_plan_verified"
			placements[0].CheckedAt = plan.Destination.CheckedAt
			placements[0].ObservedAt = plan.Destination.CheckedAt
		}
	}
	stored, created, err := s.Store.CreateTransferJobWithPlan(ctx, &persistent, job, placements...)
	if err != nil || created || stored == nil || stored.State != store.TransferCompleted || plan.AlreadySatisfied {
		return stored, created, err
	}
	// A completed ledger is reusable only while the destination still contains
	// the verified revision. If the current side-effect-free probe says missing,
	// reopen the same durable job so a Run cannot launch from stale control-plane
	// truth. Attempts remain append-only and make the rematerialization visible.
	reopened, requeued, err := s.Store.RequeueCompletedTransferJob(ctx, stored.ID, stored.PlanSHA256, plan.TotalBytes, plan.FileCount)
	if err != nil {
		return nil, false, err
	}
	if requeued {
		return reopened, false, nil
	}
	current, err := s.Store.GetTransferJob(ctx, stored.ID)
	return current, false, err
}

func (s *Service) Get(ctx context.Context, id string) (*JobDetail, error) {
	job, err := s.Store.GetTransferJob(ctx, id)
	if err != nil || job == nil {
		return nil, err
	}
	plan, err := s.Store.GetTransferPlan(ctx, job.PlanSHA256)
	if err != nil {
		return nil, err
	}
	attempts, err := s.Store.ListTransferAttempts(ctx, id)
	if err != nil {
		return nil, err
	}
	return &JobDetail{Job: *job, Plan: plan, Attempts: attempts}, nil
}

func (s *Service) Retry(ctx context.Context, id string) (*store.TransferJob, error) {
	job, err := s.Store.GetTransferJob(ctx, id)
	if err != nil || job == nil {
		return job, err
	}
	if !job.Retryable || (job.State != store.TransferFailed && job.State != store.TransferBlocked) {
		return nil, fmt.Errorf("transfer %s is not retryable from state %s", id, job.State)
	}
	expected := job.State
	job.State, job.Stage = store.TransferQueued, store.TransferQueued
	job.ErrorCode, job.LastError = "", ""
	job.FinishedAt = nil
	updated, err := s.Store.UpdateTransferJobIfState(ctx, job, expected)
	if err != nil {
		return nil, err
	}
	if !updated {
		return nil, fmt.Errorf("transfer %s changed before retry", id)
	}
	return s.Store.GetTransferJob(ctx, id)
}

func (s *Service) Cancel(ctx context.Context, id string) (*store.TransferJob, error) {
	job, err := s.Store.GetTransferJob(ctx, id)
	if err != nil || job == nil {
		return job, err
	}
	expected := job.State
	now := s.now()
	if job.State == store.TransferQueued {
		job.State, job.Stage = store.TransferCancelled, store.TransferCancelled
		job.FinishedAt = &now
		job.Retryable = false
	} else if oneOf(job.State, store.TransferPlanning, store.TransferTransferring, store.TransferVerifying, store.TransferPromoting) {
		job.State, job.Stage = store.TransferCancelling, store.TransferCancelling
	} else {
		return nil, fmt.Errorf("transfer %s cannot be cancelled from state %s", id, job.State)
	}
	updated, err := s.Store.UpdateTransferJobIfState(ctx, job, expected)
	if err != nil {
		return nil, err
	}
	if !updated {
		return nil, fmt.Errorf("transfer %s changed before cancellation", id)
	}
	return s.Store.GetTransferJob(ctx, id)
}

func DecodePlan(record *store.TransferPlan) (Plan, error) {
	if record == nil {
		return Plan{}, fmt.Errorf("transfer plan is missing")
	}
	var plan Plan
	if err := json.Unmarshal([]byte(record.PlanJSON), &plan); err != nil {
		return Plan{}, fmt.Errorf("decode transfer plan: %w", err)
	}
	if plan.PlanSHA256 != record.PlanSHA256 {
		return Plan{}, fmt.Errorf("transfer plan hash identity mismatch")
	}
	return plan, nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
