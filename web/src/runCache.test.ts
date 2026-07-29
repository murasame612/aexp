import { describe, expect, it } from "vitest";
import { replaceRunInPage } from "./runCache";
import type { Paginated, Run } from "./types";

describe("run query cache", () => {
  it("replaces the matching run immediately without mutating the cached page", () => {
    const original: Paginated<Run> = {
      items: [
        { id: "run_1", resource_id: "r", status: "running", command: "train" },
        { id: "run_2", resource_id: "r", status: "running", command: "eval" }
      ],
      total: 2,
      limit: 100,
      offset: 0
    };
    const updated: Run = { ...original.items[0], status: "succeeded", status_freshness: "fresh" };
    const result = replaceRunInPage(original, updated)!;
    expect(result.items[0]).toEqual(updated);
    expect(result.items[1]).toBe(original.items[1]);
    expect(original.items[0].status).toBe("running");
  });

  it("preserves the page reference when the run is not present", () => {
    const original: Paginated<Run> = { items: [], total: 0, limit: 100, offset: 0 };
    const result = replaceRunInPage(original, { id: "missing", resource_id: "r", status: "succeeded", command: "x" });
    expect(result).toBe(original);
  });
});
