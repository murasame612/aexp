package store

import (
	"context"
	"testing"
)

func TestReadyFormalV2ProposalAcceptsAndAuditsCleanly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_audit_ready")
	run, snapshot := createReadyPromotionRun(t, s, project.ID, "audit_ready")
	if err := s.AppendEvidenceRelease(ctx, &EvidenceRelease{
		SnapshotID: snapshot.ID, State: EvidenceReleaseReleased,
		AggregateResultJSON: `{}`, GateResultJSON: `{"passed":true}`,
	}); err != nil {
		t.Fatal(err)
	}
	proposal, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID,
		RoutingReason: "Formal result belongs to this focused audit topic.",
		SourceRunIDs:  []string{run.ID}, Actor: "test",
	}, &EvidenceGraphPatch{
		ChainID: topic.ID,
		Nodes: []EvidenceChainNode{
			{ID: "audit_ready_run", Type: EvidenceNodeRun, RunID: run.ID},
			{ID: "audit_ready_claim", Type: EvidenceNodeClaim, Title: "Ready claim"},
		},
		Edges: []EvidenceChainEdge{{
			ID: "audit_ready_supports", Type: EvidenceEdgeSupports,
			SourceNodeID: "audit_ready_run", TargetNodeID: "audit_ready_claim",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanEvidenceProposal(ctx, proposal.ID)
	if err != nil || !plan.Eligible || len(plan.Blockers) != 0 {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	accepted, err := s.ReviewEvidenceProposal(ctx, proposal.ID, "accept", "reviewer")
	if err != nil || accepted.Status != GraphProposalAccepted {
		t.Fatalf("accepted=%#v err=%v", accepted, err)
	}
	report, err := s.AuditEvidenceChain(ctx, topic.ID)
	if err != nil || !report.Eligible || len(report.Blockers) != 0 {
		t.Fatalf("audit=%#v err=%v", report, err)
	}
	if report.Revision != 1 || report.StoredGraphHash == "" || report.StoredGraphHash != report.CurrentGraphHash {
		t.Fatalf("audit identity=%#v", report)
	}
}

func TestAuditEvidenceChainReportsAllLegacyTopicProblems(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_audit_legacy")
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_audit_legacy", Name: "audit-legacy", Type: "ssh", Host: "localhost", RootDir: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: "run_audit_legacy", ResourceID: "rsrc_audit_legacy", ProjectID: project.ID, Status: RunStatusSucceeded, Kind: RunKindFormal, Command: "true"}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "legacy_run_a", Type: EvidenceNodeRun, RunID: run.ID},
			{ID: "legacy_run_b", Type: EvidenceNodeRun, RunID: run.ID},
		},
		Edges: []EvidenceChainEdge{
			{ID: "legacy_edge_a", Type: EvidenceEdgeNextStep, SourceNodeID: "legacy_run_a", TargetNodeID: "legacy_run_b"},
			{ID: "legacy_edge_b", Type: EvidenceEdgeNextStep, SourceNodeID: "legacy_run_b", TargetNodeID: "legacy_run_a"},
		},
	}
	if _, err := s.SaveEvidenceChainGraphCAS(ctx, topic.ID, graph, EvidenceGraphSaveOptions{ExpectedRevision: 0, Actor: "legacy", SourceKind: "migration"}); err != nil {
		t.Fatal(err)
	}
	report, err := s.AuditEvidenceChain(ctx, topic.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Eligible || evidenceBlockerByCode(report.Blockers, "DUPLICATE_RUN_NODE") == nil ||
		evidenceBlockerByCode(report.Blockers, "GRAPH_CYCLE") == nil {
		t.Fatalf("audit did not enumerate legacy blockers: %#v", report)
	}
}
