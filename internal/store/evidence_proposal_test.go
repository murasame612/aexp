package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestEvidenceGraphProposalAutoResolvesProjectPrimaryMap(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_auto_map", Name: "auto-map", Type: "ssh", Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	project := &ProjectDefinition{ID: "project_auto_map", Name: "Auto Map"}
	if err := s.CreateProjectDefinition(ctx, project); err != nil {
		t.Fatalf("CreateProjectDefinition: %v", err)
	}
	primary, err := s.GetActivePrimaryEvidenceChain(ctx, project.ID)
	if err != nil || primary == nil {
		t.Fatalf("primary = %#v, err = %v", primary, err)
	}
	if primary.ProjectID != project.ID || primary.Role != "primary" || primary.Status != "active" {
		t.Fatalf("unexpected primary map: %#v", primary)
	}
	ensured, err := s.EnsureProjectPrimaryEvidenceChain(ctx, project.ID)
	if err != nil {
		t.Fatalf("EnsureProjectPrimaryEvidenceChain: %v", err)
	}
	if ensured.ID != primary.ID {
		t.Fatalf("ensure returned %q, want %q", ensured.ID, primary.ID)
	}
	run := &Run{
		ID:         "run_auto_map",
		ResourceID: "rsrc_auto_map",
		ProjectID:  project.ID,
		Name:       "auto-map evidence",
		Status:     RunStatusSucceeded,
		Kind:       RunKindPilot,
		Command:    "true",
	}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	card := &ProjectRunCard{RunID: run.ID, Question: "Does the Agent need a map id?"}
	patch := &EvidenceGraphPatch{
		Nodes: []EvidenceChainNode{{ID: "issue_auto_map", Type: EvidenceNodeIssue, Title: "Map target was implicit"}},
	}
	saved, err := s.SubmitEvidenceGraphProposal(ctx, card, patch)
	if err != nil {
		t.Fatalf("SubmitEvidenceGraphProposal: %v", err)
	}
	if saved.ProjectID != project.ID || saved.ProjectName != project.Name {
		t.Fatalf("canonical project was not applied: %#v", saved)
	}
	var persisted EvidenceGraphPatch
	if err := json.Unmarshal([]byte(saved.GraphPatchJSON), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.ChainID != primary.ID {
		t.Fatalf("resolved chain = %q, want %q", persisted.ChainID, primary.ID)
	}
	if saved.BaseGraphRevision != primary.Revision {
		t.Fatalf("base revision = %d, want %d", saved.BaseGraphRevision, primary.Revision)
	}
	chains, err := s.ListEvidenceChains(ctx, EvidenceChainFilter{ProjectID: project.ID, Role: "primary", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if len(chains) != 1 {
		t.Fatalf("primary chains = %d, want 1", len(chains))
	}

	topic := &EvidenceChain{
		ID:          "chain_auto_map_topic",
		Title:       "Protocol topic",
		Description: "Protocol changes and validators.",
		RoutingHints: EvidenceGraphRoutingHints{
			Recipes:  []string{"formal-vis"},
			Keywords: []string{"paired-validator"},
		},
		ProjectID: project.ID,
		Role:      "secondary",
		Status:    "active",
	}
	if err := s.CreateEvidenceChain(ctx, topic); err != nil {
		t.Fatalf("CreateEvidenceChain topic: %v", err)
	}
	topicRun := &Run{
		ID:         "run_auto_map_topic",
		ResourceID: "rsrc_auto_map",
		ProjectID:  project.ID,
		Name:       "topic evidence",
		Status:     RunStatusSucceeded,
		Kind:       RunKindPilot,
		Command:    "true",
	}
	if err := s.CreateRun(ctx, topicRun); err != nil {
		t.Fatal(err)
	}
	topicCard := &ProjectRunCard{RunID: topicRun.ID, Question: "Does this belong in the protocol topic?"}
	topicPatch := &EvidenceGraphPatch{
		ChainID: topic.ID,
		Nodes:   []EvidenceChainNode{{ID: "issue_topic_route", Type: EvidenceNodeIssue, Title: "Protocol issue"}},
	}
	if _, err := s.SubmitEvidenceGraphProposal(ctx, topicCard, topicPatch); err == nil {
		t.Fatal("topic proposal without routing reason unexpectedly succeeded")
	} else {
		var validationErr *EvidenceGraphValidationError
		if !errors.As(err, &validationErr) || validationErr.Code != "ROUTING_REASON_REQUIRED" {
			t.Fatalf("topic proposal without routing reason error = %#v", err)
		}
	}
	topicPatch.RoutingReason = "Recipe formal-vis and paired-validator match this topic."
	topicSaved, err := s.SubmitEvidenceGraphProposal(ctx, topicCard, topicPatch)
	if err != nil {
		t.Fatalf("SubmitEvidenceGraphProposal topic: %v", err)
	}
	if topicSaved.GraphRoutingReason != topicPatch.RoutingReason {
		t.Fatalf("proposal routing reason = %q, want %q", topicSaved.GraphRoutingReason, topicPatch.RoutingReason)
	}
	topicPlan, err := s.PlanEvidenceGraphProposal(ctx, topicRun.ID)
	if err != nil {
		t.Fatalf("PlanEvidenceGraphProposal topic: %v", err)
	}
	if topicPlan.RoutingReason != topicPatch.RoutingReason || topicPlan.ChainID != topic.ID {
		t.Fatalf("topic plan routing = %#v", topicPlan)
	}
}

func TestSubmitEvidenceGraphProposalPreservesRunInterpretation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, _ := createEvidenceWorkspaceProject(t, s, "project_card_domains")
	primary, err := s.GetActivePrimaryEvidenceChain(ctx, project.ID)
	if err != nil || primary == nil {
		t.Fatalf("primary=%#v err=%v", primary, err)
	}
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_card_domains", Name: "card-domains", Type: "ssh", Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: "run_card_domains", ResourceID: "rsrc_card_domains", ProjectID: project.ID, Status: RunStatusSucceeded, Kind: RunKindPilot, Command: "true"}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	interpretation := &ProjectRunCard{
		RunID: run.ID, ProjectID: project.ID, ProjectName: project.Name,
		Question: "Does the treatment help?", Verdict: "Pilot suggests a gain.",
		EvidenceLevel: "C", KeyMetrics: "score=0.7", ArtifactPaths: "results.json",
		SupportsClaim: "candidate gain", WeakensClaim: "none", NextAction: "formal rerun",
		Important: true, ShouldPromote: true, ProposalReason: "decision-changing",
		RelatedRuns: "run_baseline",
	}
	if err := s.SaveProjectRunCard(ctx, interpretation); err != nil {
		t.Fatal(err)
	}
	_, err = s.SubmitEvidenceGraphProposal(ctx, &ProjectRunCard{RunID: run.ID}, &EvidenceGraphPatch{
		ChainID: primary.ID,
		Nodes:   []EvidenceChainNode{{ID: "issue_card_domains", Type: EvidenceNodeIssue, Title: "Pilot issue"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetProjectRunCard(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Question != interpretation.Question || stored.Verdict != interpretation.Verdict ||
		stored.EvidenceLevel != interpretation.EvidenceLevel || stored.KeyMetrics != interpretation.KeyMetrics ||
		stored.ArtifactPaths != interpretation.ArtifactPaths || stored.SupportsClaim != interpretation.SupportsClaim ||
		stored.WeakensClaim != interpretation.WeakensClaim || stored.NextAction != interpretation.NextAction ||
		stored.Important != interpretation.Important || stored.ShouldPromote != interpretation.ShouldPromote ||
		stored.ProposalReason != interpretation.ProposalReason || stored.RelatedRuns != interpretation.RelatedRuns {
		t.Fatalf("proposal overwrote interpretation: %#v", stored)
	}
	if stored.GraphStatus != GraphProposalPending || stored.GraphPatchJSON == "" {
		t.Fatalf("proposal was not persisted: %#v", stored)
	}
}

func TestSaveProjectRunCardPreservesPendingGraphProposal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project, _ := createEvidenceWorkspaceProject(t, s, "project_card_reverse")
	primary, err := s.GetActivePrimaryEvidenceChain(ctx, project.ID)
	if err != nil || primary == nil {
		t.Fatalf("primary=%#v err=%v", primary, err)
	}
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_card_reverse", Name: "card-reverse", Type: "ssh", Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: "run_card_reverse", ResourceID: "rsrc_card_reverse", ProjectID: project.ID, Status: RunStatusSucceeded, Kind: RunKindPilot, Command: "true"}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	proposed, err := s.SubmitEvidenceGraphProposal(ctx, &ProjectRunCard{RunID: run.ID}, &EvidenceGraphPatch{
		ChainID: primary.ID,
		Nodes:   []EvidenceChainNode{{ID: "issue_card_reverse", Type: EvidenceNodeIssue, Title: "Keep proposal"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposed.Question = "Updated question"
	proposed.Verdict = "Updated verdict"
	if err := s.SaveProjectRunCard(ctx, proposed); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetProjectRunCard(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.GraphStatus != proposed.GraphStatus || stored.ProposalHash != proposed.ProposalHash ||
		stored.GraphPatchJSON != proposed.GraphPatchJSON || stored.BaseGraphRevision != proposed.BaseGraphRevision {
		t.Fatalf("interpretation overwrote pending proposal: before=%#v after=%#v", proposed, stored)
	}
	if stored.Verdict != "Updated verdict" {
		t.Fatalf("interpretation was not updated: %#v", stored)
	}
}

func TestEvidenceGraphProposalLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateProjectDefinition(ctx, &ProjectDefinition{ID: "project_graph", Name: "Graph Project"}); err != nil {
		t.Fatalf("CreateProjectDefinition: %v", err)
	}
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_proposal", Name: "proposal", Type: "ssh", Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle}); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	if err := s.SaveStorageTarget(ctx, &StorageTarget{ID: "storage_proposal", Name: "proposal-storage", ResourceID: "rsrc_proposal", RootPath: "/tmp/storage"}); err != nil {
		t.Fatalf("SaveStorageTarget: %v", err)
	}
	dataset := &DatasetVersion{
		ID: "dataset_proposal_v1", DatasetID: "proposal-data", Version: "v1", StorageTargetID: "storage_proposal",
		StoragePath: "datasets/proposal-data/v1", LogicalURI: "storage://proposal-storage/datasets/proposal-data/v1",
		Revision: "sha256:dataset", ManifestSHA256: "sha256:dataset", State: DatasetStateRegistered,
	}
	if _, _, err := s.CreateDatasetVersionImmutable(ctx, dataset); err != nil {
		t.Fatalf("CreateDatasetVersionImmutable: %v", err)
	}
	run := &Run{
		ID:                    "run_proposal",
		ResourceID:            "rsrc_proposal",
		ProjectID:             "project_graph",
		Name:                  "formal evidence",
		Status:                RunStatusSucceeded,
		Kind:                  RunKindFormal,
		EvidenceGrade:         "formal",
		Command:               "python train.py",
		DatasetsJSON:          `[{"id":"dataset_proposal_v1","dataset_id":"proposal-data","version":"v1","manifest_sha256":"sha256:dataset"}]`,
		SeedsJSON:             `[41,42,43]`,
		ProjectConfigSHA256:   "config-hash",
		GitCommit:             "0123456789abcdef",
		SplitProtocol:         "paired-clean-v1",
		EvaluationProtocol:    "rgb-validator-v2",
		DataFinalizationState: RunDataFinalizationCompleted,
	}
	now := time.Now()
	if err := s.CreateRunWithBindings(ctx, run, RunBindings{Outputs: []RunOutputBinding{{
		SourcePattern: "results/metrics.json",
		LogicalURI:    "aexp://project_graph/runs/run_proposal/metrics.json",
		Role:          "metrics",
		Required:      true,
		Revision:      "sha256:metrics",
		State:         RunBindingPublished,
		PublishedAt:   &now,
	}}}); err != nil {
		t.Fatalf("CreateRunWithBindings: %v", err)
	}
	if err := s.SaveRunManifest(ctx, &RunManifest{
		RunID: run.ID, SchemaVersion: 1, State: RunManifestFinal,
		ManifestJSON: `{"status":"succeeded"}`, SHA256: "sha256:run-proposal",
		Completeness: RunManifestCompletenessCurrent, FinalizedAt: &now,
	}); err != nil {
		t.Fatalf("SaveRunManifest: %v", err)
	}
	snapshot, _, err := s.CreateEvidenceSnapshot(ctx, run.ID)
	if err != nil {
		t.Fatalf("CreateEvidenceSnapshot: %v", err)
	}
	primary, err := s.GetActivePrimaryEvidenceChain(ctx, "project_graph")
	if err != nil || primary == nil {
		t.Fatalf("GetActivePrimaryEvidenceChain: primary=%#v err=%v", primary, err)
	}
	card := &ProjectRunCard{
		ID:                "card_proposal",
		ProjectID:         "project_graph",
		ProjectName:       "Graph Project",
		RunID:             run.ID,
		Question:          "Does it improve?",
		Verdict:           "Supports the bounded claim.",
		EvidenceLevel:     "A",
		BaseGraphRevision: 0,
	}
	patch := &EvidenceGraphPatch{
		ChainID: primary.ID,
		Nodes: []EvidenceChainNode{
			{ID: "node_run_proposal", Type: EvidenceNodeRun, RunID: run.ID, ProjectCardID: card.ID, X: 900, Pinned: true},
			{ID: "claim_proposal", Type: EvidenceNodeClaim, Title: "Bounded claim"},
		},
		Edges: []EvidenceChainEdge{
			{ID: "edge_proposal", Type: EvidenceEdgeSupports, SourceNodeID: "node_run_proposal", TargetNodeID: "claim_proposal"},
		},
	}
	saved, err := s.SubmitEvidenceGraphProposal(ctx, card, patch)
	if err != nil {
		t.Fatalf("SubmitEvidenceGraphProposal: %v", err)
	}
	if saved.GraphStatus != GraphProposalPending || saved.ProposalHash == "" {
		t.Fatalf("saved proposal = %#v", saved)
	}
	repeated, err := s.SubmitEvidenceGraphProposal(ctx, card, patch)
	if err != nil {
		t.Fatalf("idempotent SubmitEvidenceGraphProposal: %v", err)
	}
	if repeated.ProposalHash != saved.ProposalHash {
		t.Fatalf("proposal hash changed: %q != %q", repeated.ProposalHash, saved.ProposalHash)
	}
	blockedPlan, err := s.PlanEvidenceGraphProposal(ctx, run.ID)
	if err != nil {
		t.Fatalf("PlanEvidenceGraphProposal registered dataset: %v", err)
	}
	if blockedPlan.Eligible || !hasEvidenceBlocker(blockedPlan.Blockers, "DATASET_NOT_VERIFIED") {
		t.Fatalf("registered dataset plan = %#v", blockedPlan)
	}
	verifiedDataset := *dataset
	verifiedDataset.State = DatasetStateVerified
	if _, _, err := s.CreateDatasetVersionImmutable(ctx, &verifiedDataset); err != nil {
		t.Fatalf("promote verified dataset: %v", err)
	}
	unreleasedPlan, err := s.PlanEvidenceGraphProposal(ctx, run.ID)
	if err != nil {
		t.Fatalf("PlanEvidenceGraphProposal unreleased snapshot: %v", err)
	}
	if unreleasedPlan.Eligible || !hasEvidenceBlocker(unreleasedPlan.Blockers, "EVIDENCE_RELEASE_MISSING") {
		t.Fatalf("unreleased snapshot plan = %#v", unreleasedPlan)
	}
	if err := s.AppendEvidenceRelease(ctx, &EvidenceRelease{
		SnapshotID: snapshot.ID, State: EvidenceReleaseReleased,
		AggregateResultJSON: `{}`, GateResultJSON: `{"passed":true}`,
	}); err != nil {
		t.Fatalf("AppendEvidenceRelease: %v", err)
	}
	before, err := s.GetEvidenceChain(ctx, primary.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanEvidenceGraphProposal(ctx, run.ID)
	if err != nil {
		t.Fatalf("PlanEvidenceGraphProposal: %v", err)
	}
	if !plan.Eligible || len(plan.Blockers) != 0 || plan.ResultGraphHash == "" {
		t.Fatalf("plan = %#v", plan)
	}
	afterPlan, _ := s.GetEvidenceChain(ctx, primary.ID)
	if afterPlan.Revision != before.Revision || afterPlan.GraphHash != before.GraphHash {
		t.Fatalf("plan had side effects: before=%#v after=%#v", before, afterPlan)
	}
	accepted, err := s.ReviewEvidenceGraphProposal(ctx, run.ID, "accept", "reviewer")
	if err != nil {
		t.Fatalf("ReviewEvidenceGraphProposal accept: %v", err)
	}
	if accepted.GraphStatus != GraphProposalAccepted || accepted.ReviewedAt == nil {
		t.Fatalf("accepted card = %#v", accepted)
	}
	chain, _ := s.GetEvidenceChain(ctx, primary.ID)
	if chain.Revision != 1 || chain.GraphHash != plan.ResultGraphHash {
		t.Fatalf("accepted chain = %#v plan=%#v", chain, plan)
	}
	graph, _ := s.GetEvidenceChainGraph(ctx, primary.ID)
	if len(graph.Nodes) != 2 || len(graph.Edges) != 1 {
		t.Fatalf("accepted graph = %#v", graph)
	}
	for _, node := range graph.Nodes {
		if node.Pinned || node.X != 0 || node.Y != 0 {
			t.Fatalf("agent proposal persisted authoritative layout: %#v", node)
		}
	}
	if _, err := s.ReviewEvidenceGraphProposal(ctx, run.ID, "accept", "reviewer"); err == nil {
		t.Fatal("accepted proposal was accepted twice")
	}
}

func hasEvidenceBlocker(blockers []EvidenceGraphBlocker, code string) bool {
	for _, blocker := range blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}

func TestEvidenceGraphProposalNoImpactRequiresReason(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createTestProject(t, s, "project_noimpact", "No Impact Project")
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_noimpact", Name: "noimpact", Type: "ssh", Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_noimpact", ResourceID: "rsrc_noimpact", Status: RunStatusSucceeded, Kind: RunKindPilot, Command: "true"}); err != nil {
		t.Fatal(err)
	}
	card := &ProjectRunCard{
		RunID:         "run_noimpact",
		ProjectID:     "project_noimpact",
		ProjectName:   "No Impact Project",
		NoGraphImpact: true,
	}
	if _, err := s.SubmitEvidenceGraphProposal(ctx, card, nil); err == nil {
		t.Fatal("no-impact proposal without reason succeeded")
	}
	card.GraphImpactReason = "Routine diagnostic; it did not change a research decision."
	saved, err := s.SubmitEvidenceGraphProposal(ctx, card, nil)
	if err != nil {
		t.Fatalf("SubmitEvidenceGraphProposal no impact: %v", err)
	}
	if !saved.NoGraphImpact || saved.GraphStatus != GraphProposalNone {
		t.Fatalf("saved no-impact card = %#v", saved)
	}
}
