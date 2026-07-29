package transfer

import (
	"context"
	"errors"
	"testing"

	"github.com/ziwu/aexp/internal/store"
)

type fakeTransport struct {
	copyRoutes        []string
	failRoute         string
	failAfterProgress bool
	verify            VerifyResult
	verifyErr         error
	promoteErr        error
	promoteHook       func() error
	copyCalls         int
	verifyCalls       int
	promoteCalls      int
}

func (f *fakeTransport) Copy(_ context.Context, request CopyRequest, progress func(Progress) error) error {
	f.copyCalls++
	f.copyRoutes = append(f.copyRoutes, request.Route.Initiator)
	if request.Route.Initiator == f.failRoute {
		if f.failAfterProgress {
			if err := progress(Progress{BytesDone: 400, FilesDone: 0}); err != nil {
				return err
			}
		}
		return &OperationError{Code: "route_timeout", Retryable: true, Err: errors.New("route timeout")}
	}
	if err := progress(Progress{BytesDone: 400, FilesDone: 0}); err != nil {
		return err
	}
	return progress(Progress{BytesDone: 1234, FilesDone: 1})
}

func (f *fakeTransport) Verify(_ context.Context, _ CopyRequest) (VerifyResult, error) {
	f.verifyCalls++
	return f.verify, f.verifyErr
}

func (f *fakeTransport) Promote(_ context.Context, _ CopyRequest) error {
	f.promoteCalls++
	if f.promoteHook != nil {
		if err := f.promoteHook(); err != nil {
			return err
		}
	}
	return f.promoteErr
}

func createWorkerJob(t *testing.T) (*store.SQLite, *Worker, *fakeTransport, string) {
	t.Helper()
	planner, db, _ := newPlannerFixture(t)
	service := NewService(db, planner)
	service.NewID = func() string { return "transfer_worker" }
	request := PlanRequest{Source: "aexp://project/data/raw", Destination: "resource://gpu/cache/raw"}
	plan, err := planner.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := service.Create(context.Background(), request, plan.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	transport := &fakeTransport{verify: VerifyResult{Revision: "sha256:source", TotalBytes: 1234, FileCount: 1}}
	return db, NewWorker(db, transport), transport, job.ID
}

func TestWorkerFallbackProgressVerifyAndPromote(t *testing.T) {
	db, worker, transport, id := createWorkerJob(t)
	transport.failRoute = "nas"
	if err := worker.Execute(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	job, err := db.GetTransferJob(context.Background(), id)
	if err != nil || job.State != store.TransferCompleted || job.BytesDone != 1234 || job.FilesDone != 1 || job.Initiator != "compute" {
		t.Fatalf("job=%#v err=%v", job, err)
	}
	if transport.copyCalls != 2 || transport.verifyCalls != 1 || transport.promoteCalls != 1 {
		t.Fatalf("transport=%#v", transport)
	}
	placements, err := db.ListPathPlacements(context.Background(), "aexp://project/data/raw")
	if err != nil || len(placements) != 2 || placements[1].Role != store.PlacementRoleCache || placements[1].ObservedState != store.PlacementObservedPresent || placements[1].Revision != "sha256:source" {
		t.Fatalf("cache placements=%#v err=%v", placements, err)
	}
	attempts, err := db.ListTransferAttempts(context.Background(), id)
	if err != nil || len(attempts) != 2 || attempts[0].State != store.TransferFailed || attempts[1].State != store.TransferCompleted {
		t.Fatalf("attempts=%#v err=%v", attempts, err)
	}
}

func TestWorkerFallbackKeepsProgressMonotonicAfterPartialFailure(t *testing.T) {
	db, worker, transport, id := createWorkerJob(t)
	transport.failRoute, transport.failAfterProgress = "nas", true
	if err := worker.Execute(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	job, _ := db.GetTransferJob(context.Background(), id)
	if job.State != store.TransferCompleted || job.BytesDone != 1234 || transport.copyCalls != 2 {
		t.Fatalf("job=%#v transport=%#v", job, transport)
	}
}

func TestWorkerRevisionMismatchFailsBeforePromotion(t *testing.T) {
	db, worker, transport, id := createWorkerJob(t)
	transport.verify.Revision = "sha256:wrong"
	if err := worker.Execute(context.Background(), id); err == nil {
		t.Fatal("revision mismatch succeeded")
	}
	job, _ := db.GetTransferJob(context.Background(), id)
	if job.State != store.TransferFailed || job.Stage != store.TransferVerifying || job.ErrorCode != "revision_mismatch" || transport.promoteCalls != 0 {
		t.Fatalf("job=%#v transport=%#v", job, transport)
	}
}

func TestWorkerPromotionConflictIsBlocked(t *testing.T) {
	db, worker, transport, id := createWorkerJob(t)
	transport.promoteErr = &OperationError{Code: "destination_conflict", Conflict: true, Err: errors.New("final contains another revision")}
	if err := worker.Execute(context.Background(), id); err == nil {
		t.Fatal("promotion conflict succeeded")
	}
	job, _ := db.GetTransferJob(context.Background(), id)
	if job.State != store.TransferBlocked || job.Stage != store.TransferPromoting || job.Retryable {
		t.Fatalf("job=%#v", job)
	}
}

func TestWorkerReportsCompletedWhenAtomicPromotionWinsCancellationRace(t *testing.T) {
	db, worker, transport, id := createWorkerJob(t)
	transport.promoteHook = func() error {
		_, err := (&Service{Store: db}).Cancel(context.Background(), id)
		return err
	}
	if err := worker.Execute(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	job, _ := db.GetTransferJob(context.Background(), id)
	if job.State != store.TransferCompleted || job.FinishedAt == nil {
		t.Fatalf("job=%#v", job)
	}
}

func TestWorkerResumeFromVerifyingDoesNotCopyAgain(t *testing.T) {
	db, worker, transport, id := createWorkerJob(t)
	job, claimed, err := db.ClaimTransferJob(context.Background(), id)
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	job.State, job.Stage = store.TransferTransferring, store.TransferTransferring
	if updated, err := db.UpdateTransferJobIfState(context.Background(), job, store.TransferPlanning); err != nil || !updated {
		t.Fatalf("to transferring=%v err=%v", updated, err)
	}
	job.State, job.Stage = store.TransferVerifying, store.TransferVerifying
	if updated, err := db.UpdateTransferJobIfState(context.Background(), job, store.TransferTransferring); err != nil || !updated {
		t.Fatalf("to verifying=%v err=%v", updated, err)
	}
	if err := worker.Resume(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	job, _ = db.GetTransferJob(context.Background(), id)
	if job.State != store.TransferCompleted || transport.copyCalls != 0 || transport.verifyCalls != 1 || transport.promoteCalls != 1 {
		t.Fatalf("job=%#v transport=%#v", job, transport)
	}
}

func TestWorkerRecordsVerifiedLogicalDestinationPlacement(t *testing.T) {
	planner, db, _ := newPlannerFixture(t)
	service := NewService(db, planner)
	service.NewID = func() string { return "transfer_publish" }
	request := PlanRequest{Source: "resource://gpu/outputs/result", Destination: "aexp://project/data/published", SourceRevision: "sha256:published"}
	plan, err := planner.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) != 0 {
		t.Fatalf("blockers=%#v", plan.Blockers)
	}
	job, _, err := service.Create(context.Background(), request, plan.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	transport := &fakeTransport{verify: VerifyResult{Revision: "sha256:published", TotalBytes: 1234, FileCount: 2}}
	if err := NewWorker(db, transport).Execute(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	placements, err := db.ListPathPlacements(context.Background(), "aexp://project/data/published")
	if err != nil || len(placements) != 1 {
		t.Fatalf("placements=%#v err=%v", placements, err)
	}
	placement := placements[0]
	if placement.ObservedState != store.PlacementObservedPresent || placement.Revision != "sha256:published" || placement.BytesPresent != 1234 || placement.ObservationSource != "transfer_verify" {
		t.Fatalf("placement=%#v", placement)
	}
}
