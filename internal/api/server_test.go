package api

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/ziwu/aexp/internal/store"
)

func TestLogReadErrorKind(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "missing", err: fmt.Errorf("log file not found: /tmp/events.jsonl"), want: "file_missing"},
		{name: "unreachable", err: fmt.Errorf("resource mu is unreachable; cannot read remote log file events.jsonl"), want: "resource_unreachable"},
		{name: "timeout", err: context.DeadlineExceeded, want: "remote_timeout"},
		{name: "other", err: fmt.Errorf("ssh handshake failed"), want: "read_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logReadErrorKind(tt.err); got != tt.want {
				t.Fatalf("logReadErrorKind(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestProjectViewsEnrichCardsWithRunsAndMarks(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_project_api",
		Name:    "mu",
		Type:    "ssh",
		Host:    "localhost",
		RootDir: "/ws",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID:         "run_project_api",
		ResourceID: "rsrc_project_api",
		Name:       "caf-100e",
		Kind:       store.RunKindFormal,
		Status:     store.RunStatusSucceeded,
		Command:    "python train.py",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRunMark(ctx, &store.RunMark{
		ID:     "mark_project_api",
		RunID:  "run_project_api",
		Actor:  "agent",
		Kind:   "key_result",
		Title:  "CAF improves mAP",
		Reason: "mAP50-95 improved.",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProjectRunCard(ctx, &store.ProjectRunCard{
		ID:            "card_project_api",
		ProjectID:     "dam-imputation",
		ProjectName:   "Dam Imputation",
		RunID:         "run_project_api",
		Question:      "Does CAF help?",
		Verdict:       "CAF improves mAP.",
		EvidenceLevel: "B",
		KeyMetrics:    "mAP50-95=0.606",
		Important:     true,
	}); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	projects, err := srv.projectViews(ctx, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("projects = %#v, want one", projects)
	}
	project := projects[0]
	if project.ProjectID != "dam-imputation" || project.TotalCards != 1 || project.ImportantRuns != 1 || project.FormalRuns != 1 {
		t.Fatalf("unexpected project aggregate: %#v", project)
	}
	if len(project.Cards) != 1 || project.Cards[0].Run == nil || project.Cards[0].Run.Name != "caf-100e" {
		t.Fatalf("card run not enriched: %#v", project.Cards)
	}
	if len(project.Cards[0].Marks) != 1 || project.Cards[0].Marks[0].Title != "CAF improves mAP" {
		t.Fatalf("card marks not enriched: %#v", project.Cards[0].Marks)
	}
}

func TestProjectViewsIncludesUnassignedRuns(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_unassigned_api",
		Name:    "mu",
		Type:    "ssh",
		Host:    "localhost",
		RootDir: "/ws",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID:         "run_with_project_card",
		ResourceID: "rsrc_unassigned_api",
		Name:       "formal-carded",
		Kind:       store.RunKindFormal,
		Status:     store.RunStatusSucceeded,
		Command:    "python train.py",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID:         "run_without_project_card",
		ResourceID: "rsrc_unassigned_api",
		Name:       "scratch-check",
		Kind:       store.RunKindSetup,
		Status:     store.RunStatusRunning,
		GPUIndex:   -2,
		Command:    "ls",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProjectRunCard(ctx, &store.ProjectRunCard{
		ID:          "card_assigned_api",
		ProjectID:   "dam-imputation",
		ProjectName: "Dam Imputation",
		RunID:       "run_with_project_card",
		Verdict:     "Assigned run.",
	}); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	projects, err := srv.projectViews(ctx, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	var unassigned *projectView
	for i := range projects {
		if projects[i].ProjectID == unassignedProjectID {
			unassigned = &projects[i]
			break
		}
	}
	if unassigned == nil {
		t.Fatalf("missing unassigned project: %#v", projects)
	}
	if len(unassigned.Cards) != 1 {
		t.Fatalf("unassigned cards = %#v, want exactly one", unassigned.Cards)
	}
	if unassigned.Cards[0].Run == nil || unassigned.Cards[0].Run.ID != "run_without_project_card" {
		t.Fatalf("unexpected unassigned run: %#v", unassigned.Cards[0])
	}
	if unassigned.RunningRuns != 1 {
		t.Fatalf("unassigned running runs = %d, want 1", unassigned.RunningRuns)
	}
}
