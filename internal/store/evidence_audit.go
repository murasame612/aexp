package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// EvidenceChainAuditReport is a side-effect-free integrity report for the
// complete current graph, not merely the latest proposal patch.
type EvidenceChainAuditReport struct {
	SchemaVersion          string                           `json:"schema_version"`
	ChainID                string                           `json:"chain_id"`
	ProjectID              string                           `json:"project_id"`
	Role                   string                           `json:"role"`
	Revision               int64                            `json:"revision"`
	StoredGraphHash        string                           `json:"stored_graph_hash"`
	CurrentGraphHash       string                           `json:"current_graph_hash"`
	Eligible               bool                             `json:"eligible"`
	ReadabilityStatus      string                           `json:"readability_status"`
	V2ComplianceStatus     string                           `json:"v2_compliance_status"`
	PublicationStatus      string                           `json:"publication_status"`
	PublicationResultCount int                              `json:"publication_result_count"`
	ResearchHealth         EvidenceResearchStructuralHealth `json:"research_health"`
	Blockers               []EvidenceGraphBlocker           `json:"blockers"`
	Warnings               []EvidenceGraphWarning           `json:"warnings"`
}

const evidenceChainAuditSchemaVersion = "evidence-map-audit-v1"

func (s *SQLite) AuditEvidenceChain(ctx context.Context, chainID string) (*EvidenceChainAuditReport, error) {
	chain, err := s.GetEvidenceChain(ctx, chainID)
	if err != nil || chain == nil {
		return nil, err
	}
	graph, err := s.GetEvidenceChainGraph(ctx, chainID)
	if err != nil {
		return nil, err
	}
	report := &EvidenceChainAuditReport{
		SchemaVersion: evidenceChainAuditSchemaVersion,
		ChainID:       chain.ID, ProjectID: chain.ProjectID, Role: chain.Role,
		Revision: chain.Revision, StoredGraphHash: chain.GraphHash,
		Blockers: make([]EvidenceGraphBlocker, 0),
		Warnings: make([]EvidenceGraphWarning, 0),
	}
	_, report.CurrentGraphHash, err = CanonicalEvidenceGraph(*graph)
	if err != nil {
		report.Blockers = append(report.Blockers, blockerFromError(err))
	} else if report.CurrentGraphHash != chain.GraphHash {
		report.Blockers = append(report.Blockers, EvidenceGraphBlocker{
			Code:    "GRAPH_HASH_MISMATCH",
			Message: fmt.Sprintf("stored graph hash %q does not match current graph hash %q", chain.GraphHash, report.CurrentGraphHash),
		})
	}

	report.Blockers = append(report.Blockers, auditEvidenceGraphShape(graph)...)
	nodes := make(map[string]EvidenceChainNode, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
		if node.Type != EvidenceNodeRun {
			continue
		}
		report.Warnings = append(report.Warnings, EvidenceGraphWarning{
			Code: "LEGACY_RUN_NODE", Message: "visual Run nodes are compatibility data; new Result provenance belongs in source_run_ids/source_snapshot_ids", NodeID: node.ID,
		})
		run, getErr := s.GetRun(ctx, node.RunID)
		if getErr != nil {
			return nil, getErr
		}
		if run == nil {
			report.Warnings = append(report.Warnings, EvidenceGraphWarning{Code: "LEGACY_RUN_NOT_FOUND", Message: fmt.Sprintf("legacy run %q does not exist", node.RunID), NodeID: node.ID})
			continue
		}
		if strings.TrimSpace(run.ProjectID) == "" || run.ProjectID != chain.ProjectID {
			report.Warnings = append(report.Warnings, EvidenceGraphWarning{Code: "LEGACY_RUN_PROJECT_MISMATCH", Message: fmt.Sprintf("legacy run %q does not belong to project %q", run.ID, chain.ProjectID), NodeID: node.ID})
		}
		if node.ProjectCardID != "" {
			card, cardErr := s.GetProjectRunCard(ctx, run.ID)
			if cardErr != nil {
				return nil, cardErr
			}
			if card == nil || card.ID != node.ProjectCardID || card.ProjectID != chain.ProjectID {
				report.Warnings = append(report.Warnings, EvidenceGraphWarning{Code: "LEGACY_PROJECT_CARD_MISMATCH", Message: fmt.Sprintf("legacy project card %q does not match run %q and project %q", node.ProjectCardID, run.ID, chain.ProjectID), NodeID: node.ID})
			}
		}
	}
	for _, edge := range graph.Edges {
		if !evidenceResearchBranchBypass(nodes, edge) {
			continue
		}
		report.Warnings = append(report.Warnings, EvidenceGraphWarning{
			Code:    "LEGACY_THREAD_BRANCH_BYPASS",
			Message: "historical outcome branch skips an explicit child hypothesis; keep it readable, but repair it through a reviewed proposal before editing either endpoint",
			NodeID:  edge.SourceNodeID,
			EdgeID:  edge.ID,
		})
	}
	if refErr := s.ValidateEvidenceMapReferences(ctx, chain, graph); refErr != nil {
		report.Blockers = append(report.Blockers, blockerFromError(refErr))
	}
	legacyUses := make([]formalRunEvidenceUse, 0)
	for _, edge := range graph.Edges {
		if edge.Type != EvidenceEdgeSupports && edge.Type != EvidenceEdgeWeakens && edge.Type != EvidenceEdgeDoesNotProve {
			continue
		}
		source := nodes[edge.SourceNodeID]
		if source.Type == EvidenceNodeRun {
			legacyUses = append(legacyUses, formalRunEvidenceUse{Node: source, EdgeID: edge.ID})
		}
	}
	readiness, err := s.formalRunEvidenceBlockers(ctx, chain.ProjectID, legacyUses)
	if err != nil {
		return nil, err
	}
	for _, blocker := range readiness {
		report.Warnings = append(report.Warnings, EvidenceGraphWarning{Code: "LEGACY_" + blocker.Code, Message: blocker.Message, NodeID: blocker.NodeID, EdgeID: blocker.EdgeID})
	}
	resultBlockers, err := s.auditEvidenceResults(ctx, chain.ProjectID, *graph)
	if err != nil {
		return nil, err
	}
	report.Blockers = append(report.Blockers, resultBlockers...)
	report.Blockers = dedupeEvidenceBlockers(report.Blockers)
	report.Warnings = dedupeEvidenceWarnings(report.Warnings)
	report.Eligible = len(report.Blockers) == 0
	projection := BuildEvidenceResearchProjection(*chain, *graph)
	report.ResearchHealth = projection.StructuralHealth
	report.ReadabilityStatus = "v2_readable"
	if projection.StructuralHealth.CompatibilityStatus == "legacy_readable" {
		report.ReadabilityStatus = "legacy_readable"
	}
	if report.CurrentGraphHash == "" || report.CurrentGraphHash != report.StoredGraphHash {
		report.ReadabilityStatus = "broken"
	}
	report.V2ComplianceStatus = "v2_compliant"
	if projection.StructuralHealth.CompatibilityStatus == "legacy_readable" {
		report.V2ComplianceStatus = "legacy_mixed"
	}
	for _, blocker := range report.Blockers {
		switch blocker.Code {
		case "DUPLICATE_NODE_ID", "DUPLICATE_EDGE_ID", "MISSING_EDGE_SOURCE", "MISSING_EDGE_TARGET", "INVALID_NODE_TYPE", "INVALID_EDGE_TYPE", "GRAPH_HASH_MISMATCH", "RESULT_DISPOSITION_REQUIRED", "RESULT_DISPOSITION_INVALID", "RESULT_DISPOSITION_EDGE_MISMATCH":
			report.V2ComplianceStatus = "v2_noncompliant"
		}
	}
	for _, node := range graph.Nodes {
		if !evidenceResultNode(node) {
			continue
		}
		disposition := evidenceAuthoringResultDisposition(node)
		if disposition == EvidenceResultDispositionConclusion || disposition == EvidenceResultDispositionMixed {
			report.PublicationResultCount++
		}
	}
	switch {
	case report.PublicationResultCount == 0:
		report.PublicationStatus = "not_applicable"
	case report.Eligible && report.V2ComplianceStatus == "v2_compliant":
		report.PublicationStatus = "publication_ready"
	default:
		report.PublicationStatus = "publication_blocked"
	}
	return report, nil
}

func (s *SQLite) auditEvidenceResults(ctx context.Context, projectID string, graph EvidenceChainGraph) ([]EvidenceGraphBlocker, error) {
	blockers := make([]EvidenceGraphBlocker, 0)
	formalUses := make([]formalRunEvidenceUse, 0)
	for _, node := range graph.Nodes {
		if !evidenceResultNode(node) {
			continue
		}
		if len(node.SourceRunIDs) == 0 && len(node.SourceSnapshotIDs) == 0 {
			blockers = append(blockers, EvidenceGraphBlocker{Code: "RESULT_PROVENANCE_REQUIRED", Message: "result claim requires at least one source_run_id or immutable source_snapshot_id", NodeID: node.ID})
		}
		disposition := evidenceAuthoringResultDisposition(node)
		if disposition == "" {
			blockers = append(blockers, EvidenceGraphBlocker{Code: "RESULT_DISPOSITION_REQUIRED", Message: "result claim must declare resultDisposition as conclusion, issue, mixed, or pending", NodeID: node.ID})
		} else if !validEvidenceResultDisposition(disposition) {
			blockers = append(blockers, EvidenceGraphBlocker{Code: "RESULT_DISPOSITION_INVALID", Message: fmt.Sprintf("resultDisposition %q is invalid", disposition), NodeID: node.ID})
		} else {
			hasConclusion, hasIssue, issueExplained := evidenceResultDispositionOutcomes(graph, node.ID)
			matches := (disposition == EvidenceResultDispositionConclusion && hasConclusion && !hasIssue) ||
				(disposition == EvidenceResultDispositionIssue && hasIssue && !hasConclusion) ||
				(disposition == EvidenceResultDispositionMixed && hasConclusion && hasIssue) ||
				(disposition == EvidenceResultDispositionPending && !hasConclusion && !hasIssue)
			if !matches {
				blockers = append(blockers, EvidenceGraphBlocker{Code: "RESULT_DISPOSITION_EDGE_MISMATCH", Message: fmt.Sprintf("resultDisposition %q does not match the Result's Conclusion/Issue outcome edges", disposition), NodeID: node.ID})
			}
			reason := strings.TrimSpace(evidenceAuthoringDispositionReason(node))
			if disposition == EvidenceResultDispositionPending && reason == "" {
				blockers = append(blockers, EvidenceGraphBlocker{Code: "RESULT_PENDING_REASON_REQUIRED", Message: "pending result disposition requires dispositionReason", NodeID: node.ID})
			}
			if (disposition == EvidenceResultDispositionIssue || disposition == EvidenceResultDispositionMixed) && reason == "" && !issueExplained {
				blockers = append(blockers, EvidenceGraphBlocker{Code: "RESULT_ISSUE_WITHOUT_INCONCLUSIVE_REASON", Message: "a Result routed to an Issue must explain why it cannot form a stable conclusion", NodeID: node.ID})
			}
		}

		formal := disposition == EvidenceResultDispositionConclusion || disposition == EvidenceResultDispositionMixed
		for _, sourceRunID := range normalizeEvidenceProposalIDs(node.SourceRunIDs) {
			run, err := s.GetRun(ctx, sourceRunID)
			if err != nil {
				return nil, err
			}
			if run == nil {
				blockers = append(blockers, EvidenceGraphBlocker{Code: "RUN_NOT_FOUND", Message: fmt.Sprintf("run %q does not exist", sourceRunID), NodeID: node.ID, RunID: sourceRunID})
				continue
			}
			if strings.TrimSpace(run.ProjectID) == "" || run.ProjectID != projectID {
				blockers = append(blockers, EvidenceGraphBlocker{Code: "RUN_PROJECT_MISMATCH", Message: fmt.Sprintf("run %q does not belong to project %q", run.ID, projectID), NodeID: node.ID, RunID: run.ID})
				continue
			}
			if !IsRunTerminalStatus(run.Status) {
				blockers = append(blockers, EvidenceGraphBlocker{Code: "RESULT_RUN_NOT_TERMINAL", Message: fmt.Sprintf("run %q is %q; Result provenance requires a terminal Run", run.ID, run.Status), NodeID: node.ID, RunID: run.ID})
			}
			if formal {
				formalUses = append(formalUses, formalRunEvidenceUse{Node: EvidenceChainNode{ID: node.ID, RunID: run.ID}})
			}
		}
		for _, snapshotID := range normalizeEvidenceProposalIDs(node.SourceSnapshotIDs) {
			snapshot, err := s.GetEvidenceSnapshot(ctx, snapshotID)
			if err != nil {
				return nil, err
			}
			if snapshot == nil {
				blockers = append(blockers, EvidenceGraphBlocker{Code: "SNAPSHOT_NOT_FOUND", Message: fmt.Sprintf("snapshot %q does not exist", snapshotID), NodeID: node.ID, SnapshotID: snapshotID})
				continue
			}
			if snapshot.ProjectID != projectID {
				blockers = append(blockers, EvidenceGraphBlocker{Code: "SNAPSHOT_PROJECT_MISMATCH", Message: fmt.Sprintf("snapshot %q does not belong to project %q", snapshotID, projectID), NodeID: node.ID, RunID: snapshot.RunID, SnapshotID: snapshotID})
				continue
			}
			run, err := s.GetRun(ctx, snapshot.RunID)
			if err != nil {
				return nil, err
			}
			if run == nil {
				blockers = append(blockers, EvidenceGraphBlocker{Code: "SNAPSHOT_RUN_NOT_FOUND", Message: fmt.Sprintf("snapshot %q references missing run %q", snapshotID, snapshot.RunID), NodeID: node.ID, RunID: snapshot.RunID, SnapshotID: snapshotID})
				continue
			}
			if strings.TrimSpace(run.ProjectID) == "" || run.ProjectID != projectID {
				blockers = append(blockers, EvidenceGraphBlocker{Code: "SNAPSHOT_PROJECT_MISMATCH", Message: fmt.Sprintf("snapshot %q run %q does not belong to project %q", snapshotID, run.ID, projectID), NodeID: node.ID, RunID: run.ID, SnapshotID: snapshotID})
				continue
			}
			if !IsRunTerminalStatus(run.Status) {
				blockers = append(blockers, EvidenceGraphBlocker{Code: "RESULT_RUN_NOT_TERMINAL", Message: fmt.Sprintf("snapshot %q references non-terminal run %q", snapshotID, run.ID), NodeID: node.ID, RunID: run.ID, SnapshotID: snapshotID})
			}
			if formal {
				formalUses = append(formalUses, formalRunEvidenceUse{Node: EvidenceChainNode{ID: node.ID, RunID: run.ID}})
				released, err := s.snapshotHasReleasedEvidence(ctx, projectID, snapshotID)
				if err != nil {
					return nil, err
				}
				if !released {
					blockers = append(blockers, EvidenceGraphBlocker{Code: "EVIDENCE_RELEASE_MISSING", Message: fmt.Sprintf("snapshot %q has no released EvidenceRelease in project %q", snapshotID, projectID), NodeID: node.ID, RunID: run.ID, SnapshotID: snapshotID})
				}
			}
		}
	}
	readiness, err := s.formalRunEvidenceBlockers(ctx, projectID, formalUses)
	if err != nil {
		return nil, err
	}
	return append(blockers, readiness...), nil
}

func (s *SQLite) snapshotHasReleasedEvidence(ctx context.Context, projectID, snapshotID string) (bool, error) {
	releases, err := s.ListEvidenceReleases(ctx, snapshotID)
	if err != nil {
		return false, err
	}
	for _, release := range releases {
		if release.ProjectID == projectID && release.State == EvidenceReleaseReleased {
			return true, nil
		}
	}
	return false, nil
}

func auditEvidenceGraphShape(graph *EvidenceChainGraph) []EvidenceGraphBlocker {
	blockers := make([]EvidenceGraphBlocker, 0)
	nodes := make(map[string]EvidenceChainNode, len(graph.Nodes))
	runNodes := make(map[string]string)
	for _, node := range graph.Nodes {
		if previous, ok := nodes[node.ID]; ok {
			blockers = append(blockers, EvidenceGraphBlocker{Code: "DUPLICATE_NODE_ID", Message: fmt.Sprintf("nodes %q and %q share an id", previous.ID, node.ID), NodeID: node.ID})
		}
		nodes[node.ID] = node
		if !ValidEvidenceNodeType(node.Type) {
			blockers = append(blockers, EvidenceGraphBlocker{Code: "INVALID_NODE_TYPE", Message: fmt.Sprintf("invalid node type %q", node.Type), NodeID: node.ID})
		}
		if node.Type == EvidenceNodeRun {
			if previous, ok := runNodes[node.RunID]; ok {
				blockers = append(blockers, EvidenceGraphBlocker{Code: "DUPLICATE_RUN_NODE", Message: fmt.Sprintf("run %q is represented by nodes %q and %q", node.RunID, previous, node.ID), NodeID: node.ID, RunID: node.RunID})
			}
			runNodes[node.RunID] = node.ID
		}
	}
	edgeIDs := make(map[string]bool)
	semantic := make(map[string]string)
	polarity := make(map[string]EvidenceChainEdge)
	adjacency := make(map[string][]string)
	indegree := make(map[string]int, len(nodes))
	for id := range nodes {
		indegree[id] = 0
	}
	for _, edge := range graph.Edges {
		if edgeIDs[edge.ID] {
			blockers = append(blockers, EvidenceGraphBlocker{Code: "DUPLICATE_EDGE_ID", Message: fmt.Sprintf("duplicate edge id %q", edge.ID), EdgeID: edge.ID})
		}
		edgeIDs[edge.ID] = true
		source, sourceOK := nodes[edge.SourceNodeID]
		target, targetOK := nodes[edge.TargetNodeID]
		if !sourceOK || !targetOK {
			code := "MISSING_EDGE_SOURCE"
			if sourceOK {
				code = "MISSING_EDGE_TARGET"
			}
			blockers = append(blockers, EvidenceGraphBlocker{Code: code, Message: fmt.Sprintf("edge %q has a missing endpoint", edge.ID), EdgeID: edge.ID})
			continue
		}
		if !ValidEvidenceEdgeType(edge.Type) {
			blockers = append(blockers, EvidenceGraphBlocker{Code: "INVALID_EDGE_TYPE", Message: fmt.Sprintf("invalid edge type %q", edge.Type), EdgeID: edge.ID})
			continue
		}
		if evidenceResultOutcomeBypass(nodes, edge) {
			blockers = append(blockers, EvidenceGraphBlocker{
				Code:    "LEGACY_RESULT_BYPASS",
				Message: fmt.Sprintf("accepted edge %q bypasses Result interpretation and connects directly to %s %q", edge.ID, target.Type, target.ID),
				NodeID:  source.ID,
				EdgeID:  edge.ID,
			})
		}
		if !isSemanticEvidenceEdge(edge.Type) {
			continue
		}
		if edge.SourceNodeID == edge.TargetNodeID {
			blockers = append(blockers, EvidenceGraphBlocker{Code: "SEMANTIC_SELF_LOOP", Message: fmt.Sprintf("edge %q is a semantic self-loop", edge.ID), EdgeID: edge.ID})
			continue
		}
		key := edge.SourceNodeID + "\x00" + edge.TargetNodeID + "\x00" + edge.Type
		if previous, ok := semantic[key]; ok {
			blockers = append(blockers, EvidenceGraphBlocker{Code: "DUPLICATE_SEMANTIC_EDGE", Message: fmt.Sprintf("edges %q and %q duplicate the same semantic relation", previous, edge.ID), EdgeID: edge.ID})
		}
		semantic[key] = edge.ID
		if edge.Type == EvidenceEdgeSupports || edge.Type == EvidenceEdgeWeakens || edge.Type == EvidenceEdgeDoesNotProve {
			polarityKey := edge.SourceNodeID + "\x00" + edge.TargetNodeID
			if previous, ok := polarity[polarityKey]; ok && previous.Type != edge.Type {
				blockers = append(blockers, EvidenceGraphBlocker{Code: "CONTRADICTORY_EVIDENCE_EDGE", Message: fmt.Sprintf("edges %q and %q assign conflicting polarity", previous.ID, edge.ID), EdgeID: edge.ID})
			}
			polarity[polarityKey] = edge
		}
		if !allowedEvidenceDirectionForNodes(source, target, edge.Type) {
			blockers = append(blockers, EvidenceGraphBlocker{Code: "INVALID_EDGE_DIRECTION", Message: fmt.Sprintf("edge %q type %q does not allow %s -> %s", edge.ID, edge.Type, source.Type, target.Type), EdgeID: edge.ID})
		}
		adjacency[edge.SourceNodeID] = append(adjacency[edge.SourceNodeID], edge.TargetNodeID)
		indegree[edge.TargetNodeID]++
	}
	queue := make([]string, 0)
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, target := range adjacency[id] {
			indegree[target]--
			if indegree[target] == 0 {
				queue = append(queue, target)
			}
		}
	}
	if visited != len(nodes) {
		blockers = append(blockers, EvidenceGraphBlocker{Code: "GRAPH_CYCLE", Message: "semantic evidence edges must form a directed acyclic graph"})
	}
	return blockers
}

func dedupeEvidenceBlockers(values []EvidenceGraphBlocker) []EvidenceGraphBlocker {
	seen := make(map[string]bool, len(values))
	out := make([]EvidenceGraphBlocker, 0, len(values))
	for _, blocker := range values {
		key := blocker.Code + "\x00" + blocker.NodeID + "\x00" + blocker.EdgeID + "\x00" + blocker.RunID + "\x00" + blocker.SnapshotID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, blocker)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := out[i].Code + out[i].NodeID + out[i].EdgeID + out[i].RunID + out[i].SnapshotID
		right := out[j].Code + out[j].NodeID + out[j].EdgeID + out[j].RunID + out[j].SnapshotID
		return left < right
	})
	return out
}

func dedupeEvidenceWarnings(values []EvidenceGraphWarning) []EvidenceGraphWarning {
	seen := make(map[string]bool, len(values))
	out := make([]EvidenceGraphWarning, 0, len(values))
	for _, warning := range values {
		key := warning.Code + "\x00" + warning.NodeID + "\x00" + warning.EdgeID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, warning)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := out[i].Code + out[i].NodeID + out[i].EdgeID
		right := out[j].Code + out[j].NodeID + out[j].EdgeID
		return left < right
	})
	return out
}
