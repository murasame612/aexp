import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
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
  type NodeMouseHandler
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { ChevronLeft, ChevronRight, ListPlus, Plus, RefreshCcw, Save, Search, Trash2 } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createEvidenceChain,
  deleteEvidenceChain,
  getEvidenceChain,
  getEvidenceChains,
  getEvidenceRunCandidates,
  saveEvidenceChainGraph,
  updateEvidenceChain
} from "./api";
import {
  apiEdgeToFlowEdge,
  apiNodeToFlowNode,
  candidateMatches,
  candidateRunNodeFields,
  candidateToNode,
  createTextNode,
  evidenceMarkerEnd,
  edgeStyle,
  edgeTypeLabel,
  evidenceEdgeTypes,
  evidenceNodeTypes,
  groupRunCandidatesByProject,
  nodeTypeLabel,
  serializeEvidenceGraph,
  type EvidenceEdgeData,
  type EvidenceFlowEdge,
  type EvidenceFlowNode,
  type EvidenceNodeData
} from "./evidenceChain";
import type { I18nKey } from "./i18n";
import type { EvidenceChainRunCandidate, EvidenceEdgeType, EvidenceNodeType } from "./types";
import { fmtShortTime, text } from "./utils";

const candidateMime = "application/x-aexp-run-candidate";
const evidenceHandleSides = [Position.Top, Position.Right, Position.Bottom, Position.Left] as const;
type EvidenceHandleSide = (typeof evidenceHandleSides)[number];

export function EvidenceChainBoard({ token, t, onOpenRun }: { token: string; t: (key: I18nKey) => string; onOpenRun: (id: string) => void }) {
  return (
    <ReactFlowProvider>
      <EvidenceChainWorkspace token={token} t={t} onOpenRun={onOpenRun} />
    </ReactFlowProvider>
  );
}

function EvidenceChainWorkspace({ token, t, onOpenRun }: { token: string; t: (key: I18nKey) => string; onOpenRun: (id: string) => void }) {
  const queryClient = useQueryClient();
  const flow = useReactFlow<EvidenceFlowNode, EvidenceFlowEdge>();
  const [chainQuery, setChainQuery] = useState("");
  const [candidateQuery, setCandidateQuery] = useState("");
  const [selectedChainId, setSelectedChainId] = useState("");
  const [selected, setSelected] = useState<{ kind: "node" | "edge"; id: string } | null>(null);
  const [dirty, setDirty] = useState(false);
  const [saveState, setSaveState] = useState<"idle" | "saved" | "saving" | "failed">("idle");
  const [chainTitleDraft, setChainTitleDraft] = useState("");
  const [chainDescriptionDraft, setChainDescriptionDraft] = useState("");
  const [leftOpen, setLeftOpen] = useState(() => (typeof window === "undefined" ? true : !window.matchMedia("(max-width: 760px)").matches));
  const [runTrayOpen, setRunTrayOpen] = useState(false);
  const [nodeMenuOpen, setNodeMenuOpen] = useState(false);
  const [selectedCandidateGroup, setSelectedCandidateGroup] = useState("");
  const lastSavedMeta = useRef<{ id: string; title: string; description: string } | null>(null);
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

  const chains = useQuery({ queryKey: ["evidence-chains", token, chainQuery], queryFn: () => getEvidenceChains(token, chainQuery), refetchInterval: 15000 });
  const detail = useQuery({ queryKey: ["evidence-chain", token, selectedChainId], queryFn: () => getEvidenceChain(token, selectedChainId), enabled: !!selectedChainId });
  const candidates = useQuery({
    queryKey: ["evidence-run-candidates", token, candidateQuery],
    queryFn: () => getEvidenceRunCandidates(token, candidateQuery, 160),
    enabled: runTrayOpen,
    refetchInterval: 12000
  });

  useEffect(() => {
    if (!selectedChainId && chains.data?.length) setSelectedChainId(chains.data[0].id);
  }, [chains.data, selectedChainId]);

  const markDirty = useCallback(() => {
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
            animated: type === "next_step",
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
  const withNodeHandlers = useCallback((node: EvidenceFlowNode): EvidenceFlowNode => ({ ...node, data: { ...node.data, labels: boardLabels, onOpenRun, onUpdateNode: updateNodeData } }), [boardLabels, onOpenRun, updateNodeData]);
  const withEdgeHandlers = useCallback(
    (edge: EvidenceFlowEdge): EvidenceFlowEdge => {
      const type = edge.data?.type || "next_step";
      return {
        ...edge,
        type: "evidence",
        animated: type === "next_step",
        markerEnd: evidenceMarkerEnd(type),
        style: edgeStyle(type),
        data: { type, rationale: edge.data?.rationale || "", ...edge.data, labels: boardLabels, onSelectEdge: selectEdge, onUpdateEdge: updateEdgeData }
      };
    },
    [boardLabels, selectEdge, updateEdgeData]
  );

  useEffect(() => {
    setNodes((current) => current.map((node) => ({ ...node, data: { ...node.data, labels: boardLabels } })));
    setEdges((current) => current.map((edge) => ({ ...edge, data: { type: edge.data?.type || "next_step", rationale: edge.data?.rationale || "", ...edge.data, labels: boardLabels } })));
  }, [boardLabels, setEdges, setNodes]);

  const candidateByRunId = useMemo(() => {
    const out = new Map<string, EvidenceChainRunCandidate>();
    for (const candidate of candidates.data || []) {
      if (!candidate.run_id || out.has(candidate.run_id)) continue;
      out.set(candidate.run_id, candidate);
    }
    return out;
  }, [candidates.data]);

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
    setNodes((current) => current.map(hydrateRunNodeFromCandidate));
  }, [candidateByRunId, hydrateRunNodeFromCandidate, setNodes]);

  useEffect(() => {
    if (!detail.data) return;
    const nextNodes = (detail.data.nodes || []).map(apiNodeToFlowNode).map(hydrateRunNodeFromCandidate).map(withNodeHandlers);
    const nextEdges = autoRouteEvidenceEdges((detail.data.edges || []).map(apiEdgeToFlowEdge).map(withEdgeHandlers), nextNodes);
    setNodes(nextNodes);
    setEdges(nextEdges);
    setChainTitleDraft(detail.data.title || "");
    setChainDescriptionDraft(detail.data.description || "");
    lastSavedMeta.current = { id: detail.data.id, title: detail.data.title || "", description: detail.data.description || "" };
    setSelected(null);
    setDirty(false);
    setSaveState("saved");
  }, [detail.data, hydrateRunNodeFromCandidate, setEdges, setNodes, withEdgeHandlers, withNodeHandlers]);

  const createChain = useMutation({
    mutationFn: () => createEvidenceChain(token, { title: t("newEvidenceChain"), description: "" }),
    onSuccess: async (chain) => {
      await queryClient.invalidateQueries({ queryKey: ["evidence-chains"] });
      setSelectedChainId(chain.id);
    }
  });
  const renameChain = useMutation({
    mutationFn: (payload: { id: string; title: string; description: string }) => updateEvidenceChain(token, payload.id, { title: payload.title, description: payload.description }),
    onSuccess: async (chain) => {
      lastSavedMeta.current = { id: chain.id, title: chain.title || "", description: chain.description || "" };
      await queryClient.invalidateQueries({ queryKey: ["evidence-chains"] });
    }
  });
  const removeChain = useMutation({
    mutationFn: (id: string) => deleteEvidenceChain(token, id),
    onSuccess: async () => {
      setSelectedChainId("");
      setSelected(null);
      setNodes([]);
      setEdges([]);
      await queryClient.invalidateQueries({ queryKey: ["evidence-chains"] });
    }
  });

  const onNodesChange = useCallback(
    (changes: Parameters<typeof onNodesChangeBase>[0]) => {
      onNodesChangeBase(changes);
      if (changes.some((change) => change.type === "remove" && selected?.kind === "node" && change.id === selected.id)) {
        setSelected(null);
      }
      if (changes.some((change) => change.type !== "select")) markDirty();
    },
    [markDirty, onNodesChangeBase, selected]
  );
  useEffect(() => {
    setEdges((current) => autoRouteEvidenceEdges(current, nodes));
  }, [nodes, setEdges]);

  const onEdgesChange = useCallback(
    (changes: Parameters<typeof onEdgesChangeBase>[0]) => {
      onEdgesChangeBase(changes);
      if (changes.some((change) => change.type === "remove" && selected?.kind === "edge" && change.id === selected.id)) {
        setSelected(null);
      }
      if (changes.some((change) => change.type !== "select")) markDirty();
    },
    [markDirty, onEdgesChangeBase, selected]
  );

  const saveNow = useCallback(async () => {
    if (!selectedChainId) return;
    setSaveState("saving");
    try {
      await saveEvidenceChainGraph(token, selectedChainId, serializeEvidenceGraph(nodes, edges));
      setDirty(false);
      setSaveState("saved");
      await queryClient.invalidateQueries({ queryKey: ["evidence-chains"] });
    } catch {
      setSaveState("failed");
    }
  }, [edges, nodes, queryClient, selectedChainId, token]);

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
    const saved = lastSavedMeta.current;
    if (!title) return;
    if (saved?.id === detail.data.id && title === saved.title && description === saved.description) return;
    const timer = window.setTimeout(() => {
      renameChain.mutate({ id: detail.data!.id, title, description });
    }, 800);
    return () => window.clearTimeout(timer);
  }, [chainDescriptionDraft, chainMetaComposing, chainTitleDraft, detail.data, hydrateRunNodeFromCandidate, renameChain, withEdgeHandlers, withNodeHandlers]);

  const filteredCandidates = useMemo(() => (candidates.data || []).filter((candidate) => candidateMatches(candidate, candidateQuery)), [candidates.data, candidateQuery]);
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

  const nodeTypes = useMemo(() => ({ evidence: EvidenceNode }), []);
  const edgeTypes = useMemo(() => ({ evidence: EvidenceEdge }), []);

  const onConnect = useCallback(
    (connection: Connection) => {
      const id = `edge_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 7)}`;
      const type: EvidenceEdgeType = "next_step";
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
              animated: true,
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
    if (!selected) return;
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
  }, [markDirty, selected, setEdges, setNodes]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (!selected || (event.key !== "Delete" && event.key !== "Backspace")) return;
      if (isEditableTarget(event.target)) return;
      event.preventDefault();
      deleteSelection();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [deleteSelection, selected]);

  const onNodeClick: NodeMouseHandler<EvidenceFlowNode> = (_event, node) => setSelected({ kind: "node", id: node.id });
  const onEdgeClick: EdgeMouseHandler<EvidenceFlowEdge> = (_event, edge) => setSelected({ kind: "edge", id: edge.id });
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
            {!leftOpen ? (
              <button className="icon-button" title={t("showBoards")} onClick={() => setLeftOpen(true)}>
                <ChevronRight size={16} />
              </button>
            ) : null}
            <div className="evidence-title-edit">
              <input
                value={chainTitleDraft}
                placeholder={t("evidenceChainTitlePlaceholder")}
                onChange={(event) => setChainTitleDraft(event.target.value)}
                onCompositionStart={() => setChainMetaComposing(true)}
                onCompositionEnd={(event) => {
                  setChainMetaComposing(false);
                  setChainTitleDraft(event.currentTarget.value);
                }}
              />
              <span className={`save-state ${saveState}`}>{saveState === "saving" ? t("saving") : saveState === "failed" ? t("saveFailed") : dirty ? t("unsaved") : t("saved")}</span>
            </div>
          </div>
          <div className="evidence-node-tools">
            <div className="evidence-node-menu-wrap">
              <button disabled={!selectedChainId} onClick={() => setNodeMenuOpen((open) => !open)}>
                <Plus size={14} />
                {t("node")}
              </button>
              {nodeMenuOpen ? (
                <div className="evidence-node-menu">
                  {evidenceNodeTypes.map((type) => (
                    <button key={type} disabled={!selectedChainId} onClick={() => addTextNode(type)}>
                      {nodeTypeLabel(type)}
                    </button>
                  ))}
                </div>
              ) : null}
            </div>
            <button disabled={!selectedChainId} className={runTrayOpen ? "active-soft" : ""} onClick={() => setRunTrayOpen((open) => !open)}>
              <ListPlus size={14} />
              {t("runsPanel")}
            </button>
            <button disabled={!dirty && saveState !== "failed"} onClick={() => void saveNow()}>
              <Save size={14} />
              {t("save")}
            </button>
            {selected ? (
              <button disabled={!selectedChainId} className="danger-inline" title={t("deleteElementTitle")} onClick={deleteSelection}>
                <Trash2 size={14} />
                {t("element")}
              </button>
            ) : null}
            <button disabled={!selectedChainId} className="danger-inline" title={t("deleteBoardTitle")} onClick={() => selectedChainId && window.confirm(t("deleteBoardConfirm")) && removeChain.mutate(selectedChainId)}>
              <Trash2 size={14} />
              {t("board")}
            </button>
          </div>
        </div>
        <div className="evidence-canvas" onDragOver={(event) => event.preventDefault()} onDrop={onDrop}>
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            edgeTypes={edgeTypes}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onNodeClick={onNodeClick}
            onEdgeClick={onEdgeClick}
            onNodeDoubleClick={onNodeDoubleClick}
            onPaneClick={clearSelection}
            connectionMode={ConnectionMode.Loose}
            fitView
          >
            <Background gap={24} color="#dce2e8" />
            <Controls />
            <MiniMap pannable zoomable />
          </ReactFlow>
        </div>
      </main>

      <aside className="evidence-sidebar">
        {!leftOpen ? (
          <button className="evidence-rail-button" title={t("showBoards")} onClick={() => setLeftOpen(true)}>
            <ChevronRight size={17} />
          </button>
        ) : null}
        <div className="evidence-sidebar-head">
          <div>
            <h2>{t("evidenceChains")}</h2>
            <span>{chains.data?.length || 0} {t("boardCount")}</span>
          </div>
          <div className="evidence-head-actions">
            <button className="icon-button" title={t("hideBoards")} onClick={() => setLeftOpen(false)}>
              <ChevronLeft size={16} />
            </button>
            <button className="icon-button" title={t("newChain")} onClick={() => createChain.mutate()}>
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
            <button key={chain.id} className={chain.id === selectedChainId ? "chain-item active" : "chain-item"} onClick={() => setSelectedChainId(chain.id)}>
              <strong>{chain.title}</strong>
              <span>{chain.description || fmtShortTime(chain.updated_at)}</span>
            </button>
          ))}
          {!chains.data?.length ? <div className="empty">{t("noData")}</div> : null}
        </div>
      </aside>

      {runTrayOpen ? (
        <section className="evidence-run-drawer">
          <div className="section-head">
            <div>
              <h2>{t("runCandidates")}</h2>
              <span className="muted">{candidateGroups.length} {t("projectCount")}</span>
            </div>
            <button className="icon-button" title={t("hideRuns")} onClick={() => setRunTrayOpen(false)}>
              <ChevronLeft size={15} />
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
          <div className="candidate-browser">
            <div className="candidate-project-list">
              {candidateGroups.map((group) => (
                <button key={group.key} className={group.key === activeCandidateGroup?.key ? "candidate-project active" : "candidate-project"} onClick={() => setSelectedCandidateGroup(group.key)}>
                  <strong>{group.title}</strong>
                  <span>{group.candidates.length} runs</span>
                </button>
              ))}
              {!candidateGroups.length ? <div className="empty">{t("noMatches")}</div> : null}
            </div>
            <div className="candidate-list">
              {activeCandidateGroup ? (
                <>
                  <div className="candidate-group-head">
                    <strong>{activeCandidateGroup.title}</strong>
                    <span>{activeCandidateGroup.subtitle}</span>
                  </div>
                  {activeCandidateGroup.candidates.map((candidate) => (
                    <CandidateItem key={candidate.id} candidate={candidate} />
                  ))}
                </>
              ) : (
                <div className="empty">{t("noMatches")}</div>
              )}
            </div>
          </div>
        </section>
      ) : null}
    </div>
  );
}

function isEditableTarget(target: EventTarget | null) {
  if (!(target instanceof HTMLElement)) return false;
  return target.isContentEditable || target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement;
}

function isComposingInput(event: Event) {
  return "isComposing" in event && Boolean((event as InputEvent).isComposing);
}

function CandidateItem({ candidate }: { candidate: EvidenceChainRunCandidate }) {
  const { runDisplayTitle } = candidateRunNodeFields(candidate);
  const title = runDisplayTitle;
  const meta = candidate.verdict || candidate.question || candidate.key_metrics || candidate.next_action || candidate.run?.status || candidate.kind;
  return (
    <div
      className="candidate-item"
      draggable
      onDragStart={(event) => {
        event.dataTransfer.setData(candidateMime, JSON.stringify(candidate));
        event.dataTransfer.effectAllowed = "copy";
      }}
    >
      <strong>{title}</strong>
      <span>{meta}</span>
      <small>{candidate.kind === "project_card" ? `L${candidate.evidence_level || "C"}` : candidate.run?.kind || "run"} · {candidate.run_id}</small>
    </div>
  );
}

function EvidenceNode({ id, data, selected }: { id: string; data: EvidenceNodeData; selected: boolean }) {
  const color = evidenceColor(data.type);
  const typeOptions: EvidenceNodeType[] = data.type === "run" ? ["run", ...evidenceNodeTypes] : evidenceNodeTypes;
  const composingRef = useRef(false);
  const bodyRef = useRef<HTMLTextAreaElement | null>(null);
  const [draft, setDraft] = useState({ title: data.title, body: data.body });

  useEffect(() => {
    if (composingRef.current) return;
    setDraft({ title: data.title, body: data.body });
  }, [data.body, data.title]);

  useLayoutEffect(() => {
    const body = bodyRef.current;
    if (!body) return;
    body.style.height = "0px";
    body.style.height = `${Math.max(72, body.scrollHeight)}px`;
  }, [draft.body]);

  const updateDraft = (field: "title" | "body", value: string, commit = true) => {
    setDraft((current) => ({ ...current, [field]: value }));
    if (commit && !composingRef.current) data.onUpdateNode?.(id, { [field]: value });
  };

  const finishComposition = (field: "title" | "body", value: string) => {
    composingRef.current = false;
    setDraft((current) => ({ ...current, [field]: value }));
    data.onUpdateNode?.(id, { [field]: value });
  };

  return (
    <div className={`evidence-node ${data.type} ${selected ? "selected" : ""}`} style={{ "--evidence-color": color } as CSSProperties}>
      <EvidenceNodeHandles />
      <div className="evidence-node-category">
        <select className="nodrag nopan" value={data.type} onChange={(event) => data.onUpdateNode?.(id, { type: event.target.value as EvidenceNodeType })}>
          {typeOptions.map((type) => (
            <option key={type} value={type}>
              {nodeTypeLabel(type)}
            </option>
          ))}
        </select>
        {data.evidenceLevel ? <span>L{data.evidenceLevel}</span> : null}
      </div>
      <div className="evidence-color-line" />
      <input
        className="evidence-node-title nodrag nopan"
        value={draft.title}
        onChange={(event) => updateDraft("title", event.target.value, !isComposingInput(event.nativeEvent))}
        onCompositionStart={() => { composingRef.current = true; }}
        onCompositionEnd={(event) => finishComposition("title", event.currentTarget.value)}
        placeholder={data.labels?.titlePlaceholder || "标题"}
      />
      <textarea
        ref={bodyRef}
        className="evidence-node-body nodrag nopan"
        value={draft.body}
        onChange={(event) => updateDraft("body", event.target.value, !isComposingInput(event.nativeEvent))}
        onCompositionStart={() => { composingRef.current = true; }}
        onCompositionEnd={(event) => finishComposition("body", event.currentTarget.value)}
        placeholder={data.labels?.nodeBodyPlaceholder || "写下假说、计划、结果或笔记..."}
      />
      {data.keyMetrics ? <em>{data.keyMetrics}</em> : null}
      {data.type === "run" && data.runId ? (
        <button className="nodrag action-button" onClick={() => data.onOpenRun?.(data.runId!)}>
          {data.labels?.openRun || "打开实验"}
        </button>
      ) : null}
    </div>
  );
}

function EvidenceNodeHandles() {
  return (
    <>
      {evidenceHandleSides.map((side) => (
        <Handle key={`source-${side}`} id={handleId("source", side)} className={`evidence-handle evidence-handle-${side} evidence-handle-source`} type="source" position={side} />
      ))}
      {evidenceHandleSides.map((side) => (
        <Handle key={`target-${side}`} id={handleId("target", side)} className={`evidence-handle evidence-handle-${side} evidence-handle-target`} type="target" position={side} />
      ))}
    </>
  );
}

function EvidenceEdge({ id, sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, markerEnd, data, label }: EdgeProps<EvidenceFlowEdge>) {
  const type = data?.type || "next_step";
  const [editing, setEditing] = useState(false);
  const composingRef = useRef(false);
  const [draft, setDraft] = useState({ label: text(label), rationale: data?.rationale || "" });
  const [edgePath, labelX, labelY] = getSmoothStepPath({ sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, borderRadius: 18, offset: 32 });
  const displayLabel = text(label) || edgeTypeLabel(type);

  useEffect(() => {
    if (composingRef.current) return;
    setDraft({ label: text(label), rationale: data?.rationale || "" });
  }, [data?.rationale, label]);

  const updateDraft = (field: "label" | "rationale", value: string, commit = true) => {
    setDraft((current) => ({ ...current, [field]: value }));
    if (commit && !composingRef.current) data?.onUpdateEdge?.(id, { [field]: value });
  };

  const finishComposition = (field: "label" | "rationale", value: string) => {
    composingRef.current = false;
    setDraft((current) => ({ ...current, [field]: value }));
    data?.onUpdateEdge?.(id, { [field]: value });
  };

  const changeType = (nextType: EvidenceEdgeType) => {
    const nextLabel = edgeTypeLabel(nextType);
    setDraft((current) => ({ ...current, label: nextLabel }));
    data?.onUpdateEdge?.(id, { type: nextType, label: nextLabel });
  };

  const finishEditing = () => {
    composingRef.current = false;
    data?.onUpdateEdge?.(id, { label: draft.label, rationale: draft.rationale });
    setEditing(false);
  };

  return (
    <>
      <BaseEdge path={edgePath} markerEnd={markerEnd} style={edgeStyle(type)} />
      <EdgeLabelRenderer>
        <div className={editing ? "evidence-edge-label editing nodrag nopan" : "evidence-edge-label nodrag nopan"} style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`, "--evidence-color": evidenceColor(type) } as CSSProperties}>
          {editing ? (
            <div className="evidence-edge-editor">
              <select value={type} onChange={(event) => changeType(event.target.value as EvidenceEdgeType)}>
                {evidenceEdgeTypes.map((edgeType) => (
                  <option key={edgeType} value={edgeType}>
                    {edgeTypeLabel(edgeType)}
                  </option>
                ))}
              </select>
              <input
                autoFocus
                value={draft.label}
                onChange={(event) => updateDraft("label", event.target.value, !isComposingInput(event.nativeEvent))}
                onCompositionStart={() => { composingRef.current = true; }}
                onCompositionEnd={(event) => finishComposition("label", event.currentTarget.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" && !composingRef.current && !event.nativeEvent.isComposing) finishEditing();
                }}
                placeholder={data?.labels?.relationLabel || "关系文字"}
              />
              <textarea
                value={draft.rationale}
                onChange={(event) => updateDraft("rationale", event.target.value, !isComposingInput(event.nativeEvent))}
                onCompositionStart={() => { composingRef.current = true; }}
                onCompositionEnd={(event) => finishComposition("rationale", event.currentTarget.value)}
                placeholder={data?.labels?.rationale || "理由"}
              />
              <button type="button" className="edge-done-button" onClick={finishEditing}>
                {data?.labels?.done || "完成"}
              </button>
            </div>
          ) : (
            <button
              type="button"
              onClick={(event) => {
                event.stopPropagation();
                data?.onSelectEdge?.(id);
              }}
              onDoubleClick={(event) => {
                event.stopPropagation();
                data?.onSelectEdge?.(id);
                setEditing(true);
              }}
              title={`${edgeTypeLabel(type)} · double click to edit`}
            >
              {displayLabel}
            </button>
          )}
        </div>
      </EdgeLabelRenderer>
    </>
  );
}

function autoRouteEvidenceEdges(edges: EvidenceFlowEdge[], nodes: EvidenceFlowNode[]): EvidenceFlowEdge[] {
  const nodeById = new Map(nodes.map((node) => [node.id, node]));
  return edges.map((edge) => {
    const type = edge.data?.type || "next_step";
    const data: EvidenceEdgeData = { ...(edge.data ?? {}), type, rationale: edge.data?.rationale || "" };
    const base: EvidenceFlowEdge = {
      ...edge,
      type: "evidence",
      animated: type === "next_step",
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
}

function chooseEvidenceHandles(source: EvidenceFlowNode, target: EvidenceFlowNode): { source: EvidenceHandleSide; target: EvidenceHandleSide } {
  const sourceCenter = nodeCenter(source);
  const targetCenter = nodeCenter(target);
  const dx = targetCenter.x - sourceCenter.x;
  const dy = targetCenter.y - sourceCenter.y;
  if (Math.abs(dx) >= Math.abs(dy)) {
    const sourceSide = dx >= 0 ? Position.Right : Position.Left;
    return { source: sourceSide, target: oppositeSide(sourceSide) };
  }
  const sourceSide = dy >= 0 ? Position.Bottom : Position.Top;
  return { source: sourceSide, target: oppositeSide(sourceSide) };
}

function nodeCenter(node: EvidenceFlowNode) {
  const measured = node.measured as { width?: number; height?: number } | undefined;
  const width = measured?.width || node.width || 286;
  const height = measured?.height || node.height || 184;
  return { x: node.position.x + width / 2, y: node.position.y + height / 2 };
}

function oppositeSide(side: EvidenceHandleSide): EvidenceHandleSide {
  switch (side) {
    case Position.Top:
      return Position.Bottom;
    case Position.Bottom:
      return Position.Top;
    case Position.Left:
      return Position.Right;
    case Position.Right:
      return Position.Left;
  }
}

function handleId(kind: "source" | "target", side: EvidenceHandleSide) {
  return `${kind}-${side}`;
}

function evidenceColor(type: EvidenceNodeType | EvidenceEdgeType) {
  switch (type) {
    case "run":
      return "#4f6f8f";
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
    case "supports":
      return "#32664b";
    case "does_not_prove":
      return "#b24b43";
    case "custom":
      return "#56616d";
    case "next_step":
    default:
      return "#4f6f8f";
  }
}
