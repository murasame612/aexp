package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

func (s *SQLite) PlanEvidenceReorganization(ctx context.Context, mapID string, patch EvidenceGraphPatch) (*EvidenceReorganizationPlan, error) {
	mapID = strings.TrimSpace(mapID)
	patch.ChainID = mapID
	plan := &EvidenceReorganizationPlan{
		MapID: mapID, Patch: patch,
		Blockers: make([]EvidenceGraphBlocker, 0), Warnings: make([]EvidenceGraphWarning, 0),
	}
	chain, err := s.GetEvidenceChain(ctx, mapID)
	if err != nil {
		return nil, err
	}
	if chain == nil {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "GRAPH_NOT_FOUND", Message: "target Evidence Map does not exist"})
		return finalizeEvidenceReorganizationPlan(plan)
	}
	plan.ProjectID = chain.ProjectID
	plan.BaseRevision = chain.Revision
	plan.BaseGraphHash = chain.GraphHash
	if chain.Status != "active" {
		plan.Blockers = append(plan.Blockers, EvidenceGraphBlocker{Code: "GRAPH_ARCHIVED", Message: "archived Evidence Map is read-only"})
	}
	current, err := s.GetEvidenceChainGraph(ctx, chain.ID)
	if err != nil {
		return nil, err
	}
	plan.Before = BuildEvidenceResearchProjection(*chain, *current)
	merged := mergeEvidenceGraph(*current, patch)
	if err := validateEvidenceLayoutIntent(patch.LayoutIntent, merged); err != nil {
		plan.Blockers = append(plan.Blockers, blockerFromError(err))
	}
	if err := ValidateEvidenceChainGraph(&merged); err != nil {
		plan.Blockers = append(plan.Blockers, blockerFromError(err))
	} else {
		_, resultHash, hashErr := CanonicalEvidenceGraph(merged)
		if hashErr != nil {
			return nil, hashErr
		}
		plan.ResultGraphHash = resultHash
		resultChain := *chain
		resultChain.Revision = chain.Revision + 1
		resultChain.GraphHash = resultHash
		plan.After = BuildEvidenceResearchProjection(resultChain, merged)
	}
	readiness := &EvidenceGraphProposalPlan{Blockers: make([]EvidenceGraphBlocker, 0)}
	if err := s.appendEvidenceEligibilityBlockers(ctx, chain.ProjectID, current, &merged, patch, readiness); err != nil {
		return nil, err
	}
	appendEvidenceThreadContractBlockers(*chain, current, merged, patch, readiness)
	plan.Blockers = append(plan.Blockers, readiness.Blockers...)
	if err := s.ValidateEvidenceMapReferences(ctx, chain, &merged); err != nil {
		plan.Blockers = append(plan.Blockers, blockerFromError(err))
	}
	authoring := &EvidenceGraphProposalPlan{Warnings: make([]EvidenceGraphWarning, 0)}
	appendEvidenceAuthoringWarnings(merged, patch, authoring)
	plan.Warnings = append(plan.Warnings, authoring.Warnings...)
	return finalizeEvidenceReorganizationPlan(plan)
}

func finalizeEvidenceReorganizationPlan(plan *EvidenceReorganizationPlan) (*EvidenceReorganizationPlan, error) {
	canonicalPatch, err := canonicalEvidencePatch(plan.Patch)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]interface{}{
		"version":         1,
		"project_id":      plan.ProjectID,
		"map_id":          plan.MapID,
		"base_revision":   plan.BaseRevision,
		"base_graph_hash": plan.BaseGraphHash,
		"patch":           canonicalPatch,
	})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	plan.PlanHash = hex.EncodeToString(sum[:])
	plan.Eligible = len(plan.Blockers) == 0
	return plan, nil
}

func (s *SQLite) CreateEvidenceReorganizationProposal(
	ctx context.Context,
	mapID, summary, actor, routingReason string,
	sourceRunIDs []string,
	patch EvidenceGraphPatch,
	expectedPlanHash string,
) (*EvidenceProposal, error) {
	expectedPlanHash = strings.TrimSpace(expectedPlanHash)
	if expectedPlanHash == "" {
		return nil, graphValidationError("EXPECTED_PLAN_HASH_REQUIRED", "expected_plan_hash is required")
	}
	plan, err := s.PlanEvidenceReorganization(ctx, mapID, patch)
	if err != nil {
		return nil, err
	}
	if plan.PlanHash != expectedPlanHash {
		return nil, graphValidationError("PLAN_HASH_MISMATCH", fmt.Sprintf("expected plan %s; current plan is %s", expectedPlanHash, plan.PlanHash))
	}
	if !plan.Eligible {
		return nil, graphValidationError("REORGANIZATION_BLOCKED", "reorganization plan contains blockers")
	}
	chain, err := s.GetEvidenceChain(ctx, plan.MapID)
	if err != nil {
		return nil, err
	}
	if chain == nil {
		return nil, graphValidationError("GRAPH_NOT_FOUND", "target Evidence Map does not exist")
	}
	return s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID:          chain.ProjectID,
		TargetChainID:      chain.ID,
		Actor:              strings.TrimSpace(actor),
		Summary:            strings.TrimSpace(summary),
		RoutingReason:      strings.TrimSpace(routingReason),
		ProjectLevelImpact: chain.Role == "primary",
		SourceRunIDs:       normalizeEvidenceProposalIDs(sourceRunIDs),
		SourceKind:         "evidence_reorganization",
		SourceID:           plan.PlanHash,
	}, &plan.Patch)
}

func (s *SQLite) RebaseEvidenceProposal(ctx context.Context, proposalID, actor string) (*EvidenceProposal, error) {
	proposal, err := s.GetEvidenceProposal(ctx, strings.TrimSpace(proposalID))
	if err != nil {
		return nil, err
	}
	if proposal == nil {
		return nil, graphValidationError("PROPOSAL_NOT_FOUND", "Evidence Proposal does not exist")
	}
	if proposal.Status != GraphProposalPending {
		return nil, graphValidationError("PROPOSAL_NOT_PENDING", fmt.Sprintf("proposal status is %q", proposal.Status))
	}
	chain, err := s.GetEvidenceChain(ctx, proposal.TargetChainID)
	if err != nil {
		return nil, err
	}
	if chain == nil {
		return nil, graphValidationError("GRAPH_NOT_FOUND", "target Evidence Map does not exist")
	}
	if chain.Revision == proposal.BaseGraphRevision {
		return proposal, nil
	}
	plan, err := s.PlanEvidenceProposal(ctx, proposal.ID)
	if err != nil {
		return nil, err
	}
	if !plan.AutoRebased {
		for _, blocker := range plan.Blockers {
			switch blocker.Code {
			case "NODE_CHANGED_SINCE_BASE", "EDGE_CHANGED_SINCE_BASE", "REVISION_CONFLICT":
				return nil, graphValidationError("REBASE_CONFLICT", blocker.Message)
			}
		}
		return nil, graphValidationError("REBASE_CONFLICT", fmt.Sprintf("proposal cannot be safely replayed from revision %d onto revision %d", proposal.BaseGraphRevision, chain.Revision))
	}
	var patch EvidenceGraphPatch
	if err := json.Unmarshal([]byte(proposal.PatchJSON), &patch); err != nil {
		return nil, err
	}
	replacement, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID:          proposal.ProjectID,
		TargetChainID:      proposal.TargetChainID,
		Actor:              strings.TrimSpace(actor),
		Summary:            proposal.Summary,
		RoutingReason:      proposal.RoutingReason,
		ProjectLevelImpact: proposal.ProjectLevelImpact,
		SourceRunIDs:       proposal.SourceRunIDs,
		SourceSnapshotIDs:  proposal.SourceSnapshotIDs,
		SourceKind:         "proposal_rebase",
		SourceID:           proposal.ID,
	}, &patch)
	if err != nil {
		return nil, err
	}
	if _, err := s.ReviewEvidenceProposal(ctx, proposal.ID, "expire", firstNonEmptyString(strings.TrimSpace(actor), "agent")); err != nil {
		return nil, err
	}
	return replacement, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
