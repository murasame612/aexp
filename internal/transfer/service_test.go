package transfer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/store"
)

func TestServiceRecomputesExpectedPlanAndCreatesAtomically(t *testing.T) {
	planner, db, _ := newPlannerFixture(t)
	service := NewService(db, planner)
	service.NewID = func() string { return "transfer_service" }
	request := PlanRequest{Source: "aexp://project/data/raw", Destination: "resource://gpu/cache/raw"}
	plan, err := planner.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Create(context.Background(), request, "sha256:stale"); err == nil {
		t.Fatal("stale expected plan hash was accepted")
	} else {
		var mismatch *PlanHashMismatchError
		if !errors.As(err, &mismatch) || mismatch.Actual != plan.PlanSHA256 {
			t.Fatalf("unexpected mismatch error: %v", err)
		}
	}
	if jobs, err := db.ListTransferJobs(context.Background(), "", 10); err != nil || len(jobs) != 0 {
		t.Fatalf("mismatch created jobs=%#v err=%v", jobs, err)
	}
	job, created, err := service.Create(context.Background(), request, plan.PlanSHA256)
	if err != nil || !created || job.ID != "transfer_service" || job.State != "queued" {
		t.Fatalf("job=%#v created=%v err=%v", job, created, err)
	}
	duplicate, created, err := service.Create(context.Background(), request, plan.PlanSHA256)
	if err != nil || created || duplicate.ID != job.ID {
		t.Fatalf("duplicate=%#v created=%v err=%v", duplicate, created, err)
	}
	detail, err := service.Get(context.Background(), job.ID)
	if err != nil || detail == nil || detail.Plan == nil || detail.Plan.PlanSHA256 != plan.PlanSHA256 {
		t.Fatalf("detail=%#v err=%v", detail, err)
	}
}

func TestServiceDoesNotPersistBlockedPlan(t *testing.T) {
	planner, db, now := newPlannerFixture(t)
	planner.Now = func() time.Time { return now.Add(20 * time.Minute) }
	service := NewService(db, planner)
	request := PlanRequest{Source: "aexp://project/data/raw", Destination: "resource://gpu/cache/raw"}
	plan, err := planner.Build(context.Background(), request)
	if err != nil || len(plan.Blockers) == 0 {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if _, _, err := service.Create(context.Background(), request, plan.PlanSHA256); err == nil {
		t.Fatal("blocked plan was persisted")
	} else {
		var blocked *PlanBlockedError
		if !errors.As(err, &blocked) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if stored, err := db.GetTransferPlan(context.Background(), plan.PlanSHA256); err != nil || stored != nil {
		t.Fatalf("blocked plan was stored: %#v err=%v", stored, err)
	}
}

func TestServiceCreateCurrentDiscoversOnceAndQueuesCopy(t *testing.T) {
	planner, db, _ := newPlannerFixture(t)
	planner.Files.Remote = pathAwarePlannerRemote{
		sourceEntry: filespace.RemoteEntry{Exists: true, Type: "directory"},
		sourceHash:  filespace.HashResult{Revision: "sha256:current", ManifestSHA256: "sha256:current", TotalBytes: 512, FileCount: 2},
	}
	service := NewService(db, planner)
	service.NewID = func() string { return "transfer_current" }
	job, created, plan, err := service.CreateCurrent(context.Background(), PlanRequest{Source: "storage://nas-store/datasets/raw", Destination: "resource://gpu/cache/raw"})
	if err != nil || !created || job == nil || job.ID != "transfer_current" || job.State != store.TransferQueued {
		t.Fatalf("job=%#v created=%v plan=%#v err=%v", job, created, plan, err)
	}
	if plan.Source.Revision != "sha256:current" || job.TotalBytes != 512 || job.FileCount != 2 {
		t.Fatalf("job=%#v plan=%#v", job, plan)
	}
	duplicate, created, _, err := service.CreateCurrent(context.Background(), PlanRequest{Source: "storage://nas-store/datasets/raw", Destination: "resource://gpu/cache/raw"})
	if err != nil || created || duplicate == nil || duplicate.ID != job.ID {
		t.Fatalf("duplicate=%#v created=%v err=%v", duplicate, created, err)
	}
}

func TestServiceCreateCurrentDoesNotPersistBlockedCopy(t *testing.T) {
	planner, db, _ := newPlannerFixture(t)
	planner.Files.Remote = pathAwarePlannerRemote{sourceEntry: filespace.RemoteEntry{Exists: false}}
	service := NewService(db, planner)
	_, _, plan, err := service.CreateCurrent(context.Background(), PlanRequest{Source: "storage://nas-store/datasets/raw", Destination: "resource://gpu/cache/raw"})
	var blocked *PlanBlockedError
	if !errors.As(err, &blocked) || len(plan.Blockers) == 0 {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if jobs, listErr := db.ListTransferJobs(context.Background(), "", 10); listErr != nil || len(jobs) != 0 {
		t.Fatalf("jobs=%#v err=%v", jobs, listErr)
	}
}

func TestServiceCancelQueuedJob(t *testing.T) {
	planner, db, _ := newPlannerFixture(t)
	service := NewService(db, planner)
	service.NewID = func() string { return "transfer_cancel" }
	request := PlanRequest{Source: "aexp://project/data/raw", Destination: "resource://gpu/cache/raw"}
	plan, _ := planner.Build(context.Background(), request)
	job, _, err := service.Create(context.Background(), request, plan.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := service.Cancel(context.Background(), job.ID)
	if err != nil || cancelled.State != "cancelled" || cancelled.FinishedAt == nil {
		t.Fatalf("cancelled=%#v err=%v", cancelled, err)
	}
}

func TestServiceRequeuesCompletedJobWhenVerifiedDestinationDisappears(t *testing.T) {
	planner, db, now := newPlannerFixture(t)
	service := NewService(db, planner)
	service.NewID = func() string { return "transfer_rematerialize" }
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
	worker := NewWorker(db, transport)
	worker.Now = func() time.Time { return now }
	if err := worker.Execute(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	missingAt := now.Add(2 * time.Minute)
	updated, err := db.UpdatePathPlacementObservation(context.Background(), plan.Destination.PlacementID, store.PlacementObservation{
		State: store.PlacementObservedMissing, Source: "test_remote_stat", CheckedAt: missingAt,
	})
	if err != nil || !updated {
		t.Fatalf("mark destination missing updated=%v err=%v", updated, err)
	}

	repairPlan, err := planner.Build(context.Background(), request)
	if err != nil || repairPlan.PlanSHA256 != plan.PlanSHA256 || repairPlan.AlreadySatisfied {
		t.Fatalf("repair plan=%#v original=%#v err=%v", repairPlan, plan, err)
	}
	reopened, created, err := service.Create(context.Background(), request, repairPlan.PlanSHA256)
	if err != nil || created || reopened.ID != job.ID || reopened.State != store.TransferQueued || reopened.BytesDone != 0 {
		t.Fatalf("reopened=%#v created=%v err=%v", reopened, created, err)
	}
	worker.Now = func() time.Time { return now.Add(3 * time.Minute) }
	if err := worker.Execute(context.Background(), reopened.ID); err != nil {
		t.Fatal(err)
	}
	attempts, err := db.ListTransferAttempts(context.Background(), reopened.ID)
	if err != nil || len(attempts) != 2 || attempts[0].State != store.TransferCompleted || attempts[1].State != store.TransferCompleted {
		t.Fatalf("attempts=%#v err=%v", attempts, err)
	}
}
