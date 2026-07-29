package freeze

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/store"
	"github.com/ziwu/aexp/internal/transfer"
)

type managedFreezeTransport struct {
	verify transfer.VerifyResult
}

func (f *managedFreezeTransport) Copy(_ context.Context, _ transfer.CopyRequest, report func(transfer.Progress) error) error {
	return report(transfer.Progress{BytesDone: f.verify.TotalBytes, FilesDone: f.verify.FileCount})
}

func (f *managedFreezeTransport) Verify(context.Context, transfer.CopyRequest) (transfer.VerifyResult, error) {
	return f.verify, nil
}

func (f *managedFreezeTransport) Promote(context.Context, transfer.CopyRequest) error { return nil }

type managedMetadataWriter struct {
	paths []string
	data  [][]byte
}

func (w *managedMetadataWriter) WriteAtomic(_ context.Context, location filespace.RemoteLocation, data []byte, _ uint32) error {
	w.paths = append(w.paths, location.PhysicalPath)
	w.data = append(w.data, append([]byte(nil), data...))
	return nil
}

func managedFreezeFixture(t *testing.T, workspace string, profile Profile) (*store.SQLite, ManagedRuntime, *store.RunFreeze, *managedMetadataWriter) {
	t.Helper()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	for _, resource := range []store.Resource{
		{ID: "nas_id", Name: "nas", Type: store.ResourceTypeSSH, Host: "nas", RootDir: "/vol/data"},
		{ID: "gpu_id", Name: "gpu", Type: store.ResourceTypeSSH, Host: "gpu", RootDir: "/work"},
	} {
		if err := db.CreateResource(ctx, &resource); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	target := &store.StorageTarget{
		ID: "storage_id", Name: "nas", ResourceID: "nas_id", RootPath: "/vol/data/aexp",
		Health: &store.StorageTargetHealth{CheckedAt: now, DataPlane: []store.StorageDataPlaneHealth{{
			ResourceID: "gpu_id", CheckedAt: now,
			NASInitiated: store.StorageConnectionHealth{Status: store.StorageStatusHealthy}, ComputeInitiated: store.StorageConnectionHealth{Status: store.StorageStatusHealthy},
		}}},
	}
	if err := db.SaveStorageTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	run := &store.Run{ID: "run_formal", ResourceID: "gpu_id", Status: store.RunStatusSucceeded, Kind: store.RunKindFormal, EvidenceGrade: store.RunEvidenceGradeFormal, Command: "python train.py", Cwd: "/work/project", ResolvedCwd: "/work/project", GitRepoRoot: workspace}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	plan := Plan{
		RunID: run.ID, Profile: profile, DestinationURI: "storage://nas/paper/project", WorkspacePath: workspace,
		Eligible: true, Files: []PlannedFile{{ArtifactID: "artifact_one", Role: "metrics", RelativePath: "results/seed.json", SourceURI: "ssh://gpu/results/seed.json", SHA256: digest, Size: 12, Required: true}},
		FileCount: 1, TotalBytes: 12, RunManifestSHA256: "sha256:run", ProfileSHA256: "sha256:profile", PlanSHA256: "sha256:plan", FreezeID: "freeze_managed", Provenance: map[string]any{"git": "commit"},
	}
	record, err := NewRecord(&plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRunFreeze(ctx, record); err != nil {
		t.Fatal(err)
	}
	remote := &managedRemoteFS{}
	files := filespace.NewService(db, remote)
	planner := transfer.NewPlanner(db, files)
	transfers := transfer.NewService(db, planner)
	_, revision, bytes, count, err := transfer.NormalizeSelection([]transfer.ManifestEntry{{Path: "results/seed.json", Type: "file", SHA256: digest, Size: 12}})
	if err != nil {
		t.Fatal(err)
	}
	payload := &managedFreezeTransport{verify: transfer.VerifyResult{Revision: revision, TotalBytes: bytes, FileCount: count}}
	writer := &managedMetadataWriter{}
	runtime := ManagedRuntime{Store: db, Planner: planner, Transfers: transfers, Worker: transfer.NewWorker(db, payload), Writer: writer}
	return db, runtime, record, writer
}

type managedRemoteFS struct{}

func (_ *managedRemoteFS) Stat(_ context.Context, _ filespace.RemoteLocation) (filespace.RemoteEntry, error) {
	return filespace.RemoteEntry{Exists: false}, nil
}
func (_ *managedRemoteFS) List(_ context.Context, _ filespace.RemoteLocation, _ string, _ int) (filespace.ListResult, error) {
	return filespace.ListResult{}, errors.New("unexpected remote list")
}
func (_ *managedRemoteFS) Hash(_ context.Context, _ filespace.RemoteLocation) (filespace.HashResult, error) {
	return filespace.HashResult{}, errors.New("unexpected remote hash")
}

func TestManagedFreezeUsesTransferJobBeforeFrozen(t *testing.T) {
	db, runtime, record, writer := managedFreezeFixture(t, "", Profile{Name: "paper", Storage: "nas"})
	if err := ExecuteManaged(context.Background(), runtime, record.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetRunFreeze(context.Background(), record.ID)
	if err != nil || stored.State != store.RunFreezeFrozen || stored.RawTransferID == "" || stored.RawManifestSHA256 == "" || stored.ManifestURI == "" || stored.FilesDone != 1 {
		t.Fatalf("freeze=%#v err=%v", stored, err)
	}
	job, _ := db.GetTransferJob(context.Background(), stored.RawTransferID)
	if job == nil || job.State != store.TransferCompleted {
		t.Fatalf("job=%#v", job)
	}
	files, _ := db.ListRunFreezeFiles(context.Background(), record.ID)
	if len(files) != 1 || len(writer.paths) != 2 {
		t.Fatalf("files=%#v writer=%#v", files, writer)
	}
	var manifest map[string]any
	if err := json.Unmarshal(writer.data[0], &manifest); err != nil && json.Unmarshal(writer.data[1], &manifest) != nil {
		t.Fatalf("metadata was not JSON: %v", err)
	}
}

func TestManagedFreezeGateFailureIsBlockedAfterRawWasFrozen(t *testing.T) {
	workspace := t.TempDir()
	aggregate := `mkdir -p "$AEXP_DERIVED_DIR"; printf 'value\n' > "$AEXP_DERIVED_DIR/table.csv"; printf '{"freeze_id":"%s","raw_manifest_sha256":"%s"}' "$AEXP_FREEZE_ID" "$AEXP_RAW_MANIFEST_SHA256" > "$AEXP_DERIVED_DIR/table.csv.provenance.json"`
	profile := Profile{Name: "paper", Storage: "nas", AggregateCommand: aggregate, AggregateOutputs: []string{"derived/table.csv"}, GateCommand: "exit 9"}
	db, runtime, record, _ := managedFreezeFixture(t, workspace, profile)
	err := ExecuteManaged(context.Background(), runtime, record.ID)
	if err != nil {
		t.Fatalf("blocked freeze should return a durable blocked state, got %v", err)
	}
	stored, _ := db.GetRunFreeze(context.Background(), record.ID)
	if stored.State != store.RunFreezeBlocked || stored.ErrorCode != "release_gate_failed" || stored.FrozenAt == nil || stored.RawTransferID == "" {
		t.Fatalf("freeze=%#v", stored)
	}
	freezeWorkspace := filepath.Join(workspace, "run_formal", record.ID)
	for _, name := range []string{"raw-manifest.json", "freeze.json"} {
		if _, err := filepath.Glob(filepath.Join(freezeWorkspace, name)); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(freezeWorkspace, name))
		if err != nil || !json.Valid(raw) {
			t.Fatalf("%s missing or invalid: %s err=%v", name, raw, err)
		}
	}
}

func TestManagedFreezeResumesFromVerifyingWithoutCreatingAnotherTransfer(t *testing.T) {
	db, runtime, record, writer := managedFreezeFixture(t, "", Profile{Name: "paper", Storage: "nas"})
	ctx := context.Background()
	if err := ExecuteManaged(ctx, runtime, record.ID); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.GetRunFreeze(ctx, record.ID)
	originalTransfer := stored.RawTransferID
	stored.State, stored.Stage = store.RunFreezeVerifying, store.RunFreezeVerifying
	if updated, err := db.UpdateRunFreezeIfState(ctx, stored, store.RunFreezeFrozen); err != nil || !updated {
		t.Fatalf("rewind: updated=%v err=%v", updated, err)
	}
	writer.paths = nil
	if err := ExecuteManaged(ctx, runtime, record.ID); err != nil {
		t.Fatal(err)
	}
	resumed, _ := db.GetRunFreeze(ctx, record.ID)
	if resumed.State != store.RunFreezeFrozen || resumed.RawTransferID != originalTransfer || len(writer.paths) != 2 {
		t.Fatalf("resumed=%#v metadata=%#v", resumed, writer.paths)
	}
	jobs, err := db.ListTransferJobs(ctx, "", 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%#v err=%v", jobs, err)
	}
}

func TestManagedFreezeResumesGateCheckingWithDerivedLedger(t *testing.T) {
	workspace := t.TempDir()
	aggregate := `mkdir -p "$AEXP_DERIVED_DIR"; printf 'value\n' > "$AEXP_DERIVED_DIR/table.csv"; printf '{"freeze_id":"%s","raw_manifest_sha256":"%s"}' "$AEXP_FREEZE_ID" "$AEXP_RAW_MANIFEST_SHA256" > "$AEXP_DERIVED_DIR/table.csv.provenance.json"`
	profile := Profile{Name: "paper", Storage: "nas", AggregateCommand: aggregate, AggregateOutputs: []string{"derived/table.csv"}, GateCommand: "exit 0"}
	db, runtime, record, _ := managedFreezeFixture(t, workspace, profile)
	ctx := context.Background()
	if err := ExecuteManaged(ctx, runtime, record.ID); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.GetRunFreeze(ctx, record.ID)
	stored.State, stored.Stage = store.RunFreezeGateChecking, store.RunFreezeGateChecking
	stored.ReleaseManifestSHA256, stored.ReleasedAt = "", nil
	if updated, err := db.UpdateRunFreezeIfState(ctx, stored, store.RunFreezeReleased); err != nil || !updated {
		t.Fatalf("rewind: updated=%v err=%v", updated, err)
	}
	if err := ExecuteManaged(ctx, runtime, record.ID); err != nil {
		t.Fatal(err)
	}
	resumed, _ := db.GetRunFreeze(ctx, record.ID)
	files, _ := db.ListRunFreezeFiles(ctx, record.ID)
	if resumed.State != store.RunFreezeReleased || resumed.ReleaseManifestSHA256 == "" || len(files) < 2 {
		t.Fatalf("resumed=%#v files=%#v", resumed, files)
	}
}
