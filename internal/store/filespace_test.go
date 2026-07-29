package store

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"
)

func seedFileSpaceResources(t *testing.T, s *SQLite) {
	t.Helper()
	ctx := context.Background()
	for _, resource := range []Resource{
		{ID: "fs_nas", Name: "fs-nas", Type: ResourceTypeSSH, Host: "nas", RootDir: "/vol/data"},
		{ID: "fs_gpu", Name: "fs-gpu", Type: ResourceTypeSSH, Host: "gpu", RootDir: "/scratch"},
	} {
		if err := s.CreateResource(ctx, &resource); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveStorageTarget(ctx, &StorageTarget{ID: "fs_storage", Name: "fs-storage", ResourceID: "fs_nas", RootPath: "/vol/data/aexp"}); err != nil {
		t.Fatal(err)
	}
}

func TestLogicalRootAndPlacementObservationCAS(t *testing.T) {
	s := newTestStore(t)
	seedFileSpaceResources(t, s)
	ctx := context.Background()
	root := &LogicalRoot{ID: "root_data", Workspace: "project", Prefix: "data", StorageTargetID: "fs_storage", PhysicalRoot: "projects/project/data"}
	if err := s.SaveLogicalRoot(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLogicalRoot(ctx, &LogicalRoot{ID: "root_overlap", Workspace: "project", Prefix: "data/raw", StorageTargetID: "fs_storage", PhysicalRoot: "raw"}); err == nil {
		t.Fatal("overlapping logical root must be rejected")
	}

	placement := &PathPlacement{
		ID: "placement_nas", LogicalURI: "aexp://project/data/raw", ResourceID: "fs_nas",
		StorageTargetID: "fs_storage", PhysicalPath: "/vol/data/aexp/projects/project/data/raw",
		Role: PlacementRoleAuthoritative, DesiredState: PlacementDesiredPresent,
	}
	if err := s.SavePathPlacement(ctx, placement); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePathPlacement(ctx, &PathPlacement{
		ID: "placement_other_authority", LogicalURI: placement.LogicalURI, ResourceID: "fs_gpu",
		PhysicalPath: "/scratch/raw", Role: PlacementRoleAuthoritative,
	}); err == nil {
		t.Fatal("second authoritative placement must be rejected")
	}
	if err := s.SavePathPlacement(ctx, &PathPlacement{
		ID: "placement_cache", LogicalURI: placement.LogicalURI, ResourceID: "fs_gpu",
		PhysicalPath: "/scratch/raw", Role: PlacementRoleCache,
	}); err != nil {
		t.Fatalf("cache placement: %v", err)
	}

	newer := time.Now().UTC().Add(time.Second)
	observed := newer.Add(-time.Millisecond)
	updated, err := s.UpdatePathPlacementObservation(ctx, placement.ID, PlacementObservation{
		State: PlacementObservedPresent, Revision: "sha256:new", ManifestSHA256: "sha256:new",
		BytesPresent: 42, Source: "remote_stat", ObservedAt: &observed, CheckedAt: newer,
	})
	if err != nil || !updated {
		t.Fatalf("new observation updated=%v err=%v", updated, err)
	}
	updated, err = s.UpdatePathPlacementObservation(ctx, placement.ID, PlacementObservation{
		State: PlacementObservedMissing, Source: "late_probe", CheckedAt: newer.Add(-time.Minute),
	})
	if err != nil || updated {
		t.Fatalf("stale observation updated=%v err=%v", updated, err)
	}
	got, err := s.GetPathPlacement(ctx, placement.ID)
	if err != nil || got == nil || got.ObservedState != PlacementObservedPresent || got.Revision != "sha256:new" || got.BytesPresent != 42 {
		t.Fatalf("placement after stale observation: %#v err=%v", got, err)
	}
	if err := s.DeleteLogicalRoot(ctx, root.ID); err == nil {
		t.Fatal("referenced logical root deletion must fail")
	}
}

func TestTransferPlanJobAtomicIdempotentAndClaimedOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	plan := &TransferPlan{
		PlanSHA256: "sha256:plan-one", Workspace: "project", SourceURI: "aexp://project/data/raw",
		DestinationURI: "resource://gpu/scratch/raw", SourceRevision: "sha256:source",
		PlanJSON: `{"initiator":"nas"}`, ExpiresAt: time.Now().Add(time.Hour),
	}
	job := &TransferJob{ID: "transfer_one", PlanSHA256: plan.PlanSHA256, TotalBytes: 42, FileCount: 2}
	createdJob, created, err := s.CreateTransferJobWithPlan(ctx, plan, job)
	if err != nil || !created || createdJob.ID != job.ID || createdJob.State != TransferQueued {
		t.Fatalf("create job=%#v created=%v err=%v", createdJob, created, err)
	}
	duplicate, created, err := s.CreateTransferJobWithPlan(ctx, plan, &TransferJob{ID: "transfer_duplicate"})
	if err != nil || created || duplicate.ID != job.ID {
		t.Fatalf("idempotent job=%#v created=%v err=%v", duplicate, created, err)
	}

	var wg sync.WaitGroup
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, claimed, claimErr := s.ClaimTransferJob(ctx, job.ID)
			results <- claimed
			errs <- claimErr
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	claims := 0
	for claimed := range results {
		if claimed {
			claims++
		}
	}
	for claimErr := range errs {
		if claimErr != nil {
			t.Fatal(claimErr)
		}
	}
	if claims != 1 {
		t.Fatalf("claims=%d, want 1", claims)
	}
	claimed, err := s.GetTransferJob(ctx, job.ID)
	if err != nil || claimed.State != TransferPlanning || claimed.Attempt != 1 {
		t.Fatalf("claimed job=%#v err=%v", claimed, err)
	}
}

func TestFileSpaceSchemaIntegrityAndForeignKeys(t *testing.T) {
	s := newTestStore(t)
	for _, table := range []string{"logical_roots", "path_placements", "transfer_plans", "transfer_jobs", "transfer_attempts"} {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table %s count=%d err=%v", table, count, err)
		}
	}
	var integrity string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%q err=%v", integrity, err)
	}
	rows, err := s.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var fkID int
		if err := rows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("foreign key violation table=%s row=%v parent=%s fk=%d", table, rowID, parent, fkID)
	}
}
