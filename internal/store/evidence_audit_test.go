package store

import (
	"context"
	"testing"
)

func TestAuditEvidenceChainValidatesV2ResultSnapshotProvenance(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_audit_v2")
	run, snapshot := createReadyPromotionRun(t, s, project.ID, "audit_v2")
	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "hypothesis", Type: EvidenceNodeClaim, DataJSON: `{"claimKind":"hypothesis"}`},
			{ID: "design", Type: EvidenceNodeExperiment},
			{ID: "result", Type: EvidenceNodeClaim, SourceSnapshotIDs: []string{snapshot.ID}, DataJSON: `{"claimKind":"result","resultDisposition":"conclusion"}`},
			{ID: "conclusion", Type: EvidenceNodeConclusion},
		},
		Edges: []EvidenceChainEdge{
			{ID: "hypothesis_design", Type: EvidenceEdgeNextStep, SourceNodeID: "hypothesis", TargetNodeID: "design"},
			{ID: "design_result", Type: EvidenceEdgeNextStep, SourceNodeID: "design", TargetNodeID: "result"},
			{ID: "result_conclusion", Type: EvidenceEdgeSupports, SourceNodeID: "result", TargetNodeID: "conclusion"},
		},
	}
	if _, err := s.SaveEvidenceChainGraphCAS(ctx, topic.ID, graph, EvidenceGraphSaveOptions{ExpectedRevision: 0, Actor: "test", SourceKind: "test"}); err != nil {
		t.Fatal(err)
	}
	report, err := s.AuditEvidenceChain(ctx, topic.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != evidenceChainAuditSchemaVersion || report.Eligible || evidenceBlockerByCode(report.Blockers, "EVIDENCE_RELEASE_MISSING") == nil {
		t.Fatalf("unreleased snapshot audit = %#v", report)
	}
	if report.V2ComplianceStatus != "v2_compliant" || report.PublicationStatus != "publication_blocked" || report.ResearchHealth.PolicyVersion != EvidenceResearchHealthPolicyVersion {
		t.Fatalf("unreleased snapshot statuses = %#v", report)
	}
	if err := s.AppendEvidenceRelease(ctx, &EvidenceRelease{SnapshotID: snapshot.ID, State: EvidenceReleaseReleased, AggregateResultJSON: `{}`, GateResultJSON: `{"passed":true}`}); err != nil {
		t.Fatal(err)
	}
	report, err = s.AuditEvidenceChain(ctx, topic.ID)
	if err != nil || !report.Eligible || len(report.Blockers) != 0 {
		t.Fatalf("released snapshot audit = %#v err=%v run=%s", report, err, run.ID)
	}
	if report.ReadabilityStatus != "v2_readable" || report.V2ComplianceStatus != "v2_compliant" || report.PublicationStatus != "publication_ready" || report.PublicationResultCount != 1 {
		t.Fatalf("released snapshot statuses = %#v", report)
	}
}

func TestAuditEvidenceChainReportsResultContractAndMissingSourcesDeterministically(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, topic := createEvidenceWorkspaceProject(t, s, "project_audit_result_contract")
	graph := EvidenceChainGraph{Nodes: []EvidenceChainNode{
		{ID: "result_b", Type: EvidenceNodeClaim, SourceRunIDs: []string{"run_missing_b"}, DataJSON: `{"claimKind":"result","resultDisposition":"pending"}`},
		{ID: "result_a", Type: EvidenceNodeClaim, SourceSnapshotIDs: []string{"snapshot_missing_a"}, DataJSON: `{"claimKind":"result","resultDisposition":"issue"}`},
	}}
	if _, err := s.SaveEvidenceChainGraphCAS(ctx, topic.ID, graph, EvidenceGraphSaveOptions{ExpectedRevision: 0, Actor: "test", SourceKind: "test"}); err != nil {
		t.Fatal(err)
	}
	first, err := s.AuditEvidenceChain(ctx, topic.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AuditEvidenceChain(ctx, topic.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Eligible || evidenceBlockerByCode(first.Blockers, "RUN_NOT_FOUND") == nil || evidenceBlockerByCode(first.Blockers, "SNAPSHOT_NOT_FOUND") == nil ||
		evidenceBlockerByCode(first.Blockers, "RESULT_PENDING_REASON_REQUIRED") == nil || evidenceBlockerByCode(first.Blockers, "RESULT_DISPOSITION_EDGE_MISMATCH") == nil {
		t.Fatalf("result audit did not enumerate blockers: %#v", first)
	}
	if len(first.Blockers) != len(second.Blockers) {
		t.Fatalf("non-deterministic blocker count: %#v %#v", first.Blockers, second.Blockers)
	}
	for i := range first.Blockers {
		if first.Blockers[i] != second.Blockers[i] {
			t.Fatalf("blocker order differs at %d: %#v %#v", i, first.Blockers, second.Blockers)
		}
	}
}

func TestAuditEvidenceChainTreatsLegacyRunReadinessAsWarning(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_audit_legacy_warning")
	resource := &Resource{ID: "resource_legacy_warning", Name: "legacy-warning", Type: "ssh", Host: "localhost", RootDir: "/tmp"}
	if err := s.CreateResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: "run_legacy_warning", ResourceID: resource.ID, ProjectID: project.ID, Status: RunStatusSucceeded, Kind: RunKindPilot, Command: "true"}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	graph := EvidenceChainGraph{Nodes: []EvidenceChainNode{{ID: "legacy", Type: EvidenceNodeRun, RunID: run.ID}}}
	if _, err := s.SaveEvidenceChainGraphCAS(ctx, topic.ID, graph, EvidenceGraphSaveOptions{ExpectedRevision: 0, Actor: "legacy", SourceKind: "migration"}); err != nil {
		t.Fatal(err)
	}
	report, err := s.AuditEvidenceChain(ctx, topic.ID)
	if err != nil || !report.Eligible || len(report.Blockers) != 0 || len(report.Warnings) == 0 {
		t.Fatalf("legacy warning audit = %#v err=%v", report, err)
	}
	if report.ReadabilityStatus != "legacy_readable" || report.V2ComplianceStatus != "legacy_mixed" || report.PublicationStatus != "not_applicable" {
		t.Fatalf("legacy status split = %#v", report)
	}
}

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

func TestAuditEvidenceChainReportsLegacyResultOutcomeBypass(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, topic := createEvidenceWorkspaceProject(t, s, "project_audit_result_bypass")
	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "legacy_result", Type: EvidenceNodeClaim, DataJSON: `{"claimKind":"result"}`},
			{ID: "legacy_hypothesis", Type: EvidenceNodeHypothesis},
		},
		Edges: []EvidenceChainEdge{{
			ID: "legacy_result_supports_hypothesis", Type: EvidenceEdgeSupports,
			SourceNodeID: "legacy_result", TargetNodeID: "legacy_hypothesis",
		}},
	}
	if _, err := s.SaveEvidenceChainGraphCAS(ctx, topic.ID, graph, EvidenceGraphSaveOptions{ExpectedRevision: 0, Actor: "legacy", SourceKind: "migration"}); err != nil {
		t.Fatal(err)
	}
	report, err := s.AuditEvidenceChain(ctx, topic.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Eligible || evidenceBlockerByCode(report.Blockers, "LEGACY_RESULT_BYPASS") == nil {
		t.Fatalf("audit did not report Result stage bypass: %#v", report)
	}
}

func TestAuditEvidenceChainWarnsAboutLegacyThreadBranchBypass(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, topic := createEvidenceWorkspaceProject(t, s, "project_audit_branch_bypass")
	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "hypothesis", Type: EvidenceNodeClaim, DataJSON: `{"claimKind":"hypothesis"}`},
			{ID: "issue", Type: EvidenceNodeIssue},
			{ID: "legacy_plan", Type: EvidenceNodePlan},
		},
		Edges: []EvidenceChainEdge{
			{ID: "hypothesis_issue", Type: EvidenceEdgeRevealsIssue, SourceNodeID: "hypothesis", TargetNodeID: "issue"},
			{ID: "legacy_branch", Type: EvidenceEdgeNextStep, SourceNodeID: "issue", TargetNodeID: "legacy_plan"},
		},
	}
	if _, err := s.SaveEvidenceChainGraphCAS(ctx, topic.ID, graph, EvidenceGraphSaveOptions{ExpectedRevision: 0, Actor: "legacy", SourceKind: "migration"}); err != nil {
		t.Fatal(err)
	}
	report, err := s.AuditEvidenceChain(ctx, topic.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, warning := range report.Warnings {
		if warning.Code == "LEGACY_THREAD_BRANCH_BYPASS" && warning.EdgeID == "legacy_branch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit did not retain readable branch debt as a warning: %#v", report)
	}
}
