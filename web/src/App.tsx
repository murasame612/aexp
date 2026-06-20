import { useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { AnimatePresence, LayoutGroup, motion } from "framer-motion";
import ReactMarkdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  Activity,
  Archive,
  BarChart3,
  Check,
  ChevronLeft,
  ChevronRight,
  Clock,
  Cpu,
  Database,
  ExternalLink,
  Grid3X3,
  Heart,
  Languages,
  Network,
  PlayCircle,
  Plus,
  RefreshCcw,
  Server,
  Settings,
  Star,
  Terminal,
  Trash2,
  X
} from "lucide-react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import * as echarts from "echarts";
import {
  ApiError,
  archiveRun,
  assignRunManualProjectCategory,
  cancelRun,
  createManualProjectCategory,
  createLocalResource,
  deleteBookmark,
  deleteResource,
  deleteRunLogically,
  getAllRunMarks,
  getArtifacts,
  getBookmarks,
  getExecEvents,
  getLogs,
  getManualProjectCategories,
  getManualRunProjectAssignments,
  getProjects,
  getResources,
  getRun,
  getRunMarks,
  getRuns,
  getStats,
  localResourceDefaults,
  refreshResource,
  restoreRun,
  runMarkAttachmentBlobUrl,
  saveBookmark,
  saveResource,
  statusCheck,
  testResource,
  unassignRunManualProjectCategory,
  wsURL
} from "./api";
import { makeT, type I18nKey } from "./i18n";
import { useAppStore } from "./store";
import type {
  Artifact,
  ConfirmState,
  ExecEvent,
  GPUInfo,
  LogsResponse,
  ManualProjectCategory,
  MetricPoint,
  ParamPoint,
  ParsedEvents,
  ProgressPoint,
  ProjectRunCard,
  ProjectView,
  Resource,
  Run,
  RunBookmark,
  RunProjectAssignment,
  RunMark
} from "./types";
import {
  filterExecs,
  filterRuns,
  fmtDuration,
  fmtMB,
  fmtShortTime,
  fmtTime,
  isActiveRun,
  isCompareEligible,
  markCountByRun,
  parseGPUs,
  parseJSON,
  runGPU,
  runTitle,
  text,
  uiEventsPath
} from "./utils";
import { parseEventLines, summarizeMetricFamilies, summarizeProgress, type MetricSeriesSummary, type ProgressSummary } from "./events";
import { isEmptyRemotePathSnapshot, logSnapshotError, mergeLogSnapshot } from "./logs";
import { EvidenceChainBoard } from "./EvidenceChainBoard";
import { ExperimentMatrixPage } from "./ExperimentMatrixPage";

type Tab = "dashboard" | "resources" | "projects" | "matrices" | "evidence" | "runs" | "favorites" | "execs";

interface RunProjectMeta {
  projectId: string;
  projectName: string;
  cardTitle: string;
  cardSummary?: string;
  evidenceLevel?: string;
  source?: "manual" | "card";
}

const pageSize = 100;

export function App() {
  const token = useAppStore((s) => s.token);
  const locale = useAppStore((s) => s.locale);
  const setToken = useAppStore((s) => s.setToken);
  const clearToken = useAppStore((s) => s.clearToken);
  const setLocale = useAppStore((s) => s.setLocale);
  const selectedRunIds = useAppStore((s) => s.selectedRunIds);
  const clearSelectedRuns = useAppStore((s) => s.clearSelectedRuns);
  const t = useMemo(() => makeT(locale), [locale]);
  const queryClient = useQueryClient();

  const [tab, setTab] = useState<Tab>(readInitialTab());
  const [tokenDraft, setTokenDraft] = useState(token);
  const [runPage, setRunPage] = useState(0);
  const [execPage, setExecPage] = useState(0);
  const [runTrash, setRunTrash] = useState(false);
  const [runStatus, setRunStatus] = useState("");
  const [runResource, setRunResource] = useState("");
  const [runKind, setRunKind] = useState("experiments");
  const [runProject, setRunProject] = useState("");
  const [runQuery, setRunQuery] = useState("");
  const [execResource, setExecResource] = useState("");
  const [execActor, setExecActor] = useState("");
  const [execQuery, setExecQuery] = useState("");
  const [projectQuery, setProjectQuery] = useState("");
  const [favoriteQuery, setFavoriteQuery] = useState("");
  const [detailRunId, setDetailRunId] = useState<string | null>(readDeepLinkRun());
  const [resourceForm, setResourceForm] = useState<Partial<Resource> | null>(null);
  const [confirm, setConfirm] = useState<ConfirmState | null>(null);
  const [compareOpen, setCompareOpen] = useState(false);
  const [selectedRunListMark, setSelectedRunListMark] = useState<RunMark | null>(null);
  const [bookmarkEditor, setBookmarkEditor] = useState<{ run: Run; note: string } | null>(null);
  const [projectEditorRun, setProjectEditorRun] = useState<Run | null>(null);

  const stats = useQuery({ queryKey: ["stats", token], queryFn: () => getStats(token), refetchInterval: 5000 });
  const resources = useQuery({ queryKey: ["resources", token], queryFn: () => getResources(token), refetchInterval: 5000 });
  const marks = useQuery({ queryKey: ["marks", token], queryFn: () => getAllRunMarks(token), refetchInterval: 10000 });
  const bookmarks = useQuery({ queryKey: ["bookmarks", token], queryFn: () => getBookmarks(token), refetchInterval: 10000 });
  const projects = useQuery({ queryKey: ["projects", token], queryFn: () => getProjects(token), refetchInterval: 12000 });
  const manualProjectCategories = useQuery({ queryKey: ["manual-project-categories", token], queryFn: () => getManualProjectCategories(token), refetchInterval: 12000 });
  const manualRunProjectAssignments = useQuery({ queryKey: ["manual-run-project-assignments", token], queryFn: () => getManualRunProjectAssignments(token), refetchInterval: 12000 });
  const runs = useQuery({
    queryKey: ["runs", token, runPage, runStatus, runResource, runTrash],
    queryFn: () =>
      getRuns(token, {
        limit: pageSize,
        offset: runPage * pageSize,
        status: runStatus,
        resource: runResource,
        trash: runTrash
      }),
    refetchInterval: 5000
  });
  const execs = useQuery({
    queryKey: ["execs", token, execPage, execResource, execActor],
    queryFn: () => getExecEvents(token, { limit: pageSize, offset: execPage * pageSize, resource_id: execResource, actor: execActor }),
    refetchInterval: 8000
  });

  const resourceList = resources.data || [];
  const runList = runs.data?.items || [];
  const bookmarkList = bookmarks.data || [];
  const markList = marks.data || [];
  const runMarks = useMemo(() => markCountByRun(markList), [markList]);
  const runMarkPreviews = useMemo(() => marksByRun(markList), [markList]);
  const resourceById = useMemo(() => new Map(resourceList.map((r) => [r.id, r])), [resourceList]);
  const projectList = projects.data || [];
  const manualCategoryList = manualProjectCategories.data || [];
  const manualAssignmentList = manualRunProjectAssignments.data || [];
  const manualCategoryById = useMemo(() => new Map(manualCategoryList.map((category) => [category.id, category])), [manualCategoryList]);
  const manualAssignmentByRun = useMemo(() => new Map(manualAssignmentList.map((assignment) => [assignment.run_id, assignment])), [manualAssignmentList]);
  const runProjectById = useMemo(() => buildRunProjectIndex(projectList, t, manualAssignmentByRun, manualCategoryById), [projectList, t, manualAssignmentByRun, manualCategoryById]);
  const visibleRuns = useMemo(() => {
    const filtered = filterRuns(runList, { query: runQuery, kind: runKind, bookmarks: bookmarkList });
    if (!runProject) return filtered;
    return filtered.filter((run) => runProjectMatches(runProjectById.get(run.id), runProject));
  }, [runList, runQuery, runKind, bookmarkList, runProject, runProjectById]);
  const activeRuns = useMemo(() => runList.filter(isActiveRun), [runList]);
  const visibleExecs = useMemo(() => filterExecs(execs.data?.items || [], execQuery), [execs.data, execQuery]);
  const favoriteRuns = useMemo(() => filterRuns(runList, { query: favoriteQuery, kind: "favorites", bookmarks: bookmarkList }), [runList, favoriteQuery, bookmarkList]);
  const visibleProjects = useMemo(() => filterProjects(projectList, projectQuery), [projectList, projectQuery]);
  const runProjectOptions = useMemo(
    () => [
      ["", t("allProjects")] as [string, string],
      ...manualCategoryList.map((category) => [manualProjectFilterValue(category.id), category.name] as [string, string]),
      ...projectList.map((project) => [projectCardFilterValue(project.project_id), projectDisplayName(project, t)] as [string, string])
    ],
    [manualCategoryList, projectList, t]
  );
  const selectedRuns = useMemo(() => runList.filter((run) => selectedRunIds.has(run.id) && isCompareEligible(run)), [runList, selectedRunIds]);

  const refreshAll = () => {
    void queryClient.invalidateQueries();
  };

  const setActiveTab = (next: Tab) => {
    setTab(next);
    history.replaceState(null, "", pathForTab(next));
  };

  useEffect(() => {
    const syncFromPath = () => setTab(readInitialTab());
    window.addEventListener("popstate", syncFromPath);
    return () => window.removeEventListener("popstate", syncFromPath);
  }, []);

  const authError = [stats.error, resources.error, runs.error].find((err) => err instanceof ApiError && err.status === 401);

  function askConfirm(next: ConfirmState) {
    setConfirm(next);
  }

  async function invalidateOperationalData() {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["resources"] }),
      queryClient.invalidateQueries({ queryKey: ["runs"] }),
      queryClient.invalidateQueries({ queryKey: ["marks"] }),
      queryClient.invalidateQueries({ queryKey: ["bookmarks"] }),
      queryClient.invalidateQueries({ queryKey: ["projects"] }),
      queryClient.invalidateQueries({ queryKey: ["manual-project-categories"] }),
      queryClient.invalidateQueries({ queryKey: ["manual-run-project-assignments"] }),
      queryClient.invalidateQueries({ queryKey: ["stats"] })
    ]);
  }

  async function refreshManualProjectData() {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["manual-project-categories"] }),
      queryClient.invalidateQueries({ queryKey: ["manual-run-project-assignments"] }),
      queryClient.invalidateQueries({ queryKey: ["projects"] })
    ]);
  }

  async function assignManualProject(runID: string, categoryID: string) {
    if (categoryID) await assignRunManualProjectCategory(token, runID, categoryID);
    else await unassignRunManualProjectCategory(token, runID);
    await refreshManualProjectData();
  }

  async function createAndAssignManualProject(runID: string, name: string) {
    const normalized = name.trim().toLowerCase();
    const existing = manualCategoryList.find((category) => category.name.trim().toLowerCase() === normalized);
    const category = existing || (await createManualProjectCategory(token, { name }));
    await assignRunManualProjectCategory(token, runID, category.id);
    await refreshManualProjectData();
  }

  return (
    <div className={tab === "evidence" ? "app-shell evidence-app-shell" : "app-shell"}>
      <aside className="side-nav">
        <div className="brand">
          <img src="/aexp-icon.svg" alt="" />
          <div>
            <strong>aexp</strong>
            <span>{t("uiV2")}</span>
          </div>
        </div>
        <nav>
          <NavButton active={tab === "dashboard"} icon={<Activity />} label={t("dashboard")} onClick={() => setActiveTab("dashboard")} />
          <NavButton active={tab === "resources"} icon={<Server />} label={t("resources")} onClick={() => setActiveTab("resources")} />
          <NavButton active={tab === "projects"} icon={<Database />} label={t("projects")} onClick={() => setActiveTab("projects")} />
          <NavButton active={tab === "matrices"} icon={<Grid3X3 />} label={t("matrices")} onClick={() => setActiveTab("matrices")} />
          <NavButton active={tab === "evidence"} icon={<Network />} label={t("evidenceChains")} onClick={() => setActiveTab("evidence")} />
          <NavButton active={tab === "runs"} icon={<PlayCircle />} label={t("runs")} onClick={() => setActiveTab("runs")} />
          <NavButton active={tab === "favorites"} icon={<Star />} label={t("favorites")} onClick={() => setActiveTab("favorites")} />
          <NavButton active={tab === "execs"} icon={<Terminal />} label={t("execs")} onClick={() => setActiveTab("execs")} />
        </nav>
        <a className="legacy-link" href="/">
          <ExternalLink size={15} />
          {t("legacy")}
        </a>
      </aside>

      <main className={tab === "evidence" ? "workspace evidence-workspace" : "workspace"}>
        <header className="topbar">
          <div>
            <h1>{labelForTab(tab, t)}</h1>
            <p>{authError ? t("invalidToken") : t("tokenMissing")}</p>
          </div>
          <div className="topbar-actions">
            <button className="icon-button" title="Language" onClick={() => setLocale(locale === "zh" ? "en" : "zh")}>
              <Languages size={17} />
            </button>
            <button className="icon-button" title={t("refresh")} onClick={refreshAll}>
              <RefreshCcw size={17} />
            </button>
            <form
              className="token-form"
              onSubmit={(event) => {
                event.preventDefault();
                setToken(tokenDraft.trim());
              }}
            >
              <input value={tokenDraft} onChange={(event) => setTokenDraft(event.target.value)} type="password" placeholder={t("apiToken")} />
              <button type="submit">{t("set")}</button>
              {token ? (
                <button
                  type="button"
                  onClick={() => {
                    clearToken();
                    setTokenDraft("");
                  }}
                >
                  {t("clear")}
                </button>
              ) : null}
            </form>
          </div>
        </header>

        <AnimatePresence mode="wait">
          <motion.section key={tab} initial={{ opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -8 }} transition={{ duration: 0.16 }}>
            {tab === "dashboard" && (
              <Dashboard
                t={t}
                stats={stats.data}
                resources={resourceList}
                activeRuns={activeRuns}
                marks={markList}
                execs={visibleExecs.slice(0, 8)}
                resourceById={resourceById}
                onOpenRun={setDetailRunId}
              />
            )}
            {tab === "resources" && (
              <ResourcesTab
                t={t}
                token={token}
                resources={resourceList}
                onEdit={setResourceForm}
                onRefresh={async (id) => {
                  await refreshResource(token, id);
                  await invalidateOperationalData();
                }}
                onTest={async (id) => {
                  await testResource(token, id);
                  await invalidateOperationalData();
                }}
                onDelete={(resource) =>
                  askConfirm({
                    title: t("delete"),
                    message: `${t("delete")} ${resource.name}?`,
                    actionLabel: t("delete"),
                    run: async () => {
                      await deleteResource(token, resource.id);
                      await invalidateOperationalData();
                    }
                  })
                }
                onAdd={() => setResourceForm(defaultResource())}
                onFillLocal={async () => {
                  const local = await localResourceDefaults(token);
                  setResourceForm(local);
                }}
              />
            )}
            {tab === "projects" && <ProjectsTab t={t} projects={visibleProjects} query={projectQuery} setQuery={setProjectQuery} resourceById={resourceById} onOpenRun={setDetailRunId} />}
            {tab === "matrices" && <ExperimentMatrixPage token={token} t={t} onOpenRun={setDetailRunId} />}
            {tab === "evidence" && <EvidenceChainBoard token={token} t={t} onOpenRun={setDetailRunId} />}
            {tab === "runs" && (
              <RunsTab
                t={t}
                resources={resourceList}
                runs={visibleRuns}
                total={runs.data?.total || 0}
                page={runPage}
                setPage={setRunPage}
                status={runStatus}
                setStatus={(value) => {
                  setRunStatus(value);
                  setRunPage(0);
                }}
                resource={runResource}
                setResource={(value) => {
                  setRunResource(value);
                  setRunPage(0);
                }}
                project={runProject}
                setProject={(value) => {
                  setRunProject(value);
                  setRunPage(0);
                }}
                projectOptions={runProjectOptions}
                runProjectById={runProjectById}
                manualAssignments={manualAssignmentByRun}
                kind={runKind}
                setKind={(value) => {
                  setRunKind(value);
                  setRunPage(0);
                }}
                trash={runTrash}
                setTrash={(value) => {
                  setRunTrash(value);
                  setRunPage(0);
                }}
                query={runQuery}
                setQuery={(value) => {
                  setRunQuery(value);
                  setRunPage(0);
                }}
                bookmarks={bookmarkList}
                marks={runMarks}
                markPreviews={runMarkPreviews}
                resourceById={resourceById}
                onOpenRun={setDetailRunId}
                onOpenMark={setSelectedRunListMark}
                onCompare={() => setCompareOpen(true)}
                onArchive={(run) =>
                  askConfirm({
                    title: t("archive"),
                    message: `${t("archive")} ${runTitle(run)}?`,
                    actionLabel: t("archive"),
                    run: async () => {
                      await archiveRun(token, run.id);
                      clearSelectedRuns();
                      await invalidateOperationalData();
                    }
                  })
                }
                onRestore={async (run) => {
                  await restoreRun(token, run.id);
                  await invalidateOperationalData();
                }}
                onDelete={(run) =>
                  askConfirm({
                    title: t("delete"),
                    message: `${t("delete")} ${runTitle(run)}?`,
                    actionLabel: t("delete"),
                    run: async () => {
                      await deleteRunLogically(token, run.id);
                      await invalidateOperationalData();
                    }
                  })
                }
                onToggleBookmark={async (run, bookmarked, note) => {
                  if (bookmarked) await deleteBookmark(token, run.id);
                  else await saveBookmark(token, run.id, note);
                  await invalidateOperationalData();
                }}
                onEditBookmarkNote={(run, note) => setBookmarkEditor({ run, note })}
                onEditManualProject={setProjectEditorRun}
              />
            )}
            {tab === "favorites" && (
              <FavoritesTab t={t} runs={favoriteRuns} bookmarks={bookmarkList} query={favoriteQuery} setQuery={setFavoriteQuery} resourceById={resourceById} onOpenRun={setDetailRunId} />
            )}
            {tab === "execs" && (
              <ExecsTab
                t={t}
                resources={resourceList}
                rows={visibleExecs}
                total={execs.data?.total || 0}
                page={execPage}
                setPage={setExecPage}
                resource={execResource}
                setResource={setExecResource}
                actor={execActor}
                setActor={setExecActor}
                query={execQuery}
                setQuery={setExecQuery}
                resourceById={resourceById}
              />
            )}
          </motion.section>
        </AnimatePresence>
      </main>

      {detailRunId ? (
        <RunDetail
          t={t}
          token={token}
          runId={detailRunId}
          resourceById={resourceById}
          manualCategories={manualCategoryList}
          manualAssignment={manualAssignmentByRun.get(detailRunId)}
          onClose={() => {
            setDetailRunId(null);
            history.replaceState(null, "", pathForTab(tab));
          }}
          onCancel={(run) =>
            askConfirm({
              title: t("cancel"),
              message: `${t("cancel")} ${runTitle(run)}?`,
              actionLabel: t("cancel"),
              run: async () => {
                await cancelRun(token, run.id);
                await invalidateOperationalData();
              }
            })
          }
          onStatusCheck={async (run) => {
            await statusCheck(token, run.id);
            await invalidateOperationalData();
          }}
          onAssignManualProject={assignManualProject}
          onCreateAndAssignManualProject={createAndAssignManualProject}
        />
      ) : null}

      {resourceForm ? (
        <ResourceModal
          t={t}
          token={token}
          resource={resourceForm}
          onClose={() => setResourceForm(null)}
          onSave={async (resource) => {
            await saveResource(token, resource);
            setResourceForm(null);
            await invalidateOperationalData();
          }}
          onCreateLocal={async (name, rootDir) => {
            await createLocalResource(token, name, rootDir);
            setResourceForm(null);
            await invalidateOperationalData();
          }}
        />
      ) : null}

      {compareOpen ? <CompareModal t={t} token={token} runs={selectedRuns} onClose={() => setCompareOpen(false)} /> : null}
      {bookmarkEditor ? (
        <BookmarkNoteModal
          t={t}
          initialNote={bookmarkEditor.note}
          run={bookmarkEditor.run}
          onClose={() => setBookmarkEditor(null)}
          onSave={async (note) => {
            await saveBookmark(token, bookmarkEditor.run.id, note);
            setBookmarkEditor(null);
            await invalidateOperationalData();
          }}
        />
      ) : null}
      {projectEditorRun ? (
        <ManualProjectModal
          t={t}
          run={projectEditorRun}
          categories={manualCategoryList}
          projects={projectList}
          assignment={manualAssignmentByRun.get(projectEditorRun.id)}
          onClose={() => setProjectEditorRun(null)}
          onAssign={async (categoryID) => {
            await assignManualProject(projectEditorRun.id, categoryID);
            setProjectEditorRun(null);
          }}
          onCreateAndAssign={async (name) => {
            await createAndAssignManualProject(projectEditorRun.id, name);
            setProjectEditorRun(null);
          }}
        />
      ) : null}
      {confirm ? <ConfirmModal t={t} state={confirm} onClose={() => setConfirm(null)} /> : null}
      {selectedRunListMark ? <MarkDetailModal mark={selectedRunListMark} token={token} t={t} onClose={() => setSelectedRunListMark(null)} /> : null}
    </div>
  );
}

function NavButton({ active, icon, label, onClick }: { active: boolean; icon: ReactNode; label: string; onClick: () => void }) {
  return (
    <button className={active ? "nav-item active" : "nav-item"} onClick={onClick}>
      {icon}
      <span>{label}</span>
    </button>
  );
}

function Dashboard({
  t,
  stats,
  resources,
  activeRuns,
  marks,
  execs,
  resourceById,
  onOpenRun
}: {
  t: T;
  stats?: { total_resources: number; active_runs: number; total_runs: number };
  resources: Resource[];
  activeRuns: Run[];
  marks: RunMark[];
  execs: ExecEvent[];
  resourceById: Map<string, Resource>;
  onOpenRun: (id: string) => void;
}) {
  return (
    <div className="stack">
      <div className="stat-grid">
        <Stat label={t("resources")} value={stats?.total_resources ?? resources.length} icon={<Server />} />
        <Stat label={t("activeRuns")} value={stats?.active_runs ?? activeRuns.length} icon={<Activity />} />
        <Stat label={t("totalRuns")} value={stats?.total_runs ?? "-"} icon={<BarChart3 />} />
      </div>
      <Section title={t("resources")}>
        <div className="resource-grid">
          {resources.length ? resources.slice(0, 2).map((r) => <ResourceCard key={r.id} resource={r} />) : <Empty t={t} />}
          {resources.length > 2 ? (
            <div className="overview-more">
              <strong>+{resources.length - 2}</strong>
              <span>{t("resourceMore")}</span>
            </div>
          ) : null}
        </div>
      </Section>
      <Section title={t("activeRuns")}>
        <div className="run-card-grid dashboard-runs">{activeRuns.length ? activeRuns.map((run) => <RunCard key={run.id} run={run} resourceById={resourceById} onOpen={() => onOpenRun(run.id)} />) : <Empty t={t} />}</div>
      </Section>
      <div className="dashboard-lower">
        <Section title={t("agentFindings")} className="dashboard-findings-section">
          {marks.length ? (
            <div className="dashboard-findings">
              {marks.slice(0, 5).map((mark) => (
                <DashboardFinding key={mark.id} mark={mark} onOpenRun={() => onOpenRun(mark.run_id)} />
              ))}
            </div>
          ) : (
            <Empty t={t} />
          )}
        </Section>
        <Section title={t("recentExec")}>
          <div className="compact-list">{execs.slice(0, 3).map((event) => <ExecCompact key={event.id} event={event} resourceById={resourceById} />)}</div>
        </Section>
      </div>
    </div>
  );
}

function ResourcesTab({
  t,
  token: _token,
  resources,
  onEdit,
  onRefresh,
  onTest,
  onDelete,
  onAdd,
  onFillLocal
}: {
  t: T;
  token: string;
  resources: Resource[];
  onEdit: (resource: Resource) => void;
  onRefresh: (id: string) => Promise<void>;
  onTest: (id: string) => Promise<void>;
  onDelete: (resource: Resource) => void;
  onAdd: () => void;
  onFillLocal: () => Promise<void>;
}) {
  return (
    <div className="stack">
      <div className="toolbar">
        <button className="primary" onClick={onAdd}>
          <Server size={16} />
          {t("addResource")}
        </button>
        <button onClick={() => void onFillLocal()}>{t("fillLocal")}</button>
      </div>
      <div className="resource-grid manage-resource-grid">
        {resources.length ? (
          resources.map((resource) => (
            <ResourceCard
              key={resource.id}
              resource={resource}
              actions={
                <>
                  <button className="action-button" title={t("refresh")} onClick={() => void onRefresh(resource.id)}>
                    <RefreshCcw size={15} />
                    {t("refresh")}
                  </button>
                  <button className="action-button primary-action" title={t("test")} onClick={() => void onTest(resource.id)}>
                    <Check size={15} />
                    {t("test")}
                  </button>
                  <button className="action-button" title={t("editResource")} onClick={() => onEdit(resource)}>
                    <Settings size={15} />
                    {t("editResource")}
                  </button>
                  <button className="action-button danger-inline" title={t("delete")} onClick={() => onDelete(resource)}>
                    <Trash2 size={15} />
                    {t("delete")}
                  </button>
                </>
              }
            />
          ))
        ) : (
          <Empty t={t} />
        )}
      </div>
    </div>
  );
}

function RunsTab(props: {
  t: T;
  resources: Resource[];
  runs: Run[];
  total: number;
  page: number;
  setPage: (page: number) => void;
  status: string;
  setStatus: (status: string) => void;
  resource: string;
  setResource: (resource: string) => void;
  project: string;
  setProject: (project: string) => void;
  projectOptions: [string, string][];
  runProjectById: Map<string, RunProjectMeta>;
  manualAssignments: Map<string, RunProjectAssignment>;
  kind: string;
  setKind: (kind: string) => void;
  trash: boolean;
  setTrash: (trash: boolean) => void;
  query: string;
  setQuery: (query: string) => void;
  bookmarks: RunBookmark[];
  marks: Map<string, number>;
  markPreviews: Map<string, RunMark[]>;
  resourceById: Map<string, Resource>;
  onOpenRun: (id: string) => void;
  onOpenMark: (mark: RunMark) => void;
  onCompare: () => void;
  onArchive: (run: Run) => void;
  onRestore: (run: Run) => Promise<void>;
  onDelete: (run: Run) => void;
  onToggleBookmark: (run: Run, bookmarked: boolean, note?: string) => Promise<void>;
  onEditBookmarkNote: (run: Run, note: string) => void;
  onEditManualProject: (run: Run) => void;
}) {
  const selectedRunIds = useAppStore((s) => s.selectedRunIds);
  const toggleSelectedRun = useAppStore((s) => s.toggleSelectedRun);
  const bookmarkByRun = useMemo(() => new Map(props.bookmarks.map((bookmark) => [bookmark.run_id, bookmark])), [props.bookmarks]);
  const selectedCount = props.runs.filter((run) => selectedRunIds.has(run.id)).length;
  const renderRun = (run: Run) => {
    const bookmark = bookmarkByRun.get(run.id);
    return (
      <RunListCard
        key={run.id}
        run={run}
        resourceById={props.resourceById}
        projectMeta={props.runProjectById.get(run.id)}
        manualAssignment={props.manualAssignments.get(run.id)}
        markCount={props.marks.get(run.id) || 0}
        markPreviews={props.markPreviews.get(run.id) || []}
        bookmark={bookmark}
        selected={selectedRunIds.has(run.id)}
        trash={props.trash}
        onOpen={() => props.onOpenRun(run.id)}
        onOpenMark={props.onOpenMark}
        onSelect={(checked) => toggleSelectedRun(run, checked)}
        onToggleBookmark={() => void props.onToggleBookmark(run, !!bookmark)}
        onEditBookmarkNote={() => props.onEditBookmarkNote(run, bookmark?.note?.trim() || "")}
        onEditManualProject={() => props.onEditManualProject(run)}
        onArchive={() => props.onArchive(run)}
        onRestore={() => void props.onRestore(run)}
        onDelete={() => props.onDelete(run)}
        t={props.t}
      />
    );
  };
  return (
    <div className="stack">
      <div className="toolbar dense">
        <Segmented
          value={props.trash ? "trash" : "main"}
          options={[
            { value: "main", label: props.t("mainRuns") },
            { value: "trash", label: props.t("trash") }
          ]}
          onChange={(value) => props.setTrash(value === "trash")}
        />
        <Select value={props.status} onChange={props.setStatus} options={[["", props.t("allStatuses")], ["running", "running"], ["succeeded", "succeeded"], ["failed", "failed"], ["cancelled", "cancelled"]]} />
        <Select value={props.resource} onChange={props.setResource} options={[["", props.t("allResources")], ...props.resources.map((r) => [r.id, r.name] as [string, string])]} />
        <Select value={props.project} onChange={props.setProject} options={props.projectOptions} />
        <Select value={props.kind} onChange={props.setKind} options={[["experiments", props.t("experiments")], ["tools", props.t("toolTasks")], ["all", props.t("allKinds")]]} />
        <input value={props.query} onChange={(event) => props.setQuery(event.target.value)} placeholder={props.t("search")} />
        <button disabled={!selectedCount} onClick={props.onCompare}>
          <BarChart3 size={16} />
          {props.t("compare")} ({selectedCount})
        </button>
      </div>
      {props.runs.length ? (
        <div className="run-list">{props.runs.map(renderRun)}</div>
      ) : (
        <div className="run-list">
          <Empty t={props.t} />
        </div>
      )}
      <Pager t={props.t} total={props.total} page={props.page} setPage={props.setPage} />
    </div>
  );
}

function ProjectsTab({
  t,
  projects,
  query,
  setQuery,
  resourceById,
  onOpenRun
}: {
  t: T;
  projects: ProjectView[];
  query: string;
  setQuery: (value: string) => void;
  resourceById: Map<string, Resource>;
  onOpenRun: (id: string) => void;
}) {
  return (
    <div className="stack">
      <div className="toolbar">
        <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("search")} />
      </div>
      <div className="project-list">
        {projects.length ? (
          projects.map((project) => {
            const cards = project.cards || [];
            return (
              <section className="project-row" key={project.project_id}>
                <ProjectSummary project={project} t={t} />
                <div className="project-evidence">
                  <div className="project-evidence-head">
                    <span className="panel-kicker">{t("evidenceRecords")}</span>
                    <span className="muted">{cards.length}</span>
                  </div>
                  {cards.map((card) => <ProjectEvidenceCard key={card.id} card={card} resourceById={resourceById} onOpenRun={onOpenRun} t={t} />)}
                  {!cards.length ? <div className="project-empty muted">{t("noPromotedCards")}</div> : null}
                </div>
              </section>
            );
          })
        ) : (
          <Empty t={t} />
        )}
      </div>
    </div>
  );
}

function ProjectSummary({ project, t }: { project: ProjectView; t: T }) {
  const cards = project.cards || [];
  const latest = cards.find((card) => card.verdict || card.question || card.key_metrics);
  const projectTitle = projectDisplayName(project, t);
  const counts = [
    { label: t("projectCards"), value: project.total_cards ?? cards.length, tone: "neutral" as const },
    { label: t("important"), value: project.important_runs || 0, tone: "accent" as const },
    { label: t("formal"), value: project.formal_runs || 0, tone: "good" as const },
    { label: t("running"), value: project.running_runs || 0, tone: "warn" as const }
  ];
  return (
    <div className="project-summary">
      <div className="project-head">
        <div>
          <h2>{projectTitle}</h2>
          <p className="muted mono">{project.project_id}</p>
        </div>
      </div>
      <div className="project-signal-grid">
        {counts.map((item) => (
          <div className={`project-signal ${item.tone}`} key={item.label}>
            <span>{item.label}</span>
            <strong>{item.value}</strong>
          </div>
        ))}
      </div>
      <div className="project-latest">
        <span className="panel-kicker">{t("latestSignal")}</span>
        <strong>{latest?.verdict || latest?.question || t("noEvidenceCard")}</strong>
        <p>{latest?.key_metrics || latest?.next_action || latest?.run?.command || t("projectCardPrompt")}</p>
      </div>
    </div>
  );
}

function ProjectEvidenceCard({ card, resourceById, onOpenRun, t }: { card: ProjectRunCard; resourceById: Map<string, Resource>; onOpenRun: (id: string) => void; t: T }) {
  const firstMark = (card.marks || []).find((mark) => mark.title || mark.reason || mark.evidence);
  const firstMarkText = firstMark ? [firstMark.title, firstMark.reason || firstMark.evidence].filter(Boolean).join(": ") : "";
  const title = card.verdict || card.supports_claim || card.weakens_claim || card.question || card.key_metrics || card.run?.name || card.run_id;
  const body =
    [card.supports_claim, card.weakens_claim, card.question, firstMarkText, card.next_action, card.proposal_reason, card.run?.command]
      .find((item) => item && item !== title && item !== card.key_metrics) || "";
  const metrics = card.key_metrics && card.key_metrics !== title ? card.key_metrics : "";
  const run = card.run;
  const status = run?.status || "-";
  const meta = [
    run?.kind || "formal",
    run?.resource_id ? resourceById.get(run.resource_id)?.name || run.resource_id : "",
    run?.gpu_index != null ? `GPU ${runGPU(run.gpu_index)}` : "",
    run?.project_env || run?.conda_env || "",
    fmtShortTime(run?.created_at)
  ].filter((item) => item && item !== "-");
  const evidenceTags = [
    card.important ? t("important") : "",
    card.marks?.length ? `${card.marks.length} ${t("marks")}` : "",
    card.artifact_paths ? t("artifacts") : "",
    card.related_runs ? t("relatedRuns") : ""
  ].filter(Boolean);
  const markSnippets = (card.marks || [])
    .map((mark) => [mark.title, mark.reason || mark.evidence].filter(Boolean).join(": "))
    .filter(Boolean)
    .slice(0, 2);
  const cardClassName = card.should_promote ? "project-card prominent" : "project-card";
  return (
    <button className={cardClassName} onClick={() => card.run_id && onOpenRun(card.run_id)} type="button">
      <div className="project-card-top">
        <div className="project-card-main">
          <span className="project-card-run">{card.run?.name || card.run_id}</span>
          <strong>{title}</strong>
        </div>
        <div className="project-card-level">
          <Pill tone={card.evidence_level === "A" || card.evidence_level === "B" ? "good" : "neutral"}>L{card.evidence_level || "C"}</Pill>
          <Pill tone={statusTone(status)}>{status}</Pill>
        </div>
      </div>
      {body ? <span className="project-card-body">{body}</span> : null}
      {evidenceTags.length ? <div className="project-card-tags">{evidenceTags.map((item) => <span className="project-evidence-chip" key={item}>{item}</span>)}</div> : null}
      {metrics ? <p className="project-card-metrics">{metrics}</p> : null}
      {markSnippets.length ? (
        <div className="project-card-marks">
          {markSnippets.map((snippet) => <span key={snippet}>{snippet}</span>)}
        </div>
      ) : null}
      <div className="project-card-meta">
        <span className="mono">{card.run_id}</span>
        {meta.map((item) => <span key={item}>{item}</span>)}
      </div>
    </button>
  );
}

function FavoritesTab({ t, runs, bookmarks: _bookmarks, query, setQuery, resourceById, onOpenRun }: { t: T; runs: Run[]; bookmarks: RunBookmark[]; query: string; setQuery: (value: string) => void; resourceById: Map<string, Resource>; onOpenRun: (id: string) => void }) {
  return (
    <div className="stack">
      <div className="toolbar">
        <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("search")} />
      </div>
      <div className="run-card-grid">{runs.length ? runs.map((run) => <RunCard key={run.id} run={run} resourceById={resourceById} onOpen={() => onOpenRun(run.id)} />) : <Empty t={t} />}</div>
    </div>
  );
}

function ExecsTab(props: {
  t: T;
  resources: Resource[];
  rows: ExecEvent[];
  total: number;
  page: number;
  setPage: (page: number) => void;
  resource: string;
  setResource: (resource: string) => void;
  actor: string;
  setActor: (actor: string) => void;
  query: string;
  setQuery: (query: string) => void;
  resourceById: Map<string, Resource>;
}) {
  return (
    <div className="stack">
      <div className="toolbar dense">
        <Select value={props.resource} onChange={props.setResource} options={[["", props.t("allResources")], ...props.resources.map((r) => [r.id, r.name] as [string, string])]} />
        <Select value={props.actor} onChange={props.setActor} options={[["", props.t("allActors")], ["cli", "cli"], ["api", "api"], ["agent", "agent"]]} />
        <input value={props.query} onChange={(event) => props.setQuery(event.target.value)} placeholder={props.t("search")} />
      </div>
      <div className="exec-list">
        {props.rows.length ? props.rows.map((event) => <ExecListItem key={event.id} event={event} resourceById={props.resourceById} />) : <Empty t={props.t} />}
      </div>
      <Pager t={props.t} total={props.total} page={props.page} setPage={props.setPage} />
    </div>
  );
}

function RunDetail({
  t,
  token,
  runId,
  resourceById,
  manualCategories,
  manualAssignment,
  onClose,
  onCancel,
  onStatusCheck,
  onAssignManualProject,
  onCreateAndAssignManualProject
}: {
  t: T;
  token: string;
  runId: string;
  resourceById: Map<string, Resource>;
  manualCategories: ManualProjectCategory[];
  manualAssignment?: RunProjectAssignment;
  onClose: () => void;
  onCancel: (run: Run) => void;
  onStatusCheck: (run: Run) => Promise<void>;
  onAssignManualProject: (runID: string, categoryID: string) => Promise<void>;
  onCreateAndAssignManualProject: (runID: string, name: string) => Promise<void>;
}) {
  const run = useQuery({ queryKey: ["run", token, runId], queryFn: () => getRun(token, runId), refetchInterval: 5000 });
  const marks = useQuery({ queryKey: ["run-marks", token, runId], queryFn: () => getRunMarks(token, runId) });
  const artifacts = useQuery({ queryKey: ["artifacts", token, runId], queryFn: () => getArtifacts(token, runId) });
  const terminal = useLiveLog(token, runId, { source: "terminal" });
  const stdout = useLiveLog(token, runId, { source: "stdout" });
  const stderr = useLiveLog(token, runId, { source: "stderr" });
  const eventsPath = run.data ? uiEventsPath(run.data) : "";
  const eventLog = useLiveLog(token, runId, eventsPath ? { path: eventsPath } : null);
  const parsedEvents = useParsedEvents(eventLog.lines.map((line) => line.content));
  const [selectedMark, setSelectedMark] = useState<RunMark | null>(null);

  useEffect(() => {
    history.replaceState(null, "", `/ui-v2/runs/${encodeURIComponent(runId)}`);
  }, [runId]);

  return (
    <Modal title={run.data ? runTitle(run.data) : runId} onClose={onClose} wide>
      {run.data ? (
        <div className="detail-shell">
          <section className="run-overview">
            <div className="run-overview-main">
              <div className="detail-summary-head">
                <span className="panel-kicker">{t("runSummary")}</span>
                <Pill tone={statusTone(run.data.status)}>{run.data.status}</Pill>
              </div>
              <strong>{runTitle(run.data)}</strong>
              <span className="mono muted">{run.data.id}</span>
            </div>
            <div className="detail-facts">
              <span>
                <em>{t("resource")}</em>
                <strong>{resourceById.get(run.data.resource_id)?.name || run.data.resource_id}</strong>
              </span>
              <span>
                <em>{t("kind")}</em>
                <strong>{run.data.kind || "formal"}</strong>
              </span>
              <span>
                <em>{t("gpu")}</em>
                <strong>{runGPU(run.data.gpu_index)}</strong>
              </span>
              <span>
                <em>{t("time")}</em>
                <strong>{fmtTime(run.data.created_at)}</strong>
              </span>
              <span>
                <em>{t("condaEnv")}</em>
                <strong>{run.data.resolved_env || run.data.conda_env || "-"}</strong>
              </span>
            </div>
            <div className="detail-side-actions">
              <button onClick={() => void onStatusCheck(run.data!)}>
                <RefreshCcw size={16} />
                {t("refreshStatus")}
              </button>
              {isActiveRun(run.data) ? (
                <button className="danger" onClick={() => onCancel(run.data!)}>
                  {t("cancel")}
                </button>
              ) : null}
            </div>
          </section>
          <div className="detail-context-grid">
            <section className="detail-summary-panel">
              <span className="panel-kicker">{t("runtime")}</span>
              <Info label="cwd" value={run.data.resolved_cwd || run.data.cwd || "-"} />
              <Info label="tmux" value={run.data.tmux_session || "-"} />
              <Info label="run dir" value={run.data.remote_run_dir || "-"} />
            </section>
            <Section title={t("artifacts")} className="artifact-panel">
              <ArtifactList artifacts={artifacts.data || []} t={t} />
            </Section>
            <Section title={t("manualProject")} className="manual-project-panel">
              <ProjectAssignmentControl
                t={t}
                categories={manualCategories}
                assignment={manualAssignment}
                onAssign={(categoryID) => onAssignManualProject(runId, categoryID)}
                onCreateAndAssign={(name) => onCreateAndAssignManualProject(runId, name)}
              />
            </Section>
          </div>
          <div className="detail-grid">
            <div className="detail-main">
              <section className="command-card">
                <div className="section-head command-head">
                  <h2>{t("command")}</h2>
                  <span className="muted mono">{run.data.resolved_cwd || run.data.cwd || "-"}</span>
                </div>
                <pre className="command-box">{run.data.command}</pre>
              </section>
              {eventsPath ? <EventDashboard t={t} parsed={parsedEvents} path={eventsPath} snapshotError={eventLog.error} run={run.data} /> : null}
              <Section title={t("agentFindings")} className="findings-section">
                {marks.data?.length ? <div className="finding-list">{marks.data.map((mark) => <Finding key={mark.id} mark={mark} onOpen={() => setSelectedMark(mark)} />)}</div> : <Empty t={t} />}
              </Section>
              <LogPanel title="terminal" state={terminal} />
              <LogPanel title="stdout" state={stdout} />
              <LogPanel title="stderr" state={stderr} hiddenWhenEmpty />
              {eventsPath ? <LogPanel title={t("events")} state={eventLog} /> : null}
            </div>
          </div>
        </div>
      ) : (
        <Empty t={t} />
      )}
      {selectedMark ? <MarkDetailModal mark={selectedMark} token={token} t={t} onClose={() => setSelectedMark(null)} /> : null}
    </Modal>
  );
}

function ArtifactList({ artifacts, t }: { artifacts: Artifact[]; t: T }) {
  if (!artifacts.length) return <Empty t={t} />;
  return (
    <div className="artifact-list">
      {artifacts.map((artifact) => (
        <div className="artifact-row" key={artifact.id || artifact.path}>
          <div>
            <strong>{artifact.path}</strong>
            <span>{[artifact.type || "file", formatBytes(artifact.size), artifact.modified_at ? fmtShortTime(artifact.modified_at) : ""].filter(Boolean).join(" · ")}</span>
          </div>
        </div>
      ))}
    </div>
  );
}

function EventDashboard({ t, parsed, path, snapshotError, run }: { t: T; parsed: ParsedEvents; path: string; snapshotError?: string | null; run: Run }) {
  const [expandedMetric, setExpandedMetric] = useState<string | null>(null);
  const latest = parsed.latestMetrics.slice(0, 16);
  const progress = summarizeProgress(parsed.progress).slice(0, 8);
  const params = parsed.params.slice(0, 24);
  const notes = parsed.notes.slice(-3);
  const metricFamilies = summarizeMetricFamilies(parsed.metrics).slice(0, 12);
  const summary = [
    { label: t("events"), value: parsed.events.length },
    { label: t("progress"), value: parsed.progress.length },
    { label: t("params"), value: parsed.params.length },
    { label: t("metrics"), value: parsed.metrics.length },
    { label: parsed.errors.length ? t("errors") : t("notes"), value: parsed.errors.length || parsed.notes.length }
  ];

  const toggleMetric = (key: string) => {
    setExpandedMetric((current) => {
      if (current === key) return null;
      return key;
    });
  };

  return (
    <section className="event-dashboard">
      <div className="section-head event-head">
        <div>
          <h2>{t("events")}</h2>
          <span className="muted mono event-path">{path} · {parsed.events.length} lines</span>
        </div>
        <div className="event-summary-strip">
          {summary.map((item) => (
            <div className="event-summary-item" key={item.label}>
              <span>{item.label}</span>
              <strong>{item.value}</strong>
            </div>
          ))}
        </div>
      </div>
      {snapshotError ? (
        <div className="event-alert">
          <strong>{t("logSnapshotFailed")}</strong>
          <span>{snapshotError}</span>
        </div>
      ) : null}
      <div className="event-layout">
        <EventFoldout className="progress-panel" title={t("progress")} count={progress.length} defaultOpen>
          {progress.length ? progress.map((row) => <ProgressStatusRow key={row.key} row={row} run={run} t={t} />) : <span className="muted">{t("noProgressEvents")}</span>}
        </EventFoldout>
        <EventFoldout className="param-panel" title={t("params")} count={params.length} defaultOpen>
          <div className="param-list">
            {params.length ? params.map((param) => (
              <div className={paramCardClass(param)} style={paramCardStyle(param)} key={`${param.series || t("defaultSeries")}-${param.name}`}>
                <div>
                  <span>{param.name}</span>
                  {param.series ? <small>{param.series}</small> : null}
                </div>
                <strong>{param.value || "-"}</strong>
              </div>
            )) : <span className="muted">{t("noParamsYet")}</span>}
          </div>
        </EventFoldout>
        <EventFoldout className="metric-panel" title={t("latestMetricValues")} count={latest.length}>
          <div className="metric-list">
            {latest.length ? latest.map((metric, index) => (
              <div className="metric-row" key={`${metric.series || t("defaultSeries")}-${metric.name}-${index}`}>
                <div className="metric-row-main">
                  <span className="metric-row-name">{metric.name}</span>
                  <span className="metric-row-context">{latestMetricContext(metric, t)}</span>
                </div>
                <strong>{formatMetricValue(metric)}</strong>
              </div>
            )) : <span className="muted">{t("noMetricsYet")}</span>}
          </div>
        </EventFoldout>
        {(parsed.errors.length || notes.length) ? (
          <EventFoldout className="event-notes" title={parsed.errors.length ? t("errors") : t("notes")} count={parsed.errors.length || notes.length} defaultOpen={!!parsed.errors.length}>
            {parsed.errors.slice(0, 3).map((error, index) => <p key={`error-${index}`}>{error}</p>)}
            {notes.map((note, index) => <p key={`note-${index}`}>{text(note.text || note.message || note.name || "")}</p>)}
          </EventFoldout>
        ) : null}
      </div>
      <EventFoldout className="metric-family-foldout" title={t("metricTrends")} count={metricFamilies.length} defaultOpen>
        <LayoutGroup id={`metric-cards-${run.id}`}>
          <section className={`metric-card-grid ${expandedMetric ? "has-expanded" : ""}`} aria-label={t("metrics")}>
            {metricFamilies.length && !expandedMetric ? metricFamilies.map((family) => {
              const key = metricFamilyKey(family);
              return (
                <MetricFamilyCard
                  key={key}
                  layoutKey={key}
                  family={family}
                  t={t}
                  compressed={false}
                  expanded={false}
                  onToggle={() => toggleMetric(key)}
                />
              );
            }) : null}
            {expandedMetric ? metricFamilies.filter((family) => metricFamilyKey(family) === expandedMetric).map((family) => {
              const key = metricFamilyKey(family);
              return (
                <MetricFamilyCard
                  key={key}
                  layoutKey={key}
                  family={family}
                  t={t}
                  compressed={false}
                  expanded
                  onToggle={() => toggleMetric(key)}
                />
              );
            }) : null}
            {expandedMetric ? (
              <motion.div className="metric-card-collapsed-grid" layout="position" transition={metricLayoutTransition}>
                {metricFamilies.filter((family) => metricFamilyKey(family) !== expandedMetric).map((family) => {
                  const key = metricFamilyKey(family);
                  return (
                    <MetricFamilyCard
                      key={key}
                      layoutKey={key}
                      family={family}
                      t={t}
                      compressed
                      expanded={false}
                      onToggle={() => toggleMetric(key)}
                    />
                  );
                })}
              </motion.div>
            ) : null}
            {!metricFamilies.length ? <span className="muted">{t("noMetricFamiliesYet")}</span> : null}
          </section>
        </LayoutGroup>
      </EventFoldout>
    </section>
  );
}

function EventFoldout({ title, count, defaultOpen = false, className, children }: { title: string; count: number; defaultOpen?: boolean; className?: string; children: ReactNode }) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div className={`event-panel ${className || ""} ${open ? "open" : "collapsed"}`}>
      <button className="event-panel-toggle" type="button" onClick={() => setOpen((value) => !value)} aria-expanded={open}>
        <span className="panel-kicker">{title}</span>
        <span className="event-panel-count">{count}</span>
        <ChevronRight className="event-panel-chevron" size={15} />
      </button>
      {open ? <div className="event-panel-body">{children}</div> : null}
    </div>
  );
}

function ProgressStatusRow({ row, run, t }: { row: ProgressSummary; run: Run; t: T }) {
  const latest = row.latest;
  const percent = progressPercent(latest);
  const state = progressState(row, run, t);
  return (
    <div className={`progress-row progress-row-${state.tone}`}>
      <div className="progress-row-title">
        <div>
          <strong>{row.label || row.name}</strong>
          {row.label && row.label !== row.name ? <span>{row.name}</span> : null}
          {row.series ? <span>{row.series}</span> : null}
        </div>
        <span className="progress-state">{state.label}</span>
      </div>
      <div className="progress-row-meter">
        {percent != null ? <ProgressMeter value={percent} /> : <span className="progress-value mono">{formatMetric(latest.current)}</span>}
      </div>
      <div className="progress-row-foot">
        <span>{latest.total ? `${formatMetric(latest.current)}/${formatMetric(latest.total)}` : formatMetric(latest.current)}</span>
        <span>{row.count} {t("updates")}</span>
      </div>
    </div>
  );
}

function ProgressMeter({ value }: { value: number }) {
  const pct = Math.max(0, Math.min(100, value || 0));
  return (
    <div className="progress-meter">
      <div>
        <i style={{ width: `${pct}%` }} />
      </div>
      <strong>{formatMetric(pct)}%</strong>
    </div>
  );
}

function progressPercent(point: ProgressPoint): number | undefined {
  if (point.percent != null) return Math.max(0, Math.min(100, point.percent));
  if (point.total && point.total > 0) return Math.max(0, Math.min(100, (point.current / point.total) * 100));
  return undefined;
}

function progressState(row: ProgressSummary, run: Run, t: T): { label: string; tone: "active" | "done" | "incomplete" | "stopped" } {
  if (row.done) return { label: t("complete"), tone: "done" };
  if (isActiveRun(run)) return { label: t("active"), tone: "active" };
  const status = (run.status || "").toLowerCase();
  if (status === "succeeded" || status === "success" || status === "finished" || status === "complete" || status === "completed") {
    return { label: t("incomplete"), tone: "incomplete" };
  }
  if (status === "failed" || status === "error" || status === "cancelled" || status === "canceled") {
    return { label: t("stopped"), tone: "stopped" };
  }
  return { label: t("finished"), tone: "incomplete" };
}

function latestMetricContext(metric: MetricPoint, t: T) {
  return [metric.series || t("defaultSeries"), metric.unit].filter(Boolean).join(" · ");
}

function metricTitle(name: string) {
  const parts = name.split("/").map((part) => part.trim()).filter(Boolean);
  if (parts.length <= 1) return name;
  const leaf = parts[parts.length - 1];
  const context = parts.slice(0, -1).join(" · ");
  return `${context} · ${leaf}`;
}

function shortSeriesName(name: string) {
  const cleaned = name.replace(/_/g, " ");
  if (cleaned.length <= 28) return cleaned;
  return `${cleaned.slice(0, 25)}...`;
}

function paramCardClass(param: ParamPoint) {
  const value = param.value || "";
  const name = param.name || "";
  const longValue = value.length > 34 || value.includes("/") || value.includes("\\");
  const longName = name.length > 24;
  return `param-row${longValue || longName ? " param-row-wide" : ""}`;
}

function paramCardStyle(param: ParamPoint): CSSProperties {
  const value = param.value || "";
  const name = param.name || "";
  const visualLength = Math.max(value.length, name.length * 0.8);
  const rows = Math.max(4, Math.min(10, 4 + Math.ceil(visualLength / 56)));
  return { "--param-rows": rows } as CSSProperties;
}

type MetricFamily = ReturnType<typeof summarizeMetricFamilies>[number];

const metricLayoutTransition = {
  layout: { type: "spring", stiffness: 430, damping: 38, mass: 0.85 },
  opacity: { duration: 0.12 }
} as const;

function metricFamilyKey(family: MetricFamily) {
  return `${family.name}\u0000${family.scaleKey}`;
}

function MetricFamilyCard({ family, t, compressed, expanded, layoutKey, onToggle }: { family: MetricFamily; t: T; compressed: boolean; expanded: boolean; layoutKey: string; onToggle: () => void }) {
  const curveCount = family.curveTrends.length;
  const referenceCount = family.referenceTrends.length;
  const primary = curveCount || referenceCount
    ? [curveCount ? `${curveCount} ${t("curves").toLowerCase()}` : "", referenceCount ? `${referenceCount} ${t("references").toLowerCase()}` : ""].filter(Boolean).join(" · ")
    : family.latest ? formatMetricValue(family.latest) : "-";
  const seriesPreview = expanded ? family.trends : family.trends.slice(0, 2);
  const axisLabel = metricAxisLabel(family, t);
  const previewTrend = family.curveTrends[0]?.trend || [];
  const hasCurve = family.axisKind !== "sample" && family.curveTrends.length > 0;
  const referenceRows = family.referenceTrends.length ? family.referenceTrends : family.trends;
  return (
    <motion.article
      className={`metric-family-card ${expanded ? "expanded" : ""} ${compressed ? "compressed" : ""}`}
      layout
      layoutId={`metric-card-${encodeURIComponent(layoutKey)}`}
      layoutDependency={expanded}
      transition={metricLayoutTransition}
    >
      <motion.button layout className="metric-card-button" type="button" onClick={onToggle} aria-expanded={expanded} transition={metricLayoutTransition}>
        <motion.div layout="position" className="metric-card-head" transition={metricLayoutTransition}>
          <span>{metricTitle(family.name)}</span>
          <strong>{primary}</strong>
        </motion.div>
        {!compressed && !expanded ? <motion.div layout="position" className="metric-card-series" transition={metricLayoutTransition}>
          {seriesPreview.map((row, index) => (
            <div key={`${row.key || t("defaultSeries")}-${index}`} title={row.fullLabel}>
              <span>{shortSeriesName(row.label || t("defaultSeries"))}</span>
              <strong>{formatMetricValue(row.latest)}</strong>
            </div>
          ))}
        </motion.div> : null}
        {!expanded && !compressed && hasCurve && previewTrend.length ? (
          <motion.div layout="position" className="metric-card-sparkline-wrap" aria-hidden="true" transition={metricLayoutTransition}>
            <span>{formatMetric(family.max)}</span>
            <svg className="metric-card-sparkline" viewBox="0 0 120 42" preserveAspectRatio="none">
              <line x1="0" y1="5" x2="120" y2="5" />
              <line x1="0" y1="21" x2="120" y2="21" />
              <line x1="0" y1="37" x2="120" y2="37" />
              <polyline points={metricSparklinePoints(previewTrend, 42)} />
            </svg>
            <span>{formatMetric(family.min)}</span>
          </motion.div>
        ) : null}
        {!expanded && !compressed && !hasCurve ? <motion.div layout="position" className="metric-sample-summary" aria-hidden="true" transition={metricLayoutTransition}>{family.count} {t("points")}</motion.div> : null}
        <motion.span layout="position" className="metric-card-foot" transition={metricLayoutTransition}>{axisLabel} · {family.count} {t("points")}</motion.span>
      </motion.button>
      <AnimatePresence initial={false}>
        {expanded ? (
          <motion.div
            className="metric-card-detail"
            layout
            key="metric-detail"
            initial={{ opacity: 0, height: 0, y: -8 }}
            animate={{ opacity: 1, height: "auto", y: 0 }}
            exit={{ opacity: 0, height: 0, y: -8 }}
            transition={{ duration: 0.22, ease: [0.22, 1, 0.36, 1] }}
          >
            {hasCurve ? <div className="metric-card-chart-head">
              <span>{t("range")}: {axisLabel}</span>
              <strong>{t("latest")}: {family.latest ? formatMetricValue(family.latest) : "-"}</strong>
            </div> : null}
            {hasCurve ? <MetricChart points={family.points} series={family.curveTrends} axisKind={family.axisKind} /> : null}
            {!hasCurve ? (
              <div className="metric-reference-board">
                <span>{t("references")}</span>
                <div className="metric-reference-grid">
                  {referenceRows.map((row, index) => (
                    <div className="metric-reference-value" key={`${row.key || t("defaultSeries")}-${index}`} title={row.fullLabel}>
                      <span><i style={{ backgroundColor: metricSeriesColor(index) }} />{row.label || t("defaultSeries")}</span>
                      <strong>{formatMetricValue(row.latest)}</strong>
                    </div>
                  ))}
                </div>
              </div>
            ) : null}
            {hasCurve ? (
              <>
                <div>
                  <span>{t("low")}</span>
                  <strong>{formatMetric(family.min)}</strong>
                </div>
                <div>
                  <span>{t("high")}</span>
                  <strong>{formatMetric(family.max)}</strong>
                </div>
                <div>
                  <span>{t("delta")}</span>
                  <strong>{formatMetricDelta(family.delta, family.deltaPct)}</strong>
                </div>
              </>
            ) : null}
            {hasCurve ? <div className="metric-card-detail-series">
              <span>{t("curves")}</span>
              {family.curveTrends.map((row, index) => (
                <p key={`${row.key || t("defaultSeries")}-${index}`} title={row.fullLabel}>
                  <span><i style={{ backgroundColor: metricSeriesColor(index) }} />{row.label || t("defaultSeries")}</span>
                  <strong>{formatMetricValue(row.latest)}</strong>
                </p>
              ))}
            </div> : null}
            {hasCurve && family.referenceTrends.length ? (
              <div className="metric-card-detail-series metric-card-reference-series">
                <span>{t("references")}</span>
                {family.referenceTrends.map((row, index) => (
                  <p key={`${row.key || t("defaultSeries")}-${index}`} title={row.fullLabel}>
                    <span><i style={{ backgroundColor: metricSeriesColor(index + family.curveTrends.length) }} />{row.label || t("defaultSeries")}</span>
                    <strong>{formatMetricValue(row.latest)}</strong>
                  </p>
                ))}
              </div>
            ) : null}
          </motion.div>
        ) : null}
      </AnimatePresence>
    </motion.article>
  );
}

function CompareModal({ t, token, runs, onClose }: { t: T; token: string; runs: Run[]; onClose: () => void }) {
  const queries = useQueries({
    queries: runs.map((run) => ({
      queryKey: ["compare-events", token, run.id, uiEventsPath(run)],
      queryFn: async () => {
        const logs = await getLogs(token, run.id, { path: uiEventsPath(run), limit: 5000, tail: true });
        const snapshotError = logSnapshotError(logs);
        if (snapshotError) throw new Error(snapshotError);
        return { run, parsed: parseEventLines(logs.lines.map((line) => line.content)) };
      },
      enabled: !!uiEventsPath(run)
    }))
  });
  const points = queries.flatMap((query) => (query.data?.parsed.metrics || []).map((point) => ({ ...point, series: query.data?.run.name || query.data?.run.id })));
  return (
    <Modal title={t("compare")} onClose={onClose} wide>
      <MetricChart points={points} />
      <div className="metric-strip">
        {runs.map((run) => (
          <div className="metric-tile" key={run.id}>
            <span>{runTitle(run)}</span>
            <strong>{run.status}</strong>
          </div>
        ))}
      </div>
    </Modal>
  );
}

function MetricChart({ points, series, compact = false, axisKind = "sample" }: { points: MetricPoint[]; series?: MetricSeriesSummary[]; compact?: boolean; axisKind?: "epoch" | "step" | "sample" }) {
  const ref = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!ref.current) return;
    const chart = echarts.init(ref.current);
    const chartSeries = series?.length
      ? series
      : summarizeChartSeries(points, axisKind);
    chart.setOption({
      animationDuration: 180,
      color: chartSeries.map((_, index) => metricSeriesColor(index)),
      grid: compact ? { left: 46, right: 12, top: 12, bottom: 28 } : { left: 46, right: 16, top: 18, bottom: 30 },
      legend: undefined,
      tooltip: { trigger: "axis", valueFormatter: (value: unknown) => formatMetric(Number(value)) },
      xAxis: {
        type: "value",
        axisLabel: { color: "#7d858c", fontSize: 10 },
        axisLine: { lineStyle: { color: "#d7dde2" } },
        axisTick: { lineStyle: { color: "#d7dde2" } },
        splitLine: { lineStyle: { color: "#edf1f4" } }
      },
      yAxis: {
        type: "value",
        scale: true,
        axisLabel: { color: "#7d858c", fontSize: 10, formatter: (value: number) => formatMetric(value) },
        axisLine: { show: true, lineStyle: { color: "#d7dde2" } },
        axisTick: { show: true, lineStyle: { color: "#d7dde2" } },
        splitLine: { lineStyle: { color: "#edf1f4" } }
      },
      series: chartSeries.slice(0, 12).map((row, index) => ({
        name: row.label,
        type: "line",
        smooth: false,
        lineStyle: { width: compact ? 1.7 : 2 },
        showSymbol: row.points.length <= 1,
        symbolSize: row.points.length <= 1 ? 8 : 4,
        data: row.points.map((point, idx) => ({
          name: row.fullLabel,
          value: [metricChartAxis(point, idx, axisKind), point.value],
          itemStyle: { color: metricSeriesColor(index) }
        }))
      }))
    });
    const resize = () => chart.resize();
    const observer = typeof ResizeObserver !== "undefined" ? new ResizeObserver(resize) : null;
    if (observer) observer.observe(ref.current);
    window.addEventListener("resize", resize);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", resize);
      chart.dispose();
    };
  }, [points, series, compact, axisKind]);
  return <div className={compact ? "chart chart-compact" : "chart"} ref={ref} />;
}

function ResourceModal({ t, token: _token, resource, onClose, onSave, onCreateLocal }: { t: T; token: string; resource: Partial<Resource>; onClose: () => void; onSave: (resource: Partial<Resource>) => Promise<void>; onCreateLocal: (name: string, rootDir: string) => Promise<void> }) {
  const [draft, setDraft] = useState<Partial<Resource>>(resource);
  const [saving, setSaving] = useState(false);
  const isLocalCandidate = !draft.id && draft.host === "127.0.0.1";
  const update = (key: keyof Resource, value: string | number) => setDraft((prev) => ({ ...prev, [key]: value }));
  return (
    <Modal title={draft.id ? t("editResource") : t("addResource")} onClose={onClose}>
      <form
        className="form-grid"
        onSubmit={async (event) => {
          event.preventDefault();
          setSaving(true);
          try {
            if (isLocalCandidate) await onCreateLocal(draft.name || "", draft.root_dir || "");
            else await onSave(draft);
          } finally {
            setSaving(false);
          }
        }}
      >
        <Field label={t("name")} value={draft.name || ""} onChange={(v) => update("name", v)} required />
        <Field label={t("host")} value={draft.host || ""} onChange={(v) => update("host", v)} required />
        <Field label={t("user")} value={draft.user || "root"} onChange={(v) => update("user", v)} />
        <Field label={t("port")} value={String(draft.port || 22)} onChange={(v) => update("port", Number(v) || 22)} type="number" />
        <Field label={t("rootDir")} value={draft.root_dir || ""} onChange={(v) => update("root_dir", v)} required wide />
        <Field label={t("remotePath")} value={draft.remote_path || ""} onChange={(v) => update("remote_path", v)} wide />
        <Field label={t("condaBase")} value={draft.conda_base || ""} onChange={(v) => update("conda_base", v)} />
        <Field label={t("condaInit")} value={draft.conda_init || ""} onChange={(v) => update("conda_init", v)} />
        <Field label={t("condaEnv")} value={draft.conda_env || ""} onChange={(v) => update("conda_env", v)} />
        <Field label={t("gpuIndices")} value={draft.gpu_indices || ""} onChange={(v) => update("gpu_indices", v)} />
        <Field label={t("authRef")} value={draft.auth_ref || ""} onChange={(v) => update("auth_ref", v)} />
        <Field label={t("socksProxy")} value={draft.socks_proxy || ""} onChange={(v) => update("socks_proxy", v)} />
        <Field label={t("proxyCommand")} value={draft.proxy_command || ""} onChange={(v) => update("proxy_command", v)} wide />
        <Field label={t("tags")} value={draft.tags || ""} onChange={(v) => update("tags", v)} wide />
        <div className="modal-actions">
          <button type="button" onClick={onClose}>
            {t("cancel")}
          </button>
          <button className="primary" disabled={saving} type="submit">
            {t("save")}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function ConfirmModal({ t, state, onClose }: { t: T; state: ConfirmState; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  return (
    <Modal title={state.title || t("confirm")} onClose={onClose}>
      <p>{state.message}</p>
      <div className="modal-actions">
        <button onClick={onClose}>{t("cancel")}</button>
        <button
          className="danger"
          disabled={busy}
          onClick={async () => {
            setBusy(true);
            try {
              await state.run();
              onClose();
            } finally {
              setBusy(false);
            }
          }}
        >
          {state.actionLabel}
        </button>
      </div>
    </Modal>
  );
}

function BookmarkNoteModal({ t, run, initialNote, onClose, onSave }: { t: T; run: Run; initialNote: string; onClose: () => void; onSave: (note: string) => Promise<void> }) {
  const [note, setNote] = useState(initialNote);
  const [saving, setSaving] = useState(false);
  return (
    <Modal title={t("humanMark")} onClose={onClose}>
      <form
        className="note-editor-form"
        onSubmit={async (event) => {
          event.preventDefault();
          setSaving(true);
          try {
            await onSave(note.trim());
          } finally {
            setSaving(false);
          }
        }}
      >
        <span className="muted mono">{run.id}</span>
        <strong>{runTitle(run)}</strong>
        <textarea value={note} onChange={(event) => setNote(event.target.value)} placeholder={t("bookmarkNoteEmpty")} rows={4} autoFocus />
        <div className="modal-actions">
          <button type="button" onClick={onClose}>
            {t("cancel")}
          </button>
          <button className="primary" type="submit" disabled={saving}>
            {t("save")}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function ManualProjectModal({
  t,
  run,
  categories,
  projects,
  assignment,
  onClose,
  onAssign,
  onCreateAndAssign
}: {
  t: T;
  run: Run;
  categories: ManualProjectCategory[];
  projects: ProjectView[];
  assignment?: RunProjectAssignment;
  onClose: () => void;
  onAssign: (categoryID: string) => Promise<void>;
  onCreateAndAssign: (name: string) => Promise<void>;
}) {
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);
  const current = assignment?.category_id || "";
  const categoryNames = new Set(categories.map((category) => category.name.trim().toLowerCase()));
  const projectChoices = projects
    .map((project) => ({ id: project.project_id, name: projectDisplayName(project, t), count: project.total_cards }))
    .filter((project) => project.id !== "__unassigned__" && project.name.trim() && !categoryNames.has(project.name.trim().toLowerCase()));
  const choose = async (categoryID: string) => {
    setSaving(true);
    try {
      await onAssign(categoryID);
    } finally {
      setSaving(false);
    }
  };
  const create = async () => {
    const name = draft.trim();
    if (!name) return;
    setSaving(true);
    try {
      await onCreateAndAssign(name);
    } finally {
      setSaving(false);
    }
  };
  const createFromExistingProject = async (name: string) => {
    setSaving(true);
    try {
      await onCreateAndAssign(name);
    } finally {
      setSaving(false);
    }
  };
  return (
    <Modal title={t("manualProject")} onClose={onClose}>
      <div className="manual-project-editor">
        <span className="muted mono">{run.id}</span>
        <strong>{runTitle(run)}</strong>
        <div className="manual-project-choice-list">
          <button className={!current ? "manual-project-choice active" : "manual-project-choice"} type="button" disabled={saving} onClick={() => void choose("")}>
            <span>{t("unassignedRuns")}</span>
          </button>
          {categories.map((category) => (
            <button key={category.id} className={current === category.id ? "manual-project-choice active" : "manual-project-choice"} type="button" disabled={saving} onClick={() => void choose(category.id)}>
              <span>{category.name}</span>
              {category.run_count ? <em>{category.run_count}</em> : null}
            </button>
          ))}
          {projectChoices.length ? <span className="manual-project-choice-heading">{t("projects")}</span> : null}
          {projectChoices.map((project) => (
            <button key={project.id} className="manual-project-choice project-source" type="button" disabled={saving} onClick={() => void createFromExistingProject(project.name)}>
              <span>{project.name}</span>
              {project.count ? <em>{project.count}</em> : null}
            </button>
          ))}
        </div>
        <div className="manual-project-create-row">
          <input value={draft} onChange={(event) => setDraft(event.target.value)} placeholder={t("newManualProject")} disabled={saving} />
          <button className="primary" type="button" disabled={saving || !draft.trim()} onClick={() => void create()}>
            <Plus size={14} />
            {t("createCategory")}
          </button>
        </div>
      </div>
    </Modal>
  );
}

function useLiveLog(token: string, runId: string, query: { source?: string; path?: string } | null) {
  const [lines, setLines] = useState<{ content: string; line_no?: number; source?: string }[]>([]);
  const [state, setState] = useState<"idle" | "live" | "reconnecting" | "error">("idle");
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let closed = false;
    let ws: WebSocket | null = null;
    let retryTimer: number | undefined;
    setLines([]);
    setError(null);
    if (!query) return;
    const fetchSnapshot = (attempt = 0) => {
      getLogs(token, runId, { ...query, limit: query.path ? 5000 : 500, tail: true })
        .then((logs: LogsResponse) => {
          if (closed) return;
          const snapshotError = logSnapshotError(logs);
          if (snapshotError) {
            setError(snapshotError);
            if (attempt < 3) retryTimer = window.setTimeout(() => fetchSnapshot(attempt + 1), 900 * 2 ** attempt);
            return;
          }
          if (isEmptyRemotePathSnapshot(logs) && attempt < 5) {
            retryTimer = window.setTimeout(() => fetchSnapshot(attempt + 1), 700 * (attempt + 1));
            return;
          }
          setError(null);
          setLines((prev) => mergeLogSnapshot(logs.lines || [], prev));
        })
        .catch((err: unknown) => {
          if (closed) return;
          setError(err instanceof Error ? err.message : String(err));
          setState("error");
          if (attempt < 3) retryTimer = window.setTimeout(() => fetchSnapshot(attempt + 1), 900 * 2 ** attempt);
        });
    };
    fetchSnapshot();
    const connect = () => {
      if (closed) return;
      setState("reconnecting");
      ws = new WebSocket(wsURL(`/ws/runs/${encodeURIComponent(runId)}/logs`, token, { ...query, snapshot: "false" }));
      ws.onopen = () => setState("live");
      ws.onmessage = (event) => {
        try {
          const payload = JSON.parse(event.data);
          if (payload.type === "log.line") {
            setLines((prev) => [...prev.slice(-4999), { content: payload.content || "", line_no: payload.line_no, source: payload.source }]);
          }
        } catch {
          setLines((prev) => [...prev.slice(-4999), { content: String(event.data) }]);
        }
      };
      ws.onclose = () => {
        if (!closed) window.setTimeout(connect, 1500);
      };
      ws.onerror = () => setState("error");
    };
    connect();
    return () => {
      closed = true;
      window.clearTimeout(retryTimer);
      ws?.close();
    };
  }, [token, runId, query?.source, query?.path]);
  return { lines, state, error };
}

function useParsedEvents(lines: string[]): ParsedEvents {
  const [parsed, setParsed] = useState<ParsedEvents>(() => parseEventLines([]));
  useEffect(() => {
    if (!lines.length) {
      setParsed(parseEventLines([]));
      return;
    }
    let done = false;
    const worker = new Worker(new URL("./eventWorker.ts", import.meta.url), { type: "module" });
    worker.onmessage = (event: MessageEvent<ParsedEvents>) => {
      if (!done) setParsed(event.data);
      worker.terminate();
    };
    worker.onerror = () => {
      if (!done) setParsed(parseEventLines(lines));
      worker.terminate();
    };
    worker.postMessage(lines);
    return () => {
      done = true;
      worker.terminate();
    };
  }, [lines.join("\n")]);
  return parsed;
}

function LogPanel({ title, state, hiddenWhenEmpty }: { title: string; state: ReturnType<typeof useLiveLog>; hiddenWhenEmpty?: boolean }) {
  if (hiddenWhenEmpty && !state.lines.length) return null;
  return (
    <section className="log-section">
      <div className="section-head">
        <h2>{title}</h2>
        <Pill tone={state.state === "live" ? "good" : state.state === "error" ? "bad" : "warn"}>{state.state}</Pill>
      </div>
      {state.error ? <div className="log-error">{state.error}</div> : null}
      <pre className="log-pane">{state.lines.map((line) => line.content).join("\n") || "-"}</pre>
    </section>
  );
}

function Modal({ title, onClose, children, wide }: { title: string; onClose: () => void; children: ReactNode; wide?: boolean }) {
  return (
    <div className="modal-backdrop" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <motion.div className={wide ? "modal wide" : "modal"} initial={{ opacity: 0, scale: 0.98, y: 12 }} animate={{ opacity: 1, scale: 1, y: 0 }} exit={{ opacity: 0, scale: 0.98, y: 12 }}>
        <header>
          <h2>{title}</h2>
          <button className="icon-button" onClick={onClose}>
            <X size={18} />
          </button>
        </header>
        {children}
      </motion.div>
    </div>
  );
}

function ResourceCard({ resource, actions }: { resource: Resource; actions?: ReactNode }) {
  const snap = resource.latest_snapshot;
  const gpus = parseGPUs(snap?.gpu_json);
  const memPct = snap?.mem_total_mb ? ((snap.mem_used_mb || 0) / snap.mem_total_mb) * 100 : 0;
  const gpuList = gpus.length ? gpus : [{ index: 0, util: 0 }];
  const hostLine = [resource.host, resource.os_type].filter(Boolean).join(" · ") || resource.type;
  const envLine = [resource.user ? `${resource.user}@${resource.host}` : resource.host, resource.conda_env].filter(Boolean).join(" · ");
  return (
    <article className={`resource-card ${actions ? "with-actions" : ""} ${statusTone(resource.status)}`}>
      <div className="resource-card-top">
        <div>
          <strong>{resource.name}</strong>
          <span>{hostLine}</span>
        </div>
        <Pill tone={statusTone(resource.status)}>{resource.status}</Pill>
      </div>
      <div className="resource-meta-line">
        <span>{envLine || resource.root_dir || "-"}</span>
        <Pill tone={statusTone(resource.ssh_status || "unknown")}>SSH {resource.ssh_status || "unknown"}</Pill>
      </div>
      <div className={resource.last_doctor_error ? "resource-alert-slot has-error" : "resource-alert-slot"}>{resource.last_doctor_error ? <p className="resource-error">{resource.last_doctor_error}</p> : null}</div>
      <div className="resource-meters">
        <ResourceMeter value={snap?.cpu_percent || 0} label="CPU" />
        <ResourceMeter value={memPct} label="RAM" detail={`${fmtMB(snap?.mem_used_mb)}/${fmtMB(snap?.mem_total_mb)}`} />
        {gpuList.flatMap((gpu: GPUInfo, index) => gpuMeters(gpu, index))}
      </div>
      {actions ? <div className="resource-card-actions">{actions}</div> : null}
    </article>
  );
}

function gpuMeters(gpu: GPUInfo, index: number) {
  const gpuIndex = gpu.index ?? index;
  const util = gpu.util ?? gpu.utilization ?? 0;
  const memUsed = gpu.mem_used_mb ?? gpu.mem_used;
  const memTotal = gpu.mem_total_mb ?? gpu.mem_total;
  const memPct = memTotal ? ((memUsed || 0) / memTotal) * 100 : 0;
  return [
    <ResourceMeter key={`gpu-${gpuIndex}-util`} value={util} label={`GPU${gpuIndex}`} detail="util" />,
    <ResourceMeter key={`gpu-${gpuIndex}-mem`} value={memPct} label={`VRAM${gpuIndex}`} detail={`${fmtMB(memUsed)}/${fmtMB(memTotal)}`} />
  ];
}

function RunCard({ run, resourceById, onOpen }: { run: Run; resourceById: Map<string, Resource>; onOpen: () => void }) {
  const kind = run.kind || "formal";
  return (
    <button className="run-card" onClick={onOpen} title={runTitle(run)}>
      <div className="card-head">
        <strong>{runTitle(run)}</strong>
        <Pill tone={statusTone(run.status)}>{run.status}</Pill>
      </div>
      <div className="run-card-meta">
        <span className="run-resource-chip">{resourceById.get(run.resource_id)?.name || run.resource_id}</span>
        <span className={`run-kind-chip ${runKindClass(kind)}`}>{kind}</span>
        <span className="run-gpu-chip">GPU {runGPU(run.gpu_index)}</span>
      </div>
      <span className="mono muted">{fmtShortTime(run.created_at)}</span>
      <span className="run-command-line">{run.command}</span>
    </button>
  );
}

function RunListCard({
  run,
  resourceById,
  projectMeta,
  manualAssignment,
  markCount,
  markPreviews,
  bookmark,
  selected,
  trash,
  onOpen,
  onOpenMark,
  onSelect,
  onToggleBookmark,
  onEditBookmarkNote,
  onEditManualProject,
  onArchive,
  onRestore,
  onDelete,
  t
}: {
  run: Run;
  resourceById: Map<string, Resource>;
  projectMeta?: RunProjectMeta;
  manualAssignment?: RunProjectAssignment;
  markCount: number;
  markPreviews: RunMark[];
  bookmark?: RunBookmark;
  selected: boolean;
  trash: boolean;
  onOpen: () => void;
  onOpenMark: (mark: RunMark) => void;
  onSelect: (checked: boolean) => void;
  onToggleBookmark: () => void;
  onEditBookmarkNote: () => void;
  onEditManualProject: () => void;
  onArchive: () => void;
  onRestore: () => void;
  onDelete: () => void;
  t: T;
}) {
  const compareEligible = isCompareEligible(run) && !trash;
  const kind = run.kind || "formal";
  const resourceName = resourceById.get(run.resource_id)?.name || run.resource_id;
  const gpu = runGPU(run.gpu_index);
  const createdAt = fmtShortTime(run.created_at);
  const bookmarkNote = bookmark?.note?.trim() || "";
  const visibleMarks = compactMarkPreviews(markPreviews);
  const manualProjectName = manualAssignment?.category_name || t("unassignedRuns");
  return (
    <article className="run-list-card">
      <div className="run-list-card-head">
        <button className="run-title-cell" type="button" onClick={onOpen}>
          <strong>{runTitle(run)}</strong>
          <span className="mono muted">{run.id}</span>
        </button>
        <Pill tone={statusTone(run.status)}>{run.status}</Pill>
      </div>
      <div className="run-project-stack">
        {projectMeta ? (
          <div className={projectMeta.source === "manual" ? "run-project-context manual" : "run-project-context"}>
            <span className="run-project-name">{projectMeta.projectName}</span>
            <strong>{projectMeta.cardTitle}</strong>
            {projectMeta.evidenceLevel ? <Pill tone={projectMeta.evidenceLevel === "A" || projectMeta.evidenceLevel === "B" ? "good" : "neutral"}>L{projectMeta.evidenceLevel}</Pill> : null}
          </div>
        ) : null}
        <button className={manualAssignment ? "run-project-assignment-summary assigned" : "run-project-assignment-summary"} type="button" onClick={onEditManualProject} title={t("manualProject")}>
          <span>{t("manualProject")}</span>
          <strong>{manualProjectName}</strong>
        </button>
      </div>
      <div className="run-list-facts">
        <span className="run-fact run-fact-resource" title={`${t("resource")}: ${resourceName}`} aria-label={`${t("resource")}: ${resourceName}`}>
          <Server size={12} className="run-fact-icon" />
          <span className="run-fact-value">{resourceName}</span>
        </span>
        <span className={`run-fact run-fact-kind run-kind-chip ${runKindClass(kind)}`} title={`${t("kind")}: ${kind}`} aria-label={`${t("kind")}: ${kind}`}>
          <span className="run-fact-dot" />
          <span className="run-fact-value">{kind}</span>
        </span>
        <span className="run-fact run-fact-gpu" title={`${t("gpu")}: ${gpu}`} aria-label={`${t("gpu")}: ${gpu}`}>
          <Cpu size={12} className="run-fact-icon" />
          <span className="run-fact-value">{gpu}</span>
        </span>
        <span className="run-fact run-fact-time" title={createdAt} aria-label={`${t("time")}: ${createdAt}`}>
          <Clock size={12} className="run-fact-icon" />
          <span className="run-fact-value">{createdAt}</span>
        </span>
      </div>
      {visibleMarks.length || !trash ? (
        <div className="run-mark-previews" aria-label={t("agentFindings")}>
          {visibleMarks.map((mark) => {
            const statement = markStatement(mark);
            return (
              <button key={mark.id} className={`run-mark-preview run-mark-preview-${markTone(mark.kind)}`} type="button" onClick={() => onOpenMark(mark)}>
                <span className="run-mark-preview-meta">
                  <Pill tone={markTone(mark.kind)}>{mark.kind || "mark"}</Pill>
                  <span>{mark.actor || "agent"}</span>
                </span>
                <span className="run-mark-preview-copy">
                  <strong>{mark.title || statement || mark.kind || t("notes")}</strong>
                </span>
              </button>
            );
          })}
          {!trash ? (
            <button className={bookmarkNote ? "run-mark-preview run-mark-preview-human has-note" : "run-mark-preview run-mark-preview-human"} type="button" onClick={onEditBookmarkNote}>
              <span className="run-mark-preview-meta">
                <Pill tone="accent">{t("humanMark")}</Pill>
              </span>
              <span className="run-mark-preview-copy">
                <strong>{bookmarkNote || t("addHumanMark")}</strong>
              </span>
            </button>
          ) : null}
        </div>
      ) : null}
      <div className="run-list-actions">
        {compareEligible ? (
          <label className="run-compare-toggle">
            <input type="checkbox" checked={selected} onChange={(event) => onSelect(event.target.checked)} />
            {t("compare")}
          </label>
        ) : null}
        <button className="icon-action primary-action" type="button" title={t("open")} onClick={onOpen}>
          <ExternalLink size={14} />
        </button>
        {!trash ? (
          <>
            <button className="icon-action" type="button" title={t("favorites")} onClick={onToggleBookmark}>
              <Heart size={15} fill={bookmark ? "currentColor" : "none"} />
            </button>
            <button className="icon-action" type="button" title={t("archive")} disabled={isActiveRun(run)} onClick={onArchive}>
              <Archive size={15} />
            </button>
          </>
        ) : (
          <>
            <button className="icon-action" type="button" title={t("restore")} onClick={onRestore}>
              <RefreshCcw size={15} />
            </button>
            <button className="icon-action danger-inline" type="button" title={t("delete")} onClick={onDelete}>
              <Trash2 size={15} />
            </button>
          </>
        )}
      </div>
    </article>
  );
}

function runKindClass(kind?: string) {
  const normalized = (kind || "formal").toLowerCase();
  if (["formal", "ablation", "smoke", "pilot", "setup", "tool", "tools", "exec", "execs"].includes(normalized)) return `run-kind-${normalized}`;
  return "run-kind-other";
}

function marksByRun(marks: RunMark[]) {
  const out = new Map<string, RunMark[]>();
  for (const mark of marks) {
    const row = out.get(mark.run_id) || [];
    row.push(mark);
    out.set(mark.run_id, row);
  }
  for (const row of out.values()) {
    row.sort((a, b) => markCreatedMs(b) - markCreatedMs(a));
  }
  return out;
}

function compactMarkPreviews(marks: RunMark[]) {
  const seen = new Set<string>();
  const visible: RunMark[] = [];
  for (const mark of marks) {
    if ((mark.actor || "").toLowerCase() === "human") continue;
    const key = [mark.kind || "", mark.title || "", markStatement(mark) || ""].join("\0").toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    visible.push(mark);
    if (visible.length >= 1) break;
  }
  return visible;
}

function markCreatedMs(mark: RunMark) {
  const value = mark.created_at ? Date.parse(mark.created_at) : 0;
  return Number.isFinite(value) ? value : 0;
}

function markTone(kind?: string) {
  return kind === "failure" ? "bad" : kind === "key_result" ? "good" : kind === "followup" ? "accent" : "neutral";
}

function DashboardFinding({ mark, onOpenRun }: { mark: RunMark; onOpenRun?: () => void }) {
  const statement = markStatement(mark);
  const tone = markTone(mark.kind);
  return (
    <button className={`dashboard-finding dashboard-finding-${tone}`} onClick={onOpenRun} type="button">
      <div className="dashboard-finding-kicker">
        <Pill tone={tone}>{mark.kind || "mark"}</Pill>
        <span className="dashboard-finding-actor">{mark.actor || "agent"}</span>
        <span className="mono muted">{fmtShortTime(mark.created_at)}</span>
        <span className="dashboard-finding-run">
          <span className="mono">{mark.run_id}</span>
          {onOpenRun ? <ExternalLink size={13} /> : null}
        </span>
      </div>
      <strong className="dashboard-finding-title">{mark.title || mark.kind || mark.run_id}</strong>
      <div className="dashboard-finding-body">
        <p>{statement || mark.run_id}</p>
      </div>
    </button>
  );
}

function Finding({ mark, onOpen }: { mark: RunMark; onOpen?: () => void }) {
  const statement = markStatement(mark);
  const tone = markTone(mark.kind);
  return (
    <button className="finding" onClick={onOpen} type="button">
      <div className="finding-meta">
        <Pill tone={tone}>{mark.kind || "mark"}</Pill>
        <span className="mono muted">{fmtShortTime(mark.created_at)}</span>
        <span>{mark.actor || "agent"}</span>
      </div>
      <div className="finding-content">
        <strong>{mark.title || mark.kind}</strong>
        <span className="finding-reason">{statement || mark.run_id}</span>
      </div>
      <span className="finding-run mono">{mark.run_id}</span>
    </button>
  );
}

function markStatement(mark: RunMark) {
  if (mark.statement?.trim()) return mark.statement.trim();
  const source = mark.reason || mark.body_md || mark.evidence || "";
  return source.split(/\n\s*\n|\n/).map((line) => line.trim()).find(Boolean) || "";
}

function markBodyMarkdown(mark: RunMark) {
  if (mark.body_md?.trim()) return mark.body_md.trim();
  return [mark.reason, mark.evidence && mark.evidence !== mark.reason ? mark.evidence : ""].filter(Boolean).join("\n\n").trim();
}

function MarkDetailModal({ mark, token, t, onClose }: { mark: RunMark; token: string; t: T; onClose: () => void }) {
  const body = markBodyMarkdown(mark);
  return (
    <Modal title={mark.title || mark.kind || t("agentFindings")} onClose={onClose}>
      <article className="mark-detail">
        <div className="finding-meta">
          <Pill tone={markTone(mark.kind)}>{mark.kind || "mark"}</Pill>
          <span className="mono muted">{fmtShortTime(mark.created_at)}</span>
          <span>{mark.actor || "agent"}</span>
          <span className="mono">{mark.run_id}</span>
        </div>
        <div className="mark-detail-body">
          {body ? <RunMarkMarkdown mark={mark} token={token} body={body} /> : <p>{mark.run_id}</p>}
        </div>
      </article>
    </Modal>
  );
}

function RunMarkMarkdown({ mark, token, body }: { mark: RunMark; token: string; body: string }) {
  const attachmentByID = new Map((mark.attachments || []).map((attachment) => [attachment.id, attachment]));
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      urlTransform={(url) => (url.startsWith("aexp-attachment://") ? url : defaultUrlTransform(url))}
      components={{
        img({ src, alt }) {
          const rawSrc = String(src || "");
          const attachmentID = rawSrc.startsWith("aexp-attachment://") ? rawSrc.replace("aexp-attachment://", "") : "";
          if (!attachmentID) return <img src={rawSrc} alt={alt || ""} />;
          const attachment = attachmentByID.get(attachmentID);
          if (!attachment) {
            return <span className="missing-attachment">Missing attachment {attachmentID}</span>;
          }
          return (
            <figure className="mark-attachment">
              <img src={runMarkAttachmentBlobUrl(mark.id, attachmentID, token)} alt={alt || attachment.caption || attachment.filename} loading="lazy" />
              <figcaption>{attachment.caption || alt || attachment.filename}</figcaption>
            </figure>
          );
        }
      }}
    >
      {body}
    </ReactMarkdown>
  );
}

function ExecCompact({ event, resourceById }: { event: ExecEvent; resourceById: Map<string, Resource> }) {
  return (
    <div className="exec-compact">
      <span className="mono muted">{fmtShortTime(event.created_at)}</span>
      <strong>{resourceById.get(event.resource_id)?.name || event.resource_id}</strong>
      <span className="truncate">{event.command}</span>
    </div>
  );
}

function ExecListItem({ event, resourceById }: { event: ExecEvent; resourceById: Map<string, Resource> }) {
  return (
    <article className="exec-list-item">
      <div className="exec-list-meta">
        <span className="mono muted">{fmtShortTime(event.created_at)}</span>
        <strong>{resourceById.get(event.resource_id)?.name || event.resource_id}</strong>
        <Pill tone="neutral">{event.actor || "cli"}</Pill>
      </div>
      <span className="command-snippet">{event.command}</span>
      <div className="exec-list-result">
        <span className="mono">exit {formatExitCode(event.exit_code)}</span>
        <span className="muted">{fmtDuration(event.duration_ms)}</span>
      </div>
    </article>
  );
}

function Section({ title, children, className }: { title: string; children: ReactNode; className?: string }) {
  return (
    <section className={className ? `panel ${className}` : "panel"}>
      <div className="section-head">
        <h2>{title}</h2>
      </div>
      {children}
    </section>
  );
}

function Stat({ label, value, icon }: { label: string; value: ReactNode; icon: ReactNode }) {
  return (
    <div className="stat">
      <span>{icon}</span>
      <div>
        <p>{label}</p>
        <strong>{value}</strong>
      </div>
    </div>
  );
}

function Meter({ value, label }: { value: number; label?: string }) {
  const pct = Math.max(0, Math.min(100, value || 0));
  return (
    <div className="meter">
      <span>{label || `${pct.toFixed(0)}%`}</span>
      <div>
        <i style={{ width: `${pct}%` }} />
      </div>
      <b>{pct.toFixed(0)}%</b>
    </div>
  );
}

function formatMetric(value: number) {
  if (!Number.isFinite(value)) return "-";
  const abs = Math.abs(value);
  if (abs === 0) return "0";
  if (abs >= 1000 || abs < 0.001) return value.toExponential(3);
  if (abs >= 10) return value.toFixed(3);
  return value.toPrecision(4);
}

function formatMetricValue(point: MetricPoint) {
  return [formatMetric(point.value), point.unit].filter(Boolean).join(" ");
}

function formatBytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${size >= 10 || unit === 0 ? size.toFixed(0) : size.toFixed(1)} ${units[unit]}`;
}

function formatMetricDelta(value: number, pct?: number) {
  if (!Number.isFinite(value)) return "-";
  const sign = value > 0 ? "+" : "";
  const pctText = pct != null && Number.isFinite(pct) ? ` (${sign}${pct.toFixed(1)}%)` : "";
  return `${sign}${formatMetric(value)}${pctText}`;
}

function formatMetricSpan(start?: number, end?: number) {
  if (start == null || end == null) return "-";
  return `${formatMetric(start)} -> ${formatMetric(end)}`;
}

function metricAxisLabel(family: MetricFamily, t: T) {
  if (family.axisKind === "epoch") return `epoch ${formatMetricSpan(family.axisStart, family.axisEnd)}`;
  if (family.axisKind === "step") return `step ${formatMetricSpan(family.axisStart, family.axisEnd)}`;
  return `${family.count} ${t("points")}`;
}

function metricChartAxis(point: MetricPoint, fallback: number, axisKind: "epoch" | "step" | "sample") {
  if (axisKind === "epoch") return point.epoch ?? fallback;
  if (axisKind === "step") return point.step ?? fallback;
  return fallback;
}

function metricSeriesColor(index: number) {
  const colors = ["#4d6f91", "#b3522f", "#648a5a", "#9a6a24", "#6e6a9a", "#3f7e82", "#8a5a44", "#58748a"];
  return colors[index % colors.length];
}

function summarizeChartSeries(points: MetricPoint[], axisKind: "epoch" | "step" | "sample"): MetricSeriesSummary[] {
  const grouped = new Map<string, MetricPoint[]>();
  for (const point of points) {
    const key = point.series ? `${point.series}/${point.name}` : point.name;
    grouped.set(key, [...(grouped.get(key) || []), point]);
  }
  return Array.from(grouped.entries()).map(([key, rows]) => ({
    key,
    label: shortSeriesName(key),
    fullLabel: key,
    latest: rows[rows.length - 1],
    count: rows.length,
    role: "curve",
    trend: rows.map((row, index) => ({ axis: metricChartAxis(row, index, axisKind), value: row.value })),
    points: rows
  }));
}

function metricSparklinePoints(points: Array<{ value: number }>, height = 34) {
  if (!points.length) return "";
  const mid = height / 2;
  if (points.length === 1) return `0,${mid} 120,${mid}`;
  const values = points.map((point) => point.value);
  const min = Math.min(...values);
  const max = Math.max(...values);
  const range = max - min || 1;
  const top = 4;
  const bottom = height - 4;
  return points.map((point, index) => {
    const x = (index / (points.length - 1)) * 120;
    const y = bottom - ((point.value - min) / range) * (bottom - top);
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  }).join(" ");
}

function formatExitCode(value: unknown) {
  if (value == null) return "-";
  if (typeof value === "number") return String(value);
  if (typeof value === "object") {
    const obj = value as { Int64?: number; Valid?: boolean };
    if (obj.Valid === false) return "-";
    if (typeof obj.Int64 === "number") return String(obj.Int64);
  }
  return text(value) || "-";
}

function ResourceMeter({ value, label, detail }: { value: number; label: string; detail?: string }) {
  const pct = Math.max(0, Math.min(100, value || 0));
  const level = pct >= 88 ? "high" : pct >= 65 ? "mid" : "low";
  return (
    <div className="resource-meter" data-level={level}>
      <span className="resource-meter-label">{label}</span>
      <b>{pct.toFixed(0)}%</b>
      {detail ? <span className="resource-meter-detail">{detail}</span> : null}
      <div className="resource-meter-track">
        <i style={{ width: `${pct}%` }} />
      </div>
    </div>
  );
}

function Pill({ children, tone }: { children: ReactNode; tone: "good" | "bad" | "warn" | "neutral" | "accent" }) {
  return <span className={`pill ${tone}`}>{children}</span>;
}

function Select({ value, onChange, options }: { value: string; onChange: (value: string) => void; options: [string, string][] }) {
  return (
    <select value={value} onChange={(event) => onChange(event.target.value)}>
      {options.map(([id, label]) => (
        <option key={id} value={id}>
          {label}
        </option>
      ))}
    </select>
  );
}

function Segmented({ value, options, onChange }: { value: string; options: { value: string; label: string }[]; onChange: (value: string) => void }) {
  return (
    <div className="segmented">
      {options.map((option) => (
        <button key={option.value} className={value === option.value ? "active" : ""} onClick={() => onChange(option.value)}>
          {option.label}
        </button>
      ))}
    </div>
  );
}

function ProjectAssignmentControl({
  t,
  categories,
  assignment,
  onAssign,
  onCreateAndAssign
}: {
  t: T;
  categories: ManualProjectCategory[];
  assignment?: RunProjectAssignment;
  onAssign: (categoryID: string) => Promise<void>;
  onCreateAndAssign: (name: string) => Promise<void>;
}) {
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);
  const current = assignment?.category_id || "";
  const options: [string, string][] = [
    ["", t("unassignedRuns")],
    ...categories.map((category) => [category.id, category.run_count ? `${category.name} (${category.run_count})` : category.name] as [string, string])
  ];
  if (current && !categories.some((category) => category.id === current)) {
    options.splice(1, 0, [current, assignment?.category_name || current]);
  }
  const create = async () => {
    const name = draft.trim();
    if (!name) return;
    setSaving(true);
    try {
      await onCreateAndAssign(name);
      setDraft("");
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="project-assignment-control">
      <Select
        value={current}
        onChange={(value: string) => {
          setSaving(true);
          void onAssign(value).finally(() => setSaving(false));
        }}
        options={options}
      />
      <div className="project-assignment-create">
        <input value={draft} onChange={(event) => setDraft(event.target.value)} placeholder={t("newManualProject")} disabled={saving} />
        <button type="button" disabled={saving || !draft.trim()} onClick={() => void create()}>
          {t("newManualProject")}
        </button>
      </div>
    </div>
  );
}

function Pager({ t, total, page, setPage }: { t: T; total: number; page: number; setPage: (page: number) => void }) {
  const maxPage = Math.max(0, Math.ceil(total / pageSize) - 1);
  return (
    <div className="pager">
      <span>
        {t("total")} {total} · {t("page")} {page + 1}/{maxPage + 1}
      </span>
      <button disabled={page <= 0} onClick={() => setPage(page - 1)}>
        <ChevronLeft size={16} />
        {t("previous")}
      </button>
      <button disabled={page >= maxPage} onClick={() => setPage(page + 1)}>
        {t("next")}
        <ChevronRight size={16} />
      </button>
    </div>
  );
}

function Field({ label, value, onChange, type, required, wide }: { label: string; value: string; onChange: (value: string) => void; type?: string; required?: boolean; wide?: boolean }) {
  return (
    <label className={wide ? "wide" : ""}>
      <span>{label}</span>
      <input value={value} onChange={(event) => onChange(event.target.value)} type={type || "text"} required={required} />
    </label>
  );
}

function Info({ label, value }: { label: string; value: string }) {
  return (
    <div className="info-row">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function Empty({ t }: { t: T }) {
  return <div className="empty">{t("noData")}</div>;
}

function defaultResource(): Partial<Resource> {
  return { type: "ssh", user: "root", port: 22, status: "unknown" };
}

function statusTone(status?: string): "good" | "bad" | "warn" | "neutral" | "accent" {
  const s = (status || "").toLowerCase();
  if (["running", "idle", "ok", "succeeded"].includes(s)) return "good";
  if (["failed", "lost", "unreachable"].includes(s)) return "bad";
  if (["starting", "queued", "busy", "created", "unknown"].includes(s)) return "warn";
  if (["formal", "ablation"].includes(s)) return "accent";
  return "neutral";
}

function labelForTab(tab: Tab, t: T) {
  const map: Record<Tab, I18nKey> = { dashboard: "dashboard", resources: "resources", projects: "projects", matrices: "matrices", evidence: "evidenceChains", runs: "runs", favorites: "favorites", execs: "execs" };
  return t(map[tab]);
}

function filterProjects(projects: ProjectView[], query: string) {
  const q = query.trim().toLowerCase();
  if (!q) return projects;
  return projects.filter((project) =>
    [
      project.project_id,
      project.project_name,
      ...(project.cards || []).flatMap((card) => [
        card.id,
        card.run_id,
        card.question,
        card.verdict,
        card.key_metrics,
        card.next_action,
        card.supports_claim,
        card.weakens_claim,
        card.artifact_paths,
        card.related_runs,
        card.run?.name,
        card.run?.command,
        card.run?.resource_id,
        card.run?.status,
        card.run?.kind,
        ...(card.marks || []).flatMap((mark) => [mark.title, mark.reason, mark.evidence, mark.kind, mark.actor])
      ])
    ].some((part) => text(part).toLowerCase().includes(q))
  );
}

function projectDisplayName(project: ProjectView, t: T) {
  return project.project_id === "__unassigned__" || project.project_name === "Unassigned runs" ? t("unassignedRuns") : project.project_name || project.project_id;
}

function manualProjectFilterValue(id: string) {
  return `manual:${id}`;
}

function projectCardFilterValue(id: string) {
  return `card:${id}`;
}

function runProjectMatches(meta: RunProjectMeta | undefined, filterValue: string) {
  if (!meta) return false;
  if (filterValue.startsWith("manual:")) return meta.source === "manual" && meta.projectId === filterValue.slice("manual:".length);
  if (filterValue.startsWith("card:")) return meta.source !== "manual" && meta.projectId === filterValue.slice("card:".length);
  return meta.projectId === filterValue;
}

function buildRunProjectIndex(projects: ProjectView[], t: T, manualAssignments?: Map<string, RunProjectAssignment>, manualCategories?: Map<string, ManualProjectCategory>) {
  const out = new Map<string, RunProjectMeta>();
  for (const assignment of manualAssignments?.values() || []) {
    const category = manualCategories?.get(assignment.category_id);
    const categoryName = assignment.category_name || category?.name || assignment.category_id;
    out.set(assignment.run_id, {
      projectId: assignment.category_id,
      projectName: t("manualProject"),
      cardTitle: categoryName,
      source: "manual"
    });
  }
  for (const project of projects) {
    const projectName = projectDisplayName(project, t);
    for (const card of project.cards || []) {
      if (!card.run_id) continue;
      const meta: RunProjectMeta = {
        projectId: project.project_id,
        projectName,
        cardTitle: card.verdict || card.question || card.run?.name || card.run_id,
        cardSummary: card.key_metrics || card.supports_claim || card.weakens_claim || card.next_action || card.proposal_reason || "",
        evidenceLevel: card.evidence_level,
        source: "card"
      };
      const current = out.get(card.run_id);
      if (!current || (current.source !== "manual" && (card.should_promote || card.important))) out.set(card.run_id, meta);
    }
  }
  return out;
}

function readDeepLinkRun() {
  const match = window.location.pathname.match(/^\/ui-v2\/runs\/([^/]+)/);
  return match ? decodeURIComponent(match[1]) : null;
}

function readInitialTab(): Tab {
  const path = window.location.pathname;
  if (path.startsWith("/ui-v2/resources")) return "resources";
  if (path.startsWith("/ui-v2/projects")) return "projects";
  if (path.startsWith("/ui-v2/matrices")) return "matrices";
  if (path.startsWith("/ui-v2/evidence-chains")) return "evidence";
  if (path.startsWith("/ui-v2/runs")) return "runs";
  if (path.startsWith("/ui-v2/favorites")) return "favorites";
  if (path.startsWith("/ui-v2/execs")) return "execs";
  return "dashboard";
}

function pathForTab(tab: Tab) {
  const map: Record<Tab, string> = {
    dashboard: "/ui-v2/",
    resources: "/ui-v2/resources",
    projects: "/ui-v2/projects",
    matrices: "/ui-v2/matrices",
    evidence: "/ui-v2/evidence-chains",
    runs: "/ui-v2/runs",
    favorites: "/ui-v2/favorites",
    execs: "/ui-v2/execs"
  };
  return map[tab];
}

type T = ReturnType<typeof makeT>;
