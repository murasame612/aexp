import { MarkerType, Position, type Edge, type Node } from "@xyflow/react";
import type { CSSProperties } from "react";
import type { EvidenceChainEdge, EvidenceChainNode, EvidenceChainRunCandidate, EvidenceEdgeType, EvidenceGraphPatch, EvidenceNodeType, EvidenceProposal } from "./types";
import { runTitle, text } from "./utils";

export const evidenceNodeTypes: EvidenceNodeType[] = ["group", "dataset", "protocol", "claim", "issue", "plan", "hypothesis", "experiment", "conclusion", "note"];
export const evidenceEdgeTypes: EvidenceEdgeType[] = ["uses", "supports", "weakens", "reveals_issue", "supersedes", "next_step", "related_to", "does_not_prove", "custom"];
export const protocolGroupMemberTypes: EvidenceNodeType[] = ["dataset", "run", "plan", "experiment"];

export function isProtocolGroupMemberType(type: EvidenceNodeType): boolean {
  return protocolGroupMemberTypes.includes(type);
}

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
  groupId?: string;
  groupKind?: "protocol";
  version?: string;
  provenanceSummary?: string;
  collapsed?: boolean;
  groupMemberCount?: number;
  groupInternalEdgeCount?: number;
  groupExternalEdgeCount?: number;
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
    case "group":
      return "协议容器";
  }
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
