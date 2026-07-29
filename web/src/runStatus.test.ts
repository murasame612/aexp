import { describe, expect, it } from "vitest";
import type { Run } from "./types";
import { runObservationPresentation, runStatusPresentation } from "./runStatus";

const base: Run = { id: "run", resource_id: "r", status: "running", lifecycle_status: "running", observation_state: "reachable", status_source: "remote_tmux", status_freshness: "fresh", command: "train" };

describe("run status presentation", () => {
  it("keeps a freshly observed running lifecycle green", () => {
    expect(runStatusPresentation(base, "zh")).toMatchObject({ label: "running", tone: "good", uncertain: false });
  });

  it("shows stale or unreachable running state as an observation warning", () => {
    const presentation = runStatusPresentation({ ...base, observation_state: "unreachable", status_freshness: "stale", status_check_error: "ssh timeout" }, "zh");
    expect(presentation).toMatchObject({ label: "状态未知", tone: "warn", uncertain: true, lifecycle: "running" });
    expect(presentation.detail).toContain("ssh timeout");
  });

  it("does not relabel a terminal lifecycle as uncertain", () => {
    expect(runStatusPresentation({ ...base, status: "succeeded", lifecycle_status: "succeeded", observation_state: "unreachable", status_freshness: "stale" }, "zh")).toMatchObject({ label: "succeeded", tone: "good", uncertain: false });
  });

  it("always exposes freshness, source, last check and error metadata for list rows", () => {
	const meta = runObservationPresentation({ ...base, status_checked_at: "2026-07-15T02:00:00Z", status_check_error: "ssh timeout" }, "zh");
	expect(meta).toMatchObject({ freshness: "fresh", source: "remote_tmux", checked: "2026-07-15T02:00:00Z", error: "ssh timeout" });
	expect(meta.label).toContain("新鲜度 fresh");
	expect(meta.label).toContain("来源 remote_tmux");
	expect(meta.label).toContain("最后检查 2026-07-15T02:00:00Z");
	expect(meta.label).toContain("ssh timeout");
  });
});
