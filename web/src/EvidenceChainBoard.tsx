import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
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
import { AlertTriangle, Archive, ArrowUpRight, Bot, Check, ChevronDown, ChevronLeft, ChevronRight, Copy, Eye, ListPlus, Network, PinOff, Plus, RefreshCcw, Route, Save, Search, Trash2, X } from "lucide-react";
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
  candidateMatches,
  candidateRunNodeFields,
  candidateToNode,
  buildEvidenceEditPatch,
  createTextNode,
  defaultEvidenceRelation,
  evidenceMapReferenceStatus,
  evidenceProposalPreview,
  evidenceWorkspaceProposalPreview,
  evidenceAutoHandlePair,
  evidenceMarkerEnd,
  edgeStyle,
  edgeTypeLabel,
  evidenceEdgeTypes,
  evidenceNodeTypes,
  filterRunCandidatesForProject,
  groupRunCandidatesByProject,
  nodeTypeLabel,
  serializeEvidenceGraph,
  layoutEvidenceGraph,
  type EvidenceEdgeData,
  type EvidenceFlowEdge,
  type EvidenceFlowNode,
  type EvidenceHandleSide,
  type EvidenceNodeData
} from "./evidenceChain";
import type { I18nKey } from "./i18n";
import { readEvidenceMapFromSearch, withEvidenceMapSearch } from "./projectRoute";
import type { EvidenceChainDetail, EvidenceChainRunCandidate, EvidenceEdgeType, EvidenceNodeType, EvidenceProposal } from "./types";
import { fmtShortTime, text } from "./utils";

const candidateMime = "application/x-aexp-run-candidate";
const evidenceHandleSides = [Position.Top, Position.Right, Position.Bottom, Position.Left] as const;
const compactEvidenceNodeSize = { width: 306, height: 138 };
type ProposalPreview = NonNullable<ReturnType<typeof evidenceProposalPreview>>;
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
  const [selectedChainId, setSelectedChainId] = useState(() => (
    projectId && typeof window !== "undefined" ? readEvidenceMapFromSearch(window.location.search) : ""
  ));
  const [selected, setSelected] = useState<{ kind: "node" | "edge"; id: string } | null>(null);
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
  const [reviewingRunID, setReviewingRunID] = useState("");
  const [promoting, setPromoting] = useState(false);
  const [draftDockOpen, setDraftDockOpen] = useState(false);
  const [previewRunID, setPreviewRunID] = useState("");
  const lastSavedMeta = useRef<{ id: string; title: string; description: string; routingHintsKey: string } | null>(null);
  const [chainMetaComposing, setChainMetaComposing] = useState(false);
  const [nodes, setNodes, onNodesChangeBase] = useNodesState<EvidenceFlowNode>([]);
  const [edges, setEdges, onEdgesChangeBase] = useEdgesState<EvidenceFlowEdge>([]);
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
    retry: false,
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
  const selectChain = useCallback((chainId: string, replace = false) => {
    if (chainId !== selectedChainId) {
      appliedDetailRef.current = null;
      setNodes([]);
      setEdges([]);
      setSelected(null);
      setNodeMenuOpen(false);
      setRunTrayOpen(false);
      setRoutingOpen(false);
      setPreviewRunID("");
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
  const candidates = useQuery({
    queryKey: ["evidence-run-candidates", token, projectId],
    queryFn: () => getEvidenceRunCandidates(token, projectId, 160),
    enabled: Boolean(selectedChainId),
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
      // Do not treat a deep-linked Map as invalid while the Project Map list is
      // still loading. The Primary query often resolves first.
      if (!chains.data) return;
      const projectChains = orderedChains;
      if (selectedChainId && projectChains.some((chain) => chain.id === selectedChainId)) return;
      const preferred = primaryMap.data?.id || projectChains.find((chain) => chain.role === "primary" && chain.status === "active")?.id || projectChains[0]?.id;
      if (preferred) {
        setSelectedChainId(preferred);
        if (selectedChainId && typeof window !== "undefined") {
          const primaryId = primaryMap.data?.id || projectChains.find((chain) => chain.role === "primary" && chain.status === "active")?.id || "";
          const search = withEvidenceMapSearch(window.location.search, preferred === primaryId ? "" : preferred);
          window.history.replaceState(window.history.state, "", `${window.location.pathname}${search}${window.location.hash}`);
        }
      }
      return;
    }
    if (!projectId && !selectedChainId && chains.data?.length) setSelectedChainId(chains.data[0].id);
  }, [chains.data, orderedChains, primaryMap.data, projectId, selectedChainId]);

  useEffect(() => {
    setSelectedChainId(projectId && typeof window !== "undefined" ? readEvidenceMapFromSearch(window.location.search) : "");
    appliedDetailRef.current = null;
    setLeftOpen(false);
    setRunTrayOpen(false);
    setSelectedCandidateGroup("");
    setPreviewRunID("");
  }, [projectId]);

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
      markDirty();
    },
    [markDirty, setNodes]
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
    return {
      ...node,
      width: compactEvidenceNodeSize.width,
      height: compactEvidenceNodeSize.height,
      style: { ...node.style, ...compactEvidenceNodeSize },
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
  const activeCanvasPreview = activePreviewEntry?.preview.chainId === selectedChainId ? activePreviewEntry : null;
  const previewPlan = useQuery({
    queryKey: ["evidence-proposal-plan", token, previewRunID],
    queryFn: () => activePreviewEntry?.kind === "workspace"
      ? planEvidenceProposal(token, activePreviewEntry.key)
      : planEvidenceGraphProposal(token, activePreviewEntry?.key || previewRunID),
    enabled: Boolean(previewRunID && activePreviewEntry),
    retry: false
  });

  useEffect(() => {
    if (previewRunID && !activePreviewEntry) setPreviewRunID("");
  }, [activePreviewEntry, previewRunID]);

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
    const nextNodes = layoutEvidenceGraph(rawNodes, rawEdges).map(withNodeHandlers);
    const nextEdges = autoRouteEvidenceEdges(rawEdges.map(withEdgeHandlers), nextNodes);
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
  useEffect(() => {
    setEdges((current) => autoRouteEvidenceEdges(current, nodes));
  }, [nodes, setEdges]);

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
    setNodes((current) => [...current, node]);
    setSelected({ kind: "node", id: node.id });
    setNodeMenuOpen(false);
    markDirty();
  };

  const arrangeGraph = useCallback((resetPins = false) => {
    setNodes((current) => layoutEvidenceGraph(current, edges, resetPins).map(withNodeHandlers));
    markDirty();
    window.requestAnimationFrame(() => flow.fitView({ padding: 0.18, duration: 240 }));
  }, [edges, flow, markDirty, setNodes, withNodeHandlers]);

  const nodeTypes = useMemo(() => ({ evidence: EvidenceNode }), []);
  const edgeTypes = useMemo(() => ({ evidence: EvidenceEdge }), []);
  const visibleNodes = useMemo(() => {
    if (!activeCanvasPreview) return nodes;
    const acceptedPinned = nodes.map((node) => ({ ...node, data: { ...node.data, pinned: true } }));
    const mergedEdges = [...edges, ...activeCanvasPreview.preview.edges];
    const laidOut = layoutEvidenceGraph([...acceptedPinned, ...activeCanvasPreview.preview.nodes], mergedEdges);
    const draftNodes = laidOut.filter((node) => node.data.draft === true).map(withNodeHandlers);
    return [...nodes, ...draftNodes];
  }, [activeCanvasPreview, edges, nodes, withNodeHandlers]);
  const visibleEdges = useMemo(
    () => autoRouteEvidenceEdges(
      activeCanvasPreview ? [...edges, ...activeCanvasPreview.preview.edges] : edges,
      visibleNodes
    ),
    [activeCanvasPreview, edges, visibleNodes]
  );
  const selectedNode = useMemo(
    () => selected?.kind === "node" ? nodes.find((node) => node.id === selected.id) || null : null,
    [nodes, selected]
  );
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

  useEffect(() => {
    if (!activeCanvasPreview) return;
    const timer = window.setTimeout(() => {
      void flow.fitView({
        nodes: activeCanvasPreview.preview.nodes.map((node) => ({ id: node.id })),
        padding: 0.32,
        duration: reduceMotion ? 0 : 260,
        maxZoom: 1
      });
    }, reduceMotion ? 0 : 80);
    return () => window.clearTimeout(timer);
  }, [activeCanvasPreview, flow, reduceMotion]);

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
        setProposalNotice(`${t("proposalAcceptedInto")}：${targetLabel}`);
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
  }, [orderedChains, queryClient, selectedChainId, t, token]);

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
      const type: EvidenceEdgeType = defaultEvidenceRelation(sourceType, targetType);
      const hasManualHandles = Boolean(connection.sourceHandle && connection.targetHandle);
      setEdges((current) =>
        autoRouteEvidenceEdges(
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
      setNodes((current) => current.filter((node) => node.id !== nodeId));
      setEdges((current) => current.filter((edge) => edge.source !== nodeId && edge.target !== nodeId));
    } else {
      const edgeId = selected.id;
      setEdges((current) => current.filter((edge) => edge.id !== edgeId));
    }
    setSelected(null);
    markDirty();
  }, [detail.data?.status, markDirty, selected, setEdges, setNodes]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (!selected || detail.data?.status === "archived" || (event.key !== "Delete" && event.key !== "Backspace")) return;
      if (isEditableTarget(event.target)) return;
      event.preventDefault();
      deleteSelection();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [deleteSelection, detail.data?.status, selected]);

  const onNodeClick: NodeMouseHandler<EvidenceFlowNode> = (_event, node) => {
    if (node.data.draft && node.data.proposalRunId) {
      setPreviewRunID(node.data.proposalRunId);
      setRunTrayOpen(false);
      clearSelection();
      return;
    }
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
    const node = withNodeHandlers(candidateToNode(candidate, flow.screenToFlowPosition({ x: event.clientX, y: event.clientY })));
    setNodes((current) => [...current, node]);
    setSelected({ kind: "node", id: node.id });
    markDirty();
  };

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
        </div>
        {selectedChainId ? (
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
                  {evidenceNodeTypes.map((type) => (
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
                title={t("deleteElementTitle")}
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
        {projectId && !selectedChainId && !chains.isLoading && !primaryMap.isLoading ? (
          <div className="evidence-primary-empty">
            <strong>{t("noProjectEvidenceGraph")}</strong>
            <span>{primaryMap.error instanceof Error ? primaryMap.error.message : t("primaryGraphAgentHint")}</span>
            <button className="primary" disabled={createChain.isPending} onClick={() => createChain.mutate()}>
              <Plus size={14} />
              {createChain.isPending ? t("creating") : t("createPrimaryEvidenceGraph")}
            </button>
          </div>
        ) : null}
        <div className="evidence-canvas" onDragOver={(event) => event.preventDefault()} onDrop={onDrop}>
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
            onNodeDragStop={(_event, node) => {
              if (node.data.draft) return;
              setNodes((current) => current.map((item) => item.id === node.id ? { ...item, data: { ...item.data, pinned: true } } : item));
              markDirty();
            }}
            onPaneClick={clearSelection}
            connectionMode={ConnectionMode.Loose}
            nodesDraggable={detail.data?.status !== "archived"}
            nodesConnectable={detail.data?.status !== "archived"}
            fitView
          >
            <Background gap={24} color="#dce2e8" />
            <Controls />
            <MiniMap pannable zoomable />
          </ReactFlow>
          {selectedChainId
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
                        <span>{preview.nodes.length} 节点 · {preview.edges.length} 关系</span>
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
                onClose={clearSelection}
                onUpdate={updateNodeData}
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
              <button className="icon-button" type="button" aria-label="收起预览" title="收起预览" onClick={() => setPreviewRunID("")}>
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
                    <span>草稿未叠加到当前画布；先确认它的目标图或让 Agent 重新路由。</span>
                  </div>
                ) : null}
              </section>
              {activePreviewEntry.preview.routingReason ? (
                <section>
                  <span className="evidence-proposal-kicker">为什么放在这里</span>
                  <p>{activePreviewEntry.preview.routingReason}</p>
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
                  <div className="evidence-proposal-ready"><Check size={15} />检查通过，可接受到正式图</div>
                ) : null}
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
                接受到正式图
              </button>
            </footer>
          </motion.aside>
        ) : null}
      </AnimatePresence>
    </div>
  );
}

function isEditableTarget(target: EventTarget | null) {
  if (!(target instanceof HTMLElement)) return false;
  return target.isContentEditable || target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement;
}

function evidenceDetailApplyKey(detail: EvidenceChainDetail) {
  const nodeKey = (detail.nodes || [])
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
  const edgeKey = (detail.edges || [])
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

function EvidenceNodeInspector({
  node,
  onClose,
  onUpdate
}: {
  node: EvidenceFlowNode;
  onClose: () => void;
  onUpdate: (nodeId: string, patch: Partial<EvidenceNodeData>) => void;
}) {
  const data = node.data;
  const typeOptions: EvidenceNodeType[] = data.type === "run"
    ? ["run", ...evidenceNodeTypes]
    : data.type === "map_ref"
      ? ["map_ref"]
      : evidenceNodeTypes;
  const readOnly = data.readOnly === true;
  const gitLabel = gitNodeLabel(data);

  return (
    <>
      <header>
        <div>
          <span className="evidence-inspector-kind" style={{ "--evidence-color": evidenceColor(data.type) } as CSSProperties}>
            {data.draft ? <Bot size={13} /> : <Network size={13} />}
            {nodeTypeLabel(data.type)}
          </span>
          <h2>{data.title || nodeTypeLabel(data.type)}</h2>
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

function EvidenceNode({ id, data, selected }: NodeProps<EvidenceFlowNode>) {
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
          <span className="evidence-node-draft-label"><Bot size={12} />Agent 草稿 · {nodeTypeLabel(data.type)}</span>
        ) : data.type === "map_ref" ? (
          <span className="evidence-node-draft-label"><Network size={12} />{nodeTypeLabel(data.type)}</span>
        ) : (
          <span>{nodeTypeLabel(data.type)}</span>
        )}
        {data.evidenceLevel ? <span>L{data.evidenceLevel}</span> : null}
      </div>
      <div className="evidence-color-line" />
      <h3 className="evidence-node-title">{data.title || nodeTypeLabel(data.type)}</h3>
      <p className={summary ? "evidence-node-summary" : "evidence-node-summary muted"}>
        {summary || "点击查看并补充完整内容"}
      </p>
      <footer className="evidence-node-meta">
        {status ? <span>{status}</span> : <span>查看详情</span>}
        {data.type === "run" && data.runId ? <code>{data.runId}</code> : null}
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
  const [edgePath, labelX, labelY] = getSmoothStepPath({
    sourceX,
    sourceY,
    targetX,
    targetY,
    sourcePosition,
    targetPosition,
    borderRadius: 18,
    offset: 30 + Math.min(Math.max(data?.routeLane || 0, 0), 6) * 9
  });
  const displayLabel = text(label) || edgeTypeLabel(type);
  const labelVisible = selected || hovered || isDraft;
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
          className={`evidence-edge-label ${labelVisible ? "is-visible" : ""} ${isDraft ? "evidence-edge-label--draft" : ""} nodrag nopan`}
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

function autoRouteEvidenceEdges(edges: EvidenceFlowEdge[], nodes: EvidenceFlowNode[]): EvidenceFlowEdge[] {
  const nodeById = new Map(nodes.map((node) => [node.id, node]));
  const routed = edges.map((edge) => {
    const type = edge.data?.type || "next_step";
    const data: EvidenceEdgeData = { ...(edge.data ?? {}), type, rationale: edge.data?.rationale || "" };
    const base: EvidenceFlowEdge = {
      ...edge,
      type: "evidence",
      animated: false,
      markerEnd: evidenceMarkerEnd(type),
      style: edgeStyle(type),
      data
    };
    const shouldAutoRoute = base.data?.autoHandles === true || !base.sourceHandle || !base.targetHandle;
    if (!shouldAutoRoute) return base;
    const source = nodeById.get(base.source);
    const target = nodeById.get(base.target);
    if (!source || !target) return base;
    const handles = chooseEvidenceHandles(source, target);
    const routedData: EvidenceEdgeData = { ...data, autoHandles: true };
    return {
      ...base,
      sourceHandle: handleId("source", handles.source),
      targetHandle: handleId("target", handles.target),
      data: routedData
    };
  });
  const routeLane = new Map<string, number>();
  const assignLanes = (
    keyFor: (edge: EvidenceFlowEdge) => string,
    sortFor: (edge: EvidenceFlowEdge) => number
  ) => {
    const groups = new Map<string, EvidenceFlowEdge[]>();
    for (const edge of routed) {
      if (edge.data?.autoHandles !== true) continue;
      const key = keyFor(edge);
      groups.set(key, [...(groups.get(key) || []), edge]);
    }
    for (const group of groups.values()) {
      group.sort((left, right) => sortFor(left) - sortFor(right) || left.id.localeCompare(right.id));
      group.forEach((edge, index) => routeLane.set(edge.id, Math.max(routeLane.get(edge.id) || 0, index)));
    }
  };
  assignLanes(
    (edge) => `${edge.source}:${edge.sourceHandle || ""}`,
    (edge) => {
      const target = nodeById.get(edge.target);
      return target ? nodeCenter(target).y : 0;
    }
  );
  assignLanes(
    (edge) => `${edge.target}:${edge.targetHandle || ""}`,
    (edge) => {
      const source = nodeById.get(edge.source);
      return source ? nodeCenter(source).y : 0;
    }
  );
  return routed.map((edge) => ({
    ...edge,
    data: { ...edge.data!, routeLane: routeLane.get(edge.id) || 0 }
  }));
}

function chooseEvidenceHandles(source: EvidenceFlowNode, target: EvidenceFlowNode): { source: EvidenceHandleSide; target: EvidenceHandleSide } {
  return evidenceAutoHandlePair(nodeCenter(source), nodeCenter(target));
}

function nodeCenter(node: EvidenceFlowNode) {
  const measured = node.measured as { width?: number; height?: number } | undefined;
  const width = measured?.width || node.width || 286;
  const height = measured?.height || node.height || 184;
  return { x: node.position.x + width / 2, y: node.position.y + height / 2 };
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
