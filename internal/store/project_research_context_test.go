package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectResearchContextIsCompactAndDrillDownOriented(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createTestProject(t, s, "project_context", "Context Project")
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_context", Name: "context", Type: "ssh", Host: "localhost", RootDir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"run_context_a", "run_context_b"} {
		if err := s.CreateRun(ctx, &Run{ID: runID, ProjectID: "project_context", ResourceID: "rsrc_context", Name: runID, Status: RunStatusSucceeded, Kind: RunKindFormal, Command: strings.Repeat("python train.py ", 100)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateProjectJournalEntry(ctx, &ProjectJournalEntry{
		ID: "journal_context", ProjectID: "project_context", Actor: "agent", Title: "Matched baseline complete",
		BodyMD:     strings.Repeat("full working reasoning must stay out of the compact context ", 80),
		NextAction: "Run the controlled ablation", RunIDs: []string{"run_context_a"},
	}); err != nil {
		t.Fatal(err)
	}
	topic := &EvidenceChain{
		ID: "chain_context_topic", Title: "Retrieval quality", Description: "When does transparent retrieval remain competitive?",
		ProjectID: "project_context", Role: "secondary", Status: "active",
		RoutingHints: EvidenceGraphRoutingHints{Keywords: []string{"retrieval", "matched baseline"}},
	}
	if err := s.CreateEvidenceChain(ctx, topic); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveEvidenceChainGraph(ctx, topic.ID, EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "hypothesis_context", Type: EvidenceNodeClaim, Title: "Transparent retrieval remains competitive", DataJSON: `{"claimKind":"hypothesis"}`},
			{ID: "design_context", Type: EvidenceNodeExperiment, Title: "Matched five-seed comparison", DataJSON: `{}`},
		},
		Edges: []EvidenceChainEdge{{ID: "edge_context", SourceNodeID: "hypothesis_context", TargetNodeID: "design_context", Type: EvidenceEdgeNextStep}},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetProjectResearchContext(ctx, "project_context", ProjectResearchContextOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ContractVersion != ProjectResearchContextVersion || got.Project.ID != "project_context" {
		t.Fatalf("context identity = %#v", got)
	}
	if len(got.Topics) != 1 || got.Topics[0].ID != topic.ID || got.Topics[0].ThreadCount != 1 {
		t.Fatalf("topic summaries = %#v", got.Topics)
	}
	if got.Runs.Total != 2 || len(got.Runs.Recent) != 2 {
		t.Fatalf("run context = %#v", got.Runs)
	}
	if len(got.Journal) != 1 || got.Journal[0].ID != "journal_context" || got.Journal[0].NextActionStatus != JournalNextActionOpen {
		t.Fatalf("journal summaries = %#v", got.Journal)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 8*1024 {
		t.Fatalf("default context is too large: %d bytes", len(encoded))
	}
	if strings.Contains(string(encoded), "full working reasoning") || strings.Contains(string(encoded), "python train.py") {
		t.Fatalf("compact context leaked Journal body or full command: %s", encoded)
	}
	if !strings.Contains(string(encoded), "aexp_get_evidence_thread_map") || !strings.Contains(string(encoded), "aexp_get_project_journal_entry") {
		t.Fatalf("context is missing explicit next reads: %s", encoded)
	}
}

func TestProjectResearchContextRejectsUnknownProject(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetProjectResearchContext(context.Background(), "missing", ProjectResearchContextOptions{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v", err)
	}
}
