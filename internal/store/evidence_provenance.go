package store

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	evidenceSourceRunIDsKey      = "sourceRunIds"
	evidenceSourceSnapshotIDsKey = "sourceSnapshotIds"
)

// normalizeEvidenceNodeProvenance keeps typed node fields and data_json in
// sync without requiring a database migration. data_json is the durable
// compatibility representation; the typed fields make the API and MCP shape
// explicit and usable.
func normalizeEvidenceNodeProvenance(node EvidenceChainNode) (EvidenceChainNode, error) {
	data := make(map[string]any)
	if err := json.Unmarshal([]byte(normalizeEvidenceDataJSON(node.DataJSON)), &data); err != nil {
		return node, fmt.Errorf("invalid data_json: %w", err)
	}

	runValues, runSpecified := evidenceProvenanceValues(data, evidenceSourceRunIDsKey, "source_run_ids")
	snapshotValues, snapshotSpecified := evidenceProvenanceValues(data, evidenceSourceSnapshotIDsKey, "source_snapshot_ids")
	if node.SourceRunIDs != nil {
		runSpecified = true
		runValues = append(runValues, node.SourceRunIDs...)
	}
	if node.SourceSnapshotIDs != nil {
		snapshotSpecified = true
		snapshotValues = append(snapshotValues, node.SourceSnapshotIDs...)
	}

	delete(data, "source_run_ids")
	delete(data, "source_snapshot_ids")
	if runSpecified {
		node.SourceRunIDs = normalizeEvidenceProposalIDs(runValues)
		data[evidenceSourceRunIDsKey] = node.SourceRunIDs
	} else {
		node.SourceRunIDs = nil
		delete(data, evidenceSourceRunIDsKey)
	}
	if snapshotSpecified {
		node.SourceSnapshotIDs = normalizeEvidenceProposalIDs(snapshotValues)
		data[evidenceSourceSnapshotIDsKey] = node.SourceSnapshotIDs
	} else {
		node.SourceSnapshotIDs = nil
		delete(data, evidenceSourceSnapshotIDsKey)
	}

	payload, err := json.Marshal(data)
	if err != nil {
		return node, err
	}
	node.DataJSON = string(payload)
	return node, nil
}

func evidenceProvenanceValues(data map[string]any, keys ...string) ([]string, bool) {
	values := make([]string, 0)
	specified := false
	for _, key := range keys {
		raw, exists := data[key]
		if !exists {
			continue
		}
		specified = true
		switch typed := raw.(type) {
		case []any:
			for _, item := range typed {
				if value, ok := item.(string); ok {
					values = append(values, value)
				}
			}
		case []string:
			values = append(values, typed...)
		}
	}
	return values, specified
}

func evidenceResultNode(node EvidenceChainNode) bool {
	return node.Type == EvidenceNodeClaim && strings.EqualFold(evidenceAuthoringClaimKind(node), "result")
}

func prepareEvidenceProposalProvenance(proposal *EvidenceProposal, patch *EvidenceGraphPatch) error {
	if proposal == nil || patch == nil {
		return nil
	}
	type resultRef struct {
		nodes *[]EvidenceChainNode
		index int
	}
	results := make([]resultRef, 0)
	for _, nodes := range []*[]EvidenceChainNode{&patch.Nodes, &patch.UpsertNodes} {
		for i := range *nodes {
			normalized, err := normalizeEvidenceNodeProvenance((*nodes)[i])
			if err != nil {
				return graphValidationError("INVALID_NODE_DATA", fmt.Sprintf("node %q data_json is invalid: %v", (*nodes)[i].ID, err))
			}
			(*nodes)[i] = normalized
			if evidenceResultNode(normalized) {
				results = append(results, resultRef{nodes: nodes, index: i})
			}
		}
	}
	// The common case is one result produced by one or more seed Runs. It is
	// unambiguous and should not force the Agent to repeat the same ids twice.
	if len(results) == 1 {
		result := &(*results[0].nodes)[results[0].index]
		if result.SourceRunIDs == nil && result.SourceSnapshotIDs == nil {
			result.SourceRunIDs = append([]string(nil), proposal.SourceRunIDs...)
			result.SourceSnapshotIDs = append([]string(nil), proposal.SourceSnapshotIDs...)
			normalized, err := normalizeEvidenceNodeProvenance(*result)
			if err != nil {
				return err
			}
			*result = normalized
		}
	}
	for _, nodes := range [][]EvidenceChainNode{patch.Nodes, patch.UpsertNodes} {
		for _, node := range nodes {
			proposal.SourceRunIDs = append(proposal.SourceRunIDs, node.SourceRunIDs...)
			proposal.SourceSnapshotIDs = append(proposal.SourceSnapshotIDs, node.SourceSnapshotIDs...)
		}
	}
	proposal.SourceRunIDs = normalizeEvidenceProposalIDs(proposal.SourceRunIDs)
	proposal.SourceSnapshotIDs = normalizeEvidenceProposalIDs(proposal.SourceSnapshotIDs)
	return nil
}
