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

const evidenceProposalColumns = `id, project_id, target_chain_id, base_graph_revision,
	actor, summary, routing_reason, project_level_impact, source_run_ids_json,
	source_snapshot_ids_json, patch_json, status, proposal_hash, reviewed_by,
	reviewed_at, source_kind, source_id, created_at, updated_at`

func normalizeEvidenceProposalIDs(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func canonicalWorkspaceProposal(proposal EvidenceProposal, patch EvidenceGraphPatch) ([]byte, string, error) {
	canonicalPatch, err := canonicalEvidencePatch(patch)
	if err != nil {
		return nil, "", err
	}
	payload, err := json.Marshal(map[string]interface{}{
		"version":              2,
		"project_id":           strings.TrimSpace(proposal.ProjectID),
		"target_map_id":        strings.TrimSpace(proposal.TargetChainID),
		"base_graph_revision":  proposal.BaseGraphRevision,
		"summary":              strings.TrimSpace(proposal.Summary),
		"routing_reason":       strings.TrimSpace(proposal.RoutingReason),
		"project_level_impact": proposal.ProjectLevelImpact,
		"source_run_ids":       normalizeEvidenceProposalIDs(proposal.SourceRunIDs),
		"source_snapshot_ids":  normalizeEvidenceProposalIDs(proposal.SourceSnapshotIDs),
		"patch":                canonicalPatch,
	})
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(payload)
	return payload, hex.EncodeToString(sum[:]), nil
}

func newEvidenceProposalAttemptHash(base string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", base, time.Now().UnixNano())))
	return hex.EncodeToString(sum[:])
}

func (s *SQLite) validateEvidenceProposalOwnership(ctx context.Context, proposal *EvidenceProposal, patch *EvidenceGraphPatch) (*EvidenceChain, error) {
	proposal.ProjectID = strings.TrimSpace(proposal.ProjectID)
	if proposal.ProjectID == "" {
		return nil, graphValidationError("PROJECT_ID_REQUIRED", "project_id is required")
	}
	project, err := s.GetProjectDefinition(ctx, proposal.ProjectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, graphValidationError("PROJECT_NOT_FOUND", fmt.Sprintf("project %q does not exist", proposal.ProjectID))
	}

	proposal.TargetChainID = strings.TrimSpace(proposal.TargetChainID)
	if proposal.TargetChainID == "" {
		if patch != nil && strings.TrimSpace(patch.ChainID) != "" {
			return nil, graphValidationError("TARGET_MAP_REQUIRED", "draft proposal cannot carry a target map only inside patch_json")
		}
		return nil, nil
	}
	if patch == nil {
		return nil, graphValidationError("GRAPH_PATCH_REQUIRED", "evidence graph patch is required")
	}
	if patch.ChainID != "" && strings.TrimSpace(patch.ChainID) != proposal.TargetChainID {
		return nil, graphValidationError("TARGET_MAP_MISMATCH", "proposal target_map_id and patch chain_id differ")
	}
	patch.ChainID = proposal.TargetChainID
	chain, err := s.GetEvidenceChain(ctx, proposal.TargetChainID)
	if err != nil {
		return nil, err
	}
	if chain == nil {
		return nil, graphValidationError("GRAPH_NOT_FOUND", fmt.Sprintf("evidence map %q does not exist", proposal.TargetChainID))
	}
	if chain.Status != "active" {
		return nil, graphValidationError("GRAPH_ARCHIVED", "archived evidence map is read-only")
	}
	if strings.TrimSpace(chain.ProjectID) == "" || chain.ProjectID != proposal.ProjectID {
		return nil, graphValidationError("GRAPH_PROJECT_MISMATCH", fmt.Sprintf("evidence map %q does not belong to project %q", chain.ID, proposal.ProjectID))
	}
	proposal.RoutingReason = strings.TrimSpace(proposal.RoutingReason)
	if chain.Role == "secondary" && proposal.RoutingReason == "" {
		return nil, graphValidationError("ROUTING_REASON_REQUIRED", "routing_reason is required when targeting a topic evidence map")
	}
	if chain.Role == "primary" && !proposal.ProjectLevelImpact {
		return nil, graphValidationError("PRIMARY_SCOPE_REQUIRED", "primary evidence map proposals require project_level_impact")
	}
	proposal.BaseGraphRevision = chain.Revision
	return chain, nil
}

func (s *SQLite) validateEvidenceProposalSources(ctx context.Context, projectID string, runIDs, snapshotIDs []string) error {
	for _, runID := range normalizeEvidenceProposalIDs(runIDs) {
		run, err := s.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if run == nil {
			return graphValidationError("RUN_NOT_FOUND", fmt.Sprintf("run %q does not exist", runID))
		}
		if strings.TrimSpace(run.ProjectID) == "" || run.ProjectID != projectID {
			return graphValidationError("RUN_PROJECT_MISMATCH", fmt.Sprintf("run %q does not belong to project %q", runID, projectID))
		}
	}
	for _, snapshotID := range normalizeEvidenceProposalIDs(snapshotIDs) {
		snapshot, err := s.GetEvidenceSnapshot(ctx, snapshotID)
		if err != nil {
			return err
		}
		if snapshot == nil {
			return graphValidationError("SNAPSHOT_NOT_FOUND", fmt.Sprintf("snapshot %q does not exist", snapshotID))
		}
		run, err := s.GetRun(ctx, snapshot.RunID)
		if err != nil {
			return err
		}
		if run == nil || strings.TrimSpace(run.ProjectID) == "" || run.ProjectID != projectID {
			return graphValidationError("SNAPSHOT_PROJECT_MISMATCH", fmt.Sprintf("snapshot %q does not belong to project %q", snapshotID, projectID))
		}
	}
	return nil
}

func (s *SQLite) CreateEvidenceProposal(ctx context.Context, proposal *EvidenceProposal, patch *EvidenceGraphPatch) (*EvidenceProposal, error) {
	if proposal == nil {
		return nil, graphValidationError("PROPOSAL_REQUIRED", "evidence proposal is required")
	}
	if patch == nil {
		patch = &EvidenceGraphPatch{}
	}
	if _, err := s.validateEvidenceProposalOwnership(ctx, proposal, patch); err != nil {
		return nil, err
	}
	var err error
	patch.LayoutIntent, err = normalizeEvidenceLayoutIntent(patch.LayoutIntent)
	if err != nil {
		return nil, err
	}
	proposal.SourceRunIDs = normalizeEvidenceProposalIDs(proposal.SourceRunIDs)
	proposal.SourceSnapshotIDs = normalizeEvidenceProposalIDs(proposal.SourceSnapshotIDs)
	for i := range patch.Nodes {
		patch.Nodes[i].ChainID = proposal.TargetChainID
		patch.Nodes[i].X = 0
		patch.Nodes[i].Y = 0
		patch.Nodes[i].Pinned = false
		cleanedData, cleanErr := stripEvidenceProposalLayoutData(patch.Nodes[i].DataJSON)
		if cleanErr != nil {
			return nil, graphValidationError("INVALID_NODE_DATA", fmt.Sprintf("node %q data_json is invalid: %v", patch.Nodes[i].ID, cleanErr))
		}
		patch.Nodes[i].DataJSON = cleanedData
	}
	for i := range patch.UpsertNodes {
		patch.UpsertNodes[i].ChainID = proposal.TargetChainID
		patch.UpsertNodes[i].X = 0
		patch.UpsertNodes[i].Y = 0
		patch.UpsertNodes[i].Pinned = false
		cleanedData, cleanErr := stripEvidenceProposalLayoutData(patch.UpsertNodes[i].DataJSON)
		if cleanErr != nil {
			return nil, graphValidationError("INVALID_NODE_DATA", fmt.Sprintf("node %q data_json is invalid: %v", patch.UpsertNodes[i].ID, cleanErr))
		}
		patch.UpsertNodes[i].DataJSON = cleanedData
	}
	for i := range patch.Edges {
		patch.Edges[i].ChainID = proposal.TargetChainID
	}
	for i := range patch.UpsertEdges {
		patch.UpsertEdges[i].ChainID = proposal.TargetChainID
	}
	if err := prepareEvidenceProposalProvenance(proposal, patch); err != nil {
		return nil, err
	}
	if err := s.validateEvidenceProposalSources(ctx, proposal.ProjectID, proposal.SourceRunIDs, proposal.SourceSnapshotIDs); err != nil {
		return nil, err
	}
	_, proposalHash, err := canonicalWorkspaceProposal(*proposal, *patch)
	if err != nil {
		return nil, err
	}
	if existing, err := s.getEvidenceProposalByHash(ctx, proposalHash); err != nil {
		return nil, err
	} else if existing != nil {
		switch existing.Status {
		case GraphProposalDraft, GraphProposalPending:
			return existing, nil
		case GraphProposalRejected:
			proposalHash = newEvidenceProposalAttemptHash(proposalHash)
		case GraphProposalExpired:
			proposalHash = newEvidenceProposalAttemptHash(proposalHash)
		case GraphProposalAccepted:
			return nil, graphValidationError("PROPOSAL_ALREADY_ACCEPTED", "an identical evidence proposal was already accepted")
		default:
			return nil, graphValidationError("PROPOSAL_TERMINAL", fmt.Sprintf("an identical evidence proposal already exists in status %q", existing.Status))
		}
	}
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	runIDsJSON, _ := json.Marshal(proposal.SourceRunIDs)
	snapshotIDsJSON, _ := json.Marshal(proposal.SourceSnapshotIDs)
	proposal.ProposalHash = proposalHash
	if proposal.ID == "" {
		proposal.ID = "proposal_" + proposalHash[:20]
	}
	proposal.Status = GraphProposalDraft
	var target interface{}
	if proposal.TargetChainID != "" {
		proposal.Status = GraphProposalPending
		target = proposal.TargetChainID
	}
	proposal.PatchJSON = string(patchJSON)
	now := time.Now()
	proposal.CreatedAt = now
	proposal.UpdatedAt = now
	_, err = s.db.ExecContext(ctx, `INSERT INTO evidence_proposals (`+evidenceProposalColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		proposal.ID, proposal.ProjectID, target, proposal.BaseGraphRevision,
		strings.TrimSpace(proposal.Actor), strings.TrimSpace(proposal.Summary), proposal.RoutingReason,
		proposal.ProjectLevelImpact, string(runIDsJSON), string(snapshotIDsJSON), proposal.PatchJSON,
		proposal.Status, proposal.ProposalHash, "", nil, strings.TrimSpace(proposal.SourceKind),
		strings.TrimSpace(proposal.SourceID), proposal.CreatedAt, proposal.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return s.GetEvidenceProposal(ctx, proposal.ID)
}

func scanEvidenceProposal(scanner interface{ Scan(...interface{}) error }, proposal *EvidenceProposal) error {
	var target sql.NullString
	var projectLevelImpact bool
	var runIDsJSON, snapshotIDsJSON string
	if err := scanner.Scan(
		&proposal.ID, &proposal.ProjectID, &target, &proposal.BaseGraphRevision,
		&proposal.Actor, &proposal.Summary, &proposal.RoutingReason, &projectLevelImpact,
		&runIDsJSON, &snapshotIDsJSON, &proposal.PatchJSON, &proposal.Status,
		&proposal.ProposalHash, &proposal.ReviewedBy, &proposal.ReviewedAt,
		&proposal.SourceKind, &proposal.SourceID, &proposal.CreatedAt, &proposal.UpdatedAt,
	); err != nil {
		return err
	}
	proposal.TargetChainID = target.String
	proposal.ProjectLevelImpact = projectLevelImpact
	_ = json.Unmarshal([]byte(runIDsJSON), &proposal.SourceRunIDs)
	_ = json.Unmarshal([]byte(snapshotIDsJSON), &proposal.SourceSnapshotIDs)
	if proposal.SourceRunIDs == nil {
		proposal.SourceRunIDs = []string{}
	}
	if proposal.SourceSnapshotIDs == nil {
		proposal.SourceSnapshotIDs = []string{}
	}
	return nil
}

func (s *SQLite) GetEvidenceProposal(ctx context.Context, id string) (*EvidenceProposal, error) {
	proposal := &EvidenceProposal{}
	err := scanEvidenceProposal(s.db.QueryRowContext(ctx, `SELECT `+evidenceProposalColumns+` FROM evidence_proposals WHERE id = ?`, strings.TrimSpace(id)), proposal)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return proposal, err
}

func (s *SQLite) getEvidenceProposalByHash(ctx context.Context, hash string) (*EvidenceProposal, error) {
	proposal := &EvidenceProposal{}
	err := scanEvidenceProposal(s.db.QueryRowContext(ctx, `SELECT `+evidenceProposalColumns+` FROM evidence_proposals WHERE proposal_hash = ?`, hash), proposal)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return proposal, err
}

func (s *SQLite) ListEvidenceProposals(ctx context.Context, filter EvidenceProposalFilter) ([]EvidenceProposal, error) {
	query := `SELECT ` + evidenceProposalColumns + ` FROM evidence_proposals WHERE 1=1`
	args := make([]interface{}, 0, 5)
	if filter.ProjectID != "" {
		query += ` AND project_id = ?`
		args = append(args, filter.ProjectID)
	}
	if filter.TargetChainID != "" {
		query += ` AND target_chain_id = ?`
		args = append(args, filter.TargetChainID)
	}
	if filter.Status != "" {
		query += ` AND status = ?`
		args = append(args, filter.Status)
	}
	query += ` ORDER BY updated_at DESC`
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 80
	}
	query += ` LIMIT ? OFFSET ?`
	args = append(args, filter.Limit, filter.Offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]EvidenceProposal, 0)
	for rows.Next() {
		var proposal EvidenceProposal
		if err := scanEvidenceProposal(rows, &proposal); err != nil {
			return nil, err
		}
		result = append(result, proposal)
	}
	return result, rows.Err()
}

func (s *SQLite) PlanEvidenceProposal(ctx context.Context, id string) (*EvidenceGraphProposalPlan, error) {
	proposal, err := s.GetEvidenceProposal(ctx, id)
	if err != nil || proposal == nil {
		return nil, err
	}
	plan := &EvidenceGraphProposalPlan{
		ProposalID:        proposal.ID,
		ProjectID:         proposal.ProjectID,
		ChainID:           proposal.TargetChainID,
		RoutingReason:     proposal.RoutingReason,
		ProposalHash:      proposal.ProposalHash,
		Status:            proposal.Status,
		BaseGraphRevision: proposal.BaseGraphRevision,
		Blockers:          make([]EvidenceGraphBlocker, 0),
		Warnings:          make([]EvidenceGraphWarning, 0),
	}
	if proposal.Status != GraphProposalPending {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "PROPOSAL_NOT_PENDING", Message: fmt.Sprintf("proposal status is %q", proposal.Status)})
		return plan, nil
	}
	if time.Since(proposal.UpdatedAt) > 14*24*time.Hour {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "PROPOSAL_EXPIRED", Message: "proposal is older than 14 days"})
	}
	chain, err := s.GetEvidenceChain(ctx, proposal.TargetChainID)
	if err != nil {
		return nil, err
	}
	if chain == nil {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "GRAPH_NOT_FOUND", Message: "target evidence map does not exist"})
		return plan, nil
	}
	plan.CurrentGraphRevision = chain.Revision
	plan.AppliedGraphRevision = chain.Revision
	plan.CurrentGraphHash = chain.GraphHash
	if chain.Status != "active" {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "GRAPH_ARCHIVED", Message: "archived evidence map is read-only"})
	}
	if strings.TrimSpace(chain.ProjectID) == "" || chain.ProjectID != proposal.ProjectID {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "GRAPH_PROJECT_MISMATCH", Message: "target evidence map does not belong to proposal project"})
	}
	var patch EvidenceGraphPatch
	if err := json.Unmarshal([]byte(proposal.PatchJSON), &patch); err != nil {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "INVALID_GRAPH_PATCH", Message: err.Error()})
		return plan, nil
	}
	plan.NodesAdded = len(patch.Nodes) + len(patch.UpsertNodes)
	plan.EdgesAdded = len(patch.Edges) + len(patch.UpsertEdges)
	current, err := s.GetEvidenceChainGraph(ctx, proposal.TargetChainID)
	if err != nil {
		return nil, err
	}
	if chain.Revision != proposal.BaseGraphRevision {
		rebaseBlockers, rebaseErr := s.evidencePatchRebaseBlockers(ctx, chain.ID, proposal.BaseGraphRevision, chain.Revision, *current, patch)
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
				plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "RUN_NOT_FOUND", Message: fmt.Sprintf("run %q does not exist", node.RunID), NodeID: node.ID, RunID: node.RunID})
				continue
			}
			if strings.TrimSpace(run.ProjectID) == "" || run.ProjectID != proposal.ProjectID {
				plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "RUN_PROJECT_MISMATCH", Message: fmt.Sprintf("run %q does not belong to project %q", run.ID, proposal.ProjectID), NodeID: node.ID, RunID: run.ID})
			}
		}
		_, resultHash, hashErr := CanonicalEvidenceGraph(merged)
		if hashErr != nil {
			plan.Blockers = append(plan.Blockers, blockerFromError(hashErr))
		} else {
			plan.ResultGraphHash = resultHash
		}
	}
	readinessPlan := &EvidenceGraphProposalPlan{Blockers: make([]EvidenceGraphBlocker, 0)}
	if err := s.appendEvidenceEligibilityBlockers(ctx, proposal.ProjectID, current, &merged, patch, readinessPlan); err != nil {
		return nil, err
	}
	for _, blocker := range readinessPlan.Blockers {
		plan.Blockers = append(plan.Blockers, blocker)
	}
	appendEvidenceThreadContractBlockers(*chain, current, merged, patch, plan)
	if err := s.ValidateEvidenceMapReferences(ctx, chain, &merged); err != nil {
		plan.Blockers = append(plan.Blockers, blockerFromError(err))
	}
	appendEvidenceAuthoringWarnings(merged, patch, plan)
	plan.Eligible = len(plan.Blockers) == 0
	return plan, nil
}

func (s *SQLite) ReviewEvidenceProposal(ctx context.Context, id, action, reviewer string) (*EvidenceProposal, error) {
	proposal, err := s.GetEvidenceProposal(ctx, id)
	if err != nil {
		return nil, err
	}
	if proposal == nil {
		return nil, sql.ErrNoRows
	}
	if proposal.Status != GraphProposalPending {
		return nil, graphValidationError("PROPOSAL_NOT_PENDING", fmt.Sprintf("proposal status is %q", proposal.Status))
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "reject" || action == "expire" {
		next := GraphProposalRejected
		if action == "expire" {
			next = GraphProposalExpired
		}
		now := time.Now()
		result, err := s.db.ExecContext(ctx, `UPDATE evidence_proposals
			SET status = ?, reviewed_by = ?, reviewed_at = ?, updated_at = ?
			WHERE id = ? AND status = ? AND proposal_hash = ?`,
			next, strings.TrimSpace(reviewer), now, now, proposal.ID, GraphProposalPending, proposal.ProposalHash)
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return nil, graphValidationError("PROPOSAL_CHANGED", "proposal changed while it was being reviewed")
		}
		return s.GetEvidenceProposal(ctx, proposal.ID)
	}
	if action != "accept" {
		return nil, graphValidationError("INVALID_REVIEW_ACTION", "action must be accept, reject, or expire")
	}
	plan, err := s.PlanEvidenceProposal(ctx, proposal.ID)
	if err != nil {
		return nil, err
	}
	if plan == nil || !plan.Eligible {
		return nil, graphValidationError("PROPOSAL_BLOCKED", "proposal has blockers; run the side-effect-free plan first")
	}
	var patch EvidenceGraphPatch
	if err := json.Unmarshal([]byte(proposal.PatchJSON), &patch); err != nil {
		return nil, err
	}
	current, err := s.GetEvidenceChainGraph(ctx, proposal.TargetChainID)
	if err != nil {
		return nil, err
	}
	merged := mergeEvidenceGraph(*current, patch)
	graphJSON, graphHash, err := CanonicalEvidenceGraph(merged)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := replaceEvidenceGraphTx(ctx, tx, proposal.TargetChainID, merged, EvidenceGraphSaveOptions{
		ExpectedRevision: plan.AppliedGraphRevision,
		Actor:            strings.TrimSpace(reviewer),
		SourceKind:       "evidence_proposal",
		SourceID:         proposal.ID,
	}, graphJSON, graphHash); err != nil {
		return nil, err
	}
	now := time.Now()
	result, err := tx.ExecContext(ctx, `UPDATE evidence_proposals
		SET status = ?, reviewed_by = ?, reviewed_at = ?, updated_at = ?
		WHERE id = ? AND status = ? AND proposal_hash = ?`,
		GraphProposalAccepted, strings.TrimSpace(reviewer), now, now,
		proposal.ID, GraphProposalPending, proposal.ProposalHash)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, graphValidationError("PROPOSAL_CHANGED", "proposal changed while it was being accepted")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetEvidenceProposal(ctx, proposal.ID)
}

func (s *SQLite) RerouteEvidenceProposal(ctx context.Context, id, targetChainID, routingReason string, projectLevelImpact bool) (*EvidenceProposal, error) {
	previous, err := s.GetEvidenceProposal(ctx, id)
	if err != nil {
		return nil, err
	}
	if previous == nil {
		return nil, sql.ErrNoRows
	}
	if previous.Status != GraphProposalDraft && previous.Status != GraphProposalPending {
		return nil, graphValidationError("PROPOSAL_NOT_ROUTABLE", fmt.Sprintf("proposal status is %q", previous.Status))
	}
	var patch EvidenceGraphPatch
	if err := json.Unmarshal([]byte(previous.PatchJSON), &patch); err != nil {
		return nil, graphValidationError("INVALID_GRAPH_PATCH", err.Error())
	}
	next := &EvidenceProposal{
		ProjectID:          previous.ProjectID,
		TargetChainID:      strings.TrimSpace(targetChainID),
		Actor:              previous.Actor,
		Summary:            previous.Summary,
		RoutingReason:      strings.TrimSpace(routingReason),
		ProjectLevelImpact: projectLevelImpact,
		SourceRunIDs:       append([]string(nil), previous.SourceRunIDs...),
		SourceSnapshotIDs:  append([]string(nil), previous.SourceSnapshotIDs...),
		SourceKind:         "reroute",
		SourceID:           previous.ID,
	}
	patch.ChainID = next.TargetChainID
	created, err := s.CreateEvidenceProposal(ctx, next, &patch)
	if err != nil {
		return nil, err
	}
	if created.ID == previous.ID {
		return created, nil
	}
	now := time.Now()
	result, err := s.db.ExecContext(ctx, `UPDATE evidence_proposals
		SET status = ?, reviewed_by = ?, reviewed_at = ?, updated_at = ?
		WHERE id = ? AND status IN (?, ?)`,
		GraphProposalExpired, "reroute", now, now, previous.ID, GraphProposalDraft, GraphProposalPending)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, graphValidationError("PROPOSAL_CHANGED", "proposal changed while it was being rerouted")
	}
	return created, nil
}
