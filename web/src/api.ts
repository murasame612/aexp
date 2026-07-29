import type {
  EvidenceChain,
  EvidenceChainDetail,
  EvidenceChainNode,
  EvidenceChainEdge,
  EvidenceChainRunCandidate,
  EvidenceGraphPatch,
  EvidenceGraphProposalPlan,
  EvidenceProposal,
  EvidencePromotionPlan,
  ExperimentMatrix,
  ExperimentMatrixDetail,
  ExperimentMatrixRow,
  ExperimentMatrixColumn,
  ExperimentMatrixCell,
  Artifact,
  ArtifactCollection,
  EvidenceSnapshot,
  EvidenceRelease,
  RunManifest,
  RunComparisonAnalysis,
  ExecEvent,
  LogsResponse,
  ManualProjectCategory,
  Paginated,
  ProjectRunCard,
  ProjectAsset,
  ProjectView,
  ProjectDefinition,
  ProjectTarget,
  ProjectTargetPreparePlan,
  ProjectTargetPrepareResponse,
  ProjectJournalEntry,
  Resource,
  PrinterStatus, PrinterJob,
  Run,
	RunProjectAssignmentResult,
	RunSummary,
	RunChangeResponse,
  RunBookmark,
  RunProjectAssignment,
  RunMark,
  Snapshot,
	StorageTarget, DatasetVersion, RunFreeze, RunFreezePlan, RunFreezeFile, RunDataBindings, LogicalRoot, PathPlacement, TransferSummary
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
	const contentType = res.headers.get("content-type") || "";
	if (!contentType.toLowerCase().includes("application/json")) {
		throw new ApiError(res.status, "UI/backend version mismatch", `Expected JSON from ${path}, received ${contentType || "an unknown content type"}. Rebuild and restart the aexp backend.`);
	}
  return (await res.json()) as T;
}

export function getRunDataBindings(token:string, runID:string) {
	return apiFetch<RunDataBindings>(`/runs/${encodeURIComponent(runID)}/data-bindings`, {token});
}

export function getStats(token: string) {
  return apiFetch<{ total_resources: number; active_runs: number; total_runs: number }>("/stats", { token });
}

export function getPrinterStatus(token: string) {
  return apiFetch<PrinterStatus>("/printer/status", { token });
}

export function configurePrinter(token: string, enabled: boolean, queue: string) {
  return apiFetch<{ enabled: boolean; queue: string; last_event_seq: number }>("/printer/config", {
    method: "PATCH", token, body: JSON.stringify({ enabled, queue })
  });
}

export function testPrinter(token: string) {
  return apiFetch<PrinterJob>("/printer/test", { method: "POST", token, body: JSON.stringify({}) });
}

export function getPrinterJobs(token: string, limit = 20) {
  return apiFetch<{ items: PrinterJob[]; total: number }>(`/printer/jobs?limit=${limit}`, { token });
}

export function retryPrinterJob(token: string, id: string) {
  return apiFetch<PrinterJob>(`/printer/jobs/${encodeURIComponent(id)}/retry`, { method: "POST", token, body: JSON.stringify({}) });
}

export function getResources(token: string) {
  return apiFetch<Resource[]>("/resources", { token });
}

export function getStorageTargets(token: string) {
  return apiFetch<{ items: StorageTarget[]; control_plane: string; local_data_path: boolean }>("/storage-targets", { token });
}

export function saveStorageTarget(token: string, target: Pick<StorageTarget, "name" | "resource_id" | "root_path">) {
  return apiFetch<StorageTarget>("/storage-targets", { method: "POST", token, body: JSON.stringify({ ...target, kind: "ssh_rsync" }) });
}

export function updateStorageTarget(token: string, id: string, target: Pick<StorageTarget, "name" | "resource_id" | "root_path">) {
	return apiFetch<StorageTarget>(`/storage-targets/${encodeURIComponent(id)}`, { method: "PUT", token, body: JSON.stringify(target) });
}

export function deleteStorageTarget(token: string, id: string) {
	return apiFetch<{ deleted: string; nas_data_deleted: false; resource_deleted: false }>(`/storage-targets/${encodeURIComponent(id)}`, { method: "DELETE", token });
}

export function testStorageTarget(token: string, id: string) {
	return apiFetch<StorageTarget>(`/storage-targets/${encodeURIComponent(id)}/test`, { method: "POST", token });
}

export function getDatasetVersions(token: string) {
  return apiFetch<{ items: DatasetVersion[]; local_data_path: boolean }>("/dataset-versions", { token });
}

export function getLogicalRoots(token:string) { return apiFetch<{items:LogicalRoot[];total:number}>("/logical-roots", {token}); }
export function saveLogicalRoot(token:string, root:Omit<LogicalRoot,"id"> & {id?:string}) {
	return apiFetch<LogicalRoot>(root.id?`/logical-roots/${encodeURIComponent(root.id)}`:"/logical-roots", {method:root.id?"PUT":"POST",token,body:JSON.stringify(root)});
}
export function deleteLogicalRoot(token:string,id:string) { return apiFetch<void>(`/logical-roots/${encodeURIComponent(id)}`, {method:"DELETE",token}); }
export function locateLogicalPath(token:string,uri:string) { return apiFetch<{items:PathPlacement[];total:number}>(`/paths/locate?uri=${encodeURIComponent(uri)}`, {token}); }
export function inspectLogicalPath(token:string,uri:string,resourceID="") { return apiFetch<{placement:PathPlacement}>("/paths/inspect", {method:"POST",token,body:JSON.stringify({uri,resource_id:resourceID})}); }
export function getTransfers(token:string) { return apiFetch<{items:TransferSummary[];total:number}>("/transfers?limit=100", {token}); }

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
  projectScope?: string;
  query?: string;
  kindGroup?: string;
  trash?: boolean;
  deleted?: boolean;
  refresh?: boolean;
}

function summaryAsRun(summary: RunSummary): Run {
	return { ...summary, command: summary.command_preview || "" };
}

export async function getRunSummaries(token: string, query: RunQuery) {
	const params = new URLSearchParams({ limit: String(query.limit), offset: String(query.offset) });
	if (query.status) params.set("status", query.status);
	if (query.resource) params.set("resource", query.resource);
	if (query.projectScope) params.set("project_scope", query.projectScope);
	if (query.query) params.set("query", query.query);
	if (query.kindGroup && query.kindGroup !== "all" && query.kindGroup !== "favorites") params.set("kind_group", query.kindGroup);
	if (query.trash) params.set("trash", "true");
	if (query.deleted) params.set("deleted", "true");
	const page = await apiFetch<Paginated<RunSummary>>(`/runs/summaries?${params}`, { token });
	return { ...page, items: page.items.map(summaryAsRun) } satisfies Paginated<Run>;
}

export async function getActiveRunSummaries(token: string, limit = 100) {
	const page = await apiFetch<Paginated<RunSummary>>(`/runs/active?limit=${limit}`, { token });
	return { ...page, items: page.items.map(summaryAsRun) } satisfies Paginated<Run>;
}

export function getRunChanges(token: string, afterSeq: number, updatedSince?: string) {
	const params = new URLSearchParams({ after_seq: String(afterSeq) });
	if (updatedSince) params.set("updated_since", updatedSince);
	return apiFetch<RunChangeResponse>(`/runs/changes?${params}`, { token });
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

export function assignRunProject(token: string, id: string, projectID: string, expectedProjectID: string, actor = "ui-v2", reason = "") {
  return apiFetch<RunProjectAssignmentResult>(`/runs/${encodeURIComponent(id)}/project`, {
    method: "PUT",
    token,
    body: JSON.stringify({
      project_id: projectID,
      expected_project_id: expectedProjectID,
      actor,
      reason
    })
  });
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

export function getLogs(token: string, id: string, query: { source?: string; path?: string; limit?: number; offset?: number; afterLine?: number; tail?: boolean }) {
  const params = new URLSearchParams();
  if (query.source) params.set("source", query.source);
  if (query.path) params.set("path", query.path);
  if (query.limit) params.set("limit", String(query.limit));
  if (query.offset) params.set("offset", String(query.offset));
	if (query.afterLine) params.set("after_line", String(query.afterLine));
  if (query.tail) params.set("tail", "true");
  return apiFetch<LogsResponse>(`/runs/${encodeURIComponent(id)}/logs?${params}`, { token });
}

export function getArtifacts(token: string, id: string) {
  return apiFetch<Artifact[]>(`/runs/${encodeURIComponent(id)}/artifacts`, { token });
}

export function getArtifactCollection(token: string, id: string) {
  return apiFetch<ArtifactCollection>(`/runs/${encodeURIComponent(id)}/artifact-collection`, { token });
}

export function collectArtifacts(token: string, id: string) {
  return apiFetch<ArtifactCollection>(`/runs/${encodeURIComponent(id)}/artifacts/collect`, { method: "POST", token });
}

export function getRunManifest(token: string, id: string) {
  return apiFetch<RunManifest>(`/runs/${encodeURIComponent(id)}/manifest`, { token });
}

export function getEvidenceSnapshots(token: string, runID: string) {
  return apiFetch<EvidenceSnapshot[]>(`/runs/${encodeURIComponent(runID)}/snapshots`, { token });
}

export function createEvidenceSnapshot(token: string, runID: string) {
  return apiFetch<EvidenceSnapshot>(`/runs/${encodeURIComponent(runID)}/snapshots`, { method: "POST", token, body: JSON.stringify({}) });
}

export function getEvidenceSnapshot(token: string, snapshotID: string) {
  return apiFetch<EvidenceSnapshot>(`/snapshots/${encodeURIComponent(snapshotID)}`, { token });
}

export function getEvidenceReleases(token: string, snapshotID: string) {
  return apiFetch<EvidenceRelease[]>(`/snapshots/${encodeURIComponent(snapshotID)}/releases`, { token });
}

export function evaluateEvidenceRelease(token: string, snapshotID: string) {
  return apiFetch<EvidenceRelease>(`/snapshots/${encodeURIComponent(snapshotID)}/releases`, { method: "POST", token, body: JSON.stringify({}) });
}

export interface FreezeRequest { profile?: string; to?: string; workspace?: string; project_config?: string; expected_plan_hash?: string; }
export function planRunFreeze(token: string, runID: string, request: FreezeRequest) { return apiFetch<RunFreezePlan>(`/runs/${encodeURIComponent(runID)}/freeze/plan`, { method:"POST", token, body:JSON.stringify(request) }); }
export function createRunFreeze(token: string, runID: string, request: FreezeRequest) { return apiFetch<RunFreeze>(`/runs/${encodeURIComponent(runID)}/freezes`, { method:"POST", token, body:JSON.stringify(request) }); }
export function getRunFreezes(token: string, runID: string) { return apiFetch<RunFreeze[]>(`/runs/${encodeURIComponent(runID)}/freezes`, { token }); }
export function getRunFreeze(token: string, freezeID: string) { return apiFetch<RunFreeze>(`/freezes/${encodeURIComponent(freezeID)}`, { token }); }
export function getRunFreezeManifest(token: string, freezeID: string) { return apiFetch<{freeze:RunFreeze;files:RunFreezeFile[]}>(`/freezes/${encodeURIComponent(freezeID)}/manifest`, { token }); }

export function analyzeRunComparison(token: string, runIds: string[], metricKey = "") {
  return apiFetch<RunComparisonAnalysis>("/run-comparisons/analyze", { method: "POST", token, body: JSON.stringify({ run_ids: runIds, metric_key: metricKey }) });
}

export function getRunMarks(token: string, id: string) {
  return apiFetch<RunMark[]>(`/runs/${encodeURIComponent(id)}/marks?limit=50`, { token });
}

export function getAllRunMarks(token: string, runIds: string[] = []) {
  const params = new URLSearchParams({ limit: String(runIds.length ? Math.min(200, Math.max(50, runIds.length * 4)) : 50) });
  if (runIds.length) params.set("run_ids", runIds.slice(0, 100).join(","));
  return apiFetch<RunMark[]>(`/run-marks?${params}`, { token });
}

export function runMarkAttachmentBlobUrl(markId: string, attachmentId: string, token?: string) {
  const query = token ? `?token=${encodeURIComponent(token)}` : "";
  return `${API}/run-marks/${encodeURIComponent(markId)}/attachments/${encodeURIComponent(attachmentId)}/blob${query}`;
}

export function getBookmarks(token: string) {
  return apiFetch<RunBookmark[]>("/run-bookmarks?limit=500", { token });
}

export function saveBookmark(token: string, runId: string, note = "") {
  return apiFetch<RunBookmark>(`/runs/${encodeURIComponent(runId)}/bookmark`, { method: "POST", token, body: JSON.stringify({ note }) });
}

export function deleteBookmark(token: string, runId: string) {
  return apiFetch<void>(`/runs/${encodeURIComponent(runId)}/bookmark`, { method: "DELETE", token });
}

export function getProjects(token: string) {
  return apiFetch<ProjectView[]>("/projects?limit=500", { token });
}

export function getProjectDefinitions(token: string) {
  return apiFetch<ProjectDefinition[]>("/project-definitions", { token });
}

export function getProjectJournal(
  token: string,
  projectId: string,
  query: { limit?: number; offset?: number; query?: string; runId?: string; nextActionStatus?: string } = {}
) {
  const params = new URLSearchParams({ limit: String(query.limit || 100) });
  if (query.offset) params.set("offset", String(query.offset));
  if (query.query) params.set("query", query.query);
  if (query.runId) params.set("run_id", query.runId);
  if (query.nextActionStatus) params.set("next_action_status", query.nextActionStatus);
  return apiFetch<ProjectJournalEntry[]>(
    `/project-definitions/${encodeURIComponent(projectId)}/journal?${params}`,
    { token }
  );
}

export function createProjectJournalEntry(
  token: string,
  projectId: string,
  entry: Pick<ProjectJournalEntry, "title"> & Partial<Pick<ProjectJournalEntry, "actor" | "body_md" | "next_action" | "run_ids">>
) {
  return apiFetch<ProjectJournalEntry>(
    `/project-definitions/${encodeURIComponent(projectId)}/journal`,
    { method: "POST", token, body: JSON.stringify(entry) }
  );
}

export function getRunJournalEntries(token: string, runId: string, limit = 20) {
  return apiFetch<ProjectJournalEntry[]>(`/runs/${encodeURIComponent(runId)}/journal?limit=${limit}`, { token });
}

export function setProjectJournalNextAction(
  token: string,
  entryId: string,
  status: "open" | "done"
) {
  return apiFetch<ProjectJournalEntry>(
    `/journal-entries/${encodeURIComponent(entryId)}/next-action`,
    { method: "PATCH", token, body: JSON.stringify({ status }) }
  );
}

export function saveProjectDefinition(token: string, project: Partial<ProjectDefinition>) {
  const id = project.id;
  return apiFetch<ProjectDefinition>(id ? `/project-definitions/${encodeURIComponent(id)}` : "/project-definitions", {
    method: id ? "PUT" : "POST",
    token,
    body: JSON.stringify(project)
  });
}

export function getProjectTargets(token: string, projectId: string) {
	return apiFetch<ProjectTarget[]>(`/project-definitions/${encodeURIComponent(projectId)}/targets`, { token });
}

export function getProjectEvidenceMap(token: string, projectId: string) {
  return apiFetch<EvidenceChain>(`/project-definitions/${encodeURIComponent(projectId)}/evidence-map`, { token });
}

export function getProjectAssets(token: string, projectId: string, limit = 50, offset = 0) {
  return apiFetch<Paginated<ProjectAsset>>(`/project-definitions/${encodeURIComponent(projectId)}/assets?limit=${limit}&offset=${offset}`, { token });
}

export function ensureProjectEvidenceMap(token: string, projectId: string) {
  return apiFetch<EvidenceChain>(`/project-definitions/${encodeURIComponent(projectId)}/evidence-map`, { method: "POST", token });
}

export function saveProjectTarget(token: string, projectId: string, target: Partial<ProjectTarget>) {
  const id = target.id;
  return apiFetch<ProjectTarget>(id ? `/project-targets/${encodeURIComponent(id)}` : `/project-definitions/${encodeURIComponent(projectId)}/targets`, {
    method: id ? "PUT" : "POST",
    token,
    body: JSON.stringify({ ...target, project_id: projectId })
  });
}

export function getProjectTargetPreparePlan(token: string, projectId: string, targetId: string) {
  return apiFetch<ProjectTargetPreparePlan>(`/project-definitions/${encodeURIComponent(projectId)}/targets/${encodeURIComponent(targetId)}/prepare-plan`, { method: "POST", token });
}

export function prepareProjectTarget(token: string, projectId: string, targetId: string) {
  return apiFetch<ProjectTargetPrepareResponse>(`/project-definitions/${encodeURIComponent(projectId)}/targets/${encodeURIComponent(targetId)}/prepare`, { method: "POST", token });
}

export function getManualProjectCategories(token: string) {
  return apiFetch<ManualProjectCategory[]>("/manual-project-categories", { token });
}

export function getManualRunProjectAssignments(token: string) {
  return apiFetch<RunProjectAssignment[]>("/manual-run-project-assignments", { token });
}

export function unassignRunManualProjectCategory(token: string, runId: string) {
  return apiFetch<void>(`/runs/${encodeURIComponent(runId)}/manual-project-category`, { method: "DELETE", token });
}

export function getExperimentMatrices(token: string, query = "") {
  const params = new URLSearchParams({ limit: "200" });
  if (query) params.set("query", query);
  return apiFetch<ExperimentMatrix[]>(`/experiment-matrices?${params}`, { token });
}

export function createExperimentMatrix(
  token: string,
  payload: Pick<ExperimentMatrix, "title" | "description" | "source_kind" | "source_id" | "source_name" | "default_metric_key" | "default_metric_goal"> & { seed_from_source?: boolean }
) {
  return apiFetch<ExperimentMatrixDetail>("/experiment-matrices", {
    method: "POST",
    token,
    body: JSON.stringify(payload)
  });
}

export function updateExperimentMatrix(token: string, id: string, payload: Pick<ExperimentMatrix, "title" | "description" | "source_kind" | "source_id" | "source_name" | "default_metric_key" | "default_metric_goal" | "data_json">) {
  return apiFetch<ExperimentMatrix>(`/experiment-matrices/${encodeURIComponent(id)}`, {
    method: "PUT",
    token,
    body: JSON.stringify(payload)
  });
}

export function deleteExperimentMatrix(token: string, id: string) {
  return apiFetch<void>(`/experiment-matrices/${encodeURIComponent(id)}`, { method: "DELETE", token });
}

export function getExperimentMatrix(token: string, id: string) {
  return apiFetch<ExperimentMatrixDetail>(`/experiment-matrices/${encodeURIComponent(id)}`, { token });
}

export function saveExperimentMatrixGrid(token: string, id: string, grid: { rows: ExperimentMatrixRow[]; columns: ExperimentMatrixColumn[]; cells: ExperimentMatrixCell[] }) {
  return apiFetch<ExperimentMatrixDetail>(`/experiment-matrices/${encodeURIComponent(id)}/grid`, {
    method: "PUT",
    token,
    body: JSON.stringify(grid)
  });
}

export function getEvidenceChains(token: string, query = "", projectID = "") {
  const params = new URLSearchParams({ limit: "200" });
  if (query) params.set("query", query);
  if (projectID) params.set("project_id", projectID);
  return apiFetch<EvidenceChain[]>(`/evidence-chains?${params}`, { token });
}

export function createEvidenceChain(token: string, payload: Pick<EvidenceChain, "title" | "description"> & Partial<Pick<EvidenceChain, "project_id" | "role" | "status" | "routing_hints">>) {
  return apiFetch<EvidenceChain>("/evidence-chains", {
    method: "POST",
    token,
    body: JSON.stringify(payload)
  });
}

export function createProjectEvidenceMap(token: string, projectID: string, payload: Pick<EvidenceChain, "title" | "description"> & Partial<Pick<EvidenceChain, "role" | "status" | "routing_hints">>) {
  return apiFetch<EvidenceChain>(`/project-definitions/${encodeURIComponent(projectID)}/evidence-maps`, {
    method: "POST",
    token,
    body: JSON.stringify(payload)
  });
}

export function updateEvidenceChain(token: string, id: string, payload: Pick<EvidenceChain, "title" | "description"> & Partial<Pick<EvidenceChain, "project_id" | "role" | "status" | "routing_hints">>) {
  return apiFetch<EvidenceChain>(`/evidence-chains/${encodeURIComponent(id)}`, {
    method: "PUT",
    token,
    body: JSON.stringify(payload)
  });
}

export function deleteEvidenceChain(token: string, id: string, permanent = false) {
  const suffix = permanent ? "?permanent=true" : "";
  return apiFetch<void>(`/evidence-chains/${encodeURIComponent(id)}${suffix}`, { method: "DELETE", token });
}

export function getEvidenceChain(token: string, id: string) {
  return apiFetch<EvidenceChainDetail>(`/evidence-chains/${encodeURIComponent(id)}`, { token });
}

export function saveEvidenceChainGraph(token: string, id: string, graph: { nodes: EvidenceChainNode[]; edges: EvidenceChainEdge[] }, expectedRevision: number) {
  return apiFetch<EvidenceChainDetail>(`/evidence-chains/${encodeURIComponent(id)}/graph`, {
    method: "PUT",
    token,
    body: JSON.stringify({ ...graph, expected_revision: expectedRevision })
  });
}

export function getEvidenceRunCandidates(token: string, query = "", limit = 80) {
  const params = new URLSearchParams({ limit: String(limit) });
  if (query) params.set("query", query);
  return apiFetch<EvidenceChainRunCandidate[]>(`/evidence-run-candidates?${params}`, { token });
}

export function getEvidenceGraphProposal(token: string, runID: string) {
  return apiFetch<ProjectRunCard>(`/runs/${encodeURIComponent(runID)}/evidence-proposal`, { token });
}

export function planEvidenceGraphProposal(token: string, runID: string) {
  return apiFetch<EvidenceGraphProposalPlan>(`/runs/${encodeURIComponent(runID)}/evidence-proposal/plan`, {
    method: "POST",
    token,
    body: "{}"
  });
}

export function reviewEvidenceGraphProposal(token: string, runID: string, action: "accept" | "reject" | "expire", reviewer = "ui") {
  return apiFetch<ProjectRunCard>(`/runs/${encodeURIComponent(runID)}/evidence-proposal/review`, {
    method: "POST",
    token,
    body: JSON.stringify({ action, reviewer })
  });
}

export function getProjectEvidenceProposals(token: string, projectID: string, status = "") {
  const params = new URLSearchParams({ limit: "100" });
  if (status) params.set("status", status);
  return apiFetch<EvidenceProposal[]>(`/project-definitions/${encodeURIComponent(projectID)}/evidence-proposals?${params}`, { token });
}

export function createProjectEvidenceProposal(
  token: string,
  projectID: string,
  payload: {
    target_map_id?: string;
    actor: string;
    summary: string;
    routing_reason?: string;
    project_level_impact?: boolean;
    source_run_ids?: string[];
    source_snapshot_ids?: string[];
    patch: EvidenceGraphPatch;
  }
) {
  return apiFetch<EvidenceProposal>(`/project-definitions/${encodeURIComponent(projectID)}/evidence-proposals`, {
    method: "POST",
    token,
    body: JSON.stringify(payload)
  });
}

export function getEvidenceProposal(token: string, proposalID: string) {
  return apiFetch<EvidenceProposal>(`/evidence-proposals/${encodeURIComponent(proposalID)}`, { token });
}

export function planEvidenceProposal(token: string, proposalID: string) {
  return apiFetch<EvidenceGraphProposalPlan>(`/evidence-proposals/${encodeURIComponent(proposalID)}/plan`, {
    method: "POST",
    token,
    body: "{}"
  });
}

export function reviewEvidenceProposal(token: string, proposalID: string, action: "accept" | "reject" | "expire", reviewer = "ui") {
  return apiFetch<EvidenceProposal>(`/evidence-proposals/${encodeURIComponent(proposalID)}/review`, {
    method: "POST",
    token,
    body: JSON.stringify({ action, reviewer })
  });
}

export function rerouteEvidenceProposal(
  token: string,
  proposalID: string,
  targetMapID: string,
  routingReason: string,
  projectLevelImpact = false
) {
  return apiFetch<EvidenceProposal>(`/evidence-proposals/${encodeURIComponent(proposalID)}/reroute`, {
    method: "POST",
    token,
    body: JSON.stringify({
      target_map_id: targetMapID,
      routing_reason: routingReason,
      project_level_impact: projectLevelImpact
    })
  });
}

export function planEvidencePromotion(
  token: string,
  sourceMapID: string,
  payload: { source_node_ids: string[]; summary: string; node_type?: "claim" | "issue" | "plan"; actor?: string }
) {
  return apiFetch<EvidencePromotionPlan>(`/evidence-chains/${encodeURIComponent(sourceMapID)}/promotion/plan`, {
    method: "POST",
    token,
    body: JSON.stringify(payload)
  });
}

export function createEvidencePromotion(
  token: string,
  sourceMapID: string,
  payload: { source_node_ids: string[]; summary: string; node_type?: "claim" | "issue" | "plan"; actor?: string; expected_plan_hash: string }
) {
  return apiFetch<EvidenceProposal>(`/evidence-chains/${encodeURIComponent(sourceMapID)}/promotions`, {
    method: "POST",
    token,
    body: JSON.stringify(payload)
  });
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
