package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (s *SQLite) ValidateEvidenceMapReferences(ctx context.Context, owner *EvidenceChain, graph *EvidenceChainGraph) error {
	if owner == nil || graph == nil {
		return graphValidationError("INVALID_GRAPH", "owner Map and graph are required")
	}
	for _, node := range graph.Nodes {
		if node.Type != EvidenceNodeMapRef {
			continue
		}
		if owner.Role != "primary" {
			return graphValidationError("MAP_REFERENCE_SCOPE_INVALID", fmt.Sprintf("Map Reference node %q is only allowed in a Primary Map", node.ID))
		}
		var reference EvidenceMapReference
		if err := json.Unmarshal([]byte(strings.TrimSpace(node.DataJSON)), &reference); err != nil {
			return graphValidationError("MAP_REFERENCE_INVALID", fmt.Sprintf("Map Reference node %q has invalid data: %v", node.ID, err))
		}
		reference.TargetMapID = strings.TrimSpace(reference.TargetMapID)
		reference.TargetGraphHash = strings.TrimSpace(reference.TargetGraphHash)
		if reference.TargetMapID == "" || reference.TargetRevision <= 0 || reference.TargetGraphHash == "" {
			return graphValidationError("MAP_REFERENCE_INCOMPLETE", fmt.Sprintf("Map Reference node %q requires target_map_id, target_revision, and target_graph_hash", node.ID))
		}
		if reference.TargetMapID == owner.ID {
			return graphValidationError("MAP_REFERENCE_CYCLE", fmt.Sprintf("Primary Map %q cannot reference itself", owner.ID))
		}
		target, err := s.GetEvidenceChain(ctx, reference.TargetMapID)
		if err != nil {
			return err
		}
		if target == nil {
			return graphValidationError("MAP_REFERENCE_NOT_FOUND", fmt.Sprintf("target Map %q does not exist", reference.TargetMapID))
		}
		if strings.TrimSpace(target.ProjectID) == "" || target.ProjectID != owner.ProjectID {
			return graphValidationError("MAP_REFERENCE_PROJECT_MISMATCH", fmt.Sprintf("target Map %q does not belong to Project %q", target.ID, owner.ProjectID))
		}
		if target.Role == "primary" {
			return graphValidationError("MAP_REFERENCE_CYCLE", fmt.Sprintf("Primary Map cannot reference Primary Map %q", target.ID))
		}
		revision, err := s.GetEvidenceChainRevision(ctx, target.ID, reference.TargetRevision)
		if err != nil {
			return err
		}
		if revision == nil {
			return graphValidationError("MAP_REFERENCE_REVISION_NOT_FOUND", fmt.Sprintf("target Map %q revision %d does not exist", target.ID, reference.TargetRevision))
		}
		if revision.GraphHash != reference.TargetGraphHash {
			return graphValidationError("MAP_REFERENCE_HASH_MISMATCH", fmt.Sprintf("target Map %q revision %d hash does not match", target.ID, reference.TargetRevision))
		}
		if len(reference.TargetNodeIDs) > 0 {
			var snapshot struct {
				Nodes []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			}
			if err := json.Unmarshal([]byte(revision.GraphJSON), &snapshot); err != nil {
				return graphValidationError("MAP_REFERENCE_REVISION_INVALID", fmt.Sprintf("target Map %q revision %d cannot be decoded", target.ID, reference.TargetRevision))
			}
			nodeIDs := make(map[string]bool, len(snapshot.Nodes))
			for _, targetNode := range snapshot.Nodes {
				nodeIDs[targetNode.ID] = true
			}
			for _, targetNodeID := range reference.TargetNodeIDs {
				if !nodeIDs[strings.TrimSpace(targetNodeID)] {
					return graphValidationError("MAP_REFERENCE_NODE_NOT_FOUND", fmt.Sprintf("target node %q is absent from Map %q revision %d", targetNodeID, target.ID, reference.TargetRevision))
				}
			}
		}
	}
	return nil
}
