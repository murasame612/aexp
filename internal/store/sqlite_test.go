package store

import (
	"context"
	"database/sql"
	"path/filepath"
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

func TestResourceCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r := &Resource{
		ID:       "rsrc_test001",
		Name:     "test-resource",
		Type:     "ssh",
		Host:     "192.168.1.100",
		Port:     22,
		User:     "root",
		RootDir:  "/workspace",
		CondaEnv: "base",
		Tags:     "test,gpu",
		Status:   ResourceStatusUnknown,
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

	byName, _ := s.GetResourceByName(ctx, "test-resource")
	if byName == nil || byName.ID != "rsrc_test001" {
		t.Error("GetResourceByName failed")
	}

	resources, _ := s.ListResources(ctx)
	if len(resources) != 1 {
		t.Errorf("len(resources) = %d, want 1", len(resources))
	}

	r.Status = ResourceStatusIdle
	s.UpdateResource(ctx, r)
	got2, _ := s.GetResource(ctx, "rsrc_test001")
	if got2.Status != ResourceStatusIdle {
		t.Errorf("status = %q, want %q", got2.Status, ResourceStatusIdle)
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
		ID:         "run_test001",
		ResourceID: "rsrc_r01",
		Name:       "test-run",
		Status:     RunStatusCreated,
		Command:    "python train.py",
		Cwd:        "/ws/project",
		CondaEnv:   "tslib",
	}

	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	got, _ := s.GetRun(ctx, "run_test001")
	if got == nil || got.Command != "python train.py" {
		t.Error("GetRun failed")
	}

	runs, _ := s.ListRuns(ctx, RunFilter{ResourceID: "rsrc_r01"})
	if len(runs) != 1 {
		t.Errorf("len(runs) = %d, want 1", len(runs))
	}

	run.Status = RunStatusRunning
	run.StartedAt = sql.NullTime{Time: time.Now(), Valid: true}
	s.UpdateRun(ctx, run)
	got2, _ := s.GetRun(ctx, "run_test001")
	if got2.Status != RunStatusRunning {
		t.Errorf("status = %q, want %q", got2.Status, RunStatusRunning)
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
