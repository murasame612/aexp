package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func createEvidenceWorkspaceProject(t *testing.T, s *SQLite, projectID string) (*ProjectDefinition, *EvidenceChain) {
	t.Helper()
	ctx := context.Background()
	project := &ProjectDefinition{ID: projectID, Name: "Evidence Workspace"}
	if err := s.CreateProjectDefinition(ctx, project); err != nil {
		t.Fatalf("CreateProjectDefinition: %v", err)
	}
	topic := &EvidenceChain{
		ID:          "chain_topic_" + projectID,
		Title:       "Protocol bootstrap",
		Description: "Initial research question, protocol, issue, and next step.",
		ProjectID:   project.ID,
		Role:        "secondary",
		Status:      "active",
	}
	if err := s.CreateEvidenceChain(ctx, topic); err != nil {
		t.Fatalf("CreateEvidenceChain: %v", err)
	}
	return project, topic
}

func TestEvidenceWorkspaceBootstrapProposalWithoutRunOrDataset(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_bootstrap")
	primary, err := s.GetActivePrimaryEvidenceChain(ctx, project.ID)
	if err != nil || primary == nil {
		t.Fatalf("primary = %#v, err = %v", primary, err)
	}

	proposal := &EvidenceProposal{
		ProjectID:     project.ID,
		TargetChainID: topic.ID,
		Actor:         "agent",
		Summary:       "Bootstrap the first usable research map.",
		RoutingReason: "This Topic owns protocol setup and the initial research question.",
	}
	patch := &EvidenceGraphPatch{
		ChainID: topic.ID,
		Nodes: []EvidenceChainNode{
			{ID: "hypothesis_bootstrap", Type: EvidenceNodeHypothesis, Title: "Bounded starting hypothesis"},
			{ID: "protocol_bootstrap", Type: EvidenceNodeProtocol, Title: "Initial evaluation protocol"},
			{ID: "claim_bootstrap", Type: EvidenceNodeClaim, Title: "Working claim"},
			{ID: "issue_bootstrap", Type: EvidenceNodeIssue, Title: "Protocol remains unverified"},
			{ID: "child_hypothesis_bootstrap", Type: EvidenceNodeClaim, Title: "Verified data will resolve the protocol issue", DataJSON: `{"claimKind":"hypothesis"}`},
		},
		Edges: []EvidenceChainEdge{
			{ID: "edge_hypothesis_claim", Type: EvidenceEdgeSupports, SourceNodeID: "hypothesis_bootstrap", TargetNodeID: "claim_bootstrap"},
			{ID: "edge_issue_child_hypothesis", Type: EvidenceEdgeNextStep, SourceNodeID: "issue_bootstrap", TargetNodeID: "child_hypothesis_bootstrap"},
			{ID: "edge_protocol_context", Type: EvidenceEdgeRelatedTo, SourceNodeID: "protocol_bootstrap", TargetNodeID: "hypothesis_bootstrap"},
		},
	}
	created, err := s.CreateEvidenceProposal(ctx, proposal, patch)
	if err != nil {
		t.Fatalf("CreateEvidenceProposal: %v", err)
	}
	if created.Status != GraphProposalPending || created.ID == "" || created.ProposalHash == "" {
		t.Fatalf("created = %#v", created)
	}
	if len(created.SourceRunIDs) != 0 || len(created.SourceSnapshotIDs) != 0 {
		t.Fatalf("bootstrap sources = %#v", created)
	}
	beforePrimary := *primary
	plan, err := s.PlanEvidenceProposal(ctx, created.ID)
	if err != nil {
		t.Fatalf("PlanEvidenceProposal: %v", err)
	}
	if !plan.Eligible || len(plan.Blockers) != 0 || plan.ResultGraphHash == "" {
		t.Fatalf("plan = %#v", plan)
	}
	accepted, err := s.ReviewEvidenceProposal(ctx, created.ID, "accept", "user")
	if err != nil {
		t.Fatalf("ReviewEvidenceProposal: %v", err)
	}
	if accepted.Status != GraphProposalAccepted || accepted.ReviewedAt == nil {
		t.Fatalf("accepted = %#v", accepted)
	}
	updatedTopic, _ := s.GetEvidenceChain(ctx, topic.ID)
	if updatedTopic.Revision != 1 || updatedTopic.GraphHash != plan.ResultGraphHash {
		t.Fatalf("topic = %#v, plan = %#v", updatedTopic, plan)
	}
	graph, err := s.GetEvidenceChainGraph(ctx, topic.ID)
	if err != nil || len(graph.Nodes) != 5 || len(graph.Edges) != 3 {
		t.Fatalf("graph = %#v, err = %v", graph, err)
	}
	afterPrimary, _ := s.GetEvidenceChain(ctx, primary.ID)
	if afterPrimary.Revision != beforePrimary.Revision || afterPrimary.GraphHash != beforePrimary.GraphHash {
		t.Fatalf("bootstrap polluted primary: before=%#v after=%#v", beforePrimary, afterPrimary)
	}
	revisions, err := s.ListEvidenceChainRevisions(ctx, topic.ID, 10)
	if err != nil || len(revisions) != 1 || revisions[0].SourceKind != "evidence_proposal" || revisions[0].SourceID != created.ID {
		t.Fatalf("revisions = %#v, err = %v", revisions, err)
	}
}

func TestEvidenceWorkspaceLayoutIntentIsReviewableAndValidated(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_layout_intent")
	patch := &EvidenceGraphPatch{
		ChainID: topic.ID,
		LayoutIntent: &EvidenceLayoutIntent{
			Flow:      "left_to_right",
			Ranks:     [][]string{{"issue_layout"}, {"hypothesis_layout"}},
			Rationale: "Put the blocker before the next action.",
		},
		Nodes: []EvidenceChainNode{
			{ID: "issue_layout", Type: EvidenceNodeIssue, Title: "Known blocker"},
			{ID: "hypothesis_layout", Type: EvidenceNodeClaim, Title: "A controlled rerun resolves the blocker", DataJSON: `{"claimKind":"hypothesis"}`},
		},
		Edges: []EvidenceChainEdge{{
			ID: "edge_layout", Type: EvidenceEdgeNextStep,
			SourceNodeID: "issue_layout", TargetNodeID: "hypothesis_layout",
		}},
	}
	created, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent",
		Summary: "Curate the research path", RoutingReason: "This Topic owns the blocker.",
	}, patch)
	if err != nil {
		t.Fatalf("CreateEvidenceProposal: %v", err)
	}
	var persisted EvidenceGraphPatch
	if err := json.Unmarshal([]byte(created.PatchJSON), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.LayoutIntent == nil || persisted.LayoutIntent.Flow != "left_to_right" ||
		len(persisted.LayoutIntent.Ranks) != 2 || persisted.LayoutIntent.Ranks[1][0] != "hypothesis_layout" {
		t.Fatalf("persisted layout intent = %#v", persisted.LayoutIntent)
	}
	plan, err := s.PlanEvidenceProposal(ctx, created.ID)
	if err != nil || !plan.Eligible {
		t.Fatalf("plan = %#v, err = %v", plan, err)
	}

	unknown, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent",
		Summary: "Invalid layout target", RoutingReason: "This Topic owns the blocker.",
	}, &EvidenceGraphPatch{
		ChainID: topic.ID,
		LayoutIntent: &EvidenceLayoutIntent{
			Flow:  "left_to_right",
			Ranks: [][]string{{"missing_node"}},
		},
		Nodes: []EvidenceChainNode{},
		Edges: []EvidenceChainEdge{},
	})
	if err != nil {
		t.Fatalf("CreateEvidenceProposal with unknown layout node: %v", err)
	}
	unknownPlan, err := s.PlanEvidenceProposal(ctx, unknown.ID)
	if err != nil || !hasEvidenceBlocker(unknownPlan.Blockers, "LAYOUT_NODE_NOT_FOUND") {
		t.Fatalf("unknown plan = %#v, err = %v", unknownPlan, err)
	}
}

func TestEvidenceWorkspaceUnroutedProposalStaysDraftAndDoesNotUsePrimary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_unrouted")
	primary, _ := s.GetActivePrimaryEvidenceChain(ctx, project.ID)

	created, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID,
		Actor:     "agent",
		Summary:   "The target Topic is not known yet.",
	}, &EvidenceGraphPatch{Nodes: []EvidenceChainNode{{ID: "issue_unrouted", Type: EvidenceNodeIssue}}})
	if err != nil {
		t.Fatalf("CreateEvidenceProposal: %v", err)
	}
	if created.Status != GraphProposalDraft || created.TargetChainID != "" {
		t.Fatalf("created = %#v", created)
	}
	plan, err := s.PlanEvidenceProposal(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Eligible || !hasEvidenceBlocker(plan.Blockers, "PROPOSAL_NOT_PENDING") {
		t.Fatalf("plan = %#v", plan)
	}
	after, _ := s.GetEvidenceChain(ctx, primary.ID)
	if after.Revision != primary.Revision || after.GraphHash != primary.GraphHash {
		t.Fatalf("draft changed primary: before=%#v after=%#v", primary, after)
	}
	rerouted, err := s.RerouteEvidenceProposal(ctx, created.ID, topic.ID, "This Topic owns unresolved research issues.", false)
	if err != nil {
		t.Fatalf("RerouteEvidenceProposal: %v", err)
	}
	if rerouted.ID == created.ID || rerouted.ProposalHash == created.ProposalHash || rerouted.Status != GraphProposalPending || rerouted.TargetChainID != topic.ID {
		t.Fatalf("rerouted = %#v, previous = %#v", rerouted, created)
	}
	previous, err := s.GetEvidenceProposal(ctx, created.ID)
	if err != nil || previous.Status != GraphProposalExpired {
		t.Fatalf("previous = %#v, err = %v", previous, err)
	}
	reroutedPlan, err := s.PlanEvidenceProposal(ctx, rerouted.ID)
	if err != nil || !reroutedPlan.Eligible {
		t.Fatalf("rerouted plan = %#v, err = %v", reroutedPlan, err)
	}
}

func TestEvidenceWorkspaceRejectsMapProjectMismatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	projectA, _ := createEvidenceWorkspaceProject(t, s, "project_a")
	_, topicB := createEvidenceWorkspaceProject(t, s, "project_b")
	_, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID:     projectA.ID,
		TargetChainID: topicB.ID,
		RoutingReason: "Incorrect cross-project target.",
	}, &EvidenceGraphPatch{ChainID: topicB.ID, Nodes: []EvidenceChainNode{{ID: "issue_cross_project", Type: EvidenceNodeIssue}}})
	var validation *EvidenceGraphValidationError
	if !errors.As(err, &validation) || validation.Code != "GRAPH_PROJECT_MISMATCH" {
		t.Fatalf("error = %#v", err)
	}
}

func TestEvidenceWorkspaceFormalBlockerIdentifiesEdgeNodeAndRun(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_gate")
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_gate", Name: "gate", Type: "ssh", Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	run := &Run{
		ID: "run_gate", ResourceID: "rsrc_gate", ProjectID: project.ID,
		Name: "unverified formal context", Status: RunStatusSucceeded,
		Kind: RunKindFormal, EvidenceGrade: "formal", Command: "true",
	}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID,
		RoutingReason: "This Topic owns the formal baseline claim.",
		SourceRunIDs:  []string{run.ID},
	}, &EvidenceGraphPatch{
		ChainID: topic.ID,
		Nodes: []EvidenceChainNode{
			{ID: "node_gate_run", Type: EvidenceNodeRun, RunID: run.ID},
			{ID: "node_gate_claim", Type: EvidenceNodeClaim, Title: "Formal claim"},
		},
		Edges: []EvidenceChainEdge{
			{ID: "edge_gate_support", Type: EvidenceEdgeSupports, SourceNodeID: "node_gate_run", TargetNodeID: "node_gate_claim"},
		},
	})
	if err != nil {
		t.Fatalf("CreateEvidenceProposal: %v", err)
	}
	plan, err := s.PlanEvidenceProposal(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Eligible || len(plan.Blockers) == 0 {
		t.Fatalf("plan = %#v", plan)
	}
	wantCodes := map[string]bool{
		"DATASET_MISSING":             false,
		"SEEDS_MISSING":               false,
		"PROJECT_CONFIG_HASH_MISSING": false,
		"GIT_COMMIT_MISSING":          false,
		"SPLIT_PROTOCOL_MISSING":      false,
		"EVALUATION_PROTOCOL_MISSING": false,
		"EVIDENCE_RELEASE_MISSING":    false,
	}
	for _, blocker := range plan.Blockers {
		if _, expected := wantCodes[blocker.Code]; expected {
			if blocker.RunID != run.ID || blocker.NodeID != "node_gate_run" || blocker.EdgeID != "edge_gate_support" {
				t.Fatalf("blocker %s lost object context: %#v", blocker.Code, blocker)
			}
			wantCodes[blocker.Code] = true
		}
		if blocker.Code == "RUN_PROJECT_MISMATCH" {
			t.Fatalf("valid project ownership was rejected: %#v", blocker)
		}
	}
	for code, found := range wantCodes {
		if !found {
			t.Fatalf("missing blocker %s in %#v", code, plan.Blockers)
		}
	}
}

func TestEvidenceWorkspaceResultProvenanceCannotBypassFormalRunGate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_result_gate")
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_result_gate", Name: "result-gate", Type: "ssh", Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	run := &Run{
		ID: "run_result_gate", ResourceID: "rsrc_result_gate", ProjectID: project.ID,
		Name: "smoke source disguised as a result", Status: RunStatusSucceeded,
		Kind: RunKindSmoke, EvidenceGrade: RunEvidenceGradeSmoke, Command: "true",
	}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID,
		RoutingReason: "This Topic owns the attempted formal conclusion.",
	}, &EvidenceGraphPatch{
		ChainID: topic.ID,
		Nodes: []EvidenceChainNode{
			{ID: "result_gate", Type: EvidenceNodeClaim, Title: "Observed metric", SourceRunIDs: []string{run.ID}, DataJSON: `{"claimKind":"result","resultDisposition":"conclusion"}`},
			{ID: "conclusion_gate", Type: EvidenceNodeConclusion, Title: "Formal conclusion"},
		},
		Edges: []EvidenceChainEdge{{ID: "edge_result_gate", Type: EvidenceEdgeSupports, SourceNodeID: "result_gate", TargetNodeID: "conclusion_gate"}},
	})
	if err != nil {
		t.Fatalf("CreateEvidenceProposal: %v", err)
	}
	plan, err := s.PlanEvidenceProposal(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	blocker := evidenceBlockerByCode(plan.Blockers, "RUN_NOT_FORMAL_EVIDENCE")
	if plan.Eligible || blocker == nil {
		t.Fatalf("smoke Result source bypassed formal readiness: %#v", plan)
	}
	if blocker.NodeID != "result_gate" || blocker.EdgeID != "edge_result_gate" || blocker.RunID != run.ID {
		t.Fatalf("result provenance blocker lost context: %#v", blocker)
	}
}

func TestEvidenceWorkspaceRejectedProposalCreatesNewAttempt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_terminal_hash")
	input := &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID,
		RoutingReason: "Rejected attempt must remain an explicit terminal record.",
	}
	patch := &EvidenceGraphPatch{
		ChainID: topic.ID,
		Nodes:   []EvidenceChainNode{{ID: "issue_terminal_hash", Type: EvidenceNodeIssue, Title: "Terminal proposal"}},
	}
	created, err := s.CreateEvidenceProposal(ctx, input, patch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewEvidenceProposal(ctx, created.ID, "reject", "reviewer"); err != nil {
		t.Fatal(err)
	}
	retry, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID,
		RoutingReason: "Rejected attempt must remain an explicit terminal record.",
	}, &EvidenceGraphPatch{
		ChainID: topic.ID,
		Nodes:   []EvidenceChainNode{{ID: "issue_terminal_hash", Type: EvidenceNodeIssue, Title: "Terminal proposal"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID == created.ID || retry.ProposalHash == created.ProposalHash || retry.Status != GraphProposalPending {
		t.Fatalf("retry did not create a new pending attempt: old=%#v new=%#v", created, retry)
	}
}

func TestEvidenceWorkspaceSupportsMultipleTopicsAndStableSourceOrdering(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, first := createEvidenceWorkspaceProject(t, s, "project_topics")
	for _, topic := range []EvidenceChain{
		{ID: "chain_topic_data", Title: "Data evolution", Description: "Dataset and protocol versions.", ProjectID: project.ID, Role: "secondary", Status: "active"},
		{ID: "chain_topic_ablation", Title: "Ablations", Description: "Controlled module comparisons.", ProjectID: project.ID, Role: "secondary", Status: "active"},
	} {
		if err := s.CreateEvidenceChain(ctx, &topic); err != nil {
			t.Fatalf("CreateEvidenceChain(%s): %v", topic.ID, err)
		}
	}
	chains, err := s.ListEvidenceChains(ctx, EvidenceChainFilter{ProjectID: project.ID, Role: "secondary", Status: "active"})
	if err != nil || len(chains) != 3 {
		t.Fatalf("topics = %#v, err = %v", chains, err)
	}
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_topics", Name: "topics", Type: "ssh", Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"run_source_a", "run_source_b"} {
		if err := s.CreateRun(ctx, &Run{ID: runID, ResourceID: "rsrc_topics", ProjectID: project.ID, Name: runID, Kind: RunKindPilot, Status: RunStatusSucceeded, Command: "true"}); err != nil {
			t.Fatal(err)
		}
	}
	patch := &EvidenceGraphPatch{
		ChainID: first.ID,
		Nodes:   []EvidenceChainNode{{ID: "issue_sources", Type: EvidenceNodeIssue, Title: "Context only"}},
	}
	left, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: first.ID, Actor: "agent", Summary: "Source order is semantic-free.",
		RoutingReason: "This Topic owns the source context.", SourceRunIDs: []string{"run_source_b", "run_source_a"},
	}, patch)
	if err != nil {
		t.Fatal(err)
	}
	right, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: first.ID, Actor: "agent", Summary: "Source order is semantic-free.",
		RoutingReason: "This Topic owns the source context.", SourceRunIDs: []string{"run_source_a", "run_source_b"},
	}, patch)
	if err != nil {
		t.Fatal(err)
	}
	if left.ID != right.ID || left.ProposalHash != right.ProposalHash {
		t.Fatalf("source ordering changed identity: left=%#v right=%#v", left, right)
	}
}

func TestEvidenceWorkspaceSingleResultInheritsProposalRunProvenance(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_result_provenance")
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_result_provenance", Name: "result provenance", Type: "ssh", Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"run_result_seed_41", "run_result_seed_42"} {
		if err := s.CreateRun(ctx, &Run{ID: runID, ResourceID: "rsrc_result_provenance", ProjectID: project.ID, Name: runID, Kind: RunKindPilot, Status: RunStatusSucceeded, Command: "true"}); err != nil {
			t.Fatal(err)
		}
	}
	created, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent",
		Summary: "Record one observed result", RoutingReason: "This Topic owns the result.",
		SourceRunIDs: []string{"run_result_seed_42", "run_result_seed_41"},
	}, &EvidenceGraphPatch{
		ChainID: topic.ID,
		Nodes: []EvidenceChainNode{
			{ID: "result_hypothesis", Type: EvidenceNodeClaim, Title: "Result hypothesis", DataJSON: `{"claimKind":"hypothesis"}`},
			{ID: "result_design", Type: EvidenceNodeExperiment, Title: "Cross-seed design"},
			{
				ID: "result_multi_seed", Type: EvidenceNodeClaim, Title: "Observed result",
				DataJSON: `{"claimKind":"result","resultDisposition":"pending","dispositionReason":"awaiting cross-seed interpretation"}`,
			},
		},
		Edges: []EvidenceChainEdge{
			{ID: "result_hypothesis_design", Type: EvidenceEdgeNextStep, SourceNodeID: "result_hypothesis", TargetNodeID: "result_design"},
			{ID: "result_design_result", Type: EvidenceEdgeNextStep, SourceNodeID: "result_design", TargetNodeID: "result_multi_seed"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var persisted EvidenceGraphPatch
	if err := json.Unmarshal([]byte(created.PatchJSON), &persisted); err != nil {
		t.Fatal(err)
	}
	var persistedResult EvidenceChainNode
	for _, node := range persisted.Nodes {
		if node.ID == "result_multi_seed" {
			persistedResult = node
		}
	}
	if got := persistedResult.SourceRunIDs; len(got) != 2 || got[0] != "run_result_seed_41" || got[1] != "run_result_seed_42" {
		t.Fatalf("result provenance = %#v", got)
	}
	plan, err := s.PlanEvidenceProposal(ctx, created.ID)
	if err != nil || !plan.Eligible || hasEvidenceBlocker(plan.Blockers, "RESULT_PROVENANCE_REQUIRED") {
		t.Fatalf("plan = %#v, err = %v", plan, err)
	}
	if plan.ProjectedResearch == nil || len(plan.ProjectedResearch.Unassigned) != 0 || len(plan.ProjectedResearch.Threads) != 1 || len(plan.ProjectedResearch.Threads[0].Stages[EvidenceResearchStageResult]) != 1 {
		t.Fatalf("projected research did not confirm one assigned Result: %#v", plan.ProjectedResearch)
	}
	if _, err := s.ReviewEvidenceProposal(ctx, created.ID, "accept", "user"); err != nil {
		t.Fatal(err)
	}
	graph, err := s.GetEvidenceChainGraph(ctx, topic.ID)
	if err != nil || len(graph.Nodes) != 3 {
		t.Fatalf("graph = %#v, err = %v", graph, err)
	}
	var storedResult EvidenceChainNode
	for _, node := range graph.Nodes {
		if node.ID == "result_multi_seed" {
			storedResult = node
		}
	}
	if got := storedResult.SourceRunIDs; len(got) != 2 || got[0] != "run_result_seed_41" || got[1] != "run_result_seed_42" {
		t.Fatalf("stored result provenance = %#v", got)
	}
}

func TestEvidenceWorkspaceResultDispositionMustMatchOutcomeEdges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_result_disposition")
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_result_disposition", Name: "result disposition", Type: "ssh", Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_result_disposition", ResourceID: "rsrc_result_disposition", ProjectID: project.ID, Name: "result disposition", Kind: RunKindPilot, Status: RunStatusSucceeded, Command: "true"}); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent",
		Summary: "Route an interpreted result", RoutingReason: "This Topic owns the result.",
	}, &EvidenceGraphPatch{
		ChainID: topic.ID,
		Nodes: []EvidenceChainNode{
			{ID: "hypothesis", Type: EvidenceNodeHypothesis, Title: "Hypothesis", DataJSON: `{}`},
			{ID: "result", Type: EvidenceNodeClaim, Title: "Result", SourceRunIDs: []string{"run_result_disposition"}, DataJSON: `{"claimKind":"result","resultDisposition":"conclusion"}`},
			{ID: "issue", Type: EvidenceNodeIssue, Title: "Issue", DataJSON: `{}`},
		},
		Edges: []EvidenceChainEdge{
			{ID: "design-result", SourceNodeID: "hypothesis", TargetNodeID: "result", Type: EvidenceEdgeNextStep},
			{ID: "result-issue", SourceNodeID: "result", TargetNodeID: "issue", Type: EvidenceEdgeRevealsIssue, Rationale: "the evaluation is underpowered"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanEvidenceProposal(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEvidenceBlocker(plan.Blockers, "RESULT_DISPOSITION_EDGE_MISMATCH") {
		t.Fatalf("result disposition blockers = %#v", plan.Blockers)
	}
}

func TestEvidenceWorkspaceRejectsResultOutcomeBypasses(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_result_bypass")
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_result_bypass", Name: "result bypass", Type: "ssh", Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_result_bypass", ResourceID: "rsrc_result_bypass", ProjectID: project.ID, Name: "result bypass", Kind: RunKindPilot, Status: RunStatusSucceeded, Command: "true"}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		target   EvidenceChainNode
		edgeType string
	}{
		{name: "plan", target: EvidenceChainNode{ID: "plan", Type: EvidenceNodePlan}, edgeType: EvidenceEdgeNextStep},
		{name: "canonical hypothesis", target: EvidenceChainNode{ID: "canonical_hypothesis", Type: EvidenceNodeClaim, DataJSON: `{"claimKind":"hypothesis"}`}, edgeType: EvidenceEdgeSupersedes},
		{name: "legacy hypothesis", target: EvidenceChainNode{ID: "legacy_hypothesis", Type: EvidenceNodeHypothesis}, edgeType: EvidenceEdgeSupports},
		{name: "legacy experiment", target: EvidenceChainNode{ID: "legacy_experiment", Type: EvidenceNodeExperiment}, edgeType: EvidenceEdgeNextStep},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resultID := "result_" + strings.ReplaceAll(tt.name, " ", "_")
			proposal, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
				ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent",
				Summary: "Attempt a Result stage bypass", RoutingReason: "This Topic owns the result.",
			}, &EvidenceGraphPatch{
				ChainID: topic.ID,
				Nodes: []EvidenceChainNode{
					{ID: resultID, Type: EvidenceNodeClaim, SourceRunIDs: []string{"run_result_bypass"}, DataJSON: `{"claimKind":"result","resultDisposition":"pending","dispositionReason":"awaiting interpretation"}`},
					tt.target,
				},
				Edges: []EvidenceChainEdge{{ID: "bypass_" + resultID, SourceNodeID: resultID, TargetNodeID: tt.target.ID, Type: tt.edgeType}},
			})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := s.PlanEvidenceProposal(ctx, proposal.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !hasEvidenceBlocker(plan.Blockers, "RESULT_OUTCOME_BYPASS") {
				t.Fatalf("bypass blockers = %#v", plan.Blockers)
			}
		})
	}
}

func TestEvidenceWorkspaceNeutralResultLinkDoesNotForceLegacyDispositionMigration(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_result_neutral_link")
	legacy := EvidenceChainGraph{Nodes: []EvidenceChainNode{
		{ID: "legacy_result", Type: EvidenceNodeClaim, DataJSON: `{"claimKind":"result"}`},
		{ID: "context_plan", Type: EvidenceNodePlan},
	}}
	if _, err := s.SaveEvidenceChainGraphCAS(ctx, topic.ID, legacy, EvidenceGraphSaveOptions{ExpectedRevision: 0, Actor: "legacy", SourceKind: "migration"}); err != nil {
		t.Fatal(err)
	}
	proposal, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent",
		Summary: "Add neutral context", RoutingReason: "This Topic owns the context.",
	}, &EvidenceGraphPatch{
		ChainID: topic.ID,
		Edges:   []EvidenceChainEdge{{ID: "neutral_context", SourceNodeID: "legacy_result", TargetNodeID: "context_plan", Type: EvidenceEdgeRelatedTo}},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanEvidenceProposal(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Eligible || hasEvidenceBlocker(plan.Blockers, "RESULT_DISPOSITION_REQUIRED") || hasEvidenceBlocker(plan.Blockers, "RESULT_PROVENANCE_REQUIRED") {
		t.Fatalf("neutral link should not migrate legacy Result: %#v", plan)
	}
}

func TestEvidenceWorkspaceMultipleResultsRequireExplicitNodeProvenance(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_result_provenance_split")
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_result_provenance_split", Name: "result provenance", Type: "ssh", Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_shared_context", ResourceID: "rsrc_result_provenance_split", ProjectID: project.ID, Name: "shared", Kind: RunKindPilot, Status: RunStatusSucceeded, Command: "true"}); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent",
		Summary: "Two distinct results", RoutingReason: "This Topic owns both results.",
		SourceRunIDs: []string{"run_shared_context"},
	}, &EvidenceGraphPatch{ChainID: topic.ID, Nodes: []EvidenceChainNode{
		{ID: "result_a", Type: EvidenceNodeClaim, Title: "Result A", DataJSON: `{"claimKind":"result"}`},
		{ID: "result_b", Type: EvidenceNodeClaim, Title: "Result B", DataJSON: `{"claimKind":"result"}`},
	}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanEvidenceProposal(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Eligible || !hasEvidenceBlocker(plan.Blockers, "RESULT_PROVENANCE_REQUIRED") {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestEvidenceWorkspaceStaleDisjointAdditionsRebaseSafely(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_proposal_cas")
	makeProposal := func(id string) *EvidenceProposal {
		proposal, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
			ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent",
			Summary: "CAS proposal " + id, RoutingReason: "This Topic owns the independent planning context.",
		}, &EvidenceGraphPatch{
			ChainID: topic.ID,
			Nodes:   []EvidenceChainNode{{ID: id, Type: EvidenceNodePlan, Title: id}},
		})
		if err != nil {
			t.Fatalf("CreateEvidenceProposal(%s): %v", id, err)
		}
		return proposal
	}
	first := makeProposal("plan_first")
	second := makeProposal("plan_second")
	if _, err := s.ReviewEvidenceProposal(ctx, first.ID, "accept", "user"); err != nil {
		t.Fatalf("accept first: %v", err)
	}
	secondPlan, err := s.PlanEvidenceProposal(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !secondPlan.Eligible || !secondPlan.AutoRebased || secondPlan.AppliedGraphRevision != secondPlan.CurrentGraphRevision {
		t.Fatalf("second plan = %#v", secondPlan)
	}
	if _, err := s.ReviewEvidenceProposal(ctx, second.ID, "accept", "user"); err != nil {
		t.Fatalf("accept disjoint stale proposal: %v", err)
	}
	if _, err := s.ReviewEvidenceProposal(ctx, first.ID, "accept", "user"); err == nil {
		t.Fatal("same proposal accepted twice")
	}
	graph, _ := s.GetEvidenceChainGraph(ctx, topic.ID)
	if len(graph.Nodes) != 2 || graph.Nodes[0].ID != "plan_first" || graph.Nodes[1].ID != "plan_second" {
		t.Fatalf("graph after CAS = %#v", graph)
	}
}

func TestEvidenceWorkspaceStaleConflictingPatchRemainsBlocked(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_proposal_conflict")
	makeProposal := func(summary string, patch EvidenceGraphPatch) *EvidenceProposal {
		proposal, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
			ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent",
			Summary: summary, RoutingReason: "This Topic owns the conflicting planning context.",
		}, &patch)
		if err != nil {
			t.Fatalf("CreateEvidenceProposal(%s): %v", summary, err)
		}
		return proposal
	}
	first := makeProposal("first", EvidenceGraphPatch{
		ChainID: topic.ID,
		Nodes:   []EvidenceChainNode{{ID: "plan_shared", Type: EvidenceNodePlan, Title: "first"}},
	})
	collision := makeProposal("collision", EvidenceGraphPatch{
		ChainID: topic.ID,
		Nodes:   []EvidenceChainNode{{ID: "plan_shared", Type: EvidenceNodePlan, Title: "second"}},
	})
	upsert := makeProposal("upsert", EvidenceGraphPatch{
		ChainID:     topic.ID,
		UpsertNodes: []EvidenceChainNode{{ID: "plan_shared", Type: EvidenceNodePlan, Title: "replacement"}},
	})
	if _, err := s.ReviewEvidenceProposal(ctx, first.ID, "accept", "user"); err != nil {
		t.Fatalf("accept first: %v", err)
	}
	for _, proposal := range []*EvidenceProposal{collision, upsert} {
		plan, err := s.PlanEvidenceProposal(ctx, proposal.ID)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Eligible || plan.AutoRebased || !hasEvidenceBlocker(plan.Blockers, "NODE_CHANGED_SINCE_BASE") {
			t.Fatalf("conflicting stale plan = %#v", plan)
		}
		if _, err := s.ReviewEvidenceProposal(ctx, proposal.ID, "accept", "user"); err == nil {
			t.Fatalf("conflicting stale proposal %s unexpectedly accepted", proposal.ID)
		}
	}
}

func TestEvidenceWorkspaceStaleUpsertRebasesOnlyWhenTargetIsUnchanged(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_proposal_upsert_rebase")
	create := func(summary string, patch EvidenceGraphPatch) *EvidenceProposal {
		proposal, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
			ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent",
			Summary: summary, RoutingReason: "This Topic owns the planning context.",
		}, &patch)
		if err != nil {
			t.Fatalf("CreateEvidenceProposal(%s): %v", summary, err)
		}
		return proposal
	}
	seed := create("seed", EvidenceGraphPatch{ChainID: topic.ID, Nodes: []EvidenceChainNode{{ID: "plan_target", Type: EvidenceNodePlan, Title: "before"}}})
	if _, err := s.ReviewEvidenceProposal(ctx, seed.ID, "accept", "user"); err != nil {
		t.Fatal(err)
	}
	staleUpsert := create("safe upsert", EvidenceGraphPatch{ChainID: topic.ID, UpsertNodes: []EvidenceChainNode{{ID: "plan_target", Type: EvidenceNodePlan, Title: "after"}}})
	unrelated := create("unrelated", EvidenceGraphPatch{ChainID: topic.ID, Nodes: []EvidenceChainNode{{ID: "issue_unrelated", Type: EvidenceNodeIssue, Title: "unrelated"}}})
	if _, err := s.ReviewEvidenceProposal(ctx, unrelated.ID, "accept", "user"); err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanEvidenceProposal(ctx, staleUpsert.ID)
	if err != nil || !plan.Eligible || !plan.AutoRebased {
		t.Fatalf("safe stale upsert plan = %#v, err = %v", plan, err)
	}
	if _, err := s.ReviewEvidenceProposal(ctx, staleUpsert.ID, "accept", "user"); err != nil {
		t.Fatalf("accept safe stale upsert: %v", err)
	}

	firstChange := create("first change", EvidenceGraphPatch{ChainID: topic.ID, UpsertNodes: []EvidenceChainNode{{ID: "plan_target", Type: EvidenceNodePlan, Title: "first concurrent change"}}})
	secondChange := create("second change", EvidenceGraphPatch{ChainID: topic.ID, UpsertNodes: []EvidenceChainNode{{ID: "plan_target", Type: EvidenceNodePlan, Title: "second concurrent change"}}})
	if _, err := s.ReviewEvidenceProposal(ctx, firstChange.ID, "accept", "user"); err != nil {
		t.Fatal(err)
	}
	blocked, err := s.PlanEvidenceProposal(ctx, secondChange.ID)
	if err != nil || blocked.Eligible || !hasEvidenceBlocker(blocked.Blockers, "NODE_CHANGED_SINCE_BASE") {
		t.Fatalf("conflicting stale upsert plan = %#v, err = %v", blocked, err)
	}
}

func TestEvidenceWorkspaceStaleDeleteDoesNotRemoveNewIncidentEdge(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_proposal_delete_rebase")
	create := func(summary string, patch EvidenceGraphPatch) *EvidenceProposal {
		proposal, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
			ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent",
			Summary: summary, RoutingReason: "This Topic owns the planning context.",
		}, &patch)
		if err != nil {
			t.Fatalf("CreateEvidenceProposal(%s): %v", summary, err)
		}
		return proposal
	}
	seed := create("seed", EvidenceGraphPatch{ChainID: topic.ID, Nodes: []EvidenceChainNode{
		{ID: "issue_delete", Type: EvidenceNodeIssue, Title: "issue"},
		{ID: "plan_keep", Type: EvidenceNodePlan, Title: "plan"},
	}})
	if _, err := s.ReviewEvidenceProposal(ctx, seed.ID, "accept", "user"); err != nil {
		t.Fatal(err)
	}
	staleDelete := create("delete issue", EvidenceGraphPatch{ChainID: topic.ID, DeleteNodeIDs: []string{"issue_delete"}})
	newRelation := create("relate issue", EvidenceGraphPatch{ChainID: topic.ID, Edges: []EvidenceChainEdge{{
		ID: "edge_new_after_base", Type: EvidenceEdgeRelatedTo, SourceNodeID: "issue_delete", TargetNodeID: "plan_keep",
	}}})
	if _, err := s.ReviewEvidenceProposal(ctx, newRelation.ID, "accept", "user"); err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanEvidenceProposal(ctx, staleDelete.ID)
	if err != nil || plan.Eligible || !hasEvidenceBlocker(plan.Blockers, "EDGE_CHANGED_SINCE_BASE") {
		t.Fatalf("stale delete plan = %#v, err = %v", plan, err)
	}
}

func TestEvidenceWorkspaceLegacyRunContextDoesNotRequireDatasetWithoutFormalAssertion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_safe_legacy")
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_safe_legacy", Name: "safe-legacy", Type: "ssh", Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: "run_safe_legacy", ResourceID: "rsrc_safe_legacy", ProjectID: project.ID, Name: "legacy context", Kind: RunKindFormal, Status: RunStatusSucceeded, Command: "true"}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	proposal, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent",
		Summary:       "Record the legacy issue without making a formal claim.",
		RoutingReason: "This Topic owns protocol failures.", SourceRunIDs: []string{run.ID},
	}, &EvidenceGraphPatch{
		ChainID: topic.ID,
		Nodes: []EvidenceChainNode{
			{ID: "legacy_run", Type: EvidenceNodeRun, RunID: run.ID, Title: "Legacy run (unverified)"},
			{ID: "legacy_issue", Type: EvidenceNodeIssue, Title: "Dataset provenance was not locked"},
			{ID: "followup_hypothesis", Type: EvidenceNodeClaim, Title: "Verified data will make the failure interpretable", DataJSON: `{"claimKind":"hypothesis"}`},
		},
		Edges: []EvidenceChainEdge{
			{ID: "reveals_legacy_issue", Type: EvidenceEdgeRevealsIssue, SourceNodeID: "legacy_run", TargetNodeID: "legacy_issue"},
			{ID: "next_after_legacy", Type: EvidenceEdgeNextStep, SourceNodeID: "legacy_issue", TargetNodeID: "followup_hypothesis"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanEvidenceProposal(ctx, proposal.ID)
	if err != nil || !plan.Eligible {
		t.Fatalf("safe legacy plan = %#v, err = %v", plan, err)
	}
}

func TestEvidenceMapOwnershipMigrationBindsOnlyExplicitMappingsAndPreservesGraph(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateProjectDefinition(ctx, &ProjectDefinition{ID: "project_migration", Name: "Migration target"}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id    string
		title string
	}{
		{id: "chain_orphan_bound", title: "Explicitly mapped legacy graph"},
		{id: "chain_orphan_archived", title: "Ambiguous legacy graph"},
	} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO evidence_chains
			(id,title,project_id,role,status,revision,graph_hash) VALUES (?,?,'','secondary','active',3,'legacy-hash')`,
			row.id, row.title); err != nil {
			t.Fatal(err)
		}
		for _, nodeID := range []string{row.id + "_a", row.id + "_b"} {
			if _, err := s.db.ExecContext(ctx, `INSERT INTO evidence_chain_nodes(id,chain_id,type,title) VALUES (?,?,?,?)`,
				nodeID, row.id, EvidenceNodeIssue, nodeID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO evidence_chain_edges
			(id,chain_id,source_node_id,target_node_id,type) VALUES (?,?,?,?,?)`,
			row.id+"_edge", row.id, row.id+"_a", row.id+"_b", EvidenceEdgeNextStep); err != nil {
			t.Fatal(err)
		}
	}

	report, err := s.PlanEvidenceMapOwnershipMigration(ctx, map[string]string{
		"chain_orphan_bound": "project_migration",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || report.OrphanCount != 2 || report.BoundCount != 1 || report.ArchivedCount != 1 {
		t.Fatalf("plan = %#v", report)
	}
	applied, err := s.ApplyEvidenceMapOwnershipMigration(ctx, map[string]string{
		"chain_orphan_bound": "project_migration",
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied.DryRun {
		t.Fatalf("applied report = %#v", applied)
	}
	bound, _ := s.GetEvidenceChain(ctx, "chain_orphan_bound")
	if bound.ProjectID != "project_migration" || bound.Status != "active" || bound.Revision != 3 || bound.GraphHash != "legacy-hash" {
		t.Fatalf("bound = %#v", bound)
	}
	archived, _ := s.GetEvidenceChain(ctx, "chain_orphan_archived")
	if archived.ProjectID != "" || archived.Status != "archived" || archived.Role != "archive" || archived.Revision != 3 || archived.GraphHash != "legacy-hash" {
		t.Fatalf("archived = %#v", archived)
	}
	for _, mapID := range []string{bound.ID, archived.ID} {
		graph, graphErr := s.GetEvidenceChainGraph(ctx, mapID)
		if graphErr != nil || len(graph.Nodes) != 2 || len(graph.Edges) != 1 {
			t.Fatalf("%s graph = %#v, err = %v", mapID, graph, graphErr)
		}
	}
	var integrity string
	if err := s.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%q err=%v", integrity, err)
	}
}

func TestEvidenceWorkspacePrimaryMapReferencePinsTopicRevisionAndHash(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_map_ref")
	topicProposal, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent",
		Summary: "Create the Topic conclusion.", RoutingReason: "This Topic owns the detailed conclusion.",
	}, &EvidenceGraphPatch{
		ChainID: topic.ID,
		Nodes:   []EvidenceChainNode{{ID: "topic_claim", Type: EvidenceNodeClaim, Title: "Topic conclusion"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewEvidenceProposal(ctx, topicProposal.ID, "accept", "user"); err != nil {
		t.Fatal(err)
	}
	topicV1, _ := s.GetEvidenceChain(ctx, topic.ID)
	referenceJSON, _ := json.Marshal(EvidenceMapReference{
		TargetMapID: topic.ID, TargetRevision: topicV1.Revision, TargetGraphHash: topicV1.GraphHash,
		TargetNodeIDs: []string{"topic_claim"}, Summary: "Detailed protocol conclusion",
	})
	primary, _ := s.GetActivePrimaryEvidenceChain(ctx, project.ID)
	primaryProposal, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: primary.ID, Actor: "agent",
		Summary: "Promote the Topic conclusion to the project index.", ProjectLevelImpact: true,
	}, &EvidenceGraphPatch{
		ChainID: primary.ID,
		Nodes: []EvidenceChainNode{
			{ID: "primary_summary", Type: EvidenceNodeClaim, Title: "Project-level conclusion"},
			{ID: "primary_topic_ref", Type: EvidenceNodeMapRef, Title: topic.Title, Body: "Open the detailed Topic.", DataJSON: string(referenceJSON)},
		},
		Edges: []EvidenceChainEdge{{ID: "summary_ref", Type: EvidenceEdgeRelatedTo, SourceNodeID: "primary_summary", TargetNodeID: "primary_topic_ref"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanEvidenceProposal(ctx, primaryProposal.ID)
	if err != nil || !plan.Eligible {
		t.Fatalf("map ref plan = %#v, err = %v", plan, err)
	}
	if _, err := s.ReviewEvidenceProposal(ctx, primaryProposal.ID, "accept", "user"); err != nil {
		t.Fatal(err)
	}

	topicGraph, _ := s.GetEvidenceChainGraph(ctx, topic.ID)
	topicGraph.Nodes = append(topicGraph.Nodes, EvidenceChainNode{ID: "topic_plan", Type: EvidenceNodePlan, Title: "Next iteration"})
	if _, err := s.SaveEvidenceChainGraphCAS(ctx, topic.ID, *topicGraph, EvidenceGraphSaveOptions{
		ExpectedRevision: topicV1.Revision, Actor: "user", SourceKind: "test", SourceID: "topic-v2",
	}); err != nil {
		t.Fatal(err)
	}
	topicV2, _ := s.GetEvidenceChain(ctx, topic.ID)
	if topicV2.Revision != topicV1.Revision+1 || topicV2.GraphHash == topicV1.GraphHash {
		t.Fatalf("topic did not advance: v1=%#v v2=%#v", topicV1, topicV2)
	}
	primaryGraph, _ := s.GetEvidenceChainGraph(ctx, primary.ID)
	var pinned EvidenceMapReference
	for _, node := range primaryGraph.Nodes {
		if node.Type == EvidenceNodeMapRef {
			if err := json.Unmarshal([]byte(node.DataJSON), &pinned); err != nil {
				t.Fatal(err)
			}
		}
	}
	if pinned.TargetRevision != topicV1.Revision || pinned.TargetGraphHash != topicV1.GraphHash {
		t.Fatalf("reference drifted after Topic update: %#v", pinned)
	}
	primaryCurrent, _ := s.GetEvidenceChain(ctx, primary.ID)
	primaryBeforeArchive := *primaryCurrent
	if err := s.DeleteEvidenceChain(ctx, topic.ID); err != nil {
		t.Fatalf("archive Topic: %v", err)
	}
	archivedTopic, err := s.GetEvidenceChain(ctx, topic.ID)
	if err != nil || archivedTopic == nil || archivedTopic.Status != "archived" || archivedTopic.Role != "archive" {
		t.Fatalf("archived Topic = %#v, err = %v", archivedTopic, err)
	}
	if archivedTopic.Revision != topicV2.Revision || archivedTopic.GraphHash != topicV2.GraphHash {
		t.Fatalf("archive changed Topic revision: before=%#v after=%#v", topicV2, archivedTopic)
	}
	archivedTopicGraph, err := s.GetEvidenceChainGraph(ctx, topic.ID)
	if err != nil || len(archivedTopicGraph.Nodes) != len(topicGraph.Nodes) || len(archivedTopicGraph.Edges) != len(topicGraph.Edges) {
		t.Fatalf("archive removed Topic history: graph=%#v err=%v", archivedTopicGraph, err)
	}
	primaryAfterArchive, _ := s.GetEvidenceChain(ctx, primary.ID)
	primaryGraphAfterArchive, _ := s.GetEvidenceChainGraph(ctx, primary.ID)
	if primaryAfterArchive.Revision != primaryBeforeArchive.Revision ||
		primaryAfterArchive.GraphHash != primaryBeforeArchive.GraphHash ||
		len(primaryGraphAfterArchive.Nodes) != len(primaryGraph.Nodes) {
		t.Fatalf("archiving Topic changed Primary reference: before=%#v after=%#v graph=%#v", primaryBeforeArchive, primaryAfterArchive, primaryGraphAfterArchive)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM evidence_proposals WHERE target_chain_id = ?`, topic.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeEvidenceChain(ctx, topic.ID); err == nil || !hasEvidenceValidationCode(err, "MAP_STILL_REFERENCED") {
		t.Fatalf("purge referenced Topic err = %v, want MAP_STILL_REFERENCED", err)
	}
}

func TestEvidenceWorkspaceRejectsInvalidMapReferences(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_bad_ref")
	primary, _ := s.GetActivePrimaryEvidenceChain(ctx, project.ID)
	badData, _ := json.Marshal(EvidenceMapReference{
		TargetMapID: primary.ID, TargetRevision: 1, TargetGraphHash: "wrong",
	})
	proposal, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: primary.ID, Actor: "agent",
		Summary: "Invalid self reference.", ProjectLevelImpact: true,
	}, &EvidenceGraphPatch{
		ChainID: primary.ID,
		Nodes:   []EvidenceChainNode{{ID: "bad_ref", Type: EvidenceNodeMapRef, DataJSON: string(badData)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanEvidenceProposal(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Eligible || !hasEvidenceBlocker(plan.Blockers, "MAP_REFERENCE_CYCLE") {
		t.Fatalf("self reference plan = %#v", plan)
	}

	topicData, _ := json.Marshal(EvidenceMapReference{
		TargetMapID: primary.ID, TargetRevision: 1, TargetGraphHash: "wrong",
	})
	topicProposal, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent",
		Summary: "Topic cannot own a Map Reference.", RoutingReason: "Testing cycle protection.",
	}, &EvidenceGraphPatch{
		ChainID: topic.ID,
		Nodes:   []EvidenceChainNode{{ID: "topic_bad_ref", Type: EvidenceNodeMapRef, DataJSON: string(topicData)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	topicPlan, _ := s.PlanEvidenceProposal(ctx, topicProposal.ID)
	if topicPlan.Eligible || !hasEvidenceBlocker(topicPlan.Blockers, "MAP_REFERENCE_SCOPE_INVALID") {
		t.Fatalf("topic reference plan = %#v", topicPlan)
	}
}

func TestPurgeEvidenceChainSafetyRules(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, emptyTopic := createEvidenceWorkspaceProject(t, s, "project_purge_empty")
	primary, err := s.GetActivePrimaryEvidenceChain(ctx, project.ID)
	if err != nil || primary == nil {
		t.Fatalf("primary = %#v, err = %v", primary, err)
	}

	if err := s.PurgeEvidenceChain(ctx, primary.ID); err == nil || !hasEvidenceValidationCode(err, "PRIMARY_MAP_REQUIRED") {
		t.Fatalf("purge primary err = %v, want PRIMARY_MAP_REQUIRED", err)
	}
	if err := s.PurgeEvidenceChain(ctx, emptyTopic.ID); err != nil {
		t.Fatalf("purge empty Topic: %v", err)
	}
	if deleted, err := s.GetEvidenceChain(ctx, emptyTopic.ID); err != nil || deleted != nil {
		t.Fatalf("deleted Topic = %#v, err = %v", deleted, err)
	}

	historyTopic := &EvidenceChain{
		ID: "chain_topic_history", Title: "Topic with history",
		ProjectID: project.ID, Role: "secondary", Status: "active",
	}
	if err := s.CreateEvidenceChain(ctx, historyTopic); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveEvidenceChainGraphCAS(ctx, historyTopic.ID, EvidenceChainGraph{
		Nodes: []EvidenceChainNode{{ID: "history_claim", Type: EvidenceNodeClaim, Title: "A bounded claim"}},
	}, EvidenceGraphSaveOptions{ExpectedRevision: 0, Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeEvidenceChain(ctx, historyTopic.ID); err == nil || !hasEvidenceValidationCode(err, "ARCHIVE_REQUIRED") {
		t.Fatalf("purge active history err = %v, want ARCHIVE_REQUIRED", err)
	}
	if err := s.DeleteEvidenceChain(ctx, historyTopic.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeEvidenceChain(ctx, historyTopic.ID); err != nil {
		t.Fatalf("purge archived history: %v", err)
	}

	proposalTopic := &EvidenceChain{
		ID: "chain_topic_proposal", Title: "Topic with draft",
		ProjectID: project.ID, Role: "secondary", Status: "active",
	}
	if err := s.CreateEvidenceChain(ctx, proposalTopic); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: proposalTopic.ID, Actor: "agent", Summary: "A pending draft",
		RoutingReason: "This proposal belongs to the disposable Topic under test.",
	}, &EvidenceGraphPatch{
		ChainID: proposalTopic.ID,
		Nodes:   []EvidenceChainNode{{ID: "proposal_plan", Type: EvidenceNodePlan, Title: "Try the next experiment"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeEvidenceChain(ctx, proposalTopic.ID); err == nil || !hasEvidenceValidationCode(err, "MAP_HAS_PROPOSALS") {
		t.Fatalf("purge Topic with proposal err = %v, want MAP_HAS_PROPOSALS", err)
	}
}

func hasEvidenceValidationCode(err error, code string) bool {
	var validation *EvidenceGraphValidationError
	return errors.As(err, &validation) && validation.Code == code
}

func TestEvidencePromotionPlansWithoutSideEffectsAndCreatesIndependentPrimaryProposal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_promotion")
	topicProposal, err := s.CreateEvidenceProposal(ctx, &EvidenceProposal{
		ProjectID: project.ID, TargetChainID: topic.ID, Actor: "agent",
		Summary: "Accept Topic evidence.", RoutingReason: "This Topic owns the ablation conclusion.",
	}, &EvidenceGraphPatch{
		ChainID: topic.ID,
		Nodes:   []EvidenceChainNode{{ID: "topic_conclusion", Type: EvidenceNodeClaim, Title: "Module does not improve the baseline"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewEvidenceProposal(ctx, topicProposal.ID, "accept", "user"); err != nil {
		t.Fatal(err)
	}
	primary, _ := s.GetActivePrimaryEvidenceChain(ctx, project.ID)
	before := *primary
	request := EvidencePromotionRequest{
		SourceMapID: topic.ID, SourceNodeIDs: []string{"topic_conclusion"},
		Summary: "The tested module does not change the project baseline.", NodeType: EvidenceNodeClaim, Actor: "agent",
	}
	plan, err := s.PlanEvidencePromotion(ctx, request)
	if err != nil || !plan.Eligible || plan.PlanHash == "" || len(plan.Patch.Nodes) != 2 {
		t.Fatalf("promotion plan = %#v, err = %v", plan, err)
	}
	afterPlan, _ := s.GetEvidenceChain(ctx, primary.ID)
	if afterPlan.Revision != before.Revision || afterPlan.GraphHash != before.GraphHash {
		t.Fatalf("promotion plan mutated Primary: before=%#v after=%#v", before, afterPlan)
	}
	proposal, err := s.CreateEvidencePromotion(ctx, request, plan.PlanHash)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := s.CreateEvidencePromotion(ctx, request, plan.PlanHash)
	if err != nil || repeated.ID != proposal.ID {
		t.Fatalf("promotion idempotency: first=%#v repeated=%#v err=%v", proposal, repeated, err)
	}
	if proposal.Status != GraphProposalPending || !proposal.ProjectLevelImpact || proposal.SourceKind != "promotion" {
		t.Fatalf("promotion proposal = %#v", proposal)
	}
	afterCreate, _ := s.GetEvidenceChain(ctx, primary.ID)
	if afterCreate.Revision != before.Revision {
		t.Fatalf("creating promotion proposal mutated Primary: %#v", afterCreate)
	}
	if _, err := s.ReviewEvidenceProposal(ctx, proposal.ID, "accept", "user"); err != nil {
		t.Fatal(err)
	}
	acceptedPrimary, _ := s.GetEvidenceChain(ctx, primary.ID)
	if acceptedPrimary.Revision != before.Revision+1 {
		t.Fatalf("accepted Primary = %#v", acceptedPrimary)
	}
	unchangedTopic, _ := s.GetEvidenceChain(ctx, topic.ID)
	if unchangedTopic.Revision != plan.SourceRevision || unchangedTopic.GraphHash != plan.SourceGraphHash {
		t.Fatalf("promotion mutated Topic: %#v", unchangedTopic)
	}
}

func TestEvidencePromotionBlocksUnreadyFormalRunWithObjectContext(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_promotion_unready")
	if err := s.CreateResource(ctx, &Resource{
		ID: "rsrc_promotion_unready", Name: "promotion-unready", Type: "ssh",
		Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	run := &Run{
		ID: "run_promotion_unready", ResourceID: "rsrc_promotion_unready", ProjectID: project.ID,
		Name: "legacy formal run", Status: RunStatusSucceeded, Kind: RunKindFormal,
		EvidenceGrade: RunEvidenceGradeFormal, Command: "true",
	}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "promotion_unready_run", Type: EvidenceNodeRun, RunID: run.ID},
			{ID: "promotion_unready_claim", Type: EvidenceNodeClaim, Title: "Legacy claim"},
		},
		Edges: []EvidenceChainEdge{{
			ID: "promotion_unready_supports", Type: EvidenceEdgeSupports,
			SourceNodeID: "promotion_unready_run", TargetNodeID: "promotion_unready_claim",
		}},
	}
	if _, err := s.SaveEvidenceChainGraphCAS(ctx, topic.ID, graph, EvidenceGraphSaveOptions{
		ExpectedRevision: 0, Actor: "migration-test", SourceKind: "legacy", SourceID: run.ID,
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanEvidencePromotion(ctx, EvidencePromotionRequest{
		SourceMapID: topic.ID, SourceNodeIDs: []string{"promotion_unready_claim"},
		Summary: "Legacy claim summary", NodeType: EvidenceNodeClaim,
	})
	if err != nil {
		t.Fatal(err)
	}
	blocker := evidenceBlockerByCode(plan.Blockers, "DATASET_MISSING")
	if plan.Eligible || blocker == nil {
		t.Fatalf("unready promotion plan = %#v", plan)
	}
	if blocker.NodeID != "promotion_unready_run" || blocker.EdgeID != "promotion_unready_supports" || blocker.RunID != run.ID {
		t.Fatalf("unready blocker lost object context: %#v", blocker)
	}
	if evidenceBlockerByCode(plan.Blockers, "EVIDENCE_RELEASE_MISSING") == nil {
		t.Fatalf("unready promotion did not require a released snapshot: %#v", plan.Blockers)
	}
}

func TestEvidencePromotionRequiresReleasedSnapshot(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_promotion_release")
	run, snapshot := createReadyPromotionRun(t, s, project.ID, "release")
	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "promotion_release_run", Type: EvidenceNodeRun, RunID: run.ID},
			{ID: "promotion_release_claim", Type: EvidenceNodeClaim, Title: "Released claim"},
		},
		Edges: []EvidenceChainEdge{{
			ID: "promotion_release_supports", Type: EvidenceEdgeSupports,
			SourceNodeID: "promotion_release_run", TargetNodeID: "promotion_release_claim",
		}},
	}
	if _, err := s.SaveEvidenceChainGraphCAS(ctx, topic.ID, graph, EvidenceGraphSaveOptions{
		ExpectedRevision: 0, Actor: "test", SourceKind: "formal-run", SourceID: run.ID,
	}); err != nil {
		t.Fatal(err)
	}
	request := EvidencePromotionRequest{
		SourceMapID: topic.ID, SourceNodeIDs: []string{"promotion_release_claim"},
		Summary: "Released formal claim", NodeType: EvidenceNodeClaim,
	}
	blocked, err := s.PlanEvidencePromotion(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	releaseBlocker := evidenceBlockerByCode(blocked.Blockers, "EVIDENCE_RELEASE_MISSING")
	if blocked.Eligible || releaseBlocker == nil {
		t.Fatalf("unreleased promotion plan = %#v", blocked)
	}
	if releaseBlocker.NodeID != "promotion_release_run" ||
		releaseBlocker.EdgeID != "promotion_release_supports" ||
		releaseBlocker.RunID != run.ID {
		t.Fatalf("release blocker lost object context: %#v", releaseBlocker)
	}
	if err := s.AppendEvidenceRelease(ctx, &EvidenceRelease{
		SnapshotID: snapshot.ID, State: EvidenceReleaseReleased,
		AggregateResultJSON: `{}`, GateResultJSON: `{"passed":true}`,
	}); err != nil {
		t.Fatal(err)
	}
	eligible, err := s.PlanEvidencePromotion(ctx, request)
	if err != nil || !eligible.Eligible || len(eligible.Blockers) != 0 {
		t.Fatalf("released promotion plan = %#v, err = %v", eligible, err)
	}
}

func TestEvidencePromotionRejectsCrossProjectRun(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, topic := createEvidenceWorkspaceProject(t, s, "project_promotion_owner")
	other, _ := createEvidenceWorkspaceProject(t, s, "project_promotion_other")
	if err := s.CreateResource(ctx, &Resource{
		ID: "rsrc_promotion_cross", Name: "promotion-cross", Type: "ssh",
		Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	run := &Run{
		ID: "run_promotion_cross", ResourceID: "rsrc_promotion_cross", ProjectID: project.ID,
		Status: RunStatusSucceeded, Kind: RunKindFormal, EvidenceGrade: RunEvidenceGradeFormal, Command: "true",
	}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "promotion_cross_run", Type: EvidenceNodeRun, RunID: run.ID},
			{ID: "promotion_cross_claim", Type: EvidenceNodeClaim, Title: "Cross-project claim"},
		},
		Edges: []EvidenceChainEdge{{
			ID: "promotion_cross_supports", Type: EvidenceEdgeSupports,
			SourceNodeID: "promotion_cross_run", TargetNodeID: "promotion_cross_claim",
		}},
	}
	if _, err := s.SaveEvidenceChainGraphCAS(ctx, topic.ID, graph, EvidenceGraphSaveOptions{
		ExpectedRevision: 0, Actor: "migration-test", SourceKind: "legacy", SourceID: run.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE runs SET project_id=? WHERE id=?`, other.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanEvidencePromotion(ctx, EvidencePromotionRequest{
		SourceMapID: topic.ID, SourceNodeIDs: []string{"promotion_cross_claim"},
		Summary: "Cross-project summary", NodeType: EvidenceNodeClaim,
	})
	if err != nil {
		t.Fatal(err)
	}
	blocker := evidenceBlockerByCode(plan.Blockers, "RUN_PROJECT_MISMATCH")
	if plan.Eligible || blocker == nil {
		t.Fatalf("cross-project promotion plan = %#v", plan)
	}
	if blocker.NodeID != "promotion_cross_run" || blocker.EdgeID != "promotion_cross_supports" || blocker.RunID != run.ID {
		t.Fatalf("cross-project blocker lost object context: %#v", blocker)
	}
	if blocker.Message == "" || project.ID == other.ID {
		t.Fatalf("unexpected cross-project test setup: blocker=%#v owner=%q other=%q", blocker, project.ID, other.ID)
	}
}

func createReadyPromotionRun(t *testing.T, s *SQLite, projectID, suffix string) (*Run, *EvidenceSnapshot) {
	t.Helper()
	ctx := context.Background()
	resourceID := "rsrc_promotion_" + suffix
	storageID := "storage_promotion_" + suffix
	datasetID := "dataset_promotion_" + suffix
	if err := s.CreateResource(ctx, &Resource{
		ID: resourceID, Name: resourceID, Type: "ssh", Host: "localhost",
		RootDir: "/tmp", Status: ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveStorageTarget(ctx, &StorageTarget{
		ID: storageID, Name: storageID, ResourceID: resourceID, RootPath: "/tmp/" + storageID,
	}); err != nil {
		t.Fatal(err)
	}
	dataset := &DatasetVersion{
		ID: datasetID, DatasetID: "promotion-data-" + suffix, Version: "v1",
		StorageTargetID: storageID, StoragePath: "datasets/promotion/v1",
		LogicalURI: "storage://" + storageID + "/datasets/promotion/v1",
		Revision:   "sha256:dataset-" + suffix, ManifestSHA256: "sha256:dataset-" + suffix,
		State: DatasetStateVerified,
	}
	if _, _, err := s.CreateDatasetVersionImmutable(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	run := &Run{
		ID: "run_promotion_" + suffix, ResourceID: resourceID, ProjectID: projectID,
		Name: "ready promotion run", Status: RunStatusSucceeded, Kind: RunKindFormal,
		EvidenceGrade: RunEvidenceGradeFormal, Command: "python train.py",
		DatasetsJSON: fmt.Sprintf(
			`[{"id":%q,"dataset_id":%q,"version":"v1","manifest_sha256":%q}]`,
			dataset.ID, dataset.DatasetID, dataset.ManifestSHA256,
		),
		SeedsJSON: `[41,42,43]`, ProjectConfigSHA256: "sha256:config-" + suffix,
		GitCommit: "0123456789abcdef", SplitProtocol: "split-v1", EvaluationProtocol: "eval-v1",
		DataFinalizationState: RunDataFinalizationCompleted,
	}
	if err := s.CreateRunWithBindings(ctx, run, RunBindings{Outputs: []RunOutputBinding{{
		SourcePattern: "results/metrics.json",
		LogicalURI:    "aexp://" + projectID + "/runs/" + run.ID + "/metrics.json",
		Role:          "metrics", Required: true, Revision: "sha256:metrics-" + suffix,
		State: RunBindingPublished, PublishedAt: &now,
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRunManifest(ctx, &RunManifest{
		RunID: run.ID, SchemaVersion: 1, State: RunManifestFinal,
		ManifestJSON: `{"status":"succeeded"}`, SHA256: "sha256:manifest-" + suffix,
		Completeness: RunManifestCompletenessCurrent, FinalizedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, created, err := s.CreateEvidenceSnapshot(ctx, run.ID)
	if err != nil || !created {
		t.Fatalf("CreateEvidenceSnapshot = %#v, created=%v, err=%v", snapshot, created, err)
	}
	return run, snapshot
}

func evidenceBlockerByCode(blockers []EvidenceGraphBlocker, code string) *EvidenceGraphBlocker {
	for index := range blockers {
		if blockers[index].Code == code {
			return &blockers[index]
		}
	}
	return nil
}
