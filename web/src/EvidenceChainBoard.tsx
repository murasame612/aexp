import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent } from "react";
import {
  addEdge,
  Background,
  BaseEdge,
  ConnectionMode,
  Controls,
  EdgeLabelRenderer,
  Handle,
  MiniMap,
  Position,
  ReactFlow,
  ReactFlowProvider,
  ViewportPortal,
  getBezierPath,
  getSmoothStepPath,
  useEdgesState,
  useNodesState,
  useReactFlow,
  type Connection,
  type EdgeProps,
  type EdgeMouseHandler,
  type NodeMouseHandler,
  type NodeProps
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { AnimatePresence, motion, useReducedMotion } from "framer-motion";
import { AlertTriangle, Archive, ArrowUpRight, Bot, Check, ChevronDown, ChevronLeft, ChevronRight, Copy, Eye, Focus, GitBranch, GripVertical, List, ListPlus, Network, PinOff, Plus, RefreshCcw, Route, Save, Search, Trash2, X } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  createEvidencePromotion,
  createEvidenceChain,
  createProjectEvidenceMap,
  createProjectEvidenceProposal,
  deleteEvidenceChain,
  ensureProjectEvidenceMap,
  getEvidenceChain,
  getEvidenceChains,
  getEvidenceAudit,
  getEvidenceResearchThreads,
  getProjectEvidenceMap,
  getProjectEvidenceProposals,
  getEvidenceRunCandidates,
  planEvidenceProposal,
  planEvidencePromotion,
  planEvidenceGraphProposal,
  reviewEvidenceProposal,
  reviewEvidenceGraphProposal,
  saveEvidenceChainGraph,
  updateEvidenceChain
} from "./api";
import {
  apiEdgeToFlowEdge,
  apiNodeToFlowNode,
  apiResearchProjectionToFlow,
  candidateMatches,
  candidateRunNodeFields,
  candidateToNode,
  buildEvidenceEditPatch,
  createTextNode,
  defaultEvidenceRelation,
  evidenceMapReferenceStatus,
  evidenceProposalPreview,
  evidenceWorkspaceProposalPreview,
  evidenceMarkerEnd,
  edgeStyle,
  edgeTypeLabel,
  evidenceNeighborhood,
  evidenceReadingSections,
  evidenceAuthoringNodeTypes,
  evidenceThreadRibbonPath,
  evidenceThreadFocusRelations,
  evidenceThreadRelationFocus,
  evidenceResearchStages,
  layoutEvidenceNeighborhood,
  evidenceEdgeTypes,
  evidenceNodeTypes,
  filterRunCandidatesForProject,
  groupRunCandidatesByProject,
  isProtocolGroupMemberType,
  nodeTypeLabel,
  proposalNoticeScopeKey,
  projectEvidenceGroups,
  prepareLoadedEvidenceGraph,
  serializeEvidenceGraph,
  shouldOverlayEvidenceProposal,
  layoutEvidenceGraph,
  layoutEvidenceGraphFromIntent,
  remapEvidenceLayoutIntent,
  resolveProjectEvidenceChainSelection,
  inspectProtocolFrameMigration,
  convertProtocolToFrame,
  protocolContainerMoveDeltaForKey,
  translateProtocolContainer,
  type EvidenceFlowEdge,
  type EvidenceFlowNode,
  type EvidenceHandleSide,
  type EvidenceGroupDescriptor,
  type EvidenceGroupFrameBounds,
  type EvidenceNeighborhood,
  type EvidenceNodeData,
  type EvidenceReadingSection,
  type EvidenceAdjacentStageRelation,
  type EvidenceThreadRelationFocus,
  type EvidenceResearchProjection,
  type EvidenceResearchThread,
  type EvidenceResearchStage
} from "./evidenceChain";
import {
  evidenceOrthogonalPath,
  evidenceRoutingGeometryKey,
  evidenceRoutingTopologyKey,
  routeEvidenceGraphEdges
} from "./evidenceRouting";
import type { I18nKey } from "./i18n";
import { readEvidenceMapFromSearch, withEvidenceMapSearch } from "./projectRoute";
import type { EvidenceChainAuditReportDTO, EvidenceChainDetail, EvidenceChainRunCandidate, EvidenceEdgeType, EvidenceLayoutIntent, EvidenceNodeType, EvidenceProposal } from "./types";
import { fmtShortTime, text } from "./utils";

const candidateMime = "application/x-aexp-run-candidate";
const evidenceHandleSides = [Position.Top, Position.Right, Position.Bottom, Position.Left] as const;
const compactEvidenceNodeSize = { width: 306, height: 138 };
type EvidenceWorkspaceView = "threads" | "list" | "focus" | "overview";
type EvidenceFocusDepth = 1 | 2 | 3 | "all";
type ProposalPreview = NonNullable<ReturnType<typeof evidenceProposalPreview>>;

export function EvidenceThreadRelationHint({ focus }: { focus: EvidenceThreadRelationFocus | null }) {
  if (!focus) return null;
  if (focus.disconnected) {
    return <span className="evidence-thread-card-open is-disconnected" role="status">没有直接前因/后果</span>;
  }
  if (focus.hiddenRelationCount > 0) {
    return <span className="evidence-thread-card-open is-disconnected" role="status">清空搜索可查看 {focus.hiddenRelationCount} 个关系</span>;
  }
  return null;
}

interface EvidenceThreadRibbonRelation {
  id: string;
  sourceNodeId: string;
  targetNodeId: string;
  type: EvidenceEdgeType;
  direct: boolean;
}

interface EvidenceThreadRibbonPath extends EvidenceThreadRibbonRelation {
  d: string;
  color: string;
  neutral: boolean;
}

function EvidenceThreadRelationOverlay({
  relations,
  reducedMotion
}: {
  relations: EvidenceThreadRibbonRelation[];
  reducedMotion: boolean;
}) {
  const svgRef = useRef<SVGSVGElement | null>(null);
  const markerPrefix = useId().replace(/:/g, "");
  const [paths, setPaths] = useState<EvidenceThreadRibbonPath[]>([]);

  useLayoutEffect(() => {
    const svg = svgRef.current;
    const container = svg?.parentElement;
    if (!svg || !container || !relations.length) {
      setPaths([]);
      return;
    }
    let frame = 0;
    const measure = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        const containerRect = container.getBoundingClientRect();
        const rects = new Map<string, { left: number; top: number; width: number; height: number }>();
        container.querySelectorAll<HTMLElement>("[data-evidence-node-id]").forEach((element) => {
          const nodeID = element.dataset.evidenceNodeId;
          if (!nodeID) return;
          const rect = element.getBoundingClientRect();
          rects.set(nodeID, {
            left: rect.left - containerRect.left,
            top: rect.top - containerRect.top,
            width: rect.width,
            height: rect.height
          });
        });
        setPaths(relations.flatMap((relation) => {
          const source = rects.get(relation.sourceNodeId);
          const target = rects.get(relation.targetNodeId);
          if (!source || !target) return [];
          return [{
            ...relation,
            d: evidenceThreadRibbonPath(source, target),
            color: evidenceColor(relation.type),
            neutral: relation.type === "related_to" || relation.type === "custom"
          }];
        }));
      });
    };
    const observer = new ResizeObserver(measure);
    observer.observe(container);
    container.querySelectorAll<HTMLElement>("[data-evidence-node-id]").forEach((element) => observer.observe(element));
    measure();
    return () => {
      cancelAnimationFrame(frame);
      observer.disconnect();
    };
  }, [relations]);

  return (
    <svg
      ref={svgRef}
      className={`evidence-thread-relation-ribbons${reducedMotion ? " is-reduced-motion" : ""}`}
      aria-hidden="true"
      focusable="false"
    >
      <defs>
        {paths.map((path, index) => (
          <marker
            id={`${markerPrefix}-${index}`}
            key={`${path.id}:${path.sourceNodeId}:${path.targetNodeId}`}
            markerWidth="7"
            markerHeight="7"
            refX="6"
            refY="3.5"
            orient="auto"
            markerUnits="userSpaceOnUse"
          >
            <path d="M 0 0 L 7 3.5 L 0 7 Z" fill={path.color} />
          </marker>
        ))}
      </defs>
      {paths.map((path, index) => (
        <g
          className={`evidence-thread-ribbon${path.direct ? " is-direct" : " is-context"}${path.neutral ? " is-neutral" : ""}`}
          key={`${path.id}:${path.sourceNodeId}:${path.targetNodeId}`}
          style={{ "--relation-ribbon-color": path.color } as CSSProperties}
        >
          <path className="evidence-thread-ribbon-halo" d={path.d} />
          <path className="evidence-thread-ribbon-band" d={path.d} />
          <path className="evidence-thread-ribbon-core" d={path.d} markerEnd={`url(#${markerPrefix}-${index})`} />
        </g>
      ))}
    </svg>
  );
}
type EvidencePreviewEntry =
  | { kind: "workspace"; key: string; title: string; proposal: EvidenceProposal; preview: ProposalPreview }
  | { kind: "legacy"; key: string; title: string; candidate: EvidenceChainRunCandidate; preview: ProposalPreview };

function parseRoutingTokens(value: string) {
  const seen = new Set<string>();
  return value
    .split(/[\n,，]/)
    .map((item) => item.trim())
    .filter((item) => {
      const key = item.toLocaleLowerCase();
      if (!key || seen.has(key)) return false;
      seen.add(key);
      return true;
    });
}

function routingHintsKey(recipes: string[], keywords: string[]) {
  return JSON.stringify({ recipes, keywords });
}

export function EvidenceChainBoard({
  token,
  t,
  onOpenRun,
  projectId = ""
}: {
  token: string;
  t: (key: I18nKey) => string;
  onOpenRun: (id: string) => void;
  projectId?: string;
}) {
  return (
    <ReactFlowProvider>
      <EvidenceChainWorkspace token={token} t={t} onOpenRun={onOpenRun} projectId={projectId} />
    </ReactFlowProvider>
  );
}

function EvidenceChainWorkspace({ token, t, onOpenRun, projectId }: { token: string; t: (key: I18nKey) => string; onOpenRun: (id: string) => void; projectId: string }) {
  const queryClient = useQueryClient();
  const flow = useReactFlow<EvidenceFlowNode, EvidenceFlowEdge>();
  const reduceMotion = useReducedMotion();
  const [chainQuery, setChainQuery] = useState("");
  const [candidateQuery, setCandidateQuery] = useState("");
  const [chainSelection, setChainSelection] = useState(() => ({
    projectId,
    chainId: projectId && typeof window !== "undefined" ? readEvidenceMapFromSearch(window.location.search) : ""
  }));
  const selectedChainId = chainSelection.projectId === projectId ? chainSelection.chainId : "";
  const setSelectedChainId = useCallback((chainId: string) => {
    setChainSelection({ projectId, chainId });
  }, [projectId]);
  const [selected, setSelected] = useState<{ kind: "node" | "edge"; id: string } | null>(null);
  const [workspaceView, setWorkspaceView] = useState<EvidenceWorkspaceView>("threads");
  const [readerQuery, setReaderQuery] = useState("");
  const [focusNodeID, setFocusNodeID] = useState("");
  const [focusDepth, setFocusDepth] = useState<EvidenceFocusDepth>("all");
  const [dirty, setDirty] = useState(false);
  const dirtyRef = useRef(false);
  const appliedDetailRef = useRef<{ chainId: string; key: string; nodeCount: number } | null>(null);
  const [saveState, setSaveState] = useState<"idle" | "saved" | "saving" | "failed" | "conflict">("idle");
  const [chainTitleDraft, setChainTitleDraft] = useState("");
  const [chainDescriptionDraft, setChainDescriptionDraft] = useState("");
  const [routingRecipesDraft, setRoutingRecipesDraft] = useState("");
  const [routingKeywordsDraft, setRoutingKeywordsDraft] = useState("");
  const [routingOpen, setRoutingOpen] = useState(false);
  const [graphIdCopied, setGraphIdCopied] = useState(false);
  const [leftOpen, setLeftOpen] = useState(() => !projectId && (typeof window === "undefined" ? true : !window.matchMedia("(max-width: 760px)").matches));
  const [runTrayOpen, setRunTrayOpen] = useState(false);
  const [nodeMenuOpen, setNodeMenuOpen] = useState(false);
  const nodeMenuTriggerRef = useRef<HTMLButtonElement | null>(null);
  const nodeMenuRef = useRef<HTMLDivElement | null>(null);
  const [selectedCandidateGroup, setSelectedCandidateGroup] = useState("");
  const [proposalNotice, setProposalNotice] = useState("");
  const proposalNoticeScopeRef = useRef("");
  const [reviewingRunID, setReviewingRunID] = useState("");
  const [promoting, setPromoting] = useState(false);
  const [draftDockOpen, setDraftDockOpen] = useState(false);
  const [previewRunID, setPreviewRunID] = useState("");
  const [proposalCanvasPreview, setProposalCanvasPreview] = useState(false);
  const proposalReturnViewRef = useRef<EvidenceWorkspaceView>("threads");
  const [layoutIntentPreview, setLayoutIntentPreview] = useState(true);
  const lastSavedMeta = useRef<{ id: string; title: string; description: string; routingHintsKey: string } | null>(null);
  const [chainMetaComposing, setChainMetaComposing] = useState(false);
  const [nodes, setNodes, onNodesChangeBase] = useNodesState<EvidenceFlowNode>([]);
  const [edges, setEdges, onEdgesChangeBase] = useEdgesState<EvidenceFlowEdge>([]);
  const dragStartGroupFramesRef = useRef<Array<{ id: string; bounds: EvidenceGroupFrameBounds }>>([]);
  const [draggingProtocolFrame, setDraggingProtocolFrame] = useState<{ id: string; bounds: EvidenceGroupFrameBounds } | null>(null);
  const draggingProtocolFrameRef = useRef<{ id: string; bounds: EvidenceGroupFrameBounds } | null>(null);
  const protocolContainerDragRef = useRef<{
    id: string;
    start: { x: number; y: number };
    latest: { x: number; y: number };
    baseline: EvidenceFlowNode[];
    wasDirty: boolean;
    moved: boolean;
  } | null>(null);
  const protocolContainerDragFrameRef = useRef<number | null>(null);
  const [movingProtocolContainerId, setMovingProtocolContainerId] = useState("");
  const routingTimerRef = useRef<number | null>(null);
  const boardLabels = useMemo(
    () => ({
      titlePlaceholder: t("titlePlaceholder"),
      nodeBodyPlaceholder: t("nodeBodyPlaceholder"),
      openRun: t("openRun"),
      relationLabel: t("relationLabel"),
      rationale: t("rationale"),
      done: t("done")
    }),
    [t]
  );

  const primaryMap = useQuery({
    queryKey: ["project-evidence-map", token, projectId],
    queryFn: () => getProjectEvidenceMap(token, projectId),
    enabled: Boolean(projectId),
    retry: (failureCount, error) => !(error instanceof ApiError && error.status === 404) && failureCount < 2,
    refetchInterval: 15000
  });
  const chains = useQuery({
    queryKey: ["evidence-chains", token, projectId, chainQuery],
    queryFn: () => getEvidenceChains(token, chainQuery, projectId),
    refetchInterval: 15000
  });
  const orderedChains = useMemo(
    () => [...(chains.data || [])].sort((left, right) => {
      const rank = (chain: { role?: string; status?: string }) => (
        chain.role === "primary" && chain.status === "active" ? 0
          : chain.status === "active" ? 1
            : 2
      );
      return rank(left) - rank(right) || (right.updated_at || "").localeCompare(left.updated_at || "");
    }),
    [chains.data]
  );
  const selectChain = useCallback((chainId: string, replace = false, preserveProposal = false) => {
    if (chainId !== selectedChainId) {
      appliedDetailRef.current = null;
      setNodes([]);
      setEdges([]);
      setSelected(null);
      setNodeMenuOpen(false);
      setRunTrayOpen(false);
      setRoutingOpen(false);
      if (!preserveProposal) setPreviewRunID("");
      setWorkspaceView("threads");
      setReaderQuery("");
      setFocusNodeID("");
    }
    setSelectedChainId(chainId);
    if (!projectId || typeof window === "undefined") return;
    const primaryId = primaryMap.data?.id
      || orderedChains.find((chain) => chain.role === "primary" && chain.status === "active")?.id
      || "";
    const mapIdForURL = chainId === primaryId ? "" : chainId;
    const search = withEvidenceMapSearch(window.location.search, mapIdForURL);
    const nextURL = `${window.location.pathname}${search}${window.location.hash}`;
    const currentURL = `${window.location.pathname}${window.location.search}${window.location.hash}`;
    if (nextURL === currentURL) return;
    if (replace) window.history.replaceState(window.history.state, "", nextURL);
    else window.history.pushState(window.history.state, "", nextURL);
  }, [orderedChains, primaryMap.data?.id, projectId, selectedChainId, setEdges, setNodes]);

  useEffect(() => {
    if (!nodeMenuOpen) return;
    nodeMenuRef.current?.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
    const closeNodeMenu = (returnFocus: boolean) => {
      setNodeMenuOpen(false);
      if (returnFocus) nodeMenuTriggerRef.current?.focus();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      closeNodeMenu(true);
    };
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node | null;
      if (nodeMenuRef.current?.contains(target) || nodeMenuTriggerRef.current?.contains(target)) return;
      closeNodeMenu(false);
    };
    window.addEventListener("keydown", onKeyDown);
    window.addEventListener("pointerdown", onPointerDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("pointerdown", onPointerDown);
    };
  }, [nodeMenuOpen]);
  const detail = useQuery({
    queryKey: ["evidence-chain", token, selectedChainId],
    queryFn: () => getEvidenceChain(token, selectedChainId),
    enabled: !!selectedChainId,
    refetchInterval: () => dirtyRef.current ? false : 5000,
    refetchOnWindowFocus: true
  });
  const researchThreads = useQuery({
    queryKey: ["evidence-research-threads", token, selectedChainId],
    queryFn: () => getEvidenceResearchThreads(token, selectedChainId),
    enabled: !!selectedChainId,
    refetchInterval: () => dirtyRef.current ? false : 5000,
    refetchOnWindowFocus: true
  });
  const evidenceAudit = useQuery({
    queryKey: ["evidence-audit", token, selectedChainId],
    queryFn: () => getEvidenceAudit(token, selectedChainId),
    enabled: !!selectedChainId,
    refetchInterval: () => dirtyRef.current ? false : 15000,
    refetchOnWindowFocus: true
  });
  const candidates = useQuery({
    queryKey: ["evidence-run-candidates", token, projectId],
    queryFn: () => getEvidenceRunCandidates(token, projectId, 160),
    enabled: Boolean(selectedChainId && (runTrayOpen || previewRunID)),
    refetchInterval: 12000
  });
  const workspaceProposals = useQuery({
    queryKey: ["project-evidence-proposals", token, projectId, "pending"],
    queryFn: () => getProjectEvidenceProposals(token, projectId, "pending"),
    enabled: Boolean(projectId && selectedChainId),
    refetchInterval: 12000
  });

  useEffect(() => {
    if (projectId) {
      const requestedId = typeof window !== "undefined" ? readEvidenceMapFromSearch(window.location.search) : "";
      const preferred = resolveProjectEvidenceChainSelection({
        requestedId,
        currentId: selectedChainId,
        chains: chains.data,
        primaryId: primaryMap.data?.id
      });
      // undefined means the discovery queries have not supplied enough facts
      // yet. It must never be converted into the definitive "no Map" state.
      if (preferred === undefined || preferred === selectedChainId) return;
      selectChain(preferred, true);
      return;
    }
    if (!selectedChainId && chains.data?.length) selectChain(chains.data[0].id, true);
  }, [chains.data, primaryMap.data?.id, projectId, selectChain, selectedChainId]);

  useEffect(() => {
    appliedDetailRef.current = null;
    setNodes([]);
    setEdges([]);
    setSelected(null);
    setLeftOpen(false);
    setRunTrayOpen(false);
    setSelectedCandidateGroup("");
    setPreviewRunID("");
    setWorkspaceView("threads");
    setReaderQuery("");
    setFocusNodeID("");
  }, [projectId, setEdges, setNodes]);

  useEffect(() => {
    if (!projectId || typeof window === "undefined") return;
    const onPopState = () => {
      setSelectedChainId(readEvidenceMapFromSearch(window.location.search));
      appliedDetailRef.current = null;
      setPreviewRunID("");
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, [projectId]);

  const markDirty = useCallback(() => {
    dirtyRef.current = true;
    setDirty(true);
    setSaveState("idle");
  }, []);

  const updateNodeData = useCallback(
    (nodeId: string, patch: Partial<EvidenceNodeData>) => {
      setNodes((current) => current.map((node) => (node.id === nodeId ? { ...node, data: { ...node.data, ...patch } } : node)));
      if (detail.data?.status !== "archived") markDirty();
    },
    [detail.data?.status, markDirty, setNodes]
  );

  const updateEdgeData = useCallback(
    (edgeId: string, patch: { type?: EvidenceEdgeType; label?: string; rationale?: string }) => {
      setEdges((current) =>
        current.map((edge) => {
          if (edge.id !== edgeId) return edge;
          const type = patch.type || edge.data?.type || "next_step";
          return {
            ...edge,
            type: "evidence",
            label: patch.label ?? edge.label ?? edgeTypeLabel(type),
            data: { ...edge.data, type, rationale: patch.rationale ?? edge.data?.rationale ?? "" },
            animated: false,
            markerEnd: evidenceMarkerEnd(type),
            style: edgeStyle(type)
          };
        })
      );
      markDirty();
    },
    [markDirty, setEdges]
  );

  const selectEdge = useCallback(
    (edgeId: string) => {
      setSelected({ kind: "edge", id: edgeId });
      setEdges((current) => current.map((edge) => ({ ...edge, selected: edge.id === edgeId })));
      setNodes((current) => current.map((node) => ({ ...node, selected: false })));
    },
    [setEdges, setNodes]
  );
  const clearSelection = useCallback(() => {
    setSelected(null);
    setEdges((current) => current.map((edge) => ({ ...edge, selected: false })));
    setNodes((current) => current.map((node) => ({ ...node, selected: false })));
  }, [setEdges, setNodes]);
  const withNodeHandlers = useCallback((node: EvidenceFlowNode): EvidenceFlowNode => {
    const referencedMap = node.data.target_map_id
      ? orderedChains.find((chain) => chain.id === node.data.target_map_id)
      : undefined;
    const mapRefStatus = evidenceMapReferenceStatus(node.data, referencedMap);
    const visualSize = node.data.type === "group"
      ? {
          width: Math.max(680, typeof node.width === "number" ? node.width : 0),
          height: Math.max(250, typeof node.height === "number" ? node.height : 0)
        }
      : compactEvidenceNodeSize;
    return {
      ...node,
      connectable: node.data.type !== "group" && node.connectable !== false,
      width: visualSize.width,
      height: visualSize.height,
      style: { ...node.style, ...visualSize },
      data: {
        ...node.data,
        mapRefStatus,
        readOnly: detail.data?.status === "archived",
        labels: boardLabels,
        onOpenRun,
        onOpenMap: selectChain,
        onUpdateNode: updateNodeData
      }
    };
  }, [boardLabels, detail.data?.status, onOpenRun, orderedChains, selectChain, updateNodeData]);
  const withEdgeHandlers = useCallback(
    (edge: EvidenceFlowEdge): EvidenceFlowEdge => {
      const type = edge.data?.type || "next_step";
      return {
        ...edge,
        type: "evidence",
        animated: false,
        markerEnd: evidenceMarkerEnd(type),
        style: edgeStyle(type),
        data: {
          type,
          rationale: edge.data?.rationale || "",
          ...edge.data,
          readOnly: detail.data?.status === "archived",
          labels: boardLabels,
          onSelectEdge: selectEdge,
          onUpdateEdge: updateEdgeData
        }
      };
    },
    [boardLabels, detail.data?.status, selectEdge, updateEdgeData]
  );
  const convertProtocolNodeToFrame = useCallback((nodeId: string) => {
    const result = convertProtocolToFrame(nodes, edges, nodeId);
    if (!result.migration.eligible || detail.data?.status === "archived") return;
    setNodes(result.nodes.map(withNodeHandlers));
    setEdges(result.edges.map(withEdgeHandlers));
    markDirty();
  }, [detail.data?.status, edges, markDirty, nodes, setEdges, setNodes, withEdgeHandlers, withNodeHandlers]);

  useEffect(() => {
    setNodes((current) => current.map((node) => ({ ...node, data: { ...node.data, labels: boardLabels } })));
    setEdges((current) => current.map((edge) => ({ ...edge, data: { type: edge.data?.type || "next_step", rationale: edge.data?.rationale || "", ...edge.data, labels: boardLabels } })));
  }, [boardLabels, setEdges, setNodes]);

  const scopedCandidates = useMemo(
    () => filterRunCandidatesForProject(candidates.data || [], projectId),
    [candidates.data, projectId]
  );
  const pendingPreviews = useMemo(() => {
    const workspace: EvidencePreviewEntry[] = (workspaceProposals.data || []).flatMap((proposal) => {
      const preview = evidenceWorkspaceProposalPreview(proposal);
      return preview ? [{
        kind: "workspace" as const,
        key: proposal.id,
        title: proposal.summary || proposal.id,
        proposal,
        preview
      }] : [];
    });
    const legacy: EvidencePreviewEntry[] = scopedCandidates.flatMap((candidate) => {
      const preview = evidenceProposalPreview(candidate);
      return preview ? [{
        kind: "legacy" as const,
        key: candidate.run_id,
        title: candidate.run ? candidateRunNodeFields(candidate).runDisplayTitle : candidate.run_id,
        candidate,
        preview
      }] : [];
    });
    return [...workspace, ...legacy];
  }, [scopedCandidates, workspaceProposals.data]);
  const activePreviewEntry = useMemo(
    () => pendingPreviews.find((item) => item.key === previewRunID) || null,
    [pendingPreviews, previewRunID]
  );
  const activePreviewTarget = useMemo(
    () => orderedChains.find((chain) => chain.id === activePreviewEntry?.preview.chainId) || null,
    [activePreviewEntry, orderedChains]
  );
  const activeCanvasPreview = activePreviewEntry && shouldOverlayEvidenceProposal(
    activePreviewEntry.preview.chainId,
    selectedChainId,
    proposalCanvasPreview
  ) ? activePreviewEntry : null;
  const previewPlan = useQuery({
    // A proposal may become safely replayable or genuinely conflicted after
    // the target Map changes. Bind the cached plan to the target identity so
    // the inspector never keeps an obsolete REVISION_CONFLICT until refresh.
    queryKey: [
      "evidence-proposal-plan",
      token,
      previewRunID,
      activePreviewTarget?.revision || 0,
      activePreviewTarget?.graph_hash || ""
    ],
    queryFn: () => activePreviewEntry?.kind === "workspace"
      ? planEvidenceProposal(token, activePreviewEntry.key)
      : planEvidenceGraphProposal(token, activePreviewEntry?.key || previewRunID),
    enabled: Boolean(previewRunID && activePreviewEntry),
    retry: false
  });
  const proposalNoticeScope = proposalNoticeScopeKey(
    selectedChainId,
    activePreviewEntry?.key || "",
    activePreviewTarget?.revision || 0
  );

  useEffect(() => {
    if (proposalNoticeScopeRef.current && proposalNoticeScopeRef.current !== proposalNoticeScope) {
      setProposalNotice("");
    }
    proposalNoticeScopeRef.current = proposalNoticeScope;
  }, [proposalNoticeScope]);

  useEffect(() => {
    if (previewRunID && !activePreviewEntry) setPreviewRunID("");
  }, [activePreviewEntry, previewRunID]);
  useEffect(() => {
    setProposalCanvasPreview(false);
    setLayoutIntentPreview(true);
  }, [previewRunID]);

  const candidateByRunId = useMemo(() => {
    const out = new Map<string, EvidenceChainRunCandidate>();
    for (const candidate of scopedCandidates) {
      if (!candidate.run_id || out.has(candidate.run_id)) continue;
      out.set(candidate.run_id, candidate);
    }
    return out;
  }, [scopedCandidates]);

  const hydrateRunNodeFromCandidate = useCallback(
    (node: EvidenceFlowNode): EvidenceFlowNode => {
      if (node.data.type !== "run" || !node.data.runId) return node;
      const candidate = candidateByRunId.get(node.data.runId);
      if (!candidate) return node;
      const { runDisplayTitle, body } = candidateRunNodeFields(candidate);
      const currentTitle = text(node.data.title).trim();
      const currentBody = text(node.data.body).trim();
      const nextBody = currentTitle && currentTitle !== runDisplayTitle
        ? [currentTitle, currentBody || body].filter(Boolean).join("\n\n")
        : currentBody || body;
      if (node.data.runTitle === runDisplayTitle && node.data.title === runDisplayTitle && node.data.body === nextBody) return node;
      return {
        ...node,
        data: {
          ...node.data,
          title: runDisplayTitle,
          body: nextBody,
          runTitle: runDisplayTitle,
          status: node.data.status || candidate.run?.status || "",
          runKind: node.data.runKind || candidate.run?.kind || "",
          keyMetrics: node.data.keyMetrics || candidate.key_metrics || "",
          evidenceLevel: node.data.evidenceLevel || candidate.evidence_level || ""
        }
      };
    },
    [candidateByRunId]
  );

  useEffect(() => {
    if (!candidateByRunId.size) return;
    if (dirtyRef.current) return;
    setNodes((current) => current.map(hydrateRunNodeFromCandidate));
  }, [candidateByRunId, hydrateRunNodeFromCandidate, setNodes]);

  useEffect(() => {
    if (!detail.data) return;
    const detailKey = evidenceDetailApplyKey(detail.data);
    const previous = appliedDetailRef.current;
    const chainChanged = previous?.chainId !== detail.data.id;
    if (!chainChanged && dirtyRef.current) return;
    if (!chainChanged && previous?.key === detailKey) return;
    const rawNodes = (detail.data.nodes || []).map(apiNodeToFlowNode).map(hydrateRunNodeFromCandidate);
    const rawEdges = (detail.data.edges || []).map(apiEdgeToFlowEdge);
    const nextNodes = prepareLoadedEvidenceGraph(rawNodes, rawEdges).map(withNodeHandlers);
    // Thread/list/focus views do not need obstacle-aware canvas routing. Keep
    // loading cheap and defer the expensive route pass until overview is
    // explicitly opened.
    const nextEdges = rawEdges.map(withEdgeHandlers);
    const shouldFitLoadedGraph = chainChanged || (previous?.nodeCount === 0 && nextNodes.length > 0);
    appliedDetailRef.current = { chainId: detail.data.id, key: detailKey, nodeCount: nextNodes.length };
    setNodes(nextNodes);
    setEdges(nextEdges);
    setChainTitleDraft(detail.data.title || "");
    setChainDescriptionDraft(detail.data.description || "");
    const recipes = detail.data.routing_hints?.recipes || [];
    const keywords = detail.data.routing_hints?.keywords || [];
    setRoutingRecipesDraft(recipes.join(", "));
    setRoutingKeywordsDraft(keywords.join(", "));
    lastSavedMeta.current = {
      id: detail.data.id,
      title: detail.data.title || "",
      description: detail.data.description || "",
      routingHintsKey: routingHintsKey(recipes, keywords)
    };
    setSelected(null);
    dirtyRef.current = false;
    setDirty(false);
    setSaveState("saved");
    if (shouldFitLoadedGraph && nextNodes.length > 0) {
      window.requestAnimationFrame(() => {
        window.requestAnimationFrame(() => {
          void flow.fitView({
            nodes: nextNodes.map((node) => ({ id: node.id })),
            padding: 0.22,
            duration: reduceMotion ? 0 : 240,
            maxZoom: 1
          });
        });
      });
    }
  }, [detail.data, flow, hydrateRunNodeFromCandidate, reduceMotion, setEdges, setNodes, withEdgeHandlers, withNodeHandlers]);

  const hasProjectPrimary = Boolean(
    primaryMap.data?.id
    || orderedChains.some((chain) => chain.role === "primary" && chain.status === "active")
  );
  const createChain = useMutation({
    mutationFn: () => projectId
      ? hasProjectPrimary
        ? createProjectEvidenceMap(token, projectId, {
          title: t("newTopicEvidenceGraph"),
          description: "",
          role: "secondary",
          status: "active"
        })
        : ensureProjectEvidenceMap(token, projectId)
      : createEvidenceChain(token, {
        title: t("newEvidenceChain"),
        description: "",
        role: "secondary",
        status: "active"
      }),
    onSuccess: async (chain) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["evidence-chains"] }),
        queryClient.invalidateQueries({ queryKey: ["project-evidence-map", token, projectId] })
      ]);
      selectChain(chain.id);
      if (chain.role !== "primary") setRoutingOpen(true);
    }
  });
  const renameChain = useMutation({
    mutationFn: (payload: { id: string; title: string; description: string; recipes: string[]; keywords: string[] }) => updateEvidenceChain(token, payload.id, {
      title: payload.title,
      description: payload.description,
      routing_hints: { recipes: payload.recipes, keywords: payload.keywords }
    }),
    onSuccess: async (chain) => {
      const recipes = chain.routing_hints?.recipes || [];
      const keywords = chain.routing_hints?.keywords || [];
      lastSavedMeta.current = {
        id: chain.id,
        title: chain.title || "",
        description: chain.description || "",
        routingHintsKey: routingHintsKey(recipes, keywords)
      };
      await queryClient.invalidateQueries({ queryKey: ["evidence-chains"] });
    }
  });
  const archiveChain = useMutation({
    mutationFn: (chain: EvidenceChainDetail) => updateEvidenceChain(token, chain.id, {
      title: chain.title,
      description: chain.description,
      project_id: chain.project_id,
      role: "archive",
      status: "archived"
    }),
    onSuccess: async (chain) => {
      queryClient.setQueryData(["evidence-chain", token, chain.id], (current: EvidenceChainDetail | undefined) => current ? { ...current, ...chain } : current);
      await queryClient.invalidateQueries({ queryKey: ["evidence-chains"] });
    }
  });
  const purgeChain = useMutation({
    mutationFn: (chain: EvidenceChainDetail) => deleteEvidenceChain(token, chain.id, true),
    onSuccess: async (_result, chain) => {
      queryClient.removeQueries({ queryKey: ["evidence-chain", token, chain.id] });
      appliedDetailRef.current = null;
      setNodes([]);
      setEdges([]);
      setSelected(null);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["evidence-chains"] }),
        queryClient.invalidateQueries({ queryKey: ["project-evidence-map", token, projectId] })
      ]);
      const nextID = primaryMap.data?.id
        || orderedChains.find((item) => item.id !== chain.id && item.role === "primary" && item.status === "active")?.id
        || orderedChains.find((item) => item.id !== chain.id && item.status === "active")?.id
        || "";
      selectChain(nextID, true);
    },
    onError: (error) => {
      const message = error instanceof ApiError
        ? `${error.message}${error.details ? `：${error.details}` : ""}`
        : error instanceof Error ? error.message : "专题图删除失败";
      window.alert(message);
    }
  });

  const onNodesChange = useCallback(
    (changes: Parameters<typeof onNodesChangeBase>[0]) => {
      const acceptedIDs = new Set(nodes.map((node) => node.id));
      const acceptedChanges = changes.filter((change) => "id" in change && acceptedIDs.has(change.id));
      if (!acceptedChanges.length) return;
      onNodesChangeBase(acceptedChanges);
      if (acceptedChanges.some((change) => change.type === "remove" && selected?.kind === "node" && change.id === selected.id)) {
        setSelected(null);
      }
      // React Flow emits internal "dimensions" and transient "position"
      // changes while measuring/rendering. Explicit resize and drag-stop
      // handlers already persist real user changes, so only removal is dirty
      // here; otherwise an idle board continuously autosaves itself.
      if (acceptedChanges.some((change) => change.type === "remove")) markDirty();
    },
    [markDirty, nodes, onNodesChangeBase, selected]
  );
  const onEdgesChange = useCallback(
    (changes: Parameters<typeof onEdgesChangeBase>[0]) => {
      const acceptedIDs = new Set(edges.map((edge) => edge.id));
      const acceptedChanges = changes.filter((change) => "id" in change && acceptedIDs.has(change.id));
      if (!acceptedChanges.length) return;
      onEdgesChangeBase(acceptedChanges);
      if (acceptedChanges.some((change) => change.type === "remove" && selected?.kind === "edge" && change.id === selected.id)) {
        setSelected(null);
      }
      // Connections are marked dirty by onConnect; only removal originates
      // exclusively from this generic change callback.
      if (acceptedChanges.some((change) => change.type === "remove")) markDirty();
    },
    [edges, markDirty, onEdgesChangeBase, selected]
  );

  const saveNow = useCallback(async () => {
    if (!selectedChainId) return;
    if (detail.data?.status === "archived") return;
    setSaveState("saving");
    try {
      const serialized = serializeEvidenceGraph(nodes, edges);
      const semanticPatch = buildEvidenceEditPatch(selectedChainId, {
        nodes: detail.data?.nodes || [],
        edges: detail.data?.edges || []
      }, serialized);
      if (semanticPatch) {
        if (!projectId) throw new Error("Evidence semantic edits require a Project-scoped Map");
        const proposal = await createProjectEvidenceProposal(token, projectId, {
          target_map_id: selectedChainId,
          actor: "ui",
          summary: `Review semantic edits to ${detail.data?.title || selectedChainId}`,
          routing_reason: detail.data?.role === "primary"
            ? "User-authored project-level semantic edit."
            : "User-authored semantic edit for this focused Topic.",
          project_level_impact: detail.data?.role === "primary",
          patch: semanticPatch
        });
        dirtyRef.current = false;
        setDirty(false);
        setSaveState("saved");
        setProposalNotice(`语义修改已保存为待审核草稿 ${proposal.id}`);
        appliedDetailRef.current = null;
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ["evidence-chain", token, selectedChainId] }),
          queryClient.invalidateQueries({ queryKey: ["project-evidence-proposals", token, projectId] }),
          queryClient.invalidateQueries({ queryKey: ["evidence-chains"] })
        ]);
        return;
      }
      const saved = await saveEvidenceChainGraph(token, selectedChainId, serialized, detail.data?.revision || 0);
      queryClient.setQueryData(["evidence-chain", token, selectedChainId], saved);
      dirtyRef.current = false;
      setDirty(false);
      setSaveState("saved");
      await queryClient.invalidateQueries({ queryKey: ["evidence-chains"] });
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        setSaveState("conflict");
        await queryClient.invalidateQueries({ queryKey: ["evidence-chain", token, selectedChainId] });
      } else {
        setSaveState("failed");
      }
    }
  }, [detail.data?.edges, detail.data?.nodes, detail.data?.revision, detail.data?.role, detail.data?.status, detail.data?.title, edges, nodes, projectId, queryClient, selectedChainId, token]);

  useEffect(() => {
    if (!dirty || !selectedChainId) return;
    const timer = window.setTimeout(() => void saveNow(), 800);
    return () => window.clearTimeout(timer);
  }, [dirty, saveNow, selectedChainId]);

  useEffect(() => {
    if (!detail.data) return;
    if (chainMetaComposing) return;
    const title = chainTitleDraft.trim();
    const description = chainDescriptionDraft.trim();
    const recipes = parseRoutingTokens(routingRecipesDraft);
    const keywords = parseRoutingTokens(routingKeywordsDraft);
    const hintsKey = routingHintsKey(recipes, keywords);
    const saved = lastSavedMeta.current;
    if (!title) return;
    if (saved?.id === detail.data.id && title === saved.title && description === saved.description && hintsKey === saved.routingHintsKey) return;
    const timer = window.setTimeout(() => {
      renameChain.mutate({ id: detail.data!.id, title, description, recipes, keywords });
    }, 800);
    return () => window.clearTimeout(timer);
  }, [chainDescriptionDraft, chainMetaComposing, chainTitleDraft, detail.data, renameChain, routingKeywordsDraft, routingRecipesDraft]);

  const filteredCandidates = useMemo(() => scopedCandidates.filter((candidate) => candidateMatches(candidate, candidateQuery)), [scopedCandidates, candidateQuery]);
  const candidateGroups = useMemo(() => groupRunCandidatesByProject(filteredCandidates, { unassignedRuns: t("unassignedRuns"), runsWithoutProjectCards: t("runsWithoutProjectCards") }), [filteredCandidates, t]);
  const activeCandidateGroup = useMemo(() => candidateGroups.find((group) => group.key === selectedCandidateGroup) || candidateGroups[0], [candidateGroups, selectedCandidateGroup]);

  useEffect(() => {
    if (!runTrayOpen) return;
    if (!candidateGroups.length) {
      setSelectedCandidateGroup("");
      return;
    }
    if (!selectedCandidateGroup || !candidateGroups.some((group) => group.key === selectedCandidateGroup)) {
      setSelectedCandidateGroup(candidateGroups[0].key);
    }
  }, [candidateGroups, runTrayOpen, selectedCandidateGroup]);

  const addTextNode = (type: EvidenceNodeType) => {
    const node = withNodeHandlers(createTextNode(type, flow.screenToFlowPosition({ x: window.innerWidth / 2, y: window.innerHeight / 2 })));
    setNodes((current) => {
      if (type !== "group") return [...current, node];
      const selectedMemberIDs = new Set(current
        .filter((item) => item.selected && isProtocolGroupMemberType(item.data.type) && !item.data.draft)
        .map((item) => item.id));
      return [
        ...current.map((item) => selectedMemberIDs.has(item.id)
          ? { ...item, data: { ...item.data, groupId: node.id } }
          : item),
        node
      ];
    });
    setSelected({ kind: "node", id: node.id });
    setNodeMenuOpen(false);
    markDirty();
  };

  const arrangeGraph = useCallback((resetPins = false) => {
    const arranged = layoutEvidenceGraph(nodes, edges, resetPins).map(withNodeHandlers);
    setNodes(arranged);
    markDirty();
    window.requestAnimationFrame(() => {
      window.requestAnimationFrame(() => {
        void flow.fitView({
          nodes: arranged.map((node) => ({ id: node.id })),
          padding: 0.18,
          duration: reduceMotion ? 0 : 240,
          maxZoom: 1
        });
      });
    });
  }, [edges, flow, markDirty, nodes, reduceMotion, setNodes, withNodeHandlers]);

  const nodeTypes = useMemo(() => ({ evidence: EvidenceNode }), []);
  const edgeTypes = useMemo(() => ({ evidence: EvidenceEdge }), []);
  const canvasBase = useMemo(() => {
    if (!activeCanvasPreview) return { nodes, edges };
    const mergedEdges = [...edges, ...activeCanvasPreview.preview.edges];
    const acceptedPinned = nodes.map((node) => ({ ...node, data: { ...node.data, pinned: true } }));
    const mergedNodes = [...nodes, ...activeCanvasPreview.preview.nodes];
    const previewNodeIDs = new Map(activeCanvasPreview.preview.nodes.flatMap((node) => (
      node.data.sourceNodeId ? [[node.data.sourceNodeId, node.id] as const] : []
    )));
    const previewIntent = layoutIntentPreview
      ? remapEvidenceLayoutIntent(activeCanvasPreview.preview.layoutIntent, previewNodeIDs)
      : undefined;
    const laidOut = previewIntent
      ? layoutEvidenceGraphFromIntent(mergedNodes, previewIntent)
      : layoutEvidenceGraph([...acceptedPinned, ...activeCanvasPreview.preview.nodes], mergedEdges);
    const draftNodes = laidOut.filter((node) => node.data.draft === true).map(withNodeHandlers);
    if (!previewIntent) return { nodes: [...nodes, ...draftNodes], edges: mergedEdges };
    const draftIDs = new Set(draftNodes.map((node) => node.id));
    return {
      nodes: laidOut.map((node) => draftIDs.has(node.id) ? withNodeHandlers(node) : node),
      edges: mergedEdges
    };
  }, [activeCanvasPreview, edges, layoutIntentPreview, nodes, withNodeHandlers]);
  const groupProjection = useMemo(
    () => projectEvidenceGroups(canvasBase.nodes, canvasBase.edges),
    [canvasBase]
  );
  // Protocol containers are a legacy storage shape, not a current research
  // concept. Keep their membership in the persisted graph for compatibility,
  // but render the member nodes directly instead of reviving the old frame UI.
  const groupFrames = useMemo<Array<{
    id: string;
    descriptor: EvidenceGroupDescriptor;
    bounds: EvidenceGroupFrameBounds;
  }>>(() => [], []);
  const beginProtocolContainerMove = useCallback((groupId: string, client: { x: number; y: number }) => {
    if (detail.data?.status === "archived") return;
    const group = nodes.find((node) => node.id === groupId && node.data.type === "group");
    if (!group || group.data.draft) return;
    const baseline = nodes.map((node) => ({
      ...node,
      selected: node.id === groupId,
      position: { ...node.position },
      data: { ...node.data }
    }));
    protocolContainerDragRef.current = {
      id: groupId,
      start: flow.screenToFlowPosition(client),
      latest: { x: 0, y: 0 },
      baseline,
      wasDirty: dirtyRef.current,
      moved: false
    };
    dirtyRef.current = true;
    setMovingProtocolContainerId(groupId);
    setSelected({ kind: "node", id: groupId });
    setEdges((current) => current.map((edge) => ({ ...edge, selected: false })));
    setNodes(baseline);
  }, [detail.data?.status, flow, nodes, setEdges, setNodes]);
  const moveProtocolContainer = useCallback((client: { x: number; y: number }) => {
    const drag = protocolContainerDragRef.current;
    if (!drag) return;
    const current = flow.screenToFlowPosition(client);
    drag.latest = {
      x: current.x - drag.start.x,
      y: current.y - drag.start.y
    };
    drag.moved = drag.moved || Math.hypot(drag.latest.x, drag.latest.y) >= 3;
    if (protocolContainerDragFrameRef.current !== null) return;
    protocolContainerDragFrameRef.current = window.requestAnimationFrame(() => {
      protocolContainerDragFrameRef.current = null;
      const active = protocolContainerDragRef.current;
      if (!active) return;
      setNodes(translateProtocolContainer(active.baseline, active.id, active.latest));
    });
  }, [flow, setNodes]);
  const endProtocolContainerMove = useCallback(() => {
    const drag = protocolContainerDragRef.current;
    if (!drag) return;
    if (protocolContainerDragFrameRef.current !== null) {
      window.cancelAnimationFrame(protocolContainerDragFrameRef.current);
      protocolContainerDragFrameRef.current = null;
    }
    setNodes(drag.moved
      ? translateProtocolContainer(drag.baseline, drag.id, drag.latest)
      : drag.baseline);
    protocolContainerDragRef.current = null;
    setMovingProtocolContainerId("");
    if (drag.moved) markDirty();
    else dirtyRef.current = drag.wasDirty;
  }, [markDirty, setNodes]);
  const moveProtocolContainerBy = useCallback((groupId: string, delta: { x: number; y: number }) => {
    if (detail.data?.status === "archived") return;
    setNodes((current) => translateProtocolContainer(current, groupId, delta));
    markDirty();
  }, [detail.data?.status, markDirty, setNodes]);
  useEffect(() => () => {
    if (protocolContainerDragFrameRef.current !== null) {
      window.cancelAnimationFrame(protocolContainerDragFrameRef.current);
      protocolContainerDragFrameRef.current = null;
    }
    protocolContainerDragRef.current = null;
  }, []);
  const visibleNodes = groupProjection.nodes;
  const visibleEdges = groupProjection.edges;
  const routingGeometryKey = evidenceRoutingGeometryKey(visibleNodes);
  const routingTopologyKey = evidenceRoutingTopologyKey(visibleEdges);
  useEffect(() => {
    if (routingTimerRef.current !== null) {
      window.clearTimeout(routingTimerRef.current);
      routingTimerRef.current = null;
    }
    if (workspaceView !== "overview" || !visibleNodes.length || !visibleEdges.length) return;
    routingTimerRef.current = window.setTimeout(() => {
      routingTimerRef.current = null;
      setEdges((current) => routeEvidenceGraphEdges(current, visibleNodes));
    }, 90);
    return () => {
      if (routingTimerRef.current !== null) {
        window.clearTimeout(routingTimerRef.current);
        routingTimerRef.current = null;
      }
    };
  }, [routingGeometryKey, routingTopologyKey, setEdges, workspaceView]);
  const selectedNode = useMemo(
    () => selected?.kind === "node" ? nodes.find((node) => node.id === selected.id) || null : null,
    [nodes, selected]
  );
  const selectedProtocolMigration = useMemo(
    () => selectedNode?.data.type === "protocol"
      ? inspectProtocolFrameMigration(nodes, edges, selectedNode.id)
      : null,
    [edges, nodes, selectedNode]
  );
  const selectedGroupHasMembers = selectedNode?.data.type === "group"
    && nodes.some((node) => node.data.groupId === selectedNode.id);
  const selectedEdge = useMemo(
    () => selected?.kind === "edge" ? edges.find((edge) => edge.id === selected.id) || null : null,
    [edges, selected]
  );
  const selectedEdgeSource = useMemo(
    () => selectedEdge ? nodes.find((node) => node.id === selectedEdge.source) || null : null,
    [nodes, selectedEdge]
  );
  const selectedEdgeTarget = useMemo(
    () => selectedEdge ? nodes.find((node) => node.id === selectedEdge.target) || null : null,
    [nodes, selectedEdge]
  );
  const researchProjection = useMemo(() => {
    const shared = researchThreads.data;
    if (!shared || shared.revision !== (detail.data?.revision || 0)) return null;
    return apiResearchProjectionToFlow(shared);
  }, [detail.data?.revision, researchThreads.data]);
  const readerSections = useMemo(() => evidenceReadingSections(nodes, readerQuery), [nodes, readerQuery]);
  const focusedNeighborhood = useMemo(
    () => evidenceNeighborhood(nodes, edges, focusNodeID, focusDepth),
    [edges, focusDepth, focusNodeID, nodes]
  );

  const inspectNode = useCallback((nodeID: string) => {
    setSelected({ kind: "node", id: nodeID });
    setNodes((current) => current.map((node) => ({ ...node, selected: node.id === nodeID })));
    setEdges((current) => current.map((edge) => ({ ...edge, selected: false })));
    setRunTrayOpen(false);
    setPreviewRunID("");
  }, [setEdges, setNodes]);

  const openNodeFocus = useCallback((nodeID: string) => {
    setFocusNodeID(nodeID);
    inspectNode(nodeID);
    setWorkspaceView("focus");
  }, [inspectNode]);

  const openProposalCanvasPreview = useCallback(() => {
    if (!activePreviewEntry || activePreviewEntry.preview.chainId !== selectedChainId) return;
    if (workspaceView !== "overview") proposalReturnViewRef.current = workspaceView;
    setProposalCanvasPreview(true);
    setWorkspaceView("overview");
  }, [activePreviewEntry, selectedChainId, workspaceView]);

  const closeProposalCanvasPreview = useCallback(() => {
    setProposalCanvasPreview(false);
    setWorkspaceView(proposalReturnViewRef.current);
  }, []);

  useEffect(() => {
    if (!activeCanvasPreview) return;
    const sourceToPreview = new Map(activeCanvasPreview.preview.nodes.flatMap((node) => (
      node.data.sourceNodeId ? [[node.data.sourceNodeId, node.id] as const] : []
    )));
    const previewNodeIDs = activeCanvasPreview.preview.layoutIntent?.ranks
      .flat()
      .map((id) => sourceToPreview.get(id) || id)
      || activeCanvasPreview.preview.nodes.map((node) => node.id);
    const timer = window.setTimeout(() => {
      void flow.fitView({
        nodes: previewNodeIDs.map((id) => ({ id })),
        padding: 0.32,
        duration: reduceMotion ? 0 : 260,
        maxZoom: 1
      });
    }, reduceMotion ? 0 : 80);
    return () => window.clearTimeout(timer);
  }, [activeCanvasPreview, flow, reduceMotion]);

  const applyAcceptedLayoutIntent = useCallback(async (chainID: string, intent: EvidenceLayoutIntent) => {
    const accepted = await getEvidenceChain(token, chainID);
    const acceptedNodes = (accepted.nodes || []).map(apiNodeToFlowNode);
    const acceptedEdges = (accepted.edges || []).map(apiEdgeToFlowEdge);
    const arranged = layoutEvidenceGraphFromIntent(acceptedNodes, intent);
    return saveEvidenceChainGraph(
      token,
      chainID,
      serializeEvidenceGraph(arranged, acceptedEdges),
      accepted.revision || 0
    );
  }, [token]);

  const reviewProposal = useCallback(async (entry: EvidencePreviewEntry, action: "accept" | "reject") => {
    setReviewingRunID(entry.key);
    setProposalNotice("");
    let targetLabel = "";
    let targetChainID = selectedChainId;
    try {
      if (action === "accept") {
        const plan = entry.kind === "workspace"
          ? await planEvidenceProposal(token, entry.key)
          : await planEvidenceGraphProposal(token, entry.key);
        if (!plan.eligible) {
          setProposalNotice(plan.blockers.map((blocker) => `${blocker.code}: ${blocker.message}`).join("\n"));
          return;
        }
        const target = orderedChains.find((chain) => chain.id === plan.chain_id);
        targetChainID = plan.chain_id || selectedChainId;
        targetLabel = target?.title || plan.chain_id;
        if (plan.chain_id && selectedChainId && plan.chain_id !== selectedChainId) {
          if (!window.confirm(`${t("proposalTargetMismatch")}：${targetLabel}\n\n${t("confirmProposalTarget")}`)) return;
        }
      }
      if (entry.kind === "workspace") {
        await reviewEvidenceProposal(token, entry.key, action);
      } else {
        await reviewEvidenceGraphProposal(token, entry.key, action);
      }
      setPreviewRunID("");
      if (action === "accept") {
        if (entry.preview.layoutIntent) {
          try {
            await applyAcceptedLayoutIntent(targetChainID, entry.preview.layoutIntent);
            setProposalNotice(`${t("proposalAcceptedInto")}：${targetLabel}；Agent 编排已应用。`);
          } catch (layoutError) {
            setProposalNotice(`语义已接受，但 Agent 编排未保存：${layoutError instanceof Error ? layoutError.message : String(layoutError)}`);
          }
        } else {
          setProposalNotice(`${t("proposalAcceptedInto")}：${targetLabel}`);
        }
      } else {
        setProposalNotice("建议已拒绝，证据图未改变。");
      }
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["evidence-run-candidates"] }),
        queryClient.invalidateQueries({ queryKey: ["project-evidence-proposals"] }),
        queryClient.invalidateQueries({ queryKey: ["projects"] }),
        queryClient.invalidateQueries({ queryKey: ["evidence-chains"] }),
        targetChainID ? queryClient.invalidateQueries({ queryKey: ["evidence-chain", token, targetChainID] }) : Promise.resolve()
      ]);
    } catch (error) {
      setProposalNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setReviewingRunID("");
    }
  }, [applyAcceptedLayoutIntent, orderedChains, queryClient, selectedChainId, t, token]);

  const promoteSelectedNode = useCallback(async () => {
    if (!selectedChainId || selected?.kind !== "node" || detail.data?.role === "primary") return;
    const sourceNode = nodes.find((node) => node.id === selected.id && !node.data.draft);
    if (!sourceNode) return;
    const defaultSummary = sourceNode.data.title || "项目级研究结论";
    const summary = window.prompt("写入主图的简短摘要（Topic 细节不会被复制）：", defaultSummary)?.trim();
    if (!summary) return;
    const nodeType = sourceNode.data.type === "issue" || sourceNode.data.type === "plan" ? sourceNode.data.type : "claim";
    setPromoting(true);
    setProposalNotice("");
    try {
      const request: { source_node_ids: string[]; summary: string; node_type: "claim" | "issue" | "plan"; actor: string } = {
        source_node_ids: [sourceNode.id], summary, node_type: nodeType, actor: "ui"
      };
      const plan = await planEvidencePromotion(token, selectedChainId, request);
      if (!plan.eligible) {
        setProposalNotice(plan.blockers.map((blocker) => `${blocker.code}: ${blocker.message}`).join("\n"));
        return;
      }
      const proposal = await createEvidencePromotion(token, selectedChainId, {
        ...request,
        expected_plan_hash: plan.plan_hash
      });
      await queryClient.invalidateQueries({ queryKey: ["project-evidence-proposals"] });
      selectChain(plan.target_primary_map_id);
      setPreviewRunID(proposal.id);
      setProposalNotice("已生成主图晋升草稿；Topic 保持不变，请在主图审核后再接受。");
      clearSelection();
    } catch (error) {
      setProposalNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setPromoting(false);
    }
  }, [clearSelection, detail.data?.role, nodes, queryClient, selectChain, selected, selectedChainId, token]);

  const onConnect = useCallback(
    (connection: Connection) => {
      if (!nodes.some((node) => node.id === connection.source) || !nodes.some((node) => node.id === connection.target)) return;
      const id = `edge_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 7)}`;
      const sourceType = nodes.find((node) => node.id === connection.source)?.data.type || "note";
      const targetType = nodes.find((node) => node.id === connection.target)?.data.type || "note";
      if (sourceType === "group" || targetType === "group") return;
      const type: EvidenceEdgeType = defaultEvidenceRelation(sourceType, targetType);
      const hasManualHandles = Boolean(connection.sourceHandle && connection.targetHandle);
      setEdges((current) =>
        routeEvidenceGraphEdges(
          addEdge(
            {
              ...connection,
              id,
              type: "evidence",
              label: edgeTypeLabel(type),
              data: { type, rationale: "", autoHandles: !hasManualHandles, onSelectEdge: selectEdge, onUpdateEdge: updateEdgeData },
              animated: false,
              markerEnd: evidenceMarkerEnd(type),
              style: edgeStyle(type)
            },
            current
          ),
          nodes
        )
      );
      setSelected({ kind: "edge", id });
      markDirty();
    },
    [markDirty, nodes, selectEdge, setEdges, updateEdgeData]
  );

  const deleteSelection = useCallback(() => {
    if (!selected || detail.data?.status === "archived") return;
    if (selected.kind === "node") {
      const nodeId = selected.id;
      const selectedGroup = nodes.find((node) => node.id === nodeId && node.data.type === "group");
      if (selectedGroup && nodes.some((node) => node.data.groupId === nodeId)) return;
      setNodes((current) => current
        .filter((node) => node.id !== nodeId)
        .map((node) => node.data.groupId === nodeId
          ? { ...node, data: { ...node.data, groupId: undefined } }
          : node));
      setEdges((current) => current.filter((edge) => edge.source !== nodeId && edge.target !== nodeId));
    } else {
      const edgeId = selected.id;
      setEdges((current) => current.filter((edge) => edge.id !== edgeId));
    }
    setSelected(null);
    markDirty();
  }, [detail.data?.status, markDirty, nodes, selected, setEdges, setNodes]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (workspaceView !== "overview" || !selected || detail.data?.status === "archived" || (event.key !== "Delete" && event.key !== "Backspace")) return;
      if (isEditableTarget(event.target)) return;
      event.preventDefault();
      deleteSelection();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [deleteSelection, detail.data?.status, selected, workspaceView]);

  useEffect(() => {
    if (workspaceView !== "focus") return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || isEditableTarget(event.target)) return;
      clearSelection();
      setWorkspaceView("threads");
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [clearSelection, workspaceView]);

  const onNodeClick: NodeMouseHandler<EvidenceFlowNode> = (_event, node) => {
    if (node.data.draft && node.data.proposalRunId) {
      setPreviewRunID(node.data.proposalRunId);
      setRunTrayOpen(false);
      clearSelection();
      return;
    }
    setFocusNodeID(node.id);
    setSelected({ kind: "node", id: node.id });
    setNodes((current) => current.map((row) => ({ ...row, selected: row.id === node.id })));
    setEdges((current) => current.map((edge) => ({ ...edge, selected: false })));
  };
  const onEdgeClick: EdgeMouseHandler<EvidenceFlowEdge> = (_event, edge) => {
    if (edge.data?.draft && edge.data.proposalRunId) {
      setPreviewRunID(edge.data.proposalRunId);
      setRunTrayOpen(false);
      clearSelection();
      return;
    }
    selectEdge(edge.id);
  };
  const onNodeDoubleClick: NodeMouseHandler<EvidenceFlowNode> = (_event, node) => {
    if (node.data.type === "run" && node.data.runId) onOpenRun(node.data.runId);
  };

  const onDrop = (event: React.DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    const raw = event.dataTransfer.getData(candidateMime);
    if (!raw) return;
    const candidate = JSON.parse(raw) as EvidenceChainRunCandidate;
    const position = flow.screenToFlowPosition({ x: event.clientX, y: event.clientY });
    const node = withNodeHandlers(candidateToNode(candidate, position));
    setNodes((current) => [...current, node]);
    setSelected({ kind: "node", id: node.id });
    markDirty();
  };

  const primaryMapAbsent = primaryMap.error instanceof ApiError
    && primaryMap.error.status === 404
    && primaryMap.error.code === "PRIMARY_MAP_NOT_FOUND";
  const mapDiscoveryPending = Boolean(projectId && !selectedChainId && (
    chains.isPending
    || chains.isFetching
    || primaryMap.isPending
    || primaryMap.isFetching
    || primaryMap.isSuccess
    || orderedChains.length > 0
  ));
  const mapDiscoveryError = chains.error || (!primaryMapAbsent ? primaryMap.error : null);
  const projectHasNoEvidenceMap = Boolean(
    projectId
    && !selectedChainId
    && !mapDiscoveryPending
    && !mapDiscoveryError
    && chains.isSuccess
    && chains.data.length === 0
    && primaryMapAbsent
  );

  return (
    <div className={`evidence-shell ${leftOpen ? "" : "left-collapsed"}`}>
      <main className="evidence-canvas-wrap">
        <div className="evidence-toolbar">
          <div className="evidence-toolbar-left">
            {projectId && selectedChainId ? (
              <div className="project-evidence-picker">
                <label title={t("chooseEvidenceGraph")}>
                  <Network size={14} />
                  <select aria-label={t("chooseEvidenceGraph")} value={selectedChainId} onChange={(event) => selectChain(event.target.value)}>
                    {orderedChains.map((chain) => (
                      <option key={chain.id} value={chain.id}>
                        {chain.role === "primary" ? `${t("primaryEvidenceGraph")} · ` : chain.status === "archived" ? `${t("archivedEvidenceGraph")} · ` : `${t("topicEvidenceGraph")} · `}
                        {chain.title}
                      </option>
                    ))}
                  </select>
                </label>
                <button
                  className="icon-button"
                  type="button"
                  aria-label={t("newTopicEvidenceGraph")}
                  title={t("newTopicEvidenceGraph")}
                  disabled={createChain.isPending}
                  onClick={() => createChain.mutate()}
                >
                  <Plus size={14} />
                </button>
                <button
                  className={routingOpen ? "icon-button active-soft" : "icon-button"}
                  type="button"
                  aria-label={t("evidenceGraphRouting")}
                  aria-expanded={routingOpen}
                  title={t("evidenceGraphRouting")}
                  onClick={() => setRoutingOpen((open) => !open)}
                >
                  <Route size={14} />
                </button>
                <AnimatePresence>
                  {routingOpen && detail.data ? (
                    <motion.section
                      className="evidence-routing-popover"
                      initial={{ opacity: 0, y: -5, scale: 0.98 }}
                      animate={{ opacity: 1, y: 0, scale: 1 }}
                      exit={{ opacity: 0, y: -5, scale: 0.98 }}
                      transition={{ duration: 0.16 }}
                      aria-label={t("evidenceGraphRouting")}
                    >
                      <div className="evidence-routing-head">
                        <div>
                          <strong>{t("evidenceGraphRouting")}</strong>
                          <span>{detail.data.role === "primary" ? t("primaryRoutingExplanation") : t("topicRoutingExplanation")}</span>
                        </div>
                        <span className={`evidence-role-badge ${detail.data.role === "primary" ? "primary" : detail.data.status === "archived" ? "archived" : "secondary"}`}>
                          {detail.data.role === "primary" ? t("primaryEvidenceGraph") : detail.data.status === "archived" ? t("archivedEvidenceGraph") : t("topicEvidenceGraph")}
                        </span>
                      </div>
                      <label>
                        <span>{t("evidenceGraphPurpose")}</span>
                        <textarea
                          value={chainDescriptionDraft}
                          placeholder={t("evidenceGraphPurposePlaceholder")}
                          disabled={detail.data.status === "archived"}
                          onChange={(event) => setChainDescriptionDraft(event.target.value)}
                          onCompositionStart={() => setChainMetaComposing(true)}
                          onCompositionEnd={(event) => {
                            setChainMetaComposing(false);
                            setChainDescriptionDraft(event.currentTarget.value);
                          }}
                        />
                      </label>
                      <div className="evidence-routing-grid">
                        <label>
                          <span>{t("evidenceGraphRecipes")}</span>
                          <input
                            value={routingRecipesDraft}
                            placeholder={t("evidenceGraphRecipesPlaceholder")}
                            disabled={detail.data.status === "archived"}
                            onChange={(event) => setRoutingRecipesDraft(event.target.value)}
                          />
                        </label>
                        <label>
                          <span>{t("evidenceGraphKeywords")}</span>
                          <input
                            value={routingKeywordsDraft}
                            placeholder={t("evidenceGraphKeywordsPlaceholder")}
                            disabled={detail.data.status === "archived"}
                            onChange={(event) => setRoutingKeywordsDraft(event.target.value)}
                          />
                        </label>
                      </div>
                      {detail.data.role !== "primary" && !chainDescriptionDraft.trim() && !routingRecipesDraft.trim() && !routingKeywordsDraft.trim() ? (
                        <p className="evidence-routing-warning">{t("routingNotConfigured")}</p>
                      ) : null}
                      <div className="evidence-routing-id">
                        <span>{t("evidenceGraphIdentity")}</span>
                        <code>{detail.data.id}</code>
                        <button
                          type="button"
                          aria-label={t("copyGraphId")}
                          title={t("copyGraphId")}
                          onClick={() => {
                            void navigator.clipboard.writeText(detail.data!.id).then(() => {
                              setGraphIdCopied(true);
                              window.setTimeout(() => setGraphIdCopied(false), 1200);
                            });
                          }}
                        >
                          {graphIdCopied ? <Check size={13} /> : <Copy size={13} />}
                        </button>
                      </div>
                    </motion.section>
                  ) : null}
                </AnimatePresence>
              </div>
            ) : null}
            <div className="evidence-title-edit">
              <input
                value={chainTitleDraft}
                aria-label="证据图标题"
                placeholder={t("evidenceChainTitlePlaceholder")}
                onChange={(event) => setChainTitleDraft(event.target.value)}
                onCompositionStart={() => setChainMetaComposing(true)}
                onCompositionEnd={(event) => {
                  setChainMetaComposing(false);
                  setChainTitleDraft(event.currentTarget.value);
                }}
              />
              {detail.data ? (
                <span className={`evidence-role-badge ${detail.data.role === "primary" ? "primary" : detail.data.status === "archived" ? "archived" : "secondary"}`}>
                  {detail.data.role === "primary" ? t("primaryEvidenceGraph") : detail.data.status === "archived" ? t("archivedEvidenceGraph") : t("topicEvidenceGraph")}
                </span>
              ) : null}
              <span aria-live="polite" className={`save-state ${saveState}`}>{saveState === "saving" ? t("saving") : saveState === "conflict" ? "图已更新，请重新载入" : saveState === "failed" ? t("saveFailed") : dirty ? t("unsaved") : t("saved")}</span>
            </div>
          </div>
          {selectedChainId ? (
            <div className="evidence-view-switch" role="tablist" aria-label="证据图阅读方式">
              <button
                type="button"
                role="tab"
                className={workspaceView === "threads" ? "active" : ""}
                aria-selected={workspaceView === "threads"}
                onClick={() => setWorkspaceView("threads")}
              >
                <GitBranch size={14} />线程
              </button>
              <button
                type="button"
                role="tab"
                className={workspaceView === "list" ? "active" : ""}
                aria-selected={workspaceView === "list"}
                onClick={() => setWorkspaceView("list")}
              >
                <List size={14} />清单
              </button>
              <button
                type="button"
                role="tab"
                className={workspaceView === "focus" ? "active" : ""}
                aria-selected={workspaceView === "focus"}
                disabled={!selectedNode && !focusedNeighborhood.center}
                title={selectedNode || focusedNeighborhood.center ? "查看当前节点的前因后果" : "先从研究线程选择一个节点"}
                onClick={() => selectedNode ? openNodeFocus(selectedNode.id) : setWorkspaceView("focus")}
              >
                <Focus size={14} />焦点
              </button>
              <button
                type="button"
                role="tab"
                className={workspaceView === "overview" ? "active" : ""}
                aria-selected={workspaceView === "overview"}
                onClick={() => setWorkspaceView("overview")}
              >
                <Network size={14} />全景
              </button>
            </div>
          ) : null}
        </div>
        {selectedChainId && workspaceView === "overview" ? (
          <motion.aside
            className="evidence-edit-rail"
            aria-label="证据图编辑工具"
            initial={{ opacity: 0, x: -8 }}
            animate={{ opacity: 1, x: 0 }}
            transition={{ duration: reduceMotion ? 0 : 0.16 }}
          >
            <div className="evidence-edit-rail-group">
            <div className="evidence-node-menu-wrap">
              <button
                ref={nodeMenuTriggerRef}
                className={nodeMenuOpen ? "evidence-edit-tool active" : "evidence-edit-tool"}
                type="button"
                aria-label="添加节点"
                aria-controls="evidence-node-menu"
                aria-expanded={nodeMenuOpen}
                data-label="节点"
                title="添加节点"
                disabled={detail.data?.status === "archived"}
                onClick={() => setNodeMenuOpen((open) => !open)}
              >
                <Plus size={14} />
              </button>
              {nodeMenuOpen ? (
                <div
                  ref={nodeMenuRef}
                  id="evidence-node-menu"
                  className="evidence-node-menu"
                  role="menu"
                  aria-label="选择节点类型"
                >
                  {evidenceAuthoringNodeTypes.map((type) => (
                    <button
                      key={type}
                      type="button"
                      role="menuitem"
                      disabled={detail.data?.status === "archived"}
                      onClick={() => addTextNode(type)}
                    >
                      {nodeTypeLabel(type)}
                    </button>
                  ))}
                </div>
              ) : null}
            </div>
            <button
              type="button"
              className={runTrayOpen ? "evidence-edit-tool active" : "evidence-edit-tool"}
              aria-label="打开实验候选"
              aria-controls="evidence-run-drawer"
              aria-expanded={runTrayOpen}
              data-label="实验候选"
              title="打开实验候选"
              onClick={() => {
                setRunTrayOpen((open) => {
                  const next = !open;
                  if (next) {
                    setPreviewRunID("");
                    clearSelection();
                  }
                  return next;
                });
              }}
            >
              <ListPlus size={14} />
            </button>
            </div>
            <div className="evidence-edit-rail-group">
            <button
              className="evidence-edit-tool"
              type="button"
              aria-label="整理整个画布"
              data-label="整理画布"
              disabled={detail.data?.status === "archived"}
              title="清除旧固定位置，按关系从左到右整理全部节点"
              onClick={() => arrangeGraph(true)}
            >
              <PinOff size={14} />
            </button>
            <button
              className="evidence-edit-tool"
              type="button"
              aria-label="保留固定节点并整理其余节点"
              data-label="保留固定"
              disabled={detail.data?.status === "archived"}
              title="保留手动拖动过的节点，只整理未固定节点"
              onClick={() => arrangeGraph(false)}
            >
              <RefreshCcw size={14} />
            </button>
            </div>
            {detail.data?.role !== "primary" && selected?.kind === "node" ? (
              <div className="evidence-edit-rail-group">
              <button
                className="evidence-edit-tool"
                type="button"
                aria-label={promoting ? "正在规划晋升主图" : "晋升主图"}
                data-label={promoting ? "规划中" : "晋升主图"}
                disabled={promoting || detail.data?.status === "archived"}
                title="只向主图提交摘要和 Topic 引用，仍需审核"
                onClick={() => void promoteSelectedNode()}
              >
                <ArrowUpRight size={14} />
              </button>
              </div>
            ) : null}
            <div className="evidence-edit-rail-group">
            <button
              className={dirty || saveState === "failed" ? "evidence-edit-tool save dirty" : "evidence-edit-tool save"}
              type="button"
              aria-label={saveState === "saving" ? "正在保存" : "保存证据图"}
              aria-busy={saveState === "saving"}
              data-label={saveState === "saving" ? "保存中" : "保存"}
              title={dirty ? "保存当前修改" : "当前内容已保存"}
              disabled={detail.data?.status === "archived" || saveState === "saving" || (!dirty && saveState !== "failed")}
              onClick={() => void saveNow()}
            >
              <Save size={14} />
            </button>
            {selected && detail.data?.status !== "archived" ? (
              <button
                className="evidence-edit-tool danger"
                type="button"
                aria-label={t("deleteElementTitle")}
                data-label="删除元素"
                title={selectedGroupHasMembers ? "协议容器包含成员，不能直接删除" : t("deleteElementTitle")}
                disabled={selectedGroupHasMembers}
                onClick={deleteSelection}
              >
                <Trash2 size={14} />
              </button>
            ) : null}
            </div>
            <div className="evidence-edit-rail-group evidence-edit-rail-group--danger">
            {detail.data?.role !== "primary" && detail.data?.status !== "archived" ? (
              <button
                className="evidence-edit-tool"
                type="button"
                aria-label="归档专题图"
                data-label="归档"
                title="归档为只读历史图"
                onClick={() => detail.data && window.confirm("归档后仍可查看，但不能继续修改。确定归档？") && archiveChain.mutate(detail.data)}
              >
                <Archive size={14} />
              </button>
            ) : null}
            {detail.data?.role !== "primary"
              && (detail.data?.status === "archived" || ((detail.data?.revision || 0) === 0 && nodes.length === 0 && edges.length === 0)) ? (
                <button
                  className="evidence-edit-tool danger"
                  type="button"
                  aria-label={purgeChain.isPending ? "正在永久删除" : "永久删除专题图"}
                  data-label={purgeChain.isPending ? "删除中" : "永久删除"}
                  disabled={purgeChain.isPending}
                  title="永久删除专题图、节点、关系和修订；主图及被引用专题图不能删除"
                  onClick={() => {
                    if (!detail.data) return;
                    const message = detail.data.status === "archived"
                      ? `永久删除专题图“${detail.data.title}”及其全部历史？此操作不可撤销。`
                      : `这是一张空专题图。永久删除“${detail.data.title}”？此操作不可撤销。`;
                    if (window.confirm(message)) purgeChain.mutate(detail.data);
                  }}
                >
                  <Trash2 size={14} />
                </button>
              ) : null}
            </div>
          </motion.aside>
        ) : null}
        {mapDiscoveryPending ? (
          <div className="evidence-primary-empty" role="status" aria-live="polite">
            <RefreshCcw size={18} className="spin" />
            <strong>正在打开证据图</strong>
            <span>正在确认这个项目的主图与专题图，请稍候。</span>
          </div>
        ) : mapDiscoveryError && projectId && !selectedChainId ? (
          <div className="evidence-primary-empty" role="alert">
            <AlertTriangle size={18} />
            <strong>证据图暂时加载失败</strong>
            <span>{mapDiscoveryError instanceof ApiError ? mapDiscoveryError.details || mapDiscoveryError.message : String(mapDiscoveryError)}</span>
            <button type="button" onClick={() => void Promise.all([chains.refetch(), primaryMap.refetch()])}>
              <RefreshCcw size={14} />
              重新加载
            </button>
          </div>
        ) : projectHasNoEvidenceMap ? (
          <div className="evidence-primary-empty">
            <strong>{t("noProjectEvidenceGraph")}</strong>
            <span>{t("primaryGraphAgentHint")}</span>
            <button className="primary" disabled={createChain.isPending} onClick={() => createChain.mutate()}>
              <Plus size={14} />
              {createChain.isPending ? t("creating") : t("createPrimaryEvidenceGraph")}
            </button>
          </div>
        ) : null}
        <div className={`evidence-canvas evidence-canvas--${workspaceView}`} onDragOver={(event) => event.preventDefault()} onDrop={workspaceView === "overview" ? onDrop : undefined}>
          {workspaceView === "overview" ? (
          <ReactFlow
            nodes={visibleNodes}
            edges={visibleEdges}
            nodeTypes={nodeTypes}
            edgeTypes={edgeTypes}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onNodeClick={onNodeClick}
            onEdgeClick={onEdgeClick}
            onNodeDoubleClick={onNodeDoubleClick}
            onNodeDragStart={(_event, node) => {
              if (!node.data.draft && detail.data?.status !== "archived") {
                // A detail request that started before the drag may still
                // resolve while React Flow is moving the node. Block the load
                // effect immediately; drag-stop will promote this to normal
                // dirty state and schedule the save.
                dirtyRef.current = true;
              }
              dragStartGroupFramesRef.current = groupFrames.map(({ id, bounds }) => ({ id, bounds }));
              const currentGroupID = typeof node.data.groupId === "string" ? node.data.groupId : "";
              const frame = currentGroupID
                ? dragStartGroupFramesRef.current.find((item) => item.id === currentGroupID)
                : undefined;
              draggingProtocolFrameRef.current = frame || null;
              setDraggingProtocolFrame(draggingProtocolFrameRef.current);
            }}
            onNodeDragStop={(_event, node) => {
              if (node.data.draft) {
                draggingProtocolFrameRef.current = null;
                setDraggingProtocolFrame(null);
                return;
              }
              if (node.data.type === "group") {
                setNodes((current) => current.map((item) => item.id === node.id
                  ? { ...item, position: node.position, data: { ...item.data, pinned: true } }
                  : item));
                draggingProtocolFrameRef.current = null;
                setDraggingProtocolFrame(null);
                markDirty();
                return;
              }
              const currentGroupID = typeof node.data.groupId === "string" ? node.data.groupId : "";
              if (currentGroupID) {
                setNodes((current) => current.map((item) => item.id === node.id
                  ? { ...item, position: node.position, data: { ...item.data, pinned: true, groupId: currentGroupID } }
                  : item));
                draggingProtocolFrameRef.current = null;
                setDraggingProtocolFrame(null);
                markDirty();
                return;
              }
              setNodes((current) => current.map((item) => item.id === node.id
                ? {
                  ...item,
                  position: node.position,
                  data: {
                    ...item.data,
                    pinned: true
                  }
                }
                : item));
              draggingProtocolFrameRef.current = null;
              setDraggingProtocolFrame(null);
              markDirty();
            }}
            onPaneClick={clearSelection}
            connectionMode={ConnectionMode.Loose}
            minZoom={0.12}
            nodesDraggable={detail.data?.status !== "archived" && !activeCanvasPreview}
            nodesConnectable={detail.data?.status !== "archived" && !activeCanvasPreview}
          >
            <Background gap={24} color="#dce2e8" />
            <ViewportPortal>
              {groupFrames.map(({ id, descriptor, bounds }) => {
                const memberIDs = new Set(descriptor.memberIds);
                const internalEdges = canvasBase.edges.filter((edge) => memberIDs.has(edge.source) && memberIDs.has(edge.target)).length;
                return (
                  <EvidenceGroupFrame
                    key={id}
                    descriptor={descriptor}
                    bounds={bounds}
                    internalEdgeCount={internalEdges}
                    onSelect={() => {
                      if (descriptor.group.data.draft && descriptor.group.data.proposalRunId) {
                        setPreviewRunID(descriptor.group.data.proposalRunId);
                        clearSelection();
                        return;
                      }
                      setSelected({ kind: "node", id });
                      setNodes((current) => current.map((node) => ({ ...node, selected: node.id === id })));
                      setEdges((current) => current.map((edge) => ({ ...edge, selected: false })));
                    }}
                    selected={selected?.kind === "node" && selected.id === id}
                    movable={detail.data?.status !== "archived" && !descriptor.group.data.draft}
                    moving={movingProtocolContainerId === id}
                    memberDragging={draggingProtocolFrame?.id === id}
                    onMoveStart={(client) => beginProtocolContainerMove(id, client)}
                    onMove={moveProtocolContainer}
                    onMoveEnd={endProtocolContainerMove}
                    onMoveBy={(delta) => moveProtocolContainerBy(id, delta)}
                  />
                );
              })}
            </ViewportPortal>
            <Controls />
            <MiniMap pannable zoomable />
          </ReactFlow>
          ) : workspaceView === "focus" ? (
            <EvidenceFocusView
              neighborhood={focusedNeighborhood}
              depth={focusDepth}
              selected={selected}
              onOpenNode={inspectNode}
              onFocusNode={openNodeFocus}
              onOpenEdge={selectEdge}
              onDepthChange={setFocusDepth}
              onBack={() => {
                clearSelection();
                setWorkspaceView("threads");
              }}
            />
          ) : workspaceView === "list" ? (
            <EvidenceListView
              sections={readerSections}
              edges={edges}
              query={readerQuery}
              totalCount={nodes.filter((node) => !node.data.draft).length}
              onQueryChange={setReaderQuery}
              onOpenNode={inspectNode}
              onFocusNode={openNodeFocus}
              onOpenEdge={selectEdge}
            />
          ) : researchProjection ? (
            <EvidenceThreadView
              projection={researchProjection}
              audit={evidenceAudit.data}
              edges={edges}
              query={readerQuery}
              totalCount={nodes.filter((node) => !node.data.draft).length}
              onQueryChange={setReaderQuery}
              onOpenNode={inspectNode}
              onFocusNode={openNodeFocus}
              onOpenEdge={selectEdge}
            />
          ) : (
            <section className="evidence-reader evidence-reader-syncing" aria-live="polite">
              <RefreshCcw size={18} />
              <strong>正在同步研究状态…</strong>
              <span>证据图与假设链必须来自同一 revision，完成后会自动显示。</span>
            </section>
          )}
          {workspaceView === "overview" && selectedChainId
            && detail.isSuccess
            && appliedDetailRef.current?.chainId === selectedChainId
            && !detail.error
            && nodes.length === 0
            && !activeCanvasPreview ? (
            <section className="evidence-map-empty" aria-label="空证据图">
              <span className="evidence-map-empty-icon"><Network size={22} /></span>
              <div>
                <strong>这张图还没有研究上下文</strong>
                <p>不需要先有实验或数据。可以从研究问题、已知协议、中途问题和下一步开始。</p>
              </div>
              <div className="evidence-map-empty-actions">
                <button
                  className="primary"
                  type="button"
                  onClick={() => {
                    setNodeMenuOpen(true);
                    setProposalNotice("请从左侧“节点”工具选择第一个节点类型。");
                  }}
                >
                  <Plus size={14} />
                  手动添加节点
                </button>
                <button
                  type="button"
                  onClick={() => {
                    const prompt = [
                      `请为 aexp Project ${projectId} 起草第一版 Evidence Topic Map。`,
                      `目标 Map ID：${selectedChainId}`,
                      `Map 标题：${detail.data?.title || ""}`,
                      `Map 用途：${detail.data?.description || "尚未填写"}`,
                      "请提交一个独立 Evidence Proposal，至少包含 hypothesis、protocol、issue、plan 和三条合法关系；不要求 Run 或 Dataset。"
                    ].join("\n");
                    void navigator.clipboard.writeText(prompt).then(
                      () => setProposalNotice("已复制包含 Project 与目标 Map ID 的 Agent 起草指令。"),
                      () => setProposalNotice(prompt)
                    );
                  }}
                >
                  <Bot size={14} />
                  让 Agent 起草
                </button>
              </div>
              {proposalNotice ? <small>{proposalNotice}</small> : null}
            </section>
          ) : null}
        </div>
        <AnimatePresence initial={false}>
          {selectedChainId ? (
            <motion.section
              className={draftDockOpen ? "evidence-draft-dock open" : "evidence-draft-dock"}
              initial={{ opacity: 0, y: 12 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0, y: 12 }}
              transition={{ duration: reduceMotion ? 0 : 0.18 }}
              aria-label="Agent 审批"
            >
              <button
                type="button"
                className="evidence-draft-dock-toggle"
                aria-expanded={draftDockOpen}
                onClick={() => setDraftDockOpen((open) => !open)}
              >
                <span><Bot size={16} />Agent 审批</span>
                <span className="evidence-draft-count">{pendingPreviews.length}</span>
                <ChevronDown className={draftDockOpen ? "rotated" : ""} size={15} />
              </button>
              {draftDockOpen ? (
                <div className="evidence-draft-list">
                  {candidates.isLoading || workspaceProposals.isLoading ? (
                    <div className="evidence-draft-empty">
                      <span>正在检查待审建议…</span>
                    </div>
                  ) : candidates.error || workspaceProposals.error ? (
                    <div className="evidence-draft-empty error">
                      <strong>待审建议加载失败</strong>
                      <button type="button" onClick={() => {
                        void candidates.refetch();
                        void workspaceProposals.refetch();
                      }}>重新加载</button>
                    </div>
                  ) : pendingPreviews.length ? pendingPreviews.map((entry) => {
                    const { preview } = entry;
                    const isActive = entry.key === previewRunID;
                    return (
                      <button
                        key={`${entry.kind}:${entry.key}`}
                        type="button"
                        className={isActive ? "evidence-draft-item active" : "evidence-draft-item"}
                        aria-pressed={isActive}
                        onClick={() => {
                          setPreviewRunID(entry.key);
                          setRunTrayOpen(false);
                          clearSelection();
                        }}
                      >
                        <span className="evidence-draft-item-head">
                          <strong>{entry.title}</strong>
                          <Eye size={14} />
                        </span>
                        <span>
                          {preview.nodes.length} 节点 · {preview.edges.length} 关系
                          {preview.layoutIntent ? ` · Agent 编排 ${preview.layoutIntent.ranks.length} 列` : ""}
                        </span>
                        <small>{entry.kind === "workspace" ? "独立 Proposal" : "历史 Run Card Proposal"}</small>
                        {preview.chainId !== selectedChainId ? <small className="target-mismatch">目标不是当前图 · 查看阻断</small> : null}
                        {preview.routingReason ? <small>{preview.routingReason}</small> : null}
                      </button>
                    );
                  }) : (
                    <div className="evidence-draft-empty">
                      <Check size={16} />
                      <strong>暂无待审建议</strong>
                      <span>Agent 的草稿会先到这里，不会直接修改正式证据图。</span>
                    </div>
                  )}
                </div>
              ) : null}
            </motion.section>
          ) : null}
        </AnimatePresence>
      </main>

      {!projectId ? <AnimatePresence initial={false}>
        {leftOpen ? (
          <motion.aside
            className="evidence-sidebar"
            initial={{ opacity: 0, x: -28 }}
            animate={{ opacity: 1, x: 0 }}
            exit={{ opacity: 0, x: -28 }}
            transition={{ duration: 0.2, ease: [0.22, 1, 0.36, 1] }}
          >
            <div className="evidence-sidebar-head">
              <div>
                <h2>{t("evidenceGraphs")}</h2>
                <span>{chains.data?.length || 0} {t("boardCount")}</span>
              </div>
              <div className="evidence-head-actions">
                <button className="icon-button" title={t("hideBoards")} onClick={() => setLeftOpen(false)}>
                  <ChevronLeft size={16} />
                </button>
                <button className="icon-button" disabled={Boolean(projectId && chains.data?.length)} title={t("newChain")} onClick={() => createChain.mutate()}>
                  <Plus size={16} />
                </button>
              </div>
            </div>
            <label className="evidence-search">
              <Search size={15} />
              <input value={chainQuery} onChange={(event) => setChainQuery(event.target.value)} placeholder={t("search")} />
            </label>
            <div className="chain-list">
              {(chains.data || []).map((chain) => (
                <button key={chain.id} className={chain.id === selectedChainId ? "chain-item active" : "chain-item"} onClick={() => selectChain(chain.id)}>
                  <strong>{chain.title}</strong>
                  <span>{chain.status === "archived" ? "已归档 · " : ""}r{chain.revision || 0} · {chain.description || fmtShortTime(chain.updated_at)}</span>
                </button>
              ))}
              {!chains.data?.length ? <div className="empty">{t("noData")}</div> : null}
            </div>
          </motion.aside>
        ) : (
          <motion.button
            className="evidence-drawer-trigger"
            type="button"
            title={t("showBoards")}
            aria-label={t("showBoards")}
            initial={{ opacity: 0, x: -12 }}
            animate={{ opacity: 1, x: 0 }}
            exit={{ opacity: 0, x: -12 }}
            transition={{ duration: 0.18 }}
            onClick={() => setLeftOpen(true)}
          >
            <ChevronRight size={16} />
            <span>图</span>
          </motion.button>
        )}
      </AnimatePresence> : null}

      <AnimatePresence initial={false}>
        {runTrayOpen ? (
        <motion.section
          id="evidence-run-drawer"
          className="evidence-run-drawer"
          initial={{ opacity: 0, x: 36 }}
          animate={{ opacity: 1, x: 0 }}
          exit={{ opacity: 0, x: 36 }}
          transition={{ duration: 0.22, ease: [0.22, 1, 0.36, 1] }}
        >
          <div className="section-head">
            <div>
              <h2>当前项目实验</h2>
              <span className="muted">{activeCandidateGroup?.candidates.length || 0} 个候选</span>
            </div>
            <button className="icon-button" title={t("hideRuns")} onClick={() => setRunTrayOpen(false)}>
              <ChevronRight size={15} />
            </button>
          </div>
          <div className="candidate-actions">
            <label className="evidence-search">
              <Search size={15} />
              <input value={candidateQuery} onChange={(event) => setCandidateQuery(event.target.value)} placeholder={t("searchRuns")} />
            </label>
            <button className="icon-button" title={t("refresh")} onClick={() => void candidates.refetch()}>
              <RefreshCcw size={15} />
            </button>
          </div>
          <div className="candidate-browser project-scoped">
            {proposalNotice ? <div className="proposal-notice" role="status">{proposalNotice}</div> : null}
            <div className="candidate-list">
              {activeCandidateGroup ? (
                <>
                  <div className="candidate-group-head">
                    <strong>{activeCandidateGroup.title}</strong>
                    <span>{activeCandidateGroup.subtitle}</span>
                  </div>
                  {activeCandidateGroup.candidates.map((candidate) => (
                    <CandidateItem
                      key={candidate.id}
                      candidate={candidate}
                      activePreview={previewRunID === candidate.run_id}
                      onPreviewProposal={(runID) => {
                        setPreviewRunID(runID);
                        setRunTrayOpen(false);
                        clearSelection();
                      }}
                    />
                  ))}
                </>
              ) : (
                <div className="empty">{t("noMatches")}</div>
              )}
            </div>
          </div>
        </motion.section>
        ) : null}
      </AnimatePresence>
      <AnimatePresence initial={false}>
        {!activePreviewEntry && !runTrayOpen && (selectedNode || selectedEdge) ? (
          <motion.aside
            className="evidence-selection-inspector"
            initial={{ opacity: 0, x: 36 }}
            animate={{ opacity: 1, x: 0 }}
            exit={{ opacity: 0, x: 36 }}
            transition={{ duration: reduceMotion ? 0 : 0.2, ease: [0.22, 1, 0.36, 1] }}
            aria-label={selectedNode ? "证据节点详情" : "证据关系详情"}
          >
            {selectedNode ? (
              <EvidenceNodeInspector
                key={selectedNode.id}
                node={selectedNode}
                groups={nodes.filter((node) => node.data.type === "group" && !node.data.draft)}
                members={selectedNode.data.type === "group"
                  ? nodes.filter((node) => node.data.groupId === selectedNode.id)
                  : []}
                protocolMigration={selectedProtocolMigration}
                migrationMembers={selectedProtocolMigration
                  ? nodes.filter((node) => selectedProtocolMigration.memberIds.includes(node.id))
                  : []}
                onClose={clearSelection}
                onUpdate={updateNodeData}
                onOpenMember={inspectNode}
                onConvertProtocol={() => convertProtocolNodeToFrame(selectedNode.id)}
              />
            ) : selectedEdge ? (
              <EvidenceEdgeInspector
                key={selectedEdge.id}
                edge={selectedEdge}
                sourceTitle={selectedEdgeSource?.data.title || selectedEdge.source}
                targetTitle={selectedEdgeTarget?.data.title || selectedEdge.target}
                onClose={clearSelection}
                onUpdate={updateEdgeData}
              />
            ) : null}
          </motion.aside>
        ) : null}
      </AnimatePresence>
      <AnimatePresence initial={false}>
        {activePreviewEntry ? (
          <motion.aside
            className="evidence-proposal-inspector"
            initial={{ opacity: 0, x: 36 }}
            animate={{ opacity: 1, x: 0 }}
            exit={{ opacity: 0, x: 36 }}
            transition={{ duration: reduceMotion ? 0 : 0.2, ease: [0.22, 1, 0.36, 1] }}
            aria-label="Agent 草稿详情"
          >
            <header>
              <div>
                <span className="evidence-agent-label"><Bot size={13} />Agent 草稿</span>
                <h2>{activePreviewEntry.title}</h2>
                <code>{activePreviewEntry.key}</code>
              </div>
              <button className="icon-button" type="button" aria-label="收起预览" title="收起预览" onClick={() => {
                if (activeCanvasPreview) closeProposalCanvasPreview();
                setPreviewRunID("");
              }}>
                <X size={16} />
              </button>
            </header>
            <div className="evidence-proposal-body">
              <div className="evidence-proposal-summary">
                <span><strong>{activePreviewEntry.preview.nodes.length}</strong> 建议节点</span>
                <span><strong>{activePreviewEntry.preview.edges.length}</strong> 建议关系</span>
              </div>
              <section>
                <span className="evidence-proposal-kicker">目标证据图</span>
                <strong>
                  {orderedChains.find((chain) => chain.id === activePreviewEntry.preview.chainId)?.title || "目标图当前不可用"}
                </strong>
                <code>{activePreviewEntry.preview.chainId}</code>
                {activePreviewEntry.preview.chainId !== selectedChainId ? (
                  <div className="evidence-proposal-blocker">
                    <AlertTriangle size={15} />
                    <span>这份草稿属于另一张证据图，切换过去后才能预览。</span>
                    {orderedChains.some((chain) => chain.id === activePreviewEntry.preview.chainId) ? (
                      <button type="button" onClick={() => selectChain(activePreviewEntry.preview.chainId, false, true)}>打开目标图</button>
                    ) : null}
                  </div>
                ) : null}
              </section>
              {activePreviewEntry.preview.routingReason ? (
                <section>
                  <span className="evidence-proposal-kicker">为什么放在这里</span>
                  <p>{activePreviewEntry.preview.routingReason}</p>
                </section>
              ) : null}
              <section className="evidence-proposal-patch">
                <div className="evidence-proposal-patch-heading">
                  <div>
                    <span className="evidence-proposal-kicker">本次变更</span>
                    <strong>只审核草稿，不加载整张图</strong>
                  </div>
                  <button
                    type="button"
                    className={activeCanvasPreview ? "active" : ""}
                    disabled={activePreviewEntry.preview.chainId !== selectedChainId}
                    onClick={activeCanvasPreview ? closeProposalCanvasPreview : openProposalCanvasPreview}
                  >
                    {activeCanvasPreview ? "退出全景" : "在全景中预览"}
                  </button>
                </div>
                <div className="evidence-proposal-patch-list" aria-label="Proposal 变更节点">
                  {activePreviewEntry.preview.nodes.map((node) => (
                    <div className="evidence-proposal-patch-node" key={node.id}>
                      <span>{nodeTypeLabel(node.data.type, node.data.claimKind || node.data.claim_kind)}</span>
                      <strong>{node.data.title || node.data.sourceNodeId || node.id}</strong>
                    </div>
                  ))}
                  {activePreviewEntry.preview.nodes.length === 0 ? <small>没有新增或修改节点</small> : null}
                </div>
                {activePreviewEntry.preview.edges.length ? (
                  <div className="evidence-proposal-patch-edges" aria-label="Proposal 变更关系">
                    {activePreviewEntry.preview.edges.map((edge) => (
                      <span key={edge.id}>{String(edge.label || edgeTypeLabel(edge.data?.type || "related_to"))}</span>
                    ))}
                  </div>
                ) : null}
              </section>
              {activePreviewEntry.preview.layoutIntent ? (
                <section className="evidence-layout-intent">
                  <div className="evidence-layout-intent-heading">
                    <div>
                      <span className="evidence-proposal-kicker">Agent 编排</span>
                      <strong>
                        {activePreviewEntry.preview.layoutIntent.ranks.length} 列 ·{" "}
                        {activePreviewEntry.preview.layoutIntent.ranks.flat().length} 个节点
                      </strong>
                    </div>
                    {activeCanvasPreview ? (
                      <button
                        type="button"
                        className={layoutIntentPreview ? "active" : ""}
                        onClick={() => setLayoutIntentPreview((current) => !current)}
                      >
                        {layoutIntentPreview ? "使用 Agent 编排" : "使用自动排版"}
                      </button>
                    ) : null}
                  </div>
                  <p>{activePreviewEntry.preview.layoutIntent.rationale || "按 Agent 给出的列和列内顺序排列；不改变节点、关系或证据内容。"}</p>
                  <small>只移动本提案明确列出的卡片；未列出的卡片不动，协议内部节点保持原来的相对位置。</small>
                </section>
              ) : null}
              <section>
                <span className="evidence-proposal-kicker">审核门禁</span>
                {previewPlan.isLoading ? <p className="muted">正在检查 provenance 与图结构…</p> : null}
                {previewPlan.error ? (
                  <div className="evidence-proposal-blocker">
                    <AlertTriangle size={15} />
                    <span>{previewPlan.error instanceof Error ? previewPlan.error.message : String(previewPlan.error)}</span>
                  </div>
                ) : null}
                {previewPlan.data?.eligible ? (
                  <div className="evidence-proposal-ready">
                    <Check size={15} />
                    {previewPlan.data.auto_rebased
                      ? `已基于最新 r${previewPlan.data.applied_graph_revision} 安全重放（草稿原为 r${previewPlan.data.base_graph_revision}），可接受到正式图`
                      : "检查通过，可接受到正式图"}
                  </div>
                ) : null}
                {previewPlan.data?.eligible && previewPlan.data.auto_rebased ? (
                  <div className="evidence-proposal-warning">
                    <AlertTriangle size={15} />
                    <span><strong>语义时效仍需判断</strong>安全重放只表示没有覆盖并发编辑，不表示旧建议仍符合当前研究结论。</span>
                  </div>
                ) : null}
                {previewPlan.data?.warnings?.map((warning) => (
                  <div className="evidence-proposal-warning" key={`${warning.code}:${warning.node_id || ""}:${warning.message}`}>
                    <AlertTriangle size={15} />
                    <span><strong>{warning.code}</strong>{warning.message}</span>
                  </div>
                ))}
                {previewPlan.data?.blockers.map((blocker) => (
                  <div className="evidence-proposal-blocker" key={`${blocker.code}:${blocker.message}`}>
                    <AlertTriangle size={15} />
                    <span><strong>{blocker.code}</strong>{blocker.message}</span>
                  </div>
                ))}
              </section>
              {proposalNotice ? <div className="proposal-notice" role="status">{proposalNotice}</div> : null}
            </div>
            <footer>
              <button
                type="button"
                disabled={reviewingRunID === activePreviewEntry.key}
                onClick={() => {
                  if (window.confirm("拒绝后不会修改正式证据图。确定拒绝这份 Agent 草稿？")) {
                    void reviewProposal(activePreviewEntry, "reject");
                  }
                }}
              >
                拒绝
              </button>
              <button
                className="primary"
                type="button"
                disabled={
                  reviewingRunID === activePreviewEntry.key
                  || previewPlan.isLoading
                  || !previewPlan.data?.eligible
                  || dirty
                  || saveState === "saving"
                }
                title={dirty ? "先等待当前图保存完成" : undefined}
                onClick={() => void reviewProposal(activePreviewEntry, "accept")}
              >
                <Check size={15} />
                {activePreviewEntry.preview.layoutIntent ? "接受并应用编排" : "接受到正式图"}
              </button>
            </footer>
          </motion.aside>
        ) : null}
      </AnimatePresence>
    </div>
  );
}

function EvidenceListView({
  sections,
  edges,
  query,
  totalCount,
  onQueryChange,
  onOpenNode,
  onFocusNode,
  onOpenEdge
}: {
  sections: EvidenceReadingSection[];
  edges: EvidenceFlowEdge[];
  query: string;
  totalCount: number;
  onQueryChange: (value: string) => void;
  onOpenNode: (nodeID: string) => void;
  onFocusNode: (nodeID: string) => void;
  onOpenEdge: (edgeID: string) => void;
}) {
  const relationCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const edge of edges) {
      if (edge.data?.draft) continue;
      counts.set(edge.source, (counts.get(edge.source) || 0) + 1);
      counts.set(edge.target, (counts.get(edge.target) || 0) + 1);
    }
    return counts;
  }, [edges]);
  const visibleCount = sections.reduce((sum, section) => sum + section.nodes.length, 0);
  return (
    <motion.section
      className="evidence-reader"
      initial={{ opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.18 }}
      aria-label="证据结构化清单"
    >
      <header className="evidence-reader-head">
        <div>
          <span className="eyebrow">STRUCTURED EVIDENCE</span>
          <h2>结构化清单</h2>
          <p>旧图和零散上下文的完整入口；点击节点后查看局部前因后果。</p>
        </div>
        <label className="evidence-reader-search">
          <Search size={16} />
          <input value={query} onChange={(event) => onQueryChange(event.target.value)} placeholder="搜索标题、正文、类型或 Run ID" />
          <span>{query ? `${visibleCount}/${totalCount}` : totalCount}</span>
        </label>
      </header>
      {sections.length ? (
        <div className="evidence-reader-sections">
          {sections.map((section) => (
            <section className="evidence-reader-section" key={section.id}>
              <header>
                <div><h3>{section.label}</h3><p>{section.description}</p></div>
                <span>{section.nodes.length}</span>
              </header>
              <div className="evidence-reader-rows">
                {section.nodes.map((node) => (
                  <button
                    type="button"
                    className="evidence-reader-row"
                    key={node.id}
                    onClick={() => onOpenNode(node.id)}
                    onDoubleClick={() => onFocusNode(node.id)}
                    title="单击查看详情，双击打开焦点"
                    style={{ "--evidence-accent": evidenceColor(node.data.type) } as CSSProperties}
                  >
                    <span className="evidence-reader-row-type">{nodeTypeLabel(node.data.type, node.data.claimKind || node.data.claim_kind)}</span>
                    <span className="evidence-reader-row-copy">
                      <strong>{node.data.title || nodeTypeLabel(node.data.type, node.data.claimKind || node.data.claim_kind)}</strong>
                      <small>{node.data.body || node.data.summary || "暂无摘要；打开后可在右侧补充详情。"}</small>
                    </span>
                    <span className="evidence-reader-row-meta">
                      {node.data.occurredAt ? <time>{fmtShortTime(String(node.data.occurredAt))}</time> : null}
                      <span>{relationCounts.get(node.id) || 0} 条关系</span>
                      <ChevronRight size={16} />
                    </span>
                  </button>
                ))}
              </div>
            </section>
          ))}
        </div>
      ) : (
        <div className="evidence-reader-empty">
          <Search size={20} />
          <strong>{totalCount ? "没有匹配的证据" : "这张图还没有研究上下文"}</strong>
          <span>{totalCount ? "换一个关键词，或清空搜索。" : "切换到全景即可添加第一个节点或让 Agent 起草。"}</span>
          {query ? <button type="button" onClick={() => onQueryChange("")}>清空搜索</button> : null}
        </div>
      )}
    </motion.section>
  );
}

function EvidenceThreadView({
  projection,
  audit,
  edges,
  query,
  totalCount,
  onQueryChange,
  onOpenNode,
  onFocusNode,
  onOpenEdge
}: {
  projection: EvidenceResearchProjection;
  audit?: EvidenceChainAuditReportDTO;
  edges: EvidenceFlowEdge[];
  query: string;
  totalCount: number;
  onQueryChange: (value: string) => void;
  onOpenNode: (nodeID: string) => void;
  onFocusNode: (nodeID: string) => void;
  onOpenEdge: (edgeID: string) => void;
}) {
  const prefersReducedMotion = useReducedMotion();
  const [relationFocus, setRelationFocus] = useState<{ threadID: string; nodeID: string } | null>(null);
  const relationCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const edge of edges) {
      if (edge.data?.draft) continue;
      counts.set(edge.source, (counts.get(edge.source) || 0) + 1);
      counts.set(edge.target, (counts.get(edge.target) || 0) + 1);
    }
    return counts;
  }, [edges]);
  const normalizedQuery = query.trim().toLocaleLowerCase();
  const matches = useCallback((node: EvidenceFlowNode) => {
    if (!normalizedQuery) return true;
    return [node.data.title, node.data.body, node.data.runId, nodeTypeLabel(node.data.type, node.data.claimKind || node.data.claim_kind)]
      .some((value) => String(value || "").toLocaleLowerCase().includes(normalizedQuery));
  }, [normalizedQuery]);
  const visibleThreads = useMemo(() => projection.threads.map((thread) => {
    const threadMatches = normalizedQuery && thread.title.toLocaleLowerCase().includes(normalizedQuery);
    const stages = Object.fromEntries(evidenceResearchStages.map((stage) => [
      stage.id,
      threadMatches ? thread.stages[stage.id] : thread.stages[stage.id].filter(matches)
    ])) as Record<EvidenceResearchStage, EvidenceFlowNode[]>;
    return { ...thread, stages };
  }).filter((thread) => evidenceResearchStages.some((stage) => thread.stages[stage.id].length)), [edges, matches, projection.threads]);
  const visibleUnassigned = projection.unassigned.filter(matches);
  const focusRelationsByThread = useMemo(() => new Map(visibleThreads.map((thread) => [
    thread.id,
    evidenceThreadFocusRelations(thread, edges)
  ])), [edges, visibleThreads]);
  const canonicalThreadsByID = useMemo(() => new Map(projection.threads.map((thread) => [thread.id, thread])), [projection.threads]);
  const activeFocusState = useMemo(() => {
    if (!relationFocus) return null;
    const canonicalThread = canonicalThreadsByID.get(relationFocus.threadID);
    const visibleThread = visibleThreads.find((thread) => thread.id === relationFocus.threadID);
    if (!canonicalThread || !visibleThread) return null;
    return evidenceThreadRelationFocus(canonicalThread, visibleThread, edges, relationFocus.nodeID);
  }, [canonicalThreadsByID, edges, relationFocus, visibleThreads]);
  const activeRibbonRelations = useMemo(() => {
    if (!relationFocus || !activeFocusState) return [];
    const relations = focusRelationsByThread.get(relationFocus.threadID);
    if (!relations) return [];
    const visibleNodeIDs = new Set([activeFocusState.originNodeId, ...activeFocusState.visiblePeerNodeIds]);
    const seen = new Set<string>();
    const ribbons: EvidenceThreadRibbonRelation[] = [];
    for (const sourceNodeId of [...visibleNodeIDs].sort()) {
      for (const relation of relations.get(sourceNodeId) || []) {
        if (relation.direction !== "outgoing" || !visibleNodeIDs.has(relation.otherNode.id)) continue;
        const key = `${relation.edge.id}:${sourceNodeId}:${relation.otherNode.id}`;
        if (seen.has(key)) continue;
        seen.add(key);
        ribbons.push({
          id: relation.edge.id,
          sourceNodeId,
          targetNodeId: relation.otherNode.id,
          type: relation.edge.data?.type || "related_to",
          direct: sourceNodeId === activeFocusState.originNodeId || relation.otherNode.id === activeFocusState.originNodeId
        });
      }
    }
    return ribbons.sort((left, right) => `${left.sourceNodeId}:${left.targetNodeId}:${left.id}`.localeCompare(`${right.sourceNodeId}:${right.targetNodeId}:${right.id}`));
  }, [activeFocusState, focusRelationsByThread, relationFocus]);
  const activeDirectPeerNodeIDs = useMemo(() => {
    if (!relationFocus) return new Set<string>();
    return new Set((focusRelationsByThread.get(relationFocus.threadID)?.get(relationFocus.nodeID) || [])
      .map((relation) => relation.otherNode.id));
  }, [focusRelationsByThread, relationFocus]);
  const threadTitles = useMemo(() => new Map(projection.threads.map((thread) => [thread.id, thread.title])), [projection.threads]);
  const relationsByThread = useMemo(() => {
    const rows = new Map<string, Array<{ direction: "in" | "out"; otherThreadId: string; label: string; kind: "branch" | "causal" }>>();
    for (const relation of projection.crossThreadRelations || []) {
      const label = String(relation.edge.label || edgeTypeLabel(relation.edge.data?.type || "next_step"));
      rows.set(relation.sourceThreadId, [...(rows.get(relation.sourceThreadId) || []), {
        direction: "out", otherThreadId: relation.targetThreadId, label, kind: relation.kind
      }]);
      rows.set(relation.targetThreadId, [...(rows.get(relation.targetThreadId) || []), {
        direction: "in", otherThreadId: relation.sourceThreadId, label, kind: relation.kind
      }]);
    }
    for (const relations of rows.values()) {
      relations.sort((left, right) => `${left.kind}:${left.direction}:${left.otherThreadId}:${left.label}`.localeCompare(`${right.kind}:${right.direction}:${right.otherThreadId}:${right.label}`));
    }
    return rows;
  }, [projection.crossThreadRelations]);
  const visibleCount = visibleThreads.reduce((sum, thread) => (
    sum + evidenceResearchStages.reduce((stageSum, stage) => stageSum + thread.stages[stage.id].filter((node) => node.data.projectionOnly !== "interpretation").length, 0)
  ), 0) + visibleUnassigned.length;
  const capacity = projection.capacity;
  const capacityLabel = capacity?.status === "split_recommended"
    ? "建议拆分专题图"
    : capacity?.status === "cleanup_required"
      ? "需要先整理当前专题"
      : capacity?.status === "near_limit"
        ? "专题接近建议容量"
        : "";
  const structuralHealth = projection.structuralHealth;
  const threadHealthByID = useMemo(() => new Map((structuralHealth?.threads || []).map((health) => [health.thread_id, health])), [structuralHealth]);
  const readabilityLabel: Record<string, string> = {
    clear: "清晰",
    dense: "结构较密",
    needs_curation: "需要整理",
    v2_readable: "新版可读",
    legacy_readable: "历史可读",
    broken: "读取异常"
  };
  const complianceLabel: Record<string, string> = {
    v2_compliant: "v2 合规",
    legacy_mixed: "历史混合",
    v2_noncompliant: "不合规"
  };
  const publicationLabel: Record<string, string> = {
    not_applicable: "尚无发布结论",
    publication_ready: "证据可发布",
    publication_blocked: "发布被阻断"
  };
  const phaseLabel: Record<string, string> = {
    empty: "空白",
    needs_curation: "待整理",
    mixed: "多阶段进行中",
    hypothesis_recorded: "假设已记录",
    design_recorded: "设计已记录",
    result_recorded: "结果待解释",
    outcome_recorded: "已有结果处置"
  };
  const lifecycleLabel: Record<string, string> = { draft: "草稿", active: "活跃", archived: "已归档" };
  const threadOutcomeSummary = (thread: EvidenceResearchThread) => {
    const outcomes = thread.stages.issue;
    const conclusions = outcomes.filter((node) => node.data.type === "conclusion").length;
    const issues = outcomes.filter((node) => node.data.type === "issue").length;
    const pending = thread.stages.conclusion.filter((node) => node.data.interpretationKind === "pending").length;
    return `${conclusions} 结论 · ${issues} 问题 · ${pending} 待解释`;
  };
  const threadDOMID = useCallback((threadID: string) => `evidence-thread-${encodeURIComponent(threadID)}`, []);
  const jumpToThread = useCallback((threadID: string) => {
    const revealAndScroll = () => {
      const target = document.getElementById(threadDOMID(threadID));
      if (!target) return;
      target.scrollIntoView({ behavior: prefersReducedMotion ? "auto" : "smooth", block: "center", inline: "nearest" });
      target.focus({ preventScroll: true });
    };
    if (!document.getElementById(threadDOMID(threadID)) && query) {
      onQueryChange("");
      requestAnimationFrame(() => requestAnimationFrame(revealAndScroll));
      return;
    }
    revealAndScroll();
  }, [onQueryChange, prefersReducedMotion, query, threadDOMID]);
  const renderCard = (node: EvidenceFlowNode, threadID = "", stage?: EvidenceResearchStage) => {
    const relationCount = node.data.projectedRelationCount ?? relationCounts.get(node.id) ?? 0;
    const threadRelations = focusRelationsByThread.get(threadID);
    const adjacentRelations = threadRelations?.get(node.id) || [];
    const focusState = relationFocus?.threadID === threadID ? activeFocusState : null;
    const isRelationOrigin = Boolean(focusState && relationFocus?.nodeID === node.id);
    const isRelationDirectPeer = Boolean(focusState?.visiblePeerNodeIds.includes(node.id) && activeDirectPeerNodeIDs.has(node.id));
    const isRelationContextPeer = Boolean(focusState?.visiblePeerNodeIds.includes(node.id) && !isRelationDirectPeer);
    const isRelationMuted = Boolean(focusState && !isRelationOrigin && !isRelationDirectPeer && !isRelationContextPeer);
    const visibleRelations: EvidenceAdjacentStageRelation[] = [...(isRelationOrigin
      ? adjacentRelations
      : isRelationDirectPeer
        ? adjacentRelations.filter((relation) => relation.otherNode.id === relationFocus?.nodeID)
        : [])].sort((left, right) => {
          // A swimlane is read left-to-right. On the focused card, surface the
          // next-stage consequence before the previous-stage context so a
          // design immediately answers “which result did this produce?”.
          if (isRelationOrigin && left.direction !== right.direction) {
            return left.direction === "outgoing" ? -1 : 1;
          }
          return left.otherNode.id.localeCompare(right.otherNode.id);
        });
    const primaryVisibleRelation = visibleRelations[0];
    const primaryRelationType = primaryVisibleRelation?.edge.data?.type || "next_step";
    const primaryRelationLabel = primaryVisibleRelation
      ? String(primaryVisibleRelation.edge.label || edgeTypeLabel(primaryRelationType))
      : "";
    const relationSummary = visibleRelations.map((relation) => {
      const direction = relation.direction === "outgoing" ? "指向" : "来自";
      const label = String(relation.edge.label || edgeTypeLabel(relation.edge.data?.type || "next_step"));
      return `${direction}${relation.otherNode.data.title || nodeTypeLabel(relation.otherNode.data.type, relation.otherNode.data.claimKind || relation.otherNode.data.claim_kind)}：${label}`;
    }).join("；");
    const resultDisposition = stage === "result"
      ? String(node.data.resultDisposition || node.data.result_disposition || "legacy").trim().toLocaleLowerCase()
      : "";
    const resultDispositionLabel: Record<string, string> = {
      conclusion: "形成结论",
      issue: "产生问题",
      mixed: "结论 + 问题",
      pending: "待解释",
      legacy: "历史未分类"
    };
    const activateRelations = () => {
      if (threadID) setRelationFocus({ threadID, nodeID: node.id });
    };
    const clearRelations = () => setRelationFocus((current) => (
      current?.threadID === threadID && current.nodeID === node.id ? null : current
    ));
    return (
      <button
        type="button"
        className={[
          "evidence-thread-card",
          node.data.projectionOnly === "interpretation" ? "is-interpretation" : "",
          isRelationOrigin ? "is-relation-origin" : "",
          isRelationDirectPeer ? "is-relation-peer" : "",
          isRelationContextPeer ? "is-relation-context" : "",
          isRelationMuted ? "is-relation-muted" : ""
        ].filter(Boolean).join(" ")}
        key={node.id}
        data-evidence-node-id={node.id}
        onClick={() => node.data.projectionOnly === "interpretation"
          ? node.data.interpretationEdgeId
            ? onOpenEdge(String(node.data.interpretationEdgeId))
            : onOpenNode(String(node.data.interpretationSourceNodeId || node.id))
          : onOpenNode(node.id)}
        onDoubleClick={() => onFocusNode(String(node.data.interpretationSourceNodeId || node.id))}
        onPointerEnter={activateRelations}
        onPointerDown={activateRelations}
        onPointerLeave={clearRelations}
        onFocus={activateRelations}
        onBlur={clearRelations}
        aria-label={`${node.data.title || nodeTypeLabel(node.data.type, node.data.claimKind || node.data.claim_kind)}${relationSummary ? `。相邻关系：${relationSummary}` : ""}`}
        title="单击查看详情，双击打开焦点"
        style={{ "--evidence-accent": evidenceColor(node.data.type) } as CSSProperties}
      >
        <span className="evidence-thread-card-top">
          <span>{node.data.projectionOnly === "interpretation" ? "解释判断" : nodeTypeLabel(node.data.type, node.data.claimKind || node.data.claim_kind)}</span>
          {resultDisposition ? <b className={`evidence-result-disposition is-${resultDisposition}`}>{resultDispositionLabel[resultDisposition] || resultDisposition}</b> : null}
          <small className={visibleRelations.length ? "is-direct-relation" : ""}>
            {isRelationOrigin && focusState?.disconnected
              ? "无直接关系"
              : isRelationOrigin && focusState?.hiddenRelationCount
                ? `${focusState.hiddenRelationCount} 个关系被搜索隐藏`
                : visibleRelations.length
              ? isRelationOrigin
                ? `${visibleRelations.length} 个直接关系`
                : `${primaryVisibleRelation?.direction === "incoming" ? "前因" : "后果"} · ${primaryRelationLabel}`
              : `${relationCount} 关系`}
          </small>
        </span>
        <strong>{node.data.title || nodeTypeLabel(node.data.type, node.data.claimKind || node.data.claim_kind)}</strong>
        {node.data.body || node.data.summary ? <p>{String(node.data.body || node.data.summary)}</p> : null}
        {node.data.unassignedReason ? <span className="evidence-unassigned-reason">待整理原因 · {String(node.data.unassignedReason)}</span> : null}
        {primaryVisibleRelation ? (
          <span
            className="evidence-thread-card-open is-relation"
            title={relationSummary}
            style={{ "--relation-accent": evidenceColor(primaryRelationType) } as CSSProperties}
          >
            <b>{primaryVisibleRelation.direction === "outgoing" ? "→" : "←"} {primaryRelationLabel}</b>
            <small>{primaryVisibleRelation.otherNode.data.title || nodeTypeLabel(primaryVisibleRelation.otherNode.data.type, primaryVisibleRelation.otherNode.data.claimKind || primaryVisibleRelation.otherNode.data.claim_kind)}</small>
            {visibleRelations.length > 1 ? <em>+{visibleRelations.length - 1}</em> : null}
          </span>
        ) : isRelationOrigin && focusState && (focusState.disconnected || focusState.hiddenRelationCount > 0) ? (
          <EvidenceThreadRelationHint focus={focusState} />
        ) : (
          <span className="evidence-thread-card-open">查看前因后果 <ChevronRight size={14} /></span>
        )}
      </button>
    );
  };
  return (
    <motion.section
      className="evidence-reader evidence-thread-view"
      initial={{ opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.18 }}
      aria-label="假设链"
    >
      <header className="evidence-reader-head">
        <div className="evidence-reader-title">
          <h2>假设链</h2>
          <span>{projection.threads.length} 条 Research Thread · 5 个 Stage Column</span>
        </div>
        <label className="evidence-reader-search">
          <Search size={16} />
          <input
            value={query}
            onChange={(event) => onQueryChange(event.target.value)}
            placeholder="搜索标题、正文、类型或 Run ID"
          />
          <span>{query ? `${visibleCount}/${totalCount}` : totalCount}</span>
        </label>
      </header>
      {structuralHealth ? (
        <details className="evidence-research-health">
          <summary>
            <span><b>可读性</b>{readabilityLabel[audit?.readability_status || structuralHealth.readability_status] || "未知状态"}</span>
            <span><b>契约</b>{complianceLabel[audit?.v2_compliance_status || structuralHealth.compatibility_status] || "未知状态"}</span>
            <span className={audit?.publication_status === "publication_blocked" ? "is-blocked" : ""}><b>发布</b>{publicationLabel[audit?.publication_status || "not_applicable"] || "未知状态"}</span>
            <small>Topic · {lifecycleLabel[structuralHealth.topic_lifecycle] || structuralHealth.topic_lifecycle} · {phaseLabel[structuralHealth.derived_topic_phase] || structuralHealth.derived_topic_phase}</small>
            <ChevronDown size={14} />
          </summary>
          <div>
            <span>{structuralHealth.assigned_count} 项已归属 · {structuralHealth.unassigned_count} 项待整理 · provenance 与结果去向按假设链检查</span>
            {capacity && capacity.status !== "healthy" ? <span>{capacityLabel} · {capacity.reasons.join("、")}</span> : null}
            {audit?.blockers?.length ? <span>{audit.blockers.length} 个发布门禁 · {audit.blockers.slice(0, 3).map((item) => item.code).join("、")}</span> : null}
          </div>
        </details>
      ) : null}
      {visibleThreads.length || visibleUnassigned.length ? (
        <div className="evidence-thread-scroll">
          {visibleThreads.length ? (
            <>
              <div className="evidence-thread-stage-heads">
                {evidenceResearchStages.map((stage, index) => (
                  <div key={stage.id} title={stage.description}>
                    <span>{String(index + 1).padStart(2, "0")}</span>
                    <strong>{stage.label}</strong>
                  </div>
                ))}
              </div>
              <div className="evidence-thread-list">
                {visibleThreads.map((thread, index) => (
                  <section
                    className={thread.parentThreadId ? "evidence-thread is-child" : "evidence-thread"}
                    id={threadDOMID(thread.id)}
                    key={thread.id}
                    tabIndex={-1}
                  >
                    <div className="evidence-thread-rail" aria-hidden="true">
                      <span>{String(index + 1).padStart(2, "0")}</span>
                      <i />
                    </div>
                    <div className="evidence-thread-lane">
                      <header>
                        <span>{thread.parentThreadId ? <GitBranch size={14} /> : null}{thread.parentThreadId ? "子假设链" : "假设链"}</span>
                        <strong>{thread.title}</strong>
                        {threadHealthByID.get(thread.id) ? (
                          <em className={`evidence-thread-health is-${threadHealthByID.get(thread.id)?.complexity_level}`}>
                            {phaseLabel[threadHealthByID.get(thread.id)?.derived_phase || ""] || threadHealthByID.get(thread.id)?.derived_phase}
                            {threadHealthByID.get(thread.id)?.complexity_level !== "normal" ? ` · ${threadHealthByID.get(thread.id)?.semantic_node_count} 节点` : ""}
                          </em>
                        ) : null}
                        <small>{threadOutcomeSummary(thread)}</small>
                      </header>
                      {(relationsByThread.get(thread.id) || []).length ? (
                        <div className="evidence-thread-bridges" aria-label="跨泳道关系">
                          {(relationsByThread.get(thread.id) || []).slice(0, 4).map((relation, relationIndex) => (
                            <button
                              type="button"
                              className={relation.kind === "branch" ? "is-branch" : ""}
                              key={`${relation.direction}:${relation.otherThreadId}:${relation.label}:${relationIndex}`}
                              onClick={() => jumpToThread(relation.otherThreadId)}
                              aria-label={`${relation.direction === "in" ? "返回" : "前往"}${threadTitles.get(relation.otherThreadId) || relation.otherThreadId}：${relation.label}`}
                            >
                              {relation.direction === "in" ? "←" : "→"} {relation.label}
                              <small>{threadTitles.get(relation.otherThreadId) || relation.otherThreadId}</small>
                              <ChevronRight size={12} />
                            </button>
                          ))}
                          {(relationsByThread.get(thread.id) || []).length > 4 ? <em>+{(relationsByThread.get(thread.id) || []).length - 4}</em> : null}
                        </div>
                      ) : null}
                      <div className={`evidence-thread-grid${relationFocus?.threadID === thread.id ? " has-relation-focus" : ""}`}>
                        {relationFocus?.threadID === thread.id && activeRibbonRelations.length ? (
                          <EvidenceThreadRelationOverlay relations={activeRibbonRelations} reducedMotion={Boolean(prefersReducedMotion)} />
                        ) : null}
                        {evidenceResearchStages.map((stage) => (
                          <div className={`evidence-thread-stage evidence-thread-stage--${stage.id}`} key={stage.id}>
                            {thread.stages[stage.id].length
                              ? thread.stages[stage.id].map((node) => renderCard(node, thread.id, stage.id))
                              : <span className="evidence-thread-gap" aria-label={`${stage.label}暂无内容`} />}
                          </div>
                        ))}
                      </div>
                    </div>
                  </section>
                ))}
              </div>
            </>
          ) : (
            <div className="evidence-thread-legacy-empty">
              <GitBranch size={18} />
              <div>
                <strong>这张历史图还没有显式假设链</strong>
                <span>原有节点保持可读并进入待整理；从明确假设开始后，才会进入五个阶段列。</span>
              </div>
            </div>
          )}
          {visibleUnassigned.length ? (
            <details className="evidence-thread-triage">
              <summary><span>待整理</span><small>{visibleUnassigned.length} 项尚未归入假设链；这里只是临时收件箱</small><ChevronDown size={15} /></summary>
              <div>{visibleUnassigned.map((node) => renderCard(node))}</div>
            </details>
          ) : null}
        </div>
      ) : (
        <div className="evidence-reader-empty">
          <Search size={20} />
          <strong>{totalCount ? "没有可显示的假设链" : "这张专题图还没有研究上下文"}</strong>
          <span>{totalCount ? "换一个关键词，或展开待整理内容。" : "请从明确假设开始起草 research-thread-v2；Stage Column 只是展示列。"}</span>
          {query ? <button type="button" onClick={() => onQueryChange("")}>清空搜索</button> : null}
        </div>
      )}
    </motion.section>
  );
}

function EvidenceFocusView({
  neighborhood,
  depth,
  selected,
  onOpenNode,
  onFocusNode,
  onOpenEdge,
  onDepthChange,
  onBack
}: {
  neighborhood: EvidenceNeighborhood;
  depth: EvidenceFocusDepth;
  selected: { kind: "node" | "edge"; id: string } | null;
  onOpenNode: (nodeID: string) => void;
  onFocusNode: (nodeID: string) => void;
  onOpenEdge: (edgeID: string) => void;
  onDepthChange: (depth: EvidenceFocusDepth) => void;
  onBack: () => void;
}) {
  const center = neighborhood.center;
  const graph = useMemo(() => layoutEvidenceNeighborhood(neighborhood), [neighborhood]);
  const focusNodes = useMemo(
    () => graph.nodes.map((node) => ({ ...node, selected: selected?.kind === "node" && selected.id === node.id })),
    [graph.nodes, selected]
  );
  const focusEdges = useMemo(
    () => graph.edges.map((edge) => ({ ...edge, selected: selected?.kind === "edge" && selected.id === edge.id })),
    [graph.edges, selected]
  );
  const focusGraphRef = useRef<HTMLDivElement | null>(null);
  const focusNodeTypes = useMemo(() => ({ evidence: EvidenceNode }), []);
  const focusEdgeTypes = useMemo(() => ({ evidence: EvidenceFocusEdge }), []);
  if (!center) {
    return (
      <section className="evidence-focus-empty">
        <Focus size={22} />
        <strong>先从清单选择一个节点</strong>
        <button type="button" onClick={onBack}>返回清单</button>
      </section>
    );
  }
  return (
    <motion.section
      key={center.id}
      className="evidence-focus-view"
      initial={{ opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.18 }}
      aria-label={`${center.data.title} 的局部关系`}
    >
      <header className="evidence-focus-head">
        <button type="button" onClick={onBack}><ChevronLeft size={15} />返回清单</button>
        <div className="evidence-focus-summary">
          <span>局部前因后果</span>
          <strong>{neighborhood.upstream.length} 上游 · {neighborhood.downstream.length} 下游 · {neighborhood.related.length} 关联</strong>
        </div>
        <div className="evidence-focus-depth" role="group" aria-label="显示关系层数">
          <span>层数</span>
          {([1, 2, 3, "all"] as EvidenceFocusDepth[]).map((value) => (
            <button
              key={value}
              type="button"
              className={depth === value ? "is-active" : ""}
              aria-pressed={depth === value}
              onClick={() => onDepthChange(value)}
            >
              {value === "all" ? "全部" : value}
            </button>
          ))}
        </div>
      </header>
      <div className="evidence-focus-legend" aria-hidden="true">
        <span><i className="upstream" />前因 / 依据</span>
        <span><i className="current" />当前节点</span>
        <span><i className="downstream" />后果 / 下一步</span>
        <span><i className="related" />背景（上 → 中）</span>
      </div>
      <div ref={focusGraphRef} className="evidence-focus-graph" aria-label={`${center.data.title} 的${depth === "all" ? "完整" : `${depth}层`}前因后果图`}>
        <ReactFlow
          nodes={focusNodes}
          edges={focusEdges}
          nodeTypes={focusNodeTypes}
          edgeTypes={focusEdgeTypes}
          key={`${center.id}-${depth}`}
          onInit={(instance) => {
            const shell = focusGraphRef.current;
            if (!graph.nodes.length || !shell) return;
            const minX = Math.min(...graph.nodes.map((node) => node.position.x));
            const maxX = Math.max(...graph.nodes.map((node) => node.position.x + (node.width || compactEvidenceNodeSize.width)));
            const minY = Math.min(...graph.nodes.map((node) => node.position.y));
            const maxY = Math.max(...graph.nodes.map((node) => node.position.y + (node.height || compactEvidenceNodeSize.height)));
            const graphWidth = Math.max(1, maxX - minX);
            const graphHeight = Math.max(1, maxY - minY);
            const focusZoom = Math.max(0.28, Math.min(
              0.84,
              (shell.clientWidth - 72) / graphWidth,
              (shell.clientHeight - 64) / graphHeight
            ));
            const graphCenterX = minX + graphWidth / 2;
            const graphCenterY = minY + graphHeight / 2;
            void instance.setViewport({
              x: shell.clientWidth / 2 - graphCenterX * focusZoom,
              y: shell.clientHeight / 2 - graphCenterY * focusZoom,
              zoom: focusZoom
            });
          }}
          minZoom={0.28}
          maxZoom={1.35}
          nodesDraggable={false}
          nodesConnectable={false}
          elementsSelectable
          onNodeClick={(_event, node) => onOpenNode(node.id)}
          onNodeDoubleClick={(_event, node) => onFocusNode(node.id)}
          onEdgeClick={(_event, edge) => onOpenEdge(edge.id)}
          proOptions={{ hideAttribution: true }}
        >
          <Background gap={24} color="#dce2e8" />
          <Controls showInteractive={false} />
        </ReactFlow>
      </div>
    </motion.section>
  );
}

function isEditableTarget(target: EventTarget | null) {
  if (!(target instanceof HTMLElement)) return false;
  return target.isContentEditable || target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement;
}

function evidenceDetailApplyKey(detail: EvidenceChainDetail) {
  const nodeKey = [...(detail.nodes || [])]
    .sort((left, right) => left.id < right.id ? -1 : left.id > right.id ? 1 : 0)
    .map((node) => [
      node.id,
      node.updated_at || "",
      node.x,
      node.y,
      node.width || "",
      node.height || "",
      node.pinned ? "1" : "0",
      node.occurred_at || "",
      node.data_json || "",
      node.title || "",
      node.body || ""
    ].join(":"))
    .join("|");
  const edgeKey = [...(detail.edges || [])]
    .sort((left, right) => left.id < right.id ? -1 : left.id > right.id ? 1 : 0)
    .map((edge) => [
      edge.id,
      edge.updated_at || "",
      edge.source_node_id,
      edge.target_node_id,
      edge.type,
      edge.label || "",
      edge.rationale || "",
      edge.data_json || ""
    ].join(":"))
    .join("|");
  return [detail.id, detail.revision || 0, detail.graph_hash || "", detail.updated_at || "", nodeKey, edgeKey].join("::");
}

function CandidateItem({ candidate, activePreview, onPreviewProposal }: { candidate: EvidenceChainRunCandidate; activePreview: boolean; onPreviewProposal: (runID: string) => void }) {
  const { runDisplayTitle } = candidateRunNodeFields(candidate);
  const title = runDisplayTitle;
  const meta = candidate.verdict || candidate.question || candidate.key_metrics || candidate.next_action || candidate.run?.status || candidate.kind;
  const hasPendingProposal = candidate.project_card?.graph_status === "pending";
  return (
    <div
      className="candidate-item"
      draggable={!hasPendingProposal}
      title={hasPendingProposal ? "先预览并审核 Agent 草稿；待审建议不能直接拖入正式图。" : undefined}
      onDragStart={(event) => {
        if (hasPendingProposal) {
          event.preventDefault();
          return;
        }
        event.dataTransfer.setData(candidateMime, JSON.stringify(candidate));
        event.dataTransfer.effectAllowed = "copy";
      }}
    >
      <strong>{title}</strong>
      <span>{meta}</span>
      <small>{candidate.kind === "project_card" ? `L${candidate.evidence_level || "C"}` : candidate.run?.kind || "run"} · {candidate.run_id}</small>
      {hasPendingProposal ? (
        <div className="candidate-proposal-actions" onPointerDown={(event) => event.stopPropagation()}>
          <span className="status-chip warning">待确认图建议</span>
          <button className={activePreview ? "active-soft" : ""} onClick={() => onPreviewProposal(candidate.run_id)}>
            <Eye size={13} />
            {activePreview ? "正在预览" : "预览建议"}
          </button>
        </div>
      ) : candidate.project_card?.graph_status && candidate.project_card.graph_status !== "none" ? (
        <span className={`status-chip graph-${candidate.project_card.graph_status}`}>{candidate.project_card.graph_status}</span>
      ) : null}
    </div>
  );
}

function EvidenceGroupFrame({
  descriptor,
  bounds,
  internalEdgeCount,
  onSelect,
  selected,
  movable,
  moving,
  memberDragging,
  onMoveStart,
  onMove,
  onMoveEnd,
  onMoveBy
}: {
  descriptor: EvidenceGroupDescriptor;
  bounds: EvidenceGroupFrameBounds;
  internalEdgeCount: number;
  onSelect: () => void;
  selected: boolean;
  movable: boolean;
  moving: boolean;
  memberDragging: boolean;
  onMoveStart: (client: { x: number; y: number }) => void;
  onMove: (client: { x: number; y: number }) => void;
  onMoveEnd: () => void;
  onMoveBy: (delta: { x: number; y: number }) => void;
}) {
  const data = descriptor.group.data;
  const pointerRef = useRef<number | null>(null);
  const finishPointerMove = (event: ReactPointerEvent<HTMLButtonElement>) => {
    if (pointerRef.current !== event.pointerId) return;
    onMove({ x: event.clientX, y: event.clientY });
    pointerRef.current = null;
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    onMoveEnd();
  };
  const onMoveHandleKeyDown = (event: ReactKeyboardEvent<HTMLButtonElement>) => {
    const delta = protocolContainerMoveDeltaForKey(event.key, event.shiftKey);
    if (!delta || !movable) return;
    event.preventDefault();
    event.stopPropagation();
    onSelect();
    onMoveBy(delta);
  };
  return (
    <section
      className={`evidence-group-frame ${data.draft ? "evidence-group-frame--draft" : ""} ${selected ? "is-selected" : ""} ${moving ? "is-moving" : ""} ${memberDragging ? "is-member-dragging" : ""}`}
      style={{
        transform: `translate(${bounds.x}px, ${bounds.y}px)`,
        width: bounds.width,
        height: bounds.height
      }}
      aria-label={`${data.title || "实验协议"}，${descriptor.memberIds.length} 个节点`}
    >
      <header className="evidence-group-frame-header">
        <button
          type="button"
          className="evidence-group-move-handle nodrag nopan"
          disabled={!movable}
          aria-label="移动实验协议"
          aria-keyshortcuts="ArrowUp ArrowDown ArrowLeft ArrowRight Shift+ArrowUp Shift+ArrowDown Shift+ArrowLeft Shift+ArrowRight"
          title="拖动可整体移动协议；方向键微调，Shift 加速"
          onClick={onSelect}
          onKeyDown={onMoveHandleKeyDown}
          onPointerDown={(event) => {
            if (!movable || event.button !== 0) return;
            event.preventDefault();
            event.stopPropagation();
            pointerRef.current = event.pointerId;
            event.currentTarget.setPointerCapture(event.pointerId);
            onMoveStart({ x: event.clientX, y: event.clientY });
          }}
          onPointerMove={(event) => {
            if (pointerRef.current !== event.pointerId) return;
            event.preventDefault();
            onMove({ x: event.clientX, y: event.clientY });
          }}
          onPointerUp={finishPointerMove}
          onPointerCancel={finishPointerMove}
          onLostPointerCapture={() => {
            if (pointerRef.current === null) return;
            pointerRef.current = null;
            onMoveEnd();
          }}
        >
          <GripVertical size={14} />
        </button>
        <button type="button" className="evidence-group-frame-title nodrag nopan" aria-pressed={selected} onClick={onSelect}>
          <span className="evidence-group-frame-kind">{data.draft ? "Agent 草稿" : "实验协议"}</span>
          <strong>{data.title || "实验协议"}</strong>
          {data.version ? <span className="evidence-group-version">{data.version}</span> : null}
        </button>
        <div className="evidence-group-frame-meta">
          <span>
            {moving
              ? "正在移动整个协议"
              : memberDragging
                ? "松开后扩展协议范围"
                : `${descriptor.memberIds.length} 节点 · ${internalEdgeCount} 内部关系`}
          </span>
        </div>
      </header>
      {!descriptor.memberIds.length ? <p>在节点详情中选择此协议即可加入</p> : null}
    </section>
  );
}

function EvidenceNodeInspector({
  node,
  groups,
  members,
  protocolMigration,
  migrationMembers,
  onClose,
  onUpdate,
  onOpenMember,
  onConvertProtocol
}: {
  node: EvidenceFlowNode;
  groups: EvidenceFlowNode[];
  members: EvidenceFlowNode[];
  protocolMigration: ReturnType<typeof inspectProtocolFrameMigration> | null;
  migrationMembers: EvidenceFlowNode[];
  onClose: () => void;
  onUpdate: (nodeId: string, patch: Partial<EvidenceNodeData>) => void;
  onOpenMember: (nodeId: string) => void;
  onConvertProtocol: () => void;
}) {
  const data = node.data;
  const regularNodeTypes = evidenceNodeTypes.filter((type) => type !== "group");
  const typeOptions: EvidenceNodeType[] = data.groupId
    ? regularNodeTypes.filter(isProtocolGroupMemberType)
    : data.type === "group"
    ? ["group"]
    : data.type === "run"
    ? ["run", ...regularNodeTypes]
    : data.type === "map_ref"
      ? ["map_ref"]
      : regularNodeTypes;
  const readOnly = data.readOnly === true;
  const gitLabel = gitNodeLabel(data);

  return (
    <>
      <header>
        <div>
          <span className="evidence-inspector-kind" style={{ "--evidence-color": evidenceColor(data.type) } as CSSProperties}>
            {data.draft ? <Bot size={13} /> : <Network size={13} />}
            {nodeTypeLabel(data.type, data.claimKind || data.claim_kind)}
          </span>
          <h2>{data.title || nodeTypeLabel(data.type, data.claimKind || data.claim_kind)}</h2>
          <code>{node.id}</code>
        </div>
        <button className="icon-button" type="button" aria-label="关闭详情" title="关闭详情" onClick={onClose}>
          <X size={16} />
        </button>
      </header>
      <div className="evidence-selection-body inspector-form">
        <label>
          <span>节点类型</span>
          <select
            disabled={readOnly || data.type === "map_ref"}
            value={data.type}
            onChange={(event) => onUpdate(node.id, { type: event.target.value as EvidenceNodeType })}
          >
            {typeOptions.map((type) => <option key={type} value={type}>{nodeTypeLabel(type)}</option>)}
          </select>
        </label>
        <label>
          <span>标题</span>
          <input
            value={data.title}
            readOnly={readOnly}
            onChange={(event) => onUpdate(node.id, { title: event.target.value })}
          />
        </label>
        {data.type === "group" ? (
          <>
            <section className="evidence-inspector-section compact">
              <span>历史协议集合</span>
              <p>仅为旧图兼容保留。新证据请使用“实验设计”，Run 与数据身份放在 provenance。</p>
            </section>
            <div className="evidence-inspector-grid">
              <label>
                <span>协议版本</span>
                <input
                  value={data.version || ""}
                  readOnly
                  placeholder="例如 v3 / clean810"
                  onChange={(event) => onUpdate(node.id, { version: event.target.value })}
                />
              </label>
              <section className="evidence-inspector-section compact">
                <span>协议成员</span>
                <p>{members.length} 个节点；关系保留在成员之间或成员与集合外节点之间。</p>
              </section>
            </div>
            {members.length ? (
              <section className="evidence-inspector-members">
                <span>集合内容</span>
                <div>
                  {members.map((member, index) => (
                    <button type="button" key={member.id} onClick={() => onOpenMember(member.id)}>
                      <small>{String(index + 1).padStart(2, "0")} · {nodeTypeLabel(member.data.type, member.data.claimKind || member.data.claim_kind)}</small>
                      <strong>{member.data.title || member.id}</strong>
                      <ChevronRight size={13} />
                    </button>
                  ))}
                </div>
              </section>
            ) : null}
            <label>
              <span>来源摘要</span>
              <input
                value={data.provenanceSummary || ""}
                readOnly={readOnly}
                placeholder="split hash、seeds、数据版本或评估等级"
                onChange={(event) => onUpdate(node.id, { provenanceSummary: event.target.value })}
              />
            </label>
            <section className="evidence-inspector-section compact">
              <span>允许的成员</span>
              <p>数据版本、实验记录、计划、实验设计</p>
            </section>
          </>
		) : data.groupId ? (
		  <section className="evidence-inspector-section compact">
			<span>历史协议归属</span>
			<p>{groups.find((group) => group.id === data.groupId)?.data.title || data.groupId}（只读兼容）</p>
		  </section>
		) : null}
        <label className="evidence-inspector-body-field">
          <span>完整内容</span>
          <textarea
            value={data.body}
            readOnly={readOnly}
            onChange={(event) => onUpdate(node.id, { body: event.target.value })}
            placeholder={data.labels?.nodeBodyPlaceholder || "写下假说、计划、结果或笔记..."}
          />
        </label>
        {data.keyMetrics ? (
          <section className="evidence-inspector-section">
            <span>关键指标</span>
            <p>{data.keyMetrics}</p>
          </section>
        ) : null}
        {data.type === "run" ? (
          <section className="run-node-facts">
            <span><strong>Run</strong>{data.runId || "—"}</span>
            <span><strong>状态</strong>{data.status || "—"}</span>
            <span><strong>类型</strong>{data.runKind || "—"}</span>
            {data.evidenceLevel ? <span><strong>证据级别</strong>L{data.evidenceLevel}</span> : null}
            {gitLabel ? <span><strong>Git</strong>{gitLabel}</span> : null}
          </section>
        ) : null}
        {data.sourceRunIds?.length || data.sourceSnapshotIds?.length ? (
          <section className="evidence-inspector-section evidence-node-provenance">
            <span>结果来源</span>
            {data.sourceRunIds?.length ? (
              <div className="evidence-provenance-links">
                {data.sourceRunIds.map((runId) => (
                  <button type="button" key={runId} onClick={() => data.onOpenRun?.(runId)}>
                    <Network size={13} />
                    <code>{runId}</code>
                    <ArrowUpRight size={12} />
                  </button>
                ))}
              </div>
            ) : null}
            {data.sourceSnapshotIds?.length ? (
              <div className="evidence-provenance-snapshots">
                {data.sourceSnapshotIds.map((snapshotId) => <code key={snapshotId}>{snapshotId}</code>)}
              </div>
            ) : null}
          </section>
        ) : null}
        {data.type === "map_ref" ? (
          <section className="run-node-facts">
            <span><strong>目标图</strong>{data.target_map_id || "—"}</span>
            <span><strong>版本</strong>r{data.target_revision || "?"}</span>
            <span><strong>状态</strong>{data.mapRefStatus || "—"}</span>
          </section>
        ) : null}
      </div>
      <footer>
        <span>{readOnly ? "归档图只读" : "修改会自动保存"}</span>
        {data.type === "run" && data.runId ? (
          <button className="primary" type="button" onClick={() => data.onOpenRun?.(data.runId!)}>
            <ArrowUpRight size={14} />
            {data.labels?.openRun || "打开实验"}
          </button>
        ) : data.type === "map_ref" && data.target_map_id ? (
          <button className="primary" type="button" onClick={() => data.onOpenMap?.(data.target_map_id!)}>
            <ArrowUpRight size={14} />
            打开 Topic
          </button>
        ) : (
          <button type="button" onClick={onClose}>完成</button>
        )}
      </footer>
    </>
  );
}

function EvidenceEdgeInspector({
  edge,
  sourceTitle,
  targetTitle,
  onClose,
  onUpdate
}: {
  edge: EvidenceFlowEdge;
  sourceTitle: string;
  targetTitle: string;
  onClose: () => void;
  onUpdate: (edgeId: string, patch: { type?: EvidenceEdgeType; label?: string; rationale?: string }) => void;
}) {
  const type = edge.data?.type || "next_step";
  const readOnly = edge.data?.readOnly === true;

  return (
    <>
      <header>
        <div>
          <span className="evidence-inspector-kind" style={{ "--evidence-color": evidenceColor(type) } as CSSProperties}>
            <Route size={13} />
            证据关系
          </span>
          <h2>{text(edge.label) || edgeTypeLabel(type)}</h2>
          <code>{edge.id}</code>
        </div>
        <button className="icon-button" type="button" aria-label="关闭详情" title="关闭详情" onClick={onClose}>
          <X size={16} />
        </button>
      </header>
      <div className="evidence-selection-body inspector-form">
        <section className="evidence-edge-endpoints">
          <span>{sourceTitle}</span>
          <ChevronRight size={15} />
          <span>{targetTitle}</span>
        </section>
        <label>
          <span>关系类型</span>
          <select
            disabled={readOnly}
            value={type}
            onChange={(event) => {
              const nextType = event.target.value as EvidenceEdgeType;
              onUpdate(edge.id, { type: nextType, label: edgeTypeLabel(nextType) });
            }}
          >
            {evidenceEdgeTypes.map((edgeType) => <option key={edgeType} value={edgeType}>{edgeTypeLabel(edgeType)}</option>)}
          </select>
        </label>
        <label>
          <span>画布标签</span>
          <input
            value={text(edge.label)}
            readOnly={readOnly}
            onChange={(event) => onUpdate(edge.id, { label: event.target.value })}
          />
        </label>
        <label className="evidence-inspector-body-field">
          <span>关系说明</span>
          <textarea
            value={edge.data?.rationale || ""}
            readOnly={readOnly}
            onChange={(event) => onUpdate(edge.id, { rationale: event.target.value })}
            placeholder={edge.data?.labels?.rationale || "解释这条关系为什么成立"}
          />
        </label>
      </div>
      <footer>
        <span>{readOnly ? "归档图只读" : "修改会自动保存"}</span>
        <button type="button" onClick={onClose}>完成</button>
      </footer>
    </>
  );
}

function EvidenceNode({ data, selected }: NodeProps<EvidenceFlowNode>) {
  const color = evidenceColor(data.type);
  const summary = text(data.keyMetrics || data.summary || data.body).trim();
  const status = data.type === "map_ref"
    ? data.mapRefStatus === "stale" ? "有更新" : data.mapRefStatus === "archived" ? "已归档" : data.mapRefStatus === "missing" ? "不可用" : "已同步"
    : data.status || data.runKind || "";

  return (
    <div
      className={`evidence-node ${data.type} ${data.draft ? "evidence-node--draft" : ""} ${selected ? "selected" : ""}`}
      style={{ "--evidence-color": color } as CSSProperties}
    >
      {!data.draft && !data.readOnly ? <EvidenceNodeHandles /> : null}
      <div className="evidence-node-category">
        {data.draft ? (
          <span className="evidence-node-draft-label"><Bot size={12} />Agent 草稿 · {nodeTypeLabel(data.type, data.claimKind || data.claim_kind)}</span>
        ) : data.type === "map_ref" ? (
          <span className="evidence-node-draft-label"><Network size={12} />{nodeTypeLabel(data.type, data.claimKind || data.claim_kind)}</span>
        ) : (
          <span>{nodeTypeLabel(data.type, data.claimKind || data.claim_kind)}</span>
        )}
        {data.evidenceLevel ? <span>L{data.evidenceLevel}</span> : null}
      </div>
      <div className="evidence-color-line" />
      <h3 className="evidence-node-title">{data.title || nodeTypeLabel(data.type, data.claimKind || data.claim_kind)}</h3>
      <p className={summary ? "evidence-node-summary" : "evidence-node-summary muted"}>
        {summary || "点击查看并补充完整内容"}
      </p>
      <footer className="evidence-node-meta">
        {status ? <span>{status}</span> : <span>查看详情</span>}
        {data.type === "run" && data.runId ? <code>{data.runId}</code> : null}
        {data.sourceRunIds?.length ? <code>{data.sourceRunIds.length} Runs</code> : null}
        {data.sourceSnapshotIds?.length ? <code>{data.sourceSnapshotIds.length} Snapshots</code> : null}
        {data.type === "map_ref" && data.target_revision ? <code>r{data.target_revision}</code> : null}
      </footer>
    </div>
  );
}

function EvidenceNodeHandles() {
  return (
    <>
      {evidenceHandleSides.map((side) => (
        <Handle
          key={`source-${side}`}
          id={handleId("source", side)}
          className={`evidence-handle evidence-handle-${side} evidence-handle-source`}
          type="source"
          position={side}
          aria-label={`从节点${side}侧发出关系`}
          title="出边"
        />
      ))}
      {evidenceHandleSides.map((side) => (
        <Handle
          key={`target-${side}`}
          id={handleId("target", side)}
          className={`evidence-handle evidence-handle-${side} evidence-handle-target`}
          type="target"
          position={side}
          aria-label={`从节点${side}侧接收关系`}
          title="入边"
        />
      ))}
    </>
  );
}

function gitNodeLabel(data: EvidenceNodeData) {
  if (!data.gitCommit) return "";
  const commit = data.gitCommit.length > 12 ? data.gitCommit.slice(0, 12) : data.gitCommit;
  const ref = data.gitBranch ? `${data.gitBranch}@${commit}` : commit;
  return data.gitDirty ? `${ref} dirty` : ref;
}

function EvidenceEdge({ id, sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, markerEnd, data, label, selected }: EdgeProps<EvidenceFlowEdge>) {
  const type = data?.type || "next_step";
  const isDraft = data?.draft === true;
  const [hovered, setHovered] = useState(false);
  const [fallbackPath, fallbackLabelX, fallbackLabelY] = getSmoothStepPath({
    sourceX,
    sourceY,
    targetX,
    targetY,
    sourcePosition,
    targetPosition,
    borderRadius: 18,
    offset: 30 + Math.min(Math.max(data?.routeLane || 0, 0), 6) * 9
  });
  const routedPoints = data?.routePoints && data.routePoints.length >= 2
    ? data.routePoints.map((point, index, points) => (
        index === 0
          ? { x: sourceX, y: sourceY }
          : index === points.length - 1
            ? { x: targetX, y: targetY }
            : point
      ))
    : null;
  const edgePath = routedPoints ? evidenceOrthogonalPath(routedPoints) : fallbackPath;
  const labelX = data?.routeLabelPoint?.x ?? fallbackLabelX;
  const labelY = data?.routeLabelPoint?.y ?? fallbackLabelY;
  const labelSafe = data?.routeSafe === true && Boolean(data.routeLabelPoint);
  const displayLabel = text(label) || edgeTypeLabel(type);
  const labelVisible = selected || hovered || isDraft || data?.focusVisible === true;
  const baseStyle = edgeStyle(type);
  const emphasizedStyle = selected || hovered
    ? {
        ...baseStyle,
        strokeWidth: Number(baseStyle.strokeWidth || 2.4) + 0.9,
        strokeOpacity: 1,
        filter: `drop-shadow(0 1px 2px ${String(baseStyle.stroke)}55)`
      }
    : baseStyle;

  return (
    <>
      <BaseEdge
        path={edgePath}
        markerEnd={markerEnd}
        style={isDraft ? { ...emphasizedStyle, strokeDasharray: "7 6", strokeOpacity: 0.58 } : emphasizedStyle}
      />
      <path
        className="evidence-edge-hitarea"
        d={edgePath}
        fill="none"
        stroke="transparent"
        strokeWidth={18}
        onPointerEnter={() => setHovered(true)}
        onPointerLeave={() => setHovered(false)}
        onClick={(event) => {
          event.stopPropagation();
          if (!isDraft) data?.onSelectEdge?.(id);
        }}
      />
      <EdgeLabelRenderer>
        <div
          className={`evidence-edge-label ${labelSafe ? "is-safe" : ""} ${labelVisible ? "is-visible" : ""} ${isDraft ? "evidence-edge-label--draft" : ""} nodrag nopan`}
          style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`, "--evidence-color": evidenceColor(type) } as CSSProperties}
          onPointerEnter={() => setHovered(true)}
          onPointerLeave={() => setHovered(false)}
        >
          <button
            type="button"
            onClick={(event) => {
              event.stopPropagation();
              if (!isDraft) data?.onSelectEdge?.(id);
            }}
            title={isDraft ? `${edgeTypeLabel(type)} · Agent 草稿` : `${edgeTypeLabel(type)} · 点击查看详情`}
          >
            {isDraft ? `草稿 · ${displayLabel}` : displayLabel}
          </button>
        </div>
      </EdgeLabelRenderer>
    </>
  );
}

function EvidenceFocusEdge({ id, sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, markerEnd, data, label, selected }: EdgeProps<EvidenceFlowEdge>) {
  const type = data?.type || "next_step";
  const [hovered, setHovered] = useState(false);
  const adjustedSourceX = sourceX + (data?.focusSourceOffsetX || 0);
  const adjustedSourceY = sourceY + (data?.focusSourceOffsetY || 0);
  const adjustedTargetX = targetX + (data?.focusTargetOffsetX || 0);
  const adjustedTargetY = targetY + (data?.focusTargetOffsetY || 0);
  const [edgePath, defaultLabelX, defaultLabelY] = getBezierPath({
    sourceX: adjustedSourceX,
    sourceY: adjustedSourceY,
    targetX: adjustedTargetX,
    targetY: adjustedTargetY,
    sourcePosition,
    targetPosition,
    curvature: data?.focusContext ? 0.22 : 0.16
  });
  const labelX = data?.focusContext
    ? adjustedSourceX + (adjustedTargetX - adjustedSourceX) * 0.28
    : defaultLabelX;
  const labelY = data?.focusContext
    ? adjustedSourceY + Math.min(34, Math.max(24, (adjustedTargetY - adjustedSourceY) * 0.18))
    : defaultLabelY - 18;
  const displayLabel = text(label) || edgeTypeLabel(type);
  const baseStyle = edgeStyle(type);
  const idleStyle = data?.focusContext
    ? { ...baseStyle, strokeWidth: 1.8, strokeOpacity: 0.66 }
    : { ...baseStyle, strokeWidth: Math.max(2.6, Number(baseStyle.strokeWidth || 2.4)), strokeOpacity: 0.94 };
  const emphasizedStyle = selected || hovered
    ? {
        ...baseStyle,
        strokeWidth: Number(baseStyle.strokeWidth || 2.4) + 0.8,
        strokeOpacity: 1,
        filter: `drop-shadow(0 1px 2px ${String(baseStyle.stroke)}44)`
      }
    : idleStyle;

  return (
    <>
      <BaseEdge path={edgePath} markerEnd={markerEnd} style={emphasizedStyle} />
      <path
        className="evidence-edge-hitarea"
        d={edgePath}
        fill="none"
        stroke="transparent"
        strokeWidth={18}
        onPointerEnter={() => setHovered(true)}
        onPointerLeave={() => setHovered(false)}
        onClick={(event) => {
          event.stopPropagation();
          data?.onSelectEdge?.(id);
        }}
      />
      <EdgeLabelRenderer>
        <div
          className={`evidence-edge-label ${data?.focusContext ? "is-context" : "is-causal is-visible"} ${selected || hovered ? "is-visible" : ""} nodrag nopan`}
          style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`, "--evidence-color": evidenceColor(type) } as CSSProperties}
          onPointerEnter={() => setHovered(true)}
          onPointerLeave={() => setHovered(false)}
        >
          <button
            type="button"
            onClick={(event) => {
              event.stopPropagation();
              data?.onSelectEdge?.(id);
            }}
            title={`${edgeTypeLabel(type)} · 点击查看详情`}
          >
            {displayLabel}
          </button>
        </div>
      </EdgeLabelRenderer>
    </>
  );
}

function handleId(kind: "source" | "target", side: EvidenceHandleSide) {
  return `${kind}-${side}`;
}

function evidenceColor(type: EvidenceNodeType | EvidenceEdgeType) {
  switch (type) {
    case "dataset":
      return "#587b67";
    case "protocol":
      return "#627c9b";
    case "run":
      return "#4f6f8f";
    case "claim":
      return "#32664b";
    case "issue":
      return "#bd6b36";
    case "hypothesis":
      return "#5d6d9b";
    case "experiment":
      return "#2f6f5e";
    case "plan":
      return "#56616d";
    case "conclusion":
      return "#32664b";
    case "note":
      return "#6f6a60";
    case "map_ref":
      return "#725b9b";
    case "group":
      return "#486f91";
    case "supports":
      return "#23805a";
    case "weakens":
    case "does_not_prove":
      return "#b94a48";
    case "uses":
      return "#3f6f91";
    case "reveals_issue":
      return "#c46a2f";
    case "supersedes":
      return "#7255aa";
    case "related_to":
    case "custom":
      return "#66717e";
    case "next_step":
    default:
      return "#466fa3";
  }
}
