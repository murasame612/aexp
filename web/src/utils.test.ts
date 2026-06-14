import { describe, expect, it } from "vitest";
import { filterExecs, filterRuns, parseGPUs } from "./utils";
import type { ExecEvent, Run } from "./types";

describe("utils", () => {
  it("filters runs by query and kind", () => {
    const rows = [
      { id: "run_1", resource_id: "r", status: "succeeded", kind: "formal", command: "python train.py", name: "paper" },
      { id: "run_2", resource_id: "r", status: "succeeded", kind: "smoke", command: "echo ok", name: "wiring" }
    ] as Run[];
    expect(filterRuns(rows, { query: "train", kind: "experiments" }).map((r) => r.id)).toEqual(["run_1"]);
    expect(filterRuns(rows, { query: "", kind: "tools" }).map((r) => r.id)).toEqual(["run_2"]);
  });

  it("filters exec events by command text", () => {
    const rows = [
      { id: "exec_1", resource_id: "mu", actor: "agent", command: "du -sh" },
      { id: "exec_2", resource_id: "ali", actor: "cli", command: "ls" }
    ] as ExecEvent[];
    expect(filterExecs(rows, "du").map((e) => e.id)).toEqual(["exec_1"]);
  });

  it("parses gpu json defensively", () => {
    expect(parseGPUs(`[{"index":0,"util":88}]`)[0].util).toBe(88);
    expect(parseGPUs("{")).toEqual([]);
  });
});
