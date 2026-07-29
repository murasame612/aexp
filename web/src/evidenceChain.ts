import { MarkerType, Position, type Edge, type Node } from "@xyflow/react";
import type { CSSProperties } from "react";
import type { EvidenceChainEdge, EvidenceChainNode, EvidenceChainRunCandidate, EvidenceEdgeType, EvidenceGraphPatch, EvidenceNodeType, EvidenceProposal } from "./types";
import { runTitle, text } from "./utils";

export const evidenceNodeTypes: EvidenceNodeType[] = ["dataset", "protocol", "claim", "issue", "plan", "hypothesis", "experiment", "conclusion", "note"];
export const evidenceEdgeTypes: EvidenceEdgeType[] = ["uses", "supports", "weakens", "reveals_issue", "supersedes", "next_step", "related_to", "does_not_prove", "custom"];

export interface EvidenceNodeData extends Record<string, unknown> {
  type: EvidenceNodeType;
  title: string;
  body: string;
  runTitle?: string;
  runId?: string;
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
  readOnly?: boolean;
  draft?: boolean;
  proposalRunId?: string;
  sourceEdgeId?: string;
  onSelectEdge?: (edgeId: string) => void;
  onUpdateEdge?: (edgeId: string, patch: { type?: EvidenceEdgeType; label?: string; rationale?: string }) => void;
  labels?: EvidenceBoardLabels;
}

export type EvidenceFlowNode = Node<EvidenceNodeData, "evidence">;
export type EvidenceFlowEdge = Edge<EvidenceEdgeData>;

export interface EvidenceProposalPreview {
  runId: string;
  proposalId?: string;
  title?: string;
  chainId: string;
  routingReason: string;
  nodes: EvidenceFlowNode[];
  edges: EvidenceFlowEdge[];
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

export function nodeTypeLabel(type: EvidenceNodeType) {
  switch (type) {
    case "dataset":
      return "数据版本";
    case "protocol":
      return "实验协议";
    case "run":
      return "实验";
    case "claim":
      return "研究结论";
    case "issue":
      return "中途问题";
    case "hypothesis":
      return "假说";
    case "experiment":
      return "实验设计";
    case "plan":
      return "计划";
    case "conclusion":
      return "结论";
    case "note":
      return "笔记";
    case "map_ref":
      return "Topic 引用";
  }
}

export function defaultNodeTitle(type: EvidenceNodeType) {
  return nodeTypeLabel(type);
}

export function defaultEvidenceRelation(source: EvidenceNodeType, target: EvidenceNodeType): EvidenceEdgeType {
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
    width: 286,
    height: 184,
    data: {
      type,
      title: defaultNodeTitle(type),
      body: ""
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
      type: node.type,
      title: node.title || defaultNodeTitle(node.type),
      body: node.body || "",
      runId: node.run_id || undefined,
      projectCardId: node.project_card_id || undefined,
      pinned: node.pinned === true,
      occurredAt: node.occurred_at || undefined,
      ...data
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
      for (const key of ["pinned", "position", "width", "height", "layout", "sourceHandle", "targetHandle", "autoHandles"]) delete value[key];
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

const evidenceColumnStep = 420;
const evidenceRowStep = 196;
const evidenceOrigin = { x: 80, y: 72 };

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

// layoutEvidenceGraph is deliberately pure and deterministic. Accepted
// semantics determine rank; coordinates remain a UI projection. Ordering
// inside a rank follows adjacent nodes instead of node type lanes so the
// common evidence DAG stays left-to-right with fewer avoidable crossings.
export function layoutEvidenceGraph(nodes: EvidenceFlowNode[], edges: EvidenceFlowEdge[], resetPins = false): EvidenceFlowNode[] {
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
  const positionByID = new Map<string, { x: number; y: number }>();
  for (const layerRank of layerRanks) {
    const layer = layers.get(layerRank) || [];
    const centeredOffset = (largestLayer - layer.length) * evidenceRowStep / 2;
    layer.forEach((node, index) => {
      positionByID.set(node.id, {
        x: evidenceOrigin.x + layerRank * evidenceColumnStep,
        y: evidenceOrigin.y + centeredOffset + index * evidenceRowStep
      });
    });
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
