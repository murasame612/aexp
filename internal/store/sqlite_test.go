package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLite {
	t.Helper()
	dir := t.TempDir()
	s, err := NewSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func createTestProject(t *testing.T, s *SQLite, id, name string) {
	t.Helper()
	if err := s.CreateProjectDefinition(context.Background(), &ProjectDefinition{ID: id, Name: name}); err != nil {
		t.Fatalf("CreateProjectDefinition(%s): %v", id, err)
	}
}

func TestReconcileLegacyProjectsCreatesCanonicalProjectMapAndRunLink(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_legacy_project", Name: "legacy-project-resource", Type: "ssh", Host: "localhost", RootDir: "/workspace", Status: ResourceStatusIdle}); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_legacy_project", ResourceID: "rsrc_legacy_project", Name: "legacy", Status: RunStatusSucceeded, Kind: RunKindFormal, Command: "python train.py"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO project_run_cards (id, project_id, project_name, run_id)
		VALUES (?, ?, ?, ?)
	`, "card_legacy_project", "legacy-research", "Legacy Research", "run_legacy_project"); err != nil {
		t.Fatalf("insert legacy orphan card: %v", err)
	}

	if err := s.reconcileLegacyProjects(); err != nil {
		t.Fatalf("reconcileLegacyProjects: %v", err)
	}
	if err := s.reconcileLegacyProjects(); err != nil {
		t.Fatalf("reconcileLegacyProjects idempotent retry: %v", err)
	}

	project, err := s.GetProjectDefinition(ctx, "legacy-research")
	if err != nil {
		t.Fatalf("GetProjectDefinition: %v", err)
	}
	if project == nil || project.Name != "Legacy Research" {
		t.Fatalf("project = %#v, want imported canonical project", project)
	}
	run, err := s.GetRun(ctx, "run_legacy_project")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run == nil || run.ProjectID != "legacy-research" {
		t.Fatalf("run = %#v, want canonical project link", run)
	}
	chain, err := s.GetActivePrimaryEvidenceChain(ctx, "legacy-research")
	if err != nil {
		t.Fatalf("GetActivePrimaryEvidenceChain: %v", err)
	}
	if chain == nil || chain.ProjectID != "legacy-research" || chain.Role != "primary" {
		t.Fatalf("primary chain = %#v, want canonical primary map", chain)
	}
}

func TestNewSQLiteMigratesLegacyRunsBeforeCreatingNewIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := `
CREATE TABLE resources (
  id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, type TEXT NOT NULL DEFAULT 'ssh', host TEXT NOT NULL,
  port INTEGER NOT NULL DEFAULT 22, user TEXT NOT NULL DEFAULT 'root', auth_ref TEXT DEFAULT '', root_dir TEXT NOT NULL,
  conda_env TEXT DEFAULT '', gpu_indices TEXT DEFAULT '', tags TEXT DEFAULT '',
  status TEXT NOT NULL DEFAULT 'unknown', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE runs (
  id TEXT PRIMARY KEY, resource_id TEXT NOT NULL REFERENCES resources(id), name TEXT DEFAULT '',
  status TEXT NOT NULL DEFAULT 'created', cwd TEXT DEFAULT '', command TEXT NOT NULL,
  conda_env TEXT DEFAULT '', env_json TEXT DEFAULT '{}', log_paths_json TEXT DEFAULT '[]',
  artifact_paths_json TEXT DEFAULT '[]', metric_paths_json TEXT DEFAULT '[]', tmux_session TEXT DEFAULT '',
  remote_run_dir TEXT DEFAULT '', exit_code INTEGER, created_by TEXT DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, started_at DATETIME, finished_at DATETIME,
  kind TEXT NOT NULL DEFAULT 'formal', gpu_index INTEGER NOT NULL DEFAULT -1
);
INSERT INTO resources (id,name,host,root_dir) VALUES ('rsrc_legacy','legacy','localhost','/ws');
INSERT INTO runs (id,resource_id,name,status,command,kind) VALUES ('run_legacy','rsrc_legacy','old run','succeeded','python train.py','formal');`
	if _, err := db.Exec(legacySchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := NewSQLite(path)
	if err != nil {
		t.Fatalf("NewSQLite legacy migration: %v", err)
	}
	defer migrated.Close()
	run, err := migrated.GetRun(context.Background(), "run_legacy")
	if err != nil || run == nil {
		t.Fatalf("legacy run missing after migration: run=%#v err=%v", run, err)
	}
	if run.Name != "old run" || run.Status != RunStatusSucceeded || run.ProjectID != "" || run.TargetID != "" {
		t.Fatalf("legacy run changed during migration: %#v", run)
	}
	var indexCount int
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name IN ('idx_runs_project_created','idx_runs_target_created')`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 2 {
		t.Fatalf("new run indexes = %d, want 2", indexCount)
	}
}

func TestNewSQLiteReconcilesDeletedHistoricalResourceAsHiddenTombstone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orphan.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := `
CREATE TABLE resources (
  id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, type TEXT NOT NULL DEFAULT 'ssh', host TEXT NOT NULL,
  port INTEGER NOT NULL DEFAULT 22, user TEXT NOT NULL DEFAULT 'root', auth_ref TEXT DEFAULT '', root_dir TEXT NOT NULL,
  conda_env TEXT DEFAULT '', gpu_indices TEXT DEFAULT '', tags TEXT DEFAULT '',
  status TEXT NOT NULL DEFAULT 'unknown', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE runs (
  id TEXT PRIMARY KEY, resource_id TEXT NOT NULL REFERENCES resources(id), name TEXT DEFAULT '',
  status TEXT NOT NULL DEFAULT 'created', cwd TEXT DEFAULT '', command TEXT NOT NULL,
  conda_env TEXT DEFAULT '', env_json TEXT DEFAULT '{}', log_paths_json TEXT DEFAULT '[]',
  artifact_paths_json TEXT DEFAULT '[]', metric_paths_json TEXT DEFAULT '[]', tmux_session TEXT DEFAULT '',
  remote_run_dir TEXT DEFAULT '', exit_code INTEGER, created_by TEXT DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, started_at DATETIME, finished_at DATETIME,
  kind TEXT NOT NULL DEFAULT 'formal', gpu_index INTEGER NOT NULL DEFAULT -1
);
INSERT INTO runs (id,resource_id,name,status,command,kind) VALUES ('run_orphan','rsrc_deleted','historical run','failed','true','smoke');`
	if _, err := db.Exec(legacySchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := NewSQLite(path)
	if err != nil {
		t.Fatalf("NewSQLite orphan reconciliation: %v", err)
	}
	defer migrated.Close()
	resource, err := migrated.GetResource(context.Background(), "rsrc_deleted")
	if err != nil || resource == nil || resource.Type != ResourceTypeTombstone || resource.Status != ResourceStatusDeleted {
		t.Fatalf("tombstone=%#v err=%v", resource, err)
	}
	resources, err := migrated.ListResources(context.Background())
	if err != nil || len(resources) != 0 {
		t.Fatalf("tombstone leaked into active resources: %#v err=%v", resources, err)
	}
	var violations int
	rows, err := migrated.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		violations++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if violations != 0 {
		t.Fatalf("foreign key violations after reconciliation: %d", violations)
	}
}

func TestDeleteReferencedResourceLeavesHiddenTombstone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	resource := &Resource{ID: "rsrc_history", Name: "history", Type: ResourceTypeSSH, Host: "host", Port: 22, User: "user", RootDir: "/work", Status: ResourceStatusIdle}
	if err := s.CreateResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO resource_snapshots (resource_id) VALUES (?)`, resource.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteResource(ctx, resource.ID); err != nil {
		t.Fatal(err)
	}
	tombstone, err := s.GetResource(ctx, resource.ID)
	if err != nil || tombstone == nil || tombstone.Type != ResourceTypeTombstone || tombstone.AuthRef != "" {
		t.Fatalf("tombstone=%#v err=%v", tombstone, err)
	}
	resources, err := s.ListResources(ctx)
	if err != nil || len(resources) != 0 {
		t.Fatalf("active resources=%#v err=%v", resources, err)
	}
}

func TestDataCenterMetadataRoundTripAndMaterializationCAS(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, resource := range []Resource{
		{ID: "rsrc_nas", Name: "nas", Type: ResourceTypeSSH, Host: "nas.local", RootDir: "/data", Status: ResourceStatusIdle},
		{ID: "rsrc_gpu", Name: "gpu", Type: ResourceTypeSSH, Host: "gpu.local", RootDir: "/scratch", Status: ResourceStatusIdle},
	} {
		if err := s.CreateResource(ctx, &resource); err != nil {
			t.Fatal(err)
		}
	}
	target := &StorageTarget{ID: "storage_nas", Name: "nas", ResourceID: "rsrc_nas", RootPath: "/data/aexp"}
	if err := s.SaveStorageTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	dataset := &DatasetVersion{ID: "dataset_facade_v3", DatasetID: "facade", Version: "v3", StorageTargetID: target.ID, StoragePath: "datasets/facade/v3", ManifestSHA256: "sha256:manifest", FileCount: 12, TotalBytes: 42}
	if err := s.SaveDatasetVersion(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	m := &DatasetMaterialization{ID: "materialization_one", DatasetVersionID: dataset.ID, ResourceID: "rsrc_gpu", LocalPath: "/scratch/.aexp/datasets/facade/v3", State: MaterializationPlanned}
	if err := s.SaveDatasetMaterialization(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDatasetVersionByRef(ctx, "facade", "v3")
	if err != nil || got == nil || got.StoragePath != dataset.StoragePath || got.TotalBytes != 42 {
		t.Fatalf("dataset roundtrip: %#v err=%v", got, err)
	}
	m.State = MaterializationTransferring
	updated, err := s.UpdateDatasetMaterializationIfState(ctx, m, MaterializationPlanned)
	if err != nil || !updated {
		t.Fatalf("CAS planned->transferring updated=%v err=%v", updated, err)
	}
	m.State = MaterializationReady
	updated, err = s.UpdateDatasetMaterializationIfState(ctx, m, MaterializationPlanned)
	if err != nil || updated {
		t.Fatalf("stale CAS updated=%v err=%v", updated, err)
	}
	placement, err := s.GetDatasetMaterialization(ctx, dataset.ID, "rsrc_gpu")
	if err != nil || placement == nil || placement.State != MaterializationTransferring {
		t.Fatalf("materialization changed unexpectedly: %#v err=%v", placement, err)
	}
}

func TestVerifiedIngestPromotesMatchingRegisteredDatasetIdentity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_promote_nas", Name: "promote-nas", Type: ResourceTypeSSH, Host: "nas.local", RootDir: "/data"}); err != nil {
		t.Fatal(err)
	}
	target := &StorageTarget{ID: "storage_promote_nas", Name: "promote-nas", ResourceID: "rsrc_promote_nas", RootPath: "/data/aexp"}
	if err := s.SaveStorageTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	registered := &DatasetVersion{
		ID: "dataset_promote_v1", DatasetID: "promote", Version: "v1", StorageTargetID: target.ID,
		StoragePath: "datasets/promote/v1", LogicalURI: "storage://promote-nas/datasets/promote/v1",
		Revision: "sha256:manifest", ManifestSHA256: "sha256:manifest", State: DatasetStateRegistered,
	}
	if got, created, err := s.CreateDatasetVersionImmutable(ctx, registered); err != nil || !created || got.State != DatasetStateRegistered {
		t.Fatalf("registered=%#v created=%v err=%v", got, created, err)
	}
	verified := *registered
	verified.State = DatasetStateVerified
	verified.FileCount = 12
	verified.TotalBytes = 42
	got, created, err := s.CreateDatasetVersionImmutable(ctx, &verified)
	if err != nil || created || got.State != DatasetStateVerified || got.FileCount != 12 || got.TotalBytes != 42 {
		t.Fatalf("promoted=%#v created=%v err=%v", got, created, err)
	}
}

func TestStorageTargetDeleteIsDependencyAwareAndPreservesResource(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateResource(ctx, &Resource{ID: "nas-delete-resource", Name: "nas-delete", Type: ResourceTypeSSH, Host: "nas", RootDir: "/data"}); err != nil {
		t.Fatal(err)
	}
	target := &StorageTarget{ID: "nas-delete-target", Name: "nas-delete", ResourceID: "nas-delete-resource", RootPath: "/data"}
	if err := s.SaveStorageTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveDatasetVersion(ctx, &DatasetVersion{ID: "nas-delete-dataset", DatasetID: "d", Version: "v1", StorageTargetID: target.ID, StoragePath: "d/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteStorageTarget(ctx, target.ID); err == nil {
		t.Fatal("referenced target deletion must fail")
	}
	if got, _ := s.GetStorageTarget(ctx, target.ID); got == nil {
		t.Fatal("referenced target was deleted")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM dataset_versions WHERE id=?`, "nas-delete-dataset"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteStorageTarget(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	if resource, _ := s.GetResource(ctx, "nas-delete-resource"); resource == nil {
		t.Fatal("backing resource must be preserved")
	}
}

func TestRunFreezeLedgerAndTerminalImmutability(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateResource(ctx, &Resource{ID: "rf", Name: "rf", Type: "ssh", Host: "rf", RootDir: "/w"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_freeze", ResourceID: "rf", Status: RunStatusSucceeded, Kind: RunKindFormal, EvidenceGrade: RunEvidenceGradeFormal, Command: "x"}); err != nil {
		t.Fatal(err)
	}
	f := &RunFreeze{ID: "freeze_one", RunID: "run_freeze", Profile: "paper", ProfileSHA256: "sha256:p", PlanSHA256: "sha256:plan", DestinationURI: "storage://nas/paper", RunManifestSHA256: "sha256:run"}
	if err := s.CreateRunFreeze(ctx, f); err != nil {
		t.Fatal(err)
	}
	files := []RunFreezeFile{{ID: "ff", FreezeID: f.ID, Kind: "raw", Role: "metrics", RelativePath: "metrics.json", SourceURI: "ssh://r/metrics.json", FrozenURI: "storage://nas/metrics.json", SHA256: "sha256:file", Required: true}}
	if err := s.ReplaceRunFreezeFiles(ctx, f.ID, files); err != nil {
		t.Fatal(err)
	}
	f.State = RunFreezeReleased
	f.Stage = RunFreezeReleased
	ok, err := s.UpdateRunFreezeIfState(ctx, f, RunFreezeQueued)
	if err != nil || !ok {
		t.Fatalf("release CAS ok=%v err=%v", ok, err)
	}
	if err := s.ReplaceRunFreezeFiles(ctx, f.ID, files); err == nil {
		t.Fatal("released freeze files were mutable")
	}
}

func TestRunStatusHelpers(t *testing.T) {
	if !IsRunRefreshableStatus(RunStatusSSHUnreachable) {
		t.Fatal("ssh_unreachable should be refreshable")
	}
	if IsRunTerminalStatus(RunStatusSSHUnreachable) {
		t.Fatal("ssh_unreachable should not be terminal")
	}
	for _, status := range []string{RunStatusLost, RunStatusContainerExpired, RunStatusLostButEventsCached} {
		if !IsRunTerminalStatus(status) {
			t.Fatalf("%s should be terminal", status)
		}
	}
	for _, status := range []string{RunStatusCreated, RunStatusQueued, RunStatusPreflighting, RunStatusStarting, RunStatusRunning, RunStatusSSHUnreachable} {
		if !IsRunActiveLifecycleStatus(status) {
			t.Fatalf("%s should be an active lifecycle status", status)
		}
	}
	if IsRunRefreshableStatus(RunStatusQueued) || IsRunRefreshableStatus(RunStatusPreflighting) {
		t.Fatal("queued/preflighting runs have no remote process to refresh")
	}
}

func TestLegacySSHUnreachableIsExposedAsRunningWithUnreachableObservation(t *testing.T) {
	run := &Run{Status: RunStatusSSHUnreachable, StatusSource: RunStatusSourceLocalCache}
	RefreshRunStatusFreshness(run, time.Now())
	if run.LifecycleStatus != RunStatusRunning || run.ObservationState != RunObservationUnreachable || run.StatusFreshness != RunStatusFreshnessStale || run.ObservationError == nil {
		t.Fatalf("legacy status was not normalized: %#v", run)
	}
}

func TestListRunSummariesReturnsOnlyActiveLifecycleStatuses(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_summary", Name: "summary", Type: ResourceTypeSSH, Host: "summary", RootDir: "/ws"}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []*Run{
		{ID: "run_queued", ResourceID: "rsrc_summary", Status: RunStatusQueued, Command: "prepare"},
		{ID: "run_preflight", ResourceID: "rsrc_summary", Status: RunStatusPreflighting, Command: "detect env"},
		{ID: "run_running", ResourceID: "rsrc_summary", Status: RunStatusRunning, Command: "train"},
		{ID: "run_done", ResourceID: "rsrc_summary", Status: RunStatusSucceeded, Command: "train"},
	} {
		if err := s.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}

	summaries, err := s.ListRunSummaries(ctx, RunFilter{Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 3 {
		t.Fatalf("active summaries = %#v", summaries)
	}
	seen := map[string]bool{}
	for _, summary := range summaries {
		seen[summary.ID] = true
	}
	if !seen["run_queued"] || !seen["run_preflight"] || !seen["run_running"] || seen["run_done"] {
		t.Fatalf("active summaries = %#v", summaries)
	}
}

func TestRunChangesCaptureCreateAndExternalUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_changes", Name: "changes", Type: ResourceTypeSSH, Host: "changes", RootDir: "/ws"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_changes", ResourceID: "rsrc_changes", Status: RunStatusQueued, Command: "train"}); err != nil {
		t.Fatal(err)
	}
	initial, err := s.ListRunChanges(ctx, 0, nil, 10)
	if err != nil || len(initial) != 1 || initial[0].Operation != RunChangeUpsert {
		t.Fatalf("initial changes=%#v err=%v", initial, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE runs SET status=? WHERE id=?`, RunStatusRunning, "run_changes"); err != nil {
		t.Fatal(err)
	}
	next, err := s.ListRunChanges(ctx, initial[0].Seq, nil, 10)
	if err != nil || len(next) != 1 || next[0].RunID != "run_changes" || next[0].Seq <= initial[0].Seq {
		t.Fatalf("next changes=%#v err=%v", next, err)
	}
}

func TestRunLaunchJobClaimIsDurableAndExclusive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_launch_job", Name: "launch", Type: ResourceTypeSSH, Host: "launch", RootDir: "/ws"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_launch_job", ResourceID: "rsrc_launch_job", Status: RunStatusQueued, Command: "train"}); err != nil {
		t.Fatal(err)
	}
	job := &RunLaunchJob{RunID: "run_launch_job", RequestJSON: `{"resource_id":"rsrc_launch_job"}`, State: RunLaunchQueued}
	if err := s.SaveRunLaunchJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := s.ClaimRunLaunchJob(ctx, job.RunID)
	if err != nil || !ok || claimed == nil || claimed.State != RunLaunchLaunching || claimed.Attempts != 1 {
		t.Fatalf("claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if _, ok, err := s.ClaimRunLaunchJob(ctx, job.RunID); err != nil || ok {
		t.Fatalf("second claim ok=%v err=%v", ok, err)
	}
	if err := s.CompleteRunLaunchJob(ctx, job.RunID, RunLaunchFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.ListPendingRunLaunchJobs(ctx)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("pending=%#v err=%v", jobs, err)
	}
}

func TestCreateRunWithLaunchJobIsAtomic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_atomic_launch", Name: "atomic", Type: ResourceTypeSSH, Host: "atomic", RootDir: "/ws"}); err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: "run_atomic_launch", ResourceID: "rsrc_atomic_launch", Status: RunStatusQueued, Command: "train"}
	if err := s.CreateRunWithLaunchJob(ctx, run, &RunLaunchJob{RunID: run.ID, RequestJSON: ""}); err == nil {
		t.Fatal("empty launch request should fail the atomic create")
	}
	if persisted, err := s.GetRun(ctx, run.ID); err != nil || persisted != nil {
		t.Fatalf("orphan queued run persisted after job failure: %#v err=%v", persisted, err)
	}
	job := &RunLaunchJob{RunID: run.ID, RequestJSON: `{"resource_id":"rsrc_atomic_launch"}`, State: RunLaunchQueued}
	if err := s.CreateRunWithLaunchJob(ctx, run, job); err != nil {
		t.Fatal(err)
	}
	if persisted, _ := s.GetRun(ctx, run.ID); persisted == nil || persisted.Status != RunStatusQueued {
		t.Fatalf("persisted=%#v", persisted)
	}
	jobs, err := s.ListPendingRunLaunchJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].RunID != run.ID {
		t.Fatalf("jobs=%#v err=%v", jobs, err)
	}
}

func TestNormalizeRunSemantics(t *testing.T) {
	cases := []struct {
		kind, task, grade, role string
	}{
		{RunKindSetup, RunTaskRolePrepare, RunEvidenceGradeNone, RunExperimentRoleUnspecified},
		{RunKindSmoke, RunTaskRoleOther, RunEvidenceGradeSmoke, RunExperimentRoleUnspecified},
		{RunKindPilot, RunTaskRoleOther, RunEvidenceGradePilot, RunExperimentRoleUnspecified},
		{RunKindFormal, RunTaskRoleOther, RunEvidenceGradeFormal, RunExperimentRoleUnspecified},
		{RunKindAblation, RunTaskRoleOther, RunEvidenceGradeFormal, RunExperimentRoleAblation},
	}
	for _, tc := range cases {
		kind, task, grade, role, err := NormalizeRunSemantics(tc.kind, "", "", "")
		if err != nil || kind != tc.kind || task != tc.task || grade != tc.grade || role != tc.role {
			t.Fatalf("NormalizeRunSemantics(%q) = %q/%q/%q/%q err=%v", tc.kind, kind, task, grade, role, err)
		}
	}
	kind, task, grade, role, err := NormalizeRunSemantics("", RunTaskRoleTrain, RunEvidenceGradeFormal, RunExperimentRoleReplication)
	if err != nil || kind != RunKindFormal || task != RunTaskRoleTrain || grade != RunEvidenceGradeFormal || role != RunExperimentRoleReplication {
		t.Fatalf("new axes normalization = %q/%q/%q/%q err=%v", kind, task, grade, role, err)
	}
	if _, _, _, _, err := NormalizeRunSemantics(RunKindSmoke, RunTaskRoleTrain, RunEvidenceGradeFormal, RunExperimentRoleUnspecified); err == nil {
		t.Fatal("expected conflicting legacy and axis semantics to fail")
	}
}

func TestResourceCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r := &Resource{
		ID:         "rsrc_test001",
		Name:       "test-resource",
		Type:       "ssh",
		Host:       "192.168.1.100",
		Port:       22,
		User:       "root",
		RootDir:    "/workspace",
		RemotePath: "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin",
		CondaEnv:   "base",
		Tags:       "test,gpu",
		Status:     ResourceStatusUnknown,
	}

	if err := s.CreateResource(ctx, r); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}

	got, err := s.GetResource(ctx, "rsrc_test001")
	if err != nil || got == nil {
		t.Fatalf("GetResource: %v", err)
	}
	if got.Name != "test-resource" {
		t.Errorf("name = %q, want %q", got.Name, "test-resource")
	}
	if got.RemotePath != "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin" {
		t.Errorf("remote_path = %q", got.RemotePath)
	}

	byName, _ := s.GetResourceByName(ctx, "test-resource")
	if byName == nil || byName.ID != "rsrc_test001" {
		t.Error("GetResourceByName failed")
	}

	resources, _ := s.ListResources(ctx)
	if len(resources) != 1 {
		t.Errorf("len(resources) = %d, want 1", len(resources))
	}

	r.Status = ResourceStatusIdle
	r.RemotePath = "/usr/local/cuda/bin:/usr/bin:/bin"
	now := time.Now()
	r.SSHStatus = ResourceSSHStatusFailed
	r.LastDoctorError = "ssh: EOF"
	r.LastCheckedAt = &now
	s.UpdateResource(ctx, r)
	got2, _ := s.GetResource(ctx, "rsrc_test001")
	if got2.Status != ResourceStatusIdle {
		t.Errorf("status = %q, want %q", got2.Status, ResourceStatusIdle)
	}
	if got2.RemotePath != "/usr/local/cuda/bin:/usr/bin:/bin" {
		t.Errorf("updated remote_path = %q", got2.RemotePath)
	}
	if got2.SSHStatus != ResourceSSHStatusFailed || got2.LastDoctorError != "ssh: EOF" || got2.LastCheckedAt == nil {
		t.Errorf("ssh status fields not persisted: %#v", got2)
	}

	s.DeleteResource(ctx, "rsrc_test001")
	got3, _ := s.GetResource(ctx, "rsrc_test001")
	if got3 != nil {
		t.Error("expected nil after delete")
	}
}

func TestRunCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateResource(ctx, &Resource{ID: "rsrc_r01", Name: "res1", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle})

	run := &Run{
		ID:               "run_test001",
		ResourceID:       "rsrc_r01",
		ProjectID:        "project_test",
		TargetID:         "target_test",
		RecipeName:       "train",
		Name:             "test-run",
		Status:           RunStatusCreated,
		Command:          "python train.py",
		Cwd:              "/ws/project",
		CondaEnv:         "tslib",
		TargetEnv:        "defect-yolo",
		ForceReason:      "preempt stale smoke run before formal rerun",
		PreemptRunID:     "run_old001",
		PreemptSave:      true,
		GitRepoRoot:      "/repo",
		GitRemoteURL:     "https://github.com/example/project.git",
		GitBranch:        "main",
		GitCommit:        "abcdef1234567890",
		GitDirty:         true,
		GitStatus:        " M train.py",
		GitDiffHash:      "sha256:abc",
		GitDiffPath:      "/tmp/run.patch",
		GitAllowDirty:    true,
		FailureKind:      RunFailureImportError,
		FailureReason:    "libxcb.so.1 missing",
		UIEventsPath:     "aexp-events.jsonl",
		StatusSource:     RunStatusSourceRemoteTmux,
		StatusObservedAt: timePtr(time.Now().Add(-5 * time.Second)),
		StatusCheckedAt:  timePtr(time.Now()),
	}

	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	got, _ := s.GetRun(ctx, "run_test001")
	if got == nil || got.Command != "python train.py" {
		t.Error("GetRun failed")
	}
	if got.UIEventsPath != "aexp-events.jsonl" {
		t.Errorf("ui_events_path = %q, want %q", got.UIEventsPath, "aexp-events.jsonl")
	}
	if got.ProjectID != "project_test" || got.TargetID != "target_test" || got.RecipeName != "train" {
		t.Errorf("run project provenance not persisted: %#v", got)
	}
	if got.TargetEnv != "defect-yolo" || got.ForceReason == "" || got.PreemptRunID != "run_old001" || !got.PreemptSave || got.FailureKind != RunFailureImportError || got.FailureReason != "libxcb.so.1 missing" {
		t.Errorf("run semantic fields not persisted: %#v", got)
	}
	if got.GitRepoRoot != "/repo" || got.GitRemoteURL != "https://github.com/example/project.git" || got.GitBranch != "main" || got.GitCommit != "abcdef1234567890" || !got.GitDirty || got.GitStatus != " M train.py" || got.GitDiffHash != "sha256:abc" || got.GitDiffPath != "/tmp/run.patch" || !got.GitAllowDirty {
		t.Errorf("run git fields not persisted: %#v", got)
	}
	if got.StatusSource != RunStatusSourceRemoteTmux || got.StatusObservedAt == nil || got.StatusCheckedAt == nil || got.StatusCheckError != "" || got.StatusFreshness != RunStatusFreshnessFresh {
		t.Errorf("run status observation fields not persisted/derived: %#v", got)
	}

	runs, _ := s.ListRuns(ctx, RunFilter{ResourceID: "rsrc_r01"})
	if len(runs) != 1 {
		t.Errorf("len(runs) = %d, want 1", len(runs))
	}
	runCount, _ := s.CountRuns(ctx, RunFilter{ResourceID: "rsrc_r01"})
	if runCount != 1 {
		t.Errorf("CountRuns = %d, want 1", runCount)
	}
	if runs[0].UIEventsPath != "aexp-events.jsonl" {
		t.Errorf("list ui_events_path = %q, want %q", runs[0].UIEventsPath, "aexp-events.jsonl")
	}

	run.Status = RunStatusRunning
	run.UIEventsPath = "events/train.jsonl"
	run.FailureKind = ""
	run.FailureReason = ""
	run.StartedAt = sql.NullTime{Time: time.Now(), Valid: true}
	s.UpdateRun(ctx, run)
	got2, _ := s.GetRun(ctx, "run_test001")
	if got2.Status != RunStatusRunning {
		t.Errorf("status = %q, want %q", got2.Status, RunStatusRunning)
	}
	if got2.UIEventsPath != "events/train.jsonl" {
		t.Errorf("updated ui_events_path = %q, want %q", got2.UIEventsPath, "events/train.jsonl")
	}
	if got2.TargetEnv != "defect-yolo" || got2.ForceReason == "" || got2.PreemptRunID != "run_old001" || !got2.PreemptSave || got2.FailureKind != "" || got2.FailureReason != "" {
		t.Errorf("updated run semantic fields = target %q force %q preempt %q/%v failure %q/%q", got2.TargetEnv, got2.ForceReason, got2.PreemptRunID, got2.PreemptSave, got2.FailureKind, got2.FailureReason)
	}
}

func TestRunStatusFreshnessAt(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		run  Run
		want string
	}{
		{name: "unknown before first remote observation", run: Run{Status: RunStatusRunning}, want: RunStatusFreshnessUnknown},
		{name: "fresh remote observation", run: Run{Status: RunStatusRunning, StatusObservedAt: timePtr(now.Add(-10 * time.Second))}, want: RunStatusFreshnessFresh},
		{name: "stale by age", run: Run{Status: RunStatusRunning, StatusObservedAt: timePtr(now.Add(-2 * time.Minute))}, want: RunStatusFreshnessStale},
		{name: "stale after failed check", run: Run{Status: RunStatusRunning, StatusObservedAt: timePtr(now.Add(-5 * time.Second)), StatusCheckError: "ssh timeout"}, want: RunStatusFreshnessStale},
		{name: "terminal is final", run: Run{Status: RunStatusSucceeded}, want: RunStatusFreshnessFresh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RunStatusFreshnessAt(&tc.run, now, 45*time.Second); got != tc.want {
				t.Fatalf("RunStatusFreshnessAt() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProjectDefinitionAndTargetsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_target", Name: "target-resource", Type: "ssh", Host: "localhost", RootDir: "/workspace", Status: ResourceStatusIdle}); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	project := &ProjectDefinition{ID: "project_dam", Name: "Dam", LocalRoot: "/src/dam", ConfigPath: "/src/dam/.aexp.yaml", ConfigHash: "sha256:config", DefaultRecipe: "train", ZoteroCollectionKey: "SHUMTSPS", LiteratureServiceProfile: "mu-paperqa"}
	if err := s.SaveProjectDefinition(ctx, project); err != nil {
		t.Fatalf("SaveProjectDefinition: %v", err)
	}
	target := &ProjectTarget{ID: "target_dam_mu", ProjectID: project.ID, Name: "mu", ResourceID: "rsrc_target", Cwd: "/workspace/dam", EnvStrategy: "auto", DesiredEnv: "dam-runtime", DefaultGPU: 0, PrepareCommand: "uv sync", Readiness: TargetReadinessDrifted, ObservedConfigHash: "sha256:old"}
	if err := s.SaveProjectTarget(ctx, target); err != nil {
		t.Fatalf("SaveProjectTarget: %v", err)
	}

	gotProject, err := s.GetProjectDefinition(ctx, project.ID)
	if err != nil || gotProject == nil {
		t.Fatalf("GetProjectDefinition: %v", err)
	}
	if gotProject.ConfigHash != "sha256:config" || gotProject.DefaultRecipe != "train" {
		t.Fatalf("project fields lost: %#v", gotProject)
	}
	if gotProject.ZoteroCollectionKey != "SHUMTSPS" || gotProject.LiteratureServiceProfile != "mu-paperqa" {
		t.Fatalf("literature binding lost: %#v", gotProject)
	}
	targets, err := s.ListProjectTargets(ctx, project.ID)
	if err != nil || len(targets) != 1 {
		t.Fatalf("ListProjectTargets: len=%d err=%v", len(targets), err)
	}
	if targets[0].DesiredEnv != "dam-runtime" || targets[0].PrepareCommand != "uv sync" || targets[0].Readiness != TargetReadinessDrifted {
		t.Fatalf("target fields lost: %#v", targets[0])
	}

	target.Readiness = TargetReadinessReady
	now := time.Now()
	target.LastPreparedAt = &now
	target.LastPrepareRunID = "run_prepare"
	target.ObservedConfigHash = project.ConfigHash
	if err := s.SaveProjectTarget(ctx, target); err != nil {
		t.Fatalf("update target: %v", err)
	}
	gotTarget, err := s.GetProjectTarget(ctx, target.ID)
	if err != nil || gotTarget == nil {
		t.Fatalf("GetProjectTarget: %v", err)
	}
	if gotTarget.Readiness != TargetReadinessReady || gotTarget.LastPreparedAt == nil || gotTarget.LastPrepareRunID != "run_prepare" {
		t.Fatalf("updated target fields lost: %#v", gotTarget)
	}
	gotTarget.Readiness = TargetReadinessUnknown
	if err := s.SaveProjectTarget(ctx, gotTarget); err != nil {
		t.Fatal(err)
	}
	acquired, err := s.BeginProjectTargetPrepare(ctx, gotTarget.ID, time.Now())
	if err != nil || !acquired {
		t.Fatalf("first prepare lock acquired=%v err=%v", acquired, err)
	}
	acquired, err = s.BeginProjectTargetPrepare(ctx, gotTarget.ID, time.Now())
	if err != nil || acquired {
		t.Fatalf("duplicate prepare lock acquired=%v err=%v", acquired, err)
	}

	projects, err := s.ListProjectDefinitions(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("ListProjectDefinitions: len=%d err=%v", len(projects), err)
	}
}

func TestRunManifestFinalIsImmutableAndArtifactInventoryRoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_manifest", Name: "manifest-res", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_manifest", ResourceID: "rsrc_manifest", Status: RunStatusSucceeded, Kind: RunKindFormal, Command: "python train.py"}); err != nil {
		t.Fatal(err)
	}
	draft := &RunManifest{RunID: "run_manifest", SchemaVersion: 1, State: RunManifestDraft, ManifestJSON: `{"status":"running"}`, SHA256: "sha256:draft", Completeness: RunManifestCompletenessCurrent}
	if err := s.SaveRunManifest(ctx, draft); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	now := time.Now()
	final := &RunManifest{RunID: draft.RunID, SchemaVersion: 1, State: RunManifestFinal, ManifestJSON: `{"status":"succeeded"}`, SHA256: "sha256:final", Completeness: RunManifestCompletenessCurrent, CreatedAt: draft.CreatedAt, FinalizedAt: &now}
	if err := s.SaveRunManifest(ctx, final); err != nil {
		t.Fatalf("save final: %v", err)
	}
	if err := s.SaveRunManifest(ctx, &RunManifest{RunID: final.RunID, SchemaVersion: 1, State: RunManifestFinal, ManifestJSON: `{}`, SHA256: "sha256:changed"}); err == nil {
		t.Fatal("expected finalized manifest mutation to fail")
	}
	gotManifest, err := s.GetRunManifest(ctx, final.RunID)
	if err != nil || gotManifest == nil || gotManifest.SHA256 != "sha256:final" || gotManifest.FinalizedAt == nil {
		t.Fatalf("manifest roundtrip: %#v err=%v", gotManifest, err)
	}

	discovered := time.Now()
	artifacts := []Artifact{{ID: "artifact_one", RunID: final.RunID, Path: "/ws/results/report.json", RelativePath: "results/report.json", SourceURI: "ssh://rsrc_manifest/ws/results/report.json", Type: "file", Role: "report", Mime: "application/json", Size: 42, SHA256: "sha256:file", CollectionState: ArtifactCollectionIndexed, DiscoveredAt: discovered, ModifiedAt: discovered}}
	if err := s.SaveArtifacts(ctx, final.RunID, artifacts); err != nil {
		t.Fatalf("SaveArtifacts: %v", err)
	}
	gotArtifacts, err := s.ListArtifacts(ctx, final.RunID)
	if err != nil || len(gotArtifacts) != 1 || gotArtifacts[0].RelativePath != "results/report.json" || gotArtifacts[0].SHA256 != "sha256:file" {
		t.Fatalf("artifact roundtrip: %#v err=%v", gotArtifacts, err)
	}
	collection := &ArtifactCollection{RunID: final.RunID, State: ArtifactCollectionIndexed, FileCount: 1, TotalBytes: 42, StartedAt: &discovered, FinishedAt: &discovered}
	if err := s.SaveArtifactCollection(ctx, collection); err != nil {
		t.Fatalf("SaveArtifactCollection: %v", err)
	}
	gotCollection, err := s.GetArtifactCollection(ctx, final.RunID)
	if err != nil || gotCollection == nil || gotCollection.State != ArtifactCollectionIndexed || gotCollection.FileCount != 1 {
		t.Fatalf("artifact collection roundtrip: %#v err=%v", gotCollection, err)
	}
}

func TestEvidenceSnapshotReferencesPublishedOutputsAndIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_snapshot", Name: "snapshot-res", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProjectDefinition(ctx, &ProjectDefinition{ID: "project_snapshot", Name: "Snapshot Project"}); err != nil {
		t.Fatal(err)
	}
	run := &Run{
		ID:                    "run_snapshot",
		ResourceID:            "rsrc_snapshot",
		ProjectID:             "project_snapshot",
		Status:                RunStatusSucceeded,
		Kind:                  RunKindFormal,
		EvidenceGrade:         RunEvidenceGradeFormal,
		DataFinalizationState: RunDataFinalizationCompleted,
		Command:               "python train.py",
	}
	if err := s.CreateRunWithBindings(ctx, run, RunBindings{Outputs: []RunOutputBinding{{
		SourcePattern: "results/metrics.json",
		LogicalURI:    "aexp://project_snapshot/runs/run_snapshot/metrics.json",
		Role:          "metrics",
		Required:      true,
	}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.SaveRunManifest(ctx, &RunManifest{
		RunID: "run_snapshot", SchemaVersion: 1, State: RunManifestFinal,
		ManifestJSON: `{"status":"succeeded"}`, SHA256: "sha256:run-manifest",
		Completeness: RunManifestCompletenessCurrent, FinalizedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.CreateEvidenceSnapshot(ctx, run.ID); err == nil {
		t.Fatal("expected unpublished output to block snapshot")
	} else {
		var blocked *EvidenceSnapshotBlockedError
		if !errors.As(err, &blocked) || len(blocked.Blockers) == 0 || blocked.Blockers[0].Code != "required_output_unpublished" {
			t.Fatalf("unexpected blocker: %#v %v", blocked, err)
		}
	}
	outputs, err := s.ListRunOutputBindings(ctx, run.ID)
	if err != nil || len(outputs) != 1 {
		t.Fatalf("ListRunOutputBindings=%#v err=%v", outputs, err)
	}
	outputs[0].State = RunBindingPublished
	outputs[0].Revision = "sha256:output-v1"
	outputs[0].PublishedAt = &now
	if err := s.UpdateRunOutputBinding(ctx, &outputs[0]); err != nil {
		t.Fatal(err)
	}

	first, created, err := s.CreateEvidenceSnapshot(ctx, run.ID)
	if err != nil || !created {
		t.Fatalf("first snapshot=%#v created=%v err=%v", first, created, err)
	}
	second, created, err := s.CreateEvidenceSnapshot(ctx, run.ID)
	if err != nil || created || second.ID != first.ID || second.ManifestSHA256 != first.ManifestSHA256 {
		t.Fatalf("idempotent snapshot=%#v created=%v err=%v", second, created, err)
	}

	outputs[0].Revision = "sha256:output-v2"
	if err := s.UpdateRunOutputBinding(ctx, &outputs[0]); err != nil {
		t.Fatal(err)
	}
	third, created, err := s.CreateEvidenceSnapshot(ctx, run.ID)
	if err != nil || !created || third.ID == first.ID {
		t.Fatalf("changed output snapshot=%#v created=%v err=%v", third, created, err)
	}
	got, err := s.GetEvidenceSnapshot(ctx, first.ID)
	if err != nil || got == nil || !strings.Contains(got.ManifestJSON, "sha256:output-v1") {
		t.Fatalf("original snapshot mutated: %#v err=%v", got, err)
	}
	listed, err := s.ListEvidenceSnapshots(ctx, run.ID)
	if err != nil || len(listed) != 2 {
		t.Fatalf("snapshot list=%#v err=%v", listed, err)
	}
}

func TestListProjectAssetsIncludesVerifiedInputsAndPublishedOutputs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_assets", Name: "assets-res", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProjectDefinition(ctx, &ProjectDefinition{ID: "project_assets", Name: "Assets Project"}); err != nil {
		t.Fatal(err)
	}
	target := &StorageTarget{ID: "storage_assets", Name: "assets-storage", ResourceID: "rsrc_assets", RootPath: "/ws/assets"}
	if err := s.SaveStorageTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveDatasetVersion(ctx, &DatasetVersion{
		ID: "dataset_assets", DatasetID: "example", Version: "v1", StorageTargetID: target.ID,
		StoragePath: "datasets/example/v1",
		LogicalURI:  "aexp://project_assets/datasets/example/versions/v1/content",
		Revision:    "sha256:dataset", ManifestSHA256: "sha256:dataset", State: DatasetStateVerified,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRunWithBindings(ctx, &Run{
		ID: "run_assets", ResourceID: "rsrc_assets", ProjectID: "project_assets",
		Status: RunStatusSucceeded, Kind: RunKindFormal, Command: "python train.py",
	}, RunBindings{
		Inputs: []RunInputBinding{{
			LogicalURI: "storage://nas/datasets/example/v1", Revision: "sha256:input",
			State: RunBindingReady, VerifiedAt: &now,
		}},
		Outputs: []RunOutputBinding{{
			LogicalURI: "storage://nas/projects/example/run_assets/metrics.json", Revision: "sha256:output",
			Role: "metrics", State: RunBindingPublished, PublishedAt: &now,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	assets, total, err := s.ListProjectAssets(ctx, "project_assets", 50, 0)
	if err != nil {
		t.Fatalf("ListProjectAssets: %v", err)
	}
	if total != 3 || len(assets) != 3 {
		t.Fatalf("assets=%#v total=%d, want dataset, input and output", assets, total)
	}
	roles := map[string]bool{}
	for _, asset := range assets {
		roles[asset.Role] = true
	}
	if !roles["dataset"] || !roles["input"] || !roles["metrics"] {
		t.Fatalf("asset roles=%#v, want dataset, input and metrics", roles)
	}
}

func TestUpdateRunIfStatusDoesNotOverwriteNewerCancellation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_cas", Name: "cas-res", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle}); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	run := &Run{ID: "run_cas", ResourceID: "rsrc_cas", Name: "cas-run", Status: RunStatusRunning, Command: "sleep 10"}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	staleProbe, err := s.GetRun(ctx, run.ID)
	if err != nil || staleProbe == nil {
		t.Fatalf("GetRun stale probe: %v", err)
	}
	cancelled, err := s.GetRun(ctx, run.ID)
	if err != nil || cancelled == nil {
		t.Fatalf("GetRun cancellation: %v", err)
	}
	cancelled.Status = RunStatusCancelled
	if err := s.UpdateRun(ctx, cancelled); err != nil {
		t.Fatalf("UpdateRun cancellation: %v", err)
	}

	staleProbe.Status = RunStatusSucceeded
	updated, err := s.UpdateRunIfStatus(ctx, staleProbe, RunStatusRunning)
	if err != nil {
		t.Fatalf("UpdateRunIfStatus: %v", err)
	}
	if updated {
		t.Fatal("stale probe unexpectedly overwrote a newer cancellation")
	}
	got, err := s.GetRun(ctx, run.ID)
	if err != nil || got == nil {
		t.Fatalf("GetRun final: %v", err)
	}
	if got.Status != RunStatusCancelled {
		t.Fatalf("final status = %q, want %q", got.Status, RunStatusCancelled)
	}
}

func timePtr(value time.Time) *time.Time { return &value }

func TestRunTrashLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateResource(ctx, &Resource{ID: "rsrc_trash", Name: "trash-res", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle})
	for _, run := range []*Run{
		{ID: "run_keep", ResourceID: "rsrc_trash", Status: RunStatusSucceeded, Command: "keep"},
		{ID: "run_trash", ResourceID: "rsrc_trash", Status: RunStatusSucceeded, Command: "trash"},
	} {
		if err := s.CreateRun(ctx, run); err != nil {
			t.Fatalf("CreateRun(%s): %v", run.ID, err)
		}
	}

	if err := s.ArchiveRun(ctx, "run_trash"); err != nil {
		t.Fatalf("ArchiveRun: %v", err)
	}
	visible, _ := s.ListRuns(ctx, RunFilter{})
	if len(visible) != 1 || visible[0].ID != "run_keep" {
		t.Fatalf("visible runs = %#v, want run_keep only", visible)
	}
	trash, _ := s.ListRuns(ctx, RunFilter{Trash: true})
	if len(trash) != 1 || trash[0].ID != "run_trash" || !trash[0].ArchivedAt.Valid {
		t.Fatalf("trash runs = %#v, want archived run_trash", trash)
	}

	if err := s.RestoreRun(ctx, "run_trash"); err != nil {
		t.Fatalf("RestoreRun: %v", err)
	}
	visible, _ = s.ListRuns(ctx, RunFilter{})
	if len(visible) != 2 {
		t.Fatalf("visible after restore = %d, want 2", len(visible))
	}

	if err := s.ArchiveRun(ctx, "run_trash"); err != nil {
		t.Fatalf("ArchiveRun again: %v", err)
	}
	if err := s.DeleteRunLogically(ctx, "run_trash"); err != nil {
		t.Fatalf("DeleteRunLogically: %v", err)
	}
	trash, _ = s.ListRuns(ctx, RunFilter{Trash: true})
	if len(trash) != 0 {
		t.Fatalf("trash after delete = %#v, want empty", trash)
	}
	deleted, _ := s.ListRuns(ctx, RunFilter{Deleted: true})
	if len(deleted) != 1 || deleted[0].ID != "run_trash" || !deleted[0].DeletedAt.Valid {
		t.Fatalf("deleted runs = %#v, want deleted run_trash", deleted)
	}
}

func TestSnapshotAndLogs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateResource(ctx, &Resource{ID: "rsrc_s01", Name: "res-snap", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle})

	snap := &Snapshot{ResourceID: "rsrc_s01", CPUPercent: 55.5, MemUsedMB: 12000, MemTotalMB: 64000, GPUJSON: `[{"index":0,"util":45}]`}
	s.SaveSnapshot(ctx, snap)
	got, _ := s.GetLatestSnapshot(ctx, "rsrc_s01")
	if got == nil || got.CPUPercent != 55.5 {
		t.Error("GetLatestSnapshot failed")
	}

	s.CreateRun(ctx, &Run{ID: "run_log01", ResourceID: "rsrc_s01", Status: RunStatusRunning, Command: "train"})

	lines := []LogLine{
		{RunID: "run_log01", Source: "stdout", LineNo: 1, Content: "Epoch 1"},
		{RunID: "run_log01", Source: "stdout", LineNo: 2, Content: "Epoch 2"},
		{RunID: "run_log01", Source: "stderr", LineNo: 1, Content: "warning"},
	}
	s.AppendLogLines(ctx, "run_log01", lines)

	stdout, _ := s.GetLogLines(ctx, "run_log01", "stdout", 0, 100)
	if len(stdout) != 2 {
		t.Errorf("stdout lines = %d, want 2", len(stdout))
	}

	count, _ := s.CountLogLines(ctx, "run_log01", "")
	if count != 3 {
		t.Errorf("total log count = %d, want 3", count)
	}
}

func TestAgentEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateResource(ctx, &Resource{ID: "rsrc_e01", Name: "res-evt", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle})
	s.CreateRun(ctx, &Run{ID: "run_evt01", ResourceID: "rsrc_e01", Status: RunStatusRunning, Command: "train"})

	e := &AgentEvent{
		RunID:      "run_evt01",
		Actor:      "agent_thread_abc",
		ToolName:   "create_run",
		InputJSON:  `{"command":"python train.py"}`,
		OutputJSON: `{"run_id":"run_evt01"}`,
	}
	s.SaveAgentEvent(ctx, e)

	events, _ := s.ListAgentEvents(ctx, "run_evt01")
	if len(events) != 1 {
		t.Errorf("events = %d, want 1", len(events))
	}
	if events[0].ToolName != "create_run" {
		t.Errorf("tool_name = %q, want %q", events[0].ToolName, "create_run")
	}
}

func TestRunMarks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateResource(ctx, &Resource{ID: "rsrc_m01", Name: "res-mark", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle})
	s.CreateRun(ctx, &Run{ID: "run_mark01", ResourceID: "rsrc_m01", Status: RunStatusSucceeded, Command: "python train.py"})

	mark := &RunMark{
		ID:        "mark_test001",
		RunID:     "run_mark01",
		Actor:     "agent",
		Kind:      "key_result",
		Title:     "Useful ablation",
		Statement: "Validation loss improved.",
		BodyMD:    "## Result\n\nValidation loss improved.\n\n![plot](aexp-attachment://att_test001)",
		Reason:    "Validation loss improved.",
		Evidence:  "logs/train.log",
	}
	if err := s.SaveRunMark(ctx, mark); err != nil {
		t.Fatalf("SaveRunMark: %v", err)
	}
	if err := s.SaveRunMarkAttachments(ctx, mark.ID, []RunMarkAttachment{{
		ID:        "att_test001",
		MarkID:    mark.ID,
		Filename:  "plot.png",
		LocalPath: "/tmp/plot.png",
		Mime:      "image/png",
		Caption:   "plot",
		Size:      123,
	}}); err != nil {
		t.Fatalf("SaveRunMarkAttachments: %v", err)
	}
	if err := s.SaveRunMark(ctx, &RunMark{
		ID:     "mark_test002",
		RunID:  "run_mark01",
		Actor:  "agent",
		Kind:   "followup",
		Title:  "Try stricter seed control",
		Reason: "Variance is still unclear.",
	}); err != nil {
		t.Fatalf("SaveRunMark followup: %v", err)
	}

	got, err := s.GetRunMark(ctx, "mark_test001")
	if err != nil || got == nil {
		t.Fatalf("GetRunMark: %v", err)
	}
	if got.Title != "Useful ablation" {
		t.Errorf("title = %q, want %q", got.Title, "Useful ablation")
	}
	if got.Statement != "Validation loss improved." || got.BodyMD == "" {
		t.Errorf("markdown fields not preserved: %#v", got)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].ID != "att_test001" {
		t.Errorf("attachments = %#v, want att_test001", got.Attachments)
	}
	attachment, err := s.GetRunMarkAttachment(ctx, mark.ID, "att_test001")
	if err != nil {
		t.Fatalf("GetRunMarkAttachment: %v", err)
	}
	if attachment == nil || attachment.LocalPath != "/tmp/plot.png" {
		t.Errorf("attachment = %#v, want /tmp/plot.png", attachment)
	}

	marks, err := s.ListRunMarks(ctx, RunMarkFilter{RunID: "run_mark01", Limit: 10})
	if err != nil {
		t.Fatalf("ListRunMarks: %v", err)
	}
	if len(marks) != 2 {
		t.Errorf("marks = %d, want 2", len(marks))
	}
	batchMarks, err := s.ListRunMarks(ctx, RunMarkFilter{RunIDs: []string{"run_mark01", "run_missing"}, Limit: 10})
	if err != nil {
		t.Fatalf("ListRunMarks batch: %v", err)
	}
	if len(batchMarks) != 2 {
		t.Errorf("batch marks = %d, want 2", len(batchMarks))
	}

	keyResults, err := s.ListRunMarks(ctx, RunMarkFilter{Kind: "key_result", Limit: 10})
	if err != nil {
		t.Fatalf("ListRunMarks kind: %v", err)
	}
	if len(keyResults) != 1 || keyResults[0].ID != "mark_test001" {
		t.Errorf("key result filter = %#v, want mark_test001", keyResults)
	}
}

func TestProjectRunCards(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createTestProject(t, s, "dam-imputation", "Dam Imputation")

	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_card01", Name: "res-card", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle}); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_card01", ResourceID: "rsrc_card01", Status: RunStatusSucceeded, Kind: RunKindFormal, Command: "python train.py"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	card := &ProjectRunCard{
		ID:            "card_test001",
		ProjectID:     "dam-imputation",
		ProjectName:   "Dam Imputation",
		RunID:         "run_card01",
		Question:      "Does CAF beat IR?",
		Verdict:       "CAF improves mAP50-95.",
		EvidenceLevel: "B",
		KeyMetrics:    "mAP50-95=0.606",
		Important:     true,
	}
	if err := s.SaveProjectRunCard(ctx, card); err != nil {
		t.Fatalf("SaveProjectRunCard: %v", err)
	}

	got, err := s.GetProjectRunCard(ctx, "run_card01")
	if err != nil || got == nil {
		t.Fatalf("GetProjectRunCard: %v", err)
	}
	if got.ProjectID != "dam-imputation" || got.EvidenceLevel != "B" || !got.Important {
		t.Fatalf("unexpected card: %#v", got)
	}
	importantRuns, err := s.ListRunSummaries(ctx, RunFilter{ImportantOnly: true})
	if err != nil || len(importantRuns) != 1 || importantRuns[0].ID != "run_card01" {
		t.Fatalf("important run summaries=%#v err=%v", importantRuns, err)
	}

	got.Verdict = "Needs rerun with seed control."
	got.EvidenceLevel = "C"
	got.Important = false
	if err := s.SaveProjectRunCard(ctx, got); err != nil {
		t.Fatalf("SaveProjectRunCard upsert: %v", err)
	}
	updated, _ := s.GetProjectRunCard(ctx, "run_card01")
	if updated.ID != "card_test001" {
		t.Fatalf("upsert should keep original id, got %q", updated.ID)
	}
	if updated.Verdict != "Needs rerun with seed control." || updated.Important {
		t.Fatalf("unexpected updated card: %#v", updated)
	}

	cards, err := s.ListProjectRunCards(ctx, ProjectRunCardFilter{ProjectID: "dam-imputation", Limit: 10})
	if err != nil {
		t.Fatalf("ListProjectRunCards: %v", err)
	}
	if len(cards) != 1 || cards[0].RunID != "run_card01" {
		t.Fatalf("cards = %#v, want run_card01", cards)
	}
	important, err := s.ListProjectRunCards(ctx, ProjectRunCardFilter{ProjectID: "dam-imputation", ImportantOnly: true})
	if err != nil {
		t.Fatalf("ListProjectRunCards important: %v", err)
	}
	if len(important) != 0 {
		t.Fatalf("important cards = %#v, want empty after update", important)
	}
	importantRuns, err = s.ListRunSummaries(ctx, RunFilter{ImportantOnly: true})
	if err != nil || len(importantRuns) != 0 {
		t.Fatalf("important run summaries after update=%#v err=%v", importantRuns, err)
	}
}

func TestProjectRunCardInsertRequiresRegisteredProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_card_project_guard", Name: "card-project-guard", Type: "ssh", Host: "localhost", RootDir: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_card_project_guard", ResourceID: "rsrc_card_project_guard", Status: RunStatusSucceeded, Kind: RunKindPilot, Command: "true"}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		projectID string
		code      string
	}{
		{name: "missing id", code: "PROJECT_ID_REQUIRED"},
		{name: "unknown project", projectID: "project-missing", code: "PROJECT_NOT_FOUND"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.SaveProjectRunCard(ctx, &ProjectRunCard{
				ID:        "card_project_guard_" + strings.ReplaceAll(tc.name, " ", "_"),
				RunID:     "run_card_project_guard",
				ProjectID: tc.projectID,
			})
			var validation *EvidenceGraphValidationError
			if !errors.As(err, &validation) || validation.Code != tc.code {
				t.Fatalf("SaveProjectRunCard error = %#v, want %s", err, tc.code)
			}
		})
	}

	createTestProject(t, s, "project-registered", "Registered Project")
	if err := s.SaveProjectRunCard(ctx, &ProjectRunCard{
		ID:          "card_project_guard_registered",
		RunID:       "run_card_project_guard",
		ProjectID:   "project-registered",
		ProjectName: "Registered Project",
	}); err != nil {
		t.Fatalf("SaveProjectRunCard registered project: %v", err)
	}
}

func TestProjectRunCardUpdateRequiresCurrentRevision(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createTestProject(t, s, "project-a", "Project A")
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_card_cas", Name: "card-cas", Type: "ssh", Host: "localhost", RootDir: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_card_cas", ResourceID: "rsrc_card_cas", Status: RunStatusSucceeded, Kind: RunKindPilot, Command: "true"}); err != nil {
		t.Fatal(err)
	}
	card := &ProjectRunCard{
		ID: "card_cas", ProjectID: "project-a", ProjectName: "Project A",
		RunID: "run_card_cas", Verdict: "first",
	}
	if err := s.SaveProjectRunCard(ctx, card); err != nil {
		t.Fatal(err)
	}
	stale := *card
	card.Verdict = "second"
	if err := s.SaveProjectRunCard(ctx, card); err != nil {
		t.Fatal(err)
	}
	stale.Verdict = "stale"
	err := s.SaveProjectRunCard(ctx, &stale)
	var conflict *ProjectRunCardRevisionConflict
	if !errors.As(err, &conflict) || conflict.RunID != card.RunID || conflict.Current.IsZero() {
		t.Fatalf("stale update error = %#v", err)
	}
	missing := *card
	missing.UpdatedAt = time.Time{}
	missing.Verdict = "missing revision"
	err = s.SaveProjectRunCard(ctx, &missing)
	if !errors.As(err, &conflict) {
		t.Fatalf("missing revision update error = %#v", err)
	}
	current, getErr := s.GetProjectRunCard(ctx, card.RunID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if current.Verdict != "second" {
		t.Fatalf("stale writer changed card: %#v", current)
	}
}

func TestProjectRunCardProjectOwnershipChangesOnlyThroughExplicitReassign(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, project := range []*ProjectDefinition{
		{ID: "project-a", Name: "Project A"},
		{ID: "project-b", Name: "Project B"},
	} {
		if err := s.CreateProjectDefinition(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_card_owner", Name: "card-owner", Type: "ssh", Host: "localhost", RootDir: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_card_owner", ResourceID: "rsrc_card_owner", ProjectID: "project-a", Status: RunStatusSucceeded, Kind: RunKindPilot, Command: "true"}); err != nil {
		t.Fatal(err)
	}
	card := &ProjectRunCard{
		ID: "card_owner", ProjectID: "project-a", ProjectName: "Project A",
		RunID: "run_card_owner", Verdict: "first",
	}
	if err := s.SaveProjectRunCard(ctx, card); err != nil {
		t.Fatal(err)
	}
	card.ProjectID, card.ProjectName, card.Verdict = "project-b", "Project B", "ordinary update"
	if err := s.SaveProjectRunCard(ctx, card); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetProjectRunCard(ctx, card.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if current.ProjectID != "project-a" {
		t.Fatalf("ordinary update changed ownership: %#v", current)
	}
	current, err = s.ReassignProjectRunCard(ctx, card.RunID, "project-b", "", current.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if current.ProjectID != "project-b" || current.ProjectName != "Project B" {
		t.Fatalf("explicit reassign failed: %#v", current)
	}
}

func TestAssignRunProjectIsNarrowAuditedAndConflictSafe(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createTestProject(t, s, "project-assign-a", "Project A")
	createTestProject(t, s, "project-assign-b", "Project B")
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_assign", Name: "assign", Type: "ssh", Host: "localhost", RootDir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, &Run{
		ID: "run_assign", ResourceID: "rsrc_assign", Status: RunStatusSucceeded,
		Kind: RunKindPilot, Name: "historical", Command: "python train.py",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProjectRunCard(ctx, &ProjectRunCard{
		ID: "card_assign", RunID: "run_assign", ProjectID: "project-assign-a", ProjectName: "Project A",
	}); err != nil {
		t.Fatal(err)
	}

	result, err := s.AssignRunProject(ctx, "run_assign", "project-assign-b", "", "test-agent", "repair historical ownership")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.PreviousProjectID != "" || result.ProjectID != "project-assign-b" || result.Run == nil || result.Run.ProjectID != "project-assign-b" {
		t.Fatalf("assignment result = %#v", result)
	}
	if result.Run.Name != "historical" || result.Run.Status != RunStatusSucceeded || result.Run.Command != "python train.py" {
		t.Fatalf("narrow assignment changed Run facts: %#v", result.Run)
	}
	card, err := s.GetProjectRunCard(ctx, "run_assign")
	if err != nil {
		t.Fatal(err)
	}
	if card.ProjectID != "project-assign-b" || card.ProjectName != "Project B" {
		t.Fatalf("project card projection was not synchronized: %#v", card)
	}
	events, err := s.ListAgentEvents(ctx, "run_assign")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ToolName != "assign_run_project" || events[0].Actor != "test-agent" || !strings.Contains(events[0].InputJSON, "repair historical ownership") {
		t.Fatalf("assignment audit = %#v", events)
	}

	if _, err := s.AssignRunProject(ctx, "run_assign", "project-assign-a", "", "stale-agent", "stale write"); err == nil {
		t.Fatal("expected stale assignment conflict")
	} else {
		var conflict *RunProjectAssignmentConflict
		if !errors.As(err, &conflict) || conflict.CurrentProjectID != "project-assign-b" {
			t.Fatalf("conflict = %#v, err=%v", conflict, err)
		}
	}
	idempotent, err := s.AssignRunProject(ctx, "run_assign", "project-assign-b", "project-assign-b", "test-agent", "")
	if err != nil || idempotent.Changed {
		t.Fatalf("idempotent assignment = %#v err=%v", idempotent, err)
	}
	events, _ = s.ListAgentEvents(ctx, "run_assign")
	if len(events) != 1 {
		t.Fatalf("idempotent assignment wrote an audit event: %#v", events)
	}
}

func TestAssignRunProjectRejectsActiveAndUnknownTargets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createTestProject(t, s, "project-active", "Active Project")
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_active_assign", Name: "active", Type: "ssh", Host: "localhost", RootDir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_active_assign", ResourceID: "rsrc_active_assign", ProjectID: "project-active", Status: RunStatusRunning, Kind: RunKindPilot, Command: "python train.py"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssignRunProject(ctx, "run_active_assign", "project-missing", "project-active", "test", ""); err == nil {
		t.Fatal("expected unknown Project error")
	} else {
		var validation *EvidenceGraphValidationError
		if !errors.As(err, &validation) || validation.Code != "PROJECT_NOT_FOUND" {
			t.Fatalf("unknown Project error = %v", err)
		}
	}
	createTestProject(t, s, "project-other", "Other")
	if _, err := s.AssignRunProject(ctx, "run_active_assign", "project-other", "project-active", "test", ""); err == nil {
		t.Fatal("expected active Run blocker")
	} else {
		var validation *EvidenceGraphValidationError
		if !errors.As(err, &validation) || validation.Code != "RUN_ACTIVE" {
			t.Fatalf("active Run error = %v", err)
		}
	}
	run, _ := s.GetRun(ctx, "run_active_assign")
	if run.ProjectID != "project-active" {
		t.Fatalf("failed assignment changed Run: %#v", run)
	}
}

func TestDeleteProjectDefinitionRefusesReferencedProjectWithoutPartialCleanup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := &ProjectDefinition{ID: "project_delete_guard", Name: "Delete Guard"}
	if err := s.CreateProjectDefinition(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_delete_guard", Name: "delete-guard", Type: "ssh", Host: "localhost", RootDir: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProjectTarget(ctx, &ProjectTarget{ID: "target_delete_guard", ProjectID: project.ID, ResourceID: "rsrc_delete_guard", Cwd: "/tmp/project"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_delete_guard", ResourceID: "rsrc_delete_guard", ProjectID: project.ID, Status: RunStatusSucceeded, Command: "true"}); err != nil {
		t.Fatal(err)
	}
	err := s.DeleteProjectDefinition(ctx, project.ID)
	var inUse *ProjectInUseError
	if !errors.As(err, &inUse) || inUse.Counts["runs"] != 1 || inUse.Counts["maps"] == 0 {
		t.Fatalf("delete error = %#v", err)
	}
	if got, err := s.GetProjectDefinition(ctx, project.ID); err != nil || got == nil {
		t.Fatalf("project was partially deleted: got=%#v err=%v", got, err)
	}
	if target, err := s.GetProjectTarget(ctx, "target_delete_guard"); err != nil || target == nil {
		t.Fatalf("target was partially deleted: target=%#v err=%v", target, err)
	}
}

func TestRunProjectScopeFiltersBeforePagination(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createTestProject(t, s, "project-a", "Project A")

	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_project_scope", Name: "project-scope", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle}); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	for _, run := range []*Run{
		{ID: "run_project_scope_native", ResourceID: "rsrc_project_scope", ProjectID: "project-a", Status: RunStatusSucceeded, Kind: RunKindFormal, Command: "python native.py"},
		{ID: "run_project_scope_card", ResourceID: "rsrc_project_scope", Status: RunStatusSucceeded, Kind: RunKindFormal, Command: "python card.py"},
		{ID: "run_project_scope_manual", ResourceID: "rsrc_project_scope", Status: RunStatusSucceeded, Kind: RunKindFormal, Command: "python manual.py"},
		{ID: "run_project_scope_other", ResourceID: "rsrc_project_scope", Status: RunStatusSucceeded, Kind: RunKindFormal, Command: "python other.py"},
	} {
		if err := s.CreateRun(ctx, run); err != nil {
			t.Fatalf("CreateRun(%s): %v", run.ID, err)
		}
	}
	if err := s.SaveProjectRunCard(ctx, &ProjectRunCard{
		ID:          "card_project_scope",
		ProjectID:   "project-a",
		ProjectName: "Project A",
		RunID:       "run_project_scope_card",
	}); err != nil {
		t.Fatalf("SaveProjectRunCard: %v", err)
	}
	if err := s.CreateManualProjectCategory(ctx, &ManualProjectCategory{ID: "manual_project_a", Name: "Project A"}); err != nil {
		t.Fatalf("CreateManualProjectCategory: %v", err)
	}
	if err := s.AssignRunToManualProjectCategory(ctx, "run_project_scope_manual", "manual_project_a"); err != nil {
		t.Fatalf("AssignRunToManualProjectCategory: %v", err)
	}

	filter := RunFilter{ProjectScopeID: "project-a", Limit: 2}
	firstPage, err := s.ListRunSummaries(ctx, filter)
	if err != nil {
		t.Fatalf("ListRunSummaries first page: %v", err)
	}
	total, err := s.CountRuns(ctx, filter)
	if err != nil {
		t.Fatalf("CountRuns: %v", err)
	}
	if total != 2 || len(firstPage) != 2 {
		t.Fatalf("first page len=%d total=%d, want len=2 total=2 canonical assignments", len(firstPage), total)
	}
	filter.Offset = 2
	secondPage, err := s.ListRunSummaries(ctx, filter)
	if err != nil {
		t.Fatalf("ListRunSummaries second page: %v", err)
	}
	if len(secondPage) != 0 {
		t.Fatalf("second page len=%d, want 0", len(secondPage))
	}
	seen := map[string]bool{}
	for _, summary := range append(firstPage, secondPage...) {
		seen[summary.ID] = true
	}
	for _, runID := range []string{"run_project_scope_native", "run_project_scope_card"} {
		if !seen[runID] {
			t.Errorf("project-scoped pagination omitted %s: %#v", runID, seen)
		}
	}
	if seen["run_project_scope_other"] || seen["run_project_scope_manual"] {
		t.Fatalf("project-scoped pagination included non-canonical assignment: %#v", seen)
	}
}

func TestRunQueryAndKindFiltersBeforePagination(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_filtered_runs", Name: "filtered-runs", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle}); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	for _, run := range []*Run{
		{ID: "run_filter_formal_match", ResourceID: "rsrc_filtered_runs", Name: "Good 810 baseline", Status: RunStatusSucceeded, Kind: RunKindFormal, Command: "python train.py"},
		{ID: "run_filter_formal_other", ResourceID: "rsrc_filtered_runs", Name: "Old baseline", Status: RunStatusSucceeded, Kind: RunKindFormal, Command: "python train.py"},
		{ID: "run_filter_smoke_match", ResourceID: "rsrc_filtered_runs", Name: "Good 810 smoke", Status: RunStatusSucceeded, Kind: RunKindSmoke, Command: "python smoke.py"},
	} {
		if err := s.CreateRun(ctx, run); err != nil {
			t.Fatalf("CreateRun(%s): %v", run.ID, err)
		}
	}

	filter := RunFilter{Query: "good 810", KindGroup: "experiments", Limit: 1}
	rows, err := s.ListRunSummaries(ctx, filter)
	if err != nil {
		t.Fatalf("ListRunSummaries: %v", err)
	}
	total, err := s.CountRuns(ctx, filter)
	if err != nil {
		t.Fatalf("CountRuns: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != "run_filter_formal_match" {
		t.Fatalf("rows=%#v total=%d, want only formal match", rows, total)
	}

	filter.KindGroup = "tools"
	rows, err = s.ListRunSummaries(ctx, filter)
	if err != nil {
		t.Fatalf("ListRunSummaries tools: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "run_filter_smoke_match" {
		t.Fatalf("tool rows=%#v, want smoke match", rows)
	}
}

func TestManualProjectCategoriesAndAssignments(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_manual_project", Name: "res-manual-project", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle}); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_manual_project", ResourceID: "rsrc_manual_project", Status: RunStatusSucceeded, Kind: RunKindAblation, Command: "python train.py"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	category := &ManualProjectCategory{
		ID:          "mpc_test001",
		Name:        "Dam downstream",
		Description: "Manual category",
	}
	if err := s.CreateManualProjectCategory(ctx, category); err != nil {
		t.Fatalf("CreateManualProjectCategory: %v", err)
	}

	got, err := s.GetManualProjectCategory(ctx, "mpc_test001")
	if err != nil || got == nil {
		t.Fatalf("GetManualProjectCategory: %v", err)
	}
	if got.Name != "Dam downstream" || got.RunCount != 0 {
		t.Fatalf("unexpected category: %#v", got)
	}

	if err := s.AssignRunToManualProjectCategory(ctx, "run_manual_project", "mpc_test001"); err != nil {
		t.Fatalf("AssignRunToManualProjectCategory: %v", err)
	}
	assignment, err := s.GetRunProjectAssignment(ctx, "run_manual_project")
	if err != nil || assignment == nil {
		t.Fatalf("GetRunProjectAssignment: %v", err)
	}
	if assignment.CategoryID != "mpc_test001" || assignment.CategoryName != "Dam downstream" {
		t.Fatalf("unexpected assignment: %#v", assignment)
	}

	categories, err := s.ListManualProjectCategories(ctx)
	if err != nil {
		t.Fatalf("ListManualProjectCategories: %v", err)
	}
	if len(categories) != 1 || categories[0].RunCount != 1 {
		t.Fatalf("categories = %#v, want one category with one run", categories)
	}
	assignments, err := s.ListRunProjectAssignments(ctx)
	if err != nil {
		t.Fatalf("ListRunProjectAssignments: %v", err)
	}
	if len(assignments) != 1 || assignments[0].RunID != "run_manual_project" {
		t.Fatalf("assignments = %#v, want run_manual_project", assignments)
	}

	if err := s.UnassignRunFromManualProjectCategory(ctx, "run_manual_project"); err != nil {
		t.Fatalf("UnassignRunFromManualProjectCategory: %v", err)
	}
	assignment, err = s.GetRunProjectAssignment(ctx, "run_manual_project")
	if err != nil {
		t.Fatalf("GetRunProjectAssignment after unassign: %v", err)
	}
	if assignment != nil {
		t.Fatalf("assignment after unassign = %#v, want nil", assignment)
	}
}

func TestRunBookmarks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateResource(ctx, &Resource{ID: "rsrc_b01", Name: "res-bookmark", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle})
	s.CreateRun(ctx, &Run{ID: "run_bookmark01", ResourceID: "rsrc_b01", Status: RunStatusSucceeded, Command: "python train.py"})

	bookmark := &RunBookmark{
		ID:    "bm_test001",
		RunID: "run_bookmark01",
		Note:  "worth comparing",
	}
	if err := s.SaveRunBookmark(ctx, bookmark); err != nil {
		t.Fatalf("SaveRunBookmark: %v", err)
	}

	got, err := s.GetRunBookmark(ctx, "run_bookmark01")
	if err != nil || got == nil {
		t.Fatalf("GetRunBookmark: %v", err)
	}
	if got.Note != "worth comparing" {
		t.Errorf("note = %q, want %q", got.Note, "worth comparing")
	}

	if err := s.SaveRunBookmark(ctx, &RunBookmark{ID: "bm_ignored", RunID: "run_bookmark01", Note: "updated note"}); err != nil {
		t.Fatalf("SaveRunBookmark upsert: %v", err)
	}
	updated, _ := s.GetRunBookmark(ctx, "run_bookmark01")
	if updated.ID != "bm_test001" {
		t.Errorf("id = %q, want original id", updated.ID)
	}
	if updated.Note != "updated note" {
		t.Errorf("updated note = %q, want %q", updated.Note, "updated note")
	}

	bookmarks, err := s.ListRunBookmarks(ctx, RunBookmarkFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRunBookmarks: %v", err)
	}
	if len(bookmarks) != 1 {
		t.Errorf("bookmarks = %d, want 1", len(bookmarks))
	}

	if err := s.DeleteRunBookmark(ctx, "run_bookmark01"); err != nil {
		t.Fatalf("DeleteRunBookmark: %v", err)
	}
	deleted, _ := s.GetRunBookmark(ctx, "run_bookmark01")
	if deleted != nil {
		t.Error("expected nil after delete")
	}
}

func TestEvidenceChainsCRUDGraphAndCandidates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createTestProject(t, s, "dam-imputation", "Dam Imputation")

	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_evidence", Name: "evidence-res", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle}); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	for _, run := range []*Run{
		{ID: "run_carded", ResourceID: "rsrc_evidence", Name: "formal-carded", Status: RunStatusSucceeded, Kind: RunKindFormal, Command: "python train.py"},
		{ID: "run_loose", ResourceID: "rsrc_evidence", Name: "loose-pilot", Status: RunStatusSucceeded, Kind: RunKindPilot, Command: "python pilot.py"},
	} {
		if err := s.CreateRun(ctx, run); err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
	}
	if err := s.SaveProjectRunCard(ctx, &ProjectRunCard{
		ID:            "card_evidence",
		ProjectID:     "dam-imputation",
		ProjectName:   "Dam Imputation",
		RunID:         "run_carded",
		Question:      "Does gated IR help?",
		Verdict:       "Improves the IR baseline.",
		EvidenceLevel: "B",
		KeyMetrics:    "mAP50-95=0.606",
	}); err != nil {
		t.Fatalf("SaveProjectRunCard: %v", err)
	}
	chain := &EvidenceChain{
		ID:          "chain_ir_gate",
		Title:       "IR gate evidence",
		Description: "Fusion reasoning",
		RoutingHints: EvidenceGraphRoutingHints{
			Recipes:  []string{" formal-ir ", "formal-ir"},
			Keywords: []string{"Fusion", " fusion ", "paired"},
		},
		ProjectID: "dam-imputation",
	}
	if err := s.CreateEvidenceChain(ctx, chain); err != nil {
		t.Fatalf("CreateEvidenceChain: %v", err)
	}
	chain.Title = "IR gate evidence v2"
	if err := s.UpdateEvidenceChain(ctx, chain); err != nil {
		t.Fatalf("UpdateEvidenceChain: %v", err)
	}
	chains, err := s.ListEvidenceChains(ctx, EvidenceChainFilter{Query: "gate", Limit: 10})
	if err != nil {
		t.Fatalf("ListEvidenceChains: %v", err)
	}
	if len(chains) != 1 || chains[0].Title != "IR gate evidence v2" {
		t.Fatalf("chains = %#v, want updated chain", chains)
	}
	if got := chains[0].RoutingHints.Recipes; len(got) != 1 || got[0] != "formal-ir" {
		t.Fatalf("routing recipes = %#v, want normalized recipe", got)
	}
	if got := chains[0].RoutingHints.Keywords; len(got) != 2 || got[0] != "Fusion" || got[1] != "paired" {
		t.Fatalf("routing keywords = %#v, want normalized keywords", got)
	}

	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "node_hyp", Type: EvidenceNodeHypothesis, Title: "IR should anchor fusion", X: 10, Y: 20},
			{ID: "node_run", Type: EvidenceNodeRun, Title: "formal-carded", RunID: "run_carded", ProjectCardID: "card_evidence", X: 320, Y: 20},
		},
		Edges: []EvidenceChainEdge{
			{ID: "edge_supports", SourceNodeID: "node_run", TargetNodeID: "node_hyp", Type: EvidenceEdgeSupports, Label: "supports", Rationale: "Improved mAP."},
		},
	}
	if err := s.SaveEvidenceChainGraph(ctx, "chain_ir_gate", graph); err != nil {
		t.Fatalf("SaveEvidenceChainGraph: %v", err)
	}
	gotGraph, err := s.GetEvidenceChainGraph(ctx, "chain_ir_gate")
	if err != nil {
		t.Fatalf("GetEvidenceChainGraph: %v", err)
	}
	if len(gotGraph.Nodes) != 2 || len(gotGraph.Edges) != 1 {
		t.Fatalf("graph = %#v, want 2 nodes and 1 edge", gotGraph)
	}

	candidates, err := s.ListEvidenceRunCandidates(ctx, EvidenceRunCandidateFilter{Query: "pilot", Limit: 10})
	if err != nil {
		t.Fatalf("ListEvidenceRunCandidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].RunID != "run_loose" || candidates[0].Kind != "run" {
		t.Fatalf("pilot candidates = %#v, want loose run", candidates)
	}
	allCandidates, err := s.ListEvidenceRunCandidates(ctx, EvidenceRunCandidateFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListEvidenceRunCandidates all: %v", err)
	}
	if len(allCandidates) < 2 || allCandidates[0].Kind != "project_card" || allCandidates[0].RunID != "run_carded" {
		t.Fatalf("candidate order = %#v, want project card first", allCandidates)
	}

	if err := s.DeleteEvidenceChain(ctx, "chain_ir_gate"); err != nil {
		t.Fatalf("DeleteEvidenceChain: %v", err)
	}
	archived, err := s.GetEvidenceChain(ctx, "chain_ir_gate")
	if err != nil || archived == nil || archived.Status != "archived" || archived.Role != "archive" {
		t.Fatalf("archived chain = %#v err=%v", archived, err)
	}
	archivedGraph, err := s.GetEvidenceChainGraph(ctx, "chain_ir_gate")
	if err != nil {
		t.Fatalf("GetEvidenceChainGraph after archive: %v", err)
	}
	if len(archivedGraph.Nodes) != 2 || len(archivedGraph.Edges) != 1 {
		t.Fatalf("graph after archive = %#v, want preserved history", archivedGraph)
	}
}

func TestExperimentMatricesCRUDGrid(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_matrix", Name: "matrix-res", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: ResourceStatusIdle}); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_matrix", ResourceID: "rsrc_matrix", Name: "matrix-run", Status: RunStatusSucceeded, Kind: RunKindAblation, Command: "python train.py"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	matrix := &ExperimentMatrix{ID: "matrix_ablation", Title: "Ablation matrix", SourceKind: "project", SourceID: "dam", SourceName: "Dam"}
	if err := s.CreateExperimentMatrix(ctx, matrix); err != nil {
		t.Fatalf("CreateExperimentMatrix: %v", err)
	}
	matrix.Title = "Ablation matrix v2"
	matrix.DefaultMetricKey = "val_loss"
	if err := s.UpdateExperimentMatrix(ctx, matrix); err != nil {
		t.Fatalf("UpdateExperimentMatrix: %v", err)
	}
	matrices, err := s.ListExperimentMatrices(ctx, ExperimentMatrixFilter{Query: "ablation", Limit: 10})
	if err != nil {
		t.Fatalf("ListExperimentMatrices: %v", err)
	}
	if len(matrices) != 1 || matrices[0].Title != "Ablation matrix v2" {
		t.Fatalf("matrices = %#v, want updated matrix", matrices)
	}

	grid := ExperimentMatrixGrid{
		Rows:    []ExperimentMatrixRow{{ID: "row_model", Label: "Model", Position: 0}},
		Columns: []ExperimentMatrixColumn{{ID: "col_metric", Label: "Metric", Position: 0}},
		Cells: []ExperimentMatrixCell{{
			ID:          "cell_metric",
			RowID:       "row_model",
			ColumnID:    "col_metric",
			RunID:       "run_matrix",
			Title:       "matrix-run",
			Statement:   "Improves validation loss.",
			MetricKey:   "val_loss",
			MetricValue: "0.12",
		}},
	}
	if err := s.SaveExperimentMatrixGrid(ctx, "matrix_ablation", grid); err != nil {
		t.Fatalf("SaveExperimentMatrixGrid: %v", err)
	}
	gotGrid, err := s.GetExperimentMatrixGrid(ctx, "matrix_ablation")
	if err != nil {
		t.Fatalf("GetExperimentMatrixGrid: %v", err)
	}
	if len(gotGrid.Rows) != 1 || len(gotGrid.Columns) != 1 || len(gotGrid.Cells) != 1 {
		t.Fatalf("grid = %#v, want 1 row/column/cell", gotGrid)
	}
	if gotGrid.Cells[0].RunID != "run_matrix" || gotGrid.Cells[0].MetricValue != "0.12" {
		t.Fatalf("cell = %#v, want linked run metric", gotGrid.Cells[0])
	}

	if err := s.DeleteExperimentMatrix(ctx, "matrix_ablation"); err != nil {
		t.Fatalf("DeleteExperimentMatrix: %v", err)
	}
	deleted, err := s.GetExperimentMatrix(ctx, "matrix_ablation")
	if err != nil || deleted != nil {
		t.Fatalf("deleted matrix = %#v err=%v, want nil", deleted, err)
	}
	emptyGrid, err := s.GetExperimentMatrixGrid(ctx, "matrix_ablation")
	if err != nil {
		t.Fatalf("GetExperimentMatrixGrid after delete: %v", err)
	}
	if len(emptyGrid.Rows) != 0 || len(emptyGrid.Columns) != 0 || len(emptyGrid.Cells) != 0 {
		t.Fatalf("grid after delete = %#v, want empty", emptyGrid)
	}
}
