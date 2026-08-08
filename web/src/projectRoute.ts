export type ProjectSection = "journal" | "literature" | "runs" | "assets" | "research-graph";

export interface ProjectRoute {
  projectId: string;
  section: ProjectSection;
}

export function parseProjectRoute(pathname: string): ProjectRoute | null {
  const match = pathname.match(/^\/ui-v2\/projects\/([^/]+)(?:\/(journal|literature|runs|assets|research-graph))?\/?$/);
  if (!match) return null;
  return {
    projectId: decodeURIComponent(match[1]),
    section: (match[2] || "journal") as ProjectSection
  };
}

export function readEvidenceMapFromSearch(search: string): string {
  return new URLSearchParams(search).get("map")?.trim() || "";
}

export function withEvidenceMapSearch(search: string, mapId: string): string {
  const params = new URLSearchParams(search);
  const normalized = mapId.trim();
  if (normalized) params.set("map", normalized);
  else params.delete("map");
  const next = params.toString();
  return next ? `?${next}` : "";
}
