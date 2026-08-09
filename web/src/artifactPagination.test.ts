import { describe, expect, it } from "vitest";
import { artifactPage, artifactRequestLimit, initialArtifactRows } from "./artifactPagination";

describe("artifact pagination", () => {
  it("uses one sentinel row so legacy inventories can reveal more artifacts", () => {
    const items = Array.from({ length: initialArtifactRows + 1 }, (_, index) => index);
    expect(artifactRequestLimit(false)).toBe(initialArtifactRows + 1);
    expect(artifactPage(items, false)).toEqual({
      visibleItems: items.slice(0, initialArtifactRows),
      hasMore: true
    });
  });

  it("returns the complete inventory after the explicit expansion", () => {
    const items = Array.from({ length: 45 }, (_, index) => index);
    expect(artifactRequestLimit(true)).toBe(0);
    expect(artifactPage(items, true)).toEqual({ visibleItems: items, hasMore: false });
  });
});
