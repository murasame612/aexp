package store

import (
	"fmt"
	"reflect"
	"testing"
)

func TestBuildEvidenceResearchProjectionStableParallelThreads(t *testing.T) {
	chain := EvidenceChain{ID: "chain_test", Revision: 7, GraphHash: "hash-r7"}
	nodes := []EvidenceChainNode{
		{ID: "h1", Type: EvidenceNodeClaim, Title: "Hypothesis A", DataJSON: `{"claimKind":"hypothesis"}`},
		{ID: "p1", Type: EvidenceNodePlan, Title: "Plan A", DataJSON: `{}`},
		{ID: "r1", Type: EvidenceNodeClaim, Title: "Result A", DataJSON: `{"claimKind":"result"}`},
		{ID: "i1", Type: EvidenceNodeIssue, Title: "Issue A", DataJSON: `{}`},
		{ID: "h2", Type: EvidenceNodeHypothesis, Title: "Hypothesis B", DataJSON: `{}`},
		{ID: "p2", Type: EvidenceNodePlan, Title: "Plan B", DataJSON: `{}`},
		{ID: "legacy", Type: EvidenceNodeIssue, Title: "Legacy fragment", DataJSON: `{}`},
	}
	edges := []EvidenceChainEdge{
		{ID: "e1", SourceNodeID: "h1", TargetNodeID: "p1", Type: EvidenceEdgeNextStep},
		{ID: "e2", SourceNodeID: "p1", TargetNodeID: "r1", Type: EvidenceEdgeNextStep},
		{ID: "e3", SourceNodeID: "r1", TargetNodeID: "i1", Type: EvidenceEdgeRevealsIssue},
		{ID: "e4", SourceNodeID: "i1", TargetNodeID: "h2", Type: EvidenceEdgeNextStep},
		{ID: "e5", SourceNodeID: "h2", TargetNodeID: "p2", Type: EvidenceEdgeNextStep},
	}
	forward := BuildEvidenceResearchProjection(chain, EvidenceChainGraph{Nodes: nodes, Edges: edges})
	if forward.ContractVersion != EvidenceResearchContractVersion {
		t.Fatalf("contract version = %q", forward.ContractVersion)
	}
	reverseNodes := append([]EvidenceChainNode(nil), nodes...)
	reverseEdges := append([]EvidenceChainEdge(nil), edges...)
	for left, right := 0, len(reverseNodes)-1; left < right; left, right = left+1, right-1 {
		reverseNodes[left], reverseNodes[right] = reverseNodes[right], reverseNodes[left]
	}
	for left, right := 0, len(reverseEdges)-1; left < right; left, right = left+1, right-1 {
		reverseEdges[left], reverseEdges[right] = reverseEdges[right], reverseEdges[left]
	}
	reversed := BuildEvidenceResearchProjection(chain, EvidenceChainGraph{Nodes: reverseNodes, Edges: reverseEdges})
	if !reflect.DeepEqual(forward, reversed) {
		t.Fatalf("projection changed with input order:\nforward=%#v\nreversed=%#v", forward, reversed)
	}
	if len(forward.Threads) != 2 || forward.Threads[0].RootNodeID != "h1" || forward.Threads[1].RootNodeID != "h2" {
		t.Fatalf("threads = %#v", forward.Threads)
	}
	if forward.Threads[1].ParentThreadID != forward.Threads[0].ID {
		t.Fatalf("child parent = %q, want %q", forward.Threads[1].ParentThreadID, forward.Threads[0].ID)
	}
	if len(forward.CrossThreadRelations) != 1 || forward.CrossThreadRelations[0].Kind != "branch" {
		t.Fatalf("cross relations = %#v", forward.CrossThreadRelations)
	}
	branch := forward.CrossThreadRelations[0]
	if branch.Edge.ID != "e4" || branch.Edge.SourceNodeID != "i1" || branch.Edge.TargetNodeID != "h2" || branch.SourceThreadID != forward.Threads[0].ID || branch.TargetThreadID != forward.Threads[1].ID {
		t.Fatalf("branch relation lost its navigation identity: %#v", branch)
	}
	if len(forward.Unassigned) != 1 || forward.Unassigned[0].Card.Node.ID != "legacy" || forward.Unassigned[0].Reason != "missing_hypothesis" {
		t.Fatalf("unassigned = %#v", forward.Unassigned)
	}
}

func TestBuildEvidenceResearchProjectionOnlyNextStepStartsChildThread(t *testing.T) {
	chain := EvidenceChain{ID: "chain_non_branch", Revision: 1, GraphHash: "hash-r1"}
	projection := BuildEvidenceResearchProjection(chain, EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "h1", Type: EvidenceNodeHypothesis, Title: "Parent", DataJSON: `{}`},
			{ID: "i1", Type: EvidenceNodeIssue, Title: "Issue", DataJSON: `{}`},
			{ID: "h2", Type: EvidenceNodeHypothesis, Title: "Independent", DataJSON: `{}`},
		},
		Edges: []EvidenceChainEdge{
			{ID: "e1", SourceNodeID: "h1", TargetNodeID: "i1", Type: EvidenceEdgeRevealsIssue},
			{ID: "e2", SourceNodeID: "i1", TargetNodeID: "h2", Type: EvidenceEdgeSupports},
		},
	})
	if len(projection.Threads) != 2 {
		t.Fatalf("threads = %#v", projection.Threads)
	}
	for _, thread := range projection.Threads {
		if thread.ParentThreadID != "" {
			t.Fatalf("non-next-step relation created child thread: %#v", thread)
		}
	}
	if len(projection.CrossThreadRelations) != 1 || projection.CrossThreadRelations[0].Kind != "causal" {
		t.Fatalf("cross relations = %#v", projection.CrossThreadRelations)
	}
}

func TestBuildEvidenceResearchProjectionConclusionMayStartChildThread(t *testing.T) {
	projection := BuildEvidenceResearchProjection(EvidenceChain{ID: "chain_positive_branch"}, EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "h1", Type: EvidenceNodeHypothesis, Title: "Parent", DataJSON: `{}`},
			{ID: "c1", Type: EvidenceNodeConclusion, Title: "Supported mechanism", DataJSON: `{}`},
			{ID: "h2", Type: EvidenceNodeHypothesis, Title: "Mechanism extension", DataJSON: `{}`},
		},
		Edges: []EvidenceChainEdge{
			{ID: "e1", SourceNodeID: "h1", TargetNodeID: "c1", Type: EvidenceEdgeSupports},
			{ID: "e2", SourceNodeID: "c1", TargetNodeID: "h2", Type: EvidenceEdgeNextStep},
		},
	})
	if len(projection.Threads) != 2 || projection.Threads[1].ParentThreadID != projection.Threads[0].ID {
		t.Fatalf("positive child threads = %#v", projection.Threads)
	}
	if len(projection.CrossThreadRelations) != 1 || projection.CrossThreadRelations[0].Kind != "branch" {
		t.Fatalf("positive branch relation = %#v", projection.CrossThreadRelations)
	}
}

func TestBuildEvidenceResearchProjectionExposesResultInterpretationsWithoutChangingCapacity(t *testing.T) {
	projection := BuildEvidenceResearchProjection(EvidenceChain{ID: "chain_interpretations"}, EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "h", Type: EvidenceNodeHypothesis, Title: "Hypothesis", DataJSON: `{}`},
			{ID: "result-mixed", Type: EvidenceNodeClaim, Title: "Mixed result", DataJSON: `{"claimKind":"result","resultDisposition":"mixed","dispositionReason":"one endpoint remains unstable"}`},
			{ID: "result-pending", Type: EvidenceNodeClaim, Title: "Pending result", DataJSON: `{"claimKind":"result","resultDisposition":"pending","dispositionReason":"awaiting matched baseline"}`},
			{ID: "conclusion", Type: EvidenceNodeConclusion, Title: "Mechanism supported", DataJSON: `{}`},
			{ID: "issue", Type: EvidenceNodeIssue, Title: "Endpoint unstable", DataJSON: `{}`},
		},
		Edges: []EvidenceChainEdge{
			{ID: "h-result", SourceNodeID: "h", TargetNodeID: "result-mixed", Type: EvidenceEdgeNextStep},
			{ID: "result-conclusion", SourceNodeID: "result-mixed", TargetNodeID: "conclusion", Type: EvidenceEdgeSupports, Rationale: "matched seeds agree"},
			{ID: "result-issue", SourceNodeID: "result-mixed", TargetNodeID: "issue", Type: EvidenceEdgeRevealsIssue},
			{ID: "h-pending", SourceNodeID: "h", TargetNodeID: "result-pending", Type: EvidenceEdgeNextStep},
		},
	})
	if !reflect.DeepEqual(projection.PresentationStages, []string{"hypothesis", "design", "result", "interpretation", "outcome"}) {
		t.Fatalf("presentation stages = %#v", projection.PresentationStages)
	}
	if len(projection.Threads) != 1 || len(projection.Threads[0].Interpretations) != 3 {
		t.Fatalf("interpretations = %#v", projection.Threads)
	}
	labels := map[string]bool{}
	for _, interpretation := range projection.Threads[0].Interpretations {
		labels[interpretation.Label] = true
	}
	if !labels["证据支持结论"] || !labels["同时暴露限制"] || !labels["待解释"] {
		t.Fatalf("interpretation labels = %#v", labels)
	}
	if projection.Capacity.ThreadNodeCount != 5 {
		t.Fatalf("read-only interpretations changed capacity: %#v", projection.Capacity)
	}
}

func TestBuildEvidenceResearchProjectionReportsTopicCapacityAndFamilies(t *testing.T) {
	nodes := []EvidenceChainNode{
		{ID: "h1", Type: EvidenceNodeHypothesis, Title: "Family A", DataJSON: `{}`},
		{ID: "i1", Type: EvidenceNodeIssue, Title: "Issue A", DataJSON: `{}`},
		{ID: "h2", Type: EvidenceNodeHypothesis, Title: "Child A", DataJSON: `{}`},
		{ID: "h3", Type: EvidenceNodeHypothesis, Title: "Family B", DataJSON: `{}`},
		{ID: "c3", Type: EvidenceNodeConclusion, Title: "Conclusion B", DataJSON: `{}`},
		{ID: "h4", Type: EvidenceNodeHypothesis, Title: "Child B", DataJSON: `{}`},
		{ID: "h5", Type: EvidenceNodeHypothesis, Title: "Family C", DataJSON: `{}`},
	}
	edges := []EvidenceChainEdge{
		{ID: "a1", SourceNodeID: "h1", TargetNodeID: "i1", Type: EvidenceEdgeRevealsIssue},
		{ID: "a2", SourceNodeID: "i1", TargetNodeID: "h2", Type: EvidenceEdgeNextStep},
		{ID: "b1", SourceNodeID: "h3", TargetNodeID: "c3", Type: EvidenceEdgeSupports},
		{ID: "b2", SourceNodeID: "c3", TargetNodeID: "h4", Type: EvidenceEdgeNextStep},
	}
	projection := BuildEvidenceResearchProjection(EvidenceChain{ID: "chain_capacity"}, EvidenceChainGraph{Nodes: nodes, Edges: edges})
	capacity := projection.Capacity
	if capacity.Status != "healthy" || capacity.ThreadCount != 5 || capacity.RootThreadCount != 3 || capacity.SuggestedTopicCount != 1 {
		t.Fatalf("capacity = %#v", capacity)
	}
	if len(capacity.ThreadFamilies) != 3 || capacity.ThreadFamilies[0].ThreadCount != 2 || capacity.ThreadFamilies[1].ThreadCount != 2 || capacity.ThreadFamilies[2].ThreadCount != 1 {
		t.Fatalf("thread families = %#v", capacity.ThreadFamilies)
	}
	if !reflect.DeepEqual(capacity.ThreadFamilies[0].ThreadIDs, []string{"thread:h1", "thread:h2"}) {
		t.Fatalf("family A threads = %#v", capacity.ThreadFamilies[0].ThreadIDs)
	}
}

func TestBuildEvidenceResearchProjectionDoesNotInventSingleFamilySplit(t *testing.T) {
	nodes := []EvidenceChainNode{{ID: "h0", Type: EvidenceNodeHypothesis, Title: "One family", DataJSON: `{}`}}
	edges := make([]EvidenceChainEdge, 0)
	previous := "h0"
	for index := 1; index <= 4; index++ {
		issueID := "i" + string(rune('0'+index))
		hypothesisID := "h" + string(rune('0'+index))
		nodes = append(nodes,
			EvidenceChainNode{ID: issueID, Type: EvidenceNodeIssue, Title: "Issue", DataJSON: `{}`},
			EvidenceChainNode{ID: hypothesisID, Type: EvidenceNodeHypothesis, Title: "Child", DataJSON: `{}`},
		)
		edges = append(edges,
			EvidenceChainEdge{ID: "reveal-" + issueID, SourceNodeID: previous, TargetNodeID: issueID, Type: EvidenceEdgeRevealsIssue},
			EvidenceChainEdge{ID: "branch-" + hypothesisID, SourceNodeID: issueID, TargetNodeID: hypothesisID, Type: EvidenceEdgeNextStep},
		)
		previous = hypothesisID
	}
	capacity := BuildEvidenceResearchProjection(EvidenceChain{ID: "chain_single_family"}, EvidenceChainGraph{Nodes: nodes, Edges: edges}).Capacity
	if capacity.Status != "healthy" || capacity.TooLarge || capacity.SplitRecommended || capacity.RootThreadCount != 1 || capacity.SuggestedTopicCount != 1 {
		t.Fatalf("single-family capacity = %#v", capacity)
	}
}

func TestBuildEvidenceResearchProjectionUnassignedOverflowRequiresCleanup(t *testing.T) {
	nodes := []EvidenceChainNode{{ID: "h", Type: EvidenceNodeHypothesis, Title: "Root", DataJSON: `{}`}}
	for index := 0; index < 9; index++ {
		nodes = append(nodes, EvidenceChainNode{ID: "orphan" + string(rune('a'+index)), Type: EvidenceNodeIssue, Title: "Orphan", DataJSON: `{}`})
	}
	capacity := BuildEvidenceResearchProjection(EvidenceChain{ID: "chain_unassigned"}, EvidenceChainGraph{Nodes: nodes}).Capacity
	if capacity.Status != "cleanup_required" || !capacity.TooLarge || capacity.SplitRecommended || capacity.SuggestedTopicCount != 1 || capacity.UnassignedCount != 9 {
		t.Fatalf("unassigned capacity = %#v", capacity)
	}
}

func TestBuildEvidenceResearchProjectionCapacityBoundaries(t *testing.T) {
	threadsAtLimit := []EvidenceChainNode{
		{ID: "h1", Type: EvidenceNodeHypothesis, Title: "One", DataJSON: `{}`},
		{ID: "h2", Type: EvidenceNodeHypothesis, Title: "Two", DataJSON: `{}`},
		{ID: "h3", Type: EvidenceNodeHypothesis, Title: "Three", DataJSON: `{}`},
		{ID: "h4", Type: EvidenceNodeHypothesis, Title: "Four", DataJSON: `{}`},
	}
	atLimit := BuildEvidenceResearchProjection(EvidenceChain{ID: "chain_at_limit"}, EvidenceChainGraph{Nodes: threadsAtLimit}).Capacity
	if atLimit.Status != "healthy" || atLimit.TooLarge || atLimit.SplitRecommended || atLimit.ThreadCount != 4 {
		t.Fatalf("thread boundary capacity = %#v", atLimit)
	}

	nodes := []EvidenceChainNode{{ID: "h", Type: EvidenceNodeHypothesis, Title: "One family", DataJSON: `{}`}}
	edges := make([]EvidenceChainEdge, 0, 32)
	for index := 0; index < 32; index++ {
		id := "p" + string(rune('A'+index))
		nodes = append(nodes, EvidenceChainNode{ID: id, Type: EvidenceNodePlan, Title: "Design", DataJSON: `{}`})
		edges = append(edges, EvidenceChainEdge{ID: "e" + id, SourceNodeID: "h", TargetNodeID: id, Type: EvidenceEdgeNextStep})
	}
	overNodes := BuildEvidenceResearchProjection(EvidenceChain{ID: "chain_node_overflow"}, EvidenceChainGraph{Nodes: nodes, Edges: edges}).Capacity
	if overNodes.ThreadNodeCount != 33 || overNodes.Status != "healthy" || overNodes.TooLarge || overNodes.SplitRecommended {
		t.Fatalf("node boundary capacity = %#v", overNodes)
	}
}

func TestBuildEvidenceResearchProjectionSplitsOnlyForHighPresentationLoadAcrossFamilies(t *testing.T) {
	nodes := make([]EvidenceChainNode, 0, 123)
	edges := make([]EvidenceChainEdge, 0, 120)
	for family := 0; family < 3; family++ {
		rootID := fmt.Sprintf("h%d", family)
		nodes = append(nodes, EvidenceChainNode{ID: rootID, Type: EvidenceNodeHypothesis, Title: fmt.Sprintf("Family %d", family), DataJSON: `{}`})
		for index := 0; index < 40; index++ {
			id := fmt.Sprintf("d%d-%02d", family, index)
			nodes = append(nodes, EvidenceChainNode{ID: id, Type: EvidenceNodePlan, Title: "Design", DataJSON: `{}`})
			edges = append(edges, EvidenceChainEdge{ID: "e-" + id, SourceNodeID: rootID, TargetNodeID: id, Type: EvidenceEdgeNextStep})
		}
	}
	capacity := BuildEvidenceResearchProjection(EvidenceChain{ID: "chain_large"}, EvidenceChainGraph{Nodes: nodes, Edges: edges}).Capacity
	if capacity.Status != "split_recommended" || !capacity.SplitRecommended || capacity.SuggestedTopicCount != 3 || capacity.ThreadNodeCount != 123 {
		t.Fatalf("high presentation load = %#v", capacity)
	}
}

func TestBuildEvidenceResearchProjectionResearchHealthV2(t *testing.T) {
	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "h", Type: EvidenceNodeHypothesis, Title: "Question", DataJSON: `{}`},
			{ID: "d", Type: EvidenceNodePlan, Title: "Design", DataJSON: `{}`},
			{ID: "r", Type: EvidenceNodeClaim, Title: "Result", Body: "Observed result", SourceRunIDs: []string{"run_1"}, DataJSON: `{"claimKind":"result","resultDisposition":"conclusion"}`},
			{ID: "c", Type: EvidenceNodeConclusion, Title: "Conclusion", DataJSON: `{}`},
		},
		Edges: []EvidenceChainEdge{
			{ID: "e1", SourceNodeID: "h", TargetNodeID: "d", Type: EvidenceEdgeNextStep},
			{ID: "e2", SourceNodeID: "d", TargetNodeID: "r", Type: EvidenceEdgeSupports},
			{ID: "e3", SourceNodeID: "r", TargetNodeID: "c", Type: EvidenceEdgeSupports},
		},
	}
	projection := BuildEvidenceResearchProjection(EvidenceChain{ID: "chain_health"}, graph)
	health := projection.StructuralHealth
	if health.PolicyVersion != EvidenceResearchHealthPolicyVersion || health.CompatibilityStatus != "v2_compliant" || health.ReadabilityStatus != "clear" || health.DerivedTopicPhase != "outcome_recorded" {
		t.Fatalf("health = %#v", health)
	}
	if len(health.Threads) != 1 || health.Threads[0].ProvenanceDeclared.Complete != 1 || health.Threads[0].DispositionComplete.Complete != 1 || health.Threads[0].OutcomeComplete.Complete != 1 {
		t.Fatalf("thread health = %#v", health.Threads)
	}

	reversed := graph
	reversed.Nodes = append([]EvidenceChainNode(nil), graph.Nodes...)
	reversed.Edges = append([]EvidenceChainEdge(nil), graph.Edges...)
	for i, j := 0, len(reversed.Nodes)-1; i < j; i, j = i+1, j-1 {
		reversed.Nodes[i], reversed.Nodes[j] = reversed.Nodes[j], reversed.Nodes[i]
	}
	for i, j := 0, len(reversed.Edges)-1; i < j; i, j = i+1, j-1 {
		reversed.Edges[i], reversed.Edges[j] = reversed.Edges[j], reversed.Edges[i]
	}
	if other := BuildEvidenceResearchProjection(EvidenceChain{ID: "chain_health"}, reversed).StructuralHealth; !reflect.DeepEqual(health, other) {
		t.Fatalf("health changed with input order:\n%#v\n%#v", health, other)
	}
}

func TestBuildEvidenceResearchProjectionThreadComplexityIsAdvisory(t *testing.T) {
	nodes := []EvidenceChainNode{{ID: "h", Type: EvidenceNodeHypothesis, Title: "One hypothesis", DataJSON: `{}`}}
	edges := make([]EvidenceChainEdge, 0)
	for index := 0; index < 11; index++ {
		id := fmt.Sprintf("d%02d", index)
		nodes = append(nodes, EvidenceChainNode{ID: id, Type: EvidenceNodePlan, Title: "Design", DataJSON: `{}`})
		edges = append(edges, EvidenceChainEdge{ID: "e" + id, SourceNodeID: "h", TargetNodeID: id, Type: EvidenceEdgeNextStep})
	}
	projection := BuildEvidenceResearchProjection(EvidenceChain{ID: "chain_dense"}, EvidenceChainGraph{Nodes: nodes, Edges: edges})
	if projection.Capacity.Status != "healthy" || projection.StructuralHealth.ReadabilityStatus != "dense" || projection.StructuralHealth.Threads[0].ComplexityLevel != "warning" {
		t.Fatalf("dense thread should be advisory, capacity=%#v health=%#v", projection.Capacity, projection.StructuralHealth)
	}
}

func TestBuildEvidenceResearchProjectionKeepsLegacyProtocolOutOfResearchStages(t *testing.T) {
	chain := EvidenceChain{ID: "chain_protocol", Revision: 2, GraphHash: "hash-r2"}
	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "h1", Type: EvidenceNodeHypothesis, Title: "A", DataJSON: `{}`},
			{ID: "h2", Type: EvidenceNodeHypothesis, Title: "B", DataJSON: `{}`},
			{ID: "run1", Type: EvidenceNodeRun, Title: "Run A", RunID: "run_a", DataJSON: `{"groupId":"protocol","seed":41}`},
			{ID: "run2", Type: EvidenceNodeRun, Title: "Run B", DataJSON: `{"groupId":"protocol"}`},
			{ID: "protocol", Type: EvidenceNodeGroup, Title: "Matched protocol", DataJSON: `{"groupKind":"protocol"}`},
		},
		Edges: []EvidenceChainEdge{
			{ID: "a", SourceNodeID: "h1", TargetNodeID: "run1", Type: EvidenceEdgeNextStep},
			{ID: "b", SourceNodeID: "h2", TargetNodeID: "run2", Type: EvidenceEdgeNextStep},
			{ID: "c", SourceNodeID: "run1", TargetNodeID: "run2", Type: EvidenceEdgeSupports, Label: "matched comparison", Rationale: "same protocol"},
		},
	}
	projection := BuildEvidenceResearchProjection(chain, graph)
	if len(projection.Threads) != 2 {
		t.Fatalf("threads = %#v", projection.Threads)
	}
	for _, thread := range projection.Threads {
		for _, stage := range evidenceResearchStageOrder {
			for _, card := range thread.Stages[stage] {
				if card.Node.ID == "protocol" || card.Node.Type == EvidenceNodeRun {
					t.Fatalf("legacy protocol facade or run leaked into research stage %s: %#v", stage, card)
				}
			}
		}
	}
	if len(projection.ProtocolGroups) != 1 {
		t.Fatalf("protocol groups = %#v", projection.ProtocolGroups)
	}
	protocol := projection.ProtocolGroups[0]
	if protocol.Group.ID != "protocol" || len(protocol.Members) != 2 || len(protocol.Relations) != 3 {
		t.Fatalf("protocol registry = %#v", protocol)
	}
	if protocol.Members[0].Node.ID != "run1" || protocol.Members[0].Node.RunID != "run_a" || protocol.Members[0].Node.DataJSON != `{"groupId":"protocol","seed":41}` {
		t.Fatalf("protocol member lost node data: %#v", protocol.Members[0])
	}
	scopes := map[string]int{}
	for _, relation := range protocol.Relations {
		scopes[relation.Scope]++
		if relation.Edge.SourceNodeID == "protocol" || relation.Edge.TargetNodeID == "protocol" {
			t.Fatalf("protocol facade received synthetic edge: %#v", relation)
		}
	}
	if scopes["internal"] != 1 || scopes["external"] != 2 {
		t.Fatalf("protocol relation scopes = %#v", scopes)
	}
	if projection.OwnerByNode["run1"] != "" || projection.OwnerByNode["run2"] != "" {
		t.Fatalf("provenance-only runs should not claim research-stage ownership: %#v", projection.OwnerByNode)
	}
	if len(projection.Unassigned) != 0 {
		t.Fatalf("provenance-only legacy nodes should not become triage noise: %#v", projection.Unassigned)
	}
	if projection.Capacity.ThreadNodeCount != 2 {
		t.Fatalf("provenance/group nodes counted toward Topic capacity: %#v", projection.Capacity)
	}
}
