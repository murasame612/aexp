package store

import (
	"context"
	"testing"
)

func TestEvidenceAuthoringWarningsKeepHealthyThreadClean(t *testing.T) {
	patch := EvidenceGraphPatch{
		Nodes: []EvidenceChainNode{
			{ID: "hypothesis", Type: EvidenceNodeClaim, DataJSON: `{"claimKind":"hypothesis"}`},
			{ID: "plan", Type: EvidenceNodePlan},
			{ID: "issue", Type: EvidenceNodeIssue},
			{ID: "child", Type: EvidenceNodeClaim, DataJSON: `{"claimKind":"hypothesis"}`},
		},
		Edges: []EvidenceChainEdge{
			{ID: "hypothesis-plan", Type: EvidenceEdgeNextStep, SourceNodeID: "hypothesis", TargetNodeID: "plan"},
			{ID: "issue-child", Type: EvidenceEdgeNextStep, SourceNodeID: "issue", TargetNodeID: "child"},
		},
	}
	graph := EvidenceChainGraph{Nodes: patch.Nodes, Edges: patch.Edges}
	plan := &EvidenceGraphProposalPlan{Warnings: make([]EvidenceGraphWarning, 0)}
	appendEvidenceAuthoringWarnings(graph, patch, plan)
	if len(plan.Warnings) != 0 {
		t.Fatalf("healthy thread warnings = %#v, want none", plan.Warnings)
	}
}

func TestEvidenceResearchBranchBypassRequiresCanonicalChildHypothesis(t *testing.T) {
	nodes := map[string]EvidenceChainNode{
		"issue":      {ID: "issue", Type: EvidenceNodeIssue},
		"conclusion": {ID: "conclusion", Type: EvidenceNodeConclusion},
		"hypothesis": {ID: "hypothesis", Type: EvidenceNodeClaim, DataJSON: `{"claimKind":"hypothesis"}`},
		"result":     {ID: "result", Type: EvidenceNodeClaim, DataJSON: `{"claimKind":"result"}`},
		"legacy-h":   {ID: "legacy-h", Type: EvidenceNodeHypothesis},
		"plan":       {ID: "plan", Type: EvidenceNodePlan},
		"experiment": {ID: "experiment", Type: EvidenceNodeExperiment},
	}
	for _, test := range []struct {
		name   string
		source string
		target string
		typeID string
		want   bool
	}{
		{name: "issue canonical hypothesis", source: "issue", target: "hypothesis", typeID: EvidenceEdgeNextStep},
		{name: "conclusion canonical hypothesis", source: "conclusion", target: "hypothesis", typeID: EvidenceEdgeNextStep},
		{name: "issue plan", source: "issue", target: "plan", typeID: EvidenceEdgeNextStep, want: true},
		{name: "conclusion experiment", source: "conclusion", target: "experiment", typeID: EvidenceEdgeNextStep, want: true},
		{name: "issue legacy hypothesis", source: "issue", target: "legacy-h", typeID: EvidenceEdgeNextStep, want: true},
		{name: "issue result", source: "issue", target: "result", typeID: EvidenceEdgeNextStep, want: true},
		{name: "neutral context", source: "issue", target: "plan", typeID: EvidenceEdgeRelatedTo},
	} {
		t.Run(test.name, func(t *testing.T) {
			edge := EvidenceChainEdge{ID: test.name, SourceNodeID: test.source, TargetNodeID: test.target, Type: test.typeID}
			if got := evidenceResearchBranchBypass(nodes, edge); got != test.want {
				t.Fatalf("bypass = %t, want %t", got, test.want)
			}
		})
	}
}

func TestEvidenceEligibilityScopesHistoricalBranchBypassToTouchedObjects(t *testing.T) {
	s := newTestStore(t)
	current := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "parent", Type: EvidenceNodeClaim, DataJSON: `{"claimKind":"hypothesis"}`},
			{ID: "issue", Type: EvidenceNodeIssue},
			{ID: "legacy-plan", Type: EvidenceNodePlan},
		},
		Edges: []EvidenceChainEdge{
			{ID: "parent-issue", Type: EvidenceEdgeRevealsIssue, SourceNodeID: "parent", TargetNodeID: "issue"},
			{ID: "legacy-bypass", Type: EvidenceEdgeNextStep, SourceNodeID: "issue", TargetNodeID: "legacy-plan"},
		},
	}
	unrelated := EvidenceGraphPatch{Nodes: []EvidenceChainNode{{ID: "note", Type: EvidenceNodeNote}}}
	merged := mergeEvidenceGraph(current, unrelated)
	plan := &EvidenceGraphProposalPlan{}
	if err := s.appendEvidenceEligibilityBlockers(context.Background(), "project", &current, &merged, unrelated, plan); err != nil {
		t.Fatal(err)
	}
	if hasEvidenceBlocker(plan.Blockers, "THREAD_BRANCH_HYPOTHESIS_REQUIRED") {
		t.Fatalf("unrelated patch inherited historical branch debt: %#v", plan.Blockers)
	}

	touched := EvidenceGraphPatch{UpsertNodes: []EvidenceChainNode{{ID: "issue", Type: EvidenceNodeIssue, Title: "Edited issue"}}}
	merged = mergeEvidenceGraph(current, touched)
	plan = &EvidenceGraphProposalPlan{}
	if err := s.appendEvidenceEligibilityBlockers(context.Background(), "project", &current, &merged, touched, plan); err != nil {
		t.Fatal(err)
	}
	if !hasEvidenceBlocker(plan.Blockers, "THREAD_BRANCH_HYPOTHESIS_REQUIRED") {
		t.Fatalf("editing a historical bypass endpoint should require repair: %#v", plan.Blockers)
	}
}

func TestEvidenceAuthoringWarningsAreAdvisoryAndPatchScoped(t *testing.T) {
	graph := EvidenceChainGraph{Nodes: []EvidenceChainNode{
		{ID: "historical_issue", Type: EvidenceNodeIssue},
		{ID: "new_issue", Type: EvidenceNodeIssue},
	}}
	patch := EvidenceGraphPatch{Nodes: []EvidenceChainNode{{ID: "new_issue", Type: EvidenceNodeIssue}}}
	plan := &EvidenceGraphProposalPlan{Eligible: true, Warnings: make([]EvidenceGraphWarning, 0)}
	appendEvidenceAuthoringWarnings(graph, patch, plan)
	if !plan.Eligible {
		t.Fatal("authoring warning changed proposal eligibility")
	}
	if len(plan.Warnings) != 1 || plan.Warnings[0].Code != "ISSUE_NEXT_STEP_MISSING" || plan.Warnings[0].NodeID != "new_issue" {
		t.Fatalf("warnings = %#v", plan.Warnings)
	}
}

func TestEvidenceGraphAllowsIssueToChildHypothesis(t *testing.T) {
	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "issue", Type: EvidenceNodeIssue},
			{ID: "hypothesis", Type: EvidenceNodeClaim, DataJSON: `{"claimKind":"hypothesis"}`},
		},
		Edges: []EvidenceChainEdge{{ID: "fork", Type: EvidenceEdgeNextStep, SourceNodeID: "issue", TargetNodeID: "hypothesis"}},
	}
	if err := ValidateEvidenceChainGraph(&graph); err != nil {
		t.Fatalf("issue -> child hypothesis should be valid: %v", err)
	}
	graph.Nodes[1].DataJSON = `{"claimKind":"result"}`
	if err := ValidateEvidenceChainGraph(&graph); err == nil {
		t.Fatal("issue -> result claim next_step should remain invalid")
	}
}

func TestEvidenceEligibilityRevalidatesResultAfterOutcomeDeletion(t *testing.T) {
	s := newTestStore(t)
	current := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "result", Type: EvidenceNodeClaim, SourceSnapshotIDs: []string{"snapshot"}, DataJSON: `{"claimKind":"result","resultDisposition":"conclusion"}`},
			{ID: "conclusion", Type: EvidenceNodeConclusion},
		},
		Edges: []EvidenceChainEdge{{ID: "outcome", Type: EvidenceEdgeSupports, SourceNodeID: "result", TargetNodeID: "conclusion"}},
	}
	patch := EvidenceGraphPatch{DeleteEdgeIDs: []string{"outcome"}}
	merged := mergeEvidenceGraph(current, patch)
	plan := &EvidenceGraphProposalPlan{}
	if err := s.appendEvidenceEligibilityBlockers(context.Background(), "project", &current, &merged, patch, plan); err != nil {
		t.Fatal(err)
	}
	if !hasEvidenceBlocker(plan.Blockers, "RESULT_DISPOSITION_EDGE_MISMATCH") {
		t.Fatalf("deleting a Result outcome bypassed disposition validation: %#v", plan.Blockers)
	}
}

func TestEvidenceEligibilityRequiresClaimKindForAuthoredResultOutcome(t *testing.T) {
	s := newTestStore(t)
	patch := EvidenceGraphPatch{
		Nodes: []EvidenceChainNode{
			{ID: "untyped", Type: EvidenceNodeClaim, SourceSnapshotIDs: []string{"snapshot"}, DataJSON: `{}`},
			{ID: "conclusion", Type: EvidenceNodeConclusion},
		},
		Edges: []EvidenceChainEdge{{ID: "outcome", Type: EvidenceEdgeSupports, SourceNodeID: "untyped", TargetNodeID: "conclusion"}},
	}
	merged := mergeEvidenceGraph(EvidenceChainGraph{}, patch)
	plan := &EvidenceGraphProposalPlan{}
	if err := s.appendEvidenceEligibilityBlockers(context.Background(), "project", &EvidenceChainGraph{}, &merged, patch, plan); err != nil {
		t.Fatal(err)
	}
	if !hasEvidenceBlocker(plan.Blockers, "CLAIM_KIND_REQUIRED") {
		t.Fatalf("untyped authored Result outcome bypassed claimKind validation: %#v", plan.Blockers)
	}
}

func TestEvidenceThreadContractRequiresDesignSpineForTouchedResult(t *testing.T) {
	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "hypothesis", Type: EvidenceNodeClaim, DataJSON: `{"claimKind":"hypothesis"}`},
			{ID: "design", Type: EvidenceNodeExperiment},
			{ID: "result", Type: EvidenceNodeClaim, SourceSnapshotIDs: []string{"snapshot"}, DataJSON: `{"claimKind":"result","resultDisposition":"pending","dispositionReason":"awaiting interpretation"}`},
		},
		Edges: []EvidenceChainEdge{
			{ID: "hypothesis-design", Type: EvidenceEdgeNextStep, SourceNodeID: "hypothesis", TargetNodeID: "design"},
		},
	}
	patch := EvidenceGraphPatch{Nodes: append([]EvidenceChainNode(nil), graph.Nodes...), Edges: append([]EvidenceChainEdge(nil), graph.Edges...)}
	plan := &EvidenceGraphProposalPlan{}
	appendEvidenceThreadContractBlockers(EvidenceChain{ID: "chain"}, nil, graph, patch, plan)
	if !hasEvidenceBlocker(plan.Blockers, "RESULT_DESIGN_LINK_MISSING") {
		t.Fatalf("result without design spine should be blocked: %#v", plan.Blockers)
	}
	if plan.ProjectedResearch == nil {
		t.Fatal("proposal plan did not expose projected research threads")
	}
	if got := len(plan.ProjectedResearch.Unassigned); got != 1 || plan.ProjectedResearch.Unassigned[0].Card.Node.ID != "result" {
		t.Fatalf("projected unassigned = %#v, want result", plan.ProjectedResearch.Unassigned)
	}

	graph.Edges = append(graph.Edges, EvidenceChainEdge{ID: "design-result", Type: EvidenceEdgeNextStep, SourceNodeID: "design", TargetNodeID: "result"})
	patch.Edges = append([]EvidenceChainEdge(nil), graph.Edges...)
	plan = &EvidenceGraphProposalPlan{}
	appendEvidenceThreadContractBlockers(EvidenceChain{ID: "chain"}, nil, graph, patch, plan)
	if hasEvidenceBlocker(plan.Blockers, "RESULT_DESIGN_LINK_MISSING") || hasEvidenceBlocker(plan.Blockers, "RESULT_THREAD_UNASSIGNED") {
		t.Fatalf("canonical design -> result spine was blocked: %#v", plan.Blockers)
	}
	if got := len(plan.ProjectedResearch.Unassigned); got != 0 {
		t.Fatalf("canonical thread has %d unassigned nodes", got)
	}
	if got := len(plan.ProjectedResearch.Threads); got != 1 || len(plan.ProjectedResearch.Threads[0].Stages[EvidenceResearchStageResult]) != 1 {
		t.Fatalf("projected threads = %#v, want one thread with result_count=1", plan.ProjectedResearch.Threads)
	}
}
