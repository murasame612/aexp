package store

import (
	"encoding/json"
	"sort"
	"strings"
)

const (
	EvidenceResearchContractVersion = "research-thread-v2"

	EvidenceResearchStageHypothesis = "hypothesis"
	EvidenceResearchStageDesign     = "design"
	EvidenceResearchStageResult     = "result"
	EvidenceResearchStageConclusion = "conclusion"
	EvidenceResearchStageIssue      = "issue"
)

var evidenceResearchStageOrder = []string{
	EvidenceResearchStageHypothesis,
	EvidenceResearchStageDesign,
	EvidenceResearchStageResult,
	EvidenceResearchStageConclusion,
	EvidenceResearchStageIssue,
}

var evidenceResearchPresentationStageOrder = []string{
	EvidenceResearchStageHypothesis,
	EvidenceResearchStageDesign,
	EvidenceResearchStageResult,
	"interpretation",
	"outcome",
}

// EvidenceResearchCard is a read-only projection of one accepted graph node.
// Legacy protocol groups remain available through ProtocolGroups, but are not
// promoted into the primary research stages as a second authoring model.
type EvidenceResearchCard struct {
	Node              EvidenceChainNode `json:"node"`
	RelationCount     int               `json:"relation_count"`
	MemberCount       int               `json:"member_count,omitempty"`
	MemberNodeIDs     []string          `json:"member_node_ids,omitempty"`
	MemberTitles      []string          `json:"member_titles,omitempty"`
	SharedThreadIDs   []string          `json:"shared_thread_ids,omitempty"`
	CanonicalThreadID string            `json:"canonical_thread_id,omitempty"`
}

// EvidenceResearchProtocolMember keeps one real member node and its canonical
// thread ownership. Membership is expressed by node.data_json.groupId; this
// projection never creates an edge to the group facade.
type EvidenceResearchProtocolMember struct {
	Node     EvidenceChainNode `json:"node"`
	ThreadID string            `json:"thread_id,omitempty"`
}

// EvidenceResearchProtocolRelation preserves a real member edge. Scope is
// internal when both endpoints belong to the protocol, external otherwise.
type EvidenceResearchProtocolRelation struct {
	Edge  EvidenceChainEdge `json:"edge"`
	Scope string            `json:"scope"`
}

type EvidenceResearchProtocolGroup struct {
	Group     EvidenceChainNode                  `json:"group"`
	Members   []EvidenceResearchProtocolMember   `json:"members"`
	Relations []EvidenceResearchProtocolRelation `json:"relations"`
}

type EvidenceResearchThread struct {
	ID                 string                            `json:"id"`
	Title              string                            `json:"title"`
	RootNodeID         string                            `json:"root_node_id"`
	ParentThreadID     string                            `json:"parent_thread_id,omitempty"`
	ExplicitHypothesis bool                              `json:"explicit_hypothesis"`
	Stages             map[string][]EvidenceResearchCard `json:"stages"`
	Interpretations    []EvidenceResearchInterpretation  `json:"interpretations,omitempty"`
}

// EvidenceResearchInterpretation is a read-only bridge between an immutable
// Result and its durable Conclusion/Issue destination. It is deliberately not
// a graph node: the real edge remains the source of truth and the UI may render
// this compactly as the fourth research lane.
type EvidenceResearchInterpretation struct {
	ID             string `json:"id"`
	ResultNodeID   string `json:"result_node_id"`
	OutcomeNodeID  string `json:"outcome_node_id,omitempty"`
	OutcomeType    string `json:"outcome_type,omitempty"`
	Kind           string `json:"kind"`
	Label          string `json:"label"`
	Rationale      string `json:"rationale,omitempty"`
	EdgeID         string `json:"edge_id,omitempty"`
	LegacyInferred bool   `json:"legacy_inferred,omitempty"`
}

type EvidenceResearchUnassigned struct {
	Card   EvidenceResearchCard `json:"card"`
	Reason string               `json:"reason"`
}

type EvidenceResearchCrossRelation struct {
	Edge           EvidenceChainEdge `json:"edge"`
	SourceThreadID string            `json:"source_thread_id"`
	TargetThreadID string            `json:"target_thread_id"`
	Kind           string            `json:"kind"`
}

const (
	EvidenceTopicCapacityPolicyVersion = "topic-presentation-v2"
	// Topic presentation limits are rendering advisories, not semantic model
	// capacity. A Topic may contain many Research Threads when they remain
	// individually legible and correctly linked.
	EvidenceTopicRecommendedMaxThreads     = 0
	EvidenceTopicRecommendedMaxThreadNodes = 120
	EvidenceTopicRecommendedMaxUnassigned  = 8
	EvidenceResearchHealthPolicyVersion    = "research-health-v2"
	EvidenceResearchThreadWarningNodes     = 12
	EvidenceResearchThreadCriticalNodes    = 16
)

type EvidenceResearchCompleteness struct {
	Total          int      `json:"total"`
	Complete       int      `json:"complete"`
	MissingNodeIDs []string `json:"missing_node_ids,omitempty"`
}

// EvidenceResearchHealthFinding is an advisory about the accepted graph. It
// never changes proposal eligibility by itself. IDs are sorted so callers can
// safely compare reports across refreshes.
type EvidenceResearchHealthFinding struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity"`
	ThreadID string   `json:"thread_id,omitempty"`
	NodeIDs  []string `json:"node_ids,omitempty"`
	Message  string   `json:"message"`
}

type EvidenceResearchThreadHealth struct {
	ThreadID                 string                          `json:"thread_id"`
	DerivedPhase             string                          `json:"derived_phase"`
	ComplexityLevel          string                          `json:"complexity_level"`
	SemanticNodeCount        int                             `json:"semantic_node_count"`
	HypothesisCount          int                             `json:"hypothesis_count"`
	DesignCount              int                             `json:"design_count"`
	ResultCount              int                             `json:"result_count"`
	ConclusionCount          int                             `json:"conclusion_count"`
	IssueCount               int                             `json:"issue_count"`
	ParallelDesignNodeIDs    []string                        `json:"parallel_design_node_ids,omitempty"`
	ParallelResultNodeIDs    []string                        `json:"parallel_result_node_ids,omitempty"`
	ProvenanceDeclared       EvidenceResearchCompleteness    `json:"provenance_declared"`
	DispositionComplete      EvidenceResearchCompleteness    `json:"disposition_complete"`
	OutcomeComplete          EvidenceResearchCompleteness    `json:"outcome_complete"`
	IssueFollowUpLinked      EvidenceResearchCompleteness    `json:"issue_follow_up_linked"`
	PossibleDuplicateResults [][]string                      `json:"possible_duplicate_result_groups,omitempty"`
	Findings                 []EvidenceResearchHealthFinding `json:"findings,omitempty"`
}

// EvidenceResearchStructuralHealth is a deterministic, read-only summary of
// graph structure. It deliberately says nothing about whether an experiment
// is currently running or whether a scientific claim is true.
type EvidenceResearchStructuralHealth struct {
	PolicyVersion       string                          `json:"policy_version"`
	Terminology         map[string]string               `json:"terminology"`
	ReadabilityStatus   string                          `json:"readability_status"`
	CompatibilityStatus string                          `json:"compatibility_status"`
	TopicLifecycle      string                          `json:"topic_lifecycle"`
	DerivedTopicPhase   string                          `json:"derived_topic_phase"`
	SemanticNodeCount   int                             `json:"semantic_node_count"`
	AssignedCount       int                             `json:"assigned_count"`
	UnassignedCount     int                             `json:"unassigned_count"`
	UnassignedRatio     float64                         `json:"unassigned_ratio"`
	Threads             []EvidenceResearchThreadHealth  `json:"threads"`
	Findings            []EvidenceResearchHealthFinding `json:"findings,omitempty"`
}

// EvidenceTopicThreadFamily is one top-level hypothesis and all child threads
// reached through explicit branch relations. Families are deterministic split
// candidates; the service does not move evidence between Maps automatically.
type EvidenceTopicThreadFamily struct {
	ID                string   `json:"id"`
	RootThreadID      string   `json:"root_thread_id"`
	Title             string   `json:"title"`
	ThreadIDs         []string `json:"thread_ids"`
	ThreadCount       int      `json:"thread_count"`
	SemanticNodeCount int      `json:"semantic_node_count"`
}

// EvidenceTopicCapacity gives Agents a shared, advisory Topic-size contract.
// Limits are deliberately soft: an Agent may finish an existing thread, but
// should not add another independent hypothesis to an overloaded Topic.
type EvidenceTopicCapacity struct {
	PolicyVersion             string                      `json:"policy_version"`
	Status                    string                      `json:"status"`
	TooLarge                  bool                        `json:"too_large"`
	SplitRecommended          bool                        `json:"split_recommended"`
	ThreadCount               int                         `json:"thread_count"`
	RootThreadCount           int                         `json:"root_thread_count"`
	ThreadNodeCount           int                         `json:"thread_node_count"`
	UnassignedCount           int                         `json:"unassigned_count"`
	RecommendedMaxThreads     int                         `json:"recommended_max_threads"`
	RecommendedMaxThreadNodes int                         `json:"recommended_max_thread_nodes"`
	RecommendedMaxUnassigned  int                         `json:"recommended_max_unassigned"`
	SuggestedTopicCount       int                         `json:"suggested_topic_count"`
	Reasons                   []string                    `json:"reasons"`
	ThreadFamilies            []EvidenceTopicThreadFamily `json:"thread_families"`
}

type EvidenceResearchProjection struct {
	ContractVersion      string                           `json:"evidence_contract_version"`
	ChainID              string                           `json:"chain_id"`
	Revision             int64                            `json:"revision"`
	GraphHash            string                           `json:"graph_hash"`
	Stages               []string                         `json:"stage_order"`
	PresentationStages   []string                         `json:"presentation_stage_order"`
	Threads              []EvidenceResearchThread         `json:"threads"`
	Unassigned           []EvidenceResearchUnassigned     `json:"unassigned"`
	CrossThreadRelations []EvidenceResearchCrossRelation  `json:"cross_thread_relations"`
	ProtocolGroups       []EvidenceResearchProtocolGroup  `json:"protocol_groups,omitempty"`
	OwnerByNode          map[string]string                `json:"owner_by_node"`
	Capacity             EvidenceTopicCapacity            `json:"capacity"`
	StructuralHealth     EvidenceResearchStructuralHealth `json:"structural_health"`
}

func evidenceResearchInterpretationLabel(edgeType, outcomeType, disposition string) string {
	if outcomeType == EvidenceNodeIssue {
		if disposition == EvidenceResultDispositionMixed {
			return "同时暴露限制"
		}
		return "暂不能形成结论"
	}
	switch edgeType {
	case EvidenceEdgeWeakens:
		return "证据削弱结论"
	case EvidenceEdgeDoesNotProve:
		return "证据尚不足以证明"
	default:
		return "证据支持结论"
	}
}

func evidenceResearchInterpretations(graph EvidenceChainGraph, ownerByNode map[string]string) map[string][]EvidenceResearchInterpretation {
	byID := make(map[string]EvidenceChainNode, len(graph.Nodes))
	for _, node := range graph.Nodes {
		byID[node.ID] = node
	}
	byThread := make(map[string][]EvidenceResearchInterpretation)
	resultsWithOutcome := make(map[string]bool)
	edges := append([]EvidenceChainEdge(nil), graph.Edges...)
	sort.SliceStable(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	for _, edge := range edges {
		source := byID[edge.SourceNodeID]
		if !evidenceResultNode(source) {
			continue
		}
		target := byID[edge.TargetNodeID]
		isConclusion := target.Type == EvidenceNodeConclusion && (edge.Type == EvidenceEdgeSupports || edge.Type == EvidenceEdgeWeakens || edge.Type == EvidenceEdgeDoesNotProve)
		isIssue := target.Type == EvidenceNodeIssue && edge.Type == EvidenceEdgeRevealsIssue
		if !isConclusion && !isIssue {
			continue
		}
		threadID := ownerByNode[source.ID]
		if threadID == "" {
			continue
		}
		disposition := evidenceAuthoringResultDisposition(source)
		rationale := strings.TrimSpace(edge.Rationale)
		if rationale == "" && isIssue {
			rationale = evidenceAuthoringDispositionReason(source)
		}
		byThread[threadID] = append(byThread[threadID], EvidenceResearchInterpretation{
			ID:             "interpretation:" + edge.ID,
			ResultNodeID:   source.ID,
			OutcomeNodeID:  target.ID,
			OutcomeType:    target.Type,
			Kind:           edge.Type,
			Label:          evidenceResearchInterpretationLabel(edge.Type, target.Type, disposition),
			Rationale:      rationale,
			EdgeID:         edge.ID,
			LegacyInferred: disposition == "",
		})
		resultsWithOutcome[source.ID] = true
	}
	for _, node := range graph.Nodes {
		if !evidenceResultNode(node) || resultsWithOutcome[node.ID] {
			continue
		}
		threadID := ownerByNode[node.ID]
		if threadID == "" {
			continue
		}
		disposition := evidenceAuthoringResultDisposition(node)
		label := "结果尚未解释"
		kind := "legacy_unspecified"
		legacy := disposition == ""
		if disposition == EvidenceResultDispositionPending {
			label = "待解释"
			kind = EvidenceResultDispositionPending
		}
		byThread[threadID] = append(byThread[threadID], EvidenceResearchInterpretation{
			ID:             "interpretation:" + node.ID + ":pending",
			ResultNodeID:   node.ID,
			Kind:           kind,
			Label:          label,
			Rationale:      evidenceAuthoringDispositionReason(node),
			LegacyInferred: legacy,
		})
	}
	for threadID := range byThread {
		sort.SliceStable(byThread[threadID], func(i, j int) bool { return byThread[threadID][i].ID < byThread[threadID][j].ID })
	}
	return byThread
}

type evidenceResearchNodeData struct {
	ClaimKindSnake string `json:"claim_kind"`
	ClaimKindCamel string `json:"claimKind"`
	GroupKindSnake string `json:"group_kind"`
	GroupKindCamel string `json:"groupKind"`
	GroupIDSnake   string `json:"group_id"`
	GroupIDCamel   string `json:"groupId"`
}

func parseEvidenceResearchNodeData(node EvidenceChainNode) evidenceResearchNodeData {
	var data evidenceResearchNodeData
	_ = json.Unmarshal([]byte(strings.TrimSpace(node.DataJSON)), &data)
	return data
}

func evidenceResearchNodeStage(node EvidenceChainNode) string {
	data := parseEvidenceResearchNodeData(node)
	claimKind := strings.ToLower(strings.TrimSpace(data.ClaimKindCamel))
	if claimKind == "" {
		claimKind = strings.ToLower(strings.TrimSpace(data.ClaimKindSnake))
	}
	switch node.Type {
	case EvidenceNodeHypothesis:
		return EvidenceResearchStageHypothesis
	case EvidenceNodeClaim:
		if claimKind == "hypothesis" {
			return EvidenceResearchStageHypothesis
		}
		return EvidenceResearchStageResult
	case EvidenceNodePlan:
		return EvidenceResearchStageDesign
	case EvidenceNodeProtocol, EvidenceNodeExperiment:
		return EvidenceResearchStageDesign
	case EvidenceNodeConclusion:
		return EvidenceResearchStageConclusion
	case EvidenceNodeIssue:
		return EvidenceResearchStageIssue
	default:
		return ""
	}
}

func evidenceResearchProvenanceOnly(node EvidenceChainNode) bool {
	switch node.Type {
	case EvidenceNodeGroup, EvidenceNodeDataset, EvidenceNodeRun:
		return true
	default:
		return false
	}
}

func evidenceResearchNodeKey(node EvidenceChainNode) string {
	when := ""
	if node.OccurredAt != nil {
		when = node.OccurredAt.UTC().Format("2006-01-02T15:04:05.000000000Z")
	}
	return when + "\x00" + strings.TrimSpace(node.Title) + "\x00" + node.ID
}

func emptyEvidenceResearchStages() map[string][]EvidenceResearchCard {
	return map[string][]EvidenceResearchCard{
		EvidenceResearchStageHypothesis: {},
		EvidenceResearchStageDesign:     {},
		EvidenceResearchStageResult:     {},
		EvidenceResearchStageConclusion: {},
		EvidenceResearchStageIssue:      {},
	}
}

func isEvidenceProtocolGroup(node EvidenceChainNode) bool {
	if node.Type != EvidenceNodeGroup {
		return false
	}
	data := parseEvidenceResearchNodeData(node)
	groupKind := strings.ToLower(strings.TrimSpace(data.GroupKindCamel))
	if groupKind == "" {
		groupKind = strings.ToLower(strings.TrimSpace(data.GroupKindSnake))
	}
	return groupKind == "protocol"
}

func evidenceResearchGroupID(node EvidenceChainNode) string {
	data := parseEvidenceResearchNodeData(node)
	if id := strings.TrimSpace(data.GroupIDCamel); id != "" {
		return id
	}
	return strings.TrimSpace(data.GroupIDSnake)
}

func evidenceResearchCard(node EvidenceChainNode, relationCounts map[string]int) EvidenceResearchCard {
	return EvidenceResearchCard{Node: node, RelationCount: relationCounts[node.ID]}
}

func evidenceResearchStartsChildThread(edge EvidenceChainEdge, source, target EvidenceChainNode) bool {
	if edge.Type != EvidenceEdgeNextStep || evidenceResearchNodeStage(target) != EvidenceResearchStageHypothesis {
		return false
	}
	return source.Type == EvidenceNodeIssue || source.Type == EvidenceNodeConclusion
}

func evidenceResearchThreadNodeCount(thread EvidenceResearchThread) int {
	total := 0
	for _, stage := range evidenceResearchStageOrder {
		total += len(thread.Stages[stage])
	}
	return total
}

func evidenceResearchReachable(adjacency map[string][]string, source, target string) bool {
	if source == target {
		return true
	}
	seen := map[string]bool{source: true}
	queue := []string{source}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range adjacency[current] {
			if next == target {
				return true
			}
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return false
}

func evidenceResearchParallelNodeIDs(cards []EvidenceResearchCard, adjacency map[string][]string) []string {
	parallel := map[string]bool{}
	for i := 0; i < len(cards); i++ {
		for j := i + 1; j < len(cards); j++ {
			left, right := cards[i].Node.ID, cards[j].Node.ID
			if evidenceResearchReachable(adjacency, left, right) || evidenceResearchReachable(adjacency, right, left) {
				continue
			}
			parallel[left], parallel[right] = true, true
		}
	}
	ids := make([]string, 0, len(parallel))
	for id := range parallel {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func evidenceResearchCompleteness(total int, complete map[string]bool, nodeIDs []string) EvidenceResearchCompleteness {
	missing := make([]string, 0)
	for _, id := range nodeIDs {
		if !complete[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return EvidenceResearchCompleteness{Total: total, Complete: total - len(missing), MissingNodeIDs: missing}
}

func evidenceResearchResultFingerprint(node EvidenceChainNode, outcomes []string) string {
	runs := normalizeEvidenceProposalIDs(node.SourceRunIDs)
	snapshots := normalizeEvidenceProposalIDs(node.SourceSnapshotIDs)
	outcomes = append([]string(nil), outcomes...)
	sort.Strings(outcomes)
	return strings.Join([]string{
		strings.TrimSpace(node.Title), strings.TrimSpace(node.Body), strings.TrimSpace(node.DataJSON),
		strings.Join(runs, ","), strings.Join(snapshots, ","), strings.Join(outcomes, ","),
	}, "\x00")
}

func buildEvidenceResearchStructuralHealth(chain EvidenceChain, graph EvidenceChainGraph, projection EvidenceResearchProjection) EvidenceResearchStructuralHealth {
	type outcomeStatus struct {
		hasConclusion  bool
		hasIssue       bool
		issueExplained bool
	}
	byID := make(map[string]EvidenceChainNode, len(graph.Nodes))
	adjacency := make(map[string][]string)
	for _, node := range graph.Nodes {
		byID[node.ID] = node
	}
	for _, edge := range graph.Edges {
		if !isSemanticEvidenceEdge(edge.Type) || byID[edge.SourceNodeID].ID == "" || byID[edge.TargetNodeID].ID == "" {
			continue
		}
		adjacency[edge.SourceNodeID] = append(adjacency[edge.SourceNodeID], edge.TargetNodeID)
	}
	for id := range adjacency {
		sort.Strings(adjacency[id])
	}
	outcomeFingerprints := make(map[string][]string)
	outcomesByResult := make(map[string]outcomeStatus)
	issueFollowUp := make(map[string]bool)
	for _, edge := range graph.Edges {
		if evidenceResultOutcomeEdge(byID, edge) {
			outcomeFingerprints[edge.SourceNodeID] = append(outcomeFingerprints[edge.SourceNodeID], edge.Type+":"+edge.TargetNodeID)
			status := outcomesByResult[edge.SourceNodeID]
			target := byID[edge.TargetNodeID]
			if target.Type == EvidenceNodeConclusion {
				status.hasConclusion = true
			} else if target.Type == EvidenceNodeIssue {
				status.hasIssue = true
				status.issueExplained = status.issueExplained || strings.TrimSpace(edge.Rationale) != ""
			}
			outcomesByResult[edge.SourceNodeID] = status
		}
		if source := byID[edge.SourceNodeID]; source.Type == EvidenceNodeIssue && evidenceResearchStartsChildThread(edge, source, byID[edge.TargetNodeID]) {
			issueFollowUp[source.ID] = true
		}
	}

	health := EvidenceResearchStructuralHealth{
		PolicyVersion: EvidenceResearchHealthPolicyVersion,
		Terminology: map[string]string{
			"topic":           "decision_question",
			"research_thread": "hypothesis_chain",
			"stage_column":    "presentation_bucket",
		},
		ReadabilityStatus:   "clear",
		CompatibilityStatus: "v2_compliant",
		TopicLifecycle:      "active",
		DerivedTopicPhase:   "empty",
		AssignedCount:       projection.Capacity.ThreadNodeCount,
		UnassignedCount:     len(projection.Unassigned),
	}
	for _, node := range graph.Nodes {
		if node.Type == EvidenceNodeRun {
			health.CompatibilityStatus = "legacy_readable"
			health.Findings = append(health.Findings, EvidenceResearchHealthFinding{Code: "LEGACY_RUN_NODE", Severity: "warning", NodeIDs: []string{node.ID}, Message: "Visual Run nodes are compatibility data; Result provenance belongs in source_run_ids/source_snapshot_ids"})
		}
	}
	health.SemanticNodeCount = health.AssignedCount + health.UnassignedCount
	if strings.EqualFold(strings.TrimSpace(chain.Status), "archived") {
		health.TopicLifecycle = "archived"
	} else if health.SemanticNodeCount == 0 {
		health.TopicLifecycle = "draft"
	}
	if health.SemanticNodeCount > 0 {
		health.UnassignedRatio = float64(health.UnassignedCount) / float64(health.SemanticNodeCount)
	}
	if health.UnassignedCount > 0 || len(projection.ProtocolGroups) > 0 {
		health.CompatibilityStatus = "legacy_readable"
	}
	phaseSet := map[string]bool{}

	for _, thread := range projection.Threads {
		threadHealth := EvidenceResearchThreadHealth{
			ThreadID:          thread.ID,
			ComplexityLevel:   "normal",
			SemanticNodeCount: evidenceResearchThreadNodeCount(thread),
			HypothesisCount:   len(thread.Stages[EvidenceResearchStageHypothesis]),
			DesignCount:       len(thread.Stages[EvidenceResearchStageDesign]),
			ResultCount:       len(thread.Stages[EvidenceResearchStageResult]),
			ConclusionCount:   len(thread.Stages[EvidenceResearchStageConclusion]),
			IssueCount:        len(thread.Stages[EvidenceResearchStageIssue]),
		}
		if threadHealth.SemanticNodeCount >= EvidenceResearchThreadCriticalNodes {
			threadHealth.ComplexityLevel = "critical"
		} else if threadHealth.SemanticNodeCount >= EvidenceResearchThreadWarningNodes {
			threadHealth.ComplexityLevel = "warning"
		}
		threadHealth.ParallelDesignNodeIDs = evidenceResearchParallelNodeIDs(thread.Stages[EvidenceResearchStageDesign], adjacency)
		threadHealth.ParallelResultNodeIDs = evidenceResearchParallelNodeIDs(thread.Stages[EvidenceResearchStageResult], adjacency)

		resultIDs := make([]string, 0, threadHealth.ResultCount)
		provenance, disposition, outcome := map[string]bool{}, map[string]bool{}, map[string]bool{}
		fingerprints := map[string][]string{}
		for _, card := range thread.Stages[EvidenceResearchStageResult] {
			node := card.Node
			resultIDs = append(resultIDs, node.ID)
			provenance[node.ID] = len(node.SourceRunIDs) > 0 || len(node.SourceSnapshotIDs) > 0
			d := evidenceAuthoringResultDisposition(node)
			disposition[node.ID] = validEvidenceResultDisposition(d)
			outcomeStatus := outcomesByResult[node.ID]
			matches := (d == EvidenceResultDispositionConclusion && outcomeStatus.hasConclusion && !outcomeStatus.hasIssue) ||
				(d == EvidenceResultDispositionIssue && outcomeStatus.hasIssue && !outcomeStatus.hasConclusion) ||
				(d == EvidenceResultDispositionMixed && outcomeStatus.hasConclusion && outcomeStatus.hasIssue) ||
				(d == EvidenceResultDispositionPending && !outcomeStatus.hasConclusion && !outcomeStatus.hasIssue && strings.TrimSpace(evidenceAuthoringDispositionReason(node)) != "")
			if (d == EvidenceResultDispositionIssue || d == EvidenceResultDispositionMixed) && strings.TrimSpace(evidenceAuthoringDispositionReason(node)) == "" && !outcomeStatus.issueExplained {
				matches = false
			}
			outcome[node.ID] = matches
			fingerprint := evidenceResearchResultFingerprint(node, outcomeFingerprints[node.ID])
			fingerprints[fingerprint] = append(fingerprints[fingerprint], node.ID)
			if evidenceAuthoringClaimKind(node) != "result" {
				health.CompatibilityStatus = "legacy_readable"
				threadHealth.Findings = append(threadHealth.Findings, EvidenceResearchHealthFinding{Code: "CLAIM_KIND_MISSING", Severity: "warning", ThreadID: thread.ID, NodeIDs: []string{node.ID}, Message: "Result card is a legacy untyped Claim"})
			}
			if !provenance[node.ID] || !disposition[node.ID] || !outcome[node.ID] {
				health.CompatibilityStatus = "legacy_readable"
			}
		}
		threadHealth.ProvenanceDeclared = evidenceResearchCompleteness(threadHealth.ResultCount, provenance, resultIDs)
		threadHealth.DispositionComplete = evidenceResearchCompleteness(threadHealth.ResultCount, disposition, resultIDs)
		threadHealth.OutcomeComplete = evidenceResearchCompleteness(threadHealth.ResultCount, outcome, resultIDs)

		issueIDs := make([]string, 0, threadHealth.IssueCount)
		for _, card := range thread.Stages[EvidenceResearchStageIssue] {
			issueIDs = append(issueIDs, card.Node.ID)
		}
		threadHealth.IssueFollowUpLinked = evidenceResearchCompleteness(threadHealth.IssueCount, issueFollowUp, issueIDs)
		for _, ids := range fingerprints {
			if len(ids) < 2 {
				continue
			}
			sort.Strings(ids)
			threadHealth.PossibleDuplicateResults = append(threadHealth.PossibleDuplicateResults, ids)
		}
		sort.SliceStable(threadHealth.PossibleDuplicateResults, func(i, j int) bool {
			return strings.Join(threadHealth.PossibleDuplicateResults[i], "\x00") < strings.Join(threadHealth.PossibleDuplicateResults[j], "\x00")
		})

		switch {
		case threadHealth.ResultCount > 0 && threadHealth.OutcomeComplete.Complete == threadHealth.ResultCount:
			threadHealth.DerivedPhase = "outcome_recorded"
		case threadHealth.ResultCount > 0:
			threadHealth.DerivedPhase = "result_recorded"
		case threadHealth.DesignCount > 0:
			threadHealth.DerivedPhase = "design_recorded"
		default:
			threadHealth.DerivedPhase = "hypothesis_recorded"
		}
		phaseSet[threadHealth.DerivedPhase] = true

		if threadHealth.ComplexityLevel != "normal" {
			threadHealth.Findings = append(threadHealth.Findings, EvidenceResearchHealthFinding{Code: "THREAD_COMPLEXITY_REVIEW", Severity: threadHealth.ComplexityLevel, ThreadID: thread.ID, Message: "Research Thread is structurally dense; review whether it still represents one hypothesis chain"})
		}
		if len(threadHealth.ParallelDesignNodeIDs) > 3 {
			threadHealth.Findings = append(threadHealth.Findings, EvidenceResearchHealthFinding{Code: "PARALLEL_DESIGN_REVIEW", Severity: "warning", ThreadID: thread.ID, NodeIDs: threadHealth.ParallelDesignNodeIDs, Message: "Many structurally parallel designs share one Research Thread"})
		}
		if len(threadHealth.ParallelResultNodeIDs) > 6 {
			threadHealth.Findings = append(threadHealth.Findings, EvidenceResearchHealthFinding{Code: "PARALLEL_RESULT_REVIEW", Severity: "warning", ThreadID: thread.ID, NodeIDs: threadHealth.ParallelResultNodeIDs, Message: "Many structurally parallel Results share one Research Thread"})
		}
		sort.SliceStable(threadHealth.Findings, func(i, j int) bool { return threadHealth.Findings[i].Code < threadHealth.Findings[j].Code })
		health.Threads = append(health.Threads, threadHealth)
		health.Findings = append(health.Findings, threadHealth.Findings...)
	}

	if health.UnassignedCount > 0 {
		health.DerivedTopicPhase = "needs_curation"
		health.ReadabilityStatus = "needs_curation"
		health.Findings = append(health.Findings, EvidenceResearchHealthFinding{Code: "UNASSIGNED_INBOX_NOT_EMPTY", Severity: "warning", Message: "Unassigned is a temporary inbox and still contains semantic nodes"})
	} else if len(phaseSet) == 1 {
		for phase := range phaseSet {
			health.DerivedTopicPhase = phase
		}
	} else if len(phaseSet) > 1 {
		health.DerivedTopicPhase = "mixed"
	}
	for _, threadHealth := range health.Threads {
		if threadHealth.ComplexityLevel != "normal" && health.ReadabilityStatus == "clear" {
			health.ReadabilityStatus = "dense"
		}
	}
	for _, edge := range graph.Edges {
		if evidenceResearchBranchBypass(byID, edge) || evidenceResultOutcomeBypass(byID, edge) {
			health.CompatibilityStatus = "legacy_readable"
		}
	}
	sort.SliceStable(health.Findings, func(i, j int) bool {
		left := health.Findings[i].Severity + "\x00" + health.Findings[i].Code + "\x00" + health.Findings[i].ThreadID + "\x00" + strings.Join(health.Findings[i].NodeIDs, "\x00")
		right := health.Findings[j].Severity + "\x00" + health.Findings[j].Code + "\x00" + health.Findings[j].ThreadID + "\x00" + strings.Join(health.Findings[j].NodeIDs, "\x00")
		return left < right
	})
	return health
}

func evidenceTopicCapacity(threads []EvidenceResearchThread, unassigned []EvidenceResearchUnassigned) EvidenceTopicCapacity {
	byID := make(map[string]EvidenceResearchThread, len(threads))
	for _, thread := range threads {
		byID[thread.ID] = thread
	}
	rootID := func(thread EvidenceResearchThread) string {
		current := thread
		seen := map[string]bool{}
		for current.ParentThreadID != "" && !seen[current.ID] {
			seen[current.ID] = true
			parent, ok := byID[current.ParentThreadID]
			if !ok {
				break
			}
			current = parent
		}
		return current.ID
	}

	familyIndex := map[string]int{}
	families := make([]EvidenceTopicThreadFamily, 0)
	assignedSemanticNodeCount := 0
	for _, thread := range threads {
		nodeCount := evidenceResearchThreadNodeCount(thread)
		assignedSemanticNodeCount += nodeCount
		root := rootID(thread)
		index, ok := familyIndex[root]
		if !ok {
			rootThread := byID[root]
			index = len(families)
			familyIndex[root] = index
			families = append(families, EvidenceTopicThreadFamily{
				ID:           "family:" + strings.TrimPrefix(root, "thread:"),
				RootThreadID: root,
				Title:        rootThread.Title,
			})
		}
		families[index].ThreadIDs = append(families[index].ThreadIDs, thread.ID)
		families[index].ThreadCount++
		families[index].SemanticNodeCount += nodeCount
	}
	status := "healthy"
	reasons := make([]string, 0)
	// Thread count is not a semantic capacity. Only rendering load and a
	// growing unassigned inbox trigger Topic-level presentation advice.
	overThreads := false
	overAssignedNodes := assignedSemanticNodeCount > EvidenceTopicRecommendedMaxThreadNodes
	overUnassigned := len(unassigned) > EvidenceTopicRecommendedMaxUnassigned
	if overAssignedNodes {
		reasons = append(reasons, "presentation_load_high")
	}
	if overUnassigned {
		reasons = append(reasons, "unassigned_limit_exceeded")
	}
	if overAssignedNodes {
		if len(families) >= 3 {
			status = "split_recommended"
		} else {
			status = "near_limit"
			reasons = append(reasons, "large_but_semantically_coherent")
		}
	} else if overUnassigned {
		status = "cleanup_required"
	}
	if status == "healthy" {
		switch {
		case assignedSemanticNodeCount >= 80:
			status = "near_limit"
			reasons = append(reasons, "presentation_load_near")
		case len(unassigned) >= 6:
			status = "near_limit"
			reasons = append(reasons, "unassigned_limit_near")
		}
	}

	suggestedTopicCount := 1
	if status == "split_recommended" {
		suggestedTopicCount = len(families)
	}

	return EvidenceTopicCapacity{
		PolicyVersion:             EvidenceTopicCapacityPolicyVersion,
		Status:                    status,
		TooLarge:                  overThreads || overAssignedNodes || overUnassigned,
		SplitRecommended:          status == "split_recommended",
		ThreadCount:               len(threads),
		RootThreadCount:           len(families),
		ThreadNodeCount:           assignedSemanticNodeCount,
		UnassignedCount:           len(unassigned),
		RecommendedMaxThreads:     EvidenceTopicRecommendedMaxThreads,
		RecommendedMaxThreadNodes: EvidenceTopicRecommendedMaxThreadNodes,
		RecommendedMaxUnassigned:  EvidenceTopicRecommendedMaxUnassigned,
		SuggestedTopicCount:       suggestedTopicCount,
		Reasons:                   reasons,
		ThreadFamilies:            families,
	}
}

// BuildEvidenceResearchProjection derives a deterministic, read-only
// hypothesis-led research view. It never persists coordinates, synthetic
// edges, ownership, revisions, or any other semantic graph data.
func BuildEvidenceResearchProjection(chain EvidenceChain, graph EvidenceChainGraph) EvidenceResearchProjection {
	nodes := append([]EvidenceChainNode(nil), graph.Nodes...)
	edges := append([]EvidenceChainEdge(nil), graph.Edges...)
	sort.SliceStable(nodes, func(i, j int) bool { return evidenceResearchNodeKey(nodes[i]) < evidenceResearchNodeKey(nodes[j]) })
	sort.SliceStable(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })

	byID := make(map[string]EvidenceChainNode, len(nodes))
	relationCounts := make(map[string]int, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	for _, edge := range edges {
		if _, ok := byID[edge.SourceNodeID]; ok {
			relationCounts[edge.SourceNodeID]++
		}
		if _, ok := byID[edge.TargetNodeID]; ok {
			relationCounts[edge.TargetNodeID]++
		}
	}

	semanticEdges := make([]EvidenceChainEdge, 0, len(edges))
	for _, edge := range edges {
		if edge.Type == EvidenceEdgeRelatedTo || edge.Type == EvidenceEdgeCustom {
			continue
		}
		if _, sourceOK := byID[edge.SourceNodeID]; !sourceOK {
			continue
		}
		if _, targetOK := byID[edge.TargetNodeID]; !targetOK {
			continue
		}
		semanticEdges = append(semanticEdges, edge)
	}

	graphNodeIDs := make([]string, 0, len(nodes))
	adjacency := make(map[string]map[string]struct{})
	assignmentAdjacency := make(map[string]map[string]struct{})
	for _, node := range nodes {
		if node.Type == EvidenceNodeGroup {
			continue
		}
		graphNodeIDs = append(graphNodeIDs, node.ID)
		adjacency[node.ID] = map[string]struct{}{}
		assignmentAdjacency[node.ID] = map[string]struct{}{}
	}
	for _, edge := range semanticEdges {
		if adjacency[edge.SourceNodeID] == nil || adjacency[edge.TargetNodeID] == nil {
			continue
		}
		adjacency[edge.SourceNodeID][edge.TargetNodeID] = struct{}{}
		adjacency[edge.TargetNodeID][edge.SourceNodeID] = struct{}{}
		source := byID[edge.SourceNodeID]
		target := byID[edge.TargetNodeID]
		startsChild := evidenceResearchStartsChildThread(edge, source, target)
		if !startsChild {
			assignmentAdjacency[edge.SourceNodeID][edge.TargetNodeID] = struct{}{}
			assignmentAdjacency[edge.TargetNodeID][edge.SourceNodeID] = struct{}{}
		}
	}
	sort.Strings(graphNodeIDs)

	components := make([][]string, 0)
	seen := map[string]bool{}
	for _, start := range graphNodeIDs {
		if seen[start] {
			continue
		}
		component := make([]string, 0)
		queue := []string{start}
		seen[start] = true
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			component = append(component, current)
			neighbors := make([]string, 0, len(adjacency[current]))
			for next := range adjacency[current] {
				neighbors = append(neighbors, next)
			}
			sort.Strings(neighbors)
			for _, next := range neighbors {
				if seen[next] {
					continue
				}
				seen[next] = true
				queue = append(queue, next)
			}
		}
		sort.Strings(component)
		components = append(components, component)
	}

	threads := make([]EvidenceResearchThread, 0)
	ownerByNode := make(map[string]string)
	unassignedReason := make(map[string]string)
	for _, component := range components {
		componentSet := make(map[string]bool, len(component))
		roots := make([]EvidenceChainNode, 0)
		for _, id := range component {
			componentSet[id] = true
			if evidenceResearchNodeStage(byID[id]) == EvidenceResearchStageHypothesis {
				roots = append(roots, byID[id])
			}
		}
		sort.SliceStable(roots, func(i, j int) bool { return evidenceResearchNodeKey(roots[i]) < evidenceResearchNodeKey(roots[j]) })
		if len(roots) == 0 {
			for _, id := range component {
				unassignedReason[id] = "missing_hypothesis"
			}
			continue
		}

		distances := make(map[string]map[string]int, len(roots))
		for _, root := range roots {
			distance := map[string]int{root.ID: 0}
			queue := []string{root.ID}
			for len(queue) > 0 {
				current := queue[0]
				queue = queue[1:]
				neighbors := make([]string, 0, len(assignmentAdjacency[current]))
				for next := range assignmentAdjacency[current] {
					neighbors = append(neighbors, next)
				}
				sort.Strings(neighbors)
				for _, next := range neighbors {
					if !componentSet[next] {
						continue
					}
					if _, exists := distance[next]; exists {
						continue
					}
					distance[next] = distance[current] + 1
					queue = append(queue, next)
				}
			}
			distances[root.ID] = distance
		}

		threadIndex := make(map[string]int, len(roots))
		for _, root := range roots {
			thread := EvidenceResearchThread{
				ID:                 "thread:" + root.ID,
				Title:              strings.TrimSpace(root.Title),
				RootNodeID:         root.ID,
				ExplicitHypothesis: true,
				Stages:             emptyEvidenceResearchStages(),
			}
			if thread.Title == "" {
				thread.Title = "未命名研究线程"
			}
			threadIndex[root.ID] = len(threads)
			threads = append(threads, thread)
		}
		for _, id := range component {
			node := byID[id]
			stage := evidenceResearchNodeStage(node)
			if stage == "" {
				unassignedReason[id] = "unsupported_node_type"
				continue
			}
			bestRoot := roots[0]
			bestDistance, ok := distances[bestRoot.ID][id]
			if !ok {
				bestDistance = int(^uint(0) >> 1)
			}
			for _, candidate := range roots[1:] {
				distance, found := distances[candidate.ID][id]
				if !found {
					distance = int(^uint(0) >> 1)
				}
				if distance < bestDistance {
					bestRoot, bestDistance = candidate, distance
				}
			}
			index := threadIndex[bestRoot.ID]
			threads[index].Stages[stage] = append(threads[index].Stages[stage], evidenceResearchCard(node, relationCounts))
			ownerByNode[id] = threads[index].ID
		}
	}
	interpretationsByThread := evidenceResearchInterpretations(EvidenceChainGraph{Nodes: nodes, Edges: edges}, ownerByNode)
	for index := range threads {
		threads[index].Interpretations = interpretationsByThread[threads[index].ID]
	}

	threadByID := make(map[string]*EvidenceResearchThread, len(threads))
	for index := range threads {
		threadByID[threads[index].ID] = &threads[index]
	}
	protocolGroups := make([]EvidenceResearchProtocolGroup, 0)
	for _, group := range nodes {
		if !isEvidenceProtocolGroup(group) {
			continue
		}
		members := make([]EvidenceChainNode, 0)
		memberSet := map[string]bool{}
		for _, node := range nodes {
			if evidenceResearchGroupID(node) != group.ID {
				continue
			}
			members = append(members, node)
			memberSet[node.ID] = true
		}
		sort.SliceStable(members, func(i, j int) bool { return evidenceResearchNodeKey(members[i]) < evidenceResearchNodeKey(members[j]) })
		protocolGroup := EvidenceResearchProtocolGroup{Group: group}
		for _, member := range members {
			protocolGroup.Members = append(protocolGroup.Members, EvidenceResearchProtocolMember{
				Node:     member,
				ThreadID: ownerByNode[member.ID],
			})
		}
		for _, edge := range edges {
			if !memberSet[edge.SourceNodeID] && !memberSet[edge.TargetNodeID] {
				continue
			}
			scope := "external"
			if memberSet[edge.SourceNodeID] && memberSet[edge.TargetNodeID] {
				scope = "internal"
			}
			protocolGroup.Relations = append(protocolGroup.Relations, EvidenceResearchProtocolRelation{Edge: edge, Scope: scope})
		}
		protocolGroups = append(protocolGroups, protocolGroup)
	}

	crossRelations := make([]EvidenceResearchCrossRelation, 0)
	for _, edge := range semanticEdges {
		sourceOwner := ownerByNode[edge.SourceNodeID]
		targetOwner := ownerByNode[edge.TargetNodeID]
		if sourceOwner == "" || targetOwner == "" || sourceOwner == targetOwner {
			continue
		}
		kind := "causal"
		if evidenceResearchStartsChildThread(edge, byID[edge.SourceNodeID], byID[edge.TargetNodeID]) {
			kind = "branch"
			if child := threadByID[targetOwner]; child != nil && child.ParentThreadID == "" {
				child.ParentThreadID = sourceOwner
			}
		}
		crossRelations = append(crossRelations, EvidenceResearchCrossRelation{Edge: edge, SourceThreadID: sourceOwner, TargetThreadID: targetOwner, Kind: kind})
	}

	for index := range threads {
		for _, stage := range evidenceResearchStageOrder {
			sort.SliceStable(threads[index].Stages[stage], func(i, j int) bool {
				return evidenceResearchNodeKey(threads[index].Stages[stage][i].Node) < evidenceResearchNodeKey(threads[index].Stages[stage][j].Node)
			})
		}
	}
	threadSortKey := func(thread EvidenceResearchThread) string { return evidenceResearchNodeKey(byID[thread.RootNodeID]) }
	children := make(map[string][]EvidenceResearchThread)
	roots := make([]EvidenceResearchThread, 0)
	for _, thread := range threads {
		if thread.ParentThreadID != "" && threadByID[thread.ParentThreadID] != nil {
			children[thread.ParentThreadID] = append(children[thread.ParentThreadID], thread)
		} else {
			roots = append(roots, thread)
		}
	}
	for id := range children {
		sort.SliceStable(children[id], func(i, j int) bool { return threadSortKey(children[id][i]) < threadSortKey(children[id][j]) })
	}
	sort.SliceStable(roots, func(i, j int) bool { return threadSortKey(roots[i]) < threadSortKey(roots[j]) })
	ordered := make([]EvidenceResearchThread, 0, len(threads))
	visitedThreads := map[string]bool{}
	var appendThread func(EvidenceResearchThread)
	appendThread = func(thread EvidenceResearchThread) {
		if visitedThreads[thread.ID] {
			return
		}
		visitedThreads[thread.ID] = true
		ordered = append(ordered, thread)
		for _, child := range children[thread.ID] {
			appendThread(child)
		}
	}
	for _, thread := range roots {
		appendThread(thread)
	}
	remaining := append([]EvidenceResearchThread(nil), threads...)
	sort.SliceStable(remaining, func(i, j int) bool { return threadSortKey(remaining[i]) < threadSortKey(remaining[j]) })
	for _, thread := range remaining {
		appendThread(thread)
	}
	sort.SliceStable(protocolGroups, func(i, j int) bool {
		return evidenceResearchNodeKey(protocolGroups[i].Group) < evidenceResearchNodeKey(protocolGroups[j].Group)
	})

	unassigned := make([]EvidenceResearchUnassigned, 0)
	for _, node := range nodes {
		if ownerByNode[node.ID] != "" {
			continue
		}
		if evidenceResearchProvenanceOnly(node) {
			continue
		}
		reason := unassignedReason[node.ID]
		if reason == "" {
			reason = "not_connected_to_thread"
		}
		unassigned = append(unassigned, EvidenceResearchUnassigned{Card: evidenceResearchCard(node, relationCounts), Reason: reason})
	}
	sort.SliceStable(unassigned, func(i, j int) bool {
		return evidenceResearchNodeKey(unassigned[i].Card.Node) < evidenceResearchNodeKey(unassigned[j].Card.Node)
	})

	projection := EvidenceResearchProjection{
		ContractVersion:      EvidenceResearchContractVersion,
		ChainID:              chain.ID,
		Revision:             chain.Revision,
		GraphHash:            chain.GraphHash,
		Stages:               append([]string(nil), evidenceResearchStageOrder...),
		PresentationStages:   append([]string(nil), evidenceResearchPresentationStageOrder...),
		Threads:              ordered,
		Unassigned:           unassigned,
		CrossThreadRelations: crossRelations,
		ProtocolGroups:       protocolGroups,
		OwnerByNode:          ownerByNode,
	}
	projection.Capacity = evidenceTopicCapacity(projection.Threads, projection.Unassigned)
	projection.StructuralHealth = buildEvidenceResearchStructuralHealth(chain, EvidenceChainGraph{Nodes: nodes, Edges: edges}, projection)
	return projection
}
