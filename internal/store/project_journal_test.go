package store

import (
	"context"
	"errors"
	"testing"
)

func TestProjectJournalSupportsProjectNotesRunLinksAndNextActions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createTestProject(t, s, "project_journal", "Project Journal")
	if err := s.CreateResource(ctx, &Resource{
		ID: "rsrc_journal", Name: "journal", Type: "ssh", Host: "localhost", RootDir: "/workspace",
	}); err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"run_journal_a", "run_journal_b"} {
		if err := s.CreateRun(ctx, &Run{
			ID: runID, ProjectID: "project_journal", ResourceID: "rsrc_journal",
			Status: RunStatusSucceeded, Command: "true",
		}); err != nil {
			t.Fatal(err)
		}
	}

	standalone := &ProjectJournalEntry{
		ID: "journal_standalone", ProjectID: "project_journal", Actor: "human",
		Title: "Change direction", BodyMD: "The protocol needs a clean split.",
	}
	if err := s.CreateProjectJournalEntry(ctx, standalone); err != nil {
		t.Fatal(err)
	}
	if standalone.NextActionStatus != JournalNextActionNone || len(standalone.RunIDs) != 0 {
		t.Fatalf("standalone entry = %#v", standalone)
	}

	linked := &ProjectJournalEntry{
		ID: "journal_linked", ProjectID: "project_journal", Actor: "agent",
		Title: "Matched baseline is ready", BodyMD: "Both seeds finished.",
		NextAction: "Run the semantic ablation",
		RunIDs:     []string{"run_journal_a", "run_journal_b", "run_journal_a"},
		LiteratureRefs: []LiteratureReference{
			{
				SourceKind: "frozen_corpus", ZoteroItemKey: "ITEM123", ZoteroURI: "zotero://select/library/items/ITEM123",
				PageLabel: "12", CorpusRevision: "corpus_abc", ChunkSHA256: "sha256:abc",
			},
			{
				SourceKind: "zotero_live", ZoteroItemKey: "ITEM456", ZoteroURI: "zotero://select/library/items/ITEM456",
				ItemVersion: 7, LibraryVersion: 42,
			},
		},
	}
	if err := s.CreateProjectJournalEntry(ctx, linked); err != nil {
		t.Fatal(err)
	}
	if linked.NextActionStatus != JournalNextActionOpen || len(linked.RunIDs) != 2 {
		t.Fatalf("linked entry = %#v", linked)
	}
	gotLinked, err := s.GetProjectJournalEntry(ctx, linked.ID)
	if err != nil || gotLinked == nil || len(gotLinked.LiteratureRefs) != 2 || gotLinked.LiteratureRefs[0].CorpusRevision != "corpus_abc" || gotLinked.LiteratureRefs[1].ItemVersion != 7 {
		t.Fatalf("literature references = %#v err=%v", gotLinked, err)
	}

	forRun, err := s.ListProjectJournalEntries(ctx, ProjectJournalFilter{RunID: "run_journal_a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(forRun) != 1 || forRun[0].ID != linked.ID {
		t.Fatalf("run backlinks = %#v", forRun)
	}

	open, err := s.ListProjectJournalEntries(ctx, ProjectJournalFilter{
		ProjectID: "project_journal", NextActionStatus: JournalNextActionOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ID != linked.ID {
		t.Fatalf("open next actions = %#v", open)
	}
	updated, err := s.UpdateProjectJournalNextActionStatus(ctx, linked.ID, JournalNextActionDone)
	if err != nil {
		t.Fatal(err)
	}
	if updated.NextActionStatus != JournalNextActionDone {
		t.Fatalf("updated status = %q", updated.NextActionStatus)
	}
}

func TestProjectJournalRejectsUnpinnedLiveZoteroReference(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createTestProject(t, s, "project_bad_live_literature", "Bad live literature")
	err := s.CreateProjectJournalEntry(ctx, &ProjectJournalEntry{
		ID: "journal_bad_live_literature", ProjectID: "project_bad_live_literature", Title: "Missing versions",
		LiteratureRefs: []LiteratureReference{{SourceKind: "zotero_live", ZoteroItemKey: "ITEM", ZoteroURI: "zotero://select/library/items/ITEM"}},
	})
	var validation *EvidenceGraphValidationError
	if !errors.As(err, &validation) || validation.Code != "LITERATURE_REFERENCE_INVALID" {
		t.Fatalf("error = %v, want LITERATURE_REFERENCE_INVALID", err)
	}
}

func TestProjectJournalRejectsUnfrozenCorpusReference(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createTestProject(t, s, "project_bad_literature", "Bad literature")
	err := s.CreateProjectJournalEntry(ctx, &ProjectJournalEntry{
		ID: "journal_bad_literature", ProjectID: "project_bad_literature", Title: "Missing hash",
		LiteratureRefs: []LiteratureReference{{SourceKind: "frozen_corpus", ZoteroItemKey: "ITEM", ZoteroURI: "zotero://select/library/items/ITEM", CorpusRevision: "corpus_abc"}},
	})
	var validation *EvidenceGraphValidationError
	if !errors.As(err, &validation) || validation.Code != "LITERATURE_REFERENCE_INVALID" {
		t.Fatalf("error = %v, want LITERATURE_REFERENCE_INVALID", err)
	}
}

func TestProjectJournalRejectsCrossProjectRun(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createTestProject(t, s, "project_left", "Left")
	createTestProject(t, s, "project_right", "Right")
	if err := s.CreateResource(ctx, &Resource{
		ID: "rsrc_cross_journal", Name: "cross-journal", Type: "ssh", Host: "localhost", RootDir: "/workspace",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, &Run{
		ID: "run_right", ProjectID: "project_right", ResourceID: "rsrc_cross_journal",
		Status: RunStatusSucceeded, Command: "true",
	}); err != nil {
		t.Fatal(err)
	}
	err := s.CreateProjectJournalEntry(ctx, &ProjectJournalEntry{
		ID: "journal_cross", ProjectID: "project_left", Title: "Wrong project",
		RunIDs: []string{"run_right"},
	})
	var validation *EvidenceGraphValidationError
	if !errors.As(err, &validation) || validation.Code != "RUN_PROJECT_MISMATCH" {
		t.Fatalf("error = %v, want RUN_PROJECT_MISMATCH", err)
	}
}
