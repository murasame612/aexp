package store

import (
	"context"
	"testing"
)

func TestEvidenceReorganizationPlanIsSideEffectFreeAndHashBound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, topic := createEvidenceWorkspaceProject(t, s, "project_reorg")
	patch := EvidenceGraphPatch{
		Nodes: []EvidenceChainNode{
			{ID: "hypothesis", Type: EvidenceNodeHypothesis, Title: "Start from a testable hypothesis"},
			{ID: "plan", Type: EvidenceNodePlan, Title: "Run the matched comparison"},
		},
		Edges: []EvidenceChainEdge{{ID: "next", Type: EvidenceEdgeNextStep, SourceNodeID: "hypothesis", TargetNodeID: "plan"}},
	}
	before, _ := s.GetEvidenceChain(ctx, topic.ID)
	plan, err := s.PlanEvidenceReorganization(ctx, topic.ID, patch)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Eligible || plan.PlanHash == "" || len(plan.After.Threads) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	afterPlan, _ := s.GetEvidenceChain(ctx, topic.ID)
	if afterPlan.Revision != before.Revision || afterPlan.GraphHash != before.GraphHash {
		t.Fatalf("planning mutated map: before=%#v after=%#v", before, afterPlan)
	}
	proposal, err := s.CreateEvidenceReorganizationProposal(ctx, topic.ID, "Organize first bounded thread", "agent", "This Topic owns the comparison", nil, patch, plan.PlanHash)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Status != GraphProposalPending || proposal.SourceKind != "evidence_reorganization" {
		t.Fatalf("proposal = %#v", proposal)
	}
	changedPatch := patch
	changedPatch.Nodes = append([]EvidenceChainNode(nil), patch.Nodes...)
	changedPatch.Nodes[1].Title = "Changed after planning"
	if _, err := s.CreateEvidenceReorganizationProposal(ctx, topic.ID, "Changed", "agent", "This Topic owns the comparison", nil, changedPatch, plan.PlanHash); err == nil {
		t.Fatal("changed patch unexpectedly matched old plan hash")
	}
}

func TestRebaseEvidenceProposalCreatesCurrentRevisionReplacement(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_rebase_tool")
	seed, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent", Summary: "Seed", RoutingReason: "Topic seed",
	}, &EvidenceGraphPatch{Nodes: []EvidenceChainNode{{ID: "hypothesis", Type: EvidenceNodeHypothesis, Title: "Hypothesis"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewEvidenceProposal(ctx, seed.ID, "accept", "user"); err != nil {
		t.Fatal(err)
	}
	stale, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent", Summary: "Add plan", RoutingReason: "Topic plan",
	}, &EvidenceGraphPatch{Nodes: []EvidenceChainNode{{ID: "plan", Type: EvidenceNodePlan, Title: "Plan"}}, Edges: []EvidenceChainEdge{{ID: "next", Type: EvidenceEdgeNextStep, SourceNodeID: "hypothesis", TargetNodeID: "plan"}}})
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent", Summary: "Add context", RoutingReason: "Topic context",
	}, &EvidenceGraphPatch{Nodes: []EvidenceChainNode{{ID: "context", Type: EvidenceNodeNote, Title: "Context"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewEvidenceProposal(ctx, unrelated.ID, "accept", "user"); err != nil {
		t.Fatal(err)
	}
	current, _ := s.GetEvidenceChain(ctx, topic.ID)
	rebased, err := s.RebaseEvidenceProposal(ctx, stale.ID, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if rebased.ID == stale.ID || rebased.BaseGraphRevision != current.Revision || rebased.SourceKind != "proposal_rebase" || rebased.SourceID != stale.ID {
		t.Fatalf("rebased = %#v current=%#v stale=%#v", rebased, current, stale)
	}
	old, _ := s.GetEvidenceProposal(ctx, stale.ID)
	if old.Status != GraphProposalExpired {
		t.Fatalf("old proposal status = %q", old.Status)
	}
}
