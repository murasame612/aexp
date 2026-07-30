import { Position } from "@xyflow/react";
import {
  edgeStyle,
  evidenceAutoHandlePair,
  evidenceMarkerEnd,
  type EvidenceEdgeData,
  type EvidenceFlowEdge,
  type EvidenceFlowNode,
  type EvidenceHandleSide,
  type EvidenceRoutePoint
} from "./evidenceChain";

interface RouteRect {
  left: number;
  top: number;
  right: number;
  bottom: number;
}

interface RouteCandidate {
  points: EvidenceRoutePoint[];
  score: number;
  signature: string;
}

const routeClearance = 20;
const endpointClearance = 22;
const routeStub = 44;
const routeBendPenalty = 54;
const labelWidth = 176;
const labelHeight = 30;

export function evidenceRoutingGeometryKey(nodes: EvidenceFlowNode[]) {
  return [...nodes]
    .sort((left, right) => left.id.localeCompare(right.id))
    .map((node) => {
      const rect = nodeRect(node);
      return `${node.id}:${round(rect.left)},${round(rect.top)},${round(rect.right)},${round(rect.bottom)}`;
    })
    .join("|");
}

export function evidenceRoutingTopologyKey(edges: EvidenceFlowEdge[]) {
  return [...edges]
    .sort((left, right) => left.id.localeCompare(right.id))
    .map((edge) => {
      const autoHandles = edge.data?.autoHandles === true;
      return [
        edge.id,
        edge.source,
        edge.target,
        autoHandles ? "" : edge.sourceHandle || "",
        autoHandles ? "" : edge.targetHandle || "",
        autoHandles ? "auto" : "manual"
      ].join(":");
    })
    .join("|");
}

export function routeEvidenceGraphEdges(edges: EvidenceFlowEdge[], nodes: EvidenceFlowNode[]): EvidenceFlowEdge[] {
  const nodeByID = new Map(nodes.map((node) => [node.id, node]));
  const routed = edges.map((edge) => {
    const type = edge.data?.type || "next_step";
    const data: EvidenceEdgeData = { ...(edge.data ?? {}), type, rationale: edge.data?.rationale || "" };
    const source = nodeByID.get(edge.source);
    const target = nodeByID.get(edge.target);
    const shouldChooseHandles = data.autoHandles === true || !edge.sourceHandle || !edge.targetHandle;
    const handles = source && target
      ? shouldChooseHandles
        ? evidenceAutoHandlePair(nodeCenter(source), nodeCenter(target))
        : {
            source: handleSide(edge.sourceHandle) || Position.Right,
            target: handleSide(edge.targetHandle) || Position.Left
          }
      : null;
    return {
      ...edge,
      type: "evidence",
      animated: false,
      markerEnd: evidenceMarkerEnd(type),
      style: edgeStyle(type),
      sourceHandle: handles ? handleID("source", handles.source) : edge.sourceHandle,
      targetHandle: handles ? handleID("target", handles.target) : edge.targetHandle,
      data: {
        ...data,
        autoHandles: shouldChooseHandles ? true : data.autoHandles
      }
    } satisfies EvidenceFlowEdge;
  });

  const laneByID = assignRouteLanes(routed, nodeByID);
  const reservedSegments = new Map<string, number>();
  const reservedLabels: RouteRect[] = [];
  const geometryByID = new Map<string, Pick<EvidenceEdgeData, "routeLane" | "routePoints" | "routeLabelPoint" | "routeSafe">>();

  for (const edge of [...routed].sort((left, right) => left.id.localeCompare(right.id))) {
    const source = nodeByID.get(edge.source);
    const target = nodeByID.get(edge.target);
    const lane = laneByID.get(edge.id) || 0;
    if (!source || !target) {
      geometryByID.set(edge.id, { routeLane: lane, routeSafe: false });
      continue;
    }
    const sourceSide = handleSide(edge.sourceHandle) || Position.Right;
    const targetSide = handleSide(edge.targetHandle) || Position.Left;
    const route = routeEvidenceEdge(
      source,
      target,
      nodes,
      sourceSide,
      targetSide,
      lane,
      reservedSegments,
      reservedLabels
    );
    if (!route) {
      geometryByID.set(edge.id, { routeLane: lane, routeSafe: false });
      continue;
    }
    for (let index = 1; index < route.points.length; index += 1) {
      const key = segmentKey(route.points[index - 1], route.points[index]);
      reservedSegments.set(key, (reservedSegments.get(key) || 0) + 1);
    }
    if (route.labelRect) reservedLabels.push(route.labelRect);
    geometryByID.set(edge.id, {
      routeLane: lane,
      routePoints: route.points,
      routeLabelPoint: route.labelPoint,
      routeSafe: true
    });
  }

  return routed.map((edge) => ({
    ...edge,
    data: {
      ...edge.data!,
      ...(geometryByID.get(edge.id) || { routeSafe: false })
    }
  }));
}

export function evidenceOrthogonalPath(points: EvidenceRoutePoint[], cornerRadius = 24): string {
  if (points.length < 2) return "";
  const commands = [`M ${round(points[0].x)} ${round(points[0].y)}`];

  for (let index = 1; index < points.length - 1; index += 1) {
    const previous = points[index - 1];
    const corner = points[index];
    const next = points[index + 1];
    const incomingLength = Math.abs(corner.x - previous.x) + Math.abs(corner.y - previous.y);
    const outgoingLength = Math.abs(next.x - corner.x) + Math.abs(next.y - corner.y);
    const isCorner = (previous.x === corner.x && corner.y === next.y)
      || (previous.y === corner.y && corner.x === next.x);

    if (!isCorner || incomingLength === 0 || outgoingLength === 0) {
      commands.push(`L ${round(corner.x)} ${round(corner.y)}`);
      continue;
    }

    const radius = Math.min(Math.max(cornerRadius, 0), incomingLength / 2, outgoingLength / 2);
    const entry = pointToward(corner, previous, radius);
    const exit = pointToward(corner, next, radius);
    commands.push(
      `L ${round(entry.x)} ${round(entry.y)}`,
      `Q ${round(corner.x)} ${round(corner.y)} ${round(exit.x)} ${round(exit.y)}`
    );
  }

  const last = points[points.length - 1];
  commands.push(`L ${round(last.x)} ${round(last.y)}`);
  return commands.join(" ");
}

function pointToward(from: EvidenceRoutePoint, to: EvidenceRoutePoint, distance: number): EvidenceRoutePoint {
  const length = Math.abs(to.x - from.x) + Math.abs(to.y - from.y);
  if (length === 0) return from;
  const ratio = distance / length;
  return {
    x: from.x + (to.x - from.x) * ratio,
    y: from.y + (to.y - from.y) * ratio
  };
}

function routeEvidenceEdge(
  source: EvidenceFlowNode,
  target: EvidenceFlowNode,
  nodes: EvidenceFlowNode[],
  sourceSide: EvidenceHandleSide,
  targetSide: EvidenceHandleSide,
  lane: number,
  reservedSegments: Map<string, number>,
  reservedLabels: RouteRect[]
) {
  const sourceAnchor = nodeAnchor(source, sourceSide);
  const targetAnchor = nodeAnchor(target, targetSide);
  const stubDistance = routeStub + Math.min(lane, 10) * 8;
  const sourceStub = movePoint(sourceAnchor, sourceSide, stubDistance);
  const targetStub = movePoint(targetAnchor, targetSide, stubDistance);
  const obstacles = nodes
    .filter((node) => node.data.type !== "group" && node.id !== source.id && node.id !== target.id)
    .map((node) => nodeRect(node, routeClearance));
  const sourceEnvelope = nodeRect(source, endpointClearance);
  const targetEnvelope = nodeRect(target, endpointClearance);
  const nodeRects = nodes
    .filter((node) => node.data.type !== "group")
    .map((node) => nodeRect(node, 6));
  const xChannels = uniqueNumbers([
    sourceStub.x,
    targetStub.x,
    (sourceStub.x + targetStub.x) / 2,
    ...obstacles.flatMap((rect) => [rect.left, rect.right])
  ]);
  const yChannels = uniqueNumbers([
    sourceStub.y,
    targetStub.y,
    (sourceStub.y + targetStub.y) / 2,
    ...obstacles.flatMap((rect) => [rect.top, rect.bottom])
  ]);
  const bounds = obstacleBounds(obstacles, sourceStub, targetStub);
  xChannels.push(bounds.left - 48, bounds.right + 48);
  yChannels.push(bounds.top - 48, bounds.bottom + 48);

  const simpleMiddles: EvidenceRoutePoint[][] = [];
  if (sourceStub.x === targetStub.x || sourceStub.y === targetStub.y) {
    simpleMiddles.push([sourceStub, targetStub]);
  }
  simpleMiddles.push(
    [sourceStub, { x: targetStub.x, y: sourceStub.y }, targetStub],
    [sourceStub, { x: sourceStub.x, y: targetStub.y }, targetStub]
  );
  for (const x of uniqueNumbers(xChannels)) {
    simpleMiddles.push([sourceStub, { x, y: sourceStub.y }, { x, y: targetStub.y }, targetStub]);
  }
  for (const y of uniqueNumbers(yChannels)) {
    simpleMiddles.push([sourceStub, { x: sourceStub.x, y }, { x: targetStub.x, y }, targetStub]);
  }

  let best: RouteCandidate | null = null;
  const candidateFor = (middle: EvidenceRoutePoint[]) => {
    const points = compactOrthogonalPoints([sourceAnchor, ...middle, targetAnchor]);
    if (
      points.length < 2
      || !pathIsClear(points, obstacles)
      || !pathClearsEndpointEnvelopes(points, sourceEnvelope, targetEnvelope, sourceSide, targetSide)
    ) return null;
    return scoreRoute(points, sourceSide, targetSide, reservedSegments);
  };
  for (const middle of simpleMiddles) {
    const candidate = candidateFor(middle);
    if (candidate && (!best || candidate.score < best.score || (candidate.score === best.score && candidate.signature < best.signature))) {
      best = candidate;
    }
  }
  if (!best) {
    for (const x of uniqueNumbers(xChannels)) {
      for (const y of uniqueNumbers(yChannels)) {
        for (const middle of [
          [sourceStub, { x, y: sourceStub.y }, { x, y }, { x: targetStub.x, y }, targetStub],
          [sourceStub, { x: sourceStub.x, y }, { x, y }, { x, y: targetStub.y }, targetStub]
        ]) {
          const candidate = candidateFor(middle);
          if (candidate && (!best || candidate.score < best.score || (candidate.score === best.score && candidate.signature < best.signature))) {
            best = candidate;
          }
        }
      }
    }
  }
  if (!best) return null;

  const label = chooseLabelPoint(best.points, nodeRects, reservedLabels);
  return {
    points: best.points,
    labelPoint: label?.point,
    labelRect: label?.rect
  };
}

function assignRouteLanes(edges: EvidenceFlowEdge[], nodeByID: Map<string, EvidenceFlowNode>) {
  const lanes = new Map<string, number>();
  const assign = (keyFor: (edge: EvidenceFlowEdge) => string, orderFor: (edge: EvidenceFlowEdge) => number) => {
    const groups = new Map<string, EvidenceFlowEdge[]>();
    for (const edge of edges) {
      const key = keyFor(edge);
      groups.set(key, [...(groups.get(key) || []), edge]);
    }
    for (const group of groups.values()) {
      group.sort((left, right) => orderFor(left) - orderFor(right) || left.id.localeCompare(right.id));
      group.forEach((edge, index) => lanes.set(edge.id, Math.max(lanes.get(edge.id) || 0, index)));
    }
  };
  assign(
    (edge) => `${edge.source}:${edge.sourceHandle || ""}`,
    (edge) => nodeByID.get(edge.target) ? nodeCenter(nodeByID.get(edge.target)!).y : 0
  );
  assign(
    (edge) => `${edge.target}:${edge.targetHandle || ""}`,
    (edge) => nodeByID.get(edge.source) ? nodeCenter(nodeByID.get(edge.source)!).y : 0
  );
  return lanes;
}

function scoreRoute(
  points: EvidenceRoutePoint[],
  sourceSide: EvidenceHandleSide,
  targetSide: EvidenceHandleSide,
  reservedSegments: Map<string, number>
): RouteCandidate {
  let length = 0;
  let bends = 0;
  let reusePenalty = 0;
  let backwardsPenalty = 0;
  let previousDirection = "";
  const preferForward = sourceSide === Position.Right && targetSide === Position.Left;
  for (let index = 1; index < points.length; index += 1) {
    const start = points[index - 1];
    const end = points[index];
    const segmentLength = Math.abs(end.x - start.x) + Math.abs(end.y - start.y);
    const direction = start.x === end.x ? "vertical" : "horizontal";
    length += segmentLength;
    if (previousDirection && direction !== previousDirection) bends += 1;
    previousDirection = direction;
    reusePenalty += (reservedSegments.get(segmentKey(start, end)) || 0) * 160;
    if (preferForward && end.x < start.x) backwardsPenalty += (start.x - end.x) * 1.4;
  }
  const signature = points.map((point) => `${round(point.x)},${round(point.y)}`).join(";");
  return {
    points,
    score: length + bends * routeBendPenalty + reusePenalty + backwardsPenalty,
    signature
  };
}

function chooseLabelPoint(points: EvidenceRoutePoint[], nodeRects: RouteRect[], reservedLabels: RouteRect[]) {
  const segments = points.slice(1).map((end, index) => {
    const start = points[index];
    return {
      start,
      end,
      length: Math.abs(end.x - start.x) + Math.abs(end.y - start.y),
      horizontal: start.y === end.y,
      index
    };
  }).sort((left, right) => right.length - left.length || Number(right.horizontal) - Number(left.horizontal) || left.index - right.index);
  for (const segment of segments) {
    const midpoint = {
      x: (segment.start.x + segment.end.x) / 2,
      y: (segment.start.y + segment.end.y) / 2
    };
    const offsets = segment.horizontal ? [0, -22, 22, -44, 44] : [0, 22, -22, 44, -44];
    for (const offset of offsets) {
      const point = segment.horizontal
        ? { x: midpoint.x, y: midpoint.y + offset }
        : { x: midpoint.x + offset, y: midpoint.y };
      const rect = {
        left: point.x - labelWidth / 2,
        right: point.x + labelWidth / 2,
        top: point.y - labelHeight / 2,
        bottom: point.y + labelHeight / 2
      };
      if ([...nodeRects, ...reservedLabels].some((other) => rectsOverlap(rect, other))) continue;
      return { point, rect };
    }
  }
  return null;
}

function nodeRect(node: EvidenceFlowNode, padding = 0): RouteRect {
  const width = typeof node.measured?.width === "number"
    ? node.measured.width
    : typeof node.width === "number"
      ? node.width
      : 306;
  const height = typeof node.measured?.height === "number"
    ? node.measured.height
    : typeof node.height === "number"
      ? node.height
      : 138;
  return {
    left: node.position.x - padding,
    top: node.position.y - padding,
    right: node.position.x + width + padding,
    bottom: node.position.y + height + padding
  };
}

function nodeCenter(node: EvidenceFlowNode) {
  const rect = nodeRect(node);
  return { x: (rect.left + rect.right) / 2, y: (rect.top + rect.bottom) / 2 };
}

function nodeAnchor(node: EvidenceFlowNode, side: EvidenceHandleSide): EvidenceRoutePoint {
  const rect = nodeRect(node);
  if (side === Position.Left) return { x: rect.left, y: (rect.top + rect.bottom) / 2 };
  if (side === Position.Right) return { x: rect.right, y: (rect.top + rect.bottom) / 2 };
  if (side === Position.Top) return { x: (rect.left + rect.right) / 2, y: rect.top };
  return { x: (rect.left + rect.right) / 2, y: rect.bottom };
}

function movePoint(point: EvidenceRoutePoint, side: EvidenceHandleSide, distance: number): EvidenceRoutePoint {
  if (side === Position.Left) return { x: point.x - distance, y: point.y };
  if (side === Position.Right) return { x: point.x + distance, y: point.y };
  if (side === Position.Top) return { x: point.x, y: point.y - distance };
  return { x: point.x, y: point.y + distance };
}

function pathIsClear(points: EvidenceRoutePoint[], obstacles: RouteRect[]) {
  for (let index = 1; index < points.length; index += 1) {
    const start = points[index - 1];
    const end = points[index];
    if (start.x !== end.x && start.y !== end.y) return false;
    if (obstacles.some((rect) => segmentIntersectsRect(start, end, rect))) return false;
  }
  return true;
}

function pathClearsEndpointEnvelopes(
  points: EvidenceRoutePoint[],
  sourceEnvelope: RouteRect,
  targetEnvelope: RouteRect,
  sourceSide: EvidenceHandleSide,
  targetSide: EvidenceHandleSide
) {
  const sourceAnchor = points[0];
  const sourceOuter = points[1];
  const targetAnchor = points[points.length - 1];
  const targetOuter = points[points.length - 2];
  if (!pointLeavesAnchor(sourceAnchor, sourceOuter, sourceSide)) return false;
  if (!pointLeavesAnchor(targetAnchor, targetOuter, targetSide)) return false;

  for (let index = 2; index < points.length; index += 1) {
    if (segmentIntersectsRect(points[index - 1], points[index], sourceEnvelope)) return false;
  }
  for (let index = 1; index < points.length - 1; index += 1) {
    if (segmentIntersectsRect(points[index - 1], points[index], targetEnvelope)) return false;
  }
  return true;
}

function pointLeavesAnchor(anchor: EvidenceRoutePoint, outer: EvidenceRoutePoint, side: EvidenceHandleSide) {
  if (side === Position.Left) return outer.y === anchor.y && outer.x <= anchor.x - routeStub;
  if (side === Position.Right) return outer.y === anchor.y && outer.x >= anchor.x + routeStub;
  if (side === Position.Top) return outer.x === anchor.x && outer.y <= anchor.y - routeStub;
  return outer.x === anchor.x && outer.y >= anchor.y + routeStub;
}

function segmentIntersectsRect(start: EvidenceRoutePoint, end: EvidenceRoutePoint, rect: RouteRect) {
  if (start.x === end.x) {
    return start.x > rect.left
      && start.x < rect.right
      && Math.max(start.y, end.y) > rect.top
      && Math.min(start.y, end.y) < rect.bottom;
  }
  return start.y > rect.top
    && start.y < rect.bottom
    && Math.max(start.x, end.x) > rect.left
    && Math.min(start.x, end.x) < rect.right;
}

function compactOrthogonalPoints(points: EvidenceRoutePoint[]) {
  const deduplicated = points.filter((point, index) => index === 0 || point.x !== points[index - 1].x || point.y !== points[index - 1].y);
  const compacted: EvidenceRoutePoint[] = [];
  for (const point of deduplicated) {
    const before = compacted[compacted.length - 2];
    const previous = compacted[compacted.length - 1];
    if (before && previous && ((before.x === previous.x && previous.x === point.x) || (before.y === previous.y && previous.y === point.y))) {
      compacted[compacted.length - 1] = point;
    } else {
      compacted.push(point);
    }
  }
  return compacted;
}

function obstacleBounds(obstacles: RouteRect[], source: EvidenceRoutePoint, target: EvidenceRoutePoint) {
  return {
    left: Math.min(source.x, target.x, ...obstacles.map((rect) => rect.left)),
    top: Math.min(source.y, target.y, ...obstacles.map((rect) => rect.top)),
    right: Math.max(source.x, target.x, ...obstacles.map((rect) => rect.right)),
    bottom: Math.max(source.y, target.y, ...obstacles.map((rect) => rect.bottom))
  };
}

function uniqueNumbers(values: number[]) {
  return [...new Set(values.map(round))].sort((left, right) => left - right);
}

function handleSide(handle: string | null | undefined): EvidenceHandleSide | null {
  if (handle?.endsWith("left")) return Position.Left;
  if (handle?.endsWith("right")) return Position.Right;
  if (handle?.endsWith("top")) return Position.Top;
  if (handle?.endsWith("bottom")) return Position.Bottom;
  return null;
}

function handleID(kind: "source" | "target", side: EvidenceHandleSide) {
  return `${kind}-${side}`;
}

function segmentKey(start: EvidenceRoutePoint, end: EvidenceRoutePoint) {
  const first = `${round(start.x)},${round(start.y)}`;
  const second = `${round(end.x)},${round(end.y)}`;
  return first < second ? `${first}|${second}` : `${second}|${first}`;
}

function rectsOverlap(left: RouteRect, right: RouteRect) {
  return left.left < right.right && left.right > right.left && left.top < right.bottom && left.bottom > right.top;
}

function round(value: number) {
  return Math.round(value * 1000) / 1000;
}
