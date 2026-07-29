package executor

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

type fakeRemoteRunner struct {
	mu     sync.Mutex
	calls  []string
	execFn func(string) (string, string, error)
}

type blockingRemoteRunner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingRemoteRunner) Exec(ctx context.Context, _ string, _ int, _ string, _ string, _ string, _ string, _ string) (string, string, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
		return "", "", errors.New("intentional launch failure")
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
}

func (b *blockingRemoteRunner) ExecStream(_ context.Context, _ string, _ int, _ string, _ string, _ string, _ string, _ string) (<-chan string, error) {
	return nil, errors.New("not used")
}

func (f *fakeRemoteRunner) Exec(_ context.Context, _ string, _ int, _ string, _ string, cmd string, _ string, _ string) (string, string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	f.mu.Unlock()
	if f.execFn != nil {
		return f.execFn(cmd)
	}
	return "", "", nil
}

func (f *fakeRemoteRunner) ExecStream(_ context.Context, _ string, _ int, _ string, _ string, _ string, _ string, _ string) (<-chan string, error) {
	ch := make(chan string)
	close(ch)
	return ch, nil
}

func (f *fakeRemoteRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func newExecutorTestStore(t *testing.T) *store.SQLite {
	t.Helper()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func createExecutorProject(t *testing.T, db *store.SQLite, id string) string {
	t.Helper()
	if err := db.CreateProjectDefinition(context.Background(), &store.ProjectDefinition{ID: id, Name: id}); err != nil {
		t.Fatal(err)
	}
	return id
}

func createExecutorDatasetVersion(t *testing.T, db *store.SQLite, resourceID, suffix, state string) store.RunDatasetInput {
	t.Helper()
	ctx := context.Background()
	targetID := "storage_" + suffix
	datasetID := "dataset_" + suffix
	hash := "sha256:" + strings.Repeat("a", 64)
	if err := db.SaveStorageTarget(ctx, &store.StorageTarget{
		ID: targetID, Name: targetID, ResourceID: resourceID, RootPath: "/workspace/storage/" + suffix,
	}); err != nil {
		t.Fatal(err)
	}
	dataset := &store.DatasetVersion{
		ID: datasetID, DatasetID: "fixture-" + suffix, Version: "v1", StorageTargetID: targetID,
		StoragePath: "datasets/" + suffix + "/v1", LogicalURI: "storage://" + targetID + "/datasets/" + suffix + "/v1",
		Revision: hash, ManifestSHA256: hash, State: state,
	}
	if _, _, err := db.CreateDatasetVersionImmutable(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	return store.RunDatasetInput{ID: dataset.ID, DatasetID: dataset.DatasetID, Version: dataset.Version, ManifestSHA256: dataset.ManifestSHA256}
}

func TestSubmitRequiresRegisteredProjectBeforeCreatingAnyRun(t *testing.T) {
	for _, kind := range []string{store.RunKindSetup, store.RunKindSmoke, store.RunKindPilot, store.RunKindFormal, store.RunKindAblation} {
		t.Run(kind, func(t *testing.T) {
			db := newExecutorTestStore(t)
			ctx := context.Background()
			resourceID := "rsrc_project_required_" + kind
			if err := db.CreateResource(ctx, &store.Resource{ID: resourceID, Name: resourceID, Type: "ssh", Host: "example", RootDir: "/workspace"}); err != nil {
				t.Fatal(err)
			}
			exec := NewExecutor(nil, db)
			for _, projectID := range []string{"", "project-not-registered"} {
				_, err := exec.SubmitAsync(ctx, SubmitRequest{
					ResourceID: resourceID, ProjectID: projectID, Kind: kind,
					GPUIndex: store.GPUIndexNone, Cwd: "/workspace/project", Command: "true",
				}, SubmitOptions{})
				var blocked *RunPreflightBlockedError
				if !errors.As(err, &blocked) || len(blocked.Blockers) != 1 {
					t.Fatalf("project %q error=%v blockers=%#v", projectID, err, blocked)
				}
				wantCode := "project_missing"
				if projectID != "" {
					wantCode = "project_not_registered"
				}
				if blocked.Blockers[0].Code != wantCode {
					t.Fatalf("project %q blocker=%#v, want %s", projectID, blocked.Blockers, wantCode)
				}
			}
			runs, err := db.ListRuns(ctx, store.RunFilter{ResourceID: resourceID})
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != 0 {
				t.Fatalf("invalid project submissions created Runs: %#v", runs)
			}
		})
	}
}

func TestSubmitAsyncPersistsRunBeforeRemotePreflight(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	projectID := createExecutorProject(t, db, "project_async")
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_async", Name: "async", Type: "ssh", Host: "example", Port: 22, User: "ziwu", RootDir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	runner := &blockingRemoteRunner{started: make(chan struct{}), release: make(chan struct{})}
	exec := NewExecutor(nil, db)
	exec.runner = runner
	run, err := exec.SubmitAsync(ctx, SubmitRequest{
		ResourceID: "rsrc_async", ProjectID: projectID, Name: "visible immediately", Kind: store.RunKindSmoke,
		GPUIndex: store.GPUIndexNone, Cwd: "/workspace/project", Command: "python smoke.py",
		AllowEphemeralPaths: true,
	}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.RunStatusQueued {
		t.Fatalf("response status=%q, want queued", run.Status)
	}
	persisted, err := db.GetRun(ctx, run.ID)
	if err != nil || persisted == nil || !store.IsRunActiveLifecycleStatus(persisted.Status) {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("background preflight did not start")
	}
	close(runner.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		persisted, _ = db.GetRun(ctx, run.ID)
		if persisted != nil && persisted.Status == store.RunStatusFailed && persisted.StatusCheckError != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run did not become failed: %#v", persisted)
}

func TestResumePendingSubmissionDoesNotRelaunchTerminalRun(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	resource := &store.Resource{ID: "rsrc_resume_terminal", Name: "resume", Type: "ssh", Host: "example", Port: 22, User: "ziwu", RootDir: "/workspace"}
	if err := db.CreateResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	run := &store.Run{ID: "run_resume_terminal", ResourceID: resource.ID, Status: store.RunStatusQueued, Command: "python train.py"}
	requestJSON := `{"resource_id":"rsrc_resume_terminal","name":"resume","kind":"smoke","gpu_index":-1,"cwd":"/workspace/project","command":"python train.py","allow_ephemeral_paths":true}`
	if err := db.CreateRunWithLaunchJob(ctx, run, &store.RunLaunchJob{RunID: run.ID, RequestJSON: requestJSON}); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := db.ClaimRunLaunchJob(ctx, run.ID); err != nil || !claimed {
		t.Fatalf("claim interrupted job: claimed=%v err=%v", claimed, err)
	}
	run.Status = store.RunStatusSucceeded
	if err := db.UpdateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRemoteRunner{}
	exec := NewExecutor(nil, db)
	exec.runner = runner
	if err := exec.ResumePendingSubmissions(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := runner.callCount(); got != 0 {
		t.Fatalf("terminal run was relaunched with %d remote calls", got)
	}
	persisted, err := db.GetRun(ctx, run.ID)
	if err != nil || persisted == nil || persisted.Status != store.RunStatusSucceeded {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
	if pending, err := db.ListPendingRunLaunchJobs(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
}

func TestResumePendingSubmissionReclaimsQueuedLaunch(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	projectID := createExecutorProject(t, db, "project_resume_queued")
	resource := &store.Resource{ID: "rsrc_resume_queued", Name: "resume", Type: "ssh", Host: "example", Port: 22, User: "ziwu", RootDir: "/workspace"}
	if err := db.CreateResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	run := &store.Run{ID: "run_resume_queued", ResourceID: resource.ID, ProjectID: projectID, Status: store.RunStatusQueued, Command: "python train.py"}
	requestJSON := `{"resource_id":"rsrc_resume_queued","project_id":"project_resume_queued","name":"resume","kind":"smoke","gpu_index":-1,"cwd":"/workspace/project","command":"python train.py","allow_ephemeral_paths":true}`
	if err := db.CreateRunWithLaunchJob(ctx, run, &store.RunLaunchJob{RunID: run.ID, RequestJSON: requestJSON}); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := db.ClaimRunLaunchJob(ctx, run.ID); err != nil || !claimed {
		t.Fatalf("claim interrupted job: claimed=%v err=%v", claimed, err)
	}
	runner := &blockingRemoteRunner{started: make(chan struct{}), release: make(chan struct{})}
	exec := NewExecutor(nil, db)
	exec.runner = runner
	if err := exec.ResumePendingSubmissions(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("reclaimed queued launch did not resume")
	}
	close(runner.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		persisted, _ := db.GetRun(ctx, run.ID)
		if persisted != nil && persisted.Status == store.RunStatusFailed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("resumed launch failure was not persisted")
}

func TestResumePendingSubmissionsFailsOrphanedLocalPhase(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	resource := &store.Resource{ID: "rsrc_resume_orphan", Name: "resume-orphan", Type: "ssh", Host: "example", Port: 22, User: "ziwu", RootDir: "/workspace"}
	if err := db.CreateResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	run := &store.Run{ID: "run_resume_orphan", ResourceID: resource.ID, Status: store.RunStatusPreflighting, Command: "python train.py"}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRemoteRunner{}
	exec := NewExecutor(nil, db)
	exec.runner = runner
	if err := exec.ResumePendingSubmissions(ctx); err != nil {
		t.Fatal(err)
	}
	persisted, err := db.GetRun(ctx, run.ID)
	if err != nil || persisted == nil {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
	if persisted.Status != store.RunStatusFailed || persisted.FailureKind != store.RunFailureLaunchOrphaned || !persisted.FinishedAt.Valid {
		t.Fatalf("orphaned run = %#v", persisted)
	}
	if runner.callCount() != 0 {
		t.Fatalf("orphaned run reached remote runner %d times", runner.callCount())
	}
}

func TestCancelPreflightingRunStopsDetachedLaunch(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	projectID := createExecutorProject(t, db, "project_cancel_preflight")
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_cancel_preflight", Name: "cancel", Type: "ssh", Host: "example", Port: 22, User: "ziwu", RootDir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	runner := &blockingRemoteRunner{started: make(chan struct{}), release: make(chan struct{})}
	exec := NewExecutor(nil, db)
	exec.runner = runner
	run, err := exec.SubmitAsync(ctx, SubmitRequest{ResourceID: "rsrc_cancel_preflight", ProjectID: projectID, Name: "cancel me", Kind: store.RunKindSmoke, GPUIndex: store.GPUIndexNone, Cwd: "/workspace/project", Command: "python smoke.py", AllowEphemeralPaths: true}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("background preflight did not start")
	}
	if err := exec.Cancel(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	persisted, err := db.GetRun(ctx, run.ID)
	if err != nil || persisted == nil || persisted.Status != store.RunStatusCancelled {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
}

func TestNormalizeUIEventsPath(t *testing.T) {
	if got := normalizeUIEventsPath("", "run_abc", true, "/workspace/.aexp/runs/run_abc"); got != ".aexp/events/run_abc.jsonl" {
		t.Fatalf("default ui events path = %q", got)
	}
	if got := normalizeUIEventsPath("", "run_abc", false, "/workspace/.aexp/runs/run_abc"); got != "/workspace/.aexp/runs/run_abc/events.jsonl" {
		t.Fatalf("run-dir ui events path = %q", got)
	}
	if got := normalizeUIEventsPath("off", "run_abc", true, "/workspace/.aexp/runs/run_abc"); got != "" {
		t.Fatalf("disabled ui events path = %q", got)
	}
	if got := normalizeUIEventsPath("events/train.jsonl", "run_abc", true, "/workspace/.aexp/runs/run_abc"); got != "events/train.jsonl" {
		t.Fatalf("explicit ui events path = %q", got)
	}
}

func TestPersistRunManifestRedactsSecretsAndUsesStableHash(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	resource := &store.Resource{ID: "rsrc_manifest", Name: "manifest", Type: "ssh", Host: "localhost", RootDir: "/workspace"}
	if err := db.CreateResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	run := &store.Run{ID: "run_manifest", ResourceID: resource.ID, Status: store.RunStatusRunning, Kind: store.RunKindFormal, Command: "python train.py", EnvJSON: `{"API_TOKEN":"super-secret","SAFE_VALUE":"visible"}`, ArtifactPathsJSON: `[]`, LogPathsJSON: `[]`, MetricPathsJSON: `[]`}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	exec := NewExecutor(nil, db)
	first, err := exec.persistRunManifest(ctx, run, resource, store.RunManifestDraft, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first.ManifestJSON, "super-secret") || !strings.Contains(first.ManifestJSON, "[redacted]") || !strings.Contains(first.ManifestJSON, "visible") {
		t.Fatalf("manifest redaction failed: %s", first.ManifestJSON)
	}
	second, err := exec.persistRunManifest(ctx, run, resource, store.RunManifestDraft, nil)
	if err != nil || second.SHA256 != first.SHA256 {
		t.Fatalf("manifest hash unstable: %q vs %q err=%v", first.SHA256, second.SHA256, err)
	}
}

func TestParseWrapperExitCode(t *testing.T) {
	code, ok := parseWrapperExitCode(`[stdout] [aexp] ========================================
[stdout] [aexp] Finished at Thu Jun 11 21:00:00 CST 2026 with exit code 0
[stdout] [aexp] ========================================`)
	if !ok || code != 0 {
		t.Fatalf("exit code = %d, %v", code, ok)
	}
	code, ok = parseWrapperExitCode(`[aexp] Finished at Thu Jun 11 21:00:00 CST 2026 with exit code 137`)
	if !ok || code != 137 {
		t.Fatalf("exit code = %d, %v", code, ok)
	}
	if _, ok := parseWrapperExitCode("tmux vanished"); ok {
		t.Fatalf("unexpected exit code")
	}
}

func TestParseRemoteStatusCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		code int
		ok   bool
	}{
		{name: "plain zero", out: "0\n", code: 0, ok: true},
		{name: "last line", out: "warning\n1\n", code: 1, ok: true},
		{name: "empty", out: "", ok: false},
		{name: "nonnumeric", out: "ssh timeout\n", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, ok := parseRemoteStatusCode(tc.out)
			if ok != tc.ok || code != tc.code {
				t.Fatalf("parseRemoteStatusCode(%q) = %d, %v; want %d, %v", tc.out, code, ok, tc.code, tc.ok)
			}
		})
	}
}

func TestParseLogSnapshotWithExplicitCursorStart(t *testing.T) {
	lines, total, err := parseLogSnapshotWithTotal("run_cursor", "stdout", "\x01AEXP_TOTAL_LINES\t10000\n\x01AEXP_START_LINE\t428\nline 428\nline 429\n")
	if err != nil || total != 10000 || len(lines) != 2 {
		t.Fatalf("parse cursor snapshot total=%d lines=%#v err=%v", total, lines, err)
	}
	if lines[0].LineNo != 428 || lines[1].LineNo != 429 {
		t.Fatalf("cursor line numbers = %d,%d", lines[0].LineNo, lines[1].LineNo)
	}
}

func TestBuildResourceRunProbeScriptBatchesActiveRuns(t *testing.T) {
	script := buildResourceRunProbeScript([]store.Run{
		{ID: "run_one", Status: store.RunStatusRunning, RemoteRunDir: "/ws/.aexp/runs/run_one", TmuxSession: "aexp_one"},
		{ID: "run_two", Status: store.RunStatusSSHUnreachable, RemoteRunDir: "/ws/.aexp/runs/run_two", TmuxSession: "aexp_two"},
		{ID: "run_done", Status: store.RunStatusSucceeded, RemoteRunDir: "/ws/.aexp/runs/run_done", TmuxSession: "aexp_done"},
	})
	if !strings.Contains(script, "run_one") || !strings.Contains(script, "run_two") {
		t.Fatalf("active runs missing from batch script: %s", script)
	}
	if strings.Contains(script, "run_done") {
		t.Fatalf("terminal run included in batch script: %s", script)
	}
	if got := strings.Count(script, "if [ -f"); got != 2 {
		t.Fatalf("batch probe clauses = %d, want 2: %s", got, script)
	}
}

func TestRefreshResourceRunGroupUsesOneNormalPathProbe(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	resource := &store.Resource{ID: "rsrc_batch", Name: "batch", Type: "ssh", Host: "localhost", RootDir: "/ws"}
	if err := db.CreateResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	runs := []store.Run{
		{ID: "run_batch_one", ResourceID: resource.ID, Status: store.RunStatusRunning, Kind: store.RunKindFormal, Command: "sleep 10", RemoteRunDir: "/ws/.aexp/runs/run_batch_one", TmuxSession: "aexp_one"},
		{ID: "run_batch_two", ResourceID: resource.ID, Status: store.RunStatusRunning, Kind: store.RunKindFormal, Command: "sleep 10", RemoteRunDir: "/ws/.aexp/runs/run_batch_two", TmuxSession: "aexp_two"},
	}
	for i := range runs {
		if err := db.CreateRun(ctx, &runs[i]); err != nil {
			t.Fatal(err)
		}
	}
	runner := &fakeRemoteRunner{execFn: func(cmd string) (string, string, error) {
		if !strings.Contains(cmd, "run_batch_one") || !strings.Contains(cmd, "run_batch_two") {
			t.Fatalf("batch command does not contain both runs: %s", cmd)
		}
		return "run_batch_one\tlive\t0\nrun_batch_two\tlive\t0\n", "", nil
	}}
	exec := NewExecutor(nil, db)
	exec.runner = runner
	refreshed, cached, err := exec.refreshResourceRunGroup(ctx, runs)
	if err != nil {
		t.Fatal(err)
	}
	if runner.callCount() != 1 || len(refreshed) != 2 || len(cached) != 0 {
		t.Fatalf("calls=%d refreshed=%#v cached=%#v", runner.callCount(), refreshed, cached)
	}
}

func TestRefreshResourceRunGroupPublishesRunningToTerminalChange(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	resource := &store.Resource{ID: "rsrc_terminal_change", Name: "terminal", Type: "ssh", Host: "localhost", RootDir: "/ws"}
	if err := db.CreateResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	run := store.Run{ID: "run_terminal_change", ResourceID: resource.ID, Status: store.RunStatusRunning, Kind: store.RunKindFormal, Command: "train", RemoteRunDir: "/ws/.aexp/runs/run_terminal_change", TmuxSession: "aexp_terminal"}
	if err := db.CreateRun(ctx, &run); err != nil {
		t.Fatal(err)
	}
	initial, err := db.ListRunChanges(ctx, 0, nil, 100)
	if err != nil || len(initial) == 0 {
		t.Fatalf("initial changes=%#v err=%v", initial, err)
	}
	cursor := initial[len(initial)-1].Seq
	runner := &fakeRemoteRunner{execFn: func(cmd string) (string, string, error) {
		if strings.Contains(cmd, "if [ -f") && strings.Contains(cmd, run.ID) {
			return run.ID + "\texit\t0\n", "", nil
		}
		return "", "", nil
	}}
	exec := NewExecutor(nil, db)
	exec.runner = runner
	refreshed, _, refreshErr := exec.refreshResourceRunGroup(ctx, []store.Run{run})
	if refreshErr != nil {
		t.Fatal(refreshErr)
	}
	if refreshed[run.ID].Status != store.RunStatusSucceeded {
		t.Fatalf("refreshed=%#v", refreshed[run.ID])
	}
	summary, err := db.GetRunSummary(ctx, run.ID)
	if err != nil || summary == nil || summary.Status != store.RunStatusSucceeded {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
	changes, err := db.ListRunChanges(ctx, cursor, nil, 100)
	if err != nil || len(changes) == 0 || changes[len(changes)-1].RunID != run.ID {
		t.Fatalf("terminal changes=%#v err=%v", changes, err)
	}
}

func TestRefreshRunsReportsProbeFailureWithoutClaimingLifecycleEnded(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	resource := &store.Resource{ID: "rsrc_unreachable", Name: "unreachable", Type: "ssh", Host: "localhost", RootDir: "/ws"}
	if err := db.CreateResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	run := store.Run{ID: "run_unreachable", ResourceID: resource.ID, Status: store.RunStatusRunning, Kind: store.RunKindFormal, Command: "train", RemoteRunDir: "/ws/.aexp/runs/run_unreachable", TmuxSession: "aexp_unreachable"}
	if err := db.CreateRun(ctx, &run); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRemoteRunner{execFn: func(string) (string, string, error) {
		return "", "", context.DeadlineExceeded
	}}
	exec := NewExecutor(nil, db)
	exec.runner = runner
	_, cached, err := exec.RefreshRunsWithConcurrency(ctx, []store.Run{run}, time.Second, 1)
	if err == nil {
		t.Fatal("probe failure was swallowed")
	}
	var refreshErr *RunRefreshError
	if !errors.As(err, &refreshErr) || len(refreshErr.Failures) != 1 || refreshErr.Failures[0].Code != "remote_timeout" {
		t.Fatalf("structured refresh error=%#v raw=%v", refreshErr, err)
	}
	if !cached[run.ID] {
		t.Fatalf("cached=%#v", cached)
	}
	stored, err := db.GetRun(ctx, run.ID)
	if err != nil || stored == nil {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	if stored.Status != store.RunStatusRunning || stored.ObservationState != store.RunObservationUnreachable || stored.StatusFreshness != store.RunStatusFreshnessStale {
		t.Fatalf("run observation did not separate lifecycle: %#v", stored)
	}
}

func TestRefreshResourceLookupFailurePersistsUnreachableObservation(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_lookup_timeout", Name: "lookup", Type: "ssh", Host: "lookup", RootDir: "/ws"}); err != nil {
		t.Fatal(err)
	}
	run := &store.Run{ID: "run_lookup_timeout", ResourceID: "rsrc_lookup_timeout", Status: store.RunStatusRunning, Command: "train"}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	exec := NewExecutor(nil, db)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, _, refreshErr := exec.refreshResourceRunGroup(cancelled, []store.Run{*run})
	if refreshErr == nil {
		t.Fatal("expected structured resource lookup failure")
	}
	persisted, err := db.GetRun(ctx, run.ID)
	if err != nil || persisted == nil {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
	if persisted.Status != store.RunStatusRunning || persisted.ObservationState != store.RunObservationUnreachable || persisted.StatusFreshness != store.RunStatusFreshnessStale || persisted.StatusCheckError == "" {
		t.Fatalf("lookup failure observation not persisted: %#v", persisted)
	}
}

func TestCollectArtifactsIndexesRemoteInventoryWithoutDownloading(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	resource := &store.Resource{ID: "rsrc_artifacts", Name: "artifacts", Type: "ssh", Host: "localhost", RootDir: "/ws"}
	if err := db.CreateResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	run := &store.Run{ID: "run_artifacts", ResourceID: resource.ID, Status: store.RunStatusSucceeded, Kind: store.RunKindFormal, Command: "python train.py", Cwd: "/ws/project", ResolvedCwd: "/ws/project", ArtifactPathsJSON: `["results/*.json","checkpoints/*.pt"]`}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRemoteRunner{execFn: func(cmd string) (string, string, error) {
		if !strings.Contains(cmd, "max_files=100000") || strings.Contains(cmd, "max_hash") {
			t.Fatalf("unexpected artifact discovery command: %s", cmd)
		}
		return `{"files":[{"path":"/ws/project/results/metrics.json","relative_path":"results/metrics.json","size":42,"mtime":1710000000,"sha256":"sha256:file","mime":"application/json"}],"errors":[]}`, "", nil
	}}
	exec := NewExecutor(nil, db)
	exec.runner = runner
	collection, err := exec.CollectArtifacts(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := db.ListArtifacts(ctx, run.ID)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts=%#v err=%v", artifacts, err)
	}
	if collection.State != store.ArtifactCollectionIndexed || collection.FileCount != 1 || artifacts[0].RelativePath != "results/metrics.json" || artifacts[0].Role != "metric" || artifacts[0].SHA256 != "sha256:file" {
		t.Fatalf("collection=%#v artifact=%#v", collection, artifacts[0])
	}
	if runner.callCount() != 1 {
		t.Fatalf("remote discovery calls=%d, want 1", runner.callCount())
	}
}

func TestSubmitPersistsProjectTargetProvenanceAndDraftManifest(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	createExecutorProject(t, db, "project_launch")
	resource := &store.Resource{ID: "rsrc_launch", Name: "launch", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: store.ResourceStatusIdle}
	if err := db.CreateResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRemoteRunner{}
	exec := NewExecutor(nil, db)
	exec.runner = runner
	run, err := exec.Submit(ctx, SubmitRequest{ResourceID: resource.ID, ProjectID: "project_launch", TargetID: "target_launch", RecipeName: "prepare", Name: "prepare launch", Kind: store.RunKindSetup, GPUIndex: store.GPUIndexNone, Command: "uv sync --frozen", Cwd: "/ws/project", ProjectEnv: ProjectEnvRaw, TargetEnv: "launch-runtime"})
	if err != nil {
		t.Fatal(err)
	}
	if run.ProjectID != "project_launch" || run.TargetID != "target_launch" || run.RecipeName != "prepare" || run.TaskRole != store.RunTaskRolePrepare || run.EvidenceGrade != store.RunEvidenceGradeNone {
		t.Fatalf("run provenance/semantics = %#v", run)
	}
	manifest, err := db.GetRunManifest(ctx, run.ID)
	if err != nil || manifest == nil || manifest.State != store.RunManifestDraft || !strings.Contains(manifest.ManifestJSON, `"target_id":"target_launch"`) {
		t.Fatalf("draft manifest=%#v err=%v", manifest, err)
	}
	collection, err := db.GetArtifactCollection(ctx, run.ID)
	if err != nil || collection == nil || collection.State != store.ArtifactCollectionDeclared {
		t.Fatalf("artifact declaration=%#v err=%v", collection, err)
	}
}

func TestValidatePersistentRunPathsRejectsEphemeralCwd(t *testing.T) {
	if err := validatePersistentRunPaths("/workspace", "/tmp/project"); err == nil || !strings.Contains(err.Error(), "ephemeral") {
		t.Fatalf("expected ephemeral cwd rejection, got %v", err)
	}
	if err := validatePersistentRunPaths("/tmp/workspace", "project"); err == nil || !strings.Contains(err.Error(), "root_dir") {
		t.Fatalf("expected ephemeral root_dir rejection, got %v", err)
	}
	if err := validatePersistentRunPaths("/workspace", "project"); err != nil {
		t.Fatalf("durable relative cwd rejected: %v", err)
	}
}

func TestWithResourceRemotePath(t *testing.T) {
	cmd := WithResourceRemotePath(&store.Resource{
		OSType:     "macos",
		RemotePath: "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin",
	}, "tmux -V")
	if !strings.HasPrefix(cmd, "export PATH='/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin':$PATH\n") {
		t.Fatalf("unexpected remote path wrapper: %q", cmd)
	}
	if !strings.HasSuffix(cmd, "tmux -V") {
		t.Fatalf("wrapped command missing original command: %q", cmd)
	}
	if got := EffectiveRemotePath(&store.Resource{OSType: "macos"}); !strings.Contains(got, "/opt/homebrew/bin") {
		t.Fatalf("macos default remote path missing homebrew bin: %q", got)
	}
	if got := WithResourceRemotePath(&store.Resource{OSType: "linux"}, "echo ok"); got != "echo ok" {
		t.Fatalf("linux without remote_path should not be wrapped: %q", got)
	}
}

func TestSubmitForceRequiresReason(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	projectID := createExecutorProject(t, db, "project_force")
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_force", Name: "force", Type: "ssh", Host: "127.0.0.1", Port: 22, User: "ziwu", RootDir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	exec := NewExecutor(NewSSHPool(10*time.Millisecond), db)
	_, err := exec.SubmitWithOptions(ctx, SubmitRequest{
		ResourceID:          "rsrc_force",
		ProjectID:           projectID,
		Name:                "dangerous force",
		Kind:                store.RunKindFormal,
		ProjectConfigSHA256: "sha256:test-config",
		Seeds:               []int64{41},
		SplitProtocol:       "fixture-split-v1",
		EvaluationProtocol:  "fixture-eval-v1",
		GPUIndex:            0,
		Force:               true,
		Cwd:                 "/workspace/project",
		Program:             "python",
		Args:                []string{"train.py"},
		AllowEphemeralPaths: true,
		RefreshProjectEnv:   false,
	}, SubmitOptions{})
	if err == nil || !strings.Contains(err.Error(), "force-reason") {
		t.Fatalf("expected force reason validation, got %v", err)
	}
}

func TestSubmitFormalRejectsDirtyGit(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	projectID := createExecutorProject(t, db, "project_git_dirty")
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_git_dirty", Name: "git-dirty", Type: "ssh", Host: "127.0.0.1", Port: 1, User: "ziwu", RootDir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	repo := newDirtyGitRepo(t)
	exec := NewExecutor(NewSSHPool(1*time.Millisecond), db)
	_, err := exec.SubmitWithOptions(ctx, SubmitRequest{
		ResourceID:          "rsrc_git_dirty",
		ProjectID:           projectID,
		Name:                "formal dirty",
		Kind:                store.RunKindFormal,
		GPUIndex:            store.GPUIndexNone,
		Cwd:                 "/workspace/project",
		Program:             "python",
		Args:                []string{"train.py"},
		AllowEphemeralPaths: true,
		GitSourceDir:        repo,
	}, SubmitOptions{})
	if err == nil || !strings.Contains(err.Error(), "clean Git worktree") {
		t.Fatalf("expected dirty git rejection, got %v", err)
	}
	runs, listErr := db.ListRuns(ctx, store.RunFilter{ResourceID: "rsrc_git_dirty"})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(runs) != 0 {
		t.Fatalf("dirty formal rejection should happen before run creation, got %#v", runs)
	}
}

func TestSubmitDirtySmokeRecordsGitButDoesNotBlock(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	projectID := createExecutorProject(t, db, "project_git_smoke")
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_git_smoke", Name: "git-smoke", Type: "ssh", Host: "127.0.0.1", Port: 1, User: "ziwu", RootDir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	repo := newDirtyGitRepo(t)
	exec := NewExecutor(NewSSHPool(1*time.Millisecond), db)
	_, err := exec.SubmitWithOptions(ctx, SubmitRequest{
		ResourceID:          "rsrc_git_smoke",
		ProjectID:           projectID,
		Name:                "dirty smoke",
		Kind:                store.RunKindSmoke,
		GPUIndex:            store.GPUIndexNone,
		Cwd:                 "/workspace/project",
		Program:             "python",
		Args:                []string{"smoke.py"},
		AllowEphemeralPaths: true,
		GitSourceDir:        repo,
	}, SubmitOptions{})
	if err == nil || strings.Contains(err.Error(), "clean Git worktree") {
		t.Fatalf("dirty smoke should pass git guard and only fail later in launch, got %v", err)
	}
	runs, listErr := db.ListRuns(ctx, store.RunFilter{ResourceID: "rsrc_git_smoke"})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(runs) != 1 || !runs[0].GitDirty || runs[0].GitCommit == "" || runs[0].GitRepoRoot == "" {
		t.Fatalf("dirty smoke git provenance not recorded: %#v", runs)
	}
}

func TestSubmitFormalDirtyOverrideRecordsPatchReference(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_git_override", Name: "git-override", Type: "ssh", Host: "127.0.0.1", Port: 1, User: "ziwu", RootDir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateProjectDefinition(ctx, &store.ProjectDefinition{ID: "project_git_override", Name: "Git override"}); err != nil {
		t.Fatal(err)
	}
	dataset := createExecutorDatasetVersion(t, db, "rsrc_git_override", "git_override", store.DatasetStateVerified)
	repo := newDirtyGitRepo(t)
	exec := NewExecutor(NewSSHPool(1*time.Millisecond), db)
	_, err := exec.SubmitWithOptions(ctx, SubmitRequest{
		ResourceID:          "rsrc_git_override",
		ProjectID:           "project_git_override",
		Name:                "formal dirty override",
		Kind:                store.RunKindFormal,
		ProjectConfigSHA256: "sha256:test-config",
		Datasets:            []store.RunDatasetInput{dataset},
		Seeds:               []int64{41},
		SplitProtocol:       "fixture-split-v1",
		EvaluationProtocol:  "fixture-eval-v1",
		GPUIndex:            store.GPUIndexNone,
		Cwd:                 "/workspace/project",
		Program:             "python",
		Args:                []string{"train.py"},
		AllowEphemeralPaths: true,
		GitSourceDir:        repo,
		AllowDirtyGit:       true,
		RecordGitDiff:       true,
	}, SubmitOptions{})
	if err == nil || strings.Contains(err.Error(), "clean Git worktree") {
		t.Fatalf("dirty override should pass git guard and only fail later in launch, got %v", err)
	}
	runs, listErr := db.ListRuns(ctx, store.RunFilter{ResourceID: "rsrc_git_override"})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %#v, want one created run", runs)
	}
	run := runs[0]
	if !run.GitDirty || !run.GitAllowDirty || run.GitDiffHash == "" || run.GitDiffPath == "" {
		t.Fatalf("dirty override provenance incomplete: %#v", run)
	}
	if _, statErr := os.Stat(run.GitDiffPath); statErr != nil {
		t.Fatalf("git diff patch not written: %v", statErr)
	}
	if !strings.HasPrefix(run.GitDiffPath, filepath.Join(home, ".aexp", "git-diffs")) {
		t.Fatalf("git diff patch path = %q, want under temp HOME", run.GitDiffPath)
	}
}

func TestSubmitFormalAndAblationRejectRegisteredDatasetBeforeRemoteLaunch(t *testing.T) {
	for _, kind := range []string{store.RunKindFormal, store.RunKindAblation} {
		t.Run(kind, func(t *testing.T) {
			db := newExecutorTestStore(t)
			ctx := context.Background()
			resourceID := "rsrc_registered_" + kind
			if err := db.CreateResource(ctx, &store.Resource{ID: resourceID, Name: resourceID, Type: "ssh", Host: "127.0.0.1", Port: 1, User: "ziwu", RootDir: "/workspace"}); err != nil {
				t.Fatal(err)
			}
			projectID := createExecutorProject(t, db, "project_registered_"+kind)
			dataset := createExecutorDatasetVersion(t, db, resourceID, "registered_"+kind, store.DatasetStateRegistered)
			runner := &fakeRemoteRunner{}
			exec := NewExecutor(nil, db)
			exec.runner = runner
			_, err := exec.SubmitVisibleWithOptions(ctx, SubmitRequest{
				ResourceID: resourceID, ProjectID: projectID, Name: "registered dataset", Kind: kind, GPUIndex: store.GPUIndexNone,
				Cwd: "/workspace/project", Program: "python", Args: []string{"train.py"}, AllowEphemeralPaths: true,
				GitSourceDir: newCleanGitRepo(t), ProjectConfigSHA256: "sha256:test-config",
				Datasets: []store.RunDatasetInput{dataset}, Seeds: []int64{41},
				SplitProtocol: "fixture-split-v1", EvaluationProtocol: "fixture-eval-v1",
			}, SubmitOptions{})
			var blocked *RunPreflightBlockedError
			if !errors.As(err, &blocked) {
				t.Fatalf("error = %v, want structured blocker", err)
			}
			found := false
			for _, blocker := range blocked.Blockers {
				if blocker.Code == "dataset_not_verified" {
					found = true
				}
			}
			if !found {
				t.Fatalf("blockers = %#v", blocked.Blockers)
			}
			if runner.callCount() != 0 {
				t.Fatalf("remote launch was called %d times", runner.callCount())
			}
		})
	}
}

func TestFormalProvenanceBlockerMakesReservedRunTerminal(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	projectID := createExecutorProject(t, db, "project_formal_blocked")
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_formal_blocked", Name: "formal-blocked", Type: "ssh", Host: "127.0.0.1", Port: 1, User: "ziwu", RootDir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "aexp@example.invalid")
	runGit(t, repo, "config", "user.name", "aexp test")
	if err := os.WriteFile(filepath.Join(repo, "train.py"), []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "train.py")
	runGit(t, repo, "commit", "-m", "fixture")
	exec := NewExecutor(NewSSHPool(time.Millisecond), db)
	_, err := exec.SubmitVisibleWithOptions(ctx, SubmitRequest{ResourceID: "rsrc_formal_blocked", ProjectID: projectID, Kind: store.RunKindFormal, GPUIndex: store.GPUIndexNone, Cwd: "/workspace/project", Program: "python", Args: []string{"train.py"}, GitSourceDir: repo, AllowEphemeralPaths: true}, SubmitOptions{})
	var blocked *RunPreflightBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("error = %v, want structured blocker", err)
	}
	want := map[string]bool{"dataset_missing": true, "seeds_missing": true, "project_config_hash_missing": true, "split_protocol_missing": true, "evaluation_protocol_missing": true}
	for _, blocker := range blocked.Blockers {
		delete(want, blocker.Code)
	}
	if len(want) != 0 {
		t.Fatalf("missing blocker codes: %#v", want)
	}
	runs, err := db.ListRuns(ctx, store.RunFilter{ResourceID: "rsrc_formal_blocked"})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != store.RunStatusFailed || runs[0].FailureKind != store.RunFailurePreflightBlocked || !runs[0].FinishedAt.Valid {
		t.Fatalf("runs = %#v", runs)
	}
}

func newDirtyGitRepo(t *testing.T) string {
	t.Helper()
	dir := newCleanGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "train.py"), []byte("print('dirty')\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newCleanGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "aexp@example.invalid")
	runGit(t, dir, "config", "user.name", "AEXP Test")
	if err := os.WriteFile(filepath.Join(dir, "train.py"), []byte("print('v1')\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "train.py")
	runGit(t, dir, "commit", "-m", "initial")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := osexec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func TestSubmitPreemptRunMustMatchResource(t *testing.T) {
	db := newExecutorTestStore(t)
	ctx := context.Background()
	projectID := createExecutorProject(t, db, "project_preempt")
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_new", Name: "new", Type: "ssh", Host: "127.0.0.1", Port: 22, User: "ziwu", RootDir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_old", Name: "old", Type: "ssh", Host: "127.0.0.1", Port: 22, User: "ziwu", RootDir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID:         "run_old",
		ResourceID: "rsrc_old",
		Status:     store.RunStatusRunning,
		Kind:       store.RunKindFormal,
		GPUIndex:   0,
		Command:    "python train.py",
	}); err != nil {
		t.Fatal(err)
	}
	exec := NewExecutor(NewSSHPool(10*time.Millisecond), db)
	_, err := exec.SubmitWithOptions(ctx, SubmitRequest{
		ResourceID:          "rsrc_new",
		ProjectID:           projectID,
		Name:                "preempt wrong host",
		Kind:                store.RunKindFormal,
		GPUIndex:            0,
		ForceReason:         "free gpu for formal rerun",
		PreemptRunID:        "run_old",
		Cwd:                 "/workspace/project",
		Program:             "python",
		Args:                []string{"train.py"},
		AllowEphemeralPaths: true,
		RefreshProjectEnv:   false,
	}, SubmitOptions{})
	if err == nil || !strings.Contains(err.Error(), "not requested resource") {
		t.Fatalf("expected preempt resource validation, got %v", err)
	}
}

func TestBuildCommandScriptInstallsAexpEventsHelper(t *testing.T) {
	req := SubmitRequest{
		Program:      "python",
		Args:         []string{"train.py"},
		Cwd:          "/workspace/project",
		UIEventsPath: ".aexp/events/run_abc.jsonl",
	}
	script := buildCommandScript(req, "", "", "", "/workspace", map[string]string{
		"AEXP_RUN_DIR":   "/workspace/.aexp/runs/run_abc",
		"AEXP_UI_EVENTS": ".aexp/events/run_abc.jsonl",
	}, nil)
	for _, want := range []string{
		`mkdir -p "$(dirname -- "$AEXP_UI_EVENTS")"`,
		`cat > "$AEXP_RUN_DIR/aexp_events.py" <<'PY'`,
		`if [ ! -e "$PWD/aexp_events.py" ]; then cat > "$PWD/aexp_events.py" <<'PY'`,
		`runpy.run_path(_helper)`,
		`export PYTHONPATH="$PWD:$AEXP_RUN_DIR${PYTHONPATH:+:$PYTHONPATH}"`,
		`export PYTHONUNBUFFERED="${PYTHONUNBUFFERED:-1}"`,
		"def metric(name, value, **fields):",
		"def training_epoch(epoch, total=None, **fields):",
		"def training_done(epoch=None, total=None, best_epoch=None, early_stopped=False, **fields):",
		"python 'train.py'",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("command script missing %q\n%s", want, script)
		}
	}
}

func TestAexpEventsPythonHelperNeverRaisesAndPreservesMetricName(t *testing.T) {
	python, err := osexec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	dir := t.TempDir()
	helperPath := filepath.Join(dir, "aexp_events.py")
	if err := os.WriteFile(helperPath, []byte(AexpEventsPythonHelper()), 0644); err != nil {
		t.Fatal(err)
	}
	script := `
import json
import os
from decimal import Decimal
import aexp_events

class Scalar:
    def item(self):
        return Decimal("1.25")

class Array:
    def tolist(self):
        return [Decimal("2.5"), 3]

class Opaque:
    pass

class Hostile:
    def __str__(self):
        raise RuntimeError("bad str")
    def __repr__(self):
        raise RuntimeError("bad repr")

event_path = os.environ["AEXP_UI_EVENTS"]
aexp_events.metric("validation_really_long_identity/loss_name", Decimal("0.5"),
                   scalar=Scalar(), array=Array(), opaque=Opaque())
aexp_events.emit(Hostile())
with open(event_path, "r", encoding="utf-8") as handle:
    events = [json.loads(line) for line in handle if line.strip()]
metric = next(item for item in events if item.get("type") == "metric")
assert metric["name"] == "validation_really_long_identity/loss_name", metric
assert metric["value"] == "Decimal('0.5')", metric
assert metric["scalar"] == "Decimal('1.25')", metric
assert metric["array"] == ["Decimal('2.5')", 3], metric

before_rank_filter = len(events)
os.environ["RANK"] = "1"
os.environ["LOCAL_RANK"] = "1"
aexp_events.metric("rank1/default-suppressed", 1.0)
with open(event_path, "r", encoding="utf-8") as handle:
    filtered_events = [json.loads(line) for line in handle if line.strip()]
assert len(filtered_events) == before_rank_filter, filtered_events

os.environ["AEXP_EVENTS_ALL_RANKS"] = "true"
aexp_events.metric("rank1/all-ranks", 2.0)
with open(event_path, "r", encoding="utf-8") as handle:
    all_rank_events = [json.loads(line) for line in handle if line.strip()]
rank_event = next(item for item in all_rank_events if item.get("name") == "rank1/all-ranks")
assert rank_event["rank"] == 1, rank_event
assert rank_event["local_rank"] == 1, rank_event
os.environ.pop("AEXP_EVENTS_ALL_RANKS", None)
os.environ["RANK"] = "0"
aexp_events.metric("rank0/default", 3.0)
with open(event_path, "r", encoding="utf-8") as handle:
    rank0_events = [json.loads(line) for line in handle if line.strip()]
assert any(item.get("name") == "rank0/default" for item in rank0_events), rank0_events
os.environ.pop("RANK", None)
os.environ.pop("LOCAL_RANK", None)

bad_parent = os.path.join(os.path.dirname(event_path), "not-a-directory")
with open(bad_parent, "w", encoding="utf-8") as handle:
    handle.write("x")
os.environ["AEXP_UI_EVENTS"] = os.path.join(bad_parent, "events.jsonl")
aexp_events.metric("train/loss", Decimal("1.0"))
os.environ.pop("AEXP_UI_EVENTS", None)
aexp_events.metric("train/loss", Decimal("2.0"))
`
	cmd := osexec.Command(python, "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PYTHONPATH="+dir, "AEXP_UI_EVENTS="+filepath.Join(dir, "events.jsonl"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("aexp_events helper raised: %v\n%s", err, output)
	}
}

func TestPersistRunProbeLogsStatusWriteFailureWithRunAndTargetStatus(t *testing.T) {
	db := newExecutorTestStore(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	exec := NewExecutor(nil, db)
	run := &store.Run{ID: "run_status_write_warning", Status: store.RunStatusFailed}
	if exec.persistRunProbe(context.Background(), run, store.RunStatusRunning) {
		t.Fatal("closed database status write unexpectedly succeeded")
	}
	output := logs.String()
	for _, want := range []string{
		"persist run status failed",
		"run_id=run_status_write_warning",
		"target_status=failed",
		"error=",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("warning missing %q:\n%s", want, output)
		}
	}
}

func TestBindingsForRequestExpandsRunIDInOutputPatternAndURI(t *testing.T) {
	bindings := bindingsForRequest(SubmitRequest{
		Outputs: []store.RunOutputBinding{{
			SourcePattern: "output/aexp/{run_id}/report.json",
			LogicalURI:    "storage://nas/runs/{run_id}/report.json",
		}},
	}, "run_abc")
	if len(bindings.Outputs) != 1 {
		t.Fatalf("outputs = %#v", bindings.Outputs)
	}
	if got := bindings.Outputs[0].SourcePattern; got != "output/aexp/run_abc/report.json" {
		t.Fatalf("source pattern = %q", got)
	}
	if got := bindings.Outputs[0].LogicalURI; got != "storage://nas/runs/run_abc/report.json" {
		t.Fatalf("logical URI = %q", got)
	}
}

func TestReplaceRunIDTokensDoesNotMutateCaller(t *testing.T) {
	input := []string{"output/aexp/{run_id}/**"}
	got := replaceRunIDTokens(input, "run_xyz")
	if got[0] != "output/aexp/run_xyz/**" || input[0] != "output/aexp/{run_id}/**" {
		t.Fatalf("got=%#v input=%#v", got, input)
	}
}

func TestValidateCwdAllowsAbsolutePathUnderFilesystemRoot(t *testing.T) {
	if err := validateCwd("/", "/home/ziwu/project"); err != nil {
		t.Fatalf("filesystem root should contain every absolute cwd: %v", err)
	}
	if err := validateCwd("/home/ziwu", "/home/ziwu-other/project"); err == nil {
		t.Fatal("sibling prefix escaped resource root")
	}
}

func TestBuildCommandScriptCondaShellModeUsesCondaRun(t *testing.T) {
	req := SubmitRequest{
		Program: "bash",
		Args:    []string{"-lc", "python scripts/train_mdd_yolo.py --epochs 100"},
		Cwd:     "/workspace/project",
	}
	script := buildCommandScript(req, "defect-yolo", "/opt/conda", "/opt/conda/etc/profile.d/conda.sh", "/workspace", nil, nil)
	if strings.Contains(script, "conda activate") {
		t.Fatalf("shell-mode conda script should not rely on conda activate:\n%s", script)
	}
	want := "conda run --no-capture-output -n 'defect-yolo' bash '-lc' 'python scripts/train_mdd_yolo.py --epochs 100'"
	if !strings.Contains(script, want) {
		t.Fatalf("command script missing conda run wrapper %q\n%s", want, script)
	}
	if !strings.Contains(script, `if ! command -v conda >/dev/null 2>&1; then`) {
		t.Fatalf("command script missing conda availability check:\n%s", script)
	}
}

func TestClassifyRunFailure(t *testing.T) {
	tests := []struct {
		name     string
		code     int
		stdout   string
		stderr   string
		wantKind string
	}{
		{
			name:     "module missing",
			code:     1,
			stderr:   "ModuleNotFoundError: No module named 'optuna'",
			wantKind: store.RunFailureDependencyError,
		},
		{
			name:     "conda mismatch import path",
			code:     1,
			stderr:   "ImportError: libxcb.so.1: cannot open shared object file\n  File \"/opt/conda/lib/python3.10/site-packages/cv2/__init__.py\"",
			wantKind: store.RunFailureEnvMismatch,
		},
		{
			name:     "data missing",
			code:     1,
			stderr:   "FileNotFoundError: [Errno 2] No such file or directory: 'dataset/train.csv'",
			wantKind: store.RunFailureDataMissing,
		},
		{
			name:     "killed 137",
			code:     137,
			stderr:   "Killed",
			wantKind: store.RunFailureKilled137,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotKind, gotReason := classifyRunFailure(tt.code, tt.stdout, tt.stderr)
			if gotKind != tt.wantKind {
				t.Fatalf("kind = %q, want %q (reason %q)", gotKind, tt.wantKind, gotReason)
			}
			if strings.TrimSpace(gotReason) == "" {
				t.Fatal("reason is empty")
			}
		})
	}
}

func TestBuildCommandScriptProjectCondaUsesCondaRun(t *testing.T) {
	req := SubmitRequest{
		Program: "bash",
		Args:    []string{"-lc", "python scripts/train.py"},
	}
	profile := &store.ProjectProfile{
		ResolvedEnv: ProjectEnvConda,
		EnvName:     "defect-yolo",
		ResolvedCwd: "/workspace/project",
	}
	script := buildCommandScript(req, "", "/opt/conda", "/opt/conda/etc/profile.d/conda.sh", "/workspace", nil, profile)
	if strings.Contains(script, "conda activate") {
		t.Fatalf("project conda script should not rely on conda activate:\n%s", script)
	}
	want := "conda run --no-capture-output -n 'defect-yolo' bash '-lc' 'python scripts/train.py'"
	if !strings.Contains(script, want) {
		t.Fatalf("project command script missing conda run wrapper %q\n%s", want, script)
	}
}

func TestBuildCommandScriptProjectCondaPrefixUsesCondaRunPrefix(t *testing.T) {
	req := SubmitRequest{
		Program: "bash",
		Args:    []string{"-lc", "python scripts/train.py"},
	}
	profile := &store.ProjectProfile{
		ResolvedEnv: ProjectEnvConda,
		EnvName:     "/opt/conda/envs/defect-yolo",
		ResolvedCwd: "/workspace/project",
	}
	script := buildCommandScript(req, "", "/opt/conda", "/opt/conda/etc/profile.d/conda.sh", "/workspace", nil, profile)
	if strings.Contains(script, "conda activate") {
		t.Fatalf("project conda prefix script should not rely on conda activate:\n%s", script)
	}
	if strings.Contains(script, " -n '/opt/conda/envs/defect-yolo' ") {
		t.Fatalf("project conda prefix script used env-name flag:\n%s", script)
	}
	want := "conda run --no-capture-output -p '/opt/conda/envs/defect-yolo' bash '-lc' 'python scripts/train.py'"
	if !strings.Contains(script, want) {
		t.Fatalf("project command script missing conda prefix wrapper %q\n%s", want, script)
	}
}

func TestWrapperScriptStreamsStdoutBeforeNewline(t *testing.T) {
	bash, err := osexec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	runDir := t.TempDir()
	commandPath := filepath.Join(runDir, "command.sh")
	if err := os.WriteFile(commandPath, []byte("#!/usr/bin/env bash\nprintf 'progress 1\\r'\nprintf 'warning 1\\r' >&2\nsleep 1\nprintf 'progress 2\\r'\nprintf 'warning 2\\r' >&2\nprintf '\\ndone\\n'\n"), 0o755); err != nil {
		t.Fatalf("write command.sh: %v", err)
	}
	wrapperPath := filepath.Join(runDir, "wrapper.sh")
	if err := os.WriteFile(wrapperPath, []byte(WrapperScript), 0o755); err != nil {
		t.Fatalf("write wrapper.sh: %v", err)
	}

	cmd := osexec.Command(bash, wrapperPath, runDir)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	stdoutPath := filepath.Join(runDir, "logs", "stdout.log")
	terminalPath := filepath.Join(runDir, "logs", "terminal.log")
	deadline := time.Now().Add(300 * time.Millisecond)
	for {
		data, _ := os.ReadFile(stdoutPath)
		if strings.Contains(string(data), "progress 1\r") {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("stdout.log did not receive carriage-return progress before newline; got %q", string(data))
		}
		time.Sleep(20 * time.Millisecond)
	}

	deadline = time.Now().Add(300 * time.Millisecond)
	for {
		terminal, _ := os.ReadFile(terminalPath)
		if strings.Contains(string(terminal), "[stdout] progress 1") && strings.Contains(string(terminal), "[stderr] warning 1") {
			break
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("wrapper exited before live terminal assertion: %v", err)
			}
			t.Fatalf("wrapper exited before terminal.log received live progress:\n%s", string(terminal))
		default:
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("terminal.log did not receive carriage-return progress while wrapper was running; got %q", string(terminal))
		}
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wrapper exited: %v", err)
		}
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("wrapper did not exit")
	}

	deadline = time.Now().Add(500 * time.Millisecond)
	for {
		terminal, err := os.ReadFile(terminalPath)
		if err != nil {
			t.Fatalf("read terminal.log: %v", err)
		}
		if strings.Contains(string(terminal), "[stdout] progress 1") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal.log missing prefixed progress line:\n%s", string(terminal))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestResolveProjectProfileUsesCachedProfile(t *testing.T) {
	ctx := context.Background()
	db := newExecutorTestStore(t)
	res := &store.Resource{
		ID:      "rsrc_profile",
		Name:    "profile-resource",
		Type:    store.ResourceTypeSSH,
		Host:    "127.0.0.1",
		Port:    1,
		User:    "nobody",
		RootDir: "/workspace",
		Status:  store.ResourceStatusIdle,
	}
	if err := db.CreateResource(ctx, res); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	if err := db.SaveProjectProfile(ctx, &store.ProjectProfile{
		ResourceID:   res.ID,
		ResourceName: res.Name,
		Cwd:          "/workspace/project",
		EnvStrategy:  ProjectEnvAuto,
		ResolvedEnv:  ProjectEnvVenv,
		Python:       "/workspace/project/.venv/bin/python",
		ResolvedCwd:  "/workspace/project",
		PythonOK:     true,
		TorchOK:      true,
		CUDA:         "ok",
		CUDAOK:       true,
		Logs:         []string{"logs/**/*.log"},
		Metrics:      []string{"runs/**/*.csv"},
	}); err != nil {
		t.Fatalf("SaveProjectProfile: %v", err)
	}

	exec := NewExecutor(NewSSHPool(1*time.Millisecond), db)
	profile, err := exec.ResolveProjectProfile(ctx, res, "/workspace/project", ProjectEnvAuto, "", false)
	if err != nil {
		t.Fatalf("ResolveProjectProfile: %v", err)
	}
	if profile.Python != "/workspace/project/.venv/bin/python" || profile.ResolvedEnv != ProjectEnvVenv {
		t.Fatalf("unexpected cached profile: %#v", profile)
	}
}

func TestResolveProjectProfileRefreshBypassesCache(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	db := newExecutorTestStore(t)
	res := &store.Resource{
		ID:      "rsrc_refresh",
		Name:    "refresh-resource",
		Type:    store.ResourceTypeSSH,
		Host:    "127.0.0.1",
		Port:    1,
		User:    "nobody",
		RootDir: "/workspace",
		Status:  store.ResourceStatusIdle,
	}
	if err := db.CreateResource(ctx, res); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	if err := db.SaveProjectProfile(ctx, &store.ProjectProfile{
		ResourceID:   res.ID,
		ResourceName: res.Name,
		Cwd:          "/workspace/project",
		EnvStrategy:  ProjectEnvAuto,
		ResolvedEnv:  ProjectEnvVenv,
		Python:       "/workspace/project/.venv/bin/python",
		ResolvedCwd:  "/workspace/project",
		PythonOK:     true,
	}); err != nil {
		t.Fatalf("SaveProjectProfile: %v", err)
	}

	exec := NewExecutor(NewSSHPool(1*time.Millisecond), db)
	_, err := exec.ResolveProjectProfile(ctx, res, "/workspace/project", ProjectEnvAuto, "", true)
	if err == nil {
		t.Fatal("expected refresh to bypass cache and attempt remote detection")
	}
}

func TestUsableCachedProjectProfile(t *testing.T) {
	if usableCachedProjectProfile(nil) {
		t.Fatal("nil profile should not be usable")
	}
	if usableCachedProjectProfile(&store.ProjectProfile{ResolvedEnv: ProjectEnvVenv, ResolvedCwd: "/p"}) {
		t.Fatal("profile without python_ok should not be usable")
	}
	if !usableCachedProjectProfile(&store.ProjectProfile{ResolvedEnv: ProjectEnvVenv, ResolvedCwd: "/p", PythonOK: true}) {
		t.Fatal("valid profile should be usable")
	}
}

func TestCancelFinishedRunIsRejected(t *testing.T) {
	ctx := context.Background()
	db := newExecutorTestStore(t)
	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_cancel",
		Name:    "cancel-resource",
		Type:    store.ResourceTypeSSH,
		Host:    "127.0.0.1",
		Port:    22,
		User:    "nobody",
		RootDir: "/workspace",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID:         "run_done",
		ResourceID: "rsrc_cancel",
		Status:     store.RunStatusSucceeded,
		Command:    "python train.py",
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	exec := NewExecutor(NewSSHPool(0), db)
	err := exec.Cancel(ctx, "run_done")
	if err == nil || !strings.Contains(err.Error(), "already finished") {
		t.Fatalf("Cancel finished run error = %v, want already finished", err)
	}
	got, err := db.GetRun(ctx, "run_done")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != store.RunStatusSucceeded {
		t.Fatalf("status changed to %q", got.Status)
	}
}

func TestCancelRaceWithNaturalCompletionPreservesSucceededStatus(t *testing.T) {
	ctx := context.Background()
	db := newExecutorTestStore(t)
	resource := &store.Resource{
		ID: "rsrc_cancel_race", Name: "cancel-race", Type: store.ResourceTypeSSH,
		Host: "127.0.0.1", Port: 22, User: "nobody", RootDir: "/workspace",
		Status: store.ResourceStatusBusy,
	}
	if err := db.CreateResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	run := &store.Run{
		ID: "run_cancel_race", ResourceID: resource.ID, Status: store.RunStatusRunning,
		Kind: store.RunKindSmoke, Command: "python smoke.py", TmuxSession: "aexp_cancel_race",
		RemoteRunDir: "/workspace/.aexp/runs/run_cancel_race",
	}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	runner := &fakeRemoteRunner{}
	runner.execFn = func(command string) (string, string, error) {
		if strings.Contains(command, "tmux send-keys") {
			once.Do(func() {
				current, err := db.GetRun(ctx, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				current.Status = store.RunStatusSucceeded
				current.ExitCode = sql.NullInt64{Int64: 0, Valid: true}
				current.FinishedAt = sql.NullTime{Time: time.Now().UTC(), Valid: true}
				if err := db.UpdateRun(ctx, current); err != nil {
					t.Fatal(err)
				}
			})
			return "", "", nil
		}
		if strings.Contains(command, "tmux has-session") {
			return "0\n", "", nil
		}
		return "", "", nil
	}
	exec := NewExecutor(NewSSHPool(time.Millisecond), db)
	exec.runner = runner
	exec.cancelGrace = time.Millisecond
	err := exec.Cancel(ctx, run.ID)
	if err == nil || !strings.Contains(err.Error(), "already finished (status: succeeded)") {
		t.Fatalf("cancel race error = %v", err)
	}
	stored, err := db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != store.RunStatusSucceeded || !stored.FinishedAt.Valid {
		t.Fatalf("natural completion was overwritten: %#v", stored)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		if strings.Contains(call, "echo 'cancelled'") {
			t.Fatalf("cancel marker written after natural completion: %s", call)
		}
	}
}

func TestCheckRunStatusDoesNotPersistSSHUnreachableOnProbeFailure(t *testing.T) {
	ctx := context.Background()
	probeCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	db := newExecutorTestStore(t)
	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_unreachable_probe",
		Name:    "unreachable-probe-resource",
		Type:    store.ResourceTypeSSH,
		Host:    "127.0.0.1",
		Port:    1,
		User:    "nobody",
		RootDir: "/workspace",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID:           "run_unreachable_probe",
		ResourceID:   "rsrc_unreachable_probe",
		Status:       store.RunStatusRunning,
		Command:      "python train.py",
		TmuxSession:  "aexp_run_unreachable_probe",
		RemoteRunDir: "/workspace/.aexp/runs/run_unreachable_probe",
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	exec := NewExecutor(NewSSHPool(50*time.Millisecond), db)
	if _, err := exec.CheckRunStatus(probeCtx, "run_unreachable_probe"); err == nil {
		t.Fatal("expected probe failure")
	}
	got, err := db.GetRun(context.Background(), "run_unreachable_probe")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != store.RunStatusRunning {
		t.Fatalf("status = %q, want running; transient SSH failure must not overwrite run lifecycle", got.Status)
	}
	if got.StatusSource != store.RunStatusSourceLocalCache || got.StatusCheckedAt == nil || got.StatusCheckError == "" || got.StatusFreshness != store.RunStatusFreshnessStale {
		t.Fatalf("failed status probe observation = source %q checked %v error %q freshness %q", got.StatusSource, got.StatusCheckedAt, got.StatusCheckError, got.StatusFreshness)
	}
}
