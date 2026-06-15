import type { Edge, Node } from "@xyflow/react";
import type { EvidenceChainEdge, EvidenceChainNode, EvidenceChainRunCandidate, EvidenceEdgeType, EvidenceNodeType } from "./types";
import { runTitle, text } from "./utils";

export const evidenceNodeTypes: EvidenceNodeType[] = ["hypothesis", "experiment", "plan", "conclusion", "note"];
export const evidenceEdgeTypes: EvidenceEdgeType[] = ["supports", "does_not_prove", "next_step", "custom"];

export interface EvidenceNodeData extends Record<string, unknown> {
  type: EvidenceNodeType;
  title: string;
  body: string;
  runId?: string;
  projectCardId?: string;
  status?: string;
  runKind?: string;
  keyMetrics?: string;
  evidenceLevel?: string;
  onOpenRun?: (runId: string) => void;
  onUpdateNode?: (nodeId: string, patch: Partial<EvidenceNodeData>) => void;
  labels?: EvidenceBoardLabels;
}

export interface EvidenceEdgeData extends Record<string, unknown> {
  type: EvidenceEdgeType;
  rationale: string;
  onSelectEdge?: (edgeId: string) => void;
  onUpdateEdge?: (edgeId: string, patch: { type?: EvidenceEdgeType; label?: string; rationale?: string }) => void;
  labels?: EvidenceBoardLabels;
}

export type EvidenceFlowNode = Node<EvidenceNodeData, "evidence">;
export type EvidenceFlowEdge = Edge<EvidenceEdgeData>;

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
    case "supports":
      return "证明";
    case "does_not_prove":
      return "不能证明";
    case "next_step":
      return "进一步";
    case "custom":
      return "自定义";
  }
}

export function nodeTypeLabel(type: EvidenceNodeType) {
  switch (type) {
    case "run":
      return "实验";
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
  }
}

export function defaultNodeTitle(type: EvidenceNodeType) {
  return nodeTypeLabel(type);
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

export function candidateToNode(candidate: EvidenceChainRunCandidate, position: { x: number; y: number }): EvidenceFlowNode {
  const title = candidate.verdict || candidate.question || (candidate.run ? runTitle(candidate.run) : candidate.run_id);
  return {
    id: `node_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 7)}`,
    type: "evidence",
    position,
    width: 286,
    height: 184,
    data: {
      type: "run",
      title,
      body: candidate.question || candidate.next_action || "",
      runId: candidate.run_id,
      projectCardId: candidate.project_card_id || "",
      status: candidate.run?.status || "",
      runKind: candidate.run?.kind || "",
      keyMetrics: candidate.key_metrics || "",
      evidenceLevel: candidate.evidence_level || ""
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
    label: edge.label ?? edgeTypeLabel(type),
    data: { type, rationale: edge.rationale || "", ...data },
    animated: type === "next_step",
    style: edgeStyle(type)
  };
}

export function serializeEvidenceGraph(nodes: EvidenceFlowNode[], edges: EvidenceFlowEdge[]) {
  return {
    nodes: nodes.map((node): EvidenceChainNode => ({
      id: node.id,
      type: node.data.type,
      title: node.data.title,
      body: node.data.body,
      run_id: node.data.runId || "",
      project_card_id: node.data.projectCardId || "",
      x: node.position.x,
      y: node.position.y,
      width: typeof node.width === "number" ? node.width : 286,
      height: typeof node.height === "number" ? node.height : 184,
      data_json: JSON.stringify({
        status: node.data.status || "",
        runKind: node.data.runKind || "",
        keyMetrics: node.data.keyMetrics || "",
        evidenceLevel: node.data.evidenceLevel || ""
      })
    })),
    edges: edges.map((edge): EvidenceChainEdge => {
      const type = edge.data?.type || "next_step";
      return {
        id: edge.id,
        source_node_id: edge.source,
        target_node_id: edge.target,
        type,
        label: edge.label == null ? edgeTypeLabel(type) : String(edge.label),
        rationale: edge.data?.rationale || "",
        data_json: "{}"
      };
    })
  };
}

export function edgeStyle(type: EvidenceEdgeType) {
  switch (type) {
    case "supports":
      return { stroke: "#32664b", strokeWidth: 2 };
    case "does_not_prove":
      return { stroke: "#b24b43", strokeWidth: 2, strokeDasharray: "6 4" };
    case "custom":
      return { stroke: "#56616d", strokeWidth: 2 };
    case "next_step":
    default:
      return { stroke: "#4f6f8f", strokeWidth: 2 };
  }
}
