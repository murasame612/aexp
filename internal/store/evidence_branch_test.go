package store

import (
	"context"
	"testing"
)

func seedEvidenceBranchOutcome(t *testing.T, s *SQLite, topic *EvidenceChain, outcomeType string) string {
	t.Helper()
	outcomeID := "outcome_" + outcomeType
	edgeType := EvidenceEdgeSupports
	if outcomeType == EvidenceNodeIssue {
		edgeType = EvidenceEdgeRevealsIssue
	}
	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "parent_hypothesis", Type: EvidenceNodeClaim, Title: "Parent hypothesis", DataJSON: `{"claimKind":"hypothesis"}`},
			{ID: outcomeID, Type: outcomeType, Title: "Accepted outcome", DataJSON: `{}`},
		},
		Edges: []EvidenceChainEdge{{ID: "parent_outcome", SourceNodeID: "parent_hypothesis", TargetNodeID: outcomeID, Type: edgeType, DataJSON: `{}`}},
	}
	if _, err := s.SaveEvidenceChainGraphCAS(context.Background(), topic.ID, graph, EvidenceGraphSaveOptions{
		ExpectedRevision: 0, Actor: "test", SourceKind: "migration",
	}); err != nil {
		t.Fatal(err)
	}
	return outcomeID
}

func TestCreateEvidenceBranchProposalIsTypedReviewableAndSideEffectFree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, topic := createEvidenceWorkspaceProject(t, s, "project_branch")
	outcomeID := seedEvidenceBranchOutcome(t, s, topic, EvidenceNodeIssue)
	before, _ := s.GetEvidenceChain(ctx, topic.ID)
	request := EvidenceBranchProposalRequest{
		MapID: topic.ID, OutcomeNodeID: outcomeID,
		HypothesisTitle:        "A controlled split resolves the limitation",
		HypothesisBodyMD:       "Test the failure boundary explicitly.",
		BranchRationale:        "The accepted issue identifies a falsifiable follow-up.",
		ExperimentDesignTitle:  "Run the matched controlled comparison",
		ExperimentDesignBodyMD: "Hold data, split and seeds fixed.",
	}
	result, err := s.CreateEvidenceBranchProposal(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Proposal == nil || result.Proposal.Status != GraphProposalPending || result.Proposal.SourceKind != "branch_from_outcome" {
		t.Fatalf("proposal = %#v", result.Proposal)
	}
	if result.Plan == nil || !result.Plan.Eligible || result.NextAction != "user_review" {
		t.Fatalf("result = %#v", result)
	}
	if result.Branch.HypothesisNodeID == "" || result.Branch.ExperimentDesignNodeID == "" || len(result.Branch.EdgeIDs) != 2 {
		t.Fatalf("branch identity = %#v", result.Branch)
	}
	afterProposal, _ := s.GetEvidenceChain(ctx, topic.ID)
	if afterProposal.Revision != before.Revision || afterProposal.GraphHash != before.GraphHash {
		t.Fatalf("proposal mutated accepted map: before=%#v after=%#v", before, afterProposal)
	}
	repeated, err := s.CreateEvidenceBranchProposal(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Proposal.ID != result.Proposal.ID || repeated.Branch.HypothesisNodeID != result.Branch.HypothesisNodeID {
		t.Fatalf("branch request was not idempotent: first=%#v repeated=%#v", result, repeated)
	}
	if _, err := s.ReviewEvidenceProposal(ctx, result.Proposal.ID, "accept", "user"); err != nil {
		t.Fatal(err)
	}
	accepted, _ := s.GetEvidenceChain(ctx, topic.ID)
	graph, _ := s.GetEvidenceChainGraph(ctx, topic.ID)
	projection := BuildEvidenceResearchProjection(*accepted, *graph)
	if len(projection.Threads) != 2 || projection.Threads[1].ParentThreadID != projection.Threads[0].ID {
		t.Fatalf("accepted branch did not produce a child thread: %#v", projection.Threads)
	}
	if len(projection.CrossThreadRelations) != 1 || projection.CrossThreadRelations[0].Kind != "branch" {
		t.Fatalf("branch relation = %#v", projection.CrossThreadRelations)
	}
}

func TestCreateEvidenceBranchProposalRejectsNonOutcomeAndPrimaryMap(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_branch_invalid")
	seedEvidenceBranchOutcome(t, s, topic, EvidenceNodeConclusion)
	for _, test := range []struct {
		name    string
		request EvidenceBranchProposalRequest
		code    string
	}{
		{
			name: "non outcome", code: "OUTCOME_NODE_TYPE_REQUIRED",
			request: EvidenceBranchProposalRequest{MapID: topic.ID, OutcomeNodeID: "parent_hypothesis", HypothesisTitle: "Child", BranchRationale: "Follow up"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := s.CreateEvidenceBranchProposal(ctx, test.request)
			if err == nil || !isEvidenceValidationCode(err, test.code) {
				t.Fatalf("err = %v, want %s", err, test.code)
			}
		})
	}
	primary, err := s.GetActivePrimaryEvidenceChain(ctx, project.ID)
	if err != nil || primary == nil {
		t.Fatalf("primary=%#v err=%v", primary, err)
	}
	_, err = s.CreateEvidenceBranchProposal(ctx, EvidenceBranchProposalRequest{
		MapID: primary.ID, OutcomeNodeID: "anything", HypothesisTitle: "Child", BranchRationale: "Follow up",
	})
	if err == nil || !isEvidenceValidationCode(err, "TOPIC_MAP_REQUIRED") {
		t.Fatalf("primary branch err = %v", err)
	}
}

func isEvidenceValidationCode(err error, code string) bool {
	validation, ok := err.(*EvidenceGraphValidationError)
	return ok && validation.Code == code
}
