import { describe, expect, it } from "vitest";
import {
  apiNodeToFlowNode,
  apiResearchProjectionToFlow,
  buildEvidenceEditPatch,
  candidateMatches,
  candidateToNode,
  defaultEvidenceRelation,
  edgeStyle,
  edgeTypeLabel,
  evidenceAuthoringNodeTypes,
  evidenceAdjacentStageRelations,
  evidenceThreadRelationFocus,
  orderEvidenceThreadStages,
  evidenceNeighborhood,
  evidenceReadingSections,
  evidenceResearchThreads,
  layoutEvidenceNeighborhood,
  evidenceAutoHandlePair,
  evidenceGroupFrameBounds,
  evidenceMapReferenceStatus,
  evidenceMarkerEnd,
  evidenceProposalPreview,
  evidenceWorkspaceProposalPreview,
  filterRunCandidatesForProject,
  groupRunCandidatesByProject,
  isProtocolGroupMemberType,
  layoutEvidenceGraph,
  layoutEvidenceGraphFromIntent,
  normalizeEvidenceLayoutIntent,
  nodeTypeLabel,
  prepareLoadedEvidenceGraph,
  proposalNoticeScopeKey,
  projectEvidenceResearchInterpretations,
  inspectProtocolFrameMigration,
  convertProtocolToFrame,
  projectEvidenceGroups,
  protocolContainerMoveDeltaForKey,
  remapEvidenceLayoutIntent,
  resolveProjectEvidenceChainSelection,
  serializeEvidenceGraph,
  shouldOverlayEvidenceProposal,
  translateProtocolContainer,
  type EvidenceFlowEdge,
  type EvidenceFlowNode
} from "./evidenceChain";
import type { EvidenceNodeType } from "./types";

describe("evidenceChain helpers", () => {
  it("labels canonical claim-backed hypotheses as hypotheses", () => {
    expect(nodeTypeLabel("claim", "hypothesis")).toBe("假说");
    expect(nodeTypeLabel("claim", "result")).toBe("实验结果");
    expect(nodeTypeLabel("claim")).toBe("实验结果");
  });

  it("changes the proposal notice scope when its map, proposal, or revision changes", () => {
    const current = proposalNoticeScopeKey("chain_topic", "proposal_1", 6);
    expect(proposalNoticeScopeKey("chain_topic", "proposal_2", 6)).not.toBe(current);
    expect(proposalNoticeScopeKey("chain_topic", "proposal_1", 7)).not.toBe(current);
    expect(proposalNoticeScopeKey("chain_primary", "proposal_1", 6)).not.toBe(current);
    expect(proposalNoticeScopeKey("chain_topic", "proposal_1", 6)).toBe(current);
  });

  it("selects the cached Primary Map when a project route omits the map query", () => {
    const chains = [
      { id: "chain_primary", role: "primary" as const, status: "active" as const },
      { id: "chain_topic", role: "secondary" as const, status: "active" as const }
    ];

    expect(resolveProjectEvidenceChainSelection({ chains })).toBe("chain_primary");
    expect(resolveProjectEvidenceChainSelection({ chains, primaryId: "chain_primary" })).toBe("chain_primary");
  });

  it("preserves a valid Topic deep link and replaces a stale one", () => {
    const chains = [
      { id: "chain_primary", role: "primary" as const, status: "active" as const },
      { id: "chain_topic", role: "secondary" as const, status: "active" as const }
    ];

    expect(resolveProjectEvidenceChainSelection({ requestedId: "chain_topic", chains })).toBe("chain_topic");
    expect(resolveProjectEvidenceChainSelection({ requestedId: "chain_deleted", chains })).toBe("chain_primary");
  });

  it("keeps discovery pending instead of misreporting a missing Map", () => {
    expect(resolveProjectEvidenceChainSelection({})).toBeUndefined();
    expect(resolveProjectEvidenceChainSelection({ requestedId: "chain_topic" })).toBe("chain_topic");
    expect(resolveProjectEvidenceChainSelection({ chains: [] })).toBe("");
  });

  it("offers only the canonical research authoring nodes", () => {
    expect(evidenceAuthoringNodeTypes).toEqual([
      "hypothesis",
      "experiment",
      "claim",
      "conclusion",
      "issue",
      "note"
    ]);
    expect(evidenceAuthoringNodeTypes).not.toContain("protocol");
    expect(evidenceAuthoringNodeTypes).not.toContain("group");
    expect(evidenceAuthoringNodeTypes).not.toContain("dataset");
    expect(evidenceAuthoringNodeTypes).not.toContain("plan");
  });

  it("projects only direct neighboring-stage relations for swimlane hover", () => {
    const node = (id: string, type: EvidenceNodeType, title: string): EvidenceFlowNode => ({
      id,
      type: "evidence",
      position: { x: 0, y: 0 },
      data: { type, title, body: "" }
    });
    const hypothesis = node("hypothesis", "hypothesis", "Hypothesis");
    const design = node("design", "experiment", "Controlled design");
    const result = node("result", "claim", "Observed result");
    const conclusion = node("conclusion", "conclusion", "Conclusion");
    const thread = {
      id: "thread",
      title: "Thread",
      rootNodeId: hypothesis.id,
      explicitHypothesis: true,
      stages: {
        hypothesis: [hypothesis], design: [design], result: [result], conclusion: [conclusion], issue: []
      }
    };
    const edge = (id: string, source: string, target: string, label: string): EvidenceFlowEdge => ({
      id, source, target, type: "evidence", label,
      data: { type: "related_to", rationale: "" }
    });
    const relations = evidenceAdjacentStageRelations(thread, [
      edge("design-result", design.id, result.id, "produced"),
      edge("hypothesis-result", hypothesis.id, result.id, "long range"),
      { ...edge("draft", result.id, conclusion.id, "draft"), data: { type: "supports", rationale: "", draft: true } }
    ]);

    expect(relations.get(design.id)).toHaveLength(1);
    expect(relations.get(design.id)?.[0]).toMatchObject({ direction: "outgoing", otherNode: { id: result.id }, otherStage: "result" });
    expect(relations.get(result.id)?.[0]).toMatchObject({ direction: "incoming", otherNode: { id: design.id }, otherStage: "design" });
    expect(relations.has(hypothesis.id)).toBe(false);
    expect(relations.has(conclusion.id)).toBe(false);
  });

  it("keeps cause/effect focus explicit for connected, disconnected, and filtered cards", () => {
    const node = (id: string, type: EvidenceNodeType): EvidenceFlowNode => ({
      id, type: "evidence", position: { x: 0, y: 0 }, data: { type, title: id, body: "" }
    });
    const hypothesis = node("hypothesis", "hypothesis");
    const design = node("design", "experiment");
    const result = node("result", "claim");
    const isolated = node("isolated", "issue");
    const thread = {
      id: "thread", title: "Thread", rootNodeId: hypothesis.id, explicitHypothesis: true,
      stages: { hypothesis: [hypothesis], design: [design], result: [result], conclusion: [], issue: [isolated] }
    };
    const edges: EvidenceFlowEdge[] = [
      { id: "h-d", source: hypothesis.id, target: design.id, data: { type: "next_step", rationale: "" } },
      { id: "d-r", source: design.id, target: result.id, data: { type: "next_step", rationale: "" } }
    ];

    expect(evidenceThreadRelationFocus(thread, thread, edges, design.id)).toEqual({
      originNodeId: design.id,
      visiblePeerNodeIds: [hypothesis.id, result.id],
      directRelationCount: 2,
      hiddenRelationCount: 0,
      disconnected: false
    });
    expect(evidenceThreadRelationFocus(thread, thread, edges, hypothesis.id)).toMatchObject({
      visiblePeerNodeIds: [design.id, result.id],
      directRelationCount: 1,
      hiddenRelationCount: 0,
      disconnected: false
    });
    expect(evidenceThreadRelationFocus(thread, thread, edges, isolated.id)).toMatchObject({
      originNodeId: isolated.id, visiblePeerNodeIds: [], directRelationCount: 0, hiddenRelationCount: 0, disconnected: true
    });
    const filtered = { ...thread, stages: { ...thread.stages, result: [] } };
    expect(evidenceThreadRelationFocus(thread, filtered, edges, design.id)).toMatchObject({
      visiblePeerNodeIds: [hypothesis.id], directRelationCount: 2, hiddenRelationCount: 1, disconnected: false
    });
  });

  it("keeps a real cross-stage edge focusable when no interpretation card represents it", () => {
    const result: EvidenceFlowNode = {
      id: "result",
      type: "evidence",
      position: { x: 0, y: 0 },
      data: { type: "claim", claimKind: "result", title: "Observed result", body: "" }
    };
    const issue: EvidenceFlowNode = {
      id: "issue",
      type: "evidence",
      position: { x: 0, y: 0 },
      data: { type: "issue", title: "Observed limitation", body: "" }
    };
    const thread = {
      id: "thread",
      title: "Thread",
      rootNodeId: result.id,
      explicitHypothesis: false,
      stages: { hypothesis: [], design: [], result: [result], conclusion: [], issue: [issue] }
    };
    const edges: EvidenceFlowEdge[] = [{
      id: "result-issue",
      source: result.id,
      target: issue.id,
      data: { type: "related_to", rationale: "legacy direct relation" }
    }];

    expect(evidenceThreadRelationFocus(thread, thread, edges, issue.id)).toMatchObject({
      originNodeId: issue.id,
      visiblePeerNodeIds: [result.id],
      directRelationCount: 1,
      hiddenRelationCount: 0,
      disconnected: false
    });
  });

  it("keeps a projected Result outcome split across the interpretation card", () => {
    const result: EvidenceFlowNode = {
      id: "result",
      type: "evidence",
      position: { x: 0, y: 0 },
      data: { type: "claim", claimKind: "result", title: "Observed result", body: "" }
    };
    const interpretation: EvidenceFlowNode = {
      id: "interpretation",
      type: "evidence",
      position: { x: 0, y: 0 },
      data: {
        type: "note",
        title: "Evidence is inconclusive",
        body: "bounded limitation",
        projectionOnly: "interpretation",
        interpretationSourceNodeId: result.id,
        interpretationOutcomeNodeId: "issue",
        interpretationEdgeId: "result-issue"
      }
    };
    const issue: EvidenceFlowNode = {
      id: "issue",
      type: "evidence",
      position: { x: 0, y: 0 },
      data: { type: "issue", title: "Observed limitation", body: "" }
    };
    const thread = {
      id: "thread",
      title: "Thread",
      rootNodeId: result.id,
      explicitHypothesis: false,
      stages: { hypothesis: [], design: [], result: [result], conclusion: [interpretation], issue: [issue] }
    };
    const edges: EvidenceFlowEdge[] = [{
      id: "result-issue",
      source: result.id,
      target: issue.id,
      data: { type: "reveals_issue", rationale: "bounded limitation" }
    }];

    expect(evidenceThreadRelationFocus(thread, thread, edges, result.id)).toMatchObject({
      visiblePeerNodeIds: [interpretation.id, issue.id], directRelationCount: 1, disconnected: false
    });
    expect(evidenceThreadRelationFocus(thread, thread, edges, issue.id)).toMatchObject({
      visiblePeerNodeIds: [interpretation.id, result.id], directRelationCount: 1, disconnected: false
    });
  });

  it("does not traverse through neutral context while highlighting the causal chain", () => {
    const node = (id: string): EvidenceFlowNode => ({
      id, type: "evidence", position: { x: 0, y: 0 }, data: { type: "claim", title: id, body: "" }
    });
    const hypothesis = node("hypothesis");
    const design = node("design");
    const result = node("result");
    const context = node("context");
    const contextTail = node("context-tail");
    const thread = {
      id: "thread", title: "Thread", rootNodeId: hypothesis.id, explicitHypothesis: true,
      stages: { hypothesis: [hypothesis, context, contextTail], design: [design], result: [result], conclusion: [], issue: [] }
    };
    const edges: EvidenceFlowEdge[] = [
      { id: "h-d", source: hypothesis.id, target: design.id, data: { type: "next_step", rationale: "" } },
      { id: "d-r", source: design.id, target: result.id, data: { type: "next_step", rationale: "" } },
      { id: "h-context", source: hypothesis.id, target: context.id, data: { type: "related_to", rationale: "" } },
      { id: "context-tail", source: context.id, target: contextTail.id, data: { type: "next_step", rationale: "" } }
    ];

    expect(evidenceThreadRelationFocus(thread, thread, edges, hypothesis.id).visiblePeerNodeIds).toEqual([
      context.id, design.id, result.id
    ]);
    expect(evidenceThreadRelationFocus(thread, thread, edges, design.id).visiblePeerNodeIds).toEqual([
      hypothesis.id, result.id
    ]);
  });

  it("highlights lineage through the selected card without leaking into sibling branches", () => {
    const node = (id: string, type: EvidenceNodeType): EvidenceFlowNode => ({
      id, type: "evidence", position: { x: 0, y: 0 }, data: { type, title: id, body: "" }
    });
    const hypothesis = node("hypothesis", "hypothesis");
    const designA = node("design-a", "experiment");
    const designB = node("design-b", "experiment");
    const resultA = node("result-a", "claim");
    const resultB = node("result-b", "claim");
    const conclusionA = node("conclusion-a", "conclusion");
    const conclusionB = node("conclusion-b", "conclusion");
    const thread = {
      id: "thread", title: "Thread", rootNodeId: hypothesis.id, explicitHypothesis: true,
      stages: {
        hypothesis: [hypothesis],
        design: [designA, designB],
        result: [resultA, resultB],
        conclusion: [conclusionA, conclusionB],
        issue: []
      }
    };
    const edges: EvidenceFlowEdge[] = [
      { id: "h-da", source: hypothesis.id, target: designA.id, data: { type: "next_step", rationale: "" } },
      { id: "da-ra", source: designA.id, target: resultA.id, data: { type: "next_step", rationale: "" } },
      { id: "ra-ca", source: resultA.id, target: conclusionA.id, data: { type: "supports", rationale: "" } },
      { id: "h-db", source: hypothesis.id, target: designB.id, data: { type: "next_step", rationale: "" } },
      { id: "db-rb", source: designB.id, target: resultB.id, data: { type: "next_step", rationale: "" } },
      { id: "rb-cb", source: resultB.id, target: conclusionB.id, data: { type: "supports", rationale: "" } }
    ];

    expect(evidenceThreadRelationFocus(thread, thread, edges, resultA.id).visiblePeerNodeIds).toEqual([
      conclusionA.id, designA.id, hypothesis.id
    ]);
  });

  it("renders the four canonical Result outcome fixtures", () => {
    const cases = [
      { name: "positive", disposition: "conclusion", outcomes: [{ id: "c", type: "conclusion", edge: "supports" }], labels: ["证据支持结论"] },
      { name: "stable-negative", disposition: "conclusion", outcomes: [{ id: "c", type: "conclusion", edge: "does_not_prove" }], labels: ["证据尚不足以证明"] },
      { name: "mixed", disposition: "mixed", outcomes: [{ id: "c", type: "conclusion", edge: "supports" }, { id: "i", type: "issue", edge: "reveals_issue" }], labels: ["证据支持结论", "同时暴露限制"] },
      { name: "issue-driven", disposition: "issue", outcomes: [{ id: "i", type: "issue", edge: "reveals_issue" }], labels: ["暂不能形成结论"] }
    ] as const;
    for (const fixture of cases) {
      const hypothesis: EvidenceFlowNode = { id: `h-${fixture.name}`, type: "evidence", position: { x: 0, y: 0 }, data: { type: "hypothesis", title: "Hypothesis", body: "" } };
      const result: EvidenceFlowNode = { id: `r-${fixture.name}`, type: "evidence", position: { x: 0, y: 0 }, data: { type: "claim", claimKind: "result", resultDisposition: fixture.disposition, dispositionReason: fixture.disposition === "conclusion" ? "" : "bounded limitation", title: "Result", body: "" } };
      const outcomeNodes = fixture.outcomes.map((outcome): EvidenceFlowNode => ({ id: `${outcome.id}-${fixture.name}`, type: "evidence", position: { x: 0, y: 0 }, data: { type: outcome.type, title: outcome.id, body: "" } }));
      const edges: EvidenceFlowEdge[] = [
        { id: `h-r-${fixture.name}`, source: hypothesis.id, target: result.id, data: { type: "next_step", rationale: "" } },
        ...fixture.outcomes.map((outcome, index): EvidenceFlowEdge => ({ id: `${outcome.edge}-${fixture.name}`, source: result.id, target: outcomeNodes[index].id, data: { type: outcome.edge, rationale: "bounded" } }))
      ];
      const projected = projectEvidenceResearchInterpretations(evidenceResearchThreads([hypothesis, result, ...outcomeNodes], edges), edges);
      expect(projected.threads[0].stages.conclusion.map((node) => node.data.title).sort(), fixture.name).toEqual([...fixture.labels].sort());
    }
  });

  it("keeps related experiment designs and results vertically close", () => {
    const node = (id: string, type: EvidenceNodeType): EvidenceFlowNode => ({
      id,
      type: "evidence",
      position: { x: 0, y: 0 },
      data: { type, title: id, body: "" }
    });
    const hypothesis = node("hypothesis", "hypothesis");
    const designA = node("design-a", "experiment");
    const designB = node("design-b", "experiment");
    const resultA = node("result-a", "claim");
    const resultB = node("result-b", "claim");
    const thread = {
      id: "thread",
      title: "Thread",
      rootNodeId: hypothesis.id,
      explicitHypothesis: true,
      stages: {
        hypothesis: [hypothesis],
        design: [designA, designB],
        result: [resultA, resultB],
        conclusion: [],
        issue: []
      }
    };
    const edges: EvidenceFlowEdge[] = [
      { id: "a", source: designA.id, target: resultB.id, data: { type: "related_to", rationale: "" } },
      { id: "b", source: designB.id, target: resultA.id, data: { type: "related_to", rationale: "" } }
    ];

    const ordered = orderEvidenceThreadStages(thread, edges);
    expect(ordered.stages.design.map((item) => item.id)).toEqual([designA.id, designB.id]);
    expect(ordered.stages.result.map((item) => item.id)).toEqual([resultB.id, resultA.id]);
    expect(orderEvidenceThreadStages(thread, [...edges].reverse()).stages.result.map((item) => item.id))
      .toEqual([resultB.id, resultA.id]);
  });

  it("separates layout-only changes from reviewable semantic edits", () => {
    const original = {
      nodes: [{ id: "claim", type: "claim" as const, title: "Claim", body: "", x: 1, y: 2, width: 100, height: 80, pinned: false, data_json: "{}" }],
      edges: []
    };
    const layout = { nodes: [{ ...original.nodes[0], x: 500, y: 600, pinned: true }], edges: [] };
    expect(buildEvidenceEditPatch("chain", original, layout)).toBeNull();
    const semantic = { nodes: [{ ...layout.nodes[0], title: "Changed claim" }], edges: [] };
    expect(buildEvidenceEditPatch("chain", original, semantic)).toMatchObject({
      chain_id: "chain",
      upsert_nodes: [{ id: "claim", title: "Changed claim" }]
    });

    const collapsed = {
      nodes: [{ ...layout.nodes[0], data_json: `{"collapsed":true}` }],
      edges: []
    };
    expect(buildEvidenceEditPatch("chain", original, collapsed)).toBeNull();
    const grouped = {
      nodes: [{ ...layout.nodes[0], data_json: `{"groupId":"protocol_group"}` }],
      edges: []
    };
    expect(buildEvidenceEditPatch("chain", original, grouped)).toMatchObject({
      chain_id: "chain",
      upsert_nodes: [{ id: "claim", data_json: `{"groupId":"protocol_group"}` }]
    });
  });

  it("matches run candidates by project card and run fields", () => {
    const candidate = {
      id: "card:one",
      kind: "project_card" as const,
      run_id: "run_abc",
      project_card_id: "card_one",
      question: "Does IR-anchor fusion help?",
      key_metrics: "mAP50-95=0.606",
      run: { id: "run_abc", resource_id: "mu", name: "fusion formal", status: "succeeded", command: "python train.py" }
    };

    expect(candidateMatches(candidate, "anchor")).toBe(true);
    expect(candidateMatches(candidate, "fusion formal")).toBe(true);
    expect(candidateMatches(candidate, "unrelated")).toBe(false);
  });

  it("maps edge labels", () => {
    expect(edgeTypeLabel("supports")).toBe("增强");
    expect(edgeTypeLabel("weakens")).toBe("削弱");
    expect(edgeTypeLabel("does_not_prove")).toBe("不能证明");
    expect(edgeTypeLabel("next_step")).toBe("下一步");
    expect(edgeTypeLabel("custom")).toBe("自定义");
  });

  it("chooses a valid typed relation for new connections", () => {
    expect(defaultEvidenceRelation("dataset", "run")).toBe("uses");
    expect(defaultEvidenceRelation("run", "claim")).toBe("supports");
    expect(defaultEvidenceRelation("run", "issue")).toBe("reveals_issue");
    expect(defaultEvidenceRelation("issue", "plan")).toBe("next_step");
    expect(defaultEvidenceRelation("claim", "claim")).toBe("supersedes");
    expect(defaultEvidenceRelation("note", "dataset")).toBe("related_to");
  });

  it("groups the default evidence reading view by research meaning", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "claim", type: "evidence", position: { x: 0, y: 0 }, data: { type: "claim", title: "Current conclusion", body: "supported" } },
      { id: "issue", type: "evidence", position: { x: 0, y: 0 }, data: { type: "issue", title: "Open problem", body: "blocked" } },
      { id: "plan", type: "evidence", position: { x: 0, y: 0 }, data: { type: "plan", title: "Next test", body: "run later" } },
      { id: "dataset", type: "evidence", position: { x: 0, y: 0 }, data: { type: "dataset", title: "Clean v1", body: "verified" } },
      { id: "draft", type: "evidence", position: { x: 0, y: 0 }, data: { type: "claim", title: "Pending", body: "", draft: true } }
    ];

    expect(evidenceReadingSections(nodes).map((section) => [section.id, section.nodes.map((node) => node.id)])).toEqual([
      ["claims", ["claim"]],
      ["issues", ["issue"]],
      ["plans", ["plan"]],
      ["context", ["dataset"]]
    ]);
    expect(evidenceReadingSections(nodes, "verified")[0].nodes.map((node) => node.id)).toEqual(["dataset"]);
  });

  it("derives stable research lanes while keeping neutral links out of the thread spine", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "hypothesis", type: "evidence", position: { x: 90, y: 40 }, data: { type: "claim", claimKind: "hypothesis", title: "H1", body: "" } },
      { id: "plan", type: "evidence", position: { x: 10, y: 20 }, data: { type: "plan", title: "Plan", body: "" } },
      { id: "run", type: "evidence", position: { x: 20, y: 20 }, data: { type: "run", title: "Run", body: "", groupId: "protocol" } },
      { id: "dataset", type: "evidence", position: { x: 30, y: 20 }, data: { type: "dataset", title: "Dataset", body: "", groupId: "protocol" } },
      { id: "protocol", type: "evidence", position: { x: 40, y: 20 }, data: { type: "group", groupKind: "protocol", title: "Frozen protocol", body: "" } },
      { id: "result", type: "evidence", position: { x: 50, y: 20 }, data: { type: "claim", claimKind: "result", title: "Result", body: "" } },
      { id: "issue", type: "evidence", position: { x: 60, y: 20 }, data: { type: "issue", title: "Issue", body: "" } },
      { id: "note", type: "evidence", position: { x: 70, y: 20 }, data: { type: "note", title: "Background", body: "" } }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "h-p", source: "hypothesis", target: "plan", data: { type: "next_step", rationale: "" } },
      { id: "p-r", source: "plan", target: "run", data: { type: "next_step", rationale: "" } },
      { id: "r-c", source: "run", target: "result", data: { type: "supports", rationale: "" } },
      { id: "c-i", source: "result", target: "issue", data: { type: "reveals_issue", rationale: "" } },
      { id: "neutral", source: "note", target: "result", data: { type: "related_to", rationale: "" } }
    ];

    const projection = evidenceResearchThreads(nodes, edges);
    expect(projection.threads).toHaveLength(1);
    expect(projection.threads[0].stages.hypothesis.map((node) => node.id)).toEqual(["hypothesis"]);
    expect(projection.threads[0].stages.design.map((node) => node.id)).toEqual(["plan"]);
    expect(projection.threads[0].stages.result.map((node) => node.id)).toEqual(["result"]);
    expect(projection.threads[0].stages.conclusion).toEqual([]);
    expect(projection.threads[0].stages.issue.map((node) => node.id)).toEqual(["issue"]);
    expect(projection.unassigned.map((node) => node.id)).toEqual(["note"]);
    expect(nodes.find((node) => node.id === "hypothesis")?.position).toEqual({ x: 90, y: 40 });
  });

  it("keeps research-thread projection deterministic with branches, cycles and reversed input", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "h-a", type: "evidence", position: { x: 0, y: 0 }, data: { type: "hypothesis", title: "A", body: "" } },
      { id: "plan-a", type: "evidence", position: { x: 0, y: 0 }, data: { type: "plan", title: "A plan", body: "" } },
      { id: "issue", type: "evidence", position: { x: 0, y: 0 }, data: { type: "issue", title: "Fork", body: "" } },
      { id: "h-b", type: "evidence", position: { x: 0, y: 0 }, data: { type: "hypothesis", title: "B", body: "" } },
      { id: "plan-b", type: "evidence", position: { x: 0, y: 0 }, data: { type: "plan", title: "B plan", body: "" } }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "a-plan", source: "h-a", target: "plan-a", data: { type: "next_step", rationale: "" } },
      { id: "plan-issue", source: "plan-a", target: "issue", data: { type: "reveals_issue", rationale: "" } },
      { id: "issue-b", source: "issue", target: "h-b", data: { type: "next_step", rationale: "" } },
      { id: "b-plan", source: "h-b", target: "plan-b", data: { type: "next_step", rationale: "" } },
      { id: "cycle", source: "plan-b", target: "issue", data: { type: "weakens", rationale: "" } }
    ];
    const summarize = (projection: ReturnType<typeof evidenceResearchThreads>) => projection.threads.map((thread) => ({
      id: thread.id,
      parent: thread.parentThreadId,
      nodes: Object.values(thread.stages).flat().map((node) => node.id)
    }));
    const forward = evidenceResearchThreads(nodes, edges);
    const reversed = evidenceResearchThreads([...nodes].reverse(), [...edges].reverse());
    expect(summarize(reversed)).toEqual(summarize(forward));
    expect(new Set(summarize(forward).flatMap((thread) => thread.nodes)).size).toBe(5);
    expect(forward.threads.find((thread) => thread.rootNodeId === "h-b")?.parentThreadId).toBe("thread:h-a");
  });

  it("orders deep next-step branches directly after their parent and ignores non-next-step links", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "h-root", type: "evidence", position: { x: 0, y: 0 }, data: { type: "hypothesis", title: "Root", body: "" } },
      { id: "issue-root", type: "evidence", position: { x: 0, y: 0 }, data: { type: "issue", title: "Root issue", body: "" } },
      { id: "h-child", type: "evidence", position: { x: 0, y: 0 }, data: { type: "hypothesis", title: "Child", body: "" } },
      { id: "issue-child", type: "evidence", position: { x: 0, y: 0 }, data: { type: "issue", title: "Child issue", body: "" } },
      { id: "h-grandchild", type: "evidence", position: { x: 0, y: 0 }, data: { type: "hypothesis", title: "Grandchild", body: "" } },
      { id: "issue-independent", type: "evidence", position: { x: 0, y: 0 }, data: { type: "issue", title: "Independent issue", body: "" } },
      { id: "h-independent", type: "evidence", position: { x: 0, y: 0 }, data: { type: "hypothesis", title: "Independent", body: "" } }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "root-issue", source: "h-root", target: "issue-root", data: { type: "reveals_issue", rationale: "" } },
      { id: "root-child", source: "issue-root", target: "h-child", data: { type: "next_step", rationale: "" } },
      { id: "child-issue", source: "h-child", target: "issue-child", data: { type: "reveals_issue", rationale: "" } },
      { id: "child-grandchild", source: "issue-child", target: "h-grandchild", data: { type: "next_step", rationale: "" } },
      { id: "not-a-branch", source: "issue-independent", target: "h-independent", data: { type: "supports", rationale: "" } }
    ];
    const projection = evidenceResearchThreads([...nodes].reverse(), [...edges].reverse());
    expect(projection.threads.map((thread) => thread.rootNodeId)).toEqual([
      "h-independent",
      "h-root",
      "h-child",
      "h-grandchild"
    ]);
    expect(projection.threads.find((thread) => thread.rootNodeId === "h-child")?.parentThreadId).toBe("thread:h-root");
    expect(projection.threads.find((thread) => thread.rootNodeId === "h-grandchild")?.parentThreadId).toBe("thread:h-child");
    expect(projection.threads.find((thread) => thread.rootNodeId === "h-independent")?.parentThreadId).toBeUndefined();
  });

  it("adapts the shared server thread projection while retaining legacy protocol diagnostics", () => {
    const projection = apiResearchProjectionToFlow({
      evidence_contract_version: "research-thread-v2",
      chain_id: "chain",
      revision: 4,
      graph_hash: "hash-r4",
      stage_order: ["hypothesis", "design", "result", "conclusion", "issue"],
      threads: [{
        id: "thread:h",
        title: "Hypothesis",
        root_node_id: "h",
        explicit_hypothesis: true,
        stages: {
          hypothesis: [{ node: { id: "h", type: "hypothesis", title: "Hypothesis", x: 999, y: 888 }, relation_count: 1 }],
          design: [{
            node: { id: "design", type: "experiment", title: "Matched design", x: 0, y: 0 },
            relation_count: 5,
          }],
          result: [{ node: { id: "result", type: "claim", title: "Observed", x: 0, y: 0, data_json: `{"claimKind":"result","resultDisposition":"conclusion"}` }, relation_count: 1 }],
          conclusion: [{ node: { id: "conclusion", type: "conclusion", title: "Supported", x: 0, y: 0 }, relation_count: 1 }],
          issue: []
        },
        interpretations: [{ id: "interpretation:result-conclusion", result_node_id: "result", outcome_node_id: "conclusion", outcome_type: "conclusion", kind: "supports", label: "证据支持结论", edge_id: "result-conclusion" }]
      }, {
        id: "thread:child",
        title: "Child",
        root_node_id: "child",
        parent_thread_id: "thread:h",
        explicit_hypothesis: true,
        stages: {
          hypothesis: [{ node: { id: "child", type: "hypothesis", title: "Child", x: 0, y: 0 }, relation_count: 1 }],
          design: [], result: [], conclusion: [], issue: []
        }
      }],
      unassigned: [{ card: { node: { id: "note", type: "note", title: "Loose", x: 0, y: 0 }, relation_count: 0 }, reason: "missing_hypothesis" }],
      cross_thread_relations: [{
        edge: { id: "branch", source_node_id: "h", target_node_id: "child", type: "next_step", label: "new branch" },
        source_thread_id: "thread:h",
        target_thread_id: "thread:child",
        kind: "branch"
      }],
      protocol_groups: [{
        group: { id: "protocol", type: "group", title: "Matched protocol", x: 0, y: 0, data_json: `{"groupKind":"protocol"}` },
        members: [{
          node: { id: "seed-1", type: "run", title: "seed 1", run_id: "run_seed_1", x: 0, y: 0, data_json: `{"groupId":"protocol"}` },
          thread_id: "thread:h"
        }],
        relations: [{
          edge: { id: "seed-result", source_node_id: "seed-1", target_node_id: "child", type: "supports", label: "supports result" },
          scope: "external"
        }]
      }],
      owner_by_node: { h: "thread:h", child: "thread:child" },
      structural_health: {
        policy_version: "research-health-v2",
        terminology: { topic: "decision_question", research_thread: "hypothesis_chain", stage_column: "presentation_bucket" },
        readability_status: "needs_curation",
        compatibility_status: "legacy_readable",
        topic_lifecycle: "active",
        derived_topic_phase: "needs_curation",
        semantic_node_count: 3,
        assigned_count: 2,
        unassigned_count: 1,
        unassigned_ratio: 1 / 3,
        threads: []
      },
      capacity: {
        policy_version: "topic-presentation-v2",
        status: "healthy",
        too_large: false,
        split_recommended: false,
        thread_count: 2,
        root_thread_count: 1,
        thread_node_count: 2,
        unassigned_count: 1,
        recommended_max_threads: 0,
        recommended_max_thread_nodes: 120,
        recommended_max_unassigned: 8,
        suggested_topic_count: 1,
        reasons: [],
        thread_families: [{
          id: "family:h",
          root_thread_id: "thread:h",
          title: "Hypothesis",
          thread_ids: ["thread:h", "thread:child"],
          thread_count: 2,
          semantic_node_count: 2
        }]
      }
    });
    const design = projection.threads[0].stages.design[0];
    expect(projection.contractVersion).toBe("research-thread-v2");
    expect(design.data.projectedRelationCount).toBe(5);
    expect(projection.threads[0].stages.conclusion[0].data).toMatchObject({ projectionOnly: "interpretation", interpretationKind: "supports" });
    expect(projection.threads[0].stages.issue.map((node) => node.id)).toEqual(["conclusion"]);
    expect(projection.threads[1].parentThreadId).toBe("thread:h");
    expect(projection.unassigned[0].data.unassignedReason).toBe("missing_hypothesis");
    expect(projection.crossThreadRelations[0]).toMatchObject({ sourceThreadId: "thread:h", targetThreadId: "thread:child", kind: "branch" });
    expect(projection.protocolGroups[0].members[0]).toMatchObject({ threadId: "thread:h", node: { id: "seed-1" } });
    expect(projection.protocolGroups[0].members[0].node.data.runId).toBe("run_seed_1");
    expect(projection.protocolGroups[0].relations[0]).toMatchObject({ scope: "external", edge: { source: "seed-1", target: "child" } });
    expect(projection.capacity).toMatchObject({ status: "healthy", suggested_topic_count: 1 });
    expect(projection.structuralHealth).toMatchObject({ policy_version: "research-health-v2", compatibility_status: "legacy_readable" });
  });

  it("starts a child research thread from a conclusion next step", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "h-root", type: "evidence", position: { x: 0, y: 0 }, data: { type: "hypothesis", title: "Root", body: "" } },
      { id: "conclusion", type: "evidence", position: { x: 0, y: 0 }, data: { type: "conclusion", title: "Mechanism supported", body: "" } },
      { id: "h-child", type: "evidence", position: { x: 0, y: 0 }, data: { type: "hypothesis", title: "Extension", body: "" } }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "root-conclusion", source: "h-root", target: "conclusion", data: { type: "supports", rationale: "" } },
      { id: "conclusion-child", source: "conclusion", target: "h-child", data: { type: "next_step", rationale: "" } }
    ];
    const projection = evidenceResearchThreads(nodes, edges);
    expect(projection.threads.find((thread) => thread.rootNodeId === "h-child")?.parentThreadId).toBe("thread:h-root");
    expect(projection.crossThreadRelations).toContainEqual(expect.objectContaining({ kind: "branch" }));
  });

  it("projects Result disposition into a read-only interpretation lane", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "h", type: "evidence", position: { x: 0, y: 0 }, data: { type: "hypothesis", title: "Hypothesis", body: "" } },
      { id: "r", type: "evidence", position: { x: 0, y: 0 }, data: { type: "claim", claimKind: "result", resultDisposition: "mixed", dispositionReason: "one endpoint is unstable", title: "Observed result", body: "" } },
      { id: "c", type: "evidence", position: { x: 0, y: 0 }, data: { type: "conclusion", title: "Mechanism supported", body: "" } },
      { id: "i", type: "evidence", position: { x: 0, y: 0 }, data: { type: "issue", title: "Endpoint unstable", body: "" } }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "h-r", source: "h", target: "r", data: { type: "next_step", rationale: "" } },
      { id: "r-c", source: "r", target: "c", data: { type: "supports", rationale: "matched seeds agree" } },
      { id: "r-i", source: "r", target: "i", data: { type: "reveals_issue", rationale: "" } }
    ];
    const projected = projectEvidenceResearchInterpretations(evidenceResearchThreads(nodes, edges), edges);
    const thread = projected.threads[0];
    expect(thread.stages.conclusion.map((node) => node.data.title)).toEqual(["证据支持结论", "同时暴露限制"]);
    expect(thread.stages.issue.map((node) => node.id)).toEqual(["c", "i"]);
    expect(thread.stages.conclusion.every((node) => node.data.projectionOnly === "interpretation" && node.data.readOnly)).toBe(true);
    const relations = evidenceAdjacentStageRelations(thread, edges);
    expect(relations.get("r")?.map((row) => row.otherStage)).toContain("conclusion");
    expect(relations.get("c")?.map((row) => row.otherStage)).toContain("conclusion");
  });

  it("keeps local preview and authoritative server interpretation projections aligned", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "h", type: "evidence", position: { x: 0, y: 0 }, data: { type: "claim", claimKind: "hypothesis", title: "Hypothesis", body: "" } },
      { id: "r", type: "evidence", position: { x: 0, y: 0 }, data: { type: "claim", claimKind: "result", resultDisposition: "issue", dispositionReason: "protocol mismatch", title: "Observed", body: "" } },
      { id: "i", type: "evidence", position: { x: 0, y: 0 }, data: { type: "issue", title: "Protocol mismatch", body: "" } }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "h-r", source: "h", target: "r", data: { type: "next_step", rationale: "" } },
      { id: "r-i", source: "r", target: "i", data: { type: "reveals_issue", rationale: "protocol mismatch" } }
    ];
    const local = projectEvidenceResearchInterpretations(evidenceResearchThreads(nodes, edges), edges).threads[0];
    const shared = apiResearchProjectionToFlow({
      evidence_contract_version: "research-thread-v2",
      chain_id: "chain",
      revision: 1,
      graph_hash: "hash",
      stage_order: ["hypothesis", "design", "result", "conclusion", "issue"],
      presentation_stage_order: ["hypothesis", "design", "result", "interpretation", "outcome"],
      threads: [{
        id: "thread:h", title: "Hypothesis", root_node_id: "h", explicit_hypothesis: true,
        stages: {
          hypothesis: [{ node: { id: "h", type: "claim", title: "Hypothesis", x: 0, y: 0, data_json: `{"claimKind":"hypothesis"}` }, relation_count: 1 }],
          design: [],
          result: [{ node: { id: "r", type: "claim", title: "Observed", x: 0, y: 0, data_json: `{"claimKind":"result","resultDisposition":"issue","dispositionReason":"protocol mismatch"}` }, relation_count: 2 }],
          conclusion: [],
          issue: [{ node: { id: "i", type: "issue", title: "Protocol mismatch", x: 0, y: 0 }, relation_count: 1 }]
        },
        interpretations: [{ id: "interpretation:r-i", result_node_id: "r", outcome_node_id: "i", outcome_type: "issue", kind: "reveals_issue", label: "暂不能形成结论", rationale: "protocol mismatch", edge_id: "r-i" }]
      }],
      unassigned: [], cross_thread_relations: [], owner_by_node: { h: "thread:h", r: "thread:h", i: "thread:h" }
    }).threads[0];
    expect(shared.stages.conclusion.map((node) => node.data.title)).toEqual(local.stages.conclusion.map((node) => node.data.title));
    expect(shared.stages.issue.map((node) => node.id)).toEqual(local.stages.issue.map((node) => node.id));
  });

  it("leaves context-only evidence in a visible triage bucket", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "note", type: "evidence", position: { x: 0, y: 0 }, data: { type: "note", title: "Note", body: "" } },
      { id: "ref", type: "evidence", position: { x: 0, y: 0 }, data: { type: "map_ref", title: "Reference", body: "" } }
    ];
    const projection = evidenceResearchThreads(nodes, [{ id: "neutral", source: "note", target: "ref", data: { type: "related_to", rationale: "" } }]);
    expect(projection.threads).toEqual([]);
    expect(projection.unassigned.map((node) => node.id)).toEqual(["note", "ref"]);
  });

  it("does not promote legacy fragments without an explicit hypothesis into fake threads", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "plan", type: "evidence", position: { x: 0, y: 0 }, data: { type: "plan", title: "Legacy plan", body: "" } },
      { id: "result", type: "evidence", position: { x: 0, y: 0 }, data: { type: "claim", claimKind: "result", title: "Legacy result", body: "" } },
      { id: "issue", type: "evidence", position: { x: 0, y: 0 }, data: { type: "issue", title: "Legacy issue", body: "" } }
    ];
    const projection = evidenceResearchThreads(nodes, [
      { id: "plan-result", source: "plan", target: "result", data: { type: "next_step", rationale: "" } },
      { id: "result-issue", source: "result", target: "issue", data: { type: "reveals_issue", rationale: "" } }
    ]);
    expect(projection.threads).toEqual([]);
    expect(projection.unassigned.map((node) => node.id)).toEqual(["issue", "plan", "result"]);
  });

  it("builds a one-hop focus view without treating neutral links as causality", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "run", type: "evidence", position: { x: 0, y: 0 }, data: { type: "run", title: "Formal run", body: "" } },
      { id: "claim", type: "evidence", position: { x: 0, y: 0 }, data: { type: "claim", title: "Claim", body: "" } },
      { id: "plan", type: "evidence", position: { x: 0, y: 0 }, data: { type: "plan", title: "Plan", body: "" } },
      { id: "note", type: "evidence", position: { x: 0, y: 0 }, data: { type: "note", title: "Context", body: "" } },
      { id: "far", type: "evidence", position: { x: 0, y: 0 }, data: { type: "issue", title: "Two hops away", body: "" } }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "supports", source: "run", target: "claim", data: { type: "supports", rationale: "" } },
      { id: "next", source: "claim", target: "plan", data: { type: "next_step", rationale: "" } },
      { id: "related", source: "note", target: "claim", data: { type: "related_to", rationale: "" } },
      { id: "far", source: "plan", target: "far", data: { type: "reveals_issue", rationale: "" } }
    ];

    const focus = evidenceNeighborhood(nodes, edges, "claim");
    expect(focus.center?.id).toBe("claim");
    expect(focus.upstream.map((row) => row.node.id)).toEqual(["run"]);
    expect(focus.downstream.map((row) => row.node.id)).toEqual(["plan"]);
    expect(focus.related.map((row) => row.node.id)).toEqual(["note"]);
    expect([...focus.upstream, ...focus.downstream, ...focus.related].some((row) => row.node.id === "far")).toBe(false);

    const graph = layoutEvidenceNeighborhood(focus);
    const positions = new Map(graph.nodes.map((node) => [node.id, node.position]));
    expect(positions.get("run")!.x).toBeLessThan(positions.get("claim")!.x);
    expect(positions.get("claim")!.x).toBeLessThan(positions.get("plan")!.x);
    expect(positions.get("note")!.y).toBeLessThan(positions.get("claim")!.y);
    expect(graph.edges.map((edge) => edge.id).sort()).toEqual(["next", "related", "supports"]);
    expect(graph.edges.find((edge) => edge.id === "supports")).toMatchObject({
      sourceHandle: "source-right",
      targetHandle: "target-left",
      data: { focusVisible: true }
    });
    expect(graph.edges.find((edge) => edge.id === "next")).toMatchObject({
      sourceHandle: "source-right",
      targetHandle: "target-left"
    });
    expect(graph.edges.find((edge) => edge.id === "related")).toMatchObject({
      source: "note",
      target: "claim",
      sourceHandle: "source-bottom",
      targetHandle: "target-top",
      data: { focusContext: true, focusVisible: true }
    });
    expect(graph.nodes.find((node) => node.id === "note")).toMatchObject({
      className: "evidence-focus-context-node",
      width: 252,
      height: 82
    });
    expect(graph.nodes.find((node) => node.id === "claim")?.className).not.toContain("evidence-focus-context-node");
  });

  it("lays four background relations out as compact cards with separate landing lanes", () => {
    const center: EvidenceFlowNode = { id: "center", type: "evidence", position: { x: 0, y: 0 }, data: { type: "plan", title: "Center", body: "" } };
    const contexts = ["a", "b", "c", "d"].map((id): EvidenceFlowNode => ({
      id,
      type: "evidence",
      position: { x: 0, y: 0 },
      data: { type: "note", title: id, body: "" }
    }));
    const edges = contexts.map((node): EvidenceFlowEdge => ({
      id: `edge-${node.id}`,
      source: node.id,
      target: center.id,
      data: { type: "related_to", rationale: node.id }
    }));
    const graph = layoutEvidenceNeighborhood(evidenceNeighborhood([center, ...contexts], edges, center.id));
    const contextNodes = graph.nodes.filter((node) => node.className?.includes("evidence-focus-context-node"));
    const contextEdges = graph.edges.filter((edge) => edge.data?.focusContext);

    expect(contextNodes).toHaveLength(4);
    expect(new Set(contextNodes.map((node) => node.position.x)).size).toBe(2);
    expect(new Set(contextNodes.map((node) => node.position.y)).size).toBe(2);
    expect(contextEdges).toHaveLength(4);
    expect(new Set(contextEdges.map((edge) => edge.data?.focusTargetOffsetX)).size).toBe(4);
    expect(contextEdges.every((edge) => edge.sourceHandle === "source-bottom" && edge.targetHandle === "target-top")).toBe(true);
  });

  it("expands causal focus views by depth without traversing neutral links or cycles", () => {
    const node = (id: string): EvidenceFlowNode => ({
      id,
      type: "evidence",
      position: { x: 0, y: 0 },
      data: { type: id === "context" ? "note" : "claim", title: id, body: "" }
    });
    const nodes = ["u4", "u3", "u2", "u1", "center", "d1", "d2", "d3", "d4", "context", "behind-context"].map(node);
    const edges: EvidenceFlowEdge[] = [
      { id: "u4-u3", source: "u4", target: "u3", data: { type: "supports", rationale: "" } },
      { id: "u3-u2", source: "u3", target: "u2", data: { type: "supports", rationale: "" } },
      { id: "u2-u1", source: "u2", target: "u1", data: { type: "supports", rationale: "" } },
      { id: "u1-center", source: "u1", target: "center", data: { type: "supports", rationale: "" } },
      { id: "center-d1", source: "center", target: "d1", data: { type: "next_step", rationale: "" } },
      { id: "d1-d2", source: "d1", target: "d2", data: { type: "next_step", rationale: "" } },
      { id: "d2-d3", source: "d2", target: "d3", data: { type: "next_step", rationale: "" } },
      { id: "d3-d4", source: "d3", target: "d4", data: { type: "next_step", rationale: "" } },
      { id: "cycle", source: "d2", target: "d1", data: { type: "weakens", rationale: "" } },
      { id: "context", source: "context", target: "center", data: { type: "related_to", rationale: "" } },
      { id: "context-tail", source: "context", target: "behind-context", data: { type: "next_step", rationale: "" } }
    ];

    const one = evidenceNeighborhood(nodes, edges, "center", 1);
    expect(one.upstream.map((row) => [row.node.id, row.depth])).toEqual([["u1", 1]]);
    expect(one.downstream.map((row) => [row.node.id, row.depth])).toEqual([["d1", 1]]);

    const two = evidenceNeighborhood(nodes, edges, "center", 2);
    expect(two.upstream.map((row) => [row.node.id, row.depth])).toEqual([["u1", 1], ["u2", 2]]);
    expect(two.downstream.map((row) => [row.node.id, row.depth])).toEqual([["d1", 1], ["d2", 2]]);
    expect(two.related.map((row) => row.node.id)).toEqual(["context"]);
    expect([...two.upstream, ...two.downstream, ...two.related].some((row) => row.node.id === "behind-context")).toBe(false);
    expect(new Set([...two.upstream, ...two.downstream].map((row) => row.node.id)).size).toBe(4);

    const three = evidenceNeighborhood(nodes, edges, "center", 3);
    expect(three.upstream.map((row) => row.node.id)).toEqual(["u1", "u2", "u3"]);
    expect(three.downstream.map((row) => row.node.id)).toEqual(["d1", "d2", "d3"]);
    const graph = layoutEvidenceNeighborhood(three);
    const positions = new Map(graph.nodes.map((item) => [item.id, item.position]));
    expect(positions.get("u3")!.x).toBeLessThan(positions.get("u2")!.x);
    expect(positions.get("u2")!.x).toBeLessThan(positions.get("u1")!.x);
    expect(positions.get("u1")!.x).toBeLessThan(positions.get("center")!.x);
    expect(positions.get("center")!.x).toBeLessThan(positions.get("d1")!.x);
    expect(positions.get("d1")!.x).toBeLessThan(positions.get("d2")!.x);
    expect(positions.get("d2")!.x).toBeLessThan(positions.get("d3")!.x);
    expect(graph.edges.some((edge) => edge.id === "cycle")).toBe(false);

    const full = evidenceNeighborhood(nodes, edges, "center", "all");
    expect(full.upstream.map((row) => row.node.id)).toEqual(["u1", "u2", "u3", "u4"]);
    expect(full.downstream.map((row) => row.node.id)).toEqual(["d1", "d2", "d3", "d4"]);
  });

  it("groups run candidates by project before unassigned runs", () => {
    const groups = groupRunCandidatesByProject([
      { id: "run:raw", kind: "run", run_id: "run_raw" },
      { id: "card:two", kind: "project_card", run_id: "run_two", project_id: "proj_b", project_name: "Beta" },
      { id: "card:one", kind: "project_card", run_id: "run_one", project_id: "proj_a", project_name: "Alpha" },
      { id: "card:three", kind: "project_card", run_id: "run_three", project_id: "proj_a", project_name: "Alpha" }
    ]);

    expect(groups.map((group) => group.title)).toEqual(["Alpha", "Beta", "Unassigned runs"]);
    expect(groups[0].candidates.map((candidate) => candidate.run_id)).toEqual(["run_one", "run_three"]);
  });

  it("keeps a project Research Graph scoped to that project's candidates", () => {
    const candidates = [
      { id: "card:a", kind: "project_card" as const, run_id: "run_a", project_id: "project-a" },
      { id: "card:b", kind: "project_card" as const, run_id: "run_b", project_id: "project-b" },
      { id: "run:raw", kind: "run" as const, run_id: "run_raw" }
    ];

    expect(filterRunCandidatesForProject(candidates, "project-a").map((candidate) => candidate.run_id)).toEqual(["run_a"]);
    expect(filterRunCandidatesForProject(candidates, "")).toEqual(candidates);
  });

  it("uses the run name as the Evidence Chain run node title", () => {
    const node = candidateToNode(
      {
        id: "card:one",
        kind: "project_card",
        run_id: "run_abc",
        project_card_id: "card_one",
        verdict: "Agent note title should not replace the run name",
        question: "Does the note explain this experiment?",
        key_metrics: "loss=0.12",
        run: { id: "run_abc", resource_id: "mu", name: "dx formal ablation", status: "succeeded", command: "python train.py", git_branch: "main", git_commit: "abcdef1234567890", git_dirty: true }
      },
      { x: 10, y: 20 }
    );

    expect(node.data.title).toBe("dx formal ablation");
    expect(node.data.runTitle).toBe("dx formal ablation");
    expect(node.data.gitBranch).toBe("main");
    expect(node.data.gitDirty).toBe(true);
    expect(node.data.body).toContain("Agent note title should not replace the run name");
    expect(node.data.body).toContain("Does the note explain this experiment?");
  });

  it("serializes React Flow nodes and edges to API graph payload", () => {
    const nodes: EvidenceFlowNode[] = [
      {
        id: "node_run",
        type: "evidence",
        position: { x: 10, y: 20 },
        width: 280,
        height: 150,
        data: {
          type: "run",
          title: "formal run",
          body: "question",
          runTitle: "formal run",
          runId: "run_abc",
          projectCardId: "card_abc",
          status: "succeeded",
          runKind: "formal",
          keyMetrics: "mAP=0.6",
          evidenceLevel: "B",
          gitBranch: "main",
          gitCommit: "abcdef1234567890",
          gitDirty: true,
          gitDiffHash: "hash123"
        }
      }
    ];
    const edges: EvidenceFlowEdge[] = [
      {
        id: "edge_1",
        source: "node_run",
        target: "node_hyp",
        sourceHandle: "source-right",
        targetHandle: "target-left",
        label: "supports the anchor hypothesis",
        data: { type: "supports", rationale: "metric improved", autoHandles: true }
      }
    ];

    const payload = serializeEvidenceGraph(nodes, edges);
    expect(payload.nodes[0]).toMatchObject({
      id: "node_run",
      type: "run",
      run_id: "run_abc",
      project_card_id: "card_abc",
      x: 10,
      y: 20
    });
    expect(JSON.parse(payload.nodes[0].data_json || "{}")).toMatchObject({ runTitle: "formal run", status: "succeeded", keyMetrics: "mAP=0.6", gitBranch: "main", gitDirty: true, gitDiffHash: "hash123" });
    expect(payload.edges[0]).toMatchObject({
      id: "edge_1",
      source_node_id: "node_run",
      target_node_id: "node_hyp",
      type: "supports",
      label: "supports the anchor hypothesis",
      rationale: "metric improved"
    });
    expect(JSON.parse(payload.edges[0].data_json || "{}")).toMatchObject({
      sourceHandle: "source-right",
      targetHandle: "target-left",
      autoHandles: true
    });
  });

  it("preserves an intentionally empty edge label", () => {
    const payload = serializeEvidenceGraph([], [
      {
        id: "edge_blank",
        source: "node_a",
        target: "node_b",
        label: "",
        data: { type: "custom", rationale: "" }
      }
    ]);

    expect(payload.edges[0].label).toBe("");
  });

  it("parses a pending graph patch into namespaced, read-only draft elements", () => {
    const preview = evidenceProposalPreview({
      id: "card:draft",
      kind: "project_card",
      run_id: "run_proposal",
      project_card: {
        id: "card_draft",
        project_id: "project_a",
        run_id: "run_proposal",
        graph_status: "pending",
        graph_patch_json: JSON.stringify({
          chain_id: "chain_primary",
          routing_reason: "This run changes the baseline claim.",
          nodes: [
            { id: "claim_new", type: "claim", title: "New claim", body: "Metric improved", x: 0, y: 0 }
          ],
          edges: [
            { id: "supports_new", source_node_id: "claim_new", target_node_id: "claim_existing", type: "supports", label: "supports", rationale: "formal result" }
          ]
        })
      }
    });

    expect(preview).not.toBeNull();
    expect(preview?.chainId).toBe("chain_primary");
    expect(preview?.nodes[0]).toMatchObject({
      id: "draft:run_proposal:claim_new",
      draggable: false,
      connectable: false,
      deletable: false,
      data: { draft: true, readOnly: true, proposalRunId: "run_proposal", sourceNodeId: "claim_new" }
    });
    expect(preview?.edges[0]).toMatchObject({
      id: "draft-edge:run_proposal:supports_new",
      source: "draft:run_proposal:claim_new",
      target: "claim_existing",
      selectable: false,
      deletable: false,
      data: { draft: true, readOnly: true, proposalRunId: "run_proposal", sourceEdgeId: "supports_new" }
    });
  });

  it("previews a Run-optional workspace proposal on its explicit target Map", () => {
    const preview = evidenceWorkspaceProposalPreview({
      id: "proposal_bootstrap",
      project_id: "project_a",
      target_map_id: "topic_context",
      base_graph_revision: 0,
      actor: "agent",
      summary: "Bootstrap the research context",
      routing_reason: "This Topic owns the initial protocol question.",
      project_level_impact: false,
      source_run_ids: [],
      source_snapshot_ids: [],
      patch_json: JSON.stringify({
        chain_id: "topic_context",
        layout_intent: {
          flow: "left_to_right",
          ranks: [["hypothesis"]],
          rationale: "Start with the research question."
        },
        nodes: [
          { id: "hypothesis", type: "hypothesis", title: "Initial hypothesis", body: "", x: 0, y: 0 }
        ],
        edges: []
      }),
      status: "pending",
      proposal_hash: "hash",
      created_at: "2026-07-25T00:00:00Z",
      updated_at: "2026-07-25T00:00:00Z"
    });

    expect(preview).not.toBeNull();
    expect(preview).toMatchObject({
      proposalId: "proposal_bootstrap",
      runId: "proposal_bootstrap",
      chainId: "topic_context",
      title: "Bootstrap the research context",
      layoutIntent: {
        flow: "left_to_right",
        ranks: [["hypothesis"]],
        rationale: "Start with the research question."
      }
    });
    expect(preview?.nodes[0]).toMatchObject({
      id: "draft:proposal_bootstrap:hypothesis",
      data: {
        draft: true,
        readOnly: true,
        proposalRunId: "proposal_bootstrap",
        sourceNodeId: "hypothesis"
      }
    });
  });

  it("only overlays a proposal on the full canvas after an explicit request", () => {
    expect(shouldOverlayEvidenceProposal("topic_context", "topic_context", false)).toBe(false);
    expect(shouldOverlayEvidenceProposal("topic_context", "topic_context", true)).toBe(true);
    expect(shouldOverlayEvidenceProposal("other_topic", "topic_context", true)).toBe(false);
  });

  it("keeps draft protocol membership inside the proposal namespace", () => {
    const preview = evidenceWorkspaceProposalPreview({
      id: "proposal_group",
      project_id: "project_a",
      target_map_id: "topic_context",
      base_graph_revision: 0,
      actor: "agent",
      summary: "Group comparable evidence",
      routing_reason: "The topic owns this protocol.",
      project_level_impact: false,
      source_run_ids: [],
      source_snapshot_ids: [],
      patch_json: JSON.stringify({
        chain_id: "topic_context",
        routing_reason: "The topic owns this protocol.",
        nodes: [
          {
            id: "protocol_group",
            type: "group",
            title: "Clean-810 protocol",
            body: "",
            data_json: `{"groupKind":"protocol","version":"v1"}`
          },
          {
            id: "protocol_issue",
            type: "issue",
            title: "Lock split",
            body: "",
            data_json: `{"groupId":"protocol_group"}`
          }
        ],
        edges: []
      }),
      status: "pending",
      proposal_hash: "hash",
      reviewed_by: "",
      created_at: "2026-07-29T00:00:00Z",
      updated_at: "2026-07-29T00:00:00Z"
    });

    expect(preview).not.toBeNull();
    expect(preview?.nodes.find((node) => node.data.type === "group")?.id).toBe("draft:proposal_group:protocol_group");
    expect(preview?.nodes.find((node) => node.data.type === "issue")?.data.groupId).toBe("draft:proposal_group:protocol_group");
  });

  it("rejects malformed proposals and never serializes draft overlay elements", () => {
    expect(evidenceProposalPreview({
      id: "card:bad",
      kind: "project_card",
      run_id: "run_bad",
      project_card: {
        id: "card_bad",
        project_id: "project_a",
        run_id: "run_bad",
        graph_status: "pending",
        graph_patch_json: "{broken"
      }
    })).toBeNull();

    const accepted: EvidenceFlowNode = { id: "accepted", type: "evidence", position: { x: 0, y: 0 }, data: { type: "claim", title: "Accepted", body: "" } };
    const draft: EvidenceFlowNode = {
      id: "draft:run_x:new",
      type: "evidence",
      position: { x: 100, y: 0 },
      data: { type: "claim", title: "Draft", body: "", draft: true, proposalRunId: "run_x" }
    };
    const payload = serializeEvidenceGraph(
      [accepted, draft],
      [
        { id: "draft-edge", source: "draft:run_x:new", target: "accepted", data: { type: "supports", rationale: "", draft: true } },
        { id: "unsafe-edge", source: "accepted", target: "draft:run_x:new", data: { type: "related_to", rationale: "" } }
      ]
    );

    expect(payload.nodes.map((node) => node.id)).toEqual(["accepted"]);
    expect(payload.edges).toEqual([]);
  });

  it("lays out semantic DAGs deterministically while preserving pins", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "claim", type: "evidence", position: { x: 0, y: 0 }, data: { type: "claim", title: "Claim", body: "", occurredAt: "2026-07-03" } },
      { id: "run-late", type: "evidence", position: { x: 0, y: 0 }, data: { type: "run", title: "Late", body: "", occurredAt: "2026-07-02" } },
      { id: "dataset", type: "evidence", position: { x: 0, y: 0 }, data: { type: "dataset", title: "Data", body: "", occurredAt: "2026-07-01" } },
      { id: "run-early", type: "evidence", position: { x: 777, y: 888 }, data: { type: "run", title: "Early", body: "", occurredAt: "2026-07-01", pinned: true } }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "uses-late", source: "dataset", target: "run-late", data: { type: "uses", rationale: "" } },
      { id: "uses-early", source: "dataset", target: "run-early", data: { type: "uses", rationale: "" } },
      { id: "supports", source: "run-late", target: "claim", data: { type: "supports", rationale: "" } },
      { id: "visual", source: "claim", target: "dataset", data: { type: "related_to", rationale: "" } }
    ];

    const first = layoutEvidenceGraph(nodes, edges);
    const second = layoutEvidenceGraph([...nodes].reverse(), [...edges].reverse());
    const positions = (rows: EvidenceFlowNode[]) => Object.fromEntries(rows.map((node) => [node.id, node.position]));
    expect(positions(second)).toEqual(positions(first));
    expect(first.find((node) => node.id === "dataset")!.position.x).toBeLessThan(first.find((node) => node.id === "run-late")!.position.x);
    expect(first.find((node) => node.id === "run-late")!.position.x).toBeLessThan(first.find((node) => node.id === "claim")!.position.x);
    expect(first.find((node) => node.id === "run-early")!.position).toEqual({ x: 777, y: 888 });

    const reset = layoutEvidenceGraph(nodes, edges, true);
    expect(reset.find((node) => node.id === "run-early")!.position).not.toEqual({ x: 777, y: 888 });
    expect(reset.find((node) => node.id === "run-early")!.data.pinned).toBe(false);
  });

  it("projects explicitly ranked cards into deterministic columns while leaving unlisted pins untouched", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "context", type: "evidence", position: { x: 10, y: 10 }, data: { type: "hypothesis", title: "Context", body: "" } },
      { id: "issue", type: "evidence", position: { x: 20, y: 20 }, data: { type: "issue", title: "Issue", body: "" } },
      { id: "claim", type: "evidence", position: { x: 777, y: 888 }, data: { type: "claim", title: "Claim", body: "", pinned: true } },
      { id: "next", type: "evidence", position: { x: 30, y: 30 }, data: { type: "plan", title: "Next", body: "" } },
      { id: "unlisted", type: "evidence", position: { x: 901, y: 902 }, data: { type: "issue", title: "Unlisted", body: "", pinned: true } }
    ];
    const intent = {
      flow: "left_to_right" as const,
      ranks: [["context", "issue"], ["claim"], ["next"]],
      rationale: "Keep the main decision path left to right."
    };

    const first = layoutEvidenceGraphFromIntent(nodes, intent);
    const second = layoutEvidenceGraphFromIntent([...nodes].reverse(), intent);
    const positions = (rows: EvidenceFlowNode[]) => Object.fromEntries(rows.map((node) => [node.id, node.position]));
    expect(positions(second)).toEqual(positions(first));
    expect(positions(first).context.x).toBe(positions(first).issue.x);
    expect(positions(first).context.y).toBeLessThan(positions(first).issue.y);
    expect(positions(first).context.x).toBeLessThan(positions(first).claim.x);
    expect(positions(first).claim).not.toEqual({ x: 777, y: 888 });
    expect(positions(first).next.x).toBeGreaterThan(positions(first).claim.x);
    expect(positions(first).unlisted).toEqual({ x: 901, y: 902 });
    expect(first.find((node) => node.id === "claim")?.data.pinned).toBe(true);
  });

  it("moves protocol containers as one block and remaps draft ids for preview", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "protocol", type: "evidence", position: { x: 300, y: 200 }, width: 680, height: 260, data: { type: "group", title: "Protocol", body: "", groupKind: "protocol" } },
      { id: "member", type: "evidence", position: { x: 340, y: 280 }, data: { type: "run", title: "Run", body: "", groupId: "protocol" } },
      { id: "claim", type: "evidence", position: { x: 900, y: 200 }, data: { type: "claim", title: "Claim", body: "" } }
    ];
    const arranged = layoutEvidenceGraphFromIntent(nodes, {
      flow: "left_to_right",
      ranks: [["protocol"], ["claim"]]
    });
    const protocol = arranged.find((node) => node.id === "protocol")!;
    const member = arranged.find((node) => node.id === "member")!;
    expect(member.position.x - protocol.position.x).toBe(40);
    expect(member.position.y - protocol.position.y).toBe(80);
    expect(protocol.position.x).toBeLessThan(arranged.find((node) => node.id === "claim")!.position.x);

    const normalized = normalizeEvidenceLayoutIntent({
      flow: "left_to_right",
      ranks: [["protocol"], ["claim"]]
    });
    expect(remapEvidenceLayoutIntent(normalized, new Map([["claim", "draft:claim"]]))?.ranks)
      .toEqual([["protocol"], ["draft:claim"]]);
  });

  it("keeps protocol ranges permanently expanded even with legacy collapsed data", () => {
    const nodes: EvidenceFlowNode[] = [
      {
        id: "protocol",
        type: "evidence",
        position: { x: 80, y: 80 },
        data: { type: "group", title: "Clean-810 protocol", body: "", collapsed: true, groupKind: "protocol" }
      },
      {
        id: "dataset",
        type: "evidence",
        position: { x: 120, y: 150 },
        data: { type: "dataset", title: "Dataset", body: "", groupId: "protocol" }
      },
      {
        id: "run",
        type: "evidence",
        position: { x: 460, y: 150 },
        data: { type: "run", title: "Run", body: "", groupId: "protocol" }
      },
      {
        id: "claim",
        type: "evidence",
        position: { x: 900, y: 150 },
        data: { type: "claim", title: "Claim", body: "" }
      }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "inside", source: "dataset", target: "run", data: { type: "uses", rationale: "" } },
      { id: "supports-a", source: "dataset", target: "claim", data: { type: "supports", rationale: "" } },
      { id: "supports-b", source: "run", target: "claim", data: { type: "supports", rationale: "" } }
    ];

    const projection = projectEvidenceGroups(nodes, edges);
    expect(projection.nodes.map((node) => node.id).sort()).toEqual(["claim", "dataset", "run"]);
    expect(projection.internalEdgeCounts).toEqual({ protocol: 1 });
    expect(projection.edges).toEqual(edges);
    expect(serializeEvidenceGraph(nodes, edges).nodes).toHaveLength(4);
    expect(serializeEvidenceGraph(nodes, edges).edges).toHaveLength(3);
  });

  it("only permits experiment-structure nodes inside protocol ranges", () => {
    const permitted: EvidenceNodeType[] = ["dataset", "run", "plan", "experiment"];
    const forbidden: EvidenceNodeType[] = ["group", "protocol", "claim", "issue", "hypothesis", "conclusion", "note", "map_ref"];
    expect(permitted.every(isProtocolGroupMemberType)).toBe(true);
    expect(forbidden.some(isProtocolGroupMemberType)).toBe(false);
  });

  it("expands a protocol frame around freely positioned members", () => {
    const group: EvidenceFlowNode = {
      id: "protocol",
      type: "evidence",
      position: { x: 100, y: 200 },
      width: 700,
      height: 420,
      data: { type: "group", title: "Protocol", body: "", groupKind: "protocol" }
    };
    const member: EvidenceFlowNode = {
      id: "run",
      type: "evidence",
      position: { x: 900, y: 760 },
      width: 306,
      height: 138,
      data: { type: "run", title: "Run", body: "", groupId: "protocol" }
    };
    const bounds = evidenceGroupFrameBounds(
      { id: "protocol", group, memberIds: ["run"] },
      [group, member]
    );

    expect(member.position).toEqual({ x: 900, y: 760 });
    expect(bounds.x).toBe(100);
    expect(bounds.y).toBe(200);
    expect(bounds.x + bounds.width).toBeGreaterThanOrEqual(900 + 306 + 24);
    expect(bounds.y + bounds.height).toBeGreaterThanOrEqual(760 + 138 + 28);
  });

  it("moves a protocol container and all of its members as one unit", () => {
    const nodes: EvidenceFlowNode[] = [
      {
        id: "protocol",
        type: "evidence",
        position: { x: 100, y: 200 },
        data: { type: "group", title: "Protocol", body: "", groupKind: "protocol" }
      },
      {
        id: "run",
        type: "evidence",
        position: { x: 180, y: 300 },
        data: { type: "run", title: "Run", body: "", groupId: "protocol" }
      },
      {
        id: "claim",
        type: "evidence",
        position: { x: 900, y: 400 },
        data: { type: "claim", title: "Claim", body: "" }
      }
    ];
    const moved = translateProtocolContainer(nodes, "protocol", { x: 120, y: -40 });
    expect(moved.find((node) => node.id === "protocol")?.position).toEqual({ x: 220, y: 160 });
    expect(moved.find((node) => node.id === "run")?.position).toEqual({ x: 300, y: 260 });
    expect(moved.find((node) => node.id === "claim")?.position).toEqual({ x: 900, y: 400 });
    expect(moved.find((node) => node.id === "protocol")?.data.pinned).toBe(true);
    expect(moved.find((node) => node.id === "run")?.data.pinned).toBe(true);
  });

  it("supports precise and accelerated keyboard movement for protocol containers", () => {
    expect(protocolContainerMoveDeltaForKey("ArrowLeft")).toEqual({ x: -10, y: 0 });
    expect(protocolContainerMoveDeltaForKey("ArrowDown", true)).toEqual({ x: 0, y: 40 });
    expect(protocolContainerMoveDeltaForKey("Enter")).toBeNull();
  });

  it("keeps a protocol frame's own geometry instead of chasing its members", () => {
    const group: EvidenceFlowNode = {
      id: "protocol",
      type: "evidence",
      position: { x: 100, y: 200 },
      width: 720,
      height: 420,
      data: { type: "group", title: "Protocol", body: "", groupKind: "protocol" }
    };
    const member: EvidenceFlowNode = {
      id: "run",
      type: "evidence",
      position: { x: 180, y: 300 },
      width: 286,
      height: 184,
      data: { type: "run", title: "Run", body: "", groupId: "protocol" }
    };
    expect(evidenceGroupFrameBounds({ id: "protocol", group, memberIds: ["run"] }, [group, member])).toEqual({
      x: 100,
      y: 200,
      width: 720,
      height: 420
    });
  });

  it("converts membership-only protocol cards into visual protocol frames", () => {
    const nodes: EvidenceFlowNode[] = [
      {
        id: "protocol",
        type: "evidence",
        position: { x: 100, y: 100 },
        data: { type: "protocol", title: "统一验证协议", body: "固定 split 与 seeds", pinned: true }
      },
      {
        id: "g1",
        type: "evidence",
        position: { x: 400, y: 100 },
        data: { type: "plan", title: "G1", body: "" }
      },
      {
        id: "g2",
        type: "evidence",
        position: { x: 700, y: 100 },
        data: { type: "plan", title: "G2", body: "" }
      }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "member-g1", source: "protocol", target: "g1", label: "统一协议", data: { type: "related_to", rationale: "" } },
      { id: "member-g2", source: "protocol", target: "g2", label: "消融与负对照", data: { type: "related_to", rationale: "" } }
    ];

    const result = convertProtocolToFrame(nodes, edges, "protocol");
    expect(result.migration).toMatchObject({
      eligible: true,
      memberIds: ["g1", "g2"],
      removableEdgeIds: ["member-g1", "member-g2"]
    });
    expect(result.nodes.find((node) => node.id === "protocol")).toMatchObject({
      connectable: false,
      data: {
        type: "group",
        title: "统一验证协议",
        body: "固定 split 与 seeds",
        groupKind: "protocol",
        version: "v1"
      }
    });
    expect(result.nodes.find((node) => node.id === "g1")?.data.groupId).toBe("protocol");
    expect(result.nodes.find((node) => node.id === "g2")?.data.groupId).toBe("protocol");
    expect(result.edges).toEqual([]);
  });

  it("refuses to erase semantic protocol relations or steal grouped members", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "protocol", type: "evidence", position: { x: 0, y: 0 }, data: { type: "protocol", title: "Protocol", body: "" } },
      { id: "run", type: "evidence", position: { x: 0, y: 0 }, data: { type: "run", title: "Run", body: "", groupId: "other" } }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "uses", source: "protocol", target: "run", label: "uses", data: { type: "uses", rationale: "" } }
    ];

    const inspection = inspectProtocolFrameMigration(nodes, edges, "protocol");
    expect(inspection.eligible).toBe(false);
    expect(inspection.blockers).toEqual(expect.arrayContaining([
      expect.stringContaining("具有研究语义"),
      expect.stringContaining("已属于另一个协议容器")
    ]));
    const result = convertProtocolToFrame(nodes, edges, "protocol");
    expect(result.nodes).toBe(nodes);
    expect(result.edges).toBe(edges);
  });

  it("lays out protocol groups as bounded subgraphs before the outer evidence flow", () => {
    const nodes: EvidenceFlowNode[] = [
      {
        id: "protocol",
        type: "evidence",
        position: { x: 0, y: 0 },
        data: { type: "group", title: "Protocol", body: "", groupKind: "protocol" }
      },
      {
        id: "dataset",
        type: "evidence",
        position: { x: 0, y: 0 },
        data: { type: "dataset", title: "Dataset", body: "", groupId: "protocol" }
      },
      {
        id: "run",
        type: "evidence",
        position: { x: 0, y: 0 },
        data: { type: "run", title: "Run", body: "", groupId: "protocol" }
      },
      {
        id: "claim",
        type: "evidence",
        position: { x: 0, y: 0 },
        data: { type: "claim", title: "Claim", body: "" }
      }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "uses", source: "dataset", target: "run", data: { type: "uses", rationale: "" } },
      { id: "supports", source: "run", target: "claim", data: { type: "supports", rationale: "" } }
    ];

    const first = layoutEvidenceGraph(nodes, edges, true);
    const second = layoutEvidenceGraph([...nodes].reverse(), [...edges].reverse(), true);
    const positions = (rows: EvidenceFlowNode[]) => Object.fromEntries(rows.map((node) => [node.id, node.position]));
    expect(positions(second)).toEqual(positions(first));
    const laidOut = positions(first);
    expect(laidOut.dataset.x).toBeLessThan(laidOut.run.x);
    expect(laidOut.dataset.x).toBeGreaterThanOrEqual(laidOut.protocol.x);
    expect(laidOut.run.x).toBeLessThan(laidOut.claim.x);
  });

  it("orders adjacent ranks by their neighbours to remove avoidable crossings", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "source-a", type: "evidence", position: { x: 0, y: 0 }, data: { type: "plan", title: "A", body: "" } },
      { id: "source-b", type: "evidence", position: { x: 0, y: 0 }, data: { type: "plan", title: "B", body: "" } },
      { id: "target-a", type: "evidence", position: { x: 0, y: 0 }, data: { type: "plan", title: "C", body: "" } },
      { id: "target-b", type: "evidence", position: { x: 0, y: 0 }, data: { type: "plan", title: "D", body: "" } }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "cross-a", source: "source-a", target: "target-b", data: { type: "next_step", rationale: "" } },
      { id: "cross-b", source: "source-b", target: "target-a", data: { type: "next_step", rationale: "" } }
    ];

    const laidOut = layoutEvidenceGraph(nodes, edges, true);
    const position = Object.fromEntries(laidOut.map((node) => [node.id, node.position]));
    expect(position["source-a"].y).toBeLessThan(position["source-b"].y);
    expect(position["target-b"].y).toBeLessThan(position["target-a"].y);
    expect(position["source-a"].x).toBeLessThan(position["target-b"].x);
    expect(position["source-b"].x).toBeLessThan(position["target-a"].x);
  });

  it("uses neutral relations to order the canvas when they still describe a directed sequence", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "problem", type: "evidence", position: { x: 0, y: 0 }, data: { type: "issue", title: "Problem", body: "" } },
      { id: "baseline", type: "evidence", position: { x: 0, y: 0 }, data: { type: "plan", title: "Baseline", body: "" } },
      { id: "ablation", type: "evidence", position: { x: 0, y: 0 }, data: { type: "plan", title: "Ablation", body: "" } }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "problem-to-baseline", source: "problem", target: "baseline", data: { type: "next_step", rationale: "" } },
      { id: "baseline-to-ablation", source: "baseline", target: "ablation", data: { type: "related_to", rationale: "" } }
    ];

    const laidOut = layoutEvidenceGraph(nodes, edges, true);
    const position = Object.fromEntries(laidOut.map((node) => [node.id, node.position]));
    expect(position.problem.x).toBeLessThan(position.baseline.x);
    expect(position.baseline.x).toBeLessThan(position.ablation.x);
  });

  it("still produces a non-overlapping layout when semantic edges contain a cycle", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "cycle-a", type: "evidence", position: { x: 0, y: 0 }, data: { type: "claim", title: "A", body: "" } },
      { id: "cycle-b", type: "evidence", position: { x: 0, y: 0 }, data: { type: "issue", title: "B", body: "" } }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "cycle-1", source: "cycle-a", target: "cycle-b", data: { type: "reveals_issue", rationale: "" } },
      { id: "cycle-2", source: "cycle-b", target: "cycle-a", data: { type: "next_step", rationale: "" } }
    ];

    const laidOut = layoutEvidenceGraph(nodes, edges, true);
    expect(new Set(laidOut.map((node) => `${node.position.x}:${node.position.y}`)).size).toBe(2);
  });

  it("uses a canonical left-to-right auto route and visually distinct relation styles", () => {
    expect(evidenceAutoHandlePair({ x: 0, y: 300 }, { x: 420, y: 0 })).toEqual({
      source: "right",
      target: "left"
    });
    expect(evidenceAutoHandlePair({ x: 0, y: 0 }, { x: 0, y: 420 })).toEqual({
      source: "bottom",
      target: "top"
    });

    const supports = edgeStyle("supports");
    const weakens = edgeStyle("weakens");
    const issue = edgeStyle("reveals_issue");
    expect(supports.stroke).not.toBe(weakens.stroke);
    expect(weakens.stroke).not.toBe(issue.stroke);
    expect(Number(supports.strokeWidth)).toBeGreaterThanOrEqual(2.4);
    expect(weakens.strokeDasharray).toBeTruthy();
    expect(evidenceMarkerEnd("supports")).toMatchObject({
      color: supports.stroke,
      width: 22,
      height: 22
    });
  });

  it("shows pinned Map References as current, stale, archived, or missing", () => {
    const reference = {
      type: "map_ref" as const,
      target_revision: 3,
      target_graph_hash: "hash-r3"
    };
    expect(evidenceMapReferenceStatus(reference, { status: "active", revision: 3, graph_hash: "hash-r3" })).toBe("current");
    expect(evidenceMapReferenceStatus(reference, { status: "active", revision: 4, graph_hash: "hash-r4" })).toBe("stale");
    expect(evidenceMapReferenceStatus(reference, { status: "archived", revision: 3, graph_hash: "hash-r3" })).toBe("archived");
    expect(evidenceMapReferenceStatus(reference, undefined)).toBe("missing");
    expect(evidenceMapReferenceStatus({ ...reference, type: "claim" }, undefined)).toBeUndefined();
  });

  it("keeps a 100-node evidence canvas layout finite and serializable", () => {
    const nodes: EvidenceFlowNode[] = Array.from({ length: 100 }, (_, index) => ({
      id: `node-${index}`,
      type: "evidence",
      position: { x: 0, y: 0 },
      data: {
        type: index % 4 === 0 ? "issue" : "note",
        title: `Node ${index}`,
        body: "A long evidence note that remains in the model while its card can stay compact."
      }
    }));
    const laidOut = layoutEvidenceGraph(nodes, []);
    expect(laidOut).toHaveLength(100);
    expect(laidOut.every((node) => Number.isFinite(node.position.x) && Number.isFinite(node.position.y))).toBe(true);
    expect(new Set(laidOut.map((node) => `${node.position.x}:${node.position.y}`)).size).toBe(100);
    const xs = laidOut.map((node) => node.position.x);
    const ys = laidOut.map((node) => node.position.y);
    const width = Math.max(...xs) - Math.min(...xs) + 306;
    const height = Math.max(...ys) - Math.min(...ys) + 138;
    expect(Math.max(width / height, height / width)).toBeLessThan(3);
    expect(serializeEvidenceGraph(laidOut, []).nodes).toHaveLength(100);
  });

  it("restores valid persisted coordinates without treating unpinned nodes as layout requests", () => {
    const nodes: EvidenceFlowNode[] = [
      { id: "a", type: "evidence", position: { x: 125, y: 240 }, data: { type: "issue", title: "A", body: "", pinned: false } },
      { id: "b", type: "evidence", position: { x: 640, y: 315 }, data: { type: "plan", title: "B", body: "", pinned: false } }
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "a-b", source: "a", target: "b", data: { type: "next_step", rationale: "" } }
    ];

    expect(prepareLoadedEvidenceGraph(nodes, edges).map((node) => node.position)).toEqual(nodes.map((node) => node.position));

    const legacy = nodes.map((node) => ({
      ...node,
      position: { x: 0, y: 0 },
      data: { ...node.data, pinned: true }
    }));
    const repaired = prepareLoadedEvidenceGraph(legacy, edges);
    expect(new Set(repaired.map((node) => `${node.position.x}:${node.position.y}`)).size).toBe(2);
    expect(repaired.every((node) => node.data.pinned === false)).toBe(true);
  });

  it("keeps authoritative database pin state over stale legacy data JSON", () => {
    const node = apiNodeToFlowNode({
      id: "pinned",
      type: "plan",
      title: "Pinned",
      body: "",
      x: 420,
      y: 180,
      width: 306,
      height: 138,
      pinned: true,
      data_json: JSON.stringify({ pinned: false, title: "stale" })
    });

    expect(node.data.pinned).toBe(true);
    expect(node.data.title).toBe("Pinned");
    expect(node.position).toEqual({ x: 420, y: 180 });
  });

  it("round-trips typed result provenance without turning Runs into graph nodes", () => {
    const node = apiNodeToFlowNode({
      id: "result",
      type: "claim",
      title: "Five-seed result",
      body: "",
      x: 0,
      y: 0,
      source_run_ids: ["run_b", "run_a"],
      source_snapshot_ids: ["snapshot_1"],
      data_json: JSON.stringify({ claimKind: "result" })
    });

    expect(node.data.sourceRunIds).toEqual(["run_b", "run_a"]);
    expect(node.data.sourceSnapshotIds).toEqual(["snapshot_1"]);
    const serialized = serializeEvidenceGraph([node], []);
    expect(serialized.nodes[0].source_run_ids).toEqual(["run_b", "run_a"]);
    expect(serialized.nodes[0].source_snapshot_ids).toEqual(["snapshot_1"]);
  });

  it("packs dense ranks without rectangle overlap or an extreme vertical strip", () => {
    const roots: EvidenceFlowNode[] = Array.from({ length: 12 }, (_, index) => ({
      id: `root-${index}`,
      type: "evidence",
      position: { x: 0, y: 0 },
      width: 306,
      height: 220,
      data: { type: "issue", title: `Root ${index}`, body: "" }
    }));
    const target: EvidenceFlowNode = {
      id: "target",
      type: "evidence",
      position: { x: 0, y: 0 },
      width: 306,
      height: 138,
      data: { type: "plan", title: "Target", body: "" }
    };
    const edges: EvidenceFlowEdge[] = roots.map((node, index) => ({
      id: `edge-${index}`,
      source: node.id,
      target: target.id,
      data: { type: "next_step", rationale: "" }
    }));
    const laidOut = layoutEvidenceGraph([...roots, target], edges, true);
    const rectangles = laidOut.map((node) => ({
      id: node.id,
      left: node.position.x,
      top: node.position.y,
      right: node.position.x + (node.width || 306),
      bottom: node.position.y + (node.height || 138)
    }));
    for (let left = 0; left < rectangles.length; left += 1) {
      for (let right = left + 1; right < rectangles.length; right += 1) {
        const a = rectangles[left];
        const b = rectangles[right];
        expect(a.right <= b.left || b.right <= a.left || a.bottom <= b.top || b.bottom <= a.top).toBe(true);
      }
    }
    const xs = laidOut.map((node) => node.position.x);
    const ys = laidOut.map((node) => node.position.y);
    const width = Math.max(...xs) - Math.min(...xs) + 306;
    const height = Math.max(...ys) - Math.min(...ys) + 220;
    expect(Math.max(width / height, height / width)).toBeLessThan(3);
  });
});
