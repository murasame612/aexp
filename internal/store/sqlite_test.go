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
	now := time.Now()
	r.SSHStatus = ResourceSSHStatusFailed
	r.LastDoctorError = "ssh: EOF"
	r.LastCheckedAt = &now
	s.UpdateResource(ctx, r)
	got2, _ := s.GetResource(ctx, "rsrc_test001")
	if got2.Status != ResourceStatusIdle {
		t.Errorf("status = %q, want %q", got2.Status, ResourceStatusIdle)
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
		ID:           "run_test001",
		ResourceID:   "rsrc_r01",
		Name:         "test-run",
		Status:       RunStatusCreated,
		Command:      "python train.py",
		Cwd:          "/ws/project",
		CondaEnv:     "tslib",
		UIEventsPath: "aexp-events.jsonl",
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

	runs, _ := s.ListRuns(ctx, RunFilter{ResourceID: "rsrc_r01"})
	if len(runs) != 1 {
		t.Errorf("len(runs) = %d, want 1", len(runs))
	}
	if runs[0].UIEventsPath != "aexp-events.jsonl" {
		t.Errorf("list ui_events_path = %q, want %q", runs[0].UIEventsPath, "aexp-events.jsonl")
	}

	run.Status = RunStatusRunning
	run.UIEventsPath = "events/train.jsonl"
	run.StartedAt = sql.NullTime{Time: time.Now(), Valid: true}
	s.UpdateRun(ctx, run)
	got2, _ := s.GetRun(ctx, "run_test001")
	if got2.Status != RunStatusRunning {
		t.Errorf("status = %q, want %q", got2.Status, RunStatusRunning)
	}
	if got2.UIEventsPath != "events/train.jsonl" {
		t.Errorf("updated ui_events_path = %q, want %q", got2.UIEventsPath, "events/train.jsonl")
	}
}

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
		ID:       "mark_test001",
		RunID:    "run_mark01",
		Actor:    "agent",
		Kind:     "key_result",
		Title:    "Useful ablation",
		Reason:   "Validation loss improved.",
		Evidence: "logs/train.log",
	}
	if err := s.SaveRunMark(ctx, mark); err != nil {
		t.Fatalf("SaveRunMark: %v", err)
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

	marks, err := s.ListRunMarks(ctx, RunMarkFilter{RunID: "run_mark01", Limit: 10})
	if err != nil {
		t.Fatalf("ListRunMarks: %v", err)
	}
	if len(marks) != 2 {
		t.Errorf("marks = %d, want 2", len(marks))
	}

	keyResults, err := s.ListRunMarks(ctx, RunMarkFilter{Kind: "key_result", Limit: 10})
	if err != nil {
		t.Fatalf("ListRunMarks kind: %v", err)
	}
	if len(keyResults) != 1 || keyResults[0].ID != "mark_test001" {
		t.Errorf("key result filter = %#v, want mark_test001", keyResults)
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
