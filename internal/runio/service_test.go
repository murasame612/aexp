package runio

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/store"
	"github.com/ziwu/aexp/internal/transfer"
)

type runIOTransport struct {
	mu        sync.Mutex
	verify    transfer.VerifyResult
	verifyErr error
	copyCalls int
	onCopy    func()
}

func (f *runIOTransport) Copy(_ context.Context, _ transfer.CopyRequest, report func(transfer.Progress) error) error {
	f.mu.Lock()
	f.copyCalls++
	onCopy := f.onCopy
	f.mu.Unlock()
	if onCopy != nil {
		onCopy()
	}
	return report(transfer.Progress{BytesDone: f.verify.TotalBytes, FilesDone: f.verify.FileCount})
}
func (f *runIOTransport) Verify(context.Context, transfer.CopyRequest) (transfer.VerifyResult, error) {
	return f.verify, f.verifyErr
}
func (f *runIOTransport) Promote(context.Context, transfer.CopyRequest) error { return nil }

func (f *runIOTransport) CopyCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.copyCalls
}

type fakeRemote struct {
	entries map[string]filespace.RemoteEntry
	hashes  map[string]filespace.HashResult
}

func (f *fakeRemote) Stat(_ context.Context, location filespace.RemoteLocation) (filespace.RemoteEntry, error) {
	if entry, ok := f.entries[location.Resource.ID+":"+location.PhysicalPath]; ok {
		return entry, nil
	}
	return filespace.RemoteEntry{Path: location.PhysicalPath}, nil
}

func (f *fakeRemote) Hash(_ context.Context, location filespace.RemoteLocation) (filespace.HashResult, error) {
	return f.hashes[location.Resource.ID+":"+location.PhysicalPath], nil
}

func (f *fakeRemote) List(context.Context, filespace.RemoteLocation, string, int) (filespace.ListResult, error) {
	return filespace.ListResult{}, nil
}

func newServiceTestStore(t *testing.T) *store.SQLite {
	t.Helper()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestEnsureInputsReusesVerifiedComputeCacheWithoutTransfer(t *testing.T) {
	ctx := context.Background()
	db := newServiceTestStore(t)
	gpu := &store.Resource{ID: "gpu", Name: "gpu", Type: "ssh", Host: "gpu", RootDir: "/workspace"}
	if err := db.CreateResource(ctx, gpu); err != nil {
		t.Fatal(err)
	}
	run := &store.Run{ID: "run_cached", ResourceID: gpu.ID, Status: store.RunStatusQueued, Kind: store.RunKindSmoke, Command: "python smoke.py"}
	revision := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	bindings := store.RunBindings{Inputs: []store.RunInputBinding{{LogicalURI: "aexp://project/data/raw", TargetPath: "data/raw", Revision: revision, Mode: "copy"}}}
	if err := db.CreateRunWithBindings(ctx, run, bindings); err != nil {
		t.Fatal(err)
	}
	remote := &fakeRemote{
		entries: map[string]filespace.RemoteEntry{"gpu:/workspace/data/raw": {Path: "/workspace/data/raw", Exists: true, Type: "directory"}},
		hashes:  map[string]filespace.HashResult{"gpu:/workspace/data/raw": {Revision: revision, ManifestSHA256: revision, FileCount: 2, TotalBytes: 42}},
	}
	files := filespace.NewService(db, remote)
	planner := transfer.NewPlanner(db, files)
	transfers := transfer.NewService(db, planner)
	service := NewService(db, files, planner, transfers, nil, remote)
	if err := service.EnsureInputs(ctx, run, gpu); err != nil {
		t.Fatal(err)
	}
	stored, err := db.ListRunInputBindings(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].State != store.RunBindingReady || stored[0].TransferID != "" || stored[0].VerifiedAt == nil {
		t.Fatalf("binding = %#v", stored)
	}
	jobs, err := db.ListTransferJobs(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("cache reuse created transfers: %#v", jobs)
	}
}

func TestEnsureInputsMaterializesMissingCacheBeforeReady(t *testing.T) {
	ctx := context.Background()
	db := newServiceTestStore(t)
	gpu, revision, service, transport := managedRunIOFixture(t, db)
	run := &store.Run{ID: "run_materialize", ResourceID: gpu.ID, Status: store.RunStatusPreflighting, Kind: store.RunKindSmoke, Command: "python smoke.py"}
	bindings := store.RunBindings{Inputs: []store.RunInputBinding{{LogicalURI: "aexp://project/data/raw", TargetPath: "data/raw", Revision: revision, Mode: "copy"}}}
	if err := db.CreateRunWithBindings(ctx, run, bindings); err != nil {
		t.Fatal(err)
	}
	if err := service.EnsureInputs(ctx, run, gpu); err != nil {
		t.Fatal(err)
	}
	stored, err := db.ListRunInputBindings(ctx, run.ID)
	if err != nil || len(stored) != 1 || stored[0].State != store.RunBindingReady || stored[0].TransferID == "" || stored[0].VerifiedAt == nil {
		t.Fatalf("bindings=%#v err=%v", stored, err)
	}
	job, err := db.GetTransferJob(ctx, stored[0].TransferID)
	if err != nil || job.State != store.TransferCompleted || transport.copyCalls != 1 {
		t.Fatalf("job=%#v copy_calls=%d err=%v", job, transport.copyCalls, err)
	}
	placements, err := db.ListPathPlacements(ctx, "aexp://project/data/raw")
	if err != nil || len(placements) != 2 || placements[1].Role != store.PlacementRoleCache || placements[1].Revision != revision {
		t.Fatalf("placements=%#v err=%v", placements, err)
	}
}

func TestRequiredOutputMissingBlocksFinalizationWithoutChangingLifecycle(t *testing.T) {
	ctx := context.Background()
	db := newServiceTestStore(t)
	gpu := &store.Resource{ID: "gpu", Name: "gpu", Type: "ssh", Host: "gpu", RootDir: "/workspace"}
	if err := db.CreateResource(ctx, gpu); err != nil {
		t.Fatal(err)
	}
	run := &store.Run{ID: "run_missing_output", ResourceID: gpu.ID, Status: store.RunStatusSucceeded, Kind: store.RunKindSmoke, Command: "python smoke.py", Cwd: "/workspace/project", DataFinalizationState: store.RunDataFinalizationPending}
	bindings := store.RunBindings{Outputs: []store.RunOutputBinding{{SourcePattern: "results/**", LogicalURI: "aexp://project/runs/run_missing_output/results", Role: "metrics", Required: true}}}
	if err := db.CreateRunWithBindings(ctx, run, bindings); err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: db}
	if err := service.FinalizeOutputs(ctx, run, gpu); err == nil {
		t.Fatal("expected required output blocker")
	}
	storedRun, err := db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRun.Status != store.RunStatusSucceeded || storedRun.DataFinalizationState != store.RunDataFinalizationBlocked {
		t.Fatalf("run = %#v", storedRun)
	}
	outputs, err := db.ListRunOutputBindings(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 1 || outputs[0].State != store.RunBindingMissing || outputs[0].ErrorCode != "output_missing" {
		t.Fatalf("outputs = %#v", outputs)
	}
}

func TestOutputPublishCompletesDurablePlacementAndKeepsRunSucceeded(t *testing.T) {
	ctx := context.Background()
	db := newServiceTestStore(t)
	gpu, revision, service, transport := managedRunIOFixture(t, db)
	run := &store.Run{ID: "run_publish", ResourceID: gpu.ID, Status: store.RunStatusSucceeded, Kind: store.RunKindSmoke, Command: "python smoke.py", Cwd: "/workspace/project", ResolvedCwd: "/workspace/project", DataFinalizationState: store.RunDataFinalizationPending}
	bindings := store.RunBindings{Outputs: []store.RunOutputBinding{{SourcePattern: "results/metrics.json", LogicalURI: "aexp://project/data/runs/run_publish/metrics.json", Role: "metrics", Required: true}}}
	if err := db.CreateRunWithBindings(ctx, run, bindings); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveArtifacts(ctx, run.ID, []store.Artifact{{ID: "artifact_metrics", RunID: run.ID, Path: "/workspace/project/results/metrics.json", RelativePath: "results/metrics.json", Type: "file", Size: 17, SHA256: revision}}); err != nil {
		t.Fatal(err)
	}
	transport.verify = transfer.VerifyResult{Revision: revision, TotalBytes: 17, FileCount: 1}
	if err := service.FinalizeOutputs(ctx, run, gpu); err != nil {
		t.Fatal(err)
	}
	storedRun, _ := db.GetRun(ctx, run.ID)
	outputs, _ := db.ListRunOutputBindings(ctx, run.ID)
	placements, _ := db.ListPathPlacements(ctx, bindings.Outputs[0].LogicalURI)
	if storedRun.Status != store.RunStatusSucceeded || storedRun.DataFinalizationState != store.RunDataFinalizationCompleted || len(outputs) != 1 || outputs[0].State != store.RunBindingPublished || outputs[0].TransferID == "" || outputs[0].Revision != revision {
		t.Fatalf("run=%#v outputs=%#v", storedRun, outputs)
	}
	if len(placements) != 1 || placements[0].Role != store.PlacementRoleAuthoritative || placements[0].Revision != revision || transport.copyCalls != 1 {
		t.Fatalf("placements=%#v copy_calls=%d", placements, transport.copyCalls)
	}
}

func TestFinalizeOutputsDoesNotOverwriteConcurrentLifecycleUpdate(t *testing.T) {
	ctx := context.Background()
	db := newServiceTestStore(t)
	gpu, revision, service, transport := managedRunIOFixture(t, db)
	started := time.Now().UTC().Add(-time.Minute)
	run := &store.Run{
		ID: "run_finalize_race", ResourceID: gpu.ID, Status: store.RunStatusSucceeded,
		Kind: store.RunKindSmoke, Command: "python smoke.py", Cwd: "/workspace/project",
		ResolvedCwd: "/workspace/project", DataFinalizationState: store.RunDataFinalizationPending,
		StartedAt: sql.NullTime{Time: started, Valid: true},
	}
	bindings := store.RunBindings{Outputs: []store.RunOutputBinding{{
		SourcePattern: "results/metrics.json",
		LogicalURI:    "aexp://project/data/runs/run_finalize_race/metrics.json",
		Role:          "metrics",
		Required:      true,
	}}}
	if err := db.CreateRunWithBindings(ctx, run, bindings); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveArtifacts(ctx, run.ID, []store.Artifact{{
		ID: "artifact_finalize_race", RunID: run.ID,
		Path: "/workspace/project/results/metrics.json", RelativePath: "results/metrics.json",
		Type: "file", Size: 17, SHA256: revision,
	}}); err != nil {
		t.Fatal(err)
	}
	transport.verify = transfer.VerifyResult{Revision: revision, TotalBytes: 17, FileCount: 1}
	cancelledAt := time.Now().UTC()
	transport.onCopy = func() {
		current, err := db.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		current.Status = store.RunStatusCancelled
		current.StatusSource = "concurrent_test"
		current.FinishedAt = sql.NullTime{Time: cancelledAt, Valid: true}
		if err := db.UpdateRun(ctx, current); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.FinalizeOutputs(ctx, run, gpu); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != store.RunStatusCancelled || stored.StatusSource != "concurrent_test" ||
		!stored.FinishedAt.Valid || !stored.FinishedAt.Time.Equal(cancelledAt) {
		t.Fatalf("concurrent lifecycle update was overwritten: %#v", stored)
	}
	if stored.DataFinalizationState != store.RunDataFinalizationCompleted {
		t.Fatalf("data finalization state = %q", stored.DataFinalizationState)
	}
}

func TestFinalizeOutputsSingleflightsConcurrentCallsForSameRun(t *testing.T) {
	ctx := context.Background()
	db := newServiceTestStore(t)
	gpu, revision, service, transport := managedRunIOFixture(t, db)
	run := &store.Run{
		ID: "run_finalize_singleflight", ResourceID: gpu.ID, Status: store.RunStatusSucceeded,
		Kind: store.RunKindSmoke, Command: "python smoke.py", Cwd: "/workspace/project",
		ResolvedCwd: "/workspace/project", DataFinalizationState: store.RunDataFinalizationPending,
	}
	bindings := store.RunBindings{Outputs: []store.RunOutputBinding{{
		SourcePattern: "results/metrics.json",
		LogicalURI:    "aexp://project/data/runs/run_finalize_singleflight/metrics.json",
		Role:          "metrics",
		Required:      true,
	}}}
	if err := db.CreateRunWithBindings(ctx, run, bindings); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveArtifacts(ctx, run.ID, []store.Artifact{{
		ID: "artifact_finalize_singleflight", RunID: run.ID,
		Path: "/workspace/project/results/metrics.json", RelativePath: "results/metrics.json",
		Type: "file", Size: 17, SHA256: revision,
	}}); err != nil {
		t.Fatal(err)
	}
	transport.verify = transfer.VerifyResult{Revision: revision, TotalBytes: 17, FileCount: 1}

	started := make(chan struct{})
	releaseCopy := make(chan struct{})
	var once sync.Once
	transport.onCopy = func() {
		once.Do(func() { close(started) })
		<-releaseCopy
	}

	errs := make(chan error, 2)
	go func() { errs <- service.FinalizeOutputs(ctx, run, gpu) }()
	<-started
	go func() { errs <- service.FinalizeOutputs(ctx, run, gpu) }()
	close(releaseCopy)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	stored, err := db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DataFinalizationState != store.RunDataFinalizationCompleted {
		t.Fatalf("data finalization state = %q", stored.DataFinalizationState)
	}
	if calls := transport.CopyCalls(); calls != 1 {
		t.Fatalf("output was published %d times, want 1", calls)
	}
}

func TestOutputPublishFailureDoesNotChangeSucceededLifecycleOrDeleteArtifact(t *testing.T) {
	ctx := context.Background()
	db := newServiceTestStore(t)
	gpu, revision, service, transport := managedRunIOFixture(t, db)
	run := &store.Run{ID: "run_publish_failed", ResourceID: gpu.ID, Status: store.RunStatusSucceeded, Kind: store.RunKindSmoke, Command: "python smoke.py", Cwd: "/workspace/project", DataFinalizationState: store.RunDataFinalizationPending}
	binding := store.RunOutputBinding{SourcePattern: "result.json", LogicalURI: "aexp://project/data/runs/run_publish_failed/result.json", Required: true}
	if err := db.CreateRunWithBindings(ctx, run, store.RunBindings{Outputs: []store.RunOutputBinding{binding}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveArtifacts(ctx, run.ID, []store.Artifact{{ID: "artifact_result", RunID: run.ID, Path: "/workspace/project/result.json", RelativePath: "result.json", Type: "file", Size: 5, SHA256: revision}}); err != nil {
		t.Fatal(err)
	}
	transport.verifyErr = errors.New("destination hash unavailable")
	if err := service.FinalizeOutputs(ctx, run, gpu); err == nil {
		t.Fatal("publish verification failure was reported as success")
	}
	storedRun, _ := db.GetRun(ctx, run.ID)
	artifacts, _ := db.ListArtifacts(ctx, run.ID)
	outputs, _ := db.ListRunOutputBindings(ctx, run.ID)
	if storedRun.Status != store.RunStatusSucceeded || storedRun.DataFinalizationState != store.RunDataFinalizationFailed || len(artifacts) != 1 || artifacts[0].ID != "artifact_result" || len(outputs) != 1 || outputs[0].State != store.RunBindingFailed {
		t.Fatalf("run=%#v artifacts=%#v outputs=%#v", storedRun, artifacts, outputs)
	}
}

func managedRunIOFixture(t *testing.T, db *store.SQLite) (*store.Resource, string, *Service, *runIOTransport) {
	t.Helper()
	ctx := context.Background()
	gpu := &store.Resource{ID: "gpu", Name: "gpu", Type: store.ResourceTypeSSH, Host: "gpu", RootDir: "/workspace"}
	nas := &store.Resource{ID: "nas", Name: "nas", Type: store.ResourceTypeSSH, Host: "nas", RootDir: "/vol/data"}
	for _, resource := range []*store.Resource{gpu, nas} {
		if err := db.CreateResource(ctx, resource); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	target := &store.StorageTarget{ID: "storage", Name: "storage", ResourceID: nas.ID, RootPath: "/vol/data/aexp", Health: &store.StorageTargetHealth{CheckedAt: now, DataPlane: []store.StorageDataPlaneHealth{{ResourceID: gpu.ID, CheckedAt: now, NASInitiated: store.StorageConnectionHealth{Status: store.StorageStatusHealthy}, ComputeInitiated: store.StorageConnectionHealth{Status: store.StorageStatusHealthy}}}}}
	if err := db.SaveStorageTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveLogicalRoot(ctx, &store.LogicalRoot{ID: "data_root", Workspace: "project", Prefix: "data", StorageTargetID: target.ID, PhysicalRoot: "projects/project/data"}); err != nil {
		t.Fatal(err)
	}
	revision := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := db.SavePathPlacement(ctx, &store.PathPlacement{ID: "input_source", LogicalURI: "aexp://project/data/raw", ResourceID: nas.ID, StorageTargetID: target.ID, PhysicalPath: "/vol/data/aexp/projects/project/data/raw", Role: store.PlacementRoleAuthoritative, DesiredState: store.PlacementDesiredPresent, ObservedState: store.PlacementObservedPresent, Revision: revision, ManifestSHA256: revision, BytesPresent: 17, CheckedAt: &now, ObservedAt: &now}); err != nil {
		t.Fatal(err)
	}
	remote := &fakeRemote{entries: map[string]filespace.RemoteEntry{}, hashes: map[string]filespace.HashResult{}}
	files := filespace.NewService(db, remote)
	planner := transfer.NewPlanner(db, files)
	transfers := transfer.NewService(db, planner)
	transport := &runIOTransport{verify: transfer.VerifyResult{Revision: revision, TotalBytes: 17, FileCount: 1}}
	service := NewService(db, files, planner, transfers, transfer.NewWorker(db, transport), remote)
	service.PollEvery = time.Millisecond
	return gpu, revision, service, transport
}

func TestMatchingArtifactsSupportsDoubleStar(t *testing.T) {
	artifacts := []store.Artifact{{RelativePath: "results/seed/metrics.json"}, {RelativePath: "checkpoints/best.pt"}}
	matched := matchingArtifacts(artifacts, "results/**")
	if len(matched) != 1 || matched[0].RelativePath != "results/seed/metrics.json" {
		t.Fatalf("matched = %#v", matched)
	}
}

func TestManagerRecoversPendingSucceededRunFinalization(t *testing.T) {
	ctx := context.Background()
	db := newServiceTestStore(t)
	gpu := &store.Resource{ID: "gpu-recovery", Name: "gpu-recovery", Type: "ssh", Host: "gpu", RootDir: "/workspace"}
	if err := db.CreateResource(ctx, gpu); err != nil {
		t.Fatal(err)
	}
	run := &store.Run{ID: "run_recovery", ResourceID: gpu.ID, Status: store.RunStatusSucceeded, Kind: store.RunKindSmoke, Command: "python smoke.py", Cwd: "/workspace/project", DataFinalizationState: store.RunDataFinalizationPending}
	bindings := store.RunBindings{Outputs: []store.RunOutputBinding{{SourcePattern: "required.json", LogicalURI: "aexp://project/runs/run_recovery/required.json", Required: true}}}
	if err := db.CreateRunWithBindings(ctx, run, bindings); err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: db}
	manager := NewManager(db, service, 10*time.Millisecond, nil)
	manager.Start()
	defer manager.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, err := db.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.DataFinalizationState == store.RunDataFinalizationBlocked {
			if current.Status != store.RunStatusSucceeded {
				t.Fatalf("lifecycle changed: %#v", current)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("manager did not recover pending finalization")
}
