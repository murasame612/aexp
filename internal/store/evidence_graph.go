package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// EvidenceGraphValidationError is safe to expose through API/CLI boundaries.
type EvidenceGraphValidationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *EvidenceGraphValidationError) Error() string {
	return e.Message
}

// EvidenceGraphRevisionConflict means a writer used a stale graph revision.
type EvidenceGraphRevisionConflict struct {
	Expected    int64  `json:"expected_revision"`
	Current     int64  `json:"current_revision"`
	CurrentHash string `json:"current_graph_hash"`
}

func (e *EvidenceGraphRevisionConflict) Error() string {
	return fmt.Sprintf("evidence graph revision conflict: expected %d, current %d", e.Expected, e.Current)
}

type canonicalEvidenceNode struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	Title         string          `json:"title,omitempty"`
	Body          string          `json:"body,omitempty"`
	RunID         string          `json:"run_id,omitempty"`
	ProjectCardID string          `json:"project_card_id,omitempty"`
	OccurredAt    string          `json:"occurred_at,omitempty"`
	Data          json.RawMessage `json:"data"`
}

type canonicalEvidenceEdge struct {
	ID           string          `json:"id"`
	SourceNodeID string          `json:"source_node_id"`
	TargetNodeID string          `json:"target_node_id"`
	Type         string          `json:"type"`
	Label        string          `json:"label,omitempty"`
	Rationale    string          `json:"rationale,omitempty"`
	Data         json.RawMessage `json:"data"`
}

type canonicalEvidenceGraph struct {
	Nodes []canonicalEvidenceNode `json:"nodes"`
	Edges []canonicalEvidenceEdge `json:"edges"`
}

// CanonicalEvidenceGraph returns a stable semantic snapshot and its SHA-256.
// Coordinates, dimensions, and pin state are deliberately excluded.
func CanonicalEvidenceGraph(graph EvidenceChainGraph) ([]byte, string, error) {
	canonical := canonicalEvidenceGraph{
		Nodes: make([]canonicalEvidenceNode, 0, len(graph.Nodes)),
		Edges: make([]canonicalEvidenceEdge, 0, len(graph.Edges)),
	}
	for _, node := range graph.Nodes {
		data, err := canonicalEvidenceData(node.DataJSON)
		if err != nil {
			return nil, "", &EvidenceGraphValidationError{
				Code:    "INVALID_NODE_DATA",
				Message: fmt.Sprintf("node %q data_json is invalid: %v", node.ID, err),
			}
		}
		occurredAt := ""
		if node.OccurredAt != nil {
			occurredAt = node.OccurredAt.UTC().Format("2006-01-02T15:04:05.000000000Z")
		}
		canonical.Nodes = append(canonical.Nodes, canonicalEvidenceNode{
			ID:            strings.TrimSpace(node.ID),
			Type:          strings.TrimSpace(node.Type),
			Title:         node.Title,
			Body:          node.Body,
			RunID:         strings.TrimSpace(node.RunID),
			ProjectCardID: strings.TrimSpace(node.ProjectCardID),
			OccurredAt:    occurredAt,
			Data:          data,
		})
	}
	for _, edge := range graph.Edges {
		data, err := canonicalEvidenceData(edge.DataJSON)
		if err != nil {
			return nil, "", &EvidenceGraphValidationError{
				Code:    "INVALID_EDGE_DATA",
				Message: fmt.Sprintf("edge %q data_json is invalid: %v", edge.ID, err),
			}
		}
		canonical.Edges = append(canonical.Edges, canonicalEvidenceEdge{
			ID:           strings.TrimSpace(edge.ID),
			SourceNodeID: strings.TrimSpace(edge.SourceNodeID),
			TargetNodeID: strings.TrimSpace(edge.TargetNodeID),
			Type:         strings.TrimSpace(edge.Type),
			Label:        edge.Label,
			Rationale:    edge.Rationale,
			Data:         data,
		})
	}
	sort.Slice(canonical.Nodes, func(i, j int) bool { return canonical.Nodes[i].ID < canonical.Nodes[j].ID })
	sort.Slice(canonical.Edges, func(i, j int) bool { return canonical.Edges[i].ID < canonical.Edges[j].ID })
	payload, err := json.Marshal(canonical)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(payload)
	return payload, hex.EncodeToString(sum[:]), nil
}

func canonicalEvidenceData(raw string) (json.RawMessage, error) {
	if strings.TrimSpace(raw) == "" {
		return json.RawMessage(`{}`), nil
	}
	var value interface{}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, err
	}
	if object, ok := value.(map[string]interface{}); ok {
		delete(object, "pinned")
		delete(object, "position")
		delete(object, "width")
		delete(object, "height")
		delete(object, "layout")
		delete(object, "sourceHandle")
		delete(object, "targetHandle")
		delete(object, "autoHandles")
		delete(object, "collapsed")
	}
	payload, err := json.Marshal(value)
	return json.RawMessage(payload), err
}

// ValidateEvidenceChainGraph validates graph structure and V1 research semantics.
// Database-backed reference checks are performed by SQLite before persistence.
func ValidateEvidenceChainGraph(graph *EvidenceChainGraph) error {
	return validateEvidenceChainGraph(graph, true)
}

// validateEvidenceChainGraph keeps historical secondary Evidence Chain boards
// editable while primary Research Graphs use the strict DAG contract.
func validateEvidenceChainGraph(graph *EvidenceChainGraph, strictResearchGraph bool) error {
	if graph == nil {
		return graphValidationError("INVALID_GRAPH", "graph is required")
	}
	nodes := make(map[string]EvidenceChainNode, len(graph.Nodes))
	runNodes := make(map[string]string)
	groupMembership := make(map[string]string)
	for i := range graph.Nodes {
		node := &graph.Nodes[i]
		node.ID = strings.TrimSpace(node.ID)
		node.Type = strings.TrimSpace(node.Type)
		node.RunID = strings.TrimSpace(node.RunID)
		node.ProjectCardID = strings.TrimSpace(node.ProjectCardID)
		if strings.TrimSpace(node.DataJSON) == "" {
			node.DataJSON = "{}"
		}
		if node.ID == "" {
			return graphValidationError("NODE_ID_REQUIRED", "node id is required")
		}
		if _, exists := nodes[node.ID]; exists {
			return graphValidationError("DUPLICATE_NODE_ID", fmt.Sprintf("duplicate node id %q", node.ID))
		}
		if !ValidEvidenceNodeType(node.Type) {
			return graphValidationError("INVALID_NODE_TYPE", fmt.Sprintf("invalid node type %q", node.Type))
		}
		if node.Type == EvidenceNodeRun {
			if node.RunID == "" {
				return graphValidationError("RUN_ID_REQUIRED", fmt.Sprintf("run node %q requires run_id", node.ID))
			}
			if existing, exists := runNodes[node.RunID]; strictResearchGraph && exists {
				return graphValidationError("DUPLICATE_RUN_NODE", fmt.Sprintf("run %q is already represented by node %q", node.RunID, existing))
			}
			runNodes[node.RunID] = node.ID
		}
		if node.Type == EvidenceNodeGroup && node.RunID != "" {
			return graphValidationError("GROUP_RUN_ID_FORBIDDEN", fmt.Sprintf("group node %q cannot reference run_id", node.ID))
		}
		if node.Type == EvidenceNodeGroup {
			groupKind, err := evidenceNodeGroupKind(node.DataJSON)
			if err != nil {
				return graphValidationError("INVALID_GROUP_KIND", fmt.Sprintf("group node %q groupKind is invalid: %v", node.ID, err))
			}
			if groupKind != "protocol" {
				return graphValidationError("INVALID_GROUP_KIND", fmt.Sprintf("group node %q requires groupKind %q", node.ID, "protocol"))
			}
		}
		if node.Width <= 0 {
			node.Width = 260
		}
		if node.Height <= 0 {
			node.Height = 140
		}
		if _, _, err := CanonicalEvidenceGraph(EvidenceChainGraph{Nodes: []EvidenceChainNode{*node}}); err != nil {
			return err
		}
		groupID, present, err := evidenceNodeGroupID(node.DataJSON)
		if err != nil {
			return graphValidationError("INVALID_GROUP_ID", fmt.Sprintf("node %q groupId is invalid: %v", node.ID, err))
		}
		if present {
			groupMembership[node.ID] = groupID
		}
		nodes[node.ID] = *node
	}
	for nodeID, groupID := range groupMembership {
		node := nodes[nodeID]
		if node.Type == EvidenceNodeGroup {
			return graphValidationError("GROUP_NESTING_NOT_SUPPORTED", fmt.Sprintf("group node %q cannot belong to another group", nodeID))
		}
		group, exists := nodes[groupID]
		if !exists {
			return graphValidationError("GROUP_NOT_FOUND", fmt.Sprintf("node %q references missing group %q", nodeID, groupID))
		}
		if group.Type != EvidenceNodeGroup {
			return graphValidationError("GROUP_TARGET_INVALID", fmt.Sprintf("node %q groupId %q points to node type %q", nodeID, groupID, group.Type))
		}
		groupKind, err := evidenceNodeGroupKind(group.DataJSON)
		if err != nil {
			return graphValidationError("INVALID_GROUP_KIND", fmt.Sprintf("group node %q groupKind is invalid: %v", groupID, err))
		}
		if groupKind == "protocol" && !validProtocolGroupMemberType(node.Type) {
			return graphValidationError(
				"GROUP_MEMBER_TYPE_NOT_ALLOWED",
				fmt.Sprintf("node %q type %q cannot belong to protocol group %q", nodeID, node.Type, groupID),
			)
		}
	}

	edgeIDs := make(map[string]bool, len(graph.Edges))
	semanticEdges := make(map[string]bool)
	polarityEdges := make(map[string]EvidenceChainEdge)
	adjacency := make(map[string][]string)
	indegree := make(map[string]int, len(nodes))
	for id := range nodes {
		indegree[id] = 0
	}
	for i := range graph.Edges {
		edge := &graph.Edges[i]
		edge.ID = strings.TrimSpace(edge.ID)
		edge.SourceNodeID = strings.TrimSpace(edge.SourceNodeID)
		edge.TargetNodeID = strings.TrimSpace(edge.TargetNodeID)
		edge.Type = strings.TrimSpace(edge.Type)
		if strings.TrimSpace(edge.DataJSON) == "" {
			edge.DataJSON = "{}"
		}
		if edge.ID == "" {
			return graphValidationError("EDGE_ID_REQUIRED", "edge id is required")
		}
		if edgeIDs[edge.ID] {
			return graphValidationError("DUPLICATE_EDGE_ID", fmt.Sprintf("duplicate edge id %q", edge.ID))
		}
		if !ValidEvidenceEdgeType(edge.Type) {
			return graphValidationError("INVALID_EDGE_TYPE", fmt.Sprintf("invalid edge type %q", edge.Type))
		}
		source, sourceOK := nodes[edge.SourceNodeID]
		target, targetOK := nodes[edge.TargetNodeID]
		if !sourceOK {
			return graphValidationError("MISSING_EDGE_SOURCE", fmt.Sprintf("edge %q source node %q does not exist", edge.ID, edge.SourceNodeID))
		}
		if !targetOK {
			return graphValidationError("MISSING_EDGE_TARGET", fmt.Sprintf("edge %q target node %q does not exist", edge.ID, edge.TargetNodeID))
		}
		if source.Type == EvidenceNodeGroup || target.Type == EvidenceNodeGroup {
			return graphValidationError("GROUP_EDGE_NOT_ALLOWED", fmt.Sprintf("edge %q cannot connect directly to a group", edge.ID))
		}
		if strictResearchGraph && isSemanticEvidenceEdge(edge.Type) {
			if edge.SourceNodeID == edge.TargetNodeID {
				return graphValidationError("SEMANTIC_SELF_LOOP", fmt.Sprintf("semantic edge %q cannot reference the same node", edge.ID))
			}
			key := edge.SourceNodeID + "\x00" + edge.TargetNodeID + "\x00" + edge.Type
			if semanticEdges[key] {
				return graphValidationError("DUPLICATE_SEMANTIC_EDGE", fmt.Sprintf("duplicate semantic edge %s --%s--> %s", edge.SourceNodeID, edge.Type, edge.TargetNodeID))
			}
			semanticEdges[key] = true
			if edge.Type == EvidenceEdgeSupports || edge.Type == EvidenceEdgeWeakens || edge.Type == EvidenceEdgeDoesNotProve {
				polarityKey := edge.SourceNodeID + "\x00" + edge.TargetNodeID
				if previous, exists := polarityEdges[polarityKey]; exists && previous.Type != edge.Type {
					return graphValidationError(
						"CONTRADICTORY_EVIDENCE_EDGE",
						fmt.Sprintf("edges %q and %q assign conflicting polarity (%s vs %s) to %s -> %s", previous.ID, edge.ID, previous.Type, edge.Type, edge.SourceNodeID, edge.TargetNodeID),
					)
				}
				polarityEdges[polarityKey] = *edge
			}
			if !allowedEvidenceDirection(source.Type, target.Type, edge.Type) {
				return graphValidationError("INVALID_EDGE_DIRECTION", fmt.Sprintf("edge type %q does not allow %s -> %s", edge.Type, source.Type, target.Type))
			}
			adjacency[edge.SourceNodeID] = append(adjacency[edge.SourceNodeID], edge.TargetNodeID)
			indegree[edge.TargetNodeID]++
		}
		if _, _, err := CanonicalEvidenceGraph(EvidenceChainGraph{Edges: []EvidenceChainEdge{*edge}}); err != nil {
			return err
		}
		edgeIDs[edge.ID] = true
	}
	queue := make([]string, 0, len(nodes))
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		sort.Strings(adjacency[id])
		for _, target := range adjacency[id] {
			indegree[target]--
			if indegree[target] == 0 {
				queue = append(queue, target)
				sort.Strings(queue)
			}
		}
	}
	if strictResearchGraph && visited != len(nodes) {
		return graphValidationError("GRAPH_CYCLE", "semantic evidence edges must form a directed acyclic graph")
	}
	return nil
}

func graphValidationError(code, message string) error {
	return &EvidenceGraphValidationError{Code: code, Message: message}
}

func ValidEvidenceNodeType(t string) bool {
	switch t {
	case EvidenceNodeDataset, EvidenceNodeProtocol, EvidenceNodeRun, EvidenceNodeClaim, EvidenceNodeIssue, EvidenceNodePlan,
		EvidenceNodeHypothesis, EvidenceNodeExperiment, EvidenceNodeConclusion, EvidenceNodeNote, EvidenceNodeMapRef, EvidenceNodeGroup:
		return true
	default:
		return false
	}
}

func evidenceNodeGroupID(raw string) (string, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return "", false, nil
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return "", false, err
	}
	value, exists := data["groupId"]
	if !exists {
		return "", false, nil
	}
	var groupID string
	if err := json.Unmarshal(value, &groupID); err != nil {
		return "", true, errors.New("must be a string")
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return "", true, errors.New("must not be blank")
	}
	return groupID, true, nil
}

func evidenceNodeGroupKind(raw string) (string, error) {
	var data map[string]json.RawMessage
	if err := json.Unmarshal([]byte(normalizeEvidenceDataJSON(raw)), &data); err != nil {
		return "", err
	}
	value, exists := data["groupKind"]
	if !exists {
		return "", errors.New("is required")
	}
	var groupKind string
	if err := json.Unmarshal(value, &groupKind); err != nil {
		return "", errors.New("must be a string")
	}
	return strings.TrimSpace(groupKind), nil
}

func validProtocolGroupMemberType(nodeType string) bool {
	switch nodeType {
	case EvidenceNodeDataset, EvidenceNodeRun, EvidenceNodePlan, EvidenceNodeExperiment:
		return true
	default:
		return false
	}
}

func ValidEvidenceEdgeType(t string) bool {
	switch t {
	case EvidenceEdgeUses, EvidenceEdgeSupports, EvidenceEdgeWeakens, EvidenceEdgeRevealsIssue,
		EvidenceEdgeSupersedes, EvidenceEdgeNextStep, EvidenceEdgeRelatedTo,
		EvidenceEdgeDoesNotProve, EvidenceEdgeCustom:
		return true
	default:
		return false
	}
}

func isSemanticEvidenceEdge(edgeType string) bool {
	return edgeType != EvidenceEdgeCustom && edgeType != EvidenceEdgeRelatedTo
}

func isLegacyEvidenceNode(nodeType string) bool {
	switch nodeType {
	case EvidenceNodeHypothesis, EvidenceNodeExperiment, EvidenceNodeConclusion, EvidenceNodeNote:
		return true
	default:
		return false
	}
}

func allowedEvidenceDirection(sourceType, targetType, edgeType string) bool {
	// Historical boards remain editable without pretending their loose semantics
	// satisfy V1 evidence rules.
	if isLegacyEvidenceNode(sourceType) || isLegacyEvidenceNode(targetType) {
		switch edgeType {
		case EvidenceEdgeSupports, EvidenceEdgeDoesNotProve, EvidenceEdgeNextStep:
			return true
		default:
			return false
		}
	}
	switch edgeType {
	case EvidenceEdgeUses:
		return (sourceType == EvidenceNodeDataset || sourceType == EvidenceNodeProtocol) && targetType == EvidenceNodeRun
	case EvidenceEdgeSupports, EvidenceEdgeWeakens, EvidenceEdgeDoesNotProve:
		return sourceType == EvidenceNodeRun && targetType == EvidenceNodeClaim
	case EvidenceEdgeRevealsIssue:
		return (sourceType == EvidenceNodeRun || sourceType == EvidenceNodeClaim) && targetType == EvidenceNodeIssue
	case EvidenceEdgeSupersedes:
		return sourceType == targetType &&
			(sourceType == EvidenceNodeDataset || sourceType == EvidenceNodeProtocol || sourceType == EvidenceNodeClaim || sourceType == EvidenceNodePlan)
	case EvidenceEdgeNextStep:
		return (sourceType == EvidenceNodeClaim || sourceType == EvidenceNodeIssue) && targetType == EvidenceNodePlan
	default:
		return false
	}
}
