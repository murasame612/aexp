import { describe, expect, it } from "vitest";
import { placementDisplayState, placementStates, runDataFinalizationPresentation, transferErrorSummary, transferPresentation, transferStates } from "./dataState";

describe("data lifecycle presentation", () => {
  it("covers every placement fact and keeps stale separate", () => {
    for (const state of placementStates) expect(placementDisplayState({ observed_state: state, freshness: "fresh" })).toBe(state);
    expect(placementDisplayState({ observed_state: "present", freshness: "stale" })).toBe("stale");
  });

  it("covers every persistent transfer state and clamps progress", () => {
    for (const state of transferStates) {
      const view = transferPresentation({ state, bytes_done: 50, total_bytes: 100 });
      expect(view.state).toBe(state);
      expect(view.terminal).toBe(["completed", "blocked", "failed", "cancelled"].includes(state));
      expect(view.percent).toBe(50);
    }
    expect(transferPresentation({ state: "transferring", bytes_done: 200, total_bytes: 100 }).percent).toBe(100);
  });

  it("does not merge process success with blocked or failed data finalization", () => {
    for (const state of ["blocked", "failed"]) {
      const view = runDataFinalizationPresentation({ status: "succeeded", data_finalization_state: state });
      expect(view.archiveProblem).toBe(true);
      expect(view.message).toContain("Computation succeeded");
    }
    expect(runDataFinalizationPresentation({ status: "succeeded", data_finalization_state: "completed" }).archiveProblem).toBe(false);
    expect(runDataFinalizationPresentation({ status: "failed", data_finalization_state: "failed" }).archiveProblem).toBe(false);
  });

  it("keeps failed transfer cards compact while preserving detail elsewhere", () => {
    const summary = transferErrorSummary("destination_hash_failed", `Traceback ${"x".repeat(400)}\nsecret second line`);
    expect(summary).toContain("destination_hash_failed");
    expect(summary).not.toContain("second line");
    expect(summary.length).toBeLessThanOrEqual(181);
  });
});
