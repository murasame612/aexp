package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

func (s *SQLite) PlanEvidencePromotion(ctx context.Context, request EvidencePromotionRequest) (*EvidencePromotionPlan, error) {
	request.SourceMapID = strings.TrimSpace(request.SourceMapID)
	request.SourceNodeIDs = normalizeEvidenceProposalIDs(request.SourceNodeIDs)
	request.Summary = strings.TrimSpace(request.Summary)
	request.NodeType = strings.TrimSpace(request.NodeType)
	if request.NodeType == "" {
		request.NodeType = EvidenceNodeClaim
	}
	plan := &EvidencePromotionPlan{
		SourceMapID: request.SourceMapID, SourceNodeIDs: request.SourceNodeIDs,
		Summary: request.Summary, NodeType: request.NodeType, Blockers: make([]EvidenceGraphBlocker, 0),
	}
	source, err := s.GetEvidenceChain(ctx, request.SourceMapID)
	if err != nil {
		return nil, err
	}
	if source == nil {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "SOURCE_MAP_NOT_FOUND", Message: "source Topic Map does not exist"})
		return finalizeEvidencePromotionPlan(plan)
	}
	plan.ProjectID = source.ProjectID
	plan.SourceRevision = source.Revision
	plan.SourceGraphHash = source.GraphHash
	if source.Role == "primary" {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "PROMOTION_SOURCE_NOT_TOPIC", Message: "promotion source must be a Topic Map"})
	}
	if source.ProjectID == "" {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "GRAPH_PROJECT_MISMATCH", Message: "source Topic Map has no Project owner"})
	}
	if source.Revision <= 0 || source.GraphHash == "" {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "SOURCE_MAP_EMPTY", Message: "source Topic Map has no accepted revision"})
	}
	if request.Summary == "" {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "PROMOTION_SUMMARY_REQUIRED", Message: "promotion summary is required"})
	}
	if request.NodeType != EvidenceNodeClaim && request.NodeType != EvidenceNodeIssue && request.NodeType != EvidenceNodePlan {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "PROMOTION_NODE_TYPE_INVALID", Message: "promotion node_type must be claim, issue, or plan"})
	}
	sourceGraph, err := s.GetEvidenceChainGraph(ctx, source.ID)
	if err != nil {
		return nil, err
	}
	sourceNodes := make(map[string]EvidenceChainNode, len(sourceGraph.Nodes))
	for _, node := range sourceGraph.Nodes {
		sourceNodes[node.ID] = node
	}
	selectedNodeIDs := make(map[string]bool, len(request.SourceNodeIDs))
	for _, nodeID := range request.SourceNodeIDs {
		selectedNodeIDs[nodeID] = true
		if _, ok := sourceNodes[nodeID]; !ok {
			plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "PROMOTION_SOURCE_NODE_NOT_FOUND", Message: fmt.Sprintf("source node %q does not exist in Topic Map revision %d", nodeID, source.Revision), NodeID: nodeID})
		}
	}
	formalUses := make([]formalRunEvidenceUse, 0)
	for _, nodeID := range request.SourceNodeIDs {
		node, ok := sourceNodes[nodeID]
		if ok && (node.Type == EvidenceNodeRun || strings.TrimSpace(node.RunID) != "") {
			formalUses = append(formalUses, formalRunEvidenceUse{Node: node})
		}
	}
	for _, edge := range sourceGraph.Edges {
		if !selectedNodeIDs[edge.TargetNodeID] ||
			(edge.Type != EvidenceEdgeSupports && edge.Type != EvidenceEdgeWeakens && edge.Type != EvidenceEdgeDoesNotProve) {
			continue
		}
		sourceNode, ok := sourceNodes[edge.SourceNodeID]
		if !ok || (sourceNode.Type != EvidenceNodeRun && strings.TrimSpace(sourceNode.RunID) == "") {
			continue
		}
		formalUses = append(formalUses, formalRunEvidenceUse{Node: sourceNode, EdgeID: edge.ID})
	}
	formalBlockers, err := s.formalRunEvidenceBlockers(ctx, source.ProjectID, formalUses)
	if err != nil {
		return nil, err
	}
	plan.Blockers = append(plan.Blockers, formalBlockers...)
	primary, err := s.GetActivePrimaryEvidenceChain(ctx, source.ProjectID)
	if err != nil {
		return nil, err
	}
	if primary == nil {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "PRIMARY_MAP_NOT_FOUND", Message: "Project has no active Primary Map"})
		return finalizeEvidencePromotionPlan(plan)
	}
	plan.TargetPrimaryMapID = primary.ID
	plan.TargetPrimaryRevision = primary.Revision
	seedPayload, _ := json.Marshal(map[string]interface{}{
		"source_map_id": source.ID, "source_revision": source.Revision, "source_graph_hash": source.GraphHash,
		"source_node_ids": request.SourceNodeIDs, "summary": request.Summary, "node_type": request.NodeType,
		"primary_map_id": primary.ID, "primary_revision": primary.Revision,
	})
	seed := sha256.Sum256(seedPayload)
	suffix := hex.EncodeToString(seed[:10])
	referenceJSON, _ := json.Marshal(EvidenceMapReference{
		TargetMapID: source.ID, TargetRevision: source.Revision, TargetGraphHash: source.GraphHash,
		TargetNodeIDs: request.SourceNodeIDs, Summary: request.Summary,
	})
	summaryID := "promotion_summary_" + suffix
	referenceID := "promotion_map_ref_" + suffix
	plan.Patch = EvidenceGraphPatch{
		ChainID: primary.ID,
		Nodes: []EvidenceChainNode{
			{ID: summaryID, Type: request.NodeType, Title: request.Summary, Body: "Promoted from Topic Map " + source.Title},
			{ID: referenceID, Type: EvidenceNodeMapRef, Title: source.Title, Body: request.Summary, DataJSON: string(referenceJSON)},
		},
		Edges: []EvidenceChainEdge{{
			ID: "promotion_edge_" + suffix, Type: EvidenceEdgeRelatedTo,
			SourceNodeID: summaryID, TargetNodeID: referenceID, Label: "详细证据",
		}},
	}
	if len(plan.Blockers) == 0 {
		current, currentErr := s.GetEvidenceChainGraph(ctx, primary.ID)
		if currentErr != nil {
			return nil, currentErr
		}
		merged := mergeEvidenceGraph(*current, plan.Patch)
		if validateErr := ValidateEvidenceChainGraph(&merged); validateErr != nil {
			plan.Blockers = append(plan.Blockers, blockerFromError(validateErr))
		} else if referenceErr := s.ValidateEvidenceMapReferences(ctx, primary, &merged); referenceErr != nil {
			plan.Blockers = append(plan.Blockers, blockerFromError(referenceErr))
		}
	}
	return finalizeEvidencePromotionPlan(plan)
}

func finalizeEvidencePromotionPlan(plan *EvidencePromotionPlan) (*EvidencePromotionPlan, error) {
	payload, err := json.Marshal(struct {
		ProjectID             string             `json:"project_id"`
		SourceMapID           string             `json:"source_map_id"`
		SourceRevision        int64              `json:"source_revision"`
		SourceGraphHash       string             `json:"source_graph_hash"`
		SourceNodeIDs         []string           `json:"source_node_ids"`
		TargetPrimaryMapID    string             `json:"target_primary_map_id"`
		TargetPrimaryRevision int64              `json:"target_primary_revision"`
		Summary               string             `json:"summary"`
		NodeType              string             `json:"node_type"`
		Patch                 EvidenceGraphPatch `json:"patch"`
	}{
		ProjectID: plan.ProjectID, SourceMapID: plan.SourceMapID, SourceRevision: plan.SourceRevision,
		SourceGraphHash: plan.SourceGraphHash, SourceNodeIDs: plan.SourceNodeIDs,
		TargetPrimaryMapID: plan.TargetPrimaryMapID, TargetPrimaryRevision: plan.TargetPrimaryRevision,
		Summary: plan.Summary, NodeType: plan.NodeType, Patch: plan.Patch,
	})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	plan.PlanHash = hex.EncodeToString(sum[:])
	plan.Eligible = len(plan.Blockers) == 0
	return plan, nil
}

func (s *SQLite) CreateEvidencePromotion(ctx context.Context, request EvidencePromotionRequest, expectedPlanHash string) (*EvidenceProposal, error) {
	expectedPlanHash = strings.TrimSpace(expectedPlanHash)
	if expectedPlanHash == "" {
		return nil, graphValidationError("EXPECTED_PLAN_HASH_REQUIRED", "expected_plan_hash is required")
	}
	plan, err := s.PlanEvidencePromotion(ctx, request)
	if err != nil {
		return nil, err
	}
	if plan.PlanHash != expectedPlanHash {
		return nil, graphValidationError("PLAN_HASH_MISMATCH", fmt.Sprintf("expected plan %s; current plan is %s", expectedPlanHash, plan.PlanHash))
	}
	if !plan.Eligible {
		return nil, graphValidationError("PROMOTION_BLOCKED", "promotion plan has blockers")
	}
	actor := strings.TrimSpace(request.Actor)
	if actor == "" {
		actor = "agent"
	}
	return s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: plan.ProjectID, TargetChainID: plan.TargetPrimaryMapID,
		Actor: actor, Summary: plan.Summary, ProjectLevelImpact: true,
		SourceKind: "promotion", SourceID: fmt.Sprintf("%s@%d:%s", plan.SourceMapID, plan.SourceRevision, plan.SourceGraphHash),
	}, &plan.Patch)
}
