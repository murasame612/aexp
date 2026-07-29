import { describe, expect, it } from "vitest";
import {
  buildEvidenceEditPatch,
  candidateMatches,
  candidateToNode,
  defaultEvidenceRelation,
  edgeStyle,
  edgeTypeLabel,
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
  inspectProtocolFrameMigration,
  convertProtocolToFrame,
  projectEvidenceGroups,
  protocolContainerMoveDeltaForKey,
  serializeEvidenceGraph,
  translateProtocolContainer,
  type EvidenceFlowEdge,
  type EvidenceFlowNode
} from "./evidenceChain";
import type { EvidenceNodeType } from "./types";

describe("evidenceChain helpers", () => {
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
      title: "Bootstrap the research context"
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
    expect(serializeEvidenceGraph(laidOut, []).nodes).toHaveLength(100);
  });
});
