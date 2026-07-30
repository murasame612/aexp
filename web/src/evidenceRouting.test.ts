import { describe, expect, it } from "vitest";
import { serializeEvidenceGraph, type EvidenceFlowEdge, type EvidenceFlowNode, type EvidenceRoutePoint } from "./evidenceChain";
import { routeEvidenceGraphEdges } from "./evidenceRouting";

function node(id: string, x: number, y: number, width = 120, height = 72): EvidenceFlowNode {
  return {
    id,
    type: "evidence",
    position: { x, y },
    width,
    height,
    data: { type: "plan", title: id, body: "" }
  };
}

function segmentIntersectsRect(
  start: EvidenceRoutePoint,
  end: EvidenceRoutePoint,
  rect: { left: number; top: number; right: number; bottom: number }
) {
  if (start.x === end.x) {
    return start.x > rect.left
      && start.x < rect.right
      && Math.max(start.y, end.y) > rect.top
      && Math.min(start.y, end.y) < rect.bottom;
  }
  if (start.y === end.y) {
    return start.y > rect.top
      && start.y < rect.bottom
      && Math.max(start.x, end.x) > rect.left
      && Math.min(start.x, end.x) < rect.right;
  }
  return true;
}

function routeSignature(edge: EvidenceFlowEdge) {
  return {
    sourceHandle: edge.sourceHandle,
    targetHandle: edge.targetHandle,
    points: edge.data?.routePoints,
    label: edge.data?.routeLabelPoint
  };
}

describe("Evidence Map orthogonal routing", () => {
  it("routes a forward edge around an unrelated card with right-to-left ports", () => {
    const source = node("source", 0, 120);
    const obstacle = node("obstacle", 220, 90, 140, 132);
    const target = node("target", 480, 120);
    const edge: EvidenceFlowEdge = {
      id: "source-target",
      source: source.id,
      target: target.id,
      data: { type: "next_step", rationale: "" }
    };

    const [routed] = routeEvidenceGraphEdges([edge], [source, obstacle, target]);
    const points = routed.data?.routePoints || [];
    expect(routed.sourceHandle).toBe("source-right");
    expect(routed.targetHandle).toBe("target-left");
    expect(routed.data?.routeSafe).toBe(true);
    expect(points.length).toBeGreaterThanOrEqual(4);

    const paddedObstacle = {
      left: obstacle.position.x - 20,
      top: obstacle.position.y - 20,
      right: obstacle.position.x + Number(obstacle.width) + 20,
      bottom: obstacle.position.y + Number(obstacle.height) + 20
    };
    for (let index = 1; index < points.length; index += 1) {
      expect(segmentIntersectsRect(points[index - 1], points[index], paddedObstacle)).toBe(false);
      expect(points[index - 1].x === points[index].x || points[index - 1].y === points[index].y).toBe(true);
    }
  });

  it("is deterministic when API node and edge order changes", () => {
    const nodes = [
      node("source-a", 0, 40),
      node("source-b", 0, 260),
      node("obstacle", 230, 130, 160, 120),
      node("target", 520, 160)
    ];
    const edges: EvidenceFlowEdge[] = [
      { id: "a-target", source: "source-a", target: "target", data: { type: "supports", rationale: "" } },
      { id: "b-target", source: "source-b", target: "target", data: { type: "supports", rationale: "" } }
    ];

    const first = routeEvidenceGraphEdges(edges, nodes);
    const second = routeEvidenceGraphEdges([...edges].reverse(), [...nodes].reverse());
    const signatures = (rows: EvidenceFlowEdge[]) => Object.fromEntries(rows.map((edge) => [edge.id, routeSignature(edge)]));
    expect(signatures(second)).toEqual(signatures(first));
    expect(first[0].data?.routeLabelPoint).not.toEqual(first[1].data?.routeLabelPoint);
  });

  it("preserves manual ports while keeping transient geometry out of persistence", () => {
    const source = node("source", 160, 0);
    const obstacle = node("obstacle", 130, 170, 180, 100);
    const target = node("target", 160, 420);
    const edge: EvidenceFlowEdge = {
      id: "manual",
      source: source.id,
      target: target.id,
      sourceHandle: "source-bottom",
      targetHandle: "target-top",
      data: { type: "next_step", rationale: "", autoHandles: false }
    };

    const [routed] = routeEvidenceGraphEdges([edge], [source, obstacle, target]);
    expect(routed.sourceHandle).toBe("source-bottom");
    expect(routed.targetHandle).toBe("target-top");
    expect(routed.data?.routeSafe).toBe(true);
    expect(routed.data?.routePoints?.length).toBeGreaterThanOrEqual(4);

    const serialized = serializeEvidenceGraph([source, obstacle, target], [routed]);
    const persistedData = JSON.parse(serialized.edges[0].data_json || "{}");
    expect(persistedData).toEqual({
      sourceHandle: "source-bottom",
      targetHandle: "target-top",
      autoHandles: false
    });
  });
});
