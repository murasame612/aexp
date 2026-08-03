import { MarkerType, Position, type Edge, type Node } from "@xyflow/react";
import type { CSSProperties } from "react";
import type { EvidenceChain, EvidenceChainEdge, EvidenceChainNode, EvidenceChainRunCandidate, EvidenceEdgeType, EvidenceGraphPatch, EvidenceLayoutIntent, EvidenceNodeType, EvidenceProposal, EvidenceResearchProjectionDTO } from "./types";
import { runTitle, text } from "./utils";

export const evidenceNodeTypes: EvidenceNodeType[] = ["group", "dataset", "protocol", "claim", "issue", "plan", "hypothesis", "experiment", "conclusion", "note"];
export const evidenceAuthoringNodeTypes: EvidenceNodeType[] = ["hypothesis", "experiment", "claim", "conclusion", "issue", "note"];
export const evidenceEdgeTypes: EvidenceEdgeType[] = ["uses", "supports", "weakens", "reveals_issue", "supersedes", "next_step", "related_to", "does_not_prove", "custom"];
export const protocolGroupMemberTypes: EvidenceNodeType[] = ["dataset", "run", "plan", "experiment"];

export function resolveProjectEvidenceChainSelection({
  requestedId,
  currentId,
  chains,
  primaryId
}: {
  requestedId?: string;
  currentId?: string;
  chains?: Array<Pick<EvidenceChain, "id" | "role" | "status">>;
  primaryId?: string;
}): string | undefined {
  const requested = requestedId?.trim() || "";
  const current = currentId?.trim() || "";
  const primary = primaryId?.trim() || "";

  // Until the project-scoped list arrives, preserve an explicit deep link and
  // accept an independently resolved Primary Map. Returning undefined means
  // discovery is still pending, not that the project has no Map.
  if (chains === undefined) return requested || primary || current || undefined;

  const byId = new Map(chains.map((chain) => [chain.id, chain]));
  if (requested && byId.has(requested)) return requested;
  if (current && byId.has(current)) return current;
  if (primary) return primary;

  return chains.find((chain) => chain.role === "primary" && chain.status === "active")?.id
    || chains.find((chain) => chain.status === "active")?.id
    || chains[0]?.id
    || "";
}

export function isProtocolGroupMemberType(type: EvidenceNodeType): boolean {
  return protocolGroupMemberTypes.includes(type);
}

export interface EvidenceNodeData extends Record<string, unknown> {
  type: EvidenceNodeType;
  title: string;
  body: string;
  runTitle?: string;
  runId?: string;
  sourceRunIds?: string[];
  sourceSnapshotIds?: string[];
  projectCardId?: string;
  status?: string;
  runKind?: string;
  keyMetrics?: string;
  evidenceLevel?: string;
  gitBranch?: string;
  gitCommit?: string;
  gitDirty?: boolean;
  gitDiffHash?: string;
  gitDiffPath?: string;
  pinned?: boolean;
  occurredAt?: string;
  readOnly?: boolean;
  draft?: boolean;
  proposalRunId?: string;
  sourceNodeId?: string;
  target_map_id?: string;
  target_revision?: number;
  target_graph_hash?: string;
  target_node_ids?: string[];
  summary?: string;
  groupId?: string;
  groupKind?: "protocol";
  version?: string;
  provenanceSummary?: string;
  collapsed?: boolean;
  groupMemberCount?: number;
  groupInternalEdgeCount?: number;
  groupExternalEdgeCount?: number;
  projectedRelationCount?: number;
  projectedMemberNodeIds?: string[];
  projectedMemberTitles?: string[];
  sharedThreadIds?: string[];
  canonicalThreadId?: string;
  unassignedReason?: string;
  resultDisposition?: "conclusion" | "issue" | "mixed" | "pending";
  dispositionReason?: string;
  projectionOnly?: "interpretation";
  interpretationEdgeId?: string;
  interpretationSourceNodeId?: string;
  interpretationOutcomeNodeId?: string;
  mapRefStatus?: "current" | "stale" | "archived" | "missing";
  onOpenRun?: (runId: string) => void;
  onOpenMap?: (mapId: string) => void;
  onUpdateNode?: (nodeId: string, patch: Partial<EvidenceNodeData>) => void;
  onResizeNode?: (nodeId: string, size: { width: number; height: number }) => void;
  labels?: EvidenceBoardLabels;
}

export interface EvidenceEdgeData extends Record<string, unknown> {
  type: EvidenceEdgeType;
  rationale: string;
  autoHandles?: boolean;
  routeLane?: number;
  routePoints?: EvidenceRoutePoint[];
  routeLabelPoint?: EvidenceRoutePoint;
  routeSafe?: boolean;
  readOnly?: boolean;
  draft?: boolean;
  proposalRunId?: string;
  sourceEdgeId?: string;
  focusVisible?: boolean;
  focusContext?: boolean;
  focusSourceOffsetX?: number;
  focusSourceOffsetY?: number;
  focusTargetOffsetX?: number;
  focusTargetOffsetY?: number;
  onSelectEdge?: (edgeId: string) => void;
  onUpdateEdge?: (edgeId: string, patch: { type?: EvidenceEdgeType; label?: string; rationale?: string }) => void;
  labels?: EvidenceBoardLabels;
}

export interface EvidenceRoutePoint {
  x: number;
  y: number;
}

export type EvidenceFlowNode = Node<EvidenceNodeData, "evidence">;
export type EvidenceFlowEdge = Edge<EvidenceEdgeData>;

export interface EvidenceProposalPreview {
  runId: string;
  proposalId?: string;
  title?: string;
  chainId: string;
  routingReason: string;
  layoutIntent?: EvidenceLayoutIntent;
  nodes: EvidenceFlowNode[];
  edges: EvidenceFlowEdge[];
}

export function shouldOverlayEvidenceProposal(
  previewChainId: string,
  selectedChainId: string,
  explicitlyRequested: boolean
): boolean {
  return explicitlyRequested && Boolean(previewChainId) && previewChainId === selectedChainId;
}

export function normalizeEvidenceLayoutIntent(value: unknown): EvidenceLayoutIntent | undefined {
  if (!value || typeof value !== "object") return undefined;
  const raw = value as { flow?: unknown; ranks?: unknown; rationale?: unknown };
  if (raw.flow !== "left_to_right" || !Array.isArray(raw.ranks) || raw.ranks.length === 0) return undefined;
  const seen = new Set<string>();
  const ranks: string[][] = [];
  for (const rank of raw.ranks) {
    if (!Array.isArray(rank) || rank.length === 0) return undefined;
    const normalized: string[] = [];
    for (const rawID of rank) {
      if (typeof rawID !== "string" || !rawID.trim() || seen.has(rawID.trim())) return undefined;
      const id = rawID.trim();
      seen.add(id);
      normalized.push(id);
    }
    ranks.push(normalized);
  }
  return {
    flow: "left_to_right",
    ranks,
    ...(typeof raw.rationale === "string" && raw.rationale.trim()
      ? { rationale: raw.rationale.trim() }
      : {})
  };
}

export function remapEvidenceLayoutIntent(
  intent: EvidenceLayoutIntent | undefined,
  nodeIDs: Map<string, string>
): EvidenceLayoutIntent | undefined {
  if (!intent) return undefined;
  return {
    ...intent,
    ranks: intent.ranks.map((rank) => rank.map((id) => nodeIDs.get(id) || id))
  };
}

export type EvidenceMapReferenceStatus = "current" | "stale" | "archived" | "missing";

export function evidenceMapReferenceStatus(
  node: Pick<EvidenceNodeData, "type" | "target_revision" | "target_graph_hash">,
  target?: { status?: string; revision?: number; graph_hash?: string }
): EvidenceMapReferenceStatus | undefined {
  if (node.type !== "map_ref") return undefined;
  if (!target) return "missing";
  if (target.status === "archived") return "archived";
  if (target.revision !== node.target_revision || target.graph_hash !== node.target_graph_hash) return "stale";
  return "current";
}

export function evidenceWorkspaceProposalPreview(proposal: EvidenceProposal): EvidenceProposalPreview | null {
  if (proposal.status !== "pending" || !proposal.target_map_id) return null;
  let patch: EvidenceGraphPatch;
  try {
    patch = JSON.parse(proposal.patch_json || "") as EvidenceGraphPatch;
  } catch {
    return null;
  }
  if (!patch || typeof patch !== "object" || !Array.isArray(patch.nodes) || !Array.isArray(patch.edges)) {
    return null;
  }
  const chainId = proposal.target_map_id || patch.chain_id;
  if (!chainId) return null;
  const previewKey = proposal.id;
  const layoutIntent = normalizeEvidenceLayoutIntent(patch.layout_intent);
  const patchNodes = [...(patch.nodes || []), ...(patch.upsert_nodes || [])];
  const patchEdges = [...(patch.edges || []), ...(patch.upsert_edges || [])];
  const nodeIDs = new Map<string, string>();
  for (const node of patchNodes) {
    if (!node || typeof node.id !== "string" || !node.id) return null;
    nodeIDs.set(node.id, `draft:${previewKey}:${node.id}`);
  }
  const nodes = patchNodes.map((node) => {
    const mapped = apiNodeToFlowNode({ ...node, id: nodeIDs.get(node.id)! });
    return {
      ...mapped,
      draggable: false,
      connectable: false,
      deletable: false,
      selectable: true,
      data: {
        ...mapped.data,
        groupId: mapped.data.groupId ? nodeIDs.get(mapped.data.groupId) || mapped.data.groupId : undefined,
        readOnly: true,
        draft: true,
        proposalRunId: previewKey,
        sourceNodeId: node.id
      }
    };
  });
  const edges = patchEdges.map((edge) => {
    const mapped = apiEdgeToFlowEdge({
      ...edge,
      id: `draft-edge:${previewKey}:${edge.id}`,
      source_node_id: nodeIDs.get(edge.source_node_id) || edge.source_node_id,
      target_node_id: nodeIDs.get(edge.target_node_id) || edge.target_node_id
    });
    return {
      ...mapped,
      selectable: false,
      deletable: false,
      data: {
        ...mapped.data,
        type: mapped.data?.type || edge.type || "next_step",
        rationale: mapped.data?.rationale || edge.rationale || "",
        readOnly: true,
        draft: true,
        proposalRunId: previewKey,
        sourceEdgeId: edge.id
      }
    };
  });
  return {
    runId: previewKey,
    proposalId: proposal.id,
    title: proposal.summary,
    chainId,
    routingReason: proposal.routing_reason || patch.routing_reason || "",
    layoutIntent,
    nodes,
    edges
  };
}

export interface EvidenceBoardLabels {
  titlePlaceholder: string;
  nodeBodyPlaceholder: string;
  openRun: string;
  relationLabel: string;
  rationale: string;
  done: string;
}

export function edgeTypeLabel(type: EvidenceEdgeType) {
  switch (type) {
    case "uses":
      return "使用";
    case "supports":
      return "增强";
    case "weakens":
      return "削弱";
    case "reveals_issue":
      return "暴露问题";
    case "supersedes":
      return "取代";
    case "does_not_prove":
      return "不能证明";
    case "next_step":
      return "下一步";
    case "related_to":
      return "相关";
    case "custom":
      return "自定义";
  }
}

export function nodeTypeLabel(type: EvidenceNodeType, claimKind?: unknown) {
  if (type === "claim" && String(claimKind || "").trim().toLocaleLowerCase() === "hypothesis") {
    return "假说";
  }
  switch (type) {
    case "dataset":
      return "数据版本";
    case "protocol":
      return "实验设计（旧协议）";
    case "run":
      return "实验";
    case "claim":
      return "实验结果";
    case "issue":
      return "中途问题";
    case "hypothesis":
      return "假说";
    case "experiment":
      return "实验设计";
    case "plan":
      return "实验设计";
    case "conclusion":
      return "研究结论";
    case "note":
      return "笔记";
    case "map_ref":
      return "Topic 引用";
    case "group":
      return "历史协议集合";
  }
}

export function proposalNoticeScopeKey(chainId: string, proposalId: string, revision: number) {
  return `${chainId.trim()}\u0000${proposalId.trim()}\u0000${revision || 0}`;
}

export type EvidenceReadingSectionID = "claims" | "issues" | "plans" | "context";

export interface EvidenceReadingSection {
  id: EvidenceReadingSectionID;
  label: string;
  description: string;
  nodes: EvidenceFlowNode[];
}

export interface EvidenceNeighbour {
  node: EvidenceFlowNode;
  edges: EvidenceFlowEdge[];
  depth: number;
}

export interface EvidenceNeighborhood {
  center: EvidenceFlowNode | null;
  upstream: EvidenceNeighbour[];
  downstream: EvidenceNeighbour[];
  related: EvidenceNeighbour[];
}

export interface EvidenceFocusGraph {
  nodes: EvidenceFlowNode[];
  edges: EvidenceFlowEdge[];
}

const readingSectionMeta: Array<Omit<EvidenceReadingSection, "nodes"> & { types: EvidenceNodeType[] }> = [
  { id: "claims", label: "当前认识", description: "结论、主张与假说", types: ["claim", "conclusion", "hypothesis"] },
  { id: "issues", label: "问题与边界", description: "尚未解决的问题和限制", types: ["issue"] },
  { id: "plans", label: "行动与验证", description: "计划和实验设计", types: ["plan", "experiment"] },
  { id: "context", label: "依据与上下文", description: "数据、协议、实验与引用", types: ["dataset", "protocol", "run", "note", "map_ref", "group"] }
];

function evidenceReadingNodeKey(node: EvidenceFlowNode) {
  return `${node.data.occurredAt || ""}\u0000${node.data.title || ""}\u0000${node.id}`;
}

export function evidenceReadingSections(nodes: EvidenceFlowNode[], query = ""): EvidenceReadingSection[] {
  const normalized = query.trim().toLocaleLowerCase();
  const matches = (node: EvidenceFlowNode) => !normalized || [
    node.data.title,
    node.data.body,
    node.data.runId,
    nodeTypeLabel(node.data.type, node.data.claimKind || node.data.claim_kind)
  ].some((value) => String(value || "").toLocaleLowerCase().includes(normalized));
  return readingSectionMeta.map((section) => ({
    id: section.id,
    label: section.label,
    description: section.description,
    nodes: nodes
      .filter((node) => !node.data.draft && section.types.includes(node.data.type) && matches(node))
      .sort((left, right) => evidenceReadingNodeKey(right).localeCompare(evidenceReadingNodeKey(left)))
  })).filter((section) => section.nodes.length > 0);
}

export type EvidenceResearchStage = "hypothesis" | "design" | "result" | "conclusion" | "issue";

export const evidenceResearchStages: Array<{ id: EvidenceResearchStage; label: string; description: string }> = [
  { id: "hypothesis", label: "假设", description: "要验证的研究判断" },
  { id: "design", label: "实验设计", description: "如何验证、比较与判定" },
  { id: "result", label: "实验结果", description: "从不可变 Run 得到的事实" },
  { id: "conclusion", label: "解释与判断", description: "结果如何被解释，以及为何进入相应去向" },
  { id: "issue", label: "结论与去向", description: "正式结论、限制、分叉与新假设" }
];

export interface EvidenceResearchThread {
  id: string;
  title: string;
  rootNodeId: string;
  parentThreadId?: string;
  explicitHypothesis: boolean;
  stages: Record<EvidenceResearchStage, EvidenceFlowNode[]>;
}

export interface EvidenceResearchCrossRelation {
  edge: EvidenceFlowEdge;
  sourceThreadId: string;
  targetThreadId: string;
  kind: "branch" | "causal";
}

export interface EvidenceResearchProtocolGroup {
  group: EvidenceFlowNode;
  members: Array<{ node: EvidenceFlowNode; threadId: string }>;
  relations: Array<{ edge: EvidenceFlowEdge; scope: "internal" | "external" }>;
}

export interface EvidenceResearchProjection {
  contractVersion?: string;
  threads: EvidenceResearchThread[];
  unassigned: EvidenceFlowNode[];
  crossThreadRelations: EvidenceResearchCrossRelation[];
  protocolGroups: EvidenceResearchProtocolGroup[];
  capacity?: EvidenceResearchProjectionDTO["capacity"];
  structuralHealth?: EvidenceResearchProjectionDTO["structural_health"];
}

export interface EvidenceAdjacentStageRelation {
  edge: EvidenceFlowEdge;
  direction: "incoming" | "outgoing";
  otherNode: EvidenceFlowNode;
  otherStage: EvidenceResearchStage;
}

export interface EvidenceThreadRelationFocus {
  originNodeId: string;
  visiblePeerNodeIds: string[];
  directRelationCount: number;
  hiddenRelationCount: number;
  disconnected: boolean;
}

export interface EvidenceRibbonRect {
  left: number;
  top: number;
  width: number;
  height: number;
}

/**
 * Route one semantic relation around the card faces without changing the
 * five-column swimlane. Horizontal relations use the facing card edges;
 * same-column relations fall back to a vertical curve.
 */
export function evidenceThreadRibbonPath(source: EvidenceRibbonRect, target: EvidenceRibbonRect) {
  const sourceCenterX = source.left + source.width / 2;
  const sourceCenterY = source.top + source.height / 2;
  const targetCenterX = target.left + target.width / 2;
  const targetCenterY = target.top + target.height / 2;
  const centerDeltaX = targetCenterX - sourceCenterX;

  if (Math.abs(centerDeltaX) < 24) {
    const direction = targetCenterY >= sourceCenterY ? 1 : -1;
    const sourceX = sourceCenterX;
    const sourceY = direction > 0 ? source.top + source.height : source.top;
    const targetX = targetCenterX;
    const faceGap = direction > 0
      ? target.top - (source.top + source.height)
      : source.top - (target.top + target.height);
    const targetClearance = Math.min(12, Math.max(7, Math.max(0, faceGap) * 0.55));
    const targetY = direction > 0 ? target.top - targetClearance : target.top + target.height + targetClearance;
    const control = Math.max(30, Math.min(120, Math.abs(targetY - sourceY) * 0.44));
    return `M ${sourceX} ${sourceY} C ${sourceX} ${sourceY + direction * control}, ${targetX} ${targetY - direction * control}, ${targetX} ${targetY}`;
  }

  const direction = centerDeltaX > 0 ? 1 : -1;
  const sourceX = direction > 0 ? source.left + source.width : source.left;
  // Keep almost the full inter-column gutter available to the curve. Scaling
  // the target clearance with the gutter squeezed the actual path into an
  // eight-pixel hook even after the columns were moved farther apart.
  const targetClearance = 6;
  const targetX = direction > 0 ? target.left - targetClearance : target.left + target.width + targetClearance;
  const distance = Math.abs(targetX - sourceX);
  const control = Math.max(8, Math.min(170, distance * 0.42));
  return `M ${sourceX} ${sourceCenterY} C ${sourceX + direction * control} ${sourceCenterY}, ${targetX - direction * control} ${targetCenterY}, ${targetX} ${targetCenterY}`;
}

/**
 * Keep the compact swimlane readable while still exposing its research spine.
 * Only direct relations between neighboring stages are eligible for the hover
 * treatment; long-range and cross-thread context remains in the detail view.
 */
export function evidenceAdjacentStageRelations(
  thread: EvidenceResearchThread,
  edges: EvidenceFlowEdge[]
): Map<string, EvidenceAdjacentStageRelation[]> {
  const stageIndex = new Map(evidenceResearchStages.map((stage, index) => [stage.id, index]));
  const nodes = new Map<string, { node: EvidenceFlowNode; stage: EvidenceResearchStage }>();
  for (const stage of evidenceResearchStages) {
    for (const node of thread.stages[stage.id]) nodes.set(node.id, { node, stage: stage.id });
  }
  const relations = new Map<string, EvidenceAdjacentStageRelation[]>();
  const append = (nodeID: string, relation: EvidenceAdjacentStageRelation) => {
    relations.set(nodeID, [...(relations.get(nodeID) || []), relation]);
  };
  for (const edge of edges) {
    if (edge.data?.draft || edge.source === edge.target) continue;
    const source = nodes.get(edge.source);
    const target = nodes.get(edge.target);
    if (!source || !target) continue;
    if (Math.abs((stageIndex.get(source.stage) || 0) - (stageIndex.get(target.stage) || 0)) !== 1) continue;
    append(edge.source, { edge, direction: "outgoing", otherNode: target.node, otherStage: target.stage });
    append(edge.target, { edge, direction: "incoming", otherNode: source.node, otherStage: source.stage });
  }
  // Interpretation cards are read-only projections of a real Result outcome
  // edge. Split that long edge into two adjacent visual hops without writing a
  // synthetic node or edge back to the Evidence Map.
  for (const interpretation of thread.stages.conclusion.filter((node) => node.data.projectionOnly === "interpretation")) {
    const source = nodes.get(String(interpretation.data.interpretationSourceNodeId || ""));
    const outcome = nodes.get(String(interpretation.data.interpretationOutcomeNodeId || ""));
    const edge = edges.find((candidate) => candidate.id === interpretation.data.interpretationEdgeId)
      || ({ id: `projection:${interpretation.id}`, source: source?.node.id || "", target: outcome?.node.id || "", data: { type: "next_step", rationale: String(interpretation.data.body || "") } } satisfies EvidenceFlowEdge);
    if (source) {
      append(source.node.id, { edge, direction: "outgoing", otherNode: interpretation, otherStage: "conclusion" });
      append(interpretation.id, { edge, direction: "incoming", otherNode: source.node, otherStage: source.stage });
    }
    if (outcome) {
      append(interpretation.id, { edge, direction: "outgoing", otherNode: outcome.node, otherStage: outcome.stage });
      append(outcome.node.id, { edge, direction: "incoming", otherNode: interpretation, otherStage: "conclusion" });
    }
  }
  for (const rows of relations.values()) {
    rows.sort((left, right) => {
      const leftKey = `${stageIndex.get(left.otherStage)}:${left.direction}:${String(left.edge.label || left.edge.data?.type || "")}:${left.otherNode.data.title || left.otherNode.id}`;
      const rightKey = `${stageIndex.get(right.otherStage)}:${right.direction}:${String(right.edge.label || right.edge.data?.type || "")}:${right.otherNode.data.title || right.otherNode.id}`;
      return leftKey.localeCompare(rightKey);
    });
  }
  return relations;
}

/**
 * Relations used by card focus must follow every semantic edge that is visible
 * to the reader, even when a legacy relation skips one of the five stages.
 * Outcome edges represented by an Interpretation projection remain split into
 * their two adjacent visual hops so focus matches the cards on screen.
 */
export function evidenceThreadFocusRelations(
  thread: EvidenceResearchThread,
  edges: EvidenceFlowEdge[]
): Map<string, EvidenceAdjacentStageRelation[]> {
  const relations = evidenceAdjacentStageRelations(thread, edges);
  const stageIndex = new Map(evidenceResearchStages.map((stage, index) => [stage.id, index]));
  const nodes = new Map<string, { node: EvidenceFlowNode; stage: EvidenceResearchStage }>();
  for (const stage of evidenceResearchStages) {
    for (const node of thread.stages[stage.id]) nodes.set(node.id, { node, stage: stage.id });
  }
  const projectedEdgeIDs = new Set(thread.stages.conclusion
    .filter((node) => node.data.projectionOnly === "interpretation")
    .map((node) => String(node.data.interpretationEdgeId || ""))
    .filter(Boolean));
  const append = (nodeID: string, relation: EvidenceAdjacentStageRelation) => {
    const current = relations.get(nodeID) || [];
    if (current.some((item) => item.edge.id === relation.edge.id && item.otherNode.id === relation.otherNode.id)) return;
    relations.set(nodeID, [...current, relation]);
  };

  for (const edge of edges) {
    if (edge.data?.draft || edge.source === edge.target || projectedEdgeIDs.has(edge.id)) continue;
    const source = nodes.get(edge.source);
    const target = nodes.get(edge.target);
    if (!source || !target) continue;
    append(edge.source, { edge, direction: "outgoing", otherNode: target.node, otherStage: target.stage });
    append(edge.target, { edge, direction: "incoming", otherNode: source.node, otherStage: source.stage });
  }

  for (const rows of relations.values()) {
    rows.sort((left, right) => {
      const leftKey = `${stageIndex.get(left.otherStage)}:${left.direction}:${String(left.edge.label || left.edge.data?.type || "")}:${left.otherNode.data.title || left.otherNode.id}`;
      const rightKey = `${stageIndex.get(right.otherStage)}:${right.direction}:${String(right.edge.label || right.edge.data?.type || "")}:${right.otherNode.data.title || right.otherNode.id}`;
      return leftKey.localeCompare(rightKey);
    });
  }
  return relations;
}

/**
 * Derive one stable focus state from the unfiltered semantic thread and its
 * current visible projection. This prevents search from turning a hidden peer
 * into a false "no relation" state and makes disconnected cards explicit.
 */
export function evidenceThreadRelationFocus(
  thread: EvidenceResearchThread,
  visibleThread: EvidenceResearchThread,
  edges: EvidenceFlowEdge[],
  originNodeId: string
): EvidenceThreadRelationFocus {
  const relationsByNode = evidenceThreadFocusRelations(thread, edges);
  const allRelations = relationsByNode.get(originNodeId) || [];
  const neutralTypes = new Set<EvidenceEdgeType>(["related_to", "custom"]);
  const connectedNodeIds = new Set<string>([originNodeId]);
  const walk = (direction: "incoming" | "outgoing") => {
    const visited = new Set<string>([originNodeId]);
    const queue = [originNodeId];
    while (queue.length) {
      const nodeID = queue.shift()!;
      for (const relation of relationsByNode.get(nodeID) || []) {
        const type = relation.edge.data?.type || "related_to";
        if (neutralTypes.has(type) || relation.direction !== direction || visited.has(relation.otherNode.id)) continue;
        visited.add(relation.otherNode.id);
        connectedNodeIds.add(relation.otherNode.id);
        queue.push(relation.otherNode.id);
      }
    }
  };
  // Preserve direction while walking. Going to an ancestor may not then turn
  // around and enter a sibling experiment; likewise a descendant may not walk
  // back into another result that happens to share an outcome.
  walk("incoming");
  walk("outgoing");
  // Neutral context is useful when directly attached to the selected card,
  // but it must not turn one hover into a highlight of the entire Topic.
  for (const relation of allRelations) {
    if (neutralTypes.has(relation.edge.data?.type || "related_to")) connectedNodeIds.add(relation.otherNode.id);
  }
  connectedNodeIds.delete(originNodeId);
  const visibleNodeIds = new Set(evidenceResearchStages.flatMap((stage) => visibleThread.stages[stage.id].map((node) => node.id)));
  const visiblePeerNodeIds = [...connectedNodeIds].filter((nodeID) => visibleNodeIds.has(nodeID)).sort();
  return {
    originNodeId,
    visiblePeerNodeIds,
    directRelationCount: allRelations.length,
    hiddenRelationCount: [...connectedNodeIds].filter((nodeID) => !visibleNodeIds.has(nodeID)).length,
    disconnected: allRelations.length === 0
  };
}

function evidenceResultDisposition(node: EvidenceFlowNode) {
  return String(node.data.resultDisposition || node.data.result_disposition || "").trim().toLocaleLowerCase();
}

function evidenceDispositionReason(node: EvidenceFlowNode) {
  return String(node.data.dispositionReason || node.data.disposition_reason || "").trim();
}

function evidenceInterpretationLabel(edge: EvidenceFlowEdge | undefined, outcome: EvidenceFlowNode | undefined, disposition: string) {
  if (outcome?.data.type === "issue") return disposition === "mixed" ? "同时暴露限制" : "暂不能形成结论";
  if (edge?.data?.type === "weakens") return "证据削弱结论";
  if (edge?.data?.type === "does_not_prove") return "证据尚不足以证明";
  return "证据支持结论";
}

/**
 * Insert a compact, read-only interpretation lane between Result and durable
 * outcomes. The cards are projections of real edges and are never serialized.
 */
export function projectEvidenceResearchInterpretations(
  projection: EvidenceResearchProjection,
  edges: EvidenceFlowEdge[]
): EvidenceResearchProjection {
  const acceptedEdges = edges.filter((edge) => !edge.data?.draft);
  return {
    ...projection,
    threads: projection.threads.map((thread) => {
      const results = thread.stages.result;
      const outcomes = [...thread.stages.conclusion, ...thread.stages.issue];
      const outcomeByID = new Map(outcomes.map((node) => [node.id, node]));
      const interpretations: EvidenceFlowNode[] = [];
      for (const result of results) {
        const disposition = evidenceResultDisposition(result);
        const outcomeEdges = acceptedEdges.filter((edge) => {
          if (edge.source !== result.id) return false;
          const outcome = outcomeByID.get(edge.target);
          return (outcome?.data.type === "conclusion" && ["supports", "weakens", "does_not_prove"].includes(edge.data?.type || ""))
            || (outcome?.data.type === "issue" && edge.data?.type === "reveals_issue");
        });
        if (!outcomeEdges.length) {
          interpretations.push({
            id: `projection:interpretation:${result.id}:pending`,
            type: "evidence",
            position: { x: 0, y: 0 },
            data: {
              type: "note",
              title: disposition === "pending" ? "待解释" : "结果尚未解释",
              body: evidenceDispositionReason(result),
              projectionOnly: "interpretation",
              interpretationSourceNodeId: result.id,
              projectedRelationCount: 1,
              readOnly: true
            }
          });
          continue;
        }
        for (const edge of outcomeEdges.sort((left, right) => left.id.localeCompare(right.id))) {
          const outcome = outcomeByID.get(edge.target);
          interpretations.push({
            id: `projection:interpretation:${edge.id}`,
            type: "evidence",
            position: { x: 0, y: 0 },
            data: {
              type: "note",
              title: evidenceInterpretationLabel(edge, outcome, disposition),
              body: String(edge.data?.rationale || (outcome?.data.type === "issue" ? evidenceDispositionReason(result) : "")),
              projectionOnly: "interpretation",
              interpretationEdgeId: edge.id,
              interpretationSourceNodeId: result.id,
              interpretationOutcomeNodeId: outcome?.id,
              projectedRelationCount: 2,
              readOnly: true
            }
          });
        }
      }
      return {
        ...thread,
        stages: {
          ...thread.stages,
          conclusion: interpretations,
          issue: outcomes
        }
      };
    })
  };
}

/**
 * Order cards inside the five fixed swimlane columns so directly related
 * cards stay vertically close. This is a deterministic, bounded
 * barycentric layout: relation distance dominates, while a small stability
 * term keeps cards near their accepted/API order and prevents refresh jitter.
 */
export function orderEvidenceThreadStages(
  thread: EvidenceResearchThread,
  edges: EvidenceFlowEdge[]
): EvidenceResearchThread {
  const relations = evidenceAdjacentStageRelations(thread, edges);
  const stages = Object.fromEntries(evidenceResearchStages.map((stage) => [
    stage.id,
    [...thread.stages[stage.id]]
  ])) as Record<EvidenceResearchStage, EvidenceFlowNode[]>;
  const originalOrder = new Map<string, number>();
  for (const stage of evidenceResearchStages) {
    stages[stage.id].forEach((node, index) => originalOrder.set(node.id, index));
  }
  const normalizedIndex = (index: number, length: number) => length > 1 ? index / (length - 1) : 0;
  const reorder = (stageID: EvidenceResearchStage, neighbourStageID: EvidenceResearchStage) => {
    const stageNodes = stages[stageID];
    const neighbourNodes = stages[neighbourStageID];
    if (stageNodes.length < 2 || !neighbourNodes.length) return;
    const neighbourOrder = new Map(neighbourNodes.map((node, index) => [node.id, normalizedIndex(index, neighbourNodes.length)]));
    const score = (node: EvidenceFlowNode) => {
      const linked = (relations.get(node.id) || [])
        .filter((relation) => relation.otherStage === neighbourStageID)
        .map((relation) => neighbourOrder.get(relation.otherNode.id))
        .filter((value): value is number => value !== undefined);
      const stable = normalizedIndex(originalOrder.get(node.id) || 0, stageNodes.length);
      if (!linked.length) return { linked: false, value: stable };
      const barycenter = linked.reduce((sum, value) => sum + value, 0) / linked.length;
      return { linked: true, value: barycenter * 0.86 + stable * 0.14 };
    };
    stageNodes.sort((left, right) => {
      const leftScore = score(left);
      const rightScore = score(right);
      const distance = leftScore.value - rightScore.value;
      if (Math.abs(distance) > 1e-9) return distance;
      if (leftScore.linked !== rightScore.linked) return leftScore.linked ? -1 : 1;
      return (originalOrder.get(left.id) || 0) - (originalOrder.get(right.id) || 0) || left.id.localeCompare(right.id);
    });
  };

  // Alternating sweeps approximate the global minimum edge-distance and
  // crossing objective without factorial search or a layout dependency.
  for (let pass = 0; pass < 4; pass += 1) {
    for (let index = 1; index < evidenceResearchStages.length; index += 1) {
      reorder(evidenceResearchStages[index].id, evidenceResearchStages[index - 1].id);
    }
    for (let index = evidenceResearchStages.length - 2; index >= 0; index -= 1) {
      reorder(evidenceResearchStages[index].id, evidenceResearchStages[index + 1].id);
    }
  }
  return { ...thread, stages };
}

export function apiResearchProjectionToFlow(projection: EvidenceResearchProjectionDTO): EvidenceResearchProjection {
  const mapCard = (card: EvidenceResearchProjectionDTO["threads"][number]["stages"]["hypothesis"][number]) => {
    const node = apiNodeToFlowNode(card.node);
    return {
      ...node,
      data: {
        ...node.data,
        projectedRelationCount: card.relation_count || 0,
        groupMemberCount: card.member_count || node.data.groupMemberCount,
        projectedMemberNodeIds: card.member_node_ids || [],
        projectedMemberTitles: card.member_titles || [],
        sharedThreadIds: card.shared_thread_ids || [],
        canonicalThreadId: card.canonical_thread_id || ""
      }
    };
  };
  return {
    contractVersion: projection.evidence_contract_version,
    threads: (projection.threads || []).map((thread) => {
      const durableOutcomes = [
        ...(thread.stages?.conclusion || []).map(mapCard),
        ...(thread.stages?.issue || []).map(mapCard)
      ];
      const interpretations = (thread.interpretations || []).map((interpretation) => ({
        id: `projection:${interpretation.id}`,
        type: "evidence" as const,
        position: { x: 0, y: 0 },
        data: {
          type: "note" as const,
          title: interpretation.label,
          body: interpretation.rationale || "",
          projectionOnly: "interpretation",
          interpretationEdgeId: interpretation.edge_id,
          interpretationSourceNodeId: interpretation.result_node_id,
          interpretationOutcomeNodeId: interpretation.outcome_node_id,
          interpretationKind: interpretation.kind,
          projectedRelationCount: interpretation.outcome_node_id ? 2 : 1,
          readOnly: true
        }
      } satisfies EvidenceFlowNode));
      return {
        id: thread.id,
        title: thread.title,
        rootNodeId: thread.root_node_id,
        parentThreadId: thread.parent_thread_id,
        explicitHypothesis: thread.explicit_hypothesis,
        stages: {
          hypothesis: (thread.stages?.hypothesis || []).map(mapCard),
          design: (thread.stages?.design || []).map(mapCard),
          result: (thread.stages?.result || []).map(mapCard),
          conclusion: interpretations,
          issue: durableOutcomes
        }
      };
    }),
    unassigned: (projection.unassigned || []).map((entry) => {
      const node = mapCard(entry.card);
      return { ...node, data: { ...node.data, unassignedReason: entry.reason } };
    }),
    crossThreadRelations: (projection.cross_thread_relations || []).map((relation) => ({
      edge: apiEdgeToFlowEdge(relation.edge),
      sourceThreadId: relation.source_thread_id,
      targetThreadId: relation.target_thread_id,
      kind: relation.kind
    })),
    protocolGroups: (projection.protocol_groups || []).map((protocol) => ({
      group: apiNodeToFlowNode(protocol.group),
      members: (protocol.members || []).map((member) => ({
        node: apiNodeToFlowNode(member.node),
        threadId: member.thread_id || ""
      })),
      relations: (protocol.relations || []).map((relation) => ({
        edge: apiEdgeToFlowEdge(relation.edge),
        scope: relation.scope
      }))
    })),
    capacity: projection.capacity,
    structuralHealth: projection.structural_health
  };
}

function evidenceClaimKind(node: EvidenceFlowNode) {
  return String(node.data.claimKind || node.data.claim_kind || "").trim().toLocaleLowerCase();
}

export function evidenceResearchStage(node: EvidenceFlowNode): EvidenceResearchStage | null {
  if (node.data.type === "hypothesis" || (node.data.type === "claim" && evidenceClaimKind(node) === "hypothesis")) return "hypothesis";
  if (["plan", "protocol", "experiment"].includes(node.data.type)) return "design";
  if (node.data.type === "claim") return "result";
  if (node.data.type === "conclusion") return "conclusion";
  if (node.data.type === "issue") return "issue";
  return null;
}

function emptyResearchStages(): Record<EvidenceResearchStage, EvidenceFlowNode[]> {
  return { hypothesis: [], design: [], result: [], conclusion: [], issue: [] };
}

function researchNodeKey(node: EvidenceFlowNode) {
  return `${node.data.occurredAt || ""}\u0000${node.data.title || ""}\u0000${node.id}`;
}

/**
 * Derive a stable research-thread reading model from the accepted Evidence DAG.
 * This is deliberately read-only: it never writes positions, group membership,
 * revisions or synthetic graph edges back to the stored map.
 */
export function evidenceResearchThreads(nodes: EvidenceFlowNode[], edges: EvidenceFlowEdge[]): EvidenceResearchProjection {
  const accepted = nodes.filter((node) => !node.data.draft);
  const byID = new Map(accepted.map((node) => [node.id, node]));
  const semanticEdges = edges
    .filter((edge) => !edge.data?.draft
      && edge.data?.type !== "related_to"
      && edge.data?.type !== "custom"
      && byID.has(edge.source)
      && byID.has(edge.target))
    .sort((left, right) => left.id.localeCompare(right.id));
  const graphNodes = accepted.filter((node) => node.data.type !== "group");
  const adjacency = new Map<string, Set<string>>();
  const assignmentAdjacency = new Map<string, Set<string>>();
  for (const node of graphNodes) adjacency.set(node.id, new Set());
  for (const node of graphNodes) assignmentAdjacency.set(node.id, new Set());
  for (const edge of semanticEdges) {
    if (!adjacency.has(edge.source) || !adjacency.has(edge.target)) continue;
    adjacency.get(edge.source)!.add(edge.target);
    adjacency.get(edge.target)!.add(edge.source);
    const source = byID.get(edge.source);
    const target = byID.get(edge.target);
    const startsChildThread = edge.data?.type === "next_step"
      && (source?.data.type === "issue" || source?.data.type === "conclusion")
      && target
      && evidenceResearchStage(target) === "hypothesis";
    if (!startsChildThread) {
      assignmentAdjacency.get(edge.source)!.add(edge.target);
      assignmentAdjacency.get(edge.target)!.add(edge.source);
    }
  }

  const components: string[][] = [];
  const seen = new Set<string>();
  for (const start of [...graphNodes].sort((left, right) => left.id.localeCompare(right.id))) {
    if (seen.has(start.id)) continue;
    const component: string[] = [];
    const queue = [start.id];
    seen.add(start.id);
    while (queue.length) {
      const current = queue.shift()!;
      component.push(current);
      for (const next of [...(adjacency.get(current) || [])].sort()) {
        if (seen.has(next)) continue;
        seen.add(next);
        queue.push(next);
      }
    }
    components.push(component.sort());
  }

  const threads: EvidenceResearchThread[] = [];
  const ownerByNode = new Map<string, string>();
  const unassignedIDs = new Set<string>();

  for (const component of components) {
    const componentNodes = component.map((id) => byID.get(id)!).filter(Boolean);
    let roots = componentNodes.filter((node) => evidenceResearchStage(node) === "hypothesis");
    if (!roots.length) {
      // Legacy evidence often starts from a plan, result or issue. Do not
      // pretend those disconnected fragments are complete research threads:
      // keep them visible in the triage bucket until an Agent or user adds an
      // explicit hypothesis. This is a read-only projection and does not
      // migrate or rewrite the stored graph.
      for (const node of componentNodes) unassignedIDs.add(node.id);
      continue;
    }
    roots = [...roots].sort((left, right) => researchNodeKey(left).localeCompare(researchNodeKey(right)));

    const distanceByRoot = new Map<string, Map<string, number>>();
    for (const root of roots) {
      const distance = new Map<string, number>([[root.id, 0]]);
      let frontier = [root.id];
      while (frontier.length) {
        const nextFrontier: string[] = [];
        for (const current of frontier) {
          const currentDistance = distance.get(current)!;
          for (const next of [...(assignmentAdjacency.get(current) || [])].sort()) {
            if (!component.includes(next) || distance.has(next)) continue;
            distance.set(next, currentDistance + 1);
            nextFrontier.push(next);
          }
        }
        frontier = nextFrontier;
      }
      distanceByRoot.set(root.id, distance);
    }

    const componentThreads = roots.map((root): EvidenceResearchThread => ({
      id: `thread:${root.id}`,
      title: root.data.title || "未命名研究线程",
      rootNodeId: root.id,
      explicitHypothesis: evidenceResearchStage(root) === "hypothesis",
      stages: emptyResearchStages()
    }));
    const threadByRoot = new Map(componentThreads.map((thread) => [thread.rootNodeId, thread]));
    threads.push(...componentThreads);

    for (const node of componentNodes) {
      const stage = evidenceResearchStage(node);
      if (!stage) {
        unassignedIDs.add(node.id);
        continue;
      }
      const root = roots.reduce((best, candidate) => {
        const candidateDistance = distanceByRoot.get(candidate.id)?.get(node.id) ?? Number.POSITIVE_INFINITY;
        const bestDistance = distanceByRoot.get(best.id)?.get(node.id) ?? Number.POSITIVE_INFINITY;
        return candidateDistance < bestDistance ? candidate : best;
      }, roots[0]);
      const thread = threadByRoot.get(root.id)!;
      thread.stages[stage].push(node);
      ownerByNode.set(node.id, thread.id);
    }
  }

  const threadByID = new Map(threads.map((thread) => [thread.id, thread]));

  for (const edge of semanticEdges) {
    const targetThread = ownerByNode.get(edge.target);
    const sourceThread = ownerByNode.get(edge.source);
    const source = byID.get(edge.source);
    const target = byID.get(edge.target);
    if (!sourceThread || !targetThread || sourceThread === targetThread || edge.data?.type !== "next_step" || !["issue", "conclusion"].includes(source?.data.type || "") || evidenceResearchStage(target!) !== "hypothesis") continue;
    const child = threadByID.get(targetThread);
    if (child && !child.parentThreadId) child.parentThreadId = sourceThread;
  }

  for (const thread of threads) {
    for (const stage of evidenceResearchStages) {
      thread.stages[stage.id].sort((left, right) => researchNodeKey(left).localeCompare(researchNodeKey(right)));
    }
  }
  const nonEmptyThreads = threads.filter((thread) => evidenceResearchStages.some((stage) => thread.stages[stage.id].length));
  const nonEmptyByID = new Map(nonEmptyThreads.map((thread) => [thread.id, thread]));
  const children = new Map<string, EvidenceResearchThread[]>();
  const roots: EvidenceResearchThread[] = [];
  for (const thread of nonEmptyThreads) {
    if (thread.parentThreadId && nonEmptyByID.has(thread.parentThreadId)) {
      children.set(thread.parentThreadId, [...(children.get(thread.parentThreadId) || []), thread]);
    } else {
      roots.push(thread);
    }
  }
  roots.sort((left, right) => left.id.localeCompare(right.id));
  for (const rows of children.values()) rows.sort((left, right) => left.id.localeCompare(right.id));
  const orderedThreads: EvidenceResearchThread[] = [];
  const visited = new Set<string>();
  const appendThread = (thread: EvidenceResearchThread) => {
    if (visited.has(thread.id)) return;
    visited.add(thread.id);
    orderedThreads.push(thread);
    for (const child of children.get(thread.id) || []) appendThread(child);
  };
  for (const root of roots) appendThread(root);
  for (const thread of [...nonEmptyThreads].sort((left, right) => left.id.localeCompare(right.id))) appendThread(thread);
  const assigned = new Set(ownerByNode.keys());
  for (const node of accepted) {
    if (["group", "dataset", "run"].includes(node.data.type)) continue;
    if (!assigned.has(node.id)) unassignedIDs.add(node.id);
  }
  const unassigned = [...unassignedIDs]
    .map((id) => byID.get(id))
    .filter((node): node is EvidenceFlowNode => Boolean(node))
    .filter((node) => !["group", "dataset", "run"].includes(node.data.type))
    .sort((left, right) => researchNodeKey(left).localeCompare(researchNodeKey(right)));
  const crossThreadRelations = semanticEdges.flatMap((edge): EvidenceResearchCrossRelation[] => {
    const sourceThreadId = ownerByNode.get(edge.source);
    const targetThreadId = ownerByNode.get(edge.target);
    if (!sourceThreadId || !targetThreadId || sourceThreadId === targetThreadId) return [];
    const source = byID.get(edge.source);
    const target = byID.get(edge.target);
    return [{
      edge,
      sourceThreadId,
      targetThreadId,
      kind: edge.data?.type === "next_step" && ["issue", "conclusion"].includes(source?.data.type || "") && target && evidenceResearchStage(target) === "hypothesis" ? "branch" : "causal"
    }];
  });
  return { threads: orderedThreads, unassigned, crossThreadRelations, protocolGroups: [] };
}

export function evidenceNeighborhood(
  nodes: EvidenceFlowNode[],
  edges: EvidenceFlowEdge[],
  focusID: string,
  hops: number | "all" = 1
): EvidenceNeighborhood {
  const byID = new Map(nodes.filter((node) => !node.data.draft).map((node) => [node.id, node]));
  const center = byID.get(focusID) || null;
  if (!center) return { center: null, upstream: [], downstream: [], related: [] };
  const depthLimit = hops === "all"
    ? Math.max(1, byID.size - 1)
    : Math.max(1, Math.min(3, Math.floor(hops)));
  const neutralTypes = new Set<EvidenceEdgeType>(["related_to", "custom"]);
  const usableEdges = edges
    .filter((edge) => !edge.data?.draft && byID.has(edge.source) && byID.has(edge.target))
    .sort((left, right) => left.id.localeCompare(right.id));
  const causalEdges = usableEdges.filter((edge) => !neutralTypes.has(edge.data?.type || "related_to"));
  const incoming = new Map<string, EvidenceFlowEdge[]>();
  const outgoing = new Map<string, EvidenceFlowEdge[]>();
  for (const edge of causalEdges) {
    incoming.set(edge.target, [...(incoming.get(edge.target) || []), edge]);
    outgoing.set(edge.source, [...(outgoing.get(edge.source) || []), edge]);
  }
  const walk = (direction: "upstream" | "downstream") => {
    const distance = new Map<string, number>([[focusID, 0]]);
    let frontier = [focusID];
    for (let depth = 1; depth <= depthLimit && frontier.length; depth += 1) {
      const next = new Set<string>();
      for (const nodeID of [...frontier].sort()) {
        const adjacent = direction === "upstream" ? incoming.get(nodeID) || [] : outgoing.get(nodeID) || [];
        for (const edge of adjacent) {
          const otherID = direction === "upstream" ? edge.source : edge.target;
          if (distance.has(otherID)) continue;
          distance.set(otherID, depth);
          next.add(otherID);
        }
      }
      frontier = [...next];
    }
    distance.delete(focusID);
    return distance;
  };
  const upstreamDistance = walk("upstream");
  const downstreamDistance = walk("downstream");
  const side = new Map<string, { direction: "upstream" | "downstream"; depth: number }>();
  const causalNodeIDs = new Set([...upstreamDistance.keys(), ...downstreamDistance.keys()]);
  for (const nodeID of [...causalNodeIDs].sort()) {
    const upstreamDepth = upstreamDistance.get(nodeID);
    const downstreamDepth = downstreamDistance.get(nodeID);
    if (upstreamDepth !== undefined && (downstreamDepth === undefined || upstreamDepth <= downstreamDepth)) {
      side.set(nodeID, { direction: "upstream", depth: upstreamDepth });
    } else if (downstreamDepth !== undefined) {
      side.set(nodeID, { direction: "downstream", depth: downstreamDepth });
    }
  }
  const rank = (nodeID: string) => {
    if (nodeID === focusID) return 0;
    const placement = side.get(nodeID);
    if (!placement) return null;
    return placement.direction === "upstream" ? -placement.depth : placement.depth;
  };
  const visibleCausalIDs = new Set([focusID, ...side.keys()]);
  const visibleCausalEdges = causalEdges.filter((edge) => {
    if (!visibleCausalIDs.has(edge.source) || !visibleCausalIDs.has(edge.target)) return false;
    const sourceRank = rank(edge.source);
    const targetRank = rank(edge.target);
    return sourceRank !== null && targetRank !== null && sourceRank < targetRank;
  });
  const rowsByID = new Map<string, EvidenceNeighbour>();
  for (const [nodeID, placement] of side) {
    rowsByID.set(nodeID, { node: byID.get(nodeID)!, edges: [], depth: placement.depth });
  }
  for (const edge of visibleCausalEdges) {
    const ownerID = edge.source === focusID ? edge.target : edge.source;
    const row = rowsByID.get(ownerID);
    if (row) row.edges.push(edge);
  }
  const relatedByID = new Map<string, EvidenceNeighbour>();
  for (const edge of usableEdges) {
    if (!neutralTypes.has(edge.data?.type || "related_to") || (edge.source !== focusID && edge.target !== focusID)) continue;
    const otherID = edge.source === focusID ? edge.target : edge.source;
    const row = relatedByID.get(otherID) || { node: byID.get(otherID)!, edges: [], depth: 1 };
    row.edges.push(edge);
    relatedByID.set(otherID, row);
  }
  const rows = (direction: "upstream" | "downstream") => [...rowsByID.values()]
    .filter((row) => side.get(row.node.id)?.direction === direction)
    .map((row) => ({ ...row, edges: [...row.edges].sort((a, b) => a.id.localeCompare(b.id)) }))
    .sort((left, right) => left.depth - right.depth || evidenceReadingNodeKey(right.node).localeCompare(evidenceReadingNodeKey(left.node)));
  const related = [...relatedByID.values()]
    .map((row) => ({ ...row, edges: [...row.edges].sort((a, b) => a.id.localeCompare(b.id)) }))
    .sort((left, right) => evidenceReadingNodeKey(right.node).localeCompare(evidenceReadingNodeKey(left.node)));
  return {
    center,
    upstream: rows("upstream"),
    downstream: rows("downstream"),
    related
  };
}

/**
 * Project one node's bounded research context into a small, deterministic
 * causal graph. The projection deliberately ignores saved canvas positions:
 * every upstream depth occupies one column to the left, every downstream depth
 * one column to the right, and direct non-directional context points down from
 * above the focus.
 */
export function layoutEvidenceNeighborhood(neighborhood: EvidenceNeighborhood): EvidenceFocusGraph {
  if (!neighborhood.center) return { nodes: [], edges: [] };

  const nodeWidth = 306;
  const nodeHeight = 138;
  const contextNodeWidth = 252;
  const contextNodeHeight = 82;
  const contextRowStep = 116;
  const rowGap = 58;
  const rowStep = nodeHeight + rowGap;
  const columnGap = 448;
  const maxDepth = Math.max(1, ...neighborhood.upstream.map((row) => row.depth), ...neighborhood.downstream.map((row) => row.depth));
  const centerX = 50 + maxDepth * columnGap;
  const rowsAtDepth = (rows: EvidenceNeighbour[], depth: number) => rows.filter((row) => row.depth === depth);
  const primaryRows = Math.max(
    1,
    ...Array.from({ length: maxDepth }, (_, index) => rowsAtDepth(neighborhood.upstream, index + 1).length),
    ...Array.from({ length: maxDepth }, (_, index) => rowsAtDepth(neighborhood.downstream, index + 1).length)
  );
  const relatedColumns = neighborhood.related.length === 4
    ? 2
    : Math.min(3, Math.max(1, neighborhood.related.length));
  const relatedRows = neighborhood.related.length ? Math.ceil(neighborhood.related.length / relatedColumns) : 0;
  const causalStartY = relatedRows ? relatedRows * contextRowStep + 76 : 0;
  const centerY = causalStartY + ((primaryRows - 1) * rowStep) / 2;
  const positions = new Map<string, { x: number; y: number }>();

  const placeCausalRows = (rows: EvidenceNeighbour[], direction: "upstream" | "downstream") => {
    for (let depth = 1; depth <= maxDepth; depth += 1) {
      const lane = rowsAtDepth(rows, depth);
      const laneStartY = causalStartY + ((primaryRows - lane.length) * rowStep) / 2;
      lane.forEach((row, index) => positions.set(row.node.id, {
        x: centerX + (direction === "upstream" ? -depth : depth) * columnGap,
        y: laneStartY + index * rowStep
      }));
    }
  };
  placeCausalRows(neighborhood.upstream, "upstream");
  positions.set(neighborhood.center.id, { x: centerX, y: centerY });
  placeCausalRows(neighborhood.downstream, "downstream");

  neighborhood.related.forEach((row, index) => {
    const line = Math.floor(index / relatedColumns);
    const lineStart = line * relatedColumns;
    const itemsOnLine = Math.min(relatedColumns, neighborhood.related.length - lineStart);
    const slot = index - lineStart;
    const linePositions = itemsOnLine === 1
      ? [centerX]
      : itemsOnLine === 2
        ? [centerX - 185, centerX + 185]
        : [centerX - columnGap, centerX, centerX + columnGap];
    positions.set(row.node.id, { x: linePositions[slot], y: line * contextRowStep });
  });

  const causalRows = [...neighborhood.upstream, ...neighborhood.downstream];
  const contextNodeIDs = new Set(neighborhood.related.map((row) => row.node.id));
  const sourceNodes = [neighborhood.center, ...causalRows.map((row) => row.node), ...neighborhood.related.map((row) => row.node)];
  const seenNodes = new Set<string>();
  const focusNodes = sourceNodes.flatMap((node) => {
    if (seenNodes.has(node.id)) return [];
    seenNodes.add(node.id);
    const projectedPosition = positions.get(node.id) || { x: centerX, y: centerY };
    const isContextNode = contextNodeIDs.has(node.id);
    const width = isContextNode ? contextNodeWidth : nodeWidth;
    const height = isContextNode ? contextNodeHeight : nodeHeight;
    const position = isContextNode
      ? { x: projectedPosition.x + (nodeWidth - contextNodeWidth) / 2, y: projectedPosition.y }
      : projectedPosition;
    return [{
      ...node,
      className: `${node.className || ""} ${isContextNode ? "evidence-focus-context-node" : ""}`.trim(),
      parentId: undefined,
      extent: undefined,
      expandParent: false,
      position,
      width,
      height,
      measured: { width, height },
      style: { ...node.style, width, height },
      draggable: false,
      connectable: false,
      deletable: false,
      selected: node.id === neighborhood.center!.id,
      data: {
        ...node.data,
        groupId: undefined,
        groupMemberCount: undefined,
        groupInternalEdgeCount: undefined,
        groupExternalEdgeCount: undefined
      }
    } satisfies EvidenceFlowNode];
  });

  const contextEdgeEntries = neighborhood.related
    .flatMap((row) => row.edges.map((edge) => ({ edge, contextNodeID: row.node.id })))
    .sort((left, right) => {
      const leftPosition = positions.get(left.contextNodeID) || { x: 0, y: 0 };
      const rightPosition = positions.get(right.contextNodeID) || { x: 0, y: 0 };
      return leftPosition.x - rightPosition.x || leftPosition.y - rightPosition.y || left.edge.id.localeCompare(right.edge.id);
    });
  const contextLandingOffsets = contextEdgeEntries.map((_, index) => {
    const midpoint = (contextEdgeEntries.length - 1) / 2;
    return Math.max(-88, Math.min(88, (index - midpoint) * 48));
  });
  const edgeEntries = [
    ...causalRows.flatMap((row) => row.edges.map((edge) => ({ edge, contextNodeID: "", contextIndex: -1 }))),
    ...contextEdgeEntries.map((entry, contextIndex) => ({ ...entry, contextIndex }))
  ];
  const seenEdges = new Set<string>();
  const focusEdges = edgeEntries.flatMap(({ edge, contextNodeID, contextIndex }) => {
    if (seenEdges.has(edge.id)) return [];
    seenEdges.add(edge.id);
    const projectedSourceID = contextNodeID || edge.source;
    const projectedTargetID = contextNodeID ? neighborhood.center!.id : edge.target;
    const source = positions.get(projectedSourceID);
    const target = positions.get(projectedTargetID);
    if (!source || !target) return [];
    const handles = contextNodeID
      ? { source: Position.Bottom, target: Position.Top }
      : evidenceAutoHandlePair(source, target);
    const type = edge.data?.type || "related_to";
    const contextTargetOffsetX = contextNodeID ? contextLandingOffsets[contextIndex] : 0;
    return [{
      ...edge,
      type: "evidence" as const,
      source: projectedSourceID,
      target: projectedTargetID,
      sourceHandle: `source-${handles.source}`,
      targetHandle: `target-${handles.target}`,
      markerEnd: evidenceMarkerEnd(type),
      style: edgeStyle(type),
      selected: false,
      animated: false,
      data: {
        ...edge.data,
        type,
        rationale: edge.data?.rationale || "",
        autoHandles: false,
        routeLane: 0,
        routePoints: undefined,
        routeLabelPoint: undefined,
        routeSafe: false,
        focusVisible: true,
        focusContext: Boolean(contextNodeID),
        focusTargetOffsetX: contextTargetOffsetX
      }
    } satisfies EvidenceFlowEdge];
  });

  return { nodes: focusNodes, edges: focusEdges };
}

export function defaultNodeTitle(type: EvidenceNodeType) {
  return nodeTypeLabel(type);
}

export function defaultEvidenceRelation(source: EvidenceNodeType, target: EvidenceNodeType): EvidenceEdgeType {
  if (source === "group" || target === "group") return "related_to";
  if ((source === "dataset" || source === "protocol") && target === "run") return "uses";
  if (source === "run" && target === "claim") return "supports";
  if ((source === "run" || source === "claim") && target === "issue") return "reveals_issue";
  if ((source === "claim" || source === "issue") && target === "plan") return "next_step";
  if (source === target && ["dataset", "protocol", "claim", "plan"].includes(source)) return "supersedes";
  if ((source === "run" || source === "experiment") && ["hypothesis", "conclusion"].includes(target)) return "supports";
  return "related_to";
}

export function candidateMatches(candidate: EvidenceChainRunCandidate, query: string) {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [
    candidate.id,
    candidate.kind,
    candidate.run_id,
    candidate.project_card_id,
    candidate.project_id,
    candidate.project_name,
    candidate.question,
    candidate.verdict,
    candidate.evidence_level,
    candidate.key_metrics,
    candidate.next_action,
    candidate.run?.id,
    candidate.run?.name,
    candidate.run?.kind,
    candidate.run?.status,
    candidate.run?.command
  ].some((part) => text(part).toLowerCase().includes(q));
}

export function filterRunCandidatesForProject(candidates: EvidenceChainRunCandidate[], projectId: string) {
  const scope = projectId.trim();
  if (!scope) return candidates;
  return candidates.filter((candidate) => candidate.project_id?.trim() === scope);
}

export interface EvidenceCandidateGroup {
  key: string;
  title: string;
  subtitle: string;
  candidates: EvidenceChainRunCandidate[];
}

export function groupRunCandidatesByProject(candidates: EvidenceChainRunCandidate[], labels = { unassignedRuns: "Unassigned runs", runsWithoutProjectCards: "Runs without project cards" }) {
  const groups = new Map<string, EvidenceCandidateGroup>();
  for (const candidate of candidates) {
    const projectID = candidate.project_id?.trim();
    const key = projectID ? `project:${projectID}` : "unassigned";
    const title = projectID ? candidate.project_name || projectID : labels.unassignedRuns;
    const subtitle = projectID || labels.runsWithoutProjectCards;
    const existing = groups.get(key);
    if (existing) {
      existing.candidates.push(candidate);
    } else {
      groups.set(key, { key, title, subtitle, candidates: [candidate] });
    }
  }
  return Array.from(groups.values()).sort((a, b) => {
    if (a.key === "unassigned") return 1;
    if (b.key === "unassigned") return -1;
    return a.title.localeCompare(b.title);
  });
}

export function candidateRunNodeFields(candidate: EvidenceChainRunCandidate) {
  const runDisplayTitle = candidate.run ? runTitle(candidate.run) : candidate.run_id;
  const body = [candidate.verdict, candidate.question, candidate.next_action, candidate.key_metrics].filter((part) => text(part).trim()).join("\n\n");
  return { runDisplayTitle, body };
}

export function candidateToNode(candidate: EvidenceChainRunCandidate, position: { x: number; y: number }): EvidenceFlowNode {
  const { runDisplayTitle, body } = candidateRunNodeFields(candidate);
  return {
    id: `node_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 7)}`,
    type: "evidence",
    position,
    width: 286,
    height: 220,
    data: {
      type: "run",
      title: runDisplayTitle,
      body,
      runTitle: runDisplayTitle,
      runId: candidate.run_id,
      projectCardId: candidate.project_card_id || "",
      status: candidate.run?.status || "",
      runKind: candidate.run?.kind || "",
      keyMetrics: candidate.key_metrics || "",
      evidenceLevel: candidate.evidence_level || "",
      gitBranch: candidate.run?.git_branch || "",
      gitCommit: candidate.run?.git_commit || "",
      gitDirty: candidate.run?.git_dirty === true,
      gitDiffHash: candidate.run?.git_diff_hash || "",
      gitDiffPath: candidate.run?.git_diff_path || "",
      pinned: false,
      occurredAt: candidate.run?.created_at || ""
    }
  };
}

export function createTextNode(type: EvidenceNodeType, position: { x: number; y: number }): EvidenceFlowNode {
  return {
    id: `node_${type}_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 7)}`,
    type: "evidence",
    position,
    width: type === "group" ? 720 : 286,
    height: type === "group" ? 420 : 184,
    data: {
      type,
      title: defaultNodeTitle(type),
      body: "",
      ...(type === "group"
        ? { groupKind: "protocol" as const, version: "v1", provenanceSummary: "" }
        : {})
    }
  };
}

export function apiNodeToFlowNode(node: EvidenceChainNode): EvidenceFlowNode {
  let data: Partial<EvidenceNodeData> = {};
  try {
    data = node.data_json ? JSON.parse(node.data_json) : {};
  } catch {
    data = {};
  }
  return {
    id: node.id,
    type: "evidence",
    position: { x: node.x || 0, y: node.y || 0 },
    width: node.width || 286,
    height: node.height || 184,
    data: {
      ...data,
      type: node.type,
      title: node.title || defaultNodeTitle(node.type),
      body: node.body || "",
      runId: node.run_id || undefined,
      sourceRunIds: node.source_run_ids || (Array.isArray(data.sourceRunIds) ? data.sourceRunIds as string[] : undefined),
      sourceSnapshotIds: node.source_snapshot_ids || (Array.isArray(data.sourceSnapshotIds) ? data.sourceSnapshotIds as string[] : undefined),
      projectCardId: node.project_card_id || undefined,
      pinned: node.pinned === true,
      occurredAt: node.occurred_at || undefined
    }
  };
}

export function apiEdgeToFlowEdge(edge: EvidenceChainEdge): EvidenceFlowEdge {
  let data: Partial<EvidenceEdgeData> = {};
  try {
    data = edge.data_json ? JSON.parse(edge.data_json) : {};
  } catch {
    data = {};
  }
  const type = edge.type || "next_step";
  return {
    id: edge.id,
    source: edge.source_node_id,
    target: edge.target_node_id,
    sourceHandle: typeof data.sourceHandle === "string" ? data.sourceHandle : undefined,
    targetHandle: typeof data.targetHandle === "string" ? data.targetHandle : undefined,
    label: edge.label ?? edgeTypeLabel(type),
    data: { type, rationale: edge.rationale || "", ...data },
    animated: false,
    markerEnd: evidenceMarkerEnd(type),
    style: edgeStyle(type)
  };
}

export function evidenceProposalPreview(candidate: EvidenceChainRunCandidate): EvidenceProposalPreview | null {
  if (candidate.project_card?.graph_status !== "pending") return null;
  let patch: EvidenceGraphPatch;
  try {
    patch = JSON.parse(candidate.project_card.graph_patch_json || "") as EvidenceGraphPatch;
  } catch {
    return null;
  }
  if (!patch || typeof patch !== "object" || typeof patch.chain_id !== "string" || !Array.isArray(patch.nodes) || !Array.isArray(patch.edges)) {
    return null;
  }
  const nodeIDs = new Map<string, string>();
  for (const node of patch.nodes) {
    if (!node || typeof node.id !== "string" || !node.id) return null;
    nodeIDs.set(node.id, `draft:${candidate.run_id}:${node.id}`);
  }
  const nodes = patch.nodes.map((node) => {
    const mapped = apiNodeToFlowNode({ ...node, id: nodeIDs.get(node.id)! });
    return {
      ...mapped,
      draggable: false,
      connectable: false,
      deletable: false,
      selectable: true,
      data: {
        ...mapped.data,
        groupId: mapped.data.groupId ? nodeIDs.get(mapped.data.groupId) || mapped.data.groupId : undefined,
        readOnly: true,
        draft: true,
        proposalRunId: candidate.run_id,
        sourceNodeId: node.id
      }
    };
  });
  const edges = patch.edges.map((edge) => {
    const mapped = apiEdgeToFlowEdge({
      ...edge,
      id: `draft-edge:${candidate.run_id}:${edge.id}`,
      source_node_id: nodeIDs.get(edge.source_node_id) || edge.source_node_id,
      target_node_id: nodeIDs.get(edge.target_node_id) || edge.target_node_id
    });
    return {
      ...mapped,
      selectable: false,
      deletable: false,
      data: {
        ...mapped.data,
        type: mapped.data?.type || edge.type || "next_step",
        rationale: mapped.data?.rationale || edge.rationale || "",
        readOnly: true,
        draft: true,
        proposalRunId: candidate.run_id,
        sourceEdgeId: edge.id
      }
    };
  });
  return {
    runId: candidate.run_id,
    chainId: patch.chain_id,
    routingReason: patch.routing_reason || candidate.project_card.graph_routing_reason || "",
    layoutIntent: normalizeEvidenceLayoutIntent(patch.layout_intent),
    nodes,
    edges
  };
}

export function serializeEvidenceGraph(nodes: EvidenceFlowNode[], edges: EvidenceFlowEdge[]) {
  const draftNodeIDs = new Set(nodes.filter((node) => node.data.draft === true).map((node) => node.id));
  return {
    nodes: nodes.filter((node) => node.data.draft !== true).map((node): EvidenceChainNode => {
      const {
        type: _type,
        title: _title,
        body: _body,
        runId: _runId,
        sourceRunIds: _sourceRunIds,
        sourceSnapshotIds: _sourceSnapshotIds,
        projectCardId: _projectCardId,
        pinned: _pinned,
        occurredAt: _occurredAt,
        readOnly: _readOnly,
        draft: _draft,
        proposalRunId: _proposalRunId,
        sourceNodeId: _sourceNodeId,
        mapRefStatus: _mapRefStatus,
        onOpenRun: _onOpenRun,
        onOpenMap: _onOpenMap,
        onUpdateNode: _onUpdateNode,
        onResizeNode: _onResizeNode,
        labels: _labels,
        ...dataJSON
      } = node.data;
      return {
        id: node.id,
        type: node.data.type,
        title: node.data.title,
        body: node.data.body,
        run_id: node.data.runId || "",
        source_run_ids: node.data.sourceRunIds,
        source_snapshot_ids: node.data.sourceSnapshotIds,
        project_card_id: node.data.projectCardId || "",
        x: node.position.x,
        y: node.position.y,
        width: typeof node.width === "number" ? node.width : typeof node.measured?.width === "number" ? node.measured.width : 286,
        height: typeof node.height === "number" ? node.height : typeof node.measured?.height === "number" ? node.measured.height : 184,
        pinned: node.data.pinned === true,
        occurred_at: node.data.occurredAt || undefined,
        data_json: JSON.stringify(dataJSON)
      };
    }),
    edges: edges
      .filter((edge) => edge.data?.draft !== true && !draftNodeIDs.has(edge.source) && !draftNodeIDs.has(edge.target))
      .map((edge): EvidenceChainEdge => {
      const type = edge.data?.type || "next_step";
      return {
        id: edge.id,
        source_node_id: edge.source,
        target_node_id: edge.target,
        type,
        label: edge.label == null ? edgeTypeLabel(type) : String(edge.label),
        rationale: edge.data?.rationale || "",
        data_json: JSON.stringify({
          sourceHandle: edge.sourceHandle || "",
          targetHandle: edge.targetHandle || "",
          autoHandles: edge.data?.autoHandles === true
        })
      };
    })
  };
}

export function buildEvidenceEditPatch(
  chainId: string,
  original: { nodes: EvidenceChainNode[]; edges: EvidenceChainEdge[] },
  current: { nodes: EvidenceChainNode[]; edges: EvidenceChainEdge[] }
): EvidenceGraphPatch | null {
  const originalNodes = new Map(original.nodes.map((node) => [node.id, node]));
  const currentNodes = new Map(current.nodes.map((node) => [node.id, node]));
  const originalEdges = new Map(original.edges.map((edge) => [edge.id, edge]));
  const currentEdges = new Map(current.edges.map((edge) => [edge.id, edge]));
  const nodes: EvidenceChainNode[] = [];
  const upsertNodes: EvidenceChainNode[] = [];
  const edges: EvidenceChainEdge[] = [];
  const upsertEdges: EvidenceChainEdge[] = [];
  for (const node of current.nodes) {
    const previous = originalNodes.get(node.id);
    if (!previous) nodes.push(node);
    else if (semanticEvidenceNode(previous) !== semanticEvidenceNode(node)) upsertNodes.push(node);
  }
  for (const edge of current.edges) {
    const previous = originalEdges.get(edge.id);
    if (!previous) edges.push(edge);
    else if (semanticEvidenceEdge(previous) !== semanticEvidenceEdge(edge)) upsertEdges.push(edge);
  }
  const deleteNodeIds = original.nodes.filter((node) => !currentNodes.has(node.id)).map((node) => node.id).sort();
  const deleteEdgeIds = original.edges.filter((edge) => !currentEdges.has(edge.id)).map((edge) => edge.id).sort();
  if (!nodes.length && !upsertNodes.length && !edges.length && !upsertEdges.length && !deleteNodeIds.length && !deleteEdgeIds.length) return null;
  return {
    chain_id: chainId,
    nodes,
    edges,
    upsert_nodes: upsertNodes,
    upsert_edges: upsertEdges,
    delete_node_ids: deleteNodeIds,
    delete_edge_ids: deleteEdgeIds
  };
}

function semanticEvidenceNode(node: EvidenceChainNode) {
  return JSON.stringify({
    id: node.id, type: node.type, title: node.title || "", body: node.body || "",
    run_id: node.run_id || "", project_card_id: node.project_card_id || "",
    occurred_at: node.occurred_at || "", data: semanticEvidenceData(node.data_json)
  });
}

function semanticEvidenceEdge(edge: EvidenceChainEdge) {
  return JSON.stringify({
    id: edge.id, source_node_id: edge.source_node_id, target_node_id: edge.target_node_id,
    type: edge.type, label: edge.label || "", rationale: edge.rationale || "",
    data: semanticEvidenceData(edge.data_json)
  });
}

function semanticEvidenceData(raw?: string) {
  try {
    const value = JSON.parse(raw || "{}") as Record<string, unknown>;
    if (value && typeof value === "object" && !Array.isArray(value)) {
      for (const key of ["pinned", "position", "width", "height", "layout", "sourceHandle", "targetHandle", "autoHandles", "collapsed"]) delete value[key];
    }
    return value;
  } catch {
    return raw || "";
  }
}

export function evidenceMarkerEnd(type: EvidenceEdgeType) {
  return {
    type: MarkerType.ArrowClosed,
    width: 22,
    height: 22,
    color: String(edgeStyle(type).stroke)
  };
}

export function edgeStyle(type: EvidenceEdgeType): CSSProperties {
  switch (type) {
    case "uses":
      return { stroke: "#3f6f91", strokeWidth: 2.4, strokeOpacity: 0.92, strokeLinecap: "round", strokeLinejoin: "round" };
    case "supports":
      return { stroke: "#23805a", strokeWidth: 2.6, strokeOpacity: 0.94, strokeLinecap: "round", strokeLinejoin: "round" };
    case "weakens":
    case "does_not_prove":
      return { stroke: "#b94a48", strokeWidth: 2.6, strokeOpacity: 0.94, strokeDasharray: "7 5", strokeLinecap: "round", strokeLinejoin: "round" };
    case "reveals_issue":
      return { stroke: "#c46a2f", strokeWidth: 2.6, strokeOpacity: 0.94, strokeLinecap: "round", strokeLinejoin: "round" };
    case "supersedes":
      return { stroke: "#7255aa", strokeWidth: 2.8, strokeOpacity: 0.94, strokeLinecap: "round", strokeLinejoin: "round" };
    case "related_to":
      return { stroke: "#6d5f91", strokeWidth: 2.3, strokeOpacity: 0.88, strokeDasharray: "3 5", strokeLinecap: "round", strokeLinejoin: "round" };
    case "custom":
      return { stroke: "#66717e", strokeWidth: 2.2, strokeOpacity: 0.82, strokeDasharray: "3 5", strokeLinecap: "round", strokeLinejoin: "round" };
    case "next_step":
    default:
      return { stroke: "#466fa3", strokeWidth: 2.6, strokeOpacity: 0.94, strokeLinecap: "round", strokeLinejoin: "round" };
  }
}

export interface EvidenceGroupDescriptor {
  id: string;
  group: EvidenceFlowNode;
  memberIds: string[];
}

export interface ProtocolFrameMigration {
  eligible: boolean;
  protocolId: string;
  memberIds: string[];
  removableEdgeIds: string[];
  blockers: string[];
}

export function inspectProtocolFrameMigration(
  nodes: EvidenceFlowNode[],
  edges: EvidenceFlowEdge[],
  protocolId: string
): ProtocolFrameMigration {
  const protocol = nodes.find((node) => node.id === protocolId);
  const blockers: string[] = [];
  if (!protocol) blockers.push("找不到协议节点");
  else if (protocol.data.type !== "protocol") blockers.push("只有实验协议节点可以转换为协议容器");
  else if (protocol.data.groupId) blockers.push("该协议已经属于另一个范围");

  const incident = edges
    .filter((edge) => edge.source === protocolId || edge.target === protocolId)
    .sort((left, right) => left.id.localeCompare(right.id));
  if (protocol && !incident.length) blockers.push("协议没有可转换的归属关系");

  const memberIds = [...new Set(incident.map((edge) => edge.source === protocolId ? edge.target : edge.source))].sort();
  const nodeById = new Map(nodes.map((node) => [node.id, node]));
  for (const edge of incident) {
    if ((edge.data?.type || "next_step") !== "related_to") {
      blockers.push(`关系「${text(edge.label) || edge.id}」具有研究语义，不能自动改成范围归属`);
    }
  }
  for (const memberId of memberIds) {
    const member = nodeById.get(memberId);
    if (!member) blockers.push(`关系指向不存在的节点 ${memberId}`);
    else if (member.data.type === "group") blockers.push(`范围 ${member.data.title || memberId} 不能嵌套`);
    else if (!isProtocolGroupMemberType(member.data.type)) blockers.push(`${member.data.title || memberId} 不是协议容器允许的节点类型`);
    else if (member.data.groupId && member.data.groupId !== protocolId) {
      blockers.push(`${member.data.title || memberId} 已属于另一个协议容器`);
    }
  }

  return {
    eligible: blockers.length === 0,
    protocolId,
    memberIds,
    removableEdgeIds: incident.map((edge) => edge.id),
    blockers: [...new Set(blockers)]
  };
}

export function convertProtocolToFrame(
  nodes: EvidenceFlowNode[],
  edges: EvidenceFlowEdge[],
  protocolId: string
): { nodes: EvidenceFlowNode[]; edges: EvidenceFlowEdge[]; migration: ProtocolFrameMigration } {
  const migration = inspectProtocolFrameMigration(nodes, edges, protocolId);
  if (!migration.eligible) return { nodes, edges, migration };
  const members = new Set(migration.memberIds);
  const removedEdges = new Set(migration.removableEdgeIds);
  const protocol = nodes.find((node) => node.id === protocolId)!;
  const frame = evidenceGroupFrameBounds(
    { id: protocolId, group: protocol, memberIds: migration.memberIds },
    nodes
  );
  return {
    nodes: nodes.map((node) => {
      if (node.id === protocolId) {
        return {
          ...node,
          position: { x: frame.x, y: frame.y },
          width: frame.width,
          height: frame.height,
          connectable: false,
          data: {
            ...node.data,
            type: "group",
            groupKind: "protocol",
            version: node.data.version || "v1",
            groupId: undefined,
            collapsed: undefined
          }
        };
      }
      if (!members.has(node.id)) return node;
      return { ...node, data: { ...node.data, groupId: protocolId } };
    }),
    edges: edges.filter((edge) => !removedEdges.has(edge.id)),
    migration
  };
}

export interface EvidenceGroupFrameBounds {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface EvidenceGroupProjection {
  nodes: EvidenceFlowNode[];
  edges: EvidenceFlowEdge[];
  groups: EvidenceGroupDescriptor[];
  internalEdgeCounts: Record<string, number>;
}

export function deriveEvidenceGroups(nodes: EvidenceFlowNode[]): EvidenceGroupDescriptor[] {
  const groups = nodes
    .filter((node) => node.data.type === "group")
    .sort((left, right) => left.id.localeCompare(right.id));
  const groupIDs = new Set(groups.map((group) => group.id));
  const members = new Map<string, string[]>();
  for (const node of nodes) {
    if (node.data.type === "group") continue;
    const groupID = typeof node.data.groupId === "string" ? node.data.groupId.trim() : "";
    if (!groupID || !groupIDs.has(groupID)) continue;
    members.set(groupID, [...(members.get(groupID) || []), node.id]);
  }
  return groups.map((group) => ({
    id: group.id,
    group,
    memberIds: [...(members.get(group.id) || [])].sort()
  }));
}

export function evidenceGroupFrameBounds(
  descriptor: EvidenceGroupDescriptor,
  nodes: EvidenceFlowNode[]
): EvidenceGroupFrameBounds {
  const nodeByID = new Map(nodes.map((node) => [node.id, node]));
  const members = descriptor.memberIds
    .map((id) => nodeByID.get(id))
    .filter((node): node is EvidenceFlowNode => Boolean(node));
  const groupLeft = descriptor.group.position.x;
  const groupTop = descriptor.group.position.y;
  const groupWidth = Math.max(420, evidenceNodeWidth(descriptor.group));
  const groupHeight = Math.max(210, evidenceNodeHeight(descriptor.group));
  if (!members.length) {
    return {
      x: groupLeft,
      y: groupTop,
      width: groupWidth,
      height: groupHeight
    };
  }
  const left = Math.min(groupLeft, ...members.map((node) => node.position.x - 24));
  const top = Math.min(groupTop, ...members.map((node) => node.position.y - 68));
  const right = Math.max(
    groupLeft + groupWidth,
    ...members.map((node) => node.position.x + evidenceNodeWidth(node) + 24)
  );
  const bottom = Math.max(
    groupTop + groupHeight,
    ...members.map((node) => node.position.y + evidenceNodeHeight(node) + 28)
  );
  return {
    x: left,
    y: top,
    width: right - left,
    height: bottom - top
  };
}

export function projectEvidenceGroups(nodes: EvidenceFlowNode[], edges: EvidenceFlowEdge[]): EvidenceGroupProjection {
  const groups = deriveEvidenceGroups(nodes);
  if (!groups.length) {
    return {
      nodes,
      edges,
      groups,
      internalEdgeCounts: {}
    };
  }
  const ownerByMember = new Map<string, string>();
  for (const group of groups) {
    for (const memberID of group.memberIds) ownerByMember.set(memberID, group.id);
  }
  const internalEdgeCounts: Record<string, number> = {};
  for (const group of groups) internalEdgeCounts[group.id] = 0;
  for (const edge of edges) {
    const sourceGroupID = ownerByMember.get(edge.source);
    if (sourceGroupID && sourceGroupID === ownerByMember.get(edge.target)) {
      internalEdgeCounts[sourceGroupID] = (internalEdgeCounts[sourceGroupID] || 0) + 1;
    }
  }
  return {
    nodes: nodes.filter((node) => node.data.type !== "group"),
    edges,
    groups,
    internalEdgeCounts
  };
}

export function translateProtocolContainer(
  nodes: EvidenceFlowNode[],
  groupId: string,
  delta: { x: number; y: number }
): EvidenceFlowNode[] {
  return nodes.map((node) => {
    if (node.id !== groupId && node.data.groupId !== groupId) return node;
    return {
      ...node,
      position: {
        x: node.position.x + delta.x,
        y: node.position.y + delta.y
      },
      data: { ...node.data, pinned: true }
    };
  });
}

export function protocolContainerMoveDeltaForKey(
  key: string,
  shiftKey = false
): { x: number; y: number } | null {
  const step = shiftKey ? 40 : 10;
  switch (key) {
    case "ArrowLeft":
      return { x: -step, y: 0 };
    case "ArrowRight":
      return { x: step, y: 0 };
    case "ArrowUp":
      return { x: 0, y: -step };
    case "ArrowDown":
      return { x: 0, y: step };
    default:
      return null;
  }
}

function evidenceNodeWidth(node: EvidenceFlowNode) {
  return typeof node.measured?.width === "number" ? node.measured.width : typeof node.width === "number" ? node.width : 306;
}

function evidenceNodeHeight(node: EvidenceFlowNode) {
  return typeof node.measured?.height === "number" ? node.measured.height : typeof node.height === "number" ? node.height : 138;
}

const evidenceColumnGap = 184;
const evidenceRowGap = 56;
const evidenceSubcolumnGap = 78;
const evidenceOrigin = { x: 80, y: 72 };

function packEvidenceGrid(nodes: EvidenceFlowNode[], maxColumns = 3) {
  const columnCount = Math.max(1, Math.min(maxColumns, nodes.length));
  const rowCount = Math.ceil(nodes.length / columnCount);
  const columnWidths = Array.from({ length: columnCount }, () => 0);
  const rowHeights = Array.from({ length: rowCount }, () => 0);
  nodes.forEach((node, index) => {
    const column = index % columnCount;
    const row = Math.floor(index / columnCount);
    columnWidths[column] = Math.max(columnWidths[column], evidenceNodeWidth(node));
    rowHeights[row] = Math.max(rowHeights[row], evidenceNodeHeight(node));
  });
  const columnOffsets: number[] = [];
  const rowOffsets: number[] = [];
  columnWidths.reduce((offset, width, index) => {
    columnOffsets[index] = offset;
    return offset + width + 52;
  }, 0);
  rowHeights.reduce((offset, height, index) => {
    rowOffsets[index] = offset;
    return offset + height + 48;
  }, 0);
  return {
    width: columnWidths.reduce((sum, width) => sum + width, 0) + Math.max(0, columnCount - 1) * 52,
    height: rowHeights.reduce((sum, height) => sum + height, 0) + Math.max(0, rowCount - 1) * 48,
    positions: new Map(nodes.map((node, index) => [
      node.id,
      {
        x: columnOffsets[index % columnCount],
        y: rowOffsets[Math.floor(index / columnCount)]
      }
    ]))
  };
}

export type EvidenceHandleSide = Position.Top | Position.Right | Position.Bottom | Position.Left;

export function evidenceAutoHandlePair(
  source: { x: number; y: number },
  target: { x: number; y: number }
): { source: EvidenceHandleSide; target: EvidenceHandleSide } {
  const dx = target.x - source.x;
  const dy = target.y - source.y;
  if (Math.abs(dx) >= 120 || Math.abs(dx) >= Math.abs(dy)) {
    if (dx < 0) return { source: Position.Left, target: Position.Right };
    return { source: Position.Right, target: Position.Left };
  }
  if (dy < 0) return { source: Position.Top, target: Position.Bottom };
  return { source: Position.Bottom, target: Position.Top };
}

export function layoutEvidenceGraphFromIntent(
  nodes: EvidenceFlowNode[],
  intent: EvidenceLayoutIntent
): EvidenceFlowNode[] {
  const normalized = normalizeEvidenceLayoutIntent(intent);
  if (!normalized) return nodes;
  const nodeByID = new Map(nodes.map((node) => [node.id, node]));
  const groups = deriveEvidenceGroups(nodes);
  const groupByID = new Map(groups.map((group) => [group.id, group]));
  const frameByID = new Map(groups.map((group) => [group.id, evidenceGroupFrameBounds(group, nodes)]));
  const rankedNodes = normalized.ranks.map((rank) => rank
    .map((id) => nodeByID.get(id))
    .filter((node): node is EvidenceFlowNode => Boolean(node))
    .filter((node) => !node.data.groupId));
  if (!rankedNodes.some((rank) => rank.length)) return nodes;

  const nodeSize = (node: EvidenceFlowNode) => {
    const frame = frameByID.get(node.id);
    return frame
      ? { width: frame.width, height: frame.height }
      : { width: evidenceNodeWidth(node), height: evidenceNodeHeight(node) };
  };
  const rankWidths = rankedNodes.map((rank) => Math.max(0, ...rank.map((node) => nodeSize(node).width)));
  const rankHeights = rankedNodes.map((rank) => (
    rank.reduce((sum, node) => sum + nodeSize(node).height, 0)
    + Math.max(0, rank.length - 1) * evidenceRowGap
  ));
  const largestRankHeight = Math.max(0, ...rankHeights);
  const desired = new Map<string, { x: number; y: number }>();
  let x = evidenceOrigin.x;
  rankedNodes.forEach((rank, rankIndex) => {
    let y = evidenceOrigin.y + (largestRankHeight - rankHeights[rankIndex]) / 2;
    for (const node of rank) {
      desired.set(node.id, { x, y });
      y += nodeSize(node).height + evidenceRowGap;
    }
    x += rankWidths[rankIndex] + evidenceColumnGap;
  });

  let arranged = nodes.map((node) => ({
    ...node,
    position: { ...node.position },
    data: { ...node.data }
  }));
  for (const rank of rankedNodes) {
    for (const node of rank) {
      const target = desired.get(node.id);
      if (!target) continue;
      const frame = frameByID.get(node.id);
      const origin = frame || node.position;
      const delta = { x: target.x - origin.x, y: target.y - origin.y };
      if (groupByID.has(node.id)) {
        arranged = arranged.map((item) => {
          if (item.id !== node.id && item.data.groupId !== node.id) return item;
          return {
            ...item,
            position: { x: item.position.x + delta.x, y: item.position.y + delta.y }
          };
        });
      } else {
        arranged = arranged.map((item) => item.id === node.id
          ? { ...item, position: target }
          : item);
      }
    }
  }
  return arranged;
}

// layoutEvidenceGraph is deliberately pure and deterministic. Accepted
// semantics determine rank; coordinates remain a UI projection. Ordering
// inside a rank follows adjacent nodes instead of node type lanes so the
// common evidence DAG stays left-to-right with fewer avoidable crossings.
function layoutFlatEvidenceGraph(nodes: EvidenceFlowNode[], edges: EvidenceFlowEdge[], resetPins = false): EvidenceFlowNode[] {
  const byID = new Map(nodes.map((node) => [node.id, node]));
  const indegree = new Map(nodes.map((node) => [node.id, 0]));
  const outgoing = new Map<string, string[]>();
  const incoming = new Map<string, string[]>();
  const stableNodeKey = (node: EvidenceFlowNode) => `${node.data.occurredAt || ""}\u0000${node.id}`;
  const layoutRelationPriority = (type: EvidenceEdgeType) => type === "custom" ? 2 : type === "related_to" ? 1 : 0;
  const orderedEdges = [...edges].sort((left, right) => {
    const leftType = left.data?.type || "next_step";
    const rightType = right.data?.type || "next_step";
    return layoutRelationPriority(leftType) - layoutRelationPriority(rightType)
      || stableNodeKey(byID.get(left.source) || ({ id: left.source, data: {} } as EvidenceFlowNode))
        .localeCompare(stableNodeKey(byID.get(right.source) || ({ id: right.source, data: {} } as EvidenceFlowNode)))
      || stableNodeKey(byID.get(left.target) || ({ id: left.target, data: {} } as EvidenceFlowNode))
        .localeCompare(stableNodeKey(byID.get(right.target) || ({ id: right.target, data: {} } as EvidenceFlowNode)))
      || left.id.localeCompare(right.id);
  });
  const reaches = (start: string, target: string) => {
    const pending = [start];
    const seen = new Set<string>();
    while (pending.length) {
      const current = pending.pop()!;
      if (current === target) return true;
      if (seen.has(current)) continue;
      seen.add(current);
      pending.push(...(outgoing.get(current) || []));
    }
    return false;
  };
  for (const edge of orderedEdges) {
    if (!byID.has(edge.source) || !byID.has(edge.target) || edge.source === edge.target) continue;
    if ((outgoing.get(edge.source) || []).includes(edge.target)) continue;
    // A research map can contain feedback/reference edges even though the
    // layout projection must be acyclic. Keep the strongest deterministic
    // edge set for ranking; every edge is still rendered.
    if (reaches(edge.target, edge.source)) continue;
    outgoing.set(edge.source, [...(outgoing.get(edge.source) || []), edge.target]);
    incoming.set(edge.target, [...(incoming.get(edge.target) || []), edge.source]);
    indegree.set(edge.target, (indegree.get(edge.target) || 0) + 1);
  }
  const queue = nodes.filter((node) => (indegree.get(node.id) || 0) === 0).sort((a, b) => stableNodeKey(a).localeCompare(stableNodeKey(b)));
  const rank = new Map(nodes.map((node) => [node.id, 0]));
  let visited = 0;
  while (queue.length) {
    const node = queue.shift()!;
    visited += 1;
    const targets = [...(outgoing.get(node.id) || [])].sort();
    for (const target of targets) {
      rank.set(target, Math.max(rank.get(target) || 0, (rank.get(node.id) || 0) + 1));
      const next = (indegree.get(target) || 0) - 1;
      indegree.set(target, next);
      if (next === 0) {
        queue.push(byID.get(target)!);
        queue.sort((a, b) => stableNodeKey(a).localeCompare(stableNodeKey(b)));
      }
    }
  }

  // The cycle projection above should make this unreachable. Retain a
  // defensive fallback so malformed external data still receives positions.
  if (visited !== nodes.length) {
    const remaining = nodes
      .filter((node) => (indegree.get(node.id) || 0) > 0)
      .sort((a, b) => stableNodeKey(a).localeCompare(stableNodeKey(b)));
    const fallbackRank = Math.max(0, ...rank.values()) + (visited > 0 ? 1 : 0);
    for (const node of remaining) rank.set(node.id, fallbackRank);
  }

  const layers = new Map<number, EvidenceFlowNode[]>();
  for (const node of nodes) {
    const nodeRank = rank.get(node.id) || 0;
    layers.set(nodeRank, [...(layers.get(nodeRank) || []), node]);
  }
  const layerRanks = [...layers.keys()].sort((a, b) => a - b);
  for (const layer of layers.values()) layer.sort((a, b) => stableNodeKey(a).localeCompare(stableNodeKey(b)));

  const neighbourOrder = () => {
    const order = new Map<string, number>();
    for (const layerRank of layerRanks) {
      (layers.get(layerRank) || []).forEach((node, index) => order.set(node.id, index));
    }
    return order;
  };
  const reorderLayer = (layerRank: number, neighbours: Map<string, string[]>) => {
    const layer = layers.get(layerRank);
    if (!layer || layer.length < 2) return;
    const order = neighbourOrder();
    const originalOrder = new Map(layer.map((node, index) => [node.id, index]));
    const barycenter = (node: EvidenceFlowNode) => {
      const values = (neighbours.get(node.id) || [])
        .map((id) => order.get(id))
        .filter((value): value is number => value !== undefined);
      return values.length ? values.reduce((sum, value) => sum + value, 0) / values.length : undefined;
    };
    layer.sort((left, right) => {
      const leftCenter = barycenter(left);
      const rightCenter = barycenter(right);
      if (leftCenter !== undefined && rightCenter !== undefined && leftCenter !== rightCenter) return leftCenter - rightCenter;
      if (leftCenter !== undefined && rightCenter === undefined) return -1;
      if (leftCenter === undefined && rightCenter !== undefined) return 1;
      const previous = (originalOrder.get(left.id) || 0) - (originalOrder.get(right.id) || 0);
      return previous || stableNodeKey(left).localeCompare(stableNodeKey(right));
    });
  };

  // Alternating barycentric sweeps are a lightweight Sugiyama ordering pass.
  // Four passes are enough for the small project evidence maps while keeping
  // the implementation synchronous and dependency-free.
  for (let pass = 0; pass < 4; pass += 1) {
    for (const layerRank of layerRanks.slice(1)) reorderLayer(layerRank, incoming);
    for (const layerRank of [...layerRanks].reverse().slice(1)) reorderLayer(layerRank, outgoing);
  }

  const largestLayer = Math.max(1, ...[...layers.values()].map((layer) => layer.length));
  // Dense evidence maps often have many independent observations converging
  // on one claim. Keeping every same-rank node in one column turns that shape
  // into an unreadable vertical strip. Wrap dense ranks into deterministic
  // subcolumns and size every slot from the actual card rectangle.
  const maxRowsPerSubcolumn = Math.max(4, Math.ceil(Math.sqrt(largestLayer * 1.2)));
  const layerGeometry = new Map<number, {
    width: number;
    height: number;
    positions: Map<string, { x: number; y: number }>;
  }>();
  for (const layerRank of layerRanks) {
    const layer = layers.get(layerRank) || [];
    const columns: EvidenceFlowNode[][] = [];
    for (let start = 0; start < layer.length; start += maxRowsPerSubcolumn) {
      columns.push(layer.slice(start, start + maxRowsPerSubcolumn));
    }
    const columnWidths = columns.map((column) => Math.max(0, ...column.map(evidenceNodeWidth)));
    const columnHeights = columns.map((column) => (
      column.reduce((sum, node) => sum + evidenceNodeHeight(node), 0)
      + Math.max(0, column.length - 1) * evidenceRowGap
    ));
    const width = columnWidths.reduce((sum, value) => sum + value, 0)
      + Math.max(0, columns.length - 1) * evidenceSubcolumnGap;
    const height = Math.max(0, ...columnHeights);
    const positions = new Map<string, { x: number; y: number }>();
    let columnX = 0;
    columns.forEach((column, columnIndex) => {
      let nodeY = (height - columnHeights[columnIndex]) / 2;
      for (const node of column) {
        positions.set(node.id, { x: columnX, y: nodeY });
        nodeY += evidenceNodeHeight(node) + evidenceRowGap;
      }
      columnX += columnWidths[columnIndex] + evidenceSubcolumnGap;
    });
    layerGeometry.set(layerRank, { width, height, positions });
  }
  const largestLayerHeight = Math.max(0, ...[...layerGeometry.values()].map((geometry) => geometry.height));
  const layerX = new Map<number, number>();
  let nextLayerX = evidenceOrigin.x;
  for (const layerRank of layerRanks) {
    const geometry = layerGeometry.get(layerRank)!;
    layerX.set(layerRank, nextLayerX);
    nextLayerX += geometry.width + evidenceColumnGap;
  }
  const positionByID = new Map<string, { x: number; y: number }>();
  for (const layerRank of layerRanks) {
    const geometry = layerGeometry.get(layerRank)!;
    const centeredOffset = (largestLayerHeight - geometry.height) / 2;
    for (const node of layers.get(layerRank) || []) {
      const local = geometry.positions.get(node.id) || { x: 0, y: 0 };
      positionByID.set(node.id, {
        x: (layerX.get(layerRank) ?? evidenceOrigin.x) + local.x,
        y: evidenceOrigin.y + centeredOffset + local.y
      });
    }
  }

  return nodes.map((node) => {
    const pinned = !resetPins && node.data.pinned === true;
    if (pinned) return node;
    return {
      ...node,
      position: positionByID.get(node.id) || { ...evidenceOrigin },
      data: { ...node.data, pinned: false }
    };
  });
}

export function layoutEvidenceGraph(nodes: EvidenceFlowNode[], edges: EvidenceFlowEdge[], resetPins = false): EvidenceFlowNode[] {
  const groups = deriveEvidenceGroups(nodes);
  if (!groups.length) return layoutFlatEvidenceGraph(nodes, edges, resetPins);

  const nodeByID = new Map(nodes.map((node) => [node.id, node]));
  const ownerByMember = new Map<string, string>();
  for (const group of groups) {
    for (const memberID of group.memberIds) ownerByMember.set(memberID, group.id);
  }
  const groupFootprint = new Map<string, { width: number; height: number }>();
  for (const descriptor of groups) {
    const members = descriptor.memberIds
      .map((id) => nodeByID.get(id))
      .filter((node): node is EvidenceFlowNode => Boolean(node));
    if (!members.length) {
      groupFootprint.set(descriptor.id, { width: 680, height: 250 });
      continue;
    }
    const memberIDs = new Set(members.map((member) => member.id));
    const memberEdges = edges.filter((edge) => memberIDs.has(edge.source) && memberIDs.has(edge.target));
    if (memberEdges.length) {
      const local = layoutFlatEvidenceGraph(members, memberEdges, true);
      const minX = Math.min(...local.map((member) => member.position.x));
      const minY = Math.min(...local.map((member) => member.position.y));
      const maxX = Math.max(...local.map((member) => member.position.x + evidenceNodeWidth(member)));
      const maxY = Math.max(...local.map((member) => member.position.y + evidenceNodeHeight(member)));
      groupFootprint.set(descriptor.id, {
        width: Math.max(360, maxX - minX + 48),
        height: Math.max(210, maxY - minY + 82)
      });
      continue;
    }
    const packed = packEvidenceGrid(members);
    groupFootprint.set(descriptor.id, {
      width: Math.max(360, packed.width + 48),
      height: Math.max(210, packed.height + 82)
    });
  }
  const outerNodes = nodes
    .filter((node) => node.data.type === "group" || !ownerByMember.has(node.id))
    .map((node) => {
      const footprint = node.data.type === "group" ? groupFootprint.get(node.id) : undefined;
      return footprint ? { ...node, width: footprint.width, height: footprint.height } : node;
    });
  const outerNodeIDs = new Set(outerNodes.map((node) => node.id));
  const outerEdges: EvidenceFlowEdge[] = [];
  const outerEdgeKeys = new Set<string>();
  for (const edge of [...edges].sort((left, right) => left.id.localeCompare(right.id))) {
    const source = ownerByMember.get(edge.source) || edge.source;
    const target = ownerByMember.get(edge.target) || edge.target;
    if (source === target || !outerNodeIDs.has(source) || !outerNodeIDs.has(target)) continue;
    const type = edge.data?.type || "next_step";
    const key = `${source}\u0000${target}\u0000${type}`;
    if (outerEdgeKeys.has(key)) continue;
    outerEdgeKeys.add(key);
    outerEdges.push({
      ...edge,
      id: `layout:${source}:${type}:${target}`,
      source,
      target
    });
  }
  const laidOutOuter = layoutFlatEvidenceGraph(outerNodes, outerEdges, resetPins);
  const positioned = new Map(laidOutOuter.map((node) => [node.id, node]));

  for (const descriptor of groups) {
    const group = positioned.get(descriptor.id) || descriptor.group;
    const members = descriptor.memberIds
      .map((id) => nodeByID.get(id))
      .filter((node): node is EvidenceFlowNode => Boolean(node))
      .sort((left, right) => {
        const leftKey = `${left.data.occurredAt || ""}\u0000${left.id}`;
        const rightKey = `${right.data.occurredAt || ""}\u0000${right.id}`;
        return leftKey.localeCompare(rightKey);
      });
    if (!members.length) continue;
    const memberIDs = new Set(members.map((member) => member.id));
    const memberEdges = edges.filter((edge) => memberIDs.has(edge.source) && memberIDs.has(edge.target));
    let memberPositions = new Map<string, { x: number; y: number }>();
    if (memberEdges.length) {
      const local = layoutFlatEvidenceGraph(members, memberEdges, true);
      const minX = Math.min(...local.map((member) => member.position.x));
      const minY = Math.min(...local.map((member) => member.position.y));
      memberPositions = new Map(local.map((member) => [
        member.id,
        {
          x: group.position.x + 24 + member.position.x - minX,
          y: group.position.y + 64 + member.position.y - minY
        }
      ]));
    } else {
      const packed = packEvidenceGrid(members);
      memberPositions = new Map(members.map((member) => {
        const local = packed.positions.get(member.id) || { x: 0, y: 0 };
        return [
          member.id,
          {
            x: group.position.x + 24 + local.x,
            y: group.position.y + 64 + local.y
          }
        ];
      }));
    }
    for (const member of members) {
      if (!resetPins && member.data.pinned === true) {
        positioned.set(member.id, member);
        continue;
      }
      positioned.set(member.id, {
        ...member,
        position: memberPositions.get(member.id) || member.position,
        data: { ...member.data, pinned: false }
      });
    }
  }

  return nodes.map((node) => positioned.get(node.id) || node);
}

export function prepareLoadedEvidenceGraph(nodes: EvidenceFlowNode[], edges: EvidenceFlowEdge[]): EvidenceFlowNode[] {
  if (nodes.length < 2) return nodes;
  const first = nodes[0]?.position;
  const hasInvalidPosition = nodes.some((node) => (
    !Number.isFinite(node.position.x) || !Number.isFinite(node.position.y)
  ));
  const allShareOnePosition = Boolean(first) && nodes.every((node) => (
    node.position.x === first.x && node.position.y === first.y
  ));
  return hasInvalidPosition || allShareOnePosition
    ? layoutEvidenceGraph(nodes, edges, true)
    : nodes;
}
