import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { EvidenceThreadRelationHint } from "./EvidenceChainBoard";
import { evidenceThreadRibbonPath } from "./evidenceChain";

describe("EvidenceThreadRelationHint", () => {
  it("renders an explicit disconnected state for pointer, keyboard, and touch focus", () => {
    const html = renderToStaticMarkup(<EvidenceThreadRelationHint focus={{
      originNodeId: "issue",
      visiblePeerNodeIds: [],
      directRelationCount: 0,
      hiddenRelationCount: 0,
      disconnected: true
    }} />);
    expect(html).toContain("role=\"status\"");
    expect(html).toContain("没有直接前因/后果");
  });

  it("distinguishes a filtered peer from a disconnected card", () => {
    const html = renderToStaticMarkup(<EvidenceThreadRelationHint focus={{
      originNodeId: "result",
      visiblePeerNodeIds: [],
      directRelationCount: 2,
      hiddenRelationCount: 2,
      disconnected: false
    }} />);
    expect(html).toContain("清空搜索可查看 2 个关系");
    expect(html).not.toContain("没有直接前因/后果");
  });
});

describe("evidenceThreadRibbonPath", () => {
  it("connects left-to-right cards from their facing edges with a smooth cubic curve", () => {
    const path = evidenceThreadRibbonPath(
      { left: 10, top: 20, width: 100, height: 60 },
      { left: 240, top: 90, width: 120, height: 80 }
    );
    expect(path).toMatch(/^M 110 50 C /);
    expect(path).toMatch(/, 234 130$/);
  });

  it("preserves a historical backward relation instead of silently reversing its arrow", () => {
    const path = evidenceThreadRibbonPath(
      { left: 260, top: 30, width: 100, height: 60 },
      { left: 20, top: 80, width: 100, height: 60 }
    );
    expect(path).toMatch(/^M 260 60 C /);
    expect(path).toMatch(/, 126 110$/);
  });

  it("uses top and bottom anchors for relations within one stage", () => {
    const path = evidenceThreadRibbonPath(
      { left: 40, top: 20, width: 120, height: 60 },
      { left: 40, top: 180, width: 120, height: 60 }
    );
    expect(path).toMatch(/^M 100 80 C 100 /);
    expect(path).toMatch(/, 100 168$/);
  });
});
