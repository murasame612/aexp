import type { ProjectDefinition } from "./types";

export function projectScopeFromFilterValue(value: string): string {
  for (const prefix of ["card:", "manual:"]) {
    if (value.startsWith(prefix)) return value.slice(prefix.length);
  }
  return value;
}

export function projectRunFilterOptions(
  projects: ProjectDefinition[]
): Array<[string, string]> {
  const seen = new Set<string>();
  const options: Array<[string, string]> = [];
  for (const project of projects) {
    const projectID = project.id.trim();
    if (!projectID || seen.has(projectID)) continue;
    seen.add(projectID);
    options.push([projectID, project.name || projectID]);
  }
  return options;
}
