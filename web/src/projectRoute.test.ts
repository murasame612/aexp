import { describe, expect, it } from "vitest";
import { parseProjectRoute, readEvidenceMapFromSearch, withEvidenceMapSearch } from "./projectRoute";

describe("parseProjectRoute", () => {
  it("recognizes every project workspace section", () => {
    expect(parseProjectRoute("/ui-v2/projects/project-a")).toEqual({ projectId: "project-a", section: "journal" });
    expect(parseProjectRoute("/ui-v2/projects/project-a/journal")).toEqual({ projectId: "project-a", section: "journal" });
    expect(parseProjectRoute("/ui-v2/projects/project-a/literature")).toEqual({ projectId: "project-a", section: "literature" });
    expect(parseProjectRoute("/ui-v2/projects/project-a/runs")).toEqual({ projectId: "project-a", section: "runs" });
    expect(parseProjectRoute("/ui-v2/projects/project-a/assets/")).toEqual({ projectId: "project-a", section: "assets" });
    expect(parseProjectRoute("/ui-v2/projects/project-a/research-graph")).toEqual({ projectId: "project-a", section: "research-graph" });
  });

  it("decodes project ids and rejects global project routes", () => {
    expect(parseProjectRoute("/ui-v2/projects/facade%20study")).toEqual({ projectId: "facade study", section: "journal" });
    expect(parseProjectRoute("/ui-v2/projects")).toBeNull();
    expect(parseProjectRoute("/ui-v2/runs")).toBeNull();
  });

  it("round-trips an explicit Evidence Map without losing unrelated query state", () => {
    expect(readEvidenceMapFromSearch("?map=chain_topic&panel=review")).toBe("chain_topic");
    expect(withEvidenceMapSearch("?panel=review", "chain topic")).toBe("?panel=review&map=chain+topic");
    expect(withEvidenceMapSearch("?panel=review&map=chain_topic", "")).toBe("?panel=review");
    expect(readEvidenceMapFromSearch("")).toBe("");
  });
});
