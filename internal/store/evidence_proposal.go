package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (s *SQLite) SubmitEvidenceGraphProposal(ctx context.Context, card *ProjectRunCard, patch *EvidenceGraphPatch) (*ProjectRunCard, error) {
	if card == nil || strings.TrimSpace(card.RunID) == "" {
		return nil, graphValidationError("RUN_ID_REQUIRED", "run_id is required")
	}
	run, err := s.GetRun(ctx, card.RunID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, graphValidationError("RUN_NOT_FOUND", fmt.Sprintf("run %q does not exist", card.RunID))
	}
	existing, err := s.GetProjectRunCard(ctx, card.RunID)
	if err != nil {
		return nil, err
	}
	if existing != nil && card.ID == "" {
		card.ID = existing.ID
	}
	if card.ID == "" {
		card.ID = "card_" + strings.TrimPrefix(card.RunID, "run_")
	}
	if run.ProjectID != "" && card.ProjectID != "" && run.ProjectID != card.ProjectID {
		return nil, graphValidationError("CROSS_PROJECT_CARD", fmt.Sprintf("run %q belongs to project %q", run.ID, run.ProjectID))
	}
	if run.ProjectID != "" {
		card.ProjectID = run.ProjectID
	}
	if card.ProjectID != "" && card.ProjectName == "" {
		if project, projectErr := s.GetProjectDefinition(ctx, card.ProjectID); projectErr != nil {
			return nil, projectErr
		} else if project != nil {
			card.ProjectName = project.Name
		}
	}
	if card.NoGraphImpact {
		if strings.TrimSpace(card.GraphImpactReason) == "" {
			return nil, graphValidationError("GRAPH_IMPACT_REASON_REQUIRED", "no_graph_impact requires graph_impact_reason")
		}
		card.GraphPatchJSON = ""
		card.GraphStatus = GraphProposalNone
		card.ProposalHash = ""
		card.BaseGraphRevision = 0
		card.ReviewedAt = nil
		if err := s.saveProjectRunGraphProposal(ctx, card); err != nil {
			return nil, err
		}
		return s.GetProjectRunCard(ctx, card.RunID)
	}
	if patch == nil {
		return nil, graphValidationError("GRAPH_PATCH_REQUIRED", "graph patch or explicit no_graph_impact reason is required")
	}
	if strings.TrimSpace(patch.ChainID) == "" {
		if strings.TrimSpace(run.ProjectID) == "" {
			return nil, graphValidationError("RUN_PROJECT_REQUIRED", fmt.Sprintf("run %q has no canonical project_id", run.ID))
		}
		chain, ensureErr := s.EnsureProjectPrimaryEvidenceChain(ctx, run.ProjectID)
		if ensureErr != nil {
			return nil, ensureErr
		}
		patch.ChainID = chain.ID
		card.BaseGraphRevision = chain.Revision
	}
	chain, err := s.GetEvidenceChain(ctx, patch.ChainID)
	if err != nil {
		return nil, err
	}
	if chain == nil {
		return nil, graphValidationError("GRAPH_NOT_FOUND", fmt.Sprintf("evidence graph %q does not exist", patch.ChainID))
	}
	if chain.Status == "archived" {
		return nil, graphValidationError("GRAPH_ARCHIVED", "archived evidence graph is read-only")
	}
	if chain.ProjectID != "" && card.ProjectID != "" && chain.ProjectID != card.ProjectID {
		return nil, graphValidationError("CROSS_PROJECT_GRAPH", fmt.Sprintf("graph belongs to project %q, card belongs to %q", chain.ProjectID, card.ProjectID))
	}
	patch.RoutingReason = strings.TrimSpace(patch.RoutingReason)
	if chain.Role == "secondary" && patch.RoutingReason == "" {
		return nil, graphValidationError("ROUTING_REASON_REQUIRED", "routing_reason is required when targeting a topic evidence graph")
	}
	patch.LayoutIntent, err = normalizeEvidenceLayoutIntent(patch.LayoutIntent)
	if err != nil {
		return nil, err
	}
	card.GraphRoutingReason = patch.RoutingReason
	for i := range patch.Nodes {
		patch.Nodes[i].ChainID = patch.ChainID
		patch.Nodes[i].X = 0
		patch.Nodes[i].Y = 0
		patch.Nodes[i].Pinned = false
		patch.Nodes[i].DataJSON, err = stripEvidenceProposalLayoutData(patch.Nodes[i].DataJSON)
		if err != nil {
			return nil, graphValidationError("INVALID_NODE_DATA", fmt.Sprintf("node %q data_json is invalid: %v", patch.Nodes[i].ID, err))
		}
	}
	for i := range patch.UpsertNodes {
		patch.UpsertNodes[i].ChainID = patch.ChainID
		patch.UpsertNodes[i].X = 0
		patch.UpsertNodes[i].Y = 0
		patch.UpsertNodes[i].Pinned = false
		patch.UpsertNodes[i].DataJSON, err = stripEvidenceProposalLayoutData(patch.UpsertNodes[i].DataJSON)
		if err != nil {
			return nil, graphValidationError("INVALID_NODE_DATA", fmt.Sprintf("node %q data_json is invalid: %v", patch.UpsertNodes[i].ID, err))
		}
	}
	for i := range patch.Edges {
		patch.Edges[i].ChainID = patch.ChainID
	}
	for i := range patch.UpsertEdges {
		patch.UpsertEdges[i].ChainID = patch.ChainID
	}
	patchJSON, proposalHash, err := canonicalEvidenceProposal(card.RunID, card.BaseGraphRevision, *patch)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.ProposalHash == proposalHash && existing.ProposalHash != "" {
		return existing, nil
	}
	if existing != nil && existing.GraphStatus == GraphProposalAccepted {
		return nil, graphValidationError("PROPOSAL_ALREADY_ACCEPTED", "an accepted proposal for this run is immutable")
	}
	card.GraphPatchJSON = string(patchJSON)
	card.GraphStatus = GraphProposalPending
	card.ProposalHash = proposalHash
	card.NoGraphImpact = false
	card.GraphImpactReason = ""
	card.ReviewedAt = nil
	if err := s.saveProjectRunGraphProposal(ctx, card); err != nil {
		return nil, err
	}
	return s.GetProjectRunCard(ctx, card.RunID)
}

func canonicalEvidenceProposal(runID string, baseRevision int64, patch EvidenceGraphPatch) ([]byte, string, error) {
	canonicalPatch, err := canonicalEvidencePatch(patch)
	if err != nil {
		return nil, "", err
	}
	payload, err := json.Marshal(map[string]interface{}{
		"version":                   2,
		"evidence_contract_version": EvidenceResearchContractVersion,
		"run_id":                    strings.TrimSpace(runID),
		"chain_id":                  strings.TrimSpace(patch.ChainID),
		"routing_reason":            strings.TrimSpace(patch.RoutingReason),
		"base_revision":             baseRevision,
		"patch":                     canonicalPatch,
	})
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(payload)

	// Persist the operational patch, including stable IDs but excluding layout.
	persisted, err := json.Marshal(EvidenceGraphPatch{
		ChainID:       patch.ChainID,
		RoutingReason: patch.RoutingReason,
		LayoutIntent:  patch.LayoutIntent,
		Nodes:         patch.Nodes,
		Edges:         patch.Edges,
		UpsertNodes:   patch.UpsertNodes,
		UpsertEdges:   patch.UpsertEdges,
		DeleteNodeIDs: patch.DeleteNodeIDs,
		DeleteEdgeIDs: patch.DeleteEdgeIDs,
	})
	if err != nil {
		return nil, "", err
	}
	return persisted, hex.EncodeToString(sum[:]), nil
}

func normalizeEvidenceLayoutIntent(intent *EvidenceLayoutIntent) (*EvidenceLayoutIntent, error) {
	if intent == nil {
		return nil, nil
	}
	flow := strings.TrimSpace(intent.Flow)
	if flow == "" {
		flow = "left_to_right"
	}
	if flow != "left_to_right" {
		return nil, graphValidationError("INVALID_LAYOUT_INTENT", fmt.Sprintf("unsupported layout flow %q", flow))
	}
	if len(intent.Ranks) == 0 || len(intent.Ranks) > 80 {
		return nil, graphValidationError("INVALID_LAYOUT_INTENT", "layout_intent.ranks must contain between 1 and 80 columns")
	}
	seen := make(map[string]bool)
	ranks := make([][]string, 0, len(intent.Ranks))
	for rankIndex, rank := range intent.Ranks {
		if len(rank) == 0 {
			return nil, graphValidationError("INVALID_LAYOUT_INTENT", fmt.Sprintf("layout rank %d is empty", rankIndex))
		}
		normalized := make([]string, 0, len(rank))
		for _, rawID := range rank {
			id := strings.TrimSpace(rawID)
			if id == "" {
				return nil, graphValidationError("INVALID_LAYOUT_INTENT", fmt.Sprintf("layout rank %d contains an empty node id", rankIndex))
			}
			if seen[id] {
				return nil, graphValidationError("INVALID_LAYOUT_INTENT", fmt.Sprintf("layout node %q appears more than once", id))
			}
			seen[id] = true
			normalized = append(normalized, id)
		}
		ranks = append(ranks, normalized)
	}
	if len(seen) > 500 {
		return nil, graphValidationError("INVALID_LAYOUT_INTENT", "layout_intent may reference at most 500 nodes")
	}
	return &EvidenceLayoutIntent{
		Flow:      flow,
		Ranks:     ranks,
		Rationale: strings.TrimSpace(intent.Rationale),
	}, nil
}

func validateEvidenceLayoutIntent(intent *EvidenceLayoutIntent, graph EvidenceChainGraph) error {
	if intent == nil {
		return nil
	}
	nodeByID := make(map[string]EvidenceChainNode, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodeByID[node.ID] = node
	}
	for _, rank := range intent.Ranks {
		for _, id := range rank {
			node, exists := nodeByID[id]
			if !exists {
				return graphValidationError("LAYOUT_NODE_NOT_FOUND", fmt.Sprintf("layout node %q does not exist in the proposed graph", id))
			}
			var data map[string]interface{}
			if json.Unmarshal([]byte(normalizeEvidenceDataJSON(node.DataJSON)), &data) == nil {
				if groupID, _ := data["groupId"].(string); strings.TrimSpace(groupID) != "" {
					return graphValidationError("LAYOUT_GROUP_MEMBER", fmt.Sprintf("layout node %q belongs to protocol %q; arrange the protocol container instead", id, groupID))
				}
			}
		}
	}
	return nil
}

func canonicalEvidencePatch(patch EvidenceGraphPatch) (interface{}, error) {
	canonicalPart := func(nodes []EvidenceChainNode, edges []EvidenceChainEdge) (interface{}, error) {
		payload, _, err := CanonicalEvidenceGraph(EvidenceChainGraph{Nodes: nodes, Edges: edges})
		if err != nil {
			return nil, err
		}
		var value interface{}
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		return value, nil
	}
	additive, err := canonicalPart(patch.Nodes, patch.Edges)
	if err != nil {
		return nil, err
	}
	upserts, err := canonicalPart(patch.UpsertNodes, patch.UpsertEdges)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"additive":        additive,
		"upserts":         upserts,
		"layout_intent":   patch.LayoutIntent,
		"delete_node_ids": normalizeEvidenceProposalIDs(patch.DeleteNodeIDs),
		"delete_edge_ids": normalizeEvidenceProposalIDs(patch.DeleteEdgeIDs),
	}, nil
}

type evidenceSemanticIndex struct {
	Nodes map[string]json.RawMessage
	Edges map[string]json.RawMessage
}

func evidenceSemanticIndexFromJSON(payload []byte) (evidenceSemanticIndex, error) {
	var graph struct {
		Nodes []json.RawMessage `json:"nodes"`
		Edges []json.RawMessage `json:"edges"`
	}
	if len(payload) == 0 {
		payload = []byte(`{"nodes":[],"edges":[]}`)
	}
	if err := json.Unmarshal(payload, &graph); err != nil {
		return evidenceSemanticIndex{}, err
	}
	index := evidenceSemanticIndex{Nodes: make(map[string]json.RawMessage, len(graph.Nodes)), Edges: make(map[string]json.RawMessage, len(graph.Edges))}
	for _, raw := range graph.Nodes {
		var item struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return evidenceSemanticIndex{}, err
		}
		index.Nodes[strings.TrimSpace(item.ID)] = raw
	}
	for _, raw := range graph.Edges {
		var item struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return evidenceSemanticIndex{}, err
		}
		index.Edges[strings.TrimSpace(item.ID)] = raw
	}
	return index, nil
}

func evidenceSemanticIndexFromGraph(graph EvidenceChainGraph) (evidenceSemanticIndex, error) {
	payload, _, err := CanonicalEvidenceGraph(graph)
	if err != nil {
		return evidenceSemanticIndex{}, err
	}
	return evidenceSemanticIndexFromJSON(payload)
}

func sameEvidenceSemanticItem(left, right json.RawMessage) bool {
	return string(left) == string(right)
}

// evidencePatchRebaseBlockers performs an object-level three-way comparison.
// A stale proposal may be replayed when every node/edge it touches is unchanged
// from its immutable base snapshot. Layout fields are intentionally absent from
// these semantic snapshots, so moving cards never creates a false conflict.
func (s *SQLite) evidencePatchRebaseBlockers(ctx context.Context, chainID string, baseRevision, currentRevision int64, current EvidenceChainGraph, patch EvidenceGraphPatch) ([]EvidenceGraphBlocker, error) {
	if baseRevision == currentRevision {
		return nil, nil
	}
	var basePayload []byte
	if baseRevision > 0 {
		revision, err := s.GetEvidenceChainRevision(ctx, chainID, baseRevision)
		if err != nil {
			return nil, err
		}
		if revision == nil {
			return []EvidenceGraphBlocker{{Code: "REVISION_CONFLICT", Message: fmt.Sprintf("base revision %d is unavailable; graph is now revision %d", baseRevision, currentRevision)}}, nil
		}
		basePayload = []byte(revision.GraphJSON)
	}
	base, err := evidenceSemanticIndexFromJSON(basePayload)
	if err != nil {
		return nil, err
	}
	latest, err := evidenceSemanticIndexFromGraph(current)
	if err != nil {
		return nil, err
	}
	proposed, err := evidenceSemanticIndexFromGraph(EvidenceChainGraph{
		Nodes: append(append([]EvidenceChainNode(nil), patch.Nodes...), patch.UpsertNodes...),
		Edges: append(append([]EvidenceChainEdge(nil), patch.Edges...), patch.UpsertEdges...),
	})
	if err != nil {
		return nil, err
	}
	blockers := make([]EvidenceGraphBlocker, 0)
	for id, next := range proposed.Nodes {
		before, existedBefore := base.Nodes[id]
		now, existsNow := latest.Nodes[id]
		if (!existedBefore && !existsNow) || (existsNow && sameEvidenceSemanticItem(now, next)) || (existedBefore && existsNow && sameEvidenceSemanticItem(before, now)) {
			continue
		}
		blockers = append(blockers, EvidenceGraphBlocker{Code: "NODE_CHANGED_SINCE_BASE", Message: fmt.Sprintf("node %q changed after proposal base revision %d", id, baseRevision), NodeID: id})
	}
	for id, next := range proposed.Edges {
		before, existedBefore := base.Edges[id]
		now, existsNow := latest.Edges[id]
		if (!existedBefore && !existsNow) || (existsNow && sameEvidenceSemanticItem(now, next)) || (existedBefore && existsNow && sameEvidenceSemanticItem(before, now)) {
			continue
		}
		blockers = append(blockers, EvidenceGraphBlocker{Code: "EDGE_CHANGED_SINCE_BASE", Message: fmt.Sprintf("edge %q changed after proposal base revision %d", id, baseRevision), EdgeID: id})
	}
	for _, rawID := range patch.DeleteNodeIDs {
		id := strings.TrimSpace(rawID)
		before, existedBefore := base.Nodes[id]
		now, existsNow := latest.Nodes[id]
		if existsNow && (!existedBefore || !sameEvidenceSemanticItem(before, now)) {
			blockers = append(blockers, EvidenceGraphBlocker{Code: "NODE_CHANGED_SINCE_BASE", Message: fmt.Sprintf("node %q changed after proposal base revision %d", id, baseRevision), NodeID: id})
			continue
		}
		for edgeID, currentEdge := range latest.Edges {
			var edge canonicalEvidenceEdge
			if err := json.Unmarshal(currentEdge, &edge); err != nil {
				return nil, err
			}
			if edge.SourceNodeID != id && edge.TargetNodeID != id {
				continue
			}
			baseEdge, existed := base.Edges[edgeID]
			if !existed || !sameEvidenceSemanticItem(baseEdge, currentEdge) {
				blockers = append(blockers, EvidenceGraphBlocker{Code: "EDGE_CHANGED_SINCE_BASE", Message: fmt.Sprintf("edge %q was added or changed after proposal base revision %d and would be removed with node %q", edgeID, baseRevision, id), NodeID: id, EdgeID: edgeID})
			}
		}
	}
	for _, rawID := range patch.DeleteEdgeIDs {
		id := strings.TrimSpace(rawID)
		before, existedBefore := base.Edges[id]
		now, existsNow := latest.Edges[id]
		if existsNow && (!existedBefore || !sameEvidenceSemanticItem(before, now)) {
			blockers = append(blockers, EvidenceGraphBlocker{Code: "EDGE_CHANGED_SINCE_BASE", Message: fmt.Sprintf("edge %q changed after proposal base revision %d", id, baseRevision), EdgeID: id})
		}
	}
	return blockers, nil
}

func (s *SQLite) PlanEvidenceGraphProposal(ctx context.Context, runID string) (*EvidenceGraphProposalPlan, error) {
	card, err := s.GetProjectRunCard(ctx, runID)
	if err != nil {
		return nil, err
	}
	if card == nil {
		return nil, nil
	}
	plan := &EvidenceGraphProposalPlan{
		RunID:             card.RunID,
		ProjectCardID:     card.ID,
		ProposalHash:      card.ProposalHash,
		Status:            card.GraphStatus,
		RoutingReason:     card.GraphRoutingReason,
		BaseGraphRevision: card.BaseGraphRevision,
		Blockers:          make([]EvidenceGraphBlocker, 0),
		Warnings:          make([]EvidenceGraphWarning, 0),
	}
	if card.GraphStatus != GraphProposalPending {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "PROPOSAL_NOT_PENDING", Message: fmt.Sprintf("proposal status is %q", card.GraphStatus)})
		return plan, nil
	}
	if time.Since(card.UpdatedAt) > 14*24*time.Hour {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "PROPOSAL_EXPIRED", Message: "proposal is older than 14 days"})
	}
	var patch EvidenceGraphPatch
	if err := json.Unmarshal([]byte(card.GraphPatchJSON), &patch); err != nil {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "INVALID_GRAPH_PATCH", Message: err.Error()})
		return plan, nil
	}
	plan.ChainID = patch.ChainID
	plan.NodesAdded = len(patch.Nodes) + len(patch.UpsertNodes)
	plan.EdgesAdded = len(patch.Edges) + len(patch.UpsertEdges)
	chain, err := s.GetEvidenceChain(ctx, patch.ChainID)
	if err != nil {
		return nil, err
	}
	if chain == nil {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "GRAPH_NOT_FOUND", Message: "target evidence graph does not exist"})
		return plan, nil
	}
	plan.ProjectID = chain.ProjectID
	plan.CurrentGraphRevision = chain.Revision
	plan.AppliedGraphRevision = chain.Revision
	plan.CurrentGraphHash = chain.GraphHash
	if chain.Status == "archived" {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "GRAPH_ARCHIVED", Message: "archived evidence graph is read-only"})
	}
	current, err := s.GetEvidenceChainGraph(ctx, patch.ChainID)
	if err != nil {
		return nil, err
	}
	if chain.Revision != card.BaseGraphRevision {
		rebaseBlockers, rebaseErr := s.evidencePatchRebaseBlockers(ctx, chain.ID, card.BaseGraphRevision, chain.Revision, *current, patch)
		if rebaseErr != nil {
			return nil, rebaseErr
		}
		plan.Blockers = append(plan.Blockers, rebaseBlockers...)
		plan.AutoRebased = len(rebaseBlockers) == 0
	}
	merged := mergeEvidenceGraph(*current, patch)
	if err := validateEvidenceLayoutIntent(patch.LayoutIntent, merged); err != nil {
		plan.Blockers = append(plan.Blockers, blockerFromError(err))
	}
	if err := ValidateEvidenceChainGraph(&merged); err != nil {
		plan.Blockers = append(plan.Blockers, blockerFromError(err))
	} else {
		for _, node := range append(append([]EvidenceChainNode(nil), patch.Nodes...), patch.UpsertNodes...) {
			if node.Type != EvidenceNodeRun {
				continue
			}
			run, runErr := s.GetRun(ctx, node.RunID)
			if runErr != nil {
				return nil, runErr
			}
			if run == nil {
				plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "RUN_NOT_FOUND", Message: fmt.Sprintf("run %q does not exist", node.RunID)})
				continue
			}
			if chain.ProjectID != "" && run.ProjectID != "" && chain.ProjectID != run.ProjectID {
				plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "CROSS_PROJECT_RUN", Message: fmt.Sprintf("run %q belongs to project %q", run.ID, run.ProjectID)})
			}
			if node.ProjectCardID != "" {
				nodeCard, cardErr := s.GetProjectRunCard(ctx, node.RunID)
				if cardErr != nil {
					return nil, cardErr
				}
				if nodeCard == nil || nodeCard.ID != node.ProjectCardID {
					plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "PROJECT_CARD_NOT_FOUND", Message: fmt.Sprintf("project card %q does not match run %q", node.ProjectCardID, node.RunID)})
				}
			}
		}
		_, resultHash, hashErr := CanonicalEvidenceGraph(merged)
		if hashErr != nil {
			plan.Blockers = append(plan.Blockers, blockerFromError(hashErr))
		} else {
			plan.ResultGraphHash = resultHash
		}
	}
	if err := s.appendEvidenceEligibilityBlockers(ctx, chain.ProjectID, current, &merged, patch, plan); err != nil {
		return nil, err
	}
	appendEvidenceThreadContractBlockers(*chain, current, merged, patch, plan)
	appendEvidenceAuthoringWarnings(merged, patch, plan)
	plan.Eligible = len(plan.Blockers) == 0
	return plan, nil
}

func mergeEvidenceGraph(current EvidenceChainGraph, patch EvidenceGraphPatch) EvidenceChainGraph {
	deleteNodes := make(map[string]bool, len(patch.DeleteNodeIDs))
	for _, id := range patch.DeleteNodeIDs {
		deleteNodes[strings.TrimSpace(id)] = true
	}
	deleteEdges := make(map[string]bool, len(patch.DeleteEdgeIDs))
	for _, id := range patch.DeleteEdgeIDs {
		deleteEdges[strings.TrimSpace(id)] = true
	}
	nodeIndex := make(map[string]int)
	merged := EvidenceChainGraph{Nodes: make([]EvidenceChainNode, 0, len(current.Nodes)+len(patch.Nodes)+len(patch.UpsertNodes))}
	for _, node := range current.Nodes {
		if deleteNodes[node.ID] {
			continue
		}
		nodeIndex[node.ID] = len(merged.Nodes)
		merged.Nodes = append(merged.Nodes, node)
	}
	for _, node := range append(append([]EvidenceChainNode(nil), patch.Nodes...), patch.UpsertNodes...) {
		if index, exists := nodeIndex[node.ID]; exists {
			// A semantic update keeps the current layout unless a later layout
			// save explicitly changes it.
			node.X, node.Y = merged.Nodes[index].X, merged.Nodes[index].Y
			node.Width, node.Height, node.Pinned = merged.Nodes[index].Width, merged.Nodes[index].Height, merged.Nodes[index].Pinned
			if node.SourceRunIDs == nil {
				node.SourceRunIDs = merged.Nodes[index].SourceRunIDs
			}
			if node.SourceSnapshotIDs == nil {
				node.SourceSnapshotIDs = merged.Nodes[index].SourceSnapshotIDs
			}
			node.DataJSON = preserveEvidenceNodeLayoutData(merged.Nodes[index].DataJSON, node.DataJSON)
			merged.Nodes[index] = node
		} else {
			nodeIndex[node.ID] = len(merged.Nodes)
			merged.Nodes = append(merged.Nodes, node)
		}
	}
	edgeIndex := make(map[string]int)
	merged.Edges = make([]EvidenceChainEdge, 0, len(current.Edges)+len(patch.Edges)+len(patch.UpsertEdges))
	for _, edge := range current.Edges {
		if deleteEdges[edge.ID] || deleteNodes[edge.SourceNodeID] || deleteNodes[edge.TargetNodeID] {
			continue
		}
		edgeIndex[edge.ID] = len(merged.Edges)
		merged.Edges = append(merged.Edges, edge)
	}
	for _, edge := range append(append([]EvidenceChainEdge(nil), patch.Edges...), patch.UpsertEdges...) {
		if index, exists := edgeIndex[edge.ID]; exists {
			merged.Edges[index] = edge
		} else {
			edgeIndex[edge.ID] = len(merged.Edges)
			merged.Edges = append(merged.Edges, edge)
		}
	}
	return merged
}

func stripEvidenceProposalLayoutData(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "{}", nil
	}
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return "", err
	}
	delete(data, "collapsed")
	payload, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func preserveEvidenceNodeLayoutData(currentRaw, proposedRaw string) string {
	var current map[string]interface{}
	var proposed map[string]interface{}
	if json.Unmarshal([]byte(normalizeEvidenceDataJSON(currentRaw)), &current) != nil {
		return proposedRaw
	}
	if json.Unmarshal([]byte(normalizeEvidenceDataJSON(proposedRaw)), &proposed) != nil {
		return proposedRaw
	}
	if collapsed, exists := current["collapsed"]; exists {
		proposed["collapsed"] = collapsed
	} else {
		delete(proposed, "collapsed")
	}
	payload, err := json.Marshal(proposed)
	if err != nil {
		return proposedRaw
	}
	return string(payload)
}

func normalizeEvidenceDataJSON(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	return raw
}

func blockerFromError(err error) EvidenceGraphBlocker {
	if validation, ok := err.(*EvidenceGraphValidationError); ok {
		return EvidenceGraphBlocker{Code: validation.Code, Message: validation.Message}
	}
	return EvidenceGraphBlocker{Code: "GRAPH_VALIDATION_FAILED", Message: err.Error()}
}

func (s *SQLite) appendEvidenceEligibilityBlockers(ctx context.Context, projectID string, current, merged *EvidenceChainGraph, patch EvidenceGraphPatch, plan *EvidenceGraphProposalPlan) error {
	nodes := make(map[string]EvidenceChainNode, len(merged.Nodes))
	for _, node := range merged.Nodes {
		nodes[node.ID] = node
	}
	patchedNodeIDs := make(map[string]bool)
	for _, node := range append(append([]EvidenceChainNode(nil), patch.Nodes...), patch.UpsertNodes...) {
		patchedNodeIDs[node.ID] = true
	}
	branchBlockers := make(map[string]bool)
	appendBranchBlocker := func(edge EvidenceChainEdge) {
		if branchBlockers[edge.ID] || !evidenceResearchBranchBypass(nodes, edge) {
			return
		}
		branchBlockers[edge.ID] = true
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{
			Code:    "THREAD_BRANCH_HYPOTHESIS_REQUIRED",
			Message: "a Conclusion or Issue may open a new research thread only through next_step to a canonical claimKind=hypothesis node",
			NodeID:  edge.SourceNodeID,
			EdgeID:  edge.ID,
		})
	}
	// An edited outgoing outcome edge also touches the Result's interpretation.
	// Neutral context links do not migrate a historical Result. A semantic edge
	// that skips Conclusion/Issue is always blocked at the patch boundary.
	for _, edge := range append(append([]EvidenceChainEdge(nil), patch.Edges...), patch.UpsertEdges...) {
		appendBranchBlocker(edge)
		source := nodes[edge.SourceNodeID]
		if source.Type == EvidenceNodeClaim && evidenceAuthoringClaimKind(source) == "" && evidenceResearchNodeStage(source) == EvidenceResearchStageResult && evidenceResultOutcomeEdge(nodes, edge) {
			plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{
				Code: "CLAIM_KIND_REQUIRED", Message: "a Claim used as a Result must declare claimKind=result", NodeID: source.ID, EdgeID: edge.ID,
			})
		}
		if evidenceResultOutcomeBypass(nodes, edge) {
			target := nodes[edge.TargetNodeID]
			plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{
				Code:    "RESULT_OUTCOME_BYPASS",
				Message: fmt.Sprintf("Result %q cannot connect directly to %s %q with %s; route it to a Conclusion and/or Issue first", edge.SourceNodeID, target.Type, edge.TargetNodeID, edge.Type),
				NodeID:  edge.SourceNodeID,
				EdgeID:  edge.ID,
			})
			continue
		}
		if evidenceResultOutcomeEdge(nodes, edge) {
			patchedNodeIDs[edge.SourceNodeID] = true
		}
	}
	// Editing either endpoint of a historical bypass makes that semantic object
	// part of the current proposal. Require the same patch to delete the bypass
	// and replace it with an explicit child hypothesis. Merely reading the map or
	// editing another thread does not inherit this historical debt.
	deletedEdgesForBranch := make(map[string]bool, len(patch.DeleteEdgeIDs))
	for _, id := range patch.DeleteEdgeIDs {
		deletedEdgesForBranch[strings.TrimSpace(id)] = true
	}
	for _, edge := range merged.Edges {
		if deletedEdgesForBranch[edge.ID] {
			continue
		}
		if patchedNodeIDs[edge.SourceNodeID] || patchedNodeIDs[edge.TargetNodeID] {
			appendBranchBlocker(edge)
		}
	}
	// Deleting or replacing an accepted outcome edge/node also touches the
	// source Result. Revalidate its disposition against the final merged graph
	// so a deletion cannot leave a stale "conclusion" or "issue" declaration.
	if current != nil {
		deletedEdges := make(map[string]bool, len(patch.DeleteEdgeIDs))
		for _, id := range patch.DeleteEdgeIDs {
			deletedEdges[strings.TrimSpace(id)] = true
		}
		deletedNodes := make(map[string]bool, len(patch.DeleteNodeIDs))
		for _, id := range patch.DeleteNodeIDs {
			deletedNodes[strings.TrimSpace(id)] = true
		}
		upsertedEdges := make(map[string]bool, len(patch.UpsertEdges))
		for _, edge := range patch.UpsertEdges {
			upsertedEdges[edge.ID] = true
		}
		currentNodes := make(map[string]EvidenceChainNode, len(current.Nodes))
		for _, node := range current.Nodes {
			currentNodes[node.ID] = node
		}
		for _, edge := range current.Edges {
			if !deletedEdges[edge.ID] && !deletedNodes[edge.TargetNodeID] && !upsertedEdges[edge.ID] {
				continue
			}
			if evidenceResultOutcomeEdge(currentNodes, edge) && !deletedNodes[edge.SourceNodeID] {
				patchedNodeIDs[edge.SourceNodeID] = true
			}
		}
	}
	patchedResultIDs := make([]string, 0)
	for nodeID := range patchedNodeIDs {
		node := nodes[nodeID]
		if !evidenceResultNode(node) {
			continue
		}
		patchedResultIDs = append(patchedResultIDs, nodeID)
		if len(node.SourceRunIDs) == 0 && len(node.SourceSnapshotIDs) == 0 {
			plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{
				Code: "RESULT_PROVENANCE_REQUIRED", Message: "result claim requires at least one source_run_id or immutable source_snapshot_id", NodeID: node.ID,
			})
		}
	}
	sort.Strings(patchedResultIDs)
	for _, nodeID := range patchedResultIDs {
		node := nodes[nodeID]
		for _, sourceRunID := range node.SourceRunIDs {
			runID := strings.TrimSpace(sourceRunID)
			run, err := s.GetRun(ctx, runID)
			if err != nil {
				return err
			}
			if run == nil {
				plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "RUN_NOT_FOUND", Message: fmt.Sprintf("run %q does not exist", runID), NodeID: node.ID, RunID: runID})
				continue
			}
			if strings.TrimSpace(run.ProjectID) == "" || run.ProjectID != projectID {
				plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "RUN_PROJECT_MISMATCH", Message: fmt.Sprintf("run %q does not belong to project %q", run.ID, projectID), NodeID: node.ID, RunID: run.ID})
				continue
			}
			if !IsRunTerminalStatus(run.Status) {
				plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "RESULT_RUN_NOT_TERMINAL", Message: fmt.Sprintf("run %q is %q; Result provenance requires a terminal Run", run.ID, run.Status), NodeID: node.ID, RunID: run.ID})
			}
		}
		disposition := evidenceAuthoringResultDisposition(node)
		if disposition == "" {
			plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{
				Code: "RESULT_DISPOSITION_REQUIRED", Message: "result claim must declare resultDisposition as conclusion, issue, mixed, or pending", NodeID: node.ID,
			})
			continue
		}
		if !validEvidenceResultDisposition(disposition) {
			plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{
				Code: "RESULT_DISPOSITION_INVALID", Message: fmt.Sprintf("resultDisposition %q is invalid; use conclusion, issue, mixed, or pending", disposition), NodeID: node.ID,
			})
			continue
		}
		hasConclusion, hasIssue, issueExplained := evidenceResultDispositionOutcomes(*merged, node.ID)
		matches := false
		switch disposition {
		case EvidenceResultDispositionConclusion:
			matches = hasConclusion && !hasIssue
		case EvidenceResultDispositionIssue:
			matches = hasIssue && !hasConclusion
		case EvidenceResultDispositionMixed:
			matches = hasConclusion && hasIssue
		case EvidenceResultDispositionPending:
			matches = !hasConclusion && !hasIssue
		}
		if !matches {
			plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{
				Code: "RESULT_DISPOSITION_EDGE_MISMATCH", Message: fmt.Sprintf("resultDisposition %q does not match the Result's Conclusion/Issue outcome edges", disposition), NodeID: node.ID,
			})
		}
		reason := strings.TrimSpace(evidenceAuthoringDispositionReason(node))
		if disposition == EvidenceResultDispositionPending && reason == "" {
			plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{
				Code: "RESULT_PENDING_REASON_REQUIRED", Message: "pending result disposition requires dispositionReason", NodeID: node.ID,
			})
		}
		if (disposition == EvidenceResultDispositionIssue || disposition == EvidenceResultDispositionMixed) && reason == "" && !issueExplained {
			plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{
				Code: "RESULT_ISSUE_WITHOUT_INCONCLUSIVE_REASON", Message: "a Result routed to an Issue must explain why it cannot form a stable conclusion, either in dispositionReason or the reveals_issue rationale", NodeID: node.ID,
			})
		}
	}
	uses := make([]formalRunEvidenceUse, 0)
	for _, edge := range append(append([]EvidenceChainEdge(nil), patch.Edges...), patch.UpsertEdges...) {
		if edge.Type != EvidenceEdgeSupports && edge.Type != EvidenceEdgeWeakens && edge.Type != EvidenceEdgeDoesNotProve {
			continue
		}
		source := nodes[edge.SourceNodeID]
		if source.Type != EvidenceNodeRun && strings.TrimSpace(source.RunID) == "" {
			continue
		}
		uses = append(uses, formalRunEvidenceUse{Node: source, EdgeID: edge.ID})
	}
	// The canonical v2 authoring model keeps Run identity on a Result's
	// source_run_ids instead of requiring visual Run nodes. Formal readiness
	// must therefore follow that provenance recursively; otherwise a Result
	// card could support a Conclusion while silently bypassing the same gate
	// that protects a legacy Run -> Claim edge.
	for _, nodeID := range patchedResultIDs {
		node := nodes[nodeID]
		disposition := evidenceAuthoringResultDisposition(node)
		if disposition != EvidenceResultDispositionConclusion && disposition != EvidenceResultDispositionMixed {
			continue
		}
		edgeID := ""
		for _, edge := range merged.Edges {
			if edge.SourceNodeID == node.ID && evidenceResultOutcomeEdge(nodes, edge) {
				edgeID = edge.ID
				break
			}
		}
		for _, runID := range node.SourceRunIDs {
			uses = append(uses, formalRunEvidenceUse{Node: EvidenceChainNode{
				ID:    node.ID,
				RunID: strings.TrimSpace(runID),
			}, EdgeID: edgeID})
		}
	}
	blockers, err := s.formalRunEvidenceBlockers(ctx, projectID, uses)
	if err != nil {
		return err
	}
	plan.Blockers = append(plan.Blockers, blockers...)
	return nil
}

type formalRunEvidenceUse struct {
	Node   EvidenceChainNode
	EdgeID string
}

// formalRunEvidenceBlockers is the shared boundary for every operation that
// turns a Run into formal research evidence. A successful formal Run is still
// not publishable evidence until immutable provenance is complete and at least
// one EvidenceSnapshot has passed a release gate.
func (s *SQLite) formalRunEvidenceBlockers(ctx context.Context, projectID string, uses []formalRunEvidenceUse) ([]EvidenceGraphBlocker, error) {
	sort.Slice(uses, func(i, j int) bool {
		leftRunID := strings.TrimSpace(uses[i].Node.RunID)
		rightRunID := strings.TrimSpace(uses[j].Node.RunID)
		if leftRunID != rightRunID {
			return leftRunID < rightRunID
		}
		if uses[i].Node.ID != uses[j].Node.ID {
			return uses[i].Node.ID < uses[j].Node.ID
		}
		return uses[i].EdgeID < uses[j].EdgeID
	})
	blockers := make([]EvidenceGraphBlocker, 0)
	checked := make(map[string]bool, len(uses))
	for _, use := range uses {
		runID := strings.TrimSpace(use.Node.RunID)
		if runID == "" {
			blockers = append(blockers, EvidenceGraphBlocker{
				Code: "RUN_ID_REQUIRED", Message: "formal Run evidence node requires run_id",
				NodeID: use.Node.ID, EdgeID: use.EdgeID,
			})
			continue
		}
		if checked[runID] {
			continue
		}
		checked[runID] = true
		run, err := s.GetRun(ctx, runID)
		if err != nil {
			return nil, err
		}
		if run == nil {
			blockers = append(blockers, EvidenceGraphBlocker{
				Code: "RUN_NOT_FOUND", Message: fmt.Sprintf("run %q does not exist", runID),
				NodeID: use.Node.ID, EdgeID: use.EdgeID, RunID: runID,
			})
			continue
		}
		if strings.TrimSpace(run.ProjectID) == "" || run.ProjectID != projectID {
			blockers = append(blockers, EvidenceGraphBlocker{
				Code: "RUN_PROJECT_MISMATCH", Message: fmt.Sprintf("run %q does not belong to project %q", run.ID, projectID),
				NodeID: use.Node.ID, EdgeID: use.EdgeID, RunID: run.ID,
			})
			continue
		}
		readiness, err := CheckRunClaimReadiness(ctx, s, run)
		if err != nil {
			return nil, err
		}
		for _, blocker := range readiness {
			code := strings.ToUpper(blocker.Code)
			if blocker.Code == "run_not_formal" {
				code = "RUN_NOT_FORMAL_EVIDENCE"
			}
			blockers = append(blockers, EvidenceGraphBlocker{
				Code:    code,
				Message: fmt.Sprintf("run %q: %s", run.ID, blocker.Message),
				NodeID:  use.Node.ID,
				EdgeID:  use.EdgeID,
				RunID:   run.ID,
			})
		}
		released, err := s.runHasReleasedEvidenceSnapshot(ctx, projectID, run.ID)
		if err != nil {
			return nil, err
		}
		if !released {
			blockers = append(blockers, EvidenceGraphBlocker{
				Code:    "EVIDENCE_RELEASE_MISSING",
				Message: fmt.Sprintf("run %q has no released EvidenceSnapshot in project %q", run.ID, projectID),
				NodeID:  use.Node.ID, EdgeID: use.EdgeID, RunID: run.ID,
			})
		}
	}
	return blockers, nil
}

func (s *SQLite) runHasReleasedEvidenceSnapshot(ctx context.Context, projectID, runID string) (bool, error) {
	snapshots, err := s.ListEvidenceSnapshots(ctx, runID)
	if err != nil {
		return false, err
	}
	for _, snapshot := range snapshots {
		if snapshot.ProjectID != projectID {
			continue
		}
		releases, err := s.ListEvidenceReleases(ctx, snapshot.ID)
		if err != nil {
			return false, err
		}
		for _, release := range releases {
			if release.ProjectID == projectID && release.State == EvidenceReleaseReleased {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *SQLite) ReviewEvidenceGraphProposal(ctx context.Context, runID, action, reviewer string) (*ProjectRunCard, error) {
	card, err := s.GetProjectRunCard(ctx, runID)
	if err != nil {
		return nil, err
	}
	if card == nil {
		return nil, sql.ErrNoRows
	}
	if card.GraphStatus != GraphProposalPending {
		return nil, graphValidationError("PROPOSAL_NOT_PENDING", fmt.Sprintf("proposal status is %q", card.GraphStatus))
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "reject" || action == "expire" {
		nextStatus := GraphProposalRejected
		if action == "expire" {
			nextStatus = GraphProposalExpired
		}
		now := time.Now()
		result, err := s.db.ExecContext(ctx,
			`UPDATE project_run_cards SET graph_status = ?, reviewed_at = ?, updated_at = ? WHERE run_id = ? AND graph_status = ? AND proposal_hash = ?`,
			nextStatus, now, now, runID, GraphProposalPending, card.ProposalHash,
		)
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return nil, graphValidationError("PROPOSAL_CHANGED", "proposal changed while it was being reviewed")
		}
		return s.GetProjectRunCard(ctx, runID)
	}
	if action != "accept" {
		return nil, graphValidationError("INVALID_REVIEW_ACTION", "action must be accept, reject, or expire")
	}
	plan, err := s.PlanEvidenceGraphProposal(ctx, runID)
	if err != nil {
		return nil, err
	}
	if plan == nil || !plan.Eligible {
		return nil, graphValidationError("PROPOSAL_BLOCKED", "proposal has blockers; run the side-effect-free plan first")
	}
	var patch EvidenceGraphPatch
	if err := json.Unmarshal([]byte(card.GraphPatchJSON), &patch); err != nil {
		return nil, err
	}
	current, err := s.GetEvidenceChainGraph(ctx, patch.ChainID)
	if err != nil {
		return nil, err
	}
	merged := mergeEvidenceGraph(*current, patch)
	chain, err := s.GetEvidenceChain(ctx, patch.ChainID)
	if err != nil {
		return nil, err
	}
	if chain == nil {
		return nil, sql.ErrNoRows
	}
	for _, node := range merged.Nodes {
		if node.Type != EvidenceNodeRun {
			continue
		}
		run, err := s.GetRun(ctx, node.RunID)
		if err != nil {
			return nil, err
		}
		if run == nil {
			return nil, graphValidationError("RUN_NOT_FOUND", fmt.Sprintf("run %q does not exist", node.RunID))
		}
		if chain.ProjectID != "" && run.ProjectID != "" && chain.ProjectID != run.ProjectID {
			return nil, graphValidationError("CROSS_PROJECT_RUN", fmt.Sprintf("run %q belongs to project %q", run.ID, run.ProjectID))
		}
	}
	graphJSON, graphHash, err := CanonicalEvidenceGraph(merged)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := replaceEvidenceGraphTx(ctx, tx, patch.ChainID, merged, EvidenceGraphSaveOptions{
		ExpectedRevision: plan.AppliedGraphRevision,
		Actor:            strings.TrimSpace(reviewer),
		SourceKind:       "project_run_card",
		SourceID:         card.ID,
	}, graphJSON, graphHash); err != nil {
		return nil, err
	}
	now := time.Now()
	result, err := tx.ExecContext(ctx,
		`UPDATE project_run_cards SET graph_status = ?, reviewed_at = ?, updated_at = ?
		 WHERE run_id = ? AND graph_status = ? AND proposal_hash = ?`,
		GraphProposalAccepted, now, now, runID, GraphProposalPending, card.ProposalHash,
	)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, graphValidationError("PROPOSAL_CHANGED", "proposal changed while it was being accepted")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetProjectRunCard(ctx, runID)
}
