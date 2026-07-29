import { describe, expect, it } from "vitest";
import { projectRunFilterOptions, projectScopeFromFilterValue } from "./projectFilters";
import type { ProjectDefinition } from "./types";

describe("project run filters", () => {
  it("uses one canonical option per project instead of repeating manual groups", () => {
    const projects: ProjectDefinition[] = [
      { id: "project-a", name: "Project A" },
      { id: "project-a", name: "Project A" },
      { id: "__unassigned__", name: "Unassigned runs" }
    ];

    expect(projectRunFilterOptions(projects)).toEqual([
      ["project-a", "Project A"],
      ["__unassigned__", "Unassigned runs"]
    ]);
  });

  it("maps both legacy filter prefixes to the canonical server scope", () => {
    expect(projectScopeFromFilterValue("card:project-a")).toBe("project-a");
    expect(projectScopeFromFilterValue("manual:manual-a")).toBe("manual-a");
    expect(projectScopeFromFilterValue("project-a")).toBe("project-a");
  });
});
