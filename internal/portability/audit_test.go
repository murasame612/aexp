package portability

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

func TestAuditReportsPortableControlPlaneGapsWithoutSecretsOrWrites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "aexp.db")
	attachmentsRoot := filepath.Join(root, "attachments")
	existingProjectRoot := filepath.Join(root, "project")
	existingDiff := filepath.Join(root, "diff.patch")
	existingAttachment := filepath.Join(attachmentsRoot, "present.png")
	for _, dir := range []string{attachmentsRoot, existingProjectRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, contents := range map[string]string{existingDiff: "diff", existingAttachment: "image"} {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	db, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secret := "PORTABILITY_SENTINEL_SECRET"
	resource := &store.Resource{ID: "resource_ssh", Name: "gpu", Type: store.ResourceTypeSSH, Host: "gpu.example", Port: 22, User: "runner", AuthRef: secret, SocksProxy: secret, ProxyCommand: secret, RootDir: "/home/runner/research"}
	if err := db.CreateResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	project := &store.ProjectDefinition{ID: "project_portability", Name: "Portability", LocalRoot: existingProjectRoot, ConfigPath: filepath.Join(root, "missing-project.yaml")}
	if err := db.SaveProjectDefinition(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProjectTarget(ctx, &store.ProjectTarget{ID: "target_portability", ProjectID: project.ID, Name: "gpu", ResourceID: resource.ID, Cwd: "/home/runner/research/project", EnvJSON: secret, PrepareCommand: secret}); err != nil {
		t.Fatal(err)
	}
	storageTarget := &store.StorageTarget{ID: "storage_portability", Name: "nas", Kind: store.StorageKindSSHRsync, ResourceID: resource.ID, RootPath: "/volume/research", ConfigJSON: secret}
	if err := db.SaveStorageTarget(ctx, storageTarget); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveLogicalRoot(ctx, &store.LogicalRoot{ID: "logical_project", Workspace: "project", Prefix: "data", StorageTargetID: storageTarget.ID, PhysicalRoot: "projects/portability/data"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SavePathPlacement(ctx, &store.PathPlacement{ID: "placement_project", LogicalURI: "aexp://project/data/raw", ResourceID: resource.ID, StorageTargetID: storageTarget.ID, PhysicalPath: "/volume/research/projects/portability/data/raw", Role: store.PlacementRoleAuthoritative, DesiredState: store.PlacementDesiredPresent}); err != nil {
		t.Fatal(err)
	}
	run := &store.Run{ID: "run_portability", ProjectID: project.ID, TargetID: "target_portability", ResourceID: resource.ID, Status: store.RunStatusSucceeded, Kind: store.RunKindFormal, Cwd: "/home/runner/research/project", ResolvedCwd: "/home/runner/research/project", RemoteRunDir: "/home/runner/research/.aexp/runs/run_portability", GitRepoRoot: "/home/runner/research/project", GitDiffPath: existingDiff, Command: secret, EnvJSON: secret}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveArtifacts(ctx, run.ID, []store.Artifact{{ID: "artifact_absolute", RunID: run.ID, Path: "/home/runner/research/project/results.json", Type: "file"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveDatasetVersion(ctx, &store.DatasetVersion{ID: "dataset_portability", DatasetID: "dataset", Version: "v1", StorageTargetID: storageTarget.ID, StoragePath: "datasets/v1", State: store.DatasetStateRegistered}); err != nil {
		t.Fatal(err)
	}
	mark := &store.RunMark{ID: "mark_portability", RunID: run.ID, Actor: "agent", Kind: "key_result", Title: "Portability evidence"}
	if err := db.SaveRunMark(ctx, mark); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRunMarkAttachments(ctx, mark.ID, []store.RunMarkAttachment{
		{ID: "attachment_present", MarkID: mark.ID, Filename: "present.png", LocalPath: existingAttachment},
		{ID: "attachment_missing", MarkID: mark.ID, Filename: "missing.png", LocalPath: filepath.Join(attachmentsRoot, "missing.png")},
	}); err != nil {
		t.Fatal(err)
	}

	fixedNow := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	report, err := (Service{Store: db, DatabasePath: dbPath, AttachmentsRoot: attachmentsRoot, Now: func() time.Time { return fixedNow }}).Audit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != SchemaVersion || report.Mode != ModeReadOnly || report.TargetMode != TargetMode || !report.GeneratedAt.Equal(fixedNow) {
		t.Fatalf("unexpected report contract: %#v", report)
	}
	if report.Status != "blocked" || report.ReadyForBundle || report.Summary.BlockingFindings != 2 || report.Summary.MissingAttachments != 1 {
		t.Fatalf("unexpected blocking summary: %#v", report.Summary)
	}
	for _, code := range []string{"ATTACHMENT_MISSING", "PROJECT_LOCAL_PATH_MISSING", "RESOURCE_REBIND_REQUIRED", "DATASET_LOGICAL_URI_MISSING", "ARTIFACT_LOGICAL_REFERENCE_MISSING"} {
		if !hasFinding(report, code) {
			t.Errorf("missing finding %s", code)
		}
	}
	if report.Summary.LogicalRoots != 1 || report.Summary.PathPlacements != 1 || report.Summary.ResourcesRequiringRebind != 1 {
		t.Fatalf("unexpected portability inventory: %#v", report.Summary)
	}
	remote := findPath(report, "run", run.ID, "remote_run_dir")
	if remote == nil || remote.State != StateNotChecked || !remote.MachineBound {
		t.Fatalf("remote path must be recorded without probing: %#v", remote)
	}
	present := findPath(report, "attachment", "attachment_present", "local_path")
	if present == nil || present.State != StatePresent {
		t.Fatalf("existing attachment must be present: %#v", present)
	}
	repeated, err := (Service{Store: db, DatabasePath: dbPath, AttachmentsRoot: attachmentsRoot, Now: func() time.Time { return fixedNow }}).Audit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report, repeated) {
		t.Fatal("unchanged state did not produce a deterministic audit report")
	}

	jsonBytes, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var human bytes.Buffer
	if err := WriteHuman(&human, report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(jsonBytes), secret) || strings.Contains(human.String(), secret) {
		t.Fatal("audit output leaked a persisted secret or command")
	}
	gotRun, err := db.GetRun(ctx, run.ID)
	if err != nil || gotRun == nil || gotRun.Status != store.RunStatusSucceeded {
		t.Fatalf("audit changed run state: %#v err=%v", gotRun, err)
	}
}

func hasFinding(report Report, code string) bool {
	for _, finding := range report.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func findPath(report Report, entityType, entityID, field string) *PathReference {
	for i := range report.Paths {
		ref := &report.Paths[i]
		if ref.EntityType == entityType && ref.EntityID == entityID && ref.Field == field {
			return ref
		}
	}
	return nil
}
