import { useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { AnimatePresence, motion } from "framer-motion";
import {
  Activity,
  Archive,
  BarChart3,
  Check,
  ChevronLeft,
  ChevronRight,
  Database,
  ExternalLink,
  Heart,
  Languages,
  Network,
  PlayCircle,
  RefreshCcw,
  Server,
  Settings,
  Star,
  Terminal,
  Trash2,
  X
} from "lucide-react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { createColumnHelper, flexRender, getCoreRowModel, useReactTable, type ColumnDef } from "@tanstack/react-table";
import { useVirtualizer } from "@tanstack/react-virtual";
import * as echarts from "echarts";
import {
  ApiError,
  archiveRun,
  cancelRun,
  createLocalResource,
  deleteBookmark,
  deleteResource,
  deleteRunLogically,
  getAllRunMarks,
  getArtifacts,
  getBookmarks,
  getExecEvents,
  getLogs,
  getProjects,
  getResources,
  getRun,
  getRunMarks,
  getRuns,
  getStats,
  localResourceDefaults,
  refreshResource,
  restoreRun,
  saveBookmark,
  saveResource,
  statusCheck,
  testResource,
  wsURL
} from "./api";
import { makeT, type I18nKey } from "./i18n";
import { useAppStore } from "./store";
import type {
  ConfirmState,
  ExecEvent,
  GPUInfo,
  LogsResponse,
  MetricPoint,
  ParsedEvents,
  ProjectView,
  Resource,
  Run,
  RunBookmark,
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
import { parseEventLines } from "./events";
import { EvidenceChainBoard } from "./EvidenceChainBoard";

type Tab = "dashboard" | "resources" | "projects" | "evidence" | "runs" | "favorites" | "execs";

const pageSize = 100;
const runColumn = createColumnHelper<Run>();
const execColumn = createColumnHelper<ExecEvent>();
const runTableColumns = "32px minmax(200px, 1.45fr) minmax(96px, 0.6fr) minmax(108px, 0.75fr) minmax(170px, 1fr) minmax(92px, 0.55fr)";
const execTableColumns = "96px minmax(100px, 0.7fr) 78px minmax(200px, 1fr) 86px";

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

  const stats = useQuery({ queryKey: ["stats", token], queryFn: () => getStats(token), refetchInterval: 5000 });
  const resources = useQuery({ queryKey: ["resources", token], queryFn: () => getResources(token), refetchInterval: 5000 });
  const marks = useQuery({ queryKey: ["marks", token], queryFn: () => getAllRunMarks(token), refetchInterval: 10000 });
  const bookmarks = useQuery({ queryKey: ["bookmarks", token], queryFn: () => getBookmarks(token), refetchInterval: 10000 });
  const projects = useQuery({ queryKey: ["projects", token], queryFn: () => getProjects(token), refetchInterval: 12000 });
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
  const resourceById = useMemo(() => new Map(resourceList.map((r) => [r.id, r])), [resourceList]);
  const visibleRuns = useMemo(() => filterRuns(runList, { query: runQuery, kind: runKind, bookmarks: bookmarkList }), [runList, runQuery, runKind, bookmarkList]);
  const activeRuns = useMemo(() => runList.filter(isActiveRun), [runList]);
  const visibleExecs = useMemo(() => filterExecs(execs.data?.items || [], execQuery), [execs.data, execQuery]);
  const favoriteRuns = useMemo(() => filterRuns(runList, { query: favoriteQuery, kind: "favorites", bookmarks: bookmarkList }), [runList, favoriteQuery, bookmarkList]);
  const visibleProjects = useMemo(() => filterProjects(projects.data || [], projectQuery), [projects.data, projectQuery]);
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
      queryClient.invalidateQueries({ queryKey: ["stats"] })
    ]);
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
            {tab === "projects" && <ProjectsTab t={t} projects={visibleProjects} query={projectQuery} setQuery={setProjectQuery} onOpenRun={setDetailRunId} />}
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
                setStatus={setRunStatus}
                resource={runResource}
                setResource={setRunResource}
                kind={runKind}
                setKind={setRunKind}
                trash={runTrash}
                setTrash={(value) => {
                  setRunTrash(value);
                  setRunPage(0);
                }}
                query={runQuery}
                setQuery={setRunQuery}
                bookmarks={bookmarkList}
                marks={runMarks}
                resourceById={resourceById}
                onOpenRun={setDetailRunId}
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
                onToggleBookmark={async (run, bookmarked) => {
                  if (bookmarked) await deleteBookmark(token, run.id);
                  else await saveBookmark(token, run.id);
                  await invalidateOperationalData();
                }}
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
      {confirm ? <ConfirmModal t={t} state={confirm} onClose={() => setConfirm(null)} /> : null}
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
        <div className="resource-grid">{resources.length ? resources.map((r) => <ResourceCard key={r.id} resource={r} />) : <Empty t={t} />}</div>
      </Section>
      <Section title={t("activeRuns")}>
        <div className="run-card-grid">{activeRuns.length ? activeRuns.map((run) => <RunCard key={run.id} run={run} resourceById={resourceById} onOpen={() => onOpenRun(run.id)} />) : <Empty t={t} />}</div>
      </Section>
      <div className="split">
        <Section title={t("agentFindings")}>
          <div className="compact-list">{marks.slice(0, 8).map((mark) => <Finding key={mark.id} mark={mark} onOpenRun={() => onOpenRun(mark.run_id)} />)}</div>
        </Section>
        <Section title={t("recentExec")}>
          <div className="compact-list">{execs.map((event) => <ExecCompact key={event.id} event={event} resourceById={resourceById} />)}</div>
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
  kind: string;
  setKind: (kind: string) => void;
  trash: boolean;
  setTrash: (trash: boolean) => void;
  query: string;
  setQuery: (query: string) => void;
  bookmarks: RunBookmark[];
  marks: Map<string, number>;
  resourceById: Map<string, Resource>;
  onOpenRun: (id: string) => void;
  onCompare: () => void;
  onArchive: (run: Run) => void;
  onRestore: (run: Run) => Promise<void>;
  onDelete: (run: Run) => void;
  onToggleBookmark: (run: Run, bookmarked: boolean) => Promise<void>;
}) {
  const selectedRunIds = useAppStore((s) => s.selectedRunIds);
  const toggleSelectedRun = useAppStore((s) => s.toggleSelectedRun);
  const bookmarkIds = useMemo(() => new Set(props.bookmarks.map((b) => b.run_id)), [props.bookmarks]);
  const selectedCount = props.runs.filter((run) => selectedRunIds.has(run.id)).length;
  const columns = useRunColumns(props.t, props.resourceById, bookmarkIds, props.marks, props.trash, props.onOpenRun, props.onArchive, props.onRestore, props.onDelete, props.onToggleBookmark, toggleSelectedRun, selectedRunIds);
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
        <Select value={props.kind} onChange={props.setKind} options={[["experiments", props.t("experiments")], ["tools", props.t("toolTasks")], ["all", props.t("allKinds")]]} />
        <input value={props.query} onChange={(event) => props.setQuery(event.target.value)} placeholder={props.t("search")} />
        <button disabled={!selectedCount} onClick={props.onCompare}>
          <BarChart3 size={16} />
          {props.t("compare")} ({selectedCount})
        </button>
      </div>
      <VirtualTable columns={columns} data={props.runs} estimateSize={82} columnTemplate={runTableColumns} minWidth={940} />
      <Pager t={props.t} total={props.total} page={props.page} setPage={props.setPage} />
    </div>
  );
}

function useRunColumns(
  t: T,
  resourceById: Map<string, Resource>,
  bookmarkIds: Set<string>,
  marks: Map<string, number>,
  trash: boolean,
  onOpenRun: (id: string) => void,
  onArchive: (run: Run) => void,
  onRestore: (run: Run) => Promise<void>,
  onDelete: (run: Run) => void,
  onToggleBookmark: (run: Run, bookmarked: boolean) => Promise<void>,
  toggleSelectedRun: (run: Run, checked: boolean) => void,
  selectedRunIds: Set<string>
) {
  return useMemo<ColumnDef<Run, any>[]>(
    () => [
      runColumn.display({
        id: "compare",
        header: "",
        cell: (info) =>
          !trash && isCompareEligible(info.row.original) ? (
            <input type="checkbox" checked={selectedRunIds.has(info.row.original.id)} onChange={(event) => toggleSelectedRun(info.row.original, event.target.checked)} onClick={(event) => event.stopPropagation()} />
          ) : null
      }),
      runColumn.accessor("name", {
        header: "Run",
        cell: (info) => (
          <button className="run-title-cell" onClick={() => onOpenRun(info.row.original.id)}>
            <strong>{info.getValue() || info.row.original.id}</strong>
            <span className="mono muted">{info.row.original.id}</span>
            <span className="run-subline">{info.row.original.kind || "formal"} · {fmtShortTime(info.row.original.created_at)}</span>
          </button>
        )
      }),
      runColumn.accessor("status", {
        header: t("status"),
        cell: (info) => (
          <div className="run-state-cell">
            <Pill tone={statusTone(info.getValue())}>{info.getValue()}</Pill>
            <span className="mono muted">{fmtShortTime(info.row.original.created_at)}</span>
          </div>
        )
      }),
      runColumn.accessor("resource_id", {
        header: t("resource"),
        cell: (info) => (
          <div className="run-meta-cell">
            <strong>{resourceById.get(info.getValue())?.name || info.getValue()}</strong>
            <span>GPU {runGPU(info.row.original.gpu_index)} · {info.row.original.project_env || info.row.original.conda_env || "-"}</span>
          </div>
        )
      }),
      runColumn.accessor("command", {
        header: t("command"),
        cell: (info) => (
          <span className="command-snippet">{info.getValue()}</span>
        )
      }),
      runColumn.display({
        id: "actions",
        header: t("actions"),
        cell: (info) => {
          const run = info.row.original;
          const bookmarked = bookmarkIds.has(run.id);
          return (
            <div className="row-actions" onClick={(event) => event.stopPropagation()}>
              <button className="icon-action primary-action" title={t("open")} onClick={() => onOpenRun(run.id)}>
                <ExternalLink size={14} />
              </button>
              {!trash ? (
                <>
                  <button className="icon-action" title={bookmarked ? t("favorites") : t("favorites")} onClick={() => void onToggleBookmark(run, bookmarked)}>
                    <Heart size={15} fill={bookmarked ? "currentColor" : "none"} />
                  </button>
                  <button className="icon-action" title={t("archive")} disabled={isActiveRun(run)} onClick={() => onArchive(run)}>
                    <Archive size={15} />
                  </button>
                </>
              ) : (
                <>
                  <button className="icon-action" title={t("restore")} onClick={() => void onRestore(run)}>
                    <RefreshCcw size={15} />
                  </button>
                  <button className="icon-action danger-inline" title={t("delete")} onClick={() => onDelete(run)}>
                    <Trash2 size={15} />
                  </button>
                </>
              )}
              {marks.get(run.id) ? <span className="mark-badge">{marks.get(run.id)}</span> : null}
            </div>
          );
        }
      })
    ],
    [bookmarkIds, marks, onArchive, onDelete, onOpenRun, onRestore, onToggleBookmark, resourceById, selectedRunIds, t, toggleSelectedRun, trash]
  );
}

function ProjectsTab({ t, projects, query, setQuery, onOpenRun }: { t: T; projects: ProjectView[]; query: string; setQuery: (value: string) => void; onOpenRun: (id: string) => void }) {
  return (
    <div className="stack">
      <div className="toolbar">
        <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("search")} />
      </div>
      <div className="project-list">
        {projects.length ? (
          projects.map((project) => (
            <section className="project-row" key={project.project_id}>
              <div className="project-head">
                <div>
                  <h2>{project.project_name || project.project_id}</h2>
                  <p className="muted mono">{project.project_id}</p>
                </div>
                <div className="pill-row">
                  <Pill tone="accent">{project.important_runs || 0} important</Pill>
                  <Pill tone="good">{project.formal_runs || 0} formal</Pill>
                  <Pill tone="warn">{project.running_runs || 0} running</Pill>
                </div>
              </div>
              <div className="project-cards">
                {(project.cards || []).slice(0, 6).map((card) => (
                  <button key={card.id} className="project-card" onClick={() => card.run_id && onOpenRun(card.run_id)}>
                    <div className="project-card-main">
                      <strong>{card.verdict || card.question || card.run?.name || card.run_id}</strong>
                      <span>{card.question || card.next_action || "-"}</span>
                    </div>
                    {card.key_metrics ? <p className="project-card-metrics">{card.key_metrics}</p> : null}
                    <div className="project-card-foot">
                      <Pill tone={card.evidence_level === "A" || card.evidence_level === "B" ? "good" : "neutral"}>L{card.evidence_level || "C"}</Pill>
                      <span className="mono">{[card.run?.status, card.run?.kind || card.run_id].filter(Boolean).join(" · ") || "-"}</span>
                    </div>
                  </button>
                ))}
                {(project.cards || []).length > 6 ? (
                  <div className="project-more">
                    <strong>+{(project.cards || []).length - 6}</strong>
                    <span>{t("projectMore")}</span>
                  </div>
                ) : null}
                {!project.cards?.length ? <div className="project-empty muted">No promoted cards yet</div> : null}
              </div>
            </section>
          ))
        ) : (
          <Empty t={t} />
        )}
      </div>
    </div>
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
  const columns = useMemo<ColumnDef<ExecEvent, any>[]>(
    () => [
      execColumn.accessor("created_at", { header: props.t("time"), cell: (info) => <span className="mono muted">{fmtShortTime(info.getValue())}</span> }),
      execColumn.accessor("resource_id", { header: props.t("resource"), cell: (info) => props.resourceById.get(info.getValue())?.name || info.getValue() }),
      execColumn.accessor("actor", { header: props.t("actor"), cell: (info) => <Pill tone="neutral">{info.getValue()}</Pill> }),
      execColumn.accessor("command", { header: props.t("command"), cell: (info) => <span className="command-snippet">{info.getValue()}</span> }),
      execColumn.display({
        id: "result",
        header: props.t("duration"),
        cell: (info) => (
          <div className="run-state-cell">
            <span className="mono">exit {info.row.original.exit_code ?? "-"}</span>
            <span className="muted">{fmtDuration(info.row.original.duration_ms)}</span>
          </div>
        )
      })
    ],
    [props]
  );
  return (
    <div className="stack">
      <div className="toolbar dense">
        <Select value={props.resource} onChange={props.setResource} options={[["", props.t("allResources")], ...props.resources.map((r) => [r.id, r.name] as [string, string])]} />
        <Select value={props.actor} onChange={props.setActor} options={[["", props.t("allActors")], ["cli", "cli"], ["api", "api"], ["agent", "agent"]]} />
        <input value={props.query} onChange={(event) => props.setQuery(event.target.value)} placeholder={props.t("search")} />
      </div>
      <VirtualTable columns={columns} data={props.rows} estimateSize={62} columnTemplate={execTableColumns} minWidth={760} />
      <Pager t={props.t} total={props.total} page={props.page} setPage={props.setPage} />
    </div>
  );
}

function RunDetail({
  t,
  token,
  runId,
  resourceById,
  onClose,
  onCancel,
  onStatusCheck
}: {
  t: T;
  token: string;
  runId: string;
  resourceById: Map<string, Resource>;
  onClose: () => void;
  onCancel: (run: Run) => void;
  onStatusCheck: (run: Run) => Promise<void>;
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

  useEffect(() => {
    history.replaceState(null, "", `/ui-v2/runs/${encodeURIComponent(runId)}`);
  }, [runId]);

  return (
    <Modal title={run.data ? runTitle(run.data) : runId} onClose={onClose} wide>
      {run.data ? (
        <div className="detail-grid">
          <div className="detail-main">
            <div className="detail-overview">
              <div className="detail-identity">
                <Pill tone={statusTone(run.data.status)}>{run.data.status}</Pill>
                <strong>{resourceById.get(run.data.resource_id)?.name || run.data.resource_id}</strong>
                <span className="mono muted">{run.data.id}</span>
              </div>
              <div className="detail-facts">
                <span>{run.data.kind || "formal"}</span>
                <span>GPU {runGPU(run.data.gpu_index)}</span>
                <span>{fmtTime(run.data.created_at)}</span>
              </div>
            </div>
            <section className="command-card">
              <div className="section-head">
                <h2>{t("command")}</h2>
                <span className="muted mono">{run.data.resolved_cwd || run.data.cwd || "-"}</span>
              </div>
              <pre className="command-box">{run.data.command}</pre>
            </section>
            <div className="toolbar">
              {isActiveRun(run.data) ? (
                <button className="danger" onClick={() => onCancel(run.data!)}>
                  {t("cancel")}
                </button>
              ) : null}
              <button onClick={() => void onStatusCheck(run.data!)}>
                <RefreshCcw size={16} />
                {t("refreshStatus")}
              </button>
            </div>
            {eventsPath ? <EventDashboard t={t} parsed={parsedEvents} path={eventsPath} /> : null}
            <Section title={t("agentFindings")} className="findings-section">
              {marks.data?.length ? <div className="finding-list">{marks.data.map((mark) => <Finding key={mark.id} mark={mark} />)}</div> : <Empty t={t} />}
            </Section>
            <LogPanel title="terminal" state={terminal} />
            <LogPanel title="stdout" state={stdout} />
            <LogPanel title="stderr" state={stderr} hiddenWhenEmpty />
            {eventsPath ? <LogPanel title={t("events")} state={eventLog} /> : null}
          </div>
          <aside className="detail-side">
            <Info label={t("time")} value={fmtTime(run.data.created_at)} />
            <Info label="cwd" value={run.data.resolved_cwd || run.data.cwd || "-"} />
            <Info label="env" value={run.data.resolved_env || run.data.conda_env || "-"} />
            <Info label="tmux" value={run.data.tmux_session || "-"} />
            <Info label="run dir" value={run.data.remote_run_dir || "-"} />
            <Section title="Artifacts">
              <pre className="mini-pre">{JSON.stringify(artifacts.data || [], null, 2)}</pre>
            </Section>
          </aside>
        </div>
      ) : (
        <Empty t={t} />
      )}
    </Modal>
  );
}

function EventDashboard({ t, parsed, path }: { t: T; parsed: ParsedEvents; path: string }) {
  const latest = parsed.latestMetrics.slice(0, 10);
  const progress = parsed.progress.slice(-5);
  const notes = parsed.notes.slice(-3);
  const metricFamilies = groupMetricFamilies(parsed.metrics).slice(0, 8);
  return (
    <section className="event-dashboard">
      <div className="section-head">
        <h2>{t("events")}</h2>
        <span className="muted mono">{path}</span>
      </div>
      <div className="event-layout">
        <div className="event-panel progress-panel">
          <span className="panel-kicker">Progress</span>
          {progress.length ? progress.map((row) => (
            <div className="progress-row" key={row.name}>
              <div>
                <strong>{row.name}</strong>
                <span>{row.total ? `${row.current}/${row.total}` : String(row.current)}</span>
              </div>
              <Meter value={row.percent ?? row.current} />
            </div>
          )) : <span className="muted">No progress events</span>}
        </div>
        <div className="event-panel metric-panel">
          <span className="panel-kicker">Latest metrics</span>
          <div className="metric-list">
            {latest.length ? latest.map((metric) => (
              <div className="metric-row" key={(metric.series || "") + metric.name}>
                <span>{metric.series ? metric.series + "/" + metric.name : metric.name}</span>
                <strong>{formatMetric(metric.value)}</strong>
              </div>
            )) : <span className="muted">No metrics yet</span>}
          </div>
        </div>
        {(parsed.errors.length || notes.length) ? (
          <div className="event-panel event-notes">
            <span className="panel-kicker">{parsed.errors.length ? "Errors" : "Notes"}</span>
            {parsed.errors.slice(0, 3).map((error, index) => <p key={`error-${index}`}>{error}</p>)}
            {notes.map((note, index) => <p key={`note-${index}`}>{text(note.text || note.message || note.name || "")}</p>)}
          </div>
        ) : null}
      </div>
      <div className="metric-family-grid">
        {metricFamilies.length ? metricFamilies.map((family) => (
          <article className="metric-family" key={family.name}>
            <div className="metric-family-head">
              <strong>{family.name}</strong>
              <span>{family.count} points</span>
            </div>
            <div className="metric-family-values">
              {family.series.map((row) => (
                <div key={row.series || family.name}>
                  <span>{row.series || "default"}</span>
                  <strong>{formatMetric(row.value)}</strong>
                </div>
              ))}
            </div>
          </article>
        )) : <span className="muted">No metric families yet</span>}
      </div>
    </section>
  );
}

function CompareModal({ t, token, runs, onClose }: { t: T; token: string; runs: Run[]; onClose: () => void }) {
  const queries = useQueries({
    queries: runs.map((run) => ({
      queryKey: ["compare-events", token, run.id, uiEventsPath(run)],
      queryFn: async () => {
        const logs = await getLogs(token, run.id, { path: uiEventsPath(run), limit: 5000, tail: true });
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

function MetricChart({ points }: { points: MetricPoint[] }) {
  const ref = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!ref.current) return;
    const chart = echarts.init(ref.current);
    const grouped = new Map<string, MetricPoint[]>();
    for (const point of points) {
      const key = point.series ? point.series + "/" + point.name : point.name;
      grouped.set(key, [...(grouped.get(key) || []), point]);
    }
    chart.setOption({
      animationDuration: 180,
      grid: { left: 40, right: 16, top: 18, bottom: 28 },
      tooltip: { trigger: "axis" },
      xAxis: { type: "value" },
      yAxis: { type: "value", scale: true },
      series: Array.from(grouped.entries()).slice(0, 12).map(([name, rows]) => ({
        name,
        type: "line",
        showSymbol: false,
        data: rows.map((row, idx) => [row.step ?? row.epoch ?? idx, row.value])
      }))
    });
    const resize = () => chart.resize();
    window.addEventListener("resize", resize);
    return () => {
      window.removeEventListener("resize", resize);
      chart.dispose();
    };
  }, [points]);
  return <div className="chart" ref={ref} />;
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

function VirtualTable<T>({
  columns,
  data,
  estimateSize,
  columnTemplate,
  minWidth
}: {
  columns: ColumnDef<T, any>[];
  data: T[];
  estimateSize: number;
  columnTemplate: string;
  minWidth: number;
}) {
  const table = useReactTable({ data, columns, getCoreRowModel: getCoreRowModel() });
  const parentRef = useRef<HTMLDivElement | null>(null);
  const rows = table.getRowModel().rows;
  const virtualizer = useVirtualizer({ count: rows.length, getScrollElement: () => parentRef.current, estimateSize: () => estimateSize, overscan: 12 });
  const gridStyle = { "--table-columns": columnTemplate, "--table-min-width": `${minWidth}px` } as CSSProperties & Record<string, string>;
  return (
    <div className="table-shell" style={gridStyle}>
      <div className="table-scroll" ref={parentRef}>
        {table.getHeaderGroups().map((group) => (
          <div className="virtual-header" key={group.id}>
            {group.headers.map((header) => (
              <div className="virtual-head-cell" key={header.id}>
                {header.isPlaceholder ? null : flexRender(header.column.columnDef.header, header.getContext())}
              </div>
            ))}
          </div>
        ))}
        <div className="virtual-space" style={{ height: `${virtualizer.getTotalSize()}px` }}>
          {virtualizer.getVirtualItems().map((virtualRow) => {
            const row = rows[virtualRow.index];
            return (
              <div className="virtual-row" key={row.id} style={{ transform: `translateY(${virtualRow.start}px)`, height: `${virtualRow.size}px` }}>
                {row.getVisibleCells().map((cell) => (
                  <div className="virtual-cell" key={cell.id}>
                    {flexRender(cell.column.columnDef.cell, cell.getContext())}
                  </div>
                ))}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

function useLiveLog(token: string, runId: string, query: { source?: string; path?: string } | null) {
  const [lines, setLines] = useState<{ content: string; line_no?: number; source?: string }[]>([]);
  const [state, setState] = useState<"idle" | "live" | "reconnecting" | "error">("idle");
  useEffect(() => {
    let closed = false;
    let ws: WebSocket | null = null;
    setLines([]);
    if (!query) return;
    getLogs(token, runId, { ...query, limit: query.path ? 5000 : 500, tail: true })
      .then((logs: LogsResponse) => {
        if (!closed) setLines(logs.lines || []);
      })
      .catch(() => {
        if (!closed) setState("error");
      });
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
      ws?.close();
    };
  }, [token, runId, query?.source, query?.path]);
  return { lines, state };
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
  return (
    <button className="run-card" onClick={onOpen} title={runTitle(run)}>
      <div className="card-head">
        <strong>{runTitle(run)}</strong>
        <Pill tone={statusTone(run.status)}>{run.status}</Pill>
      </div>
      <div className="run-card-meta">
        <span>{resourceById.get(run.resource_id)?.name || run.resource_id}</span>
        <span>{run.kind || "formal"}</span>
        <span>GPU {runGPU(run.gpu_index)}</span>
      </div>
      <span className="mono muted">{fmtShortTime(run.created_at)}</span>
      <span className="run-command-line">{run.command}</span>
    </button>
  );
}

function Finding({ mark, onOpenRun }: { mark: RunMark; onOpenRun?: () => void }) {
  const body = mark.reason || mark.evidence || "";
  return (
    <button className="finding" onClick={onOpenRun}>
      <div className="finding-head">
        <Pill tone="neutral">{mark.kind || "mark"}</Pill>
        <span className="mono muted">{fmtShortTime(mark.created_at)}</span>
      </div>
      <strong>{mark.title || mark.kind}</strong>
      <span>{body || mark.run_id}</span>
      {mark.evidence && mark.reason ? <code>{mark.evidence}</code> : null}
    </button>
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

function groupMetricFamilies(points: MetricPoint[]) {
  const grouped = new Map<string, MetricPoint[]>();
  for (const point of points) {
    grouped.set(point.name, [...(grouped.get(point.name) || []), point]);
  }
  return Array.from(grouped.entries()).map(([name, rows]) => {
    const latestBySeries = new Map<string, MetricPoint>();
    for (const row of rows) {
      latestBySeries.set(row.series || "", row);
    }
    return {
      name,
      count: rows.length,
      series: Array.from(latestBySeries.values()).slice(0, 5)
    };
  });
}

function ResourceMeter({ value, label, detail }: { value: number; label: string; detail?: string }) {
  const pct = Math.max(0, Math.min(100, value || 0));
  return (
    <div className="resource-meter">
      <span className="resource-meter-label">{label}</span>
      <span className="resource-meter-detail">{detail || ""}</span>
      <div className="resource-meter-track">
        <i style={{ width: `${pct}%` }} />
      </div>
      <b>{pct.toFixed(0)}%</b>
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
  const map: Record<Tab, I18nKey> = { dashboard: "dashboard", resources: "resources", projects: "projects", evidence: "evidenceChains", runs: "runs", favorites: "favorites", execs: "execs" };
  return t(map[tab]);
}

function filterProjects(projects: ProjectView[], query: string) {
  const q = query.trim().toLowerCase();
  if (!q) return projects;
  return projects.filter((project) =>
    [project.project_id, project.project_name, ...(project.cards || []).flatMap((card) => [card.question, card.verdict, card.key_metrics, card.run?.name])].some((part) => text(part).toLowerCase().includes(q))
  );
}

function readDeepLinkRun() {
  const match = window.location.pathname.match(/^\/ui-v2\/runs\/([^/]+)/);
  return match ? decodeURIComponent(match[1]) : null;
}

function readInitialTab(): Tab {
  const path = window.location.pathname;
  if (path.startsWith("/ui-v2/resources")) return "resources";
  if (path.startsWith("/ui-v2/projects")) return "projects";
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
    evidence: "/ui-v2/evidence-chains",
    runs: "/ui-v2/runs",
    favorites: "/ui-v2/favorites",
    execs: "/ui-v2/execs"
  };
  return map[tab];
}

type T = ReturnType<typeof makeT>;
