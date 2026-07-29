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
	ChainID          string                 `json:"chain_id"`
	ProjectID        string                 `json:"project_id"`
	Role             string                 `json:"role"`
	Revision         int64                  `json:"revision"`
	StoredGraphHash  string                 `json:"stored_graph_hash"`
	CurrentGraphHash string                 `json:"current_graph_hash"`
	Eligible         bool                   `json:"eligible"`
	Blockers         []EvidenceGraphBlocker `json:"blockers"`
}

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
		ChainID: chain.ID, ProjectID: chain.ProjectID, Role: chain.Role,
		Revision: chain.Revision, StoredGraphHash: chain.GraphHash,
		Blockers: make([]EvidenceGraphBlocker, 0),
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
		run, getErr := s.GetRun(ctx, node.RunID)
		if getErr != nil {
			return nil, getErr
		}
		if run == nil {
			report.Blockers = append(report.Blockers, EvidenceGraphBlocker{Code: "RUN_NOT_FOUND", Message: fmt.Sprintf("run %q does not exist", node.RunID), NodeID: node.ID, RunID: node.RunID})
			continue
		}
		if strings.TrimSpace(run.ProjectID) == "" || run.ProjectID != chain.ProjectID {
			report.Blockers = append(report.Blockers, EvidenceGraphBlocker{Code: "RUN_PROJECT_MISMATCH", Message: fmt.Sprintf("run %q does not belong to project %q", run.ID, chain.ProjectID), NodeID: node.ID, RunID: run.ID})
		}
		if node.ProjectCardID != "" {
			card, cardErr := s.GetProjectRunCard(ctx, run.ID)
			if cardErr != nil {
				return nil, cardErr
			}
			if card == nil || card.ID != node.ProjectCardID || card.ProjectID != chain.ProjectID {
				report.Blockers = append(report.Blockers, EvidenceGraphBlocker{Code: "PROJECT_CARD_MISMATCH", Message: fmt.Sprintf("project card %q does not match run %q and project %q", node.ProjectCardID, run.ID, chain.ProjectID), NodeID: node.ID, RunID: run.ID})
			}
		}
	}
	if refErr := s.ValidateEvidenceMapReferences(ctx, chain, graph); refErr != nil {
		report.Blockers = append(report.Blockers, blockerFromError(refErr))
	}
	uses := make([]formalRunEvidenceUse, 0)
	for _, edge := range graph.Edges {
		if edge.Type != EvidenceEdgeSupports && edge.Type != EvidenceEdgeWeakens && edge.Type != EvidenceEdgeDoesNotProve {
			continue
		}
		source := nodes[edge.SourceNodeID]
		if source.Type == EvidenceNodeRun {
			uses = append(uses, formalRunEvidenceUse{Node: source, EdgeID: edge.ID})
		}
	}
	readiness, err := s.formalRunEvidenceBlockers(ctx, chain.ProjectID, uses)
	if err != nil {
		return nil, err
	}
	report.Blockers = append(report.Blockers, readiness...)
	report.Blockers = dedupeEvidenceBlockers(report.Blockers)
	report.Eligible = len(report.Blockers) == 0
	return report, nil
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
		if !allowedEvidenceDirection(source.Type, target.Type, edge.Type) {
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
		key := blocker.Code + "\x00" + blocker.NodeID + "\x00" + blocker.EdgeID + "\x00" + blocker.RunID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, blocker)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := out[i].Code + out[i].NodeID + out[i].EdgeID + out[i].RunID
		right := out[j].Code + out[j].NodeID + out[j].EdgeID + out[j].RunID
		return left < right
	})
	return out
}
