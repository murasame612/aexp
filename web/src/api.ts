import type {
  EvidenceChain,
  EvidenceChainDetail,
  EvidenceChainNode,
  EvidenceChainEdge,
  EvidenceChainRunCandidate,
  Artifact,
  ExecEvent,
  LogsResponse,
  Paginated,
  ProjectView,
  Resource,
  Run,
  RunBookmark,
  RunMark,
  Snapshot
} from "./types";

const API = "/api/v1";

export class ApiError extends Error {
  status: number;
  details: string;

  constructor(status: number, message: string, details = "") {
    super(message);
    this.status = status;
    this.details = details;
  }
}

export interface RequestOptions extends RequestInit {
  token?: string;
}

function headers(token?: string, body?: BodyInit | null): HeadersInit {
  const out: Record<string, string> = {};
  if (token) out.Authorization = "Bearer " + token;
  if (body != null && !(body instanceof FormData)) out["Content-Type"] = "application/json";
  return out;
}

export async function apiFetch<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const res = await fetch(API + path, {
    ...options,
    headers: {
      ...headers(options.token, options.body),
      ...(options.headers || {})
    }
  });
  if (!res.ok) {
    let details = "";
    try {
      const payload = await res.json();
      details = String(payload.details || payload.error || "");
    } catch {
      details = await res.text().catch(() => "");
    }
    throw new ApiError(res.status, res.status === 401 ? "Unauthorized" : "Request failed", details);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export function getStats(token: string) {
  return apiFetch<{ total_resources: number; active_runs: number; total_runs: number }>("/stats", { token });
}

export function getResources(token: string) {
  return apiFetch<Resource[]>("/resources", { token });
}

export function getResource(token: string, id: string) {
  return apiFetch<Resource>(`/resources/${encodeURIComponent(id)}`, { token });
}

export function saveResource(token: string, resource: Partial<Resource>) {
  const id = resource.id;
  return apiFetch<Resource>(id ? `/resources/${encodeURIComponent(id)}` : "/resources", {
    method: id ? "PUT" : "POST",
    token,
    body: JSON.stringify(resource)
  });
}

export function deleteResource(token: string, id: string) {
  return apiFetch<void>(`/resources/${encodeURIComponent(id)}`, { method: "DELETE", token });
}

export function refreshResource(token: string, id: string) {
  return apiFetch<Resource>(`/resources/${encodeURIComponent(id)}/refresh`, { method: "POST", token });
}

export function testResource(token: string, id: string) {
  return apiFetch<Resource>(`/resources/${encodeURIComponent(id)}/test`, { method: "POST", token });
}

export function localResourceDefaults(token: string) {
  return apiFetch<Resource>("/resources/local-defaults", { token });
}

export function createLocalResource(token: string, name: string, rootDir: string) {
  return apiFetch<Resource>("/resources/local", {
    method: "POST",
    token,
    body: JSON.stringify({ name, root_dir: rootDir })
  });
}

export interface RunQuery {
  limit: number;
  offset: number;
  status?: string;
  resource?: string;
  trash?: boolean;
  deleted?: boolean;
  refresh?: boolean;
}

export function getRuns(token: string, query: RunQuery) {
  const params = new URLSearchParams({
    limit: String(query.limit),
    offset: String(query.offset),
    meta: "true"
  });
  if (query.status) params.set("status", query.status);
  if (query.resource) params.set("resource", query.resource);
  if (query.trash) params.set("trash", "true");
  if (query.deleted) params.set("deleted", "true");
  if (query.refresh === false) params.set("refresh", "false");
  return apiFetch<Paginated<Run>>(`/runs?${params}`, { token });
}

export function getRun(token: string, id: string) {
  return apiFetch<Run>(`/runs/${encodeURIComponent(id)}`, { token });
}

export function cancelRun(token: string, id: string) {
  return apiFetch<Run>(`/runs/${encodeURIComponent(id)}/cancel`, { method: "POST", token });
}

export function archiveRun(token: string, id: string) {
  return apiFetch<Run>(`/runs/${encodeURIComponent(id)}/archive`, { method: "POST", token });
}

export function restoreRun(token: string, id: string) {
  return apiFetch<Run>(`/runs/${encodeURIComponent(id)}/restore`, { method: "POST", token });
}

export function deleteRunLogically(token: string, id: string) {
  return apiFetch<void>(`/runs/${encodeURIComponent(id)}`, { method: "DELETE", token });
}

export function statusCheck(token: string, id: string) {
  return apiFetch<Run>(`/runs/${encodeURIComponent(id)}/status-check`, { method: "POST", token });
}

export function getLogs(token: string, id: string, query: { source?: string; path?: string; limit?: number; offset?: number; tail?: boolean }) {
  const params = new URLSearchParams();
  if (query.source) params.set("source", query.source);
  if (query.path) params.set("path", query.path);
  if (query.limit) params.set("limit", String(query.limit));
  if (query.offset) params.set("offset", String(query.offset));
  if (query.tail) params.set("tail", "true");
  return apiFetch<LogsResponse>(`/runs/${encodeURIComponent(id)}/logs?${params}`, { token });
}

export function getArtifacts(token: string, id: string) {
  return apiFetch<Artifact[]>(`/runs/${encodeURIComponent(id)}/artifacts`, { token });
}

export function getRunMarks(token: string, id: string) {
  return apiFetch<RunMark[]>(`/runs/${encodeURIComponent(id)}/marks?limit=50`, { token });
}

export function getAllRunMarks(token: string) {
  return apiFetch<RunMark[]>("/run-marks?limit=500", { token });
}

export function runMarkAttachmentBlobUrl(markId: string, attachmentId: string, token?: string) {
  const query = token ? `?token=${encodeURIComponent(token)}` : "";
  return `${API}/run-marks/${encodeURIComponent(markId)}/attachments/${encodeURIComponent(attachmentId)}/blob${query}`;
}

export function getBookmarks(token: string) {
  return apiFetch<RunBookmark[]>("/run-bookmarks?limit=500", { token });
}

export function saveBookmark(token: string, runId: string) {
  return apiFetch<RunBookmark>(`/runs/${encodeURIComponent(runId)}/bookmark`, { method: "POST", token, body: JSON.stringify({}) });
}

export function deleteBookmark(token: string, runId: string) {
  return apiFetch<void>(`/runs/${encodeURIComponent(runId)}/bookmark`, { method: "DELETE", token });
}

export function getProjects(token: string) {
  return apiFetch<ProjectView[]>("/projects?limit=500", { token });
}

export function getEvidenceChains(token: string, query = "") {
  const params = new URLSearchParams({ limit: "200" });
  if (query) params.set("query", query);
  return apiFetch<EvidenceChain[]>(`/evidence-chains?${params}`, { token });
}

export function createEvidenceChain(token: string, payload: Pick<EvidenceChain, "title" | "description">) {
  return apiFetch<EvidenceChain>("/evidence-chains", {
    method: "POST",
    token,
    body: JSON.stringify(payload)
  });
}

export function updateEvidenceChain(token: string, id: string, payload: Pick<EvidenceChain, "title" | "description">) {
  return apiFetch<EvidenceChain>(`/evidence-chains/${encodeURIComponent(id)}`, {
    method: "PUT",
    token,
    body: JSON.stringify(payload)
  });
}

export function deleteEvidenceChain(token: string, id: string) {
  return apiFetch<void>(`/evidence-chains/${encodeURIComponent(id)}`, { method: "DELETE", token });
}

export function getEvidenceChain(token: string, id: string) {
  return apiFetch<EvidenceChainDetail>(`/evidence-chains/${encodeURIComponent(id)}`, { token });
}

export function saveEvidenceChainGraph(token: string, id: string, graph: { nodes: EvidenceChainNode[]; edges: EvidenceChainEdge[] }) {
  return apiFetch<EvidenceChainDetail>(`/evidence-chains/${encodeURIComponent(id)}/graph`, {
    method: "PUT",
    token,
    body: JSON.stringify(graph)
  });
}

export function getEvidenceRunCandidates(token: string, query = "", limit = 80) {
  const params = new URLSearchParams({ limit: String(limit) });
  if (query) params.set("query", query);
  return apiFetch<EvidenceChainRunCandidate[]>(`/evidence-run-candidates?${params}`, { token });
}

export function getExecEvents(token: string, query: { limit: number; offset: number; resource_id?: string; actor?: string }) {
  const params = new URLSearchParams({ limit: String(query.limit), offset: String(query.offset), meta: "true" });
  if (query.resource_id) params.set("resource_id", query.resource_id);
  if (query.actor) params.set("actor", query.actor);
  return apiFetch<Paginated<ExecEvent>>(`/exec-events?${params}`, { token });
}

export function getSnapshots(token: string, resourceID: string) {
  return apiFetch<Snapshot[]>(`/resources/${encodeURIComponent(resourceID)}/snapshots?limit=200`, { token });
}

export function wsURL(path: string, token: string, params: Record<string, string> = {}) {
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  const search = new URLSearchParams(params);
  if (token) search.set("token", token);
  const qs = search.toString();
  return `${proto}//${window.location.host}${path}${qs ? "?" + qs : ""}`;
}

export function unwrapPaginated<T>(payload: T[] | Paginated<T>): Paginated<T> {
  if (Array.isArray(payload)) return { items: payload, total: payload.length, limit: payload.length, offset: 0 };
  return payload;
}
