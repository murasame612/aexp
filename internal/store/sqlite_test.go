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
		ID:            "run_test001",
		ResourceID:    "rsrc_r01",
		Name:          "test-run",
		Status:        RunStatusCreated,
		Command:       "python train.py",
		Cwd:           "/ws/project",
		CondaEnv:      "tslib",
		TargetEnv:     "defect-yolo",
		ForceReason:   "preempt stale smoke run before formal rerun",
		PreemptRunID:  "run_old001",
		PreemptSave:   true,
		GitRepoRoot:   "/repo",
		GitRemoteURL:  "https://github.com/example/project.git",
		GitBranch:     "main",
		GitCommit:     "abcdef1234567890",
		GitDirty:      true,
		GitStatus:     " M train.py",
		GitDiffHash:   "sha256:abc",
		GitDiffPath:   "/tmp/run.patch",
		GitAllowDirty: true,
		FailureKind:   RunFailureImportError,
		FailureReason: "libxcb.so.1 missing",
		UIEventsPath:  "aexp-events.jsonl",
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
	if got.TargetEnv != "defect-yolo" || got.ForceReason == "" || got.PreemptRunID != "run_old001" || !got.PreemptSave || got.FailureKind != RunFailureImportError || got.FailureReason != "libxcb.so.1 missing" {
		t.Errorf("run semantic fields not persisted: %#v", got)
	}
	if got.GitRepoRoot != "/repo" || got.GitRemoteURL != "https://github.com/example/project.git" || got.GitBranch != "main" || got.GitCommit != "abcdef1234567890" || !got.GitDirty || got.GitStatus != " M train.py" || got.GitDiffHash != "sha256:abc" || got.GitDiffPath != "/tmp/run.patch" || !got.GitAllowDirty {
		t.Errorf("run git fields not persisted: %#v", got)
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

	if err := s.SaveProjectRunCard(ctx, &ProjectRunCard{
		ID:            "card_ignored",
		ProjectID:     "dam-imputation",
		ProjectName:   "Dam Imputation",
		RunID:         "run_card01",
		Question:      "Does CAF beat IR?",
		Verdict:       "Needs rerun with seed control.",
		EvidenceLevel: "C",
	}); err != nil {
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

	chain := &EvidenceChain{ID: "chain_ir_gate", Title: "IR gate evidence", Description: "Fusion reasoning"}
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
	deleted, err := s.GetEvidenceChain(ctx, "chain_ir_gate")
	if err != nil || deleted != nil {
		t.Fatalf("deleted chain = %#v err=%v, want nil", deleted, err)
	}
	emptyGraph, err := s.GetEvidenceChainGraph(ctx, "chain_ir_gate")
	if err != nil {
		t.Fatalf("GetEvidenceChainGraph after delete: %v", err)
	}
	if len(emptyGraph.Nodes) != 0 || len(emptyGraph.Edges) != 0 {
		t.Fatalf("graph after delete = %#v, want empty", emptyGraph)
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
