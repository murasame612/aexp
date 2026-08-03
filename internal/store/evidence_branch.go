package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// EvidenceBranchProposalRequest is the typed research-thread-v2 authoring
// surface for opening one child hypothesis from an accepted Conclusion or
// Issue. It deliberately excludes raw node ids, edge types, coordinates and
// acceptance controls.
type EvidenceBranchProposalRequest struct {
	MapID                  string
	OutcomeNodeID          string
	HypothesisTitle        string
	HypothesisBodyMD       string
	BranchRationale        string
	ExperimentDesignTitle  string
	ExperimentDesignBodyMD string
	Summary                string
	Actor                  string
}

type EvidenceBranchProposalIdentity struct {
	MapID                  string   `json:"map_id"`
	OutcomeNodeID          string   `json:"outcome_node_id"`
	HypothesisNodeID       string   `json:"hypothesis_node_id"`
	ExperimentDesignNodeID string   `json:"experiment_design_node_id,omitempty"`
	EdgeIDs                []string `json:"edge_ids"`
}

type EvidenceBranchProposalResult struct {
	Proposal   *EvidenceProposal              `json:"proposal"`
	Plan       *EvidenceGraphProposalPlan     `json:"plan"`
	Branch     EvidenceBranchProposalIdentity `json:"branch"`
	Capacity   EvidenceTopicCapacity          `json:"capacity"`
	ChildCount int                            `json:"existing_child_count"`
	NextAction string                         `json:"next_action"`
}

func evidenceBranchStableSuffix(request EvidenceBranchProposalRequest) (string, error) {
	payload, err := json.Marshal(struct {
		Version                string `json:"version"`
		MapID                  string `json:"map_id"`
		OutcomeNodeID          string `json:"outcome_node_id"`
		HypothesisTitle        string `json:"hypothesis_title"`
		HypothesisBodyMD       string `json:"hypothesis_body_md"`
		BranchRationale        string `json:"branch_rationale"`
		ExperimentDesignTitle  string `json:"experiment_design_title"`
		ExperimentDesignBodyMD string `json:"experiment_design_body_md"`
	}{
		Version: "research-thread-branch-v1", MapID: request.MapID, OutcomeNodeID: request.OutcomeNodeID,
		HypothesisTitle: request.HypothesisTitle, HypothesisBodyMD: request.HypothesisBodyMD,
		BranchRationale: request.BranchRationale, ExperimentDesignTitle: request.ExperimentDesignTitle,
		ExperimentDesignBodyMD: request.ExperimentDesignBodyMD,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:10]), nil
}

func normalizeEvidenceBranchRequest(request EvidenceBranchProposalRequest) (EvidenceBranchProposalRequest, error) {
	request.MapID = strings.TrimSpace(request.MapID)
	request.OutcomeNodeID = strings.TrimSpace(request.OutcomeNodeID)
	request.HypothesisTitle = strings.TrimSpace(request.HypothesisTitle)
	request.HypothesisBodyMD = strings.TrimSpace(request.HypothesisBodyMD)
	request.BranchRationale = strings.TrimSpace(request.BranchRationale)
	request.ExperimentDesignTitle = strings.TrimSpace(request.ExperimentDesignTitle)
	request.ExperimentDesignBodyMD = strings.TrimSpace(request.ExperimentDesignBodyMD)
	request.Summary = strings.TrimSpace(request.Summary)
	request.Actor = strings.TrimSpace(request.Actor)
	switch {
	case request.MapID == "":
		return request, graphValidationError("MAP_ID_REQUIRED", "map_id is required")
	case request.OutcomeNodeID == "":
		return request, graphValidationError("OUTCOME_NODE_ID_REQUIRED", "outcome_node_id is required")
	case request.HypothesisTitle == "":
		return request, graphValidationError("HYPOTHESIS_TITLE_REQUIRED", "hypothesis_title is required")
	case request.BranchRationale == "":
		return request, graphValidationError("BRANCH_RATIONALE_REQUIRED", "branch_rationale is required")
	case request.ExperimentDesignTitle == "" && request.ExperimentDesignBodyMD != "":
		return request, graphValidationError("EXPERIMENT_DESIGN_TITLE_REQUIRED", "experiment_design_title is required when experiment_design_body_md is provided")
	}
	if request.Actor == "" {
		request.Actor = "agent"
	}
	if request.Summary == "" {
		request.Summary = "Open child hypothesis: " + request.HypothesisTitle
	}
	return request, nil
}

// CreateEvidenceBranchProposal creates and plans a pending proposal. It never
// accepts the proposal or mutates the accepted Evidence Map.
func (s *SQLite) CreateEvidenceBranchProposal(ctx context.Context, request EvidenceBranchProposalRequest) (*EvidenceBranchProposalResult, error) {
	request, err := normalizeEvidenceBranchRequest(request)
	if err != nil {
		return nil, err
	}
	chain, err := s.GetEvidenceChain(ctx, request.MapID)
	if err != nil {
		return nil, err
	}
	if chain == nil {
		return nil, graphValidationError("GRAPH_NOT_FOUND", fmt.Sprintf("evidence map %q does not exist", request.MapID))
	}
	if chain.Status != "active" {
		return nil, graphValidationError("GRAPH_ARCHIVED", "archived evidence map is read-only")
	}
	if chain.Role != "secondary" {
		return nil, graphValidationError("TOPIC_MAP_REQUIRED", "child research threads must be authored inside an active Topic Map")
	}
	graph, err := s.GetEvidenceChainGraph(ctx, chain.ID)
	if err != nil {
		return nil, err
	}
	nodes := make(map[string]EvidenceChainNode, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
	}
	outcome, exists := nodes[request.OutcomeNodeID]
	if !exists {
		return nil, graphValidationError("OUTCOME_NODE_NOT_FOUND", fmt.Sprintf("outcome node %q does not exist in Map %q", request.OutcomeNodeID, chain.ID))
	}
	if outcome.Type != EvidenceNodeConclusion && outcome.Type != EvidenceNodeIssue {
		return nil, graphValidationError("OUTCOME_NODE_TYPE_REQUIRED", fmt.Sprintf("node %q is %q; only an accepted Conclusion or Issue may open a child hypothesis", outcome.ID, outcome.Type))
	}
	projection := BuildEvidenceResearchProjection(*chain, *graph)
	if projection.OwnerByNode[outcome.ID] == "" {
		return nil, graphValidationError("OUTCOME_THREAD_REQUIRED", "the selected outcome is not owned by a hypothesis-led research thread; curate its parent thread before branching")
	}
	childCount := 0
	for _, edge := range graph.Edges {
		if edge.SourceNodeID == outcome.ID && edge.Type == EvidenceEdgeNextStep {
			target := nodes[edge.TargetNodeID]
			if target.Type == EvidenceNodeClaim && evidenceAuthoringClaimKind(target) == "hypothesis" {
				childCount++
			}
		}
	}

	suffix, err := evidenceBranchStableSuffix(request)
	if err != nil {
		return nil, err
	}
	hypothesisID := "claim_hypothesis_" + suffix
	branchEdgeID := "edge_branch_" + suffix
	patch := EvidenceGraphPatch{
		ChainID: chain.ID,
		Nodes: []EvidenceChainNode{{
			ID: hypothesisID, Type: EvidenceNodeClaim, Title: request.HypothesisTitle,
			Body: request.HypothesisBodyMD, DataJSON: `{"claimKind":"hypothesis"}`,
		}},
		Edges: []EvidenceChainEdge{{
			ID: branchEdgeID, SourceNodeID: outcome.ID, TargetNodeID: hypothesisID,
			Type: EvidenceEdgeNextStep, Label: "开启子假设", Rationale: request.BranchRationale, DataJSON: `{}`,
		}},
	}
	identity := EvidenceBranchProposalIdentity{
		MapID: chain.ID, OutcomeNodeID: outcome.ID, HypothesisNodeID: hypothesisID,
		EdgeIDs: []string{branchEdgeID},
	}
	if request.ExperimentDesignTitle != "" {
		designID := "experiment_design_" + suffix
		designEdgeID := "edge_design_" + suffix
		patch.Nodes = append(patch.Nodes, EvidenceChainNode{
			ID: designID, Type: EvidenceNodeExperiment, Title: request.ExperimentDesignTitle,
			Body: request.ExperimentDesignBodyMD, DataJSON: `{}`,
		})
		patch.Edges = append(patch.Edges, EvidenceChainEdge{
			ID: designEdgeID, SourceNodeID: hypothesisID, TargetNodeID: designID,
			Type: EvidenceEdgeNextStep, Label: "设计验证", DataJSON: `{}`,
		})
		identity.ExperimentDesignNodeID = designID
		identity.EdgeIDs = append(identity.EdgeIDs, designEdgeID)
	}
	proposal, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: chain.ProjectID, TargetChainID: chain.ID, Actor: request.Actor,
		Summary:       request.Summary,
		RoutingReason: fmt.Sprintf("Follow-up branch from accepted outcome %s in Topic %s", outcome.ID, chain.Title),
		SourceKind:    "branch_from_outcome", SourceID: outcome.ID,
	}, &patch)
	if err != nil {
		return nil, err
	}
	plan, err := s.PlanEvidenceProposal(ctx, proposal.ID)
	if err != nil {
		return nil, err
	}
	return &EvidenceBranchProposalResult{
		Proposal: proposal, Plan: plan, Branch: identity, Capacity: projection.Capacity,
		ChildCount: childCount, NextAction: "user_review",
	}, nil
}
