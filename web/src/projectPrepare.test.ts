import { describe, expect, it } from "vitest";
import { canStartPrepare, targetConfigDrifted, targetDraftErrors } from "./projectPrepare";
import type { ProjectDefinition, ProjectTarget } from "./types";

const target: ProjectTarget = { id: "t", project_id: "p", name: "mu", resource_id: "r", cwd: "/ws/p", env_strategy: "auto", default_gpu: -1, prepare_command: "uv sync", readiness: "unknown" };

describe("project prepare launch guard", () => {
  it("requires an explicit target and prepare command", () => {
    expect(targetDraftErrors({})).toEqual(["name is required", "resource is required", "cwd is required", "prepare command is required"]);
    expect(targetDraftErrors(target)).toEqual([]);
  });

  it("requires plan review and prevents duplicate active prepare", () => {
    expect(canStartPrepare(target, false)).toBe(false);
    expect(canStartPrepare(target, true)).toBe(true);
    expect(canStartPrepare({ ...target, readiness: "checking" }, true)).toBe(false);
  });

  it("marks a previously ready target drifted when its project fingerprint changes", () => {
    const project: ProjectDefinition = { id: "p", name: "P", config_hash: "sha256:new" };
    expect(targetConfigDrifted(project, { ...target, readiness: "ready", observed_config_hash: "sha256:old" })).toBe(true);
    expect(targetConfigDrifted(project, { ...target, readiness: "ready", observed_config_hash: "sha256:new" })).toBe(false);
  });
});
