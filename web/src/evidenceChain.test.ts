import { describe, expect, it } from "vitest";
import { candidateMatches, edgeTypeLabel, groupRunCandidatesByProject, serializeEvidenceGraph, type EvidenceFlowEdge, type EvidenceFlowNode } from "./evidenceChain";

describe("evidenceChain helpers", () => {
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
    expect(edgeTypeLabel("supports")).toBe("证明");
    expect(edgeTypeLabel("does_not_prove")).toBe("不能证明");
    expect(edgeTypeLabel("next_step")).toBe("进一步");
    expect(edgeTypeLabel("custom")).toBe("自定义");
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
          runId: "run_abc",
          projectCardId: "card_abc",
          status: "succeeded",
          runKind: "formal",
          keyMetrics: "mAP=0.6",
          evidenceLevel: "B"
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
    expect(JSON.parse(payload.nodes[0].data_json || "{}")).toMatchObject({ status: "succeeded", keyMetrics: "mAP=0.6" });
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
});
