package dataset

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/store"
	"github.com/ziwu/aexp/internal/transfer"
)

type datasetRemoteFS struct {
	entries map[string]filespace.RemoteEntry
	hashes  map[string]filespace.HashResult
}

func (f *datasetRemoteFS) Stat(_ context.Context, location filespace.RemoteLocation) (filespace.RemoteEntry, error) {
	if entry, ok := f.entries[location.PhysicalPath]; ok {
		return entry, nil
	}
	return filespace.RemoteEntry{Path: location.PhysicalPath, Exists: false}, nil
}

func (f *datasetRemoteFS) List(context.Context, filespace.RemoteLocation, string, int) (filespace.ListResult, error) {
	return filespace.ListResult{}, errors.New("not implemented")
}

func (f *datasetRemoteFS) Hash(_ context.Context, location filespace.RemoteLocation) (filespace.HashResult, error) {
	if result, ok := f.hashes[location.PhysicalPath]; ok {
		return result, nil
	}
	return filespace.HashResult{}, errors.New("hash not found")
}

type datasetTransport struct {
	verify transfer.VerifyResult
}

func (f *datasetTransport) Copy(_ context.Context, request transfer.CopyRequest, report func(transfer.Progress) error) error {
	return report(transfer.Progress{BytesDone: f.verify.TotalBytes, FilesDone: f.verify.FileCount})
}

func (f *datasetTransport) Verify(context.Context, transfer.CopyRequest) (transfer.VerifyResult, error) {
	return f.verify, nil
}

func (f *datasetTransport) Promote(context.Context, transfer.CopyRequest) error { return nil }

func datasetFixture(t *testing.T) (*store.SQLite, *Service, *transfer.Worker, *datasetRemoteFS) {
	t.Helper()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	for _, resource := range []store.Resource{
		{ID: "nas_id", Name: "nas", Type: store.ResourceTypeSSH, Host: "nas", RootDir: "/vol/data"},
		{ID: "gpu_id", Name: "gpu", Type: store.ResourceTypeSSH, Host: "gpu", RootDir: "/scratch"},
	} {
		if err := db.CreateResource(ctx, &resource); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	target := &store.StorageTarget{
		ID: "storage_id", Name: "nas-store", ResourceID: "nas_id", RootPath: "/vol/data/aexp",
		Health: &store.StorageTargetHealth{CheckedAt: now, DataPlane: []store.StorageDataPlaneHealth{{
			ResourceID: "gpu_id", CheckedAt: now,
			NASInitiated: store.StorageConnectionHealth{Status: store.StorageStatusHealthy}, ComputeInitiated: store.StorageConnectionHealth{Status: store.StorageStatusHealthy},
		}}},
	}
	if err := db.SaveStorageTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveLogicalRoot(ctx, &store.LogicalRoot{ID: "datasets_root", Workspace: "project", Prefix: "datasets", StorageTargetID: target.ID, PhysicalRoot: "projects/project/datasets"}); err != nil {
		t.Fatal(err)
	}
	remote := &datasetRemoteFS{entries: map[string]filespace.RemoteEntry{}, hashes: map[string]filespace.HashResult{}}
	fileService := filespace.NewService(db, remote)
	planner := transfer.NewPlanner(db, fileService)
	planner.RouteTTL = time.Hour
	transfers := transfer.NewService(db, planner)
	fakeTransport := &datasetTransport{}
	return db, NewService(db, planner, transfers, remote), transfer.NewWorker(db, fakeTransport), remote
}

func TestIngestPublishesBeforeImmutableRegistryIsWritten(t *testing.T) {
	db, service, worker, _ := datasetFixture(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "samples.csv"), []byte("a,b\n1,2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	planned, err := service.PlanIngest(context.Background(), "facade@v3", source, "aexp://project/datasets/facade/v3")
	if err != nil || len(planned.Transfer.Blockers) != 0 {
		t.Fatalf("plan=%#v err=%v", planned, err)
	}
	job, _, err := service.StartIngest(context.Background(), "facade@v3", source, "aexp://project/datasets/facade/v3", planned.Transfer.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.FinalizeIngest(context.Background(), "facade@v3", job.ID, "directory"); err == nil {
		t.Fatal("queued transfer was registered as an immutable dataset")
	}
	if dataset, _ := db.GetDatasetVersionByRef(context.Background(), "facade", "v3"); dataset != nil {
		t.Fatalf("registry was written before publish: %#v", dataset)
	}
	fake := worker.Transport.(*datasetTransport)
	fake.verify = transfer.VerifyResult{Revision: planned.Source.Revision, TotalBytes: planned.Source.TotalBytes, FileCount: planned.Source.FileCount}
	if err := worker.Execute(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	dataset, created, err := service.FinalizeIngest(context.Background(), "facade@v3", job.ID, "directory")
	if err != nil || !created || dataset.Revision != planned.Source.Revision || dataset.LogicalURI != "aexp://project/datasets/facade/v3" {
		t.Fatalf("dataset=%#v created=%v err=%v", dataset, created, err)
	}
	again, created, err := service.FinalizeIngest(context.Background(), "facade@v3", job.ID, "directory")
	if err != nil || created || again.ID != dataset.ID {
		t.Fatalf("idempotent finalize dataset=%#v created=%v err=%v", again, created, err)
	}
	conflicting := *dataset
	conflicting.ID = "dataset_conflict"
	conflicting.Revision = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, _, err := db.CreateDatasetVersionImmutable(context.Background(), &conflicting); err == nil {
		t.Fatal("immutable dataset tag accepted a different revision")
	} else {
		var conflict *store.DatasetVersionConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("unexpected conflict error: %v", err)
		}
	}
}

func TestMaterializeUsesTransferAndOnlyBecomesReadyAfterVerification(t *testing.T) {
	db, service, worker, _ := datasetFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	dataset := &store.DatasetVersion{
		ID: "dataset_v3", DatasetID: "facade", Version: "v3", StorageTargetID: "storage_id",
		StoragePath: "projects/project/datasets/facade/v3", LogicalURI: "aexp://project/datasets/facade/v3",
		Revision:       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ManifestSHA256: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: store.DatasetStateVerified,
	}
	if _, _, err := db.CreateDatasetVersionImmutable(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	if err := db.SavePathPlacement(ctx, &store.PathPlacement{
		ID: "dataset_source", LogicalURI: dataset.LogicalURI, ResourceID: "nas_id", StorageTargetID: "storage_id",
		PhysicalPath: "/vol/data/aexp/projects/project/datasets/facade/v3", Role: store.PlacementRoleAuthoritative,
		DesiredState: store.PlacementDesiredPresent, ObservedState: store.PlacementObservedPresent,
		Revision: dataset.Revision, ManifestSHA256: dataset.Revision, CheckedAt: &now, ObservedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Materialize(ctx, "facade@v3", "gpu_id", "cache/facade/v3")
	if err != nil || result.Transfer == nil || result.Materialization.State != store.MaterializationTransferring || result.Materialization.TransferID == "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	worker.Transport.(*datasetTransport).verify = transfer.VerifyResult{Revision: dataset.Revision, TotalBytes: 42, FileCount: 3}
	if err := worker.Execute(ctx, result.Transfer.ID); err != nil {
		t.Fatal(err)
	}
	ready, err := service.ReconcileMaterialization(ctx, dataset.ID, "gpu_id")
	if err != nil || ready.State != store.MaterializationReady || ready.BytesPresent != 42 || ready.VerifiedSHA256 != dataset.Revision || ready.VerifiedAt == nil {
		t.Fatalf("ready=%#v err=%v", ready, err)
	}
	placements, _ := db.ListPathPlacements(ctx, dataset.LogicalURI)
	if len(placements) != 2 || placements[1].Role != store.PlacementRoleCache || placements[1].Revision != dataset.Revision {
		t.Fatalf("placements=%#v", placements)
	}
}

func TestMaterializeReusesMatchingCacheAndRejectsDifferentRevision(t *testing.T) {
	db, service, _, remote := datasetFixture(t)
	ctx := context.Background()
	dataset := &store.DatasetVersion{
		ID: "dataset_v3", DatasetID: "facade", Version: "v3", StorageTargetID: "storage_id", StoragePath: "legacy/facade/v3",
		LogicalURI: "aexp://project/datasets/facade/v3", Revision: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if _, _, err := db.CreateDatasetVersionImmutable(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	physical := "/scratch/cache/facade/v3"
	remote.entries[physical] = filespace.RemoteEntry{Path: physical, Exists: true, Type: "directory"}
	remote.hashes[physical] = filespace.HashResult{Revision: dataset.Revision, ManifestSHA256: dataset.Revision, TotalBytes: 10, FileCount: 1}
	result, err := service.Materialize(ctx, "facade@v3", "gpu_id", "cache/facade/v3")
	if err != nil || !result.Reused || result.Transfer != nil || result.Materialization.State != store.MaterializationReady {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	remote.hashes[physical] = filespace.HashResult{Revision: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	_, err = service.Materialize(ctx, "facade@v3", "gpu_id", "cache/facade/v3")
	var conflict *DestinationConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected destination conflict, got %v", err)
	}
}

func TestVerifyExistingDatasetCacheRecordsReadyPlacementWithoutTransfer(t *testing.T) {
	db, service, _, remote := datasetFixture(t)
	ctx := context.Background()
	dataset := &store.DatasetVersion{
		ID: "dataset_v3", DatasetID: "facade", Version: "v3", StorageTargetID: "storage_id",
		StoragePath: "projects/project/datasets/facade/v3", LogicalURI: "aexp://project/datasets/facade/v3",
		Revision: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: store.DatasetStateVerified,
	}
	if _, _, err := db.CreateDatasetVersionImmutable(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	physical := "/scratch/cache/facade/v3"
	remote.entries[physical] = filespace.RemoteEntry{Path: physical, Exists: true, Type: "directory"}
	remote.hashes[physical] = filespace.HashResult{Revision: dataset.Revision, ManifestSHA256: dataset.Revision, TotalBytes: 128, FileCount: 4}

	materialization, err := service.Verify(ctx, "facade@v3", "gpu_id", "cache/facade/v3")
	if err != nil || materialization.State != store.MaterializationReady || materialization.VerifiedSHA256 != dataset.Revision || materialization.BytesPresent != 128 || materialization.VerifiedAt == nil {
		t.Fatalf("materialization=%#v err=%v", materialization, err)
	}
	jobs, err := db.ListTransferJobs(ctx, "", 100)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("verify created transfer jobs: jobs=%#v err=%v", jobs, err)
	}
	placements, err := db.ListPathPlacements(ctx, dataset.LogicalURI)
	if err != nil || len(placements) != 1 || placements[0].Role != store.PlacementRoleCache || placements[0].ObservedState != store.PlacementObservedPresent || placements[0].Revision != dataset.Revision {
		t.Fatalf("placements=%#v err=%v", placements, err)
	}
}

func TestVerifyMissingOrMismatchedDatasetCacheFailsWithoutTransfer(t *testing.T) {
	db, service, _, remote := datasetFixture(t)
	ctx := context.Background()
	dataset := &store.DatasetVersion{
		ID: "dataset_v3", DatasetID: "facade", Version: "v3", StorageTargetID: "storage_id",
		LogicalURI: "aexp://project/datasets/facade/v3",
		Revision:   "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: store.DatasetStateVerified,
	}
	if _, _, err := db.CreateDatasetVersionImmutable(ctx, dataset); err != nil {
		t.Fatal(err)
	}

	missing, err := service.Verify(ctx, "facade@v3", "gpu_id", "cache/facade/v3")
	if err == nil || missing == nil || missing.State != store.MaterializationFailed || missing.LastError != "dataset cache is missing" {
		t.Fatalf("missing=%#v err=%v", missing, err)
	}

	physical := "/scratch/cache/facade/v3"
	remote.entries[physical] = filespace.RemoteEntry{Path: physical, Exists: true, Type: "directory"}
	remote.hashes[physical] = filespace.HashResult{Revision: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", TotalBytes: 64}
	mismatched, err := service.Verify(ctx, "facade@v3", "gpu_id", "cache/facade/v3")
	var conflict *DestinationConflictError
	if !errors.As(err, &conflict) || mismatched == nil || mismatched.State != store.MaterializationFailed || mismatched.VerifiedSHA256 == "" {
		t.Fatalf("mismatched=%#v err=%v", mismatched, err)
	}
	jobs, listErr := db.ListTransferJobs(ctx, "", 100)
	if listErr != nil || len(jobs) != 0 {
		t.Fatalf("failed verify created transfer jobs: jobs=%#v err=%v", jobs, listErr)
	}
}
