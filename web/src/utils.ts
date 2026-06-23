import type { GPUInfo, MetricPoint, Run, RunBookmark, RunMark } from "./types";

export function text(value: unknown): string {
  return value == null ? "" : String(value);
}

export function parseJSON<T>(raw: unknown, fallback: T): T {
  if (typeof raw !== "string" || !raw.trim()) return fallback;
  try {
    return JSON.parse(raw) as T;
  } catch {
    return fallback;
  }
}

export function parseGPUs(raw: unknown): GPUInfo[] {
  const parsed = parseJSON<GPUInfo[]>(raw, []);
  return Array.isArray(parsed) ? parsed : [];
}

export function fmtTime(raw: unknown): string {
  const value = timeString(raw);
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return date.toLocaleString();
}

export function fmtShortTime(raw: unknown): string {
  const value = timeString(raw);
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return date.toLocaleDateString(undefined, { month: "2-digit", day: "2-digit" }) + " " + date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

export function timeString(raw: unknown): string {
  if (typeof raw === "string") return raw;
  if (raw && typeof raw === "object") {
    const obj = raw as { Time?: string; Valid?: boolean };
    if (obj.Valid === false) return "";
    return obj.Time || "";
  }
  return "";
}

export function fmtDuration(ms?: number): string {
  if (!ms || ms < 0) return "-";
  const s = Math.round(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m ${sec}s`;
  return `${sec}s`;
}

export function fmtMB(value?: number): string {
  if (!value || value < 0) return "-";
  if (value >= 1024) return `${(value / 1024).toFixed(1)} GB`;
  return `${value.toFixed(0)} MB`;
}

export function runGPU(index?: number): string {
  if (index === -2) return "none";
  if (index === -1 || index == null) return "all";
  return String(index);
}

export function runTitle(run: Run): string {
  return run.name || run.id;
}

export function isActiveRun(run: Run) {
  return run.status === "running" || run.status === "starting" || run.status === "queued" || run.status === "created" || run.status === "ssh_unreachable";
}

export function isCompareEligible(run: Run) {
  const kind = (run.kind || "formal").toLowerCase();
  return (kind === "formal" || kind === "ablation") && !!uiEventsPath(run);
}

export function uiEventsPath(run: Run): string {
  return run.ui_events_path || "";
}

export function filterRuns(runs: Run[], opts: { query?: string; kind?: string; bookmarks?: RunBookmark[] }) {
  const q = (opts.query || "").trim().toLowerCase();
  const bookmarkIds = new Set((opts.bookmarks || []).map((b) => b.run_id));
  return runs.filter((run) => {
    const kind = (run.kind || "formal").toLowerCase();
    if (opts.kind === "experiments" && (kind === "setup" || kind === "smoke")) return false;
    if (opts.kind === "tools" && kind !== "setup" && kind !== "smoke") return false;
    if (opts.kind === "favorites" && !bookmarkIds.has(run.id)) return false;
    if (!q) return true;
    return [run.id, run.name, run.command, run.cwd, run.resource_id, kind, run.status].some((part) => text(part).toLowerCase().includes(q));
  });
}

export function filterExecs<T extends { id: string; actor?: string; command?: string; resource_id?: string }>(rows: T[], query: string) {
  const q = query.trim().toLowerCase();
  if (!q) return rows;
  return rows.filter((row) => [row.id, row.actor, row.command, row.resource_id].some((part) => text(part).toLowerCase().includes(q)));
}

export function markCountByRun(marks: RunMark[]) {
  const out = new Map<string, number>();
  for (const mark of marks) out.set(mark.run_id, (out.get(mark.run_id) || 0) + 1);
  return out;
}

export function latestMetricByName(points: MetricPoint[]) {
  const map = new Map<string, MetricPoint>();
  for (const point of points) {
    map.set((point.series ? point.series + "/" : "") + point.name, point);
  }
  return Array.from(map.values()).sort((a, b) => a.name.localeCompare(b.name));
}
