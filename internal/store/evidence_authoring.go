package store

import (
	"encoding/json"
	"sort"
	"strings"
)

const (
	EvidenceResultDispositionConclusion = "conclusion"
	EvidenceResultDispositionIssue      = "issue"
	EvidenceResultDispositionMixed      = "mixed"
	EvidenceResultDispositionPending    = "pending"
)

// appendEvidenceAuthoringWarnings checks only nodes introduced or updated by
// the proposal. Historical graph debt remains visible in the UI, but it does
// not get repeated on every unrelated proposal plan.
func appendEvidenceAuthoringWarnings(merged EvidenceChainGraph, patch EvidenceGraphPatch, plan *EvidenceGraphProposalPlan) {
	if plan == nil {
		return
	}
	patched := make(map[string]EvidenceChainNode)
	for _, node := range append(append([]EvidenceChainNode(nil), patch.Nodes...), patch.UpsertNodes...) {
		patched[node.ID] = node
	}
	if len(patched) == 0 {
		return
	}
	nodes := make(map[string]EvidenceChainNode, len(merged.Nodes))
	hasHypothesis := false
	for _, node := range merged.Nodes {
		nodes[node.ID] = node
		if evidenceAuthoringClaimKind(node) == "hypothesis" || node.Type == EvidenceNodeHypothesis {
			hasHypothesis = true
		}
	}
	incident := make(map[string]int)
	nextSteps := make(map[string]int)
	runOutcomes := make(map[string]int)
	resultInputs := make(map[string]int)
	for _, edge := range merged.Edges {
		if edge.Type == EvidenceEdgeRelatedTo || edge.Type == EvidenceEdgeCustom {
			continue
		}
		incident[edge.SourceNodeID]++
		incident[edge.TargetNodeID]++
		if edge.Type == EvidenceEdgeNextStep {
			nextSteps[edge.SourceNodeID]++
		}
		if edge.Type == EvidenceEdgeSupports || edge.Type == EvidenceEdgeWeakens || edge.Type == EvidenceEdgeDoesNotProve {
			if nodes[edge.SourceNodeID].Type == EvidenceNodeRun {
				runOutcomes[edge.SourceNodeID]++
			}
			resultInputs[edge.TargetNodeID]++
		}
		if edge.Type == EvidenceEdgeRevealsIssue && nodes[edge.SourceNodeID].Type == EvidenceNodeRun {
			runOutcomes[edge.SourceNodeID]++
		}
	}

	durableCount := 0
	for _, node := range patched {
		if evidenceAuthoringDurableType(node.Type) {
			durableCount++
		}
	}
	if durableCount >= 2 && !hasHypothesis {
		plan.Warnings = append(plan.Warnings, EvidenceGraphWarning{
			Code:    "THREAD_HYPOTHESIS_MISSING",
			Message: "proposal adds a research thread without an explicit hypothesis; add a claim with claimKind=hypothesis or route the nodes into an existing thread",
		})
	}

	for _, node := range patched {
		mergedNode, exists := nodes[node.ID]
		if exists {
			node = mergedNode
		}
		switch node.Type {
		case EvidenceNodeIssue:
			if nextSteps[node.ID] == 0 {
				plan.Warnings = append(plan.Warnings, EvidenceGraphWarning{Code: "ISSUE_NEXT_STEP_MISSING", Message: "issue has no next_step child hypothesis", NodeID: node.ID})
			}
		case EvidenceNodeRun:
			if runOutcomes[node.ID] == 0 {
				plan.Warnings = append(plan.Warnings, EvidenceGraphWarning{Code: "RUN_OUTCOME_MISSING", Message: "run is not connected to a result claim or issue", NodeID: node.ID})
			}
		case EvidenceNodeConclusion:
			if resultInputs[node.ID] == 0 {
				plan.Warnings = append(plan.Warnings, EvidenceGraphWarning{Code: "RESULT_EVIDENCE_MISSING", Message: "result has no supporting, weakening, or does-not-prove evidence", NodeID: node.ID})
			}
		case EvidenceNodeClaim:
			if evidenceAuthoringClaimKind(node) == "result" && len(node.SourceRunIDs) == 0 && len(node.SourceSnapshotIDs) == 0 {
				plan.Warnings = append(plan.Warnings, EvidenceGraphWarning{Code: "RESULT_EVIDENCE_MISSING", Message: "result claim has no immutable Run or EvidenceSnapshot provenance", NodeID: node.ID})
			} else if incident[node.ID] == 0 {
				plan.Warnings = append(plan.Warnings, EvidenceGraphWarning{Code: "SEMANTIC_NODE_DISCONNECTED", Message: "claim is not connected to the current research thread", NodeID: node.ID})
			}
		case EvidenceNodeHypothesis, EvidenceNodePlan, EvidenceNodeExperiment:
			if incident[node.ID] == 0 && evidenceAuthoringGroupID(node) == "" {
				plan.Warnings = append(plan.Warnings, EvidenceGraphWarning{Code: "SEMANTIC_NODE_DISCONNECTED", Message: "semantic node is not connected to the current research thread", NodeID: node.ID})
			}
		case EvidenceNodeGroup:
			if evidenceAuthoringGroupKind(node) == "protocol" {
				plan.Warnings = append(plan.Warnings, EvidenceGraphWarning{Code: "LEGACY_PROTOCOL_GROUP_DEPRECATED", Message: "new protocol collections are deprecated; use one experiment design node and keep runs, datasets, seeds and config hashes in provenance", NodeID: node.ID})
			}
		}
	}
	sort.SliceStable(plan.Warnings, func(i, j int) bool {
		if plan.Warnings[i].Code != plan.Warnings[j].Code {
			return plan.Warnings[i].Code < plan.Warnings[j].Code
		}
		return plan.Warnings[i].NodeID < plan.Warnings[j].NodeID
	})
}

// appendEvidenceThreadContractBlockers validates the authored execution spine
// against the same read projection used by the UI and MCP. Metadata such as a
// caller-supplied threadRootId is intentionally ignored: visible typed edges
// are the only source of Research Thread ownership.
func appendEvidenceThreadContractBlockers(chain EvidenceChain, current *EvidenceChainGraph, merged EvidenceChainGraph, patch EvidenceGraphPatch, plan *EvidenceGraphProposalPlan) {
	if plan == nil {
		return
	}
	previewChain := chain
	previewChain.Revision++
	if strings.TrimSpace(plan.ResultGraphHash) != "" {
		previewChain.GraphHash = plan.ResultGraphHash
	}
	plan.ProjectedResearch = ptrEvidenceResearchProjection(BuildEvidenceResearchProjection(previewChain, merged))

	nodes := make(map[string]EvidenceChainNode, len(merged.Nodes))
	for _, node := range merged.Nodes {
		nodes[node.ID] = node
	}
	touched := make(map[string]bool)
	for _, node := range append(append([]EvidenceChainNode(nil), patch.Nodes...), patch.UpsertNodes...) {
		touched[node.ID] = true
	}
	for _, edge := range append(append([]EvidenceChainEdge(nil), patch.Edges...), patch.UpsertEdges...) {
		if edge.Type == EvidenceEdgeRelatedTo || edge.Type == EvidenceEdgeCustom {
			continue
		}
		touched[edge.SourceNodeID] = true
		touched[edge.TargetNodeID] = true
	}
	if current != nil && len(patch.DeleteEdgeIDs) > 0 {
		deleted := make(map[string]bool, len(patch.DeleteEdgeIDs))
		for _, id := range patch.DeleteEdgeIDs {
			deleted[strings.TrimSpace(id)] = true
		}
		for _, edge := range current.Edges {
			if deleted[edge.ID] {
				touched[edge.SourceNodeID] = true
				touched[edge.TargetNodeID] = true
			}
		}
	}

	designInput := make(map[string]bool)
	for _, edge := range merged.Edges {
		if edge.Type == EvidenceEdgeNextStep && evidenceResearchNodeStage(nodes[edge.SourceNodeID]) == EvidenceResearchStageDesign && evidenceResearchNodeStage(nodes[edge.TargetNodeID]) == EvidenceResearchStageResult {
			designInput[edge.TargetNodeID] = true
		}
	}
	owner := plan.ProjectedResearch.OwnerByNode
	ids := make([]string, 0, len(touched))
	for id := range touched {
		if nodes[id].Type == EvidenceNodeClaim && evidenceAuthoringClaimKind(nodes[id]) == "result" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !designInput[id] {
			plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{
				Code: "RESULT_DESIGN_LINK_MISSING", Message: "a new or touched Result must have an incoming experiment design --next_step--> Result edge", NodeID: id,
			})
		}
		if owner[id] == "" {
			plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{
				Code: "RESULT_THREAD_UNASSIGNED", Message: "the projected Result is not owned by any hypothesis-led Research Thread; connect Hypothesis --next_step--> Design --next_step--> Result", NodeID: id,
			})
		}
	}
}

func ptrEvidenceResearchProjection(value EvidenceResearchProjection) *EvidenceResearchProjection {
	return &value
}

func evidenceAuthoringDurableType(nodeType string) bool {
	switch nodeType {
	case EvidenceNodeClaim, EvidenceNodeIssue, EvidenceNodePlan, EvidenceNodeRun, EvidenceNodeHypothesis, EvidenceNodeConclusion, EvidenceNodeExperiment:
		return true
	default:
		return false
	}
}

func evidenceAuthoringData(node EvidenceChainNode) map[string]any {
	data := make(map[string]any)
	_ = json.Unmarshal([]byte(node.DataJSON), &data)
	return data
}

func evidenceAuthoringString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := data[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func evidenceAuthoringClaimKind(node EvidenceChainNode) string {
	return strings.ToLower(evidenceAuthoringString(evidenceAuthoringData(node), "claimKind", "claim_kind"))
}

func evidenceAuthoringResultDisposition(node EvidenceChainNode) string {
	return strings.ToLower(evidenceAuthoringString(evidenceAuthoringData(node), "resultDisposition", "result_disposition"))
}

func evidenceAuthoringDispositionReason(node EvidenceChainNode) string {
	return evidenceAuthoringString(evidenceAuthoringData(node), "dispositionReason", "disposition_reason")
}

func validEvidenceResultDisposition(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case EvidenceResultDispositionConclusion, EvidenceResultDispositionIssue, EvidenceResultDispositionMixed, EvidenceResultDispositionPending:
		return true
	default:
		return false
	}
}

func evidenceResultDispositionOutcomes(graph EvidenceChainGraph, resultID string) (hasConclusion, hasIssue, issueExplained bool) {
	nodes := make(map[string]EvidenceChainNode, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range graph.Edges {
		if edge.SourceNodeID != resultID {
			continue
		}
		target := nodes[edge.TargetNodeID]
		switch {
		case target.Type == EvidenceNodeConclusion && (edge.Type == EvidenceEdgeSupports || edge.Type == EvidenceEdgeWeakens || edge.Type == EvidenceEdgeDoesNotProve):
			hasConclusion = true
		case target.Type == EvidenceNodeIssue && edge.Type == EvidenceEdgeRevealsIssue:
			hasIssue = true
			if strings.TrimSpace(edge.Rationale) != "" {
				issueExplained = true
			}
		}
	}
	return hasConclusion, hasIssue, issueExplained
}

func evidenceResultOutcomeEdge(nodes map[string]EvidenceChainNode, edge EvidenceChainEdge) bool {
	// The read model treats an untyped legacy Claim as a Result unless it is an
	// explicit hypothesis. Use the same classification here so copied/reorged
	// legacy Result cards cannot bypass the new authoring contract.
	if evidenceResearchNodeStage(nodes[edge.SourceNodeID]) != EvidenceResearchStageResult {
		return false
	}
	target := nodes[edge.TargetNodeID]
	if target.Type == EvidenceNodeConclusion {
		return edge.Type == EvidenceEdgeSupports || edge.Type == EvidenceEdgeWeakens || edge.Type == EvidenceEdgeDoesNotProve
	}
	return target.Type == EvidenceNodeIssue && edge.Type == EvidenceEdgeRevealsIssue
}

// evidenceResultOutcomeBypass identifies a semantic edge that skips the
// Result -> interpretation -> Conclusion/Issue authoring contract. Historical
// graphs remain readable; proposal planning blocks only newly added/upserted
// bypasses, while full-map audit reports already accepted debt.
func evidenceResultOutcomeBypass(nodes map[string]EvidenceChainNode, edge EvidenceChainEdge) bool {
	if evidenceResearchNodeStage(nodes[edge.SourceNodeID]) != EvidenceResearchStageResult || !isSemanticEvidenceEdge(edge.Type) {
		return false
	}
	return !evidenceResultOutcomeEdge(nodes, edge)
}

// evidenceResearchBranchBypass identifies an outcome branch that skips the
// explicit child hypothesis required by research-thread-v2. Historical maps
// may still contain Issue/Conclusion -> Plan/Experiment edges; proposal
// eligibility applies this check only to newly added or semantically touched
// objects so that unrelated edits are not blocked by historical debt.
func evidenceResearchBranchBypass(nodes map[string]EvidenceChainNode, edge EvidenceChainEdge) bool {
	if edge.Type != EvidenceEdgeNextStep {
		return false
	}
	source := nodes[edge.SourceNodeID]
	if source.Type != EvidenceNodeIssue && source.Type != EvidenceNodeConclusion {
		return false
	}
	target := nodes[edge.TargetNodeID]
	return target.Type != EvidenceNodeClaim || evidenceAuthoringClaimKind(target) != "hypothesis"
}

func evidenceAuthoringGroupID(node EvidenceChainNode) string {
	return evidenceAuthoringString(evidenceAuthoringData(node), "groupId", "group_id")
}

func evidenceAuthoringGroupKind(node EvidenceChainNode) string {
	return strings.ToLower(evidenceAuthoringString(evidenceAuthoringData(node), "groupKind", "group_kind"))
}

func evidenceAuthoringHasProtocolIdentity(node EvidenceChainNode) bool {
	data := evidenceAuthoringData(node)
	return evidenceAuthoringString(data, "version", "provenanceSummary", "provenance_summary") != ""
}
