import { lazy, Suspense, useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { AnimatePresence, LayoutGroup, motion } from "framer-motion";
import ReactMarkdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  Activity,
  Archive,
  BarChart3,
  BookOpen,
  Check,
  ChevronLeft,
  ChevronRight,
  Clock,
  Cpu,
  Database,
  ExternalLink,
  Heart,
  HardDrive,
  KeyRound,
  Languages,
  Network,
  PlayCircle,
  Plus,
  RefreshCcw,
  Server,
  Settings,
  Terminal,
  Trash2
} from "lucide-react";
import { runDataFinalizationPresentation } from "./dataState";
import { StatusCapsule } from "./StatusCapsule";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  assignRunProject,
  archiveRun,
  cancelRun,
  collectArtifacts,
  createLocalResource,
  deleteBookmark,
  deleteResource,
  deleteRunLogically,
  getArtifacts,
  getArtifactCollection,
  getBookmarks,
  getExecEvents,
  getHealth,
  getLogs,
  getProjectDefinitions,
  getRunJournalEntries,
  getResources,
  getRun,
	getRunDataBindings,
	getRunChanges,
  getRunManifest,
  getRunMarks,
  getActiveRunSummaries,
  getRunSummaries,
  getStats,
  localResourceDefaults,
  refreshResource,
  restoreRun,
  runMarkAttachmentBlobUrl,
  saveBookmark,
  saveResource,
  statusCheck,
  testResource,
} from "./api";
import { makeT, type I18nKey } from "./i18n";
import { useAppStore } from "./store";
import type {
  Artifact,
  ArtifactCollection,
  ConfirmState,
  ExecEvent,
  GPUInfo,
  MetricPoint,
	Locale,
	Paginated,
  ParamPoint,
  ParsedEvents,
  ProgressPoint,
  ProjectDefinition,
  Resource,
  Run,
  RunBookmark,
  RunMark
} from "./types";
import { runStatusPresentation } from "./runStatus";
import { RunObservationMeta } from "./RunObservationMeta";
import {
  filterExecs,
  filterRuns,
  fmtDuration,
  fmtMB,
  fmtShortTime,
  fmtTime,
  isActiveRun,
  isCompareEligible,
  parseGPUs,
  parseJSON,
  runGPU,
  runTitle,
  text,
  uiEventsPath
} from "./utils";
import { parseEventLines, summarizeMetricFamilies, summarizeProgress, type ProgressSummary } from "./events";
import { logSnapshotError } from "./logs";
import { replaceRunInPage } from "./runCache";
import { useLiveLog, type LiveLogState } from "./useLiveLog";
import { Modal } from "./Modal";
import { metricSeriesColor } from "./metricColors";
import { catchUpRunChanges, readRunChangeStream, seedRunChangeCheckpoint, type RunChangeCheckpoint } from "./runChanges";
import { applyRunChange, invalidateRunQueriesOnReturn, runListEnabledForTab, runSummaryKeys } from "./runSync";
import { projectRunFilterOptions, projectScopeFromFilterValue } from "./projectFilters";
import { parseProjectRoute } from "./projectRoute";
import { useDebouncedValue } from "./useDebouncedValue";
import { artifactPage, artifactRequestLimit, initialArtifactRows } from "./artifactPagination";

type Tab = "dashboard" | "resources" | "dataCenter" | "launchpad" | "projects" | "journal" | "literature" | "projectAssets" | "matrices" | "evidence" | "runs" | "favorites" | "execs" | "settings";

const MetricChart = lazy(() => import("./MetricChart").then((module) => ({ default: module.MetricChart })));
const CompareModal = lazy(() => import("./CompareModal").then((module) => ({ default: module.CompareModal })));
const EvidenceChainBoard = lazy(() => import("./EvidenceChainBoard").then((module) => ({ default: module.EvidenceChainBoard })));
const ExperimentMatrixPage = lazy(() => import("./ExperimentMatrixPage").then((module) => ({ default: module.ExperimentMatrixPage })));
const ProjectLaunchpadPage = lazy(() => import("./ProjectLaunchpadPage").then((module) => ({ default: module.ProjectLaunchpadPage })));
const ProjectAssetsPage = lazy(() => import("./ProjectAssetsPage").then((module) => ({ default: module.ProjectAssetsPage })));
const ProjectJournalPage = lazy(() => import("./ProjectJournalPage").then((module) => ({ default: module.ProjectJournalPage })));
const ProjectLiteraturePage = lazy(() => import("./ProjectLiteraturePage").then((module) => ({ default: module.ProjectLiteraturePage })));
const DataCenterPage = lazy(() => import("./DataCenterPage").then((module) => ({ default: module.DataCenterPage })));
const EvidenceFreezePanel = lazy(() => import("./EvidenceFreezePanel").then((module) => ({ default: module.EvidenceFreezePanel })));
const SettingsPage = lazy(() => import("./SettingsPage").then((module) => ({ default: module.SettingsPage })));

interface RunProjectMeta {
  projectId: string;
  projectName: string;
}

function displayError(cause: unknown): string {
	if (cause instanceof ApiError) return cause.details || cause.message;
	return cause instanceof Error ? cause.message : String(cause);
}

const runPageSize = 20;
const execPageSize = 100;

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
	const runChangeCheckpoint = useRef<RunChangeCheckpoint>({ cursor: 0 });

  const [tab, setTab] = useState<Tab>(readInitialTab());
  const [tokenDraft, setTokenDraft] = useState(token);
  const [tokenEditorOpen, setTokenEditorOpen] = useState(false);
  const [manualRefreshing, setManualRefreshing] = useState(false);
  const [runPage, setRunPage] = useState(0);
  const [execPage, setExecPage] = useState(0);
  const [runTrash, setRunTrash] = useState(false);
  const [runStatus, setRunStatus] = useState("");
  const [runResource, setRunResource] = useState("");
  const [runKind, setRunKind] = useState("experiments");
  const [runProject, setRunProject] = useState(() => window.location.pathname.endsWith("/runs") ? readProjectFromPath() : "");
  const [runQuery, setRunQuery] = useState("");
  const [execResource, setExecResource] = useState("");
  const [execActor, setExecActor] = useState("");
  const [execQuery, setExecQuery] = useState("");
  const [projectQuery, setProjectQuery] = useState("");
  const [evidenceProjectId, setEvidenceProjectId] = useState(readEvidenceProjectFromPath);
  const [projectDetailID, setProjectDetailID] = useState(readProjectFromPath);
  const [favoriteQuery, setFavoriteQuery] = useState("");
  const [detailRunId, setDetailRunId] = useState<string | null>(readDeepLinkRun());
  const [resourceForm, setResourceForm] = useState<Partial<Resource> | null>(null);
  const [confirm, setConfirm] = useState<ConfirmState | null>(null);
  const [compareOpen, setCompareOpen] = useState(false);
  const runProjectScope = projectScopeFromFilterValue(runProject);
	const debouncedRunQuery = useDebouncedValue(runQuery.trim(), 300);

	const needsRuns = runListEnabledForTab(tab);
	const needsProjects = tab === "projects" || tab === "journal" || tab === "literature" || tab === "projectAssets" || tab === "evidence" || tab === "runs" || tab === "favorites" || Boolean(detailRunId);
	const needsResources = tab === "dashboard" || tab === "resources" || tab === "dataCenter" || tab === "launchpad" || Boolean(detailRunId);
	const needsExecs = tab === "dashboard" || tab === "execs";
  const health = useQuery({ queryKey: ["health"], queryFn: getHealth, refetchInterval: 30_000, retry: 1 });
  const stats = useQuery({ queryKey: ["stats", token], queryFn: () => getStats(token), enabled: tab === "dashboard", refetchInterval: 5000 });
  const resources = useQuery({ queryKey: ["resources", token], queryFn: () => getResources(token), enabled: needsResources, refetchInterval: 5000 });
  const bookmarks = useQuery({ queryKey: ["bookmarks", token], queryFn: () => getBookmarks(token), enabled: tab === "runs" || tab === "favorites", refetchInterval: 30000 });
  const projects = useQuery({ queryKey: ["project-definitions", token], queryFn: () => getProjectDefinitions(token), enabled: needsProjects, refetchInterval: 30000 });
  const activeRunSummaries = useQuery({
	queryKey: runSummaryKeys.active(token),
	queryFn: () => getActiveRunSummaries(token, 100),
	refetchInterval: 30_000,
	refetchOnWindowFocus: "always"
  });
  const runs = useQuery({
    queryKey: runSummaryKeys.list(token, runPage, runStatus, runResource, runTrash, runProjectScope, runKind, debouncedRunQuery),
    queryFn: () =>
      getRunSummaries(token, {
        limit: runPageSize,
        offset: runPage * runPageSize,
        status: runStatus,
        resource: runResource,
        projectScope: runProjectScope,
        query: debouncedRunQuery,
        kindGroup: runKind,
        trash: runTrash,
        refresh: false
      }),
    enabled: needsRuns,
	refetchInterval: 30_000,
	refetchOnWindowFocus: "always",
	placeholderData: keepPreviousData
  });
	const runSyncCursor = needsRuns ? runs.data?.change_cursor : activeRunSummaries.data?.change_cursor;
	const runChangeCatchup = useQuery({
	queryKey: ["run-change-catchup", token, runSyncCursor ?? "waiting"],
	queryFn: () => {
		runChangeCheckpoint.current = seedRunChangeCheckpoint(runChangeCheckpoint.current, runSyncCursor);
		return catchUpRunChanges(runChangeCheckpoint.current, (cursor, updatedSince) => getRunChanges(token, cursor, updatedSince));
	},
	enabled: runSyncCursor != null,
	refetchInterval: 30_000,
	refetchOnWindowFocus: "always"
	});
  const execs = useQuery({
    queryKey: ["execs", token, tab === "dashboard" ? "dashboard" : "page", execPage, execResource, execActor],
    queryFn: () => getExecEvents(token, {
      limit: tab === "dashboard" ? 3 : execPageSize,
      offset: tab === "dashboard" ? 0 : execPage * execPageSize,
      resource_id: execResource,
      actor: execActor
    }),
    enabled: needsExecs,
    refetchInterval: 15000
  });

  const resourceList = resources.data || [];
  const runList = runs.data?.items || [];
  const bookmarkList = bookmarks.data || [];
  const resourceById = useMemo(() => new Map(resourceList.map((r) => [r.id, r])), [resourceList]);
  const projectList = projects.data || [];
  const selectedProject = projectList.find((project) => project.id === projectDetailID);
  const projectWorkspace = Boolean(projectDetailID);
  const projectWorkspaceName = selectedProject?.name || projectDetailID;
  const runProjectById = useMemo(() => buildRunProjectIndex(projectList, runList), [projectList, runList]);
  const visibleRuns = useMemo(() => {
    if (runKind === "favorites") return filterRuns(runList, { kind: "favorites", bookmarks: bookmarkList });
    return runList;
  }, [runList, runKind, bookmarkList]);
  const activeRuns = activeRunSummaries.data?.items || [];
  const detailRunPreview = useMemo(
    () => detailRunId ? runList.find((run) => run.id === detailRunId) || activeRuns.find((run) => run.id === detailRunId) : undefined,
    [activeRuns, detailRunId, runList]
  );
  const visibleExecs = useMemo(() => filterExecs(execs.data?.items || [], execQuery), [execs.data, execQuery]);
  const favoriteRuns = useMemo(() => filterRuns(runList, { query: favoriteQuery, kind: "favorites", bookmarks: bookmarkList }), [runList, favoriteQuery, bookmarkList]);
  const visibleProjects = useMemo(() => filterProjects(projectList, projectQuery), [projectList, projectQuery]);
  const runProjectOptions = useMemo(
    () => [
      ["", t("allProjects")] as [string, string],
      ...projectRunFilterOptions(projectList)
    ],
    [projectList, t]
  );
  const selectedRuns = useMemo(() => runList.filter((run) => selectedRunIds.has(run.id) && isCompareEligible(run)), [runList, selectedRunIds]);

	useEffect(() => {
		if (runSyncCursor != null && runChangeCheckpoint.current.cursor === 0) runChangeCheckpoint.current.cursor = runSyncCursor;
	}, [runSyncCursor]);

	useEffect(() => {
		if (!runChangeCatchup.data) return;
		for (const change of runChangeCatchup.data.changes) applyRunChange(queryClient, token, change);
		runChangeCheckpoint.current = runChangeCatchup.data.checkpoint;
	}, [queryClient, runChangeCatchup.data, token]);

  const refreshAll = async () => {
    if (manualRefreshing) return;
    setManualRefreshing(true);
    try {
      await queryClient.invalidateQueries({ type: "active" });
    } finally {
      setManualRefreshing(false);
    }
  };

  const setActiveTab = (next: Tab) => {
    if (next === "projects" || next === "runs" || next === "settings") setProjectDetailID("");
    if (next === "runs") {
      setRunProject("");
      setRunPage(0);
    }
    setTab(next);
    history.replaceState(null, "", pathForTab(next, evidenceProjectId, projectDetailID));
  };

  const openProjectJournal = (projectID: string, options?: { runID?: string; compose?: boolean; entryID?: string }) => {
    setProjectDetailID(projectID);
    setTab("journal");
    const params = new URLSearchParams();
    if (options?.runID) params.set("run", options.runID);
    if (options?.compose) params.set("compose", "1");
    if (options?.entryID) params.set("entry", options.entryID);
    const search = params.toString();
    history.replaceState(null, "", `${projectJournalPath(projectID)}${search ? `?${search}` : ""}`);
  };

  const openProjectRuns = (projectID: string) => {
    setProjectDetailID(projectID);
    setRunProject(projectID);
    setRunPage(0);
    setTab("runs");
    history.replaceState(null, "", projectRunsPath(projectID));
  };

  const openProjectLiterature = (projectID: string) => {
    setProjectDetailID(projectID);
    setTab("literature");
    history.replaceState(null, "", projectLiteraturePath(projectID));
  };

  const openProjectAssets = (projectID: string) => {
    setProjectDetailID(projectID);
    setTab("projectAssets");
    history.replaceState(null, "", projectAssetsPath(projectID));
  };

  const openProjectResearchGraph = (projectID: string) => {
    setProjectDetailID(projectID);
    setEvidenceProjectId(projectID);
    setTab("evidence");
    history.replaceState(null, "", projectResearchGraphPath(projectID));
  };

  useEffect(() => {
    const syncFromPath = () => {
      const evidenceID = readEvidenceProjectFromPath();
      const projectID = readProjectFromPath();
      setEvidenceProjectId(evidenceID);
      setProjectDetailID(projectID);
      if (window.location.pathname.endsWith("/runs") && projectID) {
        setRunProject(projectID);
        setRunPage(0);
      }
      setTab(readInitialTab());
    };
    window.addEventListener("popstate", syncFromPath);
    return () => window.removeEventListener("popstate", syncFromPath);
  }, []);

  useEffect(() => {
    if (window.location.pathname.startsWith("/ui-v2/evidence-chains")) {
      history.replaceState(null, "", pathForTab("projects"));
    }
  }, []);

	useEffect(() => {
		const initialCursor = runSyncCursor;
		if (initialCursor == null) return;
		let stopped = false;
		let cursor = Math.max(initialCursor, runChangeCheckpoint.current.cursor);
		let controller: AbortController | null = null;
		let reconnectNow = false;
		const wait = (ms: number) => new Promise<void>((resolve) => window.setTimeout(resolve, ms));
		const connect = async () => {
			let retry = 0;
			while (!stopped) {
				controller = new AbortController();
				try {
					cursor = await readRunChangeStream(token, cursor, (change) => {
						cursor = Math.max(cursor, change.seq);
						runChangeCheckpoint.current.cursor = Math.max(runChangeCheckpoint.current.cursor, change.seq);
						applyRunChange(queryClient, token, change);
					}, controller.signal);
					retry = 0;
					if (!stopped) await wait(1000);
				} catch {
					if (stopped) return;
					void queryClient.invalidateQueries({ queryKey: runSummaryKeys.active(token) });
					if (!reconnectNow) await wait(Math.min(10_000, 1000 * 2 ** retry++));
					reconnectNow = false;
				}
			}
		};
		void connect();
		const refreshOnReturn = () => {
			if (!invalidateRunQueriesOnReturn(queryClient, token, detailRunId, document.visibilityState)) return;
			reconnectNow = true;
			controller?.abort();
		};
		window.addEventListener("online", refreshOnReturn);
		window.addEventListener("pageshow", refreshOnReturn);
		document.addEventListener("visibilitychange", refreshOnReturn);
		return () => {
			stopped = true;
			controller?.abort();
			window.removeEventListener("online", refreshOnReturn);
			window.removeEventListener("pageshow", refreshOnReturn);
			document.removeEventListener("visibilitychange", refreshOnReturn);
		};
	}, [runSyncCursor, detailRunId, queryClient, token]);

  const authError = [stats.error, resources.error, activeRunSummaries.error, runs.error, projects.error, bookmarks.error, execs.error].find(
    (err) => err instanceof ApiError && err.status === 401
  );

  useEffect(() => {
    if (authError) setTokenEditorOpen(true);
  }, [authError]);

  function askConfirm(next: ConfirmState) {
    setConfirm(next);
  }

  async function invalidateOperationalData() {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["resources"] }),
      queryClient.invalidateQueries({ queryKey: ["run-summaries"] }),
      queryClient.invalidateQueries({ queryKey: ["marks"] }),
      queryClient.invalidateQueries({ queryKey: ["bookmarks"] }),
      queryClient.invalidateQueries({ queryKey: ["projects"] }),
      queryClient.invalidateQueries({ queryKey: ["manual-project-categories"] }),
      queryClient.invalidateQueries({ queryKey: ["manual-run-project-assignments"] }),
      queryClient.invalidateQueries({ queryKey: ["stats"] })
    ]);
  }

  return (
    <div className={projectWorkspace ? "app-shell project-app-shell" : "app-shell global-app-shell"}>
      {!projectWorkspace ? <aside className="side-nav">
        <div className="brand">
          <img src="/ui-v2/researchos-icon.png" alt="" />
          <div>
            <strong>ResearchOS</strong>
            <span>{t("uiV2")}</span>
          </div>
        </div>
        <nav>
          <NavButton active={tab === "projects" || tab === "journal" || tab === "literature" || tab === "projectAssets" || tab === "evidence" || (tab === "runs" && Boolean(projectDetailID))} icon={<Database />} label={t("projects")} onClick={() => setActiveTab("projects")} />
          <NavButton active={(tab === "runs" || tab === "favorites") && !projectDetailID} icon={<PlayCircle />} label={t("activeRuns")} onClick={() => setActiveTab("runs")} />
          <NavButton
            active={["settings", "resources", "dataCenter", "launchpad", "execs"].includes(tab)}
            icon={<Settings />}
            label={t("settings")}
            onClick={() => setActiveTab("settings")}
          />
        </nav>
      </aside> : null}

      <main className={projectWorkspace ? "workspace project-workspace" : "workspace global-workspace"}>
        {!projectWorkspace ? <header className="topbar">
          <div>
            <h1>{labelForTab(tab, t)}</h1>
            <p className={authError || health.isError ? "connection-copy bad" : "connection-copy"}>
              {authError
                ? t("invalidToken")
                : health.isError
                  ? t("serviceOffline")
                  : health.data
                    ? `${t("serviceReady")} · ${health.data.hostname}`
                    : t("loading")}
            </p>
          </div>
          <div className="topbar-actions">
            <ConnectionBadge
              label={manualRefreshing ? t("refreshing") : health.data?.status === "ok" ? t("connected") : health.isError ? t("offline") : t("connecting")}
              tone={health.isError ? "bad" : health.data?.status === "ok" ? "good" : "pending"}
            />
            <button className={tokenEditorOpen ? "icon-button active-soft" : "icon-button"} title={t("editToken")} aria-expanded={tokenEditorOpen} onClick={() => setTokenEditorOpen((open) => !open)}>
              <KeyRound size={16} />
            </button>
            <button className="icon-button" title="Language" onClick={() => setLocale(locale === "zh" ? "en" : "zh")}>
              <Languages size={17} />
            </button>
            <button className="icon-button" title={t("refresh")} disabled={manualRefreshing} aria-busy={manualRefreshing} onClick={() => void refreshAll()}>
              <RefreshCcw size={17} className={manualRefreshing ? "spin" : ""} />
            </button>
            {tokenEditorOpen ? <form
              className="token-form"
              onSubmit={(event) => {
                event.preventDefault();
                setToken(tokenDraft.trim());
                setTokenEditorOpen(false);
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
                    setTokenEditorOpen(false);
                  }}
                >
                  {t("clear")}
                </button>
              ) : null}
            </form> : null}
          </div>
        </header> : null}

        {projectWorkspace ? (
          <header className="project-header">
            <button className="project-exit" type="button" onClick={() => setActiveTab("projects")}>
              <ChevronLeft size={16} />
              <span>{locale === "zh" ? "退出项目" : "Exit project"}</span>
            </button>
            <strong className="project-header-name" title={projectWorkspaceName}>{projectWorkspaceName}</strong>
            <nav className="project-header-tabs" aria-label={`${projectWorkspaceName} Project`}>
            <button className={tab === "journal" ? "active" : ""} onClick={() => openProjectJournal(projectDetailID)}>{t("journal")}</button>
            <button className={tab === "literature" ? "active" : ""} onClick={() => openProjectLiterature(projectDetailID)}>{t("literature")}</button>
            <button className={tab === "runs" ? "active" : ""} onClick={() => openProjectRuns(projectDetailID)}>{t("runs")}</button>
            <button className={tab === "projectAssets" ? "active" : ""} onClick={() => openProjectAssets(projectDetailID)}>{t("assets")}</button>
            <button className={tab === "evidence" ? "active" : ""} onClick={() => openProjectResearchGraph(projectDetailID)}>{t("evidenceChains")}</button>
            </nav>
            <div className="project-header-actions">
              <ConnectionBadge
                label={manualRefreshing ? t("refreshing") : health.data?.status === "ok" ? t("connected") : health.isError ? t("offline") : t("connecting")}
                tone={health.isError ? "bad" : health.data?.status === "ok" ? "good" : "pending"}
                compact
              />
              <button className="icon-button" type="button" title="Language" onClick={() => setLocale(locale === "zh" ? "en" : "zh")}>
                <Languages size={17} />
              </button>
              <button className="icon-button" type="button" title={t("refresh")} disabled={manualRefreshing} aria-busy={manualRefreshing} onClick={() => void refreshAll()}>
                <RefreshCcw size={17} className={manualRefreshing ? "spin" : ""} />
              </button>
            </div>
          </header>
        ) : null}

        {["settings", "resources", "dataCenter", "launchpad", "execs"].includes(tab) ? (
          <nav className="settings-section-nav" aria-label={t("settings")}>
            <button className={tab === "resources" ? "active" : ""} onClick={() => setActiveTab("resources")}><Server size={15} />{t("resources")}</button>
            <button className={tab === "dataCenter" ? "active" : ""} onClick={() => setActiveTab("dataCenter")}><HardDrive size={15} />{t("dataCenter")}</button>
            <button className={tab === "launchpad" ? "active" : ""} onClick={() => setActiveTab("launchpad")}><Cpu size={15} />{t("launchpad")}</button>
            <button className={tab === "execs" ? "active" : ""} onClick={() => setActiveTab("execs")}><Terminal size={15} />{t("execs")}</button>
            <button className={tab === "settings" ? "active" : ""} onClick={() => setActiveTab("settings")}><Settings size={15} />{t("settings")}</button>
          </nav>
        ) : null}

        <AnimatePresence mode="wait">
          <motion.section
            className={[
              "app-page",
              projectWorkspace ? "project-page" : "",
              projectWorkspace && tab === "evidence" ? "project-page-evidence" : ""
            ].filter(Boolean).join(" ")}
            key={tab}
            initial={{ opacity: 0, y: 8 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -8 }}
            transition={{ duration: 0.16 }}
          >
            <Suspense fallback={<PageLoading locale={locale} />}>
            {tab === "dashboard" && (
              <Dashboard
                t={t}
				locale={locale}
                stats={stats.data}
                resources={resourceList}
                activeRuns={activeRuns}
                execs={visibleExecs.slice(0, 8)}
                resourceById={resourceById}
                onOpenRun={setDetailRunId}
                onOpenResources={() => setActiveTab("resources")}
              />
            )}
            {tab === "resources" && (
              <ResourcesTab
                t={t}
                token={token}
                resources={resourceList}
				loading={resources.isPending}
				error={resources.isError ? displayError(resources.error) : null}
				refreshing={resources.isFetching && !resources.isPending}
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
            {tab === "projects" && (
              projectWorkspace && !selectedProject ? (
                <div className="project-route-state">
                  <AsyncState
                    label={
                      projects.isPending
                        ? t("loading")
                        : projects.error
                          ? displayError(projects.error)
                          : locale === "zh"
                            ? `没有找到项目 ${projectDetailID}`
                            : `Project ${projectDetailID} was not found`
                    }
                    tone={projects.isPending ? undefined : "bad"}
                  />
                </div>
              ) : (
                <ProjectsTab
                  t={t}
                  projects={visibleProjects}
                  selectedProject={selectedProject}
                  query={projectQuery}
                  setQuery={setProjectQuery}
                  onOpenProject={openProjectJournal}
                  onOpenGraph={openProjectResearchGraph}
                  onOpenRuns={openProjectRuns}
                  onOpenAssets={openProjectAssets}
                  onOpenLiterature={openProjectLiterature}
                  onManage={() => setActiveTab("launchpad")}
                />
              )
            )}
            {tab === "journal" && selectedProject ? (
              <ProjectJournalPage
                token={token}
                locale={locale}
                project={selectedProject}
                onOpenRun={setDetailRunId}
              />
            ) : null}
            {tab === "journal" && projectWorkspace && !selectedProject ? (
              <div className="project-route-state">
                <AsyncState
                  label={projects.isPending
                    ? t("loading")
                    : projects.error
                      ? displayError(projects.error)
                      : locale === "zh"
                        ? `没有找到项目 ${projectDetailID}`
                        : `Project ${projectDetailID} was not found`}
                  tone={projects.isPending ? undefined : "bad"}
                />
              </div>
            ) : null}
            {tab === "literature" && selectedProject ? (
              <ProjectLiteraturePage
                token={token}
                locale={locale}
                project={selectedProject}
                onOpenJournal={(entryID) => openProjectJournal(selectedProject.id, { entryID })}
              />
            ) : null}
            {tab === "projectAssets" && projectDetailID ? <ProjectAssetsPage token={token} projectId={projectDetailID} /> : null}
			{tab === "dataCenter" && <DataCenterPage token={token} locale={locale} resources={resourceList} />}
            {tab === "settings" && <SettingsPage token={token} locale={locale} />}
            {tab === "launchpad" && <ProjectLaunchpadPage token={token} locale={locale} resources={resourceList} onOpenRun={setDetailRunId} />}
            {tab === "matrices" && <ExperimentMatrixPage token={token} t={t} onOpenRun={setDetailRunId} />}
            {tab === "evidence" && (
              <EvidenceChainBoard
                token={token}
                t={t}
                onOpenRun={setDetailRunId}
                projectId={evidenceProjectId}
              />
            )}
            {tab === "runs" && (
              <RunsTab
                t={t}
				locale={locale}
                resources={resourceList}
                runs={visibleRuns}
				loading={runs.isPending}
				error={runs.isError ? displayError(runs.error) : null}
				refreshing={runs.isFetching && !runs.isPending}
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
                lockedProject={projectDetailID || undefined}
                setProject={(value) => {
                  setRunProject(value);
                  setRunPage(0);
                  if (projectDetailID) {
                    const nextProjectID = projectScopeFromFilterValue(value);
                    setProjectDetailID(nextProjectID);
                    history.replaceState(null, "", nextProjectID ? projectRunsPath(nextProjectID) : "/ui-v2/runs");
                  }
                }}
                projectOptions={runProjectOptions}
                runProjectById={runProjectById}
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
              <FavoritesTab t={t} locale={locale} runs={favoriteRuns} bookmarks={bookmarkList} query={favoriteQuery} setQuery={setFavoriteQuery} resourceById={resourceById} onOpenRun={setDetailRunId} />
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
            </Suspense>
          </motion.section>
        </AnimatePresence>
      </main>

      {detailRunId ? (
        <RunDetail
          t={t}
		  locale={locale}
          token={token}
          runId={detailRunId}
          preview={detailRunPreview}
          resourceById={resourceById}
          projects={projectList}
          onClose={() => {
            setDetailRunId(null);
            const returnPath = tab === "runs" && projectDetailID && runProject === projectDetailID
              ? projectRunsPath(projectDetailID)
              : pathForTab(tab, evidenceProjectId, projectDetailID);
            history.replaceState(null, "", returnPath);
          }}
          onOpenProjectJournal={(run, compose) => {
            if (!run.project_id) return;
            setDetailRunId(null);
            openProjectJournal(run.project_id, { runID: run.id, compose });
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
			const updated = await statusCheck(token, run.id);
			queryClient.setQueryData(["run", token, run.id], updated);
			queryClient.setQueriesData<Paginated<Run>>({ queryKey: ["run-summaries", token, "list"] }, (page) => replaceRunInPage(page, updated));
			await queryClient.invalidateQueries({ queryKey: runSummaryKeys.active(token) });
			await Promise.all([
			  queryClient.invalidateQueries({ queryKey: ["stats", token] }),
			  queryClient.invalidateQueries({ queryKey: ["resources", token] }),
			  queryClient.invalidateQueries({ queryKey: ["projects", token] })
			]);
			return updated;
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

      {compareOpen ? <Suspense fallback={null}><CompareModal t={t} token={token} runs={selectedRuns} onClose={() => setCompareOpen(false)} /></Suspense> : null}
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

function ConnectionBadge({ label, tone, compact = false }: { label: string; tone: "good" | "bad" | "pending"; compact?: boolean }) {
  return (
    <span className={`connection-badge ${tone} ${compact ? "compact" : ""}`} role="status">
      <i aria-hidden="true" />
      <span>{label}</span>
    </span>
  );
}

function PageLoading({ locale }: { locale: Locale }) {
  return (
    <div className="page-loading" role="status" aria-live="polite">
      <RefreshCcw className="spin" size={18} />
      <div>
        <strong>{locale === "zh" ? "正在打开工作区" : "Opening workspace"}</strong>
        <span>{locale === "zh" ? "仅加载当前页面需要的模块" : "Loading only the modules this page needs"}</span>
      </div>
    </div>
  );
}

function Dashboard({
  t,
	locale,
  stats,
  resources,
  activeRuns,
  execs,
  resourceById,
  onOpenRun,
  onOpenResources
}: {
  t: T;
	locale: Locale;
  stats?: { total_resources: number; active_runs: number; total_runs: number };
  resources: Resource[];
  activeRuns: Run[];
  execs: ExecEvent[];
  resourceById: Map<string, Resource>;
  onOpenRun: (id: string) => void;
  onOpenResources: () => void;
}) {
  const shownResources = resources.slice(0, 5);
  const moreResources = resources.slice(5);
  return (
    <div className="stack">
      <div className="stat-grid">
        <Stat label={t("resources")} value={stats?.total_resources ?? resources.length} icon={<Server />} />
        <Stat label={t("activeRuns")} value={stats?.active_runs ?? activeRuns.length} icon={<Activity />} />
        <Stat label={t("totalRuns")} value={stats?.total_runs ?? "-"} icon={<BarChart3 />} />
      </div>
      <Section title={t("resources")}>
        <div className="resource-grid">
          {resources.length ? shownResources.map((r) => <ResourceCard key={r.id} resource={r} t={t} />) : <Empty t={t} />}
          {moreResources.length ? (
            <button type="button" className="overview-more" onClick={onOpenResources}>
              <strong>+{moreResources.length}</strong>
              <span className="overview-more-names">{moreResources.map((r) => r.name).join("、")}</span>
              <span className="overview-more-cta">{t("resourceMore")}</span>
            </button>
          ) : null}
        </div>
      </Section>
      <Section title={t("activeRuns")}>
        <div className="run-card-grid dashboard-runs">{activeRuns.length ? activeRuns.map((run) => <RunCard key={run.id} run={run} locale={locale} resourceById={resourceById} onOpen={() => onOpenRun(run.id)} />) : <Empty t={t} />}</div>
      </Section>
      <Section title={t("recentExec")}>
        <div className="compact-list">{execs.slice(0, 3).map((event) => <ExecCompact key={event.id} event={event} resourceById={resourceById} />)}</div>
      </Section>
    </div>
  );
}

function ResourcesTab({
  t,
  token: _token,
  resources,
	loading,
	error,
	refreshing,
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
	loading: boolean;
	error: string | null;
	refreshing: boolean;
  onEdit: (resource: Resource) => void;
  onRefresh: (id: string) => Promise<void>;
  onTest: (id: string) => Promise<void>;
  onDelete: (resource: Resource) => void;
  onAdd: () => void;
  onFillLocal: () => Promise<void>;
}) {
	const [busyByID, setBusyByID] = useState<Record<string, { action: "refresh" | "test"; error?: string } | undefined>>({});
	async function runAction(id: string, action: "refresh" | "test", fn: (id: string) => Promise<void>) {
		setBusyByID((current) => ({ ...current, [id]: { action } }));
		try {
			await fn(id);
			setBusyByID((current) => ({ ...current, [id]: undefined }));
		} catch (cause) {
			setBusyByID((current) => ({ ...current, [id]: { action, error: displayError(cause) } }));
		}
	}
  return (
    <div className="stack">
	  {loading ? <AsyncState label={t("loading")} /> : null}
	  {error ? <AsyncState label={error} tone="bad" /> : null}
	  {refreshing && !loading ? <AsyncState label={t("refreshing")} compact /> : null}
      <div className="toolbar">
        <button className="primary" onClick={onAdd}>
          <Server size={16} />
          {t("addResource")}
        </button>
        <button onClick={() => void onFillLocal()}>{t("fillLocal")}</button>
      </div>
      <div className="resource-grid manage-resource-grid">
        {!loading && resources.length ? (
          resources.map((resource) => (
            <ResourceCard
              key={resource.id}
              resource={resource}
              t={t}
              actions={
                <>
				  {busyByID[resource.id]?.error ? <span className="action-error">{busyByID[resource.id]?.error}</span> : null}
                  <button className="action-button" title={t("refresh")} disabled={Boolean(busyByID[resource.id])} aria-busy={busyByID[resource.id]?.action === "refresh"} onClick={() => void runAction(resource.id, "refresh", onRefresh)}>
                    <RefreshCcw size={15} className={busyByID[resource.id]?.action === "refresh" ? "spin" : ""} />
                    {busyByID[resource.id]?.action === "refresh" ? t("refreshing") : t("refresh")}
                  </button>
                  <button className="action-button primary-action" title={t("test")} disabled={Boolean(busyByID[resource.id])} aria-busy={busyByID[resource.id]?.action === "test"} onClick={() => void runAction(resource.id, "test", onTest)}>
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
		) : !loading ? (
          <Empty t={t} />
		) : null}
      </div>
    </div>
  );
}

function RunsTab(props: {
  t: T;
	locale: Locale;
  resources: Resource[];
  runs: Run[];
	loading: boolean;
	error: string | null;
	refreshing: boolean;
  total: number;
  page: number;
  setPage: (page: number) => void;
  status: string;
  setStatus: (status: string) => void;
  resource: string;
  setResource: (resource: string) => void;
  project: string;
  lockedProject?: string;
  setProject: (project: string) => void;
  projectOptions: [string, string][];
  runProjectById: Map<string, RunProjectMeta>;
  kind: string;
  setKind: (kind: string) => void;
  trash: boolean;
  setTrash: (trash: boolean) => void;
  query: string;
  setQuery: (query: string) => void;
  bookmarks: RunBookmark[];
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
  const bookmarkByRun = useMemo(() => new Map(props.bookmarks.map((bookmark) => [bookmark.run_id, bookmark])), [props.bookmarks]);
  const selectedCount = props.runs.filter((run) => selectedRunIds.has(run.id)).length;
  const hasFilters = Boolean(props.status || props.resource || (!props.lockedProject && props.project) || props.kind !== "experiments" || props.query);
  const renderRun = (run: Run) => {
    const bookmark = bookmarkByRun.get(run.id);
    return (
      <RunListCard
        key={run.id}
        run={run}
		locale={props.locale}
        resourceById={props.resourceById}
        projectMeta={props.runProjectById.get(run.id)}
        hideProject={Boolean(props.lockedProject)}
        bookmark={bookmark}
        selected={selectedRunIds.has(run.id)}
        trash={props.trash}
        onOpen={() => props.onOpenRun(run.id)}
        onSelect={(checked) => toggleSelectedRun(run, checked)}
        onToggleBookmark={() => void props.onToggleBookmark(run, !!bookmark)}
        onArchive={() => props.onArchive(run)}
        onRestore={() => void props.onRestore(run)}
        onDelete={() => props.onDelete(run)}
        t={props.t}
      />
    );
  };
  return (
    <div className="stack">
	  {props.loading ? <AsyncState label={props.t("loading")} /> : null}
	  {props.error ? <AsyncState label={props.error} tone="bad" /> : null}
      <div className="toolbar dense run-filter-toolbar">
        <Segmented
          value={props.trash ? "trash" : "main"}
          options={[
            { value: "main", label: props.t("mainRuns") },
            { value: "trash", label: props.t("trash") }
          ]}
          onChange={(value) => props.setTrash(value === "trash")}
        />
        <label className="filter-field"><span>{props.t("status")}</span><Select value={props.status} onChange={props.setStatus} options={[["", props.t("allStatuses")], ["queued", "queued"], ["preflighting", "preflighting"], ["starting", "starting"], ["running", "running"], ["succeeded", "succeeded"], ["failed", "failed"], ["cancelled", "cancelled"], ["ssh_unreachable", "ssh_unreachable"], ["container_expired", "container_expired"], ["run_lost_but_events_cached", "lost + cached"]]} /></label>
        <label className="filter-field"><span>{props.t("resource")}</span><Select value={props.resource} onChange={props.setResource} options={[["", props.t("allResources")], ...props.resources.map((r) => [r.id, r.name] as [string, string])]} /></label>
        {props.lockedProject ? null : <label className="filter-field"><span>{props.t("projects")}</span><Select value={props.project} onChange={props.setProject} options={props.projectOptions} /></label>}
        <label className="filter-field"><span>{props.t("kind")}</span><Select value={props.kind} onChange={props.setKind} options={[["experiments", props.t("experiments")], ["tools", props.t("toolTasks")], ["all", props.t("allKinds")]]} /></label>
        <label className="filter-field filter-search"><span>{props.t("search")}</span><input value={props.query} onChange={(event) => props.setQuery(event.target.value)} placeholder={props.t("searchRuns")} /></label>
        {hasFilters ? <button className="ghost-button clear-run-filters" type="button" onClick={() => {
          props.setStatus("");
          props.setResource("");
          if (!props.lockedProject) props.setProject("");
          props.setKind("experiments");
          props.setQuery("");
        }}>{props.t("clearFilters")}</button> : null}
        <button disabled={!selectedCount} onClick={props.onCompare}>
          <BarChart3 size={16} />
          {props.t("compare")} ({selectedCount})
        </button>
      </div>
	  {!props.loading ? <div className="run-list-context" role="status" aria-live="polite">
	    <span>{props.total} {props.t("results")}</span>
	    {props.refreshing ? <span><RefreshCcw className="spin" size={13} />{props.t("updatingResults")}</span> : null}
	  </div> : null}
	  {!props.loading && props.runs.length ? (
        <div className="run-list">{props.runs.map(renderRun)}</div>
	  ) : !props.loading ? (
        <div className="run-list">
          <Empty t={props.t} />
        </div>
	  ) : null}
      <Pager t={props.t} total={props.total} page={props.page} pageSize={runPageSize} setPage={props.setPage} />
    </div>
  );
}

function ProjectsTab({
  t,
  projects,
  selectedProject,
  query,
  setQuery,
  onOpenProject,
  onOpenGraph,
  onOpenRuns,
  onOpenAssets,
  onOpenLiterature,
  onManage
}: {
  t: T;
  projects: ProjectDefinition[];
  selectedProject?: ProjectDefinition;
  query: string;
  setQuery: (value: string) => void;
  onOpenProject: (projectID: string) => void;
  onOpenGraph: (projectID: string) => void;
  onOpenRuns: (projectID: string) => void;
  onOpenAssets: (projectID: string) => void;
  onOpenLiterature: (projectID: string) => void;
  onManage: () => void;
}) {
  if (selectedProject) {
    return <section className="project-overview-page">
      <span className="panel-kicker">Project Overview</span>
      <h2>{selectedProject.name}</h2>
      <code>{selectedProject.id}</code>
      <p>{selectedProject.description || "这个 Project 是 Runs、Assets 与 Evidence Map 的共同范围。"}</p>
      <dl>
        <div><dt>Local root</dt><dd>{selectedProject.local_root || "未配置"}</dd></div>
        <div><dt>Repository</dt><dd>{selectedProject.source_repo || "未配置"}</dd></div>
        <div><dt>Default recipe</dt><dd>{selectedProject.default_recipe || "未配置"}</dd></div>
        <div><dt>Release gate</dt><dd>{selectedProject.gate_command || "未配置"}</dd></div>
      </dl>
    </section>;
  }
  return (
    <div className="stack">
      <div className="toolbar project-index-toolbar">
        <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("search")} />
        <button type="button" onClick={onManage}><Settings size={15} />{t("launchpad")}</button>
      </div>
      <div className="project-index">
        {projects.length ? (
          projects.map((project) => {
            const projectTitle = project.name || project.id;
            return (
              <section className="project-index-card" key={project.id}>
                <div className="project-index-identity">
                  <span className="project-index-mark" aria-hidden="true">{projectTitle.slice(0, 1).toUpperCase()}</span>
                  <div className="project-index-copy">
                    <h2>{projectTitle}</h2>
                    {project.id !== projectTitle ? <p className="mono muted">{project.id}</p> : null}
                    <p>{project.description === "Imported from the legacy evidence-card project index."
                      ? t("projectScope")
                      : project.description || project.local_root || t("projectScope")}</p>
                  </div>
                </div>
                <button className="project-index-open primary" type="button" onClick={() => onOpenProject(project.id)}>
                  <BookOpen size={15} />
                  {t("journal")}
                  <ChevronRight size={16} />
                </button>
                <div className="project-index-meta">
                  <div className="project-index-facts">
                    {project.updated_at ? <span><Clock size={13} />{fmtShortTime(project.updated_at)}</span> : null}
                    {project.source_repo ? <span>{project.source_repo}</span> : null}
                    {project.default_recipe ? <span>recipe · {project.default_recipe}</span> : null}
                  </div>
                  <nav className="project-index-tabs" aria-label={`${projectTitle} sections`}>
                    <button className="project-destination literature" type="button" onClick={() => onOpenLiterature(project.id)}>
                    <BookOpen size={15} />
                    {t("literature")}
                    </button>
                    <button className="project-destination runs" type="button" onClick={() => onOpenRuns(project.id)}>
                    <PlayCircle size={15} />
                    {t("runs")}
                    </button>
                    <button className="project-destination assets" type="button" onClick={() => onOpenAssets(project.id)}>
                    <Archive size={15} />
                    {t("assets")}
                    </button>
                    <button className="project-destination evidence" type="button" onClick={() => onOpenGraph(project.id)}>
                    <Network size={15} />
                    {t("evidenceChains")}
                    </button>
                  </nav>
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

function FavoritesTab({ t, locale, runs, bookmarks: _bookmarks, query, setQuery, resourceById, onOpenRun }: { t: T; locale: Locale; runs: Run[]; bookmarks: RunBookmark[]; query: string; setQuery: (value: string) => void; resourceById: Map<string, Resource>; onOpenRun: (id: string) => void }) {
  return (
    <div className="stack">
      <div className="toolbar">
        <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("search")} />
      </div>
      <div className="run-card-grid">{runs.length ? runs.map((run) => <RunCard key={run.id} run={run} locale={locale} resourceById={resourceById} onOpen={() => onOpenRun(run.id)} />) : <Empty t={t} />}</div>
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
      <Pager t={props.t} total={props.total} page={props.page} pageSize={execPageSize} setPage={props.setPage} />
    </div>
  );
}

function RunDetail({
  t,
	locale,
  token,
  runId,
  preview,
  resourceById,
  projects,
  onClose,
  onCancel,
  onStatusCheck,
  onOpenProjectJournal
}: {
  t: T;
	locale: Locale;
  token: string;
  runId: string;
  preview?: Run;
  resourceById: Map<string, Resource>;
  projects: ProjectDefinition[];
  onClose: () => void;
  onCancel: (run: Run) => void;
  onStatusCheck: (run: Run) => Promise<Run>;
  onOpenProjectJournal: (run: Run, compose: boolean) => void;
}) {
  const [selectedMark, setSelectedMark] = useState<RunMark | null>(null);
  const [detailTab, setDetailTab] = useState<"overview" | "evidence" | "raw">("overview");
  const [legacyMarksOpen, setLegacyMarksOpen] = useState(false);
  const [projectBindingOpen, setProjectBindingOpen] = useState(false);
  const [projectDraft, setProjectDraft] = useState("");
  const [projectBindingNotice, setProjectBindingNotice] = useState<string | null>(null);
  const [showAllArtifacts, setShowAllArtifacts] = useState(false);
  const run = useQuery({
    queryKey: ["run", token, runId],
    queryFn: () => getRun(token, runId),
    refetchInterval: 30000,
    refetchOnWindowFocus: "always",
    placeholderData: preview
  });
  const fullRunReady = Boolean(run.data && !run.isPlaceholderData);
  const marks = useQuery({
    queryKey: ["run-marks", token, runId],
    queryFn: () => getRunMarks(token, runId),
    enabled: fullRunReady && detailTab === "raw" && legacyMarksOpen
  });
  const journalEntries = useQuery({
    queryKey: ["run-journal", token, runId],
    queryFn: () => getRunJournalEntries(token, runId),
    enabled: fullRunReady && detailTab === "overview",
    refetchInterval: 10_000,
    refetchOnWindowFocus: "always"
  });
  const artifactLimit = artifactRequestLimit(showAllArtifacts);
  const artifacts = useQuery({
    queryKey: ["artifacts", token, runId, artifactLimit],
    queryFn: () => getArtifacts(token, runId, artifactLimit),
    enabled: fullRunReady && detailTab === "overview",
    placeholderData: keepPreviousData
  });
  const artifactCollection = useQuery({
    queryKey: ["artifact-collection", token, runId],
    queryFn: () => getArtifactCollection(token, runId),
    enabled: fullRunReady && detailTab === "overview",
    refetchInterval: (query) => query.state.data?.state === "discovering" ? 1500 : 8000
  });
  const manifest = useQuery({ queryKey: ["run-manifest", token, runId], queryFn: () => getRunManifest(token, runId), enabled: fullRunReady && detailTab === "overview", retry: false });
	const dataBindings = useQuery({ queryKey: ["run-data-bindings", token, runId], queryFn: () => getRunDataBindings(token, runId), enabled: fullRunReady && detailTab === "overview" });
  const queryClient = useQueryClient();
  const assignProject = useMutation({
    mutationFn: async ({ projectID, expectedProjectID }: { projectID: string; expectedProjectID: string }) =>
      assignRunProject(token, runId, projectID, expectedProjectID, "ui-v2", expectedProjectID ? "Run Detail explicit reassignment" : "Run Detail historical assignment"),
    onSuccess: async (result) => {
      queryClient.setQueryData(["run", token, runId], result.run);
      setProjectBindingOpen(false);
      setProjectBindingNotice(
        result.warnings.length > 0
          ? `${locale === "zh" ? "项目已更改；不可变历史未改写" : "Project changed; immutable history was not rewritten"} · ${result.warnings.length}`
          : (locale === "zh" ? "项目归属已更新" : "Project ownership updated")
      );
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: runSummaryKeys.root(token) }),
        queryClient.invalidateQueries({ queryKey: runSummaryKeys.active(token) }),
        queryClient.invalidateQueries({ queryKey: ["project-definitions", token] }),
        queryClient.invalidateQueries({ queryKey: ["run-journal", token, runId] }),
        queryClient.invalidateQueries({ queryKey: ["evidence-run-candidates"] }),
        queryClient.invalidateQueries({ queryKey: ["projects"] })
      ]);
    },
    onError: async () => {
      await queryClient.invalidateQueries({ queryKey: ["run", token, runId] });
    }
  });
  const collectArtifactInventory = useMutation({
    mutationFn: () => collectArtifacts(token, runId),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["artifact-collection", token, runId] }),
        queryClient.invalidateQueries({ queryKey: ["artifacts", token, runId] })
      ]);
    }
  });
  const logsLive = fullRunReady && run.data ? isActiveRun(run.data) : false;
  const [selectedLogSource, setSelectedLogSource] = useState<"terminal" | "stdout" | "stderr" | null>(null);
  const terminal = useLiveLog(token, runId, selectedLogSource === "terminal" ? { source: "terminal" } : null, logsLive);
  const stdout = useLiveLog(token, runId, selectedLogSource === "stdout" ? { source: "stdout" } : null, logsLive);
  const stderr = useLiveLog(token, runId, selectedLogSource === "stderr" ? { source: "stderr" } : null, logsLive);
  const eventsPath = fullRunReady && run.data ? uiEventsPath(run.data) : "";
  const eventLog = useLiveLog(token, runId, eventsPath ? { path: eventsPath } : null, logsLive);
  const eventLines = useMemo(() => eventLog.lines.map((line) => line.content), [eventLog.lines]);
  const parsedEvents = useParsedEvents(eventLines);
	const [statusCheckBusy, setStatusCheckBusy] = useState(false);
	const [statusCheckError, setStatusCheckError] = useState<string | null>(null);
  const activeRawLogSource = selectedLogSource || "terminal";
  const selectedLog = activeRawLogSource === "terminal" ? terminal : activeRawLogSource === "stdout" ? stdout : stderr;
	const statusPresentation = run.data ? runStatusPresentation(run.data, locale) : null;
	async function refreshCurrentStatus() {
		if (!run.data || statusCheckBusy) return;
		setStatusCheckBusy(true);
		setStatusCheckError(null);
		try {
			await onStatusCheck(run.data);
		} catch (cause) {
			setStatusCheckError(displayError(cause));
		} finally {
			setStatusCheckBusy(false);
		}
	}

  useEffect(() => {
    history.replaceState(null, "", `/ui-v2/runs/${encodeURIComponent(runId)}`);
  }, [runId]);

  useEffect(() => {
    setSelectedLogSource(null);
    setDetailTab("overview");
    setLegacyMarksOpen(false);
    setProjectBindingOpen(false);
    setProjectDraft("");
    setProjectBindingNotice(null);
    setShowAllArtifacts(false);
  }, [runId]);
  const managedInputs = dataBindings.data?.inputs || [];
  const managedOutputs = dataBindings.data?.outputs || [];
  const artifactItems = artifacts.data || [];
  const { visibleItems: visibleArtifactItems, hasMore: artifactHasMore } = artifactPage(artifactItems, showAllArtifacts);
  const hasCollectionTotals = (artifactCollection.data?.file_count || 0) > 0;
  const artifactCountLabel = hasCollectionTotals
    ? String(artifactCollection.data?.file_count)
    : showAllArtifacts
      ? String(artifactItems.length)
      : artifactHasMore
        ? `${initialArtifactRows}+`
        : String(visibleArtifactItems.length);
  const artifactSummaryBytes = hasCollectionTotals
    ? artifactCollection.data?.total_bytes
    : artifactHasMore
      ? null
      : visibleArtifactItems.reduce((sum, artifact) => sum + artifact.size, 0);

  return (
    <Modal title={run.data ? runTitle(run.data) : runId} onClose={onClose} wide>
      {run.data ? (
        <div className="detail-shell">
		  {run.isFetching && !run.isPending && !run.isPlaceholderData ? <AsyncState label={t("refreshing")} compact /> : null}
		  {run.error ? <AsyncState label={displayError(run.error)} tone="bad" compact actionLabel={t("retry")} onAction={() => void run.refetch()} /> : null}
		  {statusCheckError ? <AsyncState label={statusCheckError} tone="bad" compact /> : null}
          <section className="run-overview">
            <div className="run-overview-main">
              <div className="detail-summary-head">
                <span className="panel-kicker">{t("runSummary")}</span>
                <Pill tone={statusPresentation?.tone || statusTone(run.data.status)}>{statusPresentation?.label || run.data.status}</Pill>
              </div>
              <strong>{runTitle(run.data)}</strong>
              <span className="mono muted">{run.data.id}</span>
			  {statusPresentation?.uncertain ? <div className="run-observation-warning" title={statusPresentation.detail}>{statusPresentation.detail}</div> : null}
            </div>
            <div className="detail-facts">
              <span>
                <em>{t("resource")}</em>
                <strong>{resourceById.get(run.data.resource_id)?.name || run.data.resource_id}</strong>
              </span>
              <span>
                <em>{t("kind")}</em>
                <strong>{run.data.kind || "legacy / unknown"}</strong>
              </span>
              <span>
                <em>{t("projects")}</em>
                <strong>{run.data.project_id || t("unassigned")}</strong>
              </span>
			  <span className={runDataFinalizationPresentation(run.data).archiveProblem ? "detail-failure-reason" : ""} title={runDataFinalizationPresentation(run.data).message}>
				<em>{t("dataFinalization")}</em>
				<strong>{runDataFinalizationPresentation(run.data).state}</strong>
			  </span>
			  {run.data.data_finalization_error ? <span className="detail-failure-reason"><em>data error</em><strong>{run.data.data_finalization_error}</strong></span> : null}
              <span>
                <em>{t("gpu")}</em>
                <strong>{runGPU(run.data.gpu_index)}</strong>
              </span>
              <span>
                <em>{t("time")}</em>
                <strong>{fmtTime(run.data.created_at)}</strong>
              </span>
			  <span>
				<em>{t("freshness")}</em>
				<strong>{run.data.status_freshness || "unknown"} · {run.data.status_source || "local_cache"}</strong>
			  </span>
			  {run.data.status_checked_at ? (
				<span>
				  <em>{t("checkedAt")}</em>
				  <strong>{fmtTime(run.data.status_checked_at)}</strong>
				</span>
			  ) : null}
			  {run.data.status_check_error ? (
				<span className="detail-failure-reason">
				  <em>{t("statusCheckError")}</em>
				  <strong>{run.data.status_check_error}</strong>
				</span>
			  ) : null}
              <span>
                <em>{t("condaEnv")}</em>
                <strong>{run.data.resolved_env || run.data.conda_env || "-"}</strong>
              </span>
              {run.data.target_env ? (
                <span>
                  <em>{t("targetEnv")}</em>
                  <strong>{run.data.target_env}</strong>
                </span>
              ) : null}
              {run.data.preempt_run_id ? (
                <span>
                  <em>{t("preemptRun")}</em>
                  <strong>{run.data.preempt_run_id}</strong>
                </span>
              ) : null}
              {run.data.force_reason ? (
                <span className="detail-failure-reason">
                  <em>{t("forceReason")}</em>
                  <strong>{run.data.force_reason}</strong>
                </span>
              ) : null}
              {run.data.failure_kind ? (
                <span>
                  <em>{t("failureKind")}</em>
                  <strong>{run.data.failure_kind}</strong>
                </span>
              ) : null}
              {run.data.failure_reason ? (
                <span className="detail-failure-reason">
                  <em>{t("failureReason")}</em>
                  <strong>{run.data.failure_reason}</strong>
                </span>
              ) : null}
            </div>
            <div className="run-project-binding">
              <div>
                <em>{locale === "zh" ? "项目归属" : "Project ownership"}</em>
                <strong>{projects.find((project) => project.id === run.data?.project_id)?.name || run.data.project_id || (locale === "zh" ? "尚未绑定" : "Unassigned")}</strong>
                {projectBindingNotice ? <small>{projectBindingNotice}</small> : null}
                {assignProject.error ? <small className="detail-failure-reason">{displayError(assignProject.error)}</small> : null}
              </div>
              {projectBindingOpen ? (
                <div className="run-project-binding-editor">
                  <select value={projectDraft} disabled={assignProject.isPending} onChange={(event) => setProjectDraft(event.target.value)}>
                    <option value="">{locale === "zh" ? "选择项目" : "Choose a Project"}</option>
                    {projects.map((project) => <option key={project.id} value={project.id}>{project.name} · {project.id}</option>)}
                  </select>
                  <button
                    className="primary"
                    disabled={!projectDraft || projectDraft === (run.data.project_id || "") || assignProject.isPending}
                    onClick={() => {
                      const current = run.data?.project_id || "";
                      if (current && !window.confirm(locale === "zh" ? `确认将此 Run 从 ${current} 改绑到 ${projectDraft}？不可变历史不会被搬移。` : `Reassign this Run from ${current} to ${projectDraft}? Immutable history will not move.`)) return;
                      assignProject.mutate({ projectID: projectDraft, expectedProjectID: current });
                    }}
                  >
                    {assignProject.isPending ? t("saving") : (run.data.project_id ? (locale === "zh" ? "确认改绑" : "Confirm") : (locale === "zh" ? "绑定" : "Assign"))}
                  </button>
                  <button className="ghost-button" disabled={assignProject.isPending} onClick={() => setProjectBindingOpen(false)}>{t("cancel")}</button>
                </div>
              ) : (
                <button
                  className="ghost-button"
                  disabled={isActiveRun(run.data) || run.isPlaceholderData}
                  title={isActiveRun(run.data) ? (locale === "zh" ? "运行结束后才能改绑" : "Wait until the Run is terminal") : ""}
                  onClick={() => {
                    setProjectDraft(run.data?.project_id || "");
                    setProjectBindingOpen(true);
                    setProjectBindingNotice(null);
                  }}
                >
                  {run.data.project_id ? (locale === "zh" ? "更改" : "Change") : (locale === "zh" ? "选择项目" : "Choose Project")}
                </button>
              )}
            </div>
            <div className="detail-side-actions">
			  <button disabled={statusCheckBusy || run.isPlaceholderData} aria-busy={statusCheckBusy} onClick={() => void refreshCurrentStatus()}>
				<RefreshCcw size={16} className={statusCheckBusy ? "spin" : ""} />
				{statusCheckBusy ? t("refreshing") : t("refreshStatus")}
              </button>
              {isActiveRun(run.data) ? (
                <button className="danger" onClick={() => onCancel(run.data!)}>
                  {t("cancel")}
                </button>
              ) : null}
            </div>
          </section>
          {run.isPlaceholderData ? (
            <RunDetailLoading locale={locale} />
          ) : (
          <>
          <div className="detail-tabs" role="tablist" aria-label={t("runDetail")}>
            <button className={detailTab === "overview" ? "active" : ""} type="button" role="tab" aria-selected={detailTab === "overview"} onClick={() => setDetailTab("overview")}>
              {t("overview")}
            </button>
			<button className={detailTab === "evidence" ? "active" : ""} type="button" role="tab" aria-selected={detailTab === "evidence"} onClick={() => setDetailTab("evidence")}>{t("evidenceFreeze")}</button>
            <button
              className={detailTab === "raw" ? "active" : ""}
              type="button"
              role="tab"
              aria-selected={detailTab === "raw"}
              onClick={() => {
                setDetailTab("raw");
                setSelectedLogSource((current) => current || "terminal");
              }}
            >
              {t("raw")}
            </button>
          </div>
          {detailTab === "overview" ? (
            <>
              <div className="detail-context-grid">
                <section className="detail-summary-panel">
                  <span className="panel-kicker">{t("runtime")}</span>
                  <Info label="cwd" value={run.data.resolved_cwd || run.data.cwd || "-"} />
                  <Info label="tmux" value={run.data.tmux_session || "-"} />
                  <Info label="run dir" value={run.data.remote_run_dir || "-"} />
                  <Info label="manifest" value={manifest.data ? `${manifest.data.state} · ${manifest.data.sha256.slice(0, 22)}…` : "legacy / unavailable"} />
                </section>
                <section className="run-data-surface">
                  <div className="run-data-surface-head">
                    <div>
                      <span className="panel-kicker">{t("dataAndArtifacts")}</span>
                      <h2>{t("dataAndArtifacts")}</h2>
                    </div>
                    <div className="run-data-counts" aria-label={t("dataAndArtifacts")}>
                      <span>{managedInputs.length} {t("managedInputs")}</span>
                      <span>{managedOutputs.length} {t("managedOutputs")}</span>
                      <span>{artifactCountLabel} {t("artifacts")}</span>
                    </div>
                  </div>

                  <div className="managed-data-block">
                    <div className="run-data-subhead">
                      <div>
                        <Database size={16} />
                        <h3>{t("managedData")}</h3>
                      </div>
                    </div>
                    {dataBindings.isPending ? <AsyncState label={t("loading")} compact /> : null}
                    {dataBindings.error ? <div className="action-error">{displayError(dataBindings.error)}</div> : null}
                    {!dataBindings.isPending && !dataBindings.error && !managedInputs.length && !managedOutputs.length ? (
                      <div className="managed-data-empty">
                        <HardDrive size={18} />
                        <div>
                          <strong>{t("managedDataEmpty")}</strong>
                          <span>{t("managedDataEmptyHint")}</span>
                        </div>
                      </div>
                    ) : null}
                    {managedInputs.length || managedOutputs.length ? (
                      <div className="managed-data-list">
                        {managedInputs.map((input) => (
                          <div className="managed-data-row" key={input.id}>
                            <span className="data-direction input">{t("input")}</span>
                            <div>
                              <strong title={input.logical_uri}>{input.logical_uri}</strong>
                              <span title={input.target_path}>→ {input.target_path}</span>
                              <small>{input.state} · {input.revision || t("unpinned")}</small>
                              {input.last_error ? <span className="action-error">{input.last_error}</span> : null}
                            </div>
                          </div>
                        ))}
                        {managedOutputs.map((output) => (
                          <div className="managed-data-row" key={output.id}>
                            <span className="data-direction output">{t("output")}</span>
                            <div>
                              <strong title={output.logical_uri}>{output.logical_uri}</strong>
                              <span title={output.source_pattern}>{output.source_pattern} →</span>
                              <small>{output.state} · {output.role || "other"} · {output.revision || t("notPublished")}{output.required ? ` · ${t("required")}` : ""}</small>
                              {output.last_error ? <span className="action-error">{output.last_error}</span> : null}
                            </div>
                          </div>
                        ))}
                      </div>
                    ) : null}
                  </div>

                  <div className="artifact-block">
                    <div className="run-data-subhead artifact-subhead">
                      <div>
                        <Archive size={16} />
                        <h3>{t("artifacts")}</h3>
                        <span>{artifactCountLabel}{artifactSummaryBytes != null ? ` · ${formatBytes(artifactSummaryBytes)}` : ""}</span>
                      </div>
                      <button
                        className="artifact-refresh"
                        type="button"
                        disabled={collectArtifactInventory.isPending || artifactCollection.data?.state === "discovering"}
                        onClick={() => collectArtifactInventory.mutate()}
                      >
                        <RefreshCcw size={14} className={artifactCollection.data?.state === "discovering" ? "spin" : ""} />
                        {artifactCollection.data?.state === "discovering" ? t("loading") : t("refresh")}
                      </button>
                    </div>
                    <ArtifactList
                      artifacts={visibleArtifactItems}
                      collection={artifactCollection.data}
                      t={t}
                      loadingAll={artifacts.isFetching && showAllArtifacts}
                      onShowAll={artifactHasMore || (!showAllArtifacts && (artifactCollection.data?.file_count || 0) > visibleArtifactItems.length) ? () => setShowAllArtifacts(true) : undefined}
                    />
                    {collectArtifactInventory.error ? <div className="action-error">{displayError(collectArtifactInventory.error)}</div> : null}
                  </div>
                </section>
              </div>
              <div className="detail-grid">
                <div className="detail-main">
                  {run.data.project_id ? (
                    <section className="run-journal-backlinks">
                      <div>
                        <span className="panel-kicker">{locale === "zh" ? "项目工作日志" : "Project journal"}</span>
                        <strong>
                          {journalEntries.data?.length || 0}
                          {locale === "zh" ? " 条关联日志" : journalEntries.data?.length === 1 ? " linked entry" : " linked entries"}
                        </strong>
                        {journalEntries.data?.[0] ? <span>{journalEntries.data[0].title}</span> : (
                          <span>{locale === "zh" ? "这次实验还没有被写进项目工作日志。" : "This Run is not referenced by the project journal yet."}</span>
                        )}
                      </div>
                      <div>
                        <button type="button" onClick={() => onOpenProjectJournal(run.data!, false)}>
                          {locale === "zh" ? "查看日志" : "View journal"}
                        </button>
                        <button className="primary" type="button" onClick={() => onOpenProjectJournal(run.data!, true)}>
                          <Plus size={14} />
                          {locale === "zh" ? "写工作日志" : "Write journal"}
                        </button>
                      </div>
                    </section>
                  ) : (
                    <section className="run-journal-backlinks is-unassigned">
                      <div>
                        <span className="panel-kicker">{locale === "zh" ? "项目工作日志" : "Project journal"}</span>
                        <strong>{locale === "zh" ? "这个 Run 尚未归属项目" : "This Run is not assigned to a Project"}</strong>
                        <span>
                          {locale === "zh"
                            ? "研究推理只写入项目日志；先把 Run 归属到项目，才能关联日志。"
                            : "Research reasoning belongs in the Project journal. Assign this Run to a Project before linking an entry."}
                        </span>
                      </div>
                      <div>
                        <button type="button" disabled>
                          {locale === "zh" ? "需先归属项目" : "Project required"}
                        </button>
                      </div>
                    </section>
                  )}
                  {eventsPath ? <EventDashboard t={t} parsed={parsedEvents} path={eventsPath} snapshotError={eventLog.error} liveState={eventLog.state} cursor={eventLog.cursor} run={run.data} /> : null}
                </div>
              </div>
            </>
          ) : detailTab === "evidence" ? (
			<Suspense fallback={<AsyncState label={t("loading")} />}><EvidenceFreezePanel token={token} run={run.data} /></Suspense>
		  ) : (
            <div className="detail-grid raw-detail-grid">
              <div className="detail-main">
                <section className="command-card">
                  <div className="section-head command-head">
                    <h2>{t("command")}</h2>
                    <span className="muted mono">{run.data.resolved_cwd || run.data.cwd || "-"}</span>
                  </div>
                  <pre className="command-box">{run.data.command}</pre>
                </section>
                <section className="log-section raw-log-section">
                  <div className="section-head">
                    <h2>{t("rawLogs")}</h2>
                    <span className="muted">{t("rawLogHint")}</span>
                  </div>
                  <div className="log-source-actions">
                    {(["terminal", "stdout", "stderr"] as const).map((source) => (
                      <button
                        key={source}
                        className={activeRawLogSource === source ? "active" : ""}
                        type="button"
                        onClick={() => setSelectedLogSource(source)}
                      >
                        {source}
                      </button>
                    ))}
                  </div>
                  <LogPanel title={activeRawLogSource} state={selectedLog} />
                </section>
                {eventsPath ? <LogPanel title={t("rawEventJson")} state={eventLog} /> : null}
                <details
                  className="legacy-run-marks"
                  open={legacyMarksOpen}
                  onToggle={(event) => setLegacyMarksOpen(event.currentTarget.open)}
                >
                  <summary>
                    <span>
                      <strong>{locale === "zh" ? "历史 Run 标注" : "Historical Run notes"}</strong>
                      <small>{locale === "zh" ? "兼容只读，不再用于新增研究日志" : "Read-only compatibility; not used for new research notes"}</small>
                    </span>
                    <span className="legacy-badge">{locale === "zh" ? "旧数据" : "Legacy"}</span>
                  </summary>
                  <div className="legacy-run-marks-body">
                    {marks.isPending ? <AsyncState label={t("loading")} /> : null}
                    {marks.isError ? <AsyncState label={displayError(marks.error)} tone="bad" /> : null}
                    {!marks.isPending && !marks.isError && marks.data?.length ? (
                      <div className="finding-list">
                        {marks.data.map((mark) => <Finding key={mark.id} mark={mark} onOpen={() => setSelectedMark(mark)} />)}
                      </div>
                    ) : null}
                    {!marks.isPending && !marks.isError && !marks.data?.length ? <Empty t={t} /> : null}
                  </div>
                </details>
              </div>
            </div>
          )}
          </>
          )}
        </div>
	  ) : run.isPending ? (
		<AsyncState label={t("loading")} />
	  ) : run.isError ? (
		<AsyncState label={displayError(run.error)} tone="bad" />
	  ) : (
		<Empty t={t} />
      )}
      {selectedMark ? <MarkDetailModal mark={selectedMark} token={token} t={t} onClose={() => setSelectedMark(null)} /> : null}
    </Modal>
  );
}

function RunDetailLoading({ locale }: { locale: Locale }) {
  return (
    <section className="run-detail-loading" role="status" aria-live="polite">
      <RefreshCcw className="spin" size={18} />
      <div>
        <strong>{locale === "zh" ? "正在读取完整运行记录" : "Loading the full Run record"}</strong>
        <span>{locale === "zh" ? "摘要已可用；环境、产物和事件会分阶段出现。" : "The summary is ready; environment, artifacts, and events will appear in stages."}</span>
      </div>
      <div className="run-detail-loading-lines" aria-hidden="true"><i /><i /><i /></div>
    </section>
  );
}

function ArtifactList({ artifacts, collection, t, onShowAll, loadingAll = false }: { artifacts: Artifact[]; collection?: ArtifactCollection; t: T; onShowAll?: () => void; loadingAll?: boolean }) {
  if (collection?.state === "discovering") return <div className="artifact-state"><RefreshCcw className="spin" size={14} /><span>{t("artifactDiscovering")}</span></div>;
  if (collection?.state === "failed") return <div className="artifact-state bad"><span>{t("artifactCollectionFailed")}: {collection.error || t("unknownError")}</span></div>;
  if (!artifacts.length) return <div className="artifact-state"><Archive size={17} /><span>{collection?.state === "indexed" ? t("noArtifactMatches") : t("artifactNotIndexed")}</span></div>;
  return (
    <div className="artifact-list">
      {collection?.state === "partial" ? <div className="artifact-state bad">{t("partialInventory")}: {collection.error}</div> : null}
      {artifacts.map((artifact) => (
        <div className="artifact-row" key={artifact.id || artifact.path}>
          <div>
            <strong title={artifact.relative_path || artifact.path}>{artifact.relative_path || artifact.path}</strong>
            <span title={artifact.sha256 || undefined}>{[artifact.role, artifact.type || "file", formatBytes(artifact.size), artifact.sha256 ? artifact.sha256.slice(0, 18) + "…" : t("checksumUnavailable"), artifact.modified_at ? fmtShortTime(artifact.modified_at) : ""].filter(Boolean).join(" · ")}</span>
          </div>
        </div>
      ))}
      {onShowAll ? (
        <button className="artifact-show-all" type="button" onClick={onShowAll}>
          {t("showAllArtifacts")}{collection?.file_count ? ` · ${collection.file_count}` : ""}
        </button>
      ) : null}
      {loadingAll ? <div className="artifact-state"><RefreshCcw className="spin" size={14} /><span>{t("loading")}</span></div> : null}
    </div>
  );
}

function EventDashboard({ t, parsed, path, snapshotError, liveState, cursor, run }: { t: T; parsed: ParsedEvents; path: string; snapshotError?: string | null; liveState: LiveLogState; cursor: number; run: Run }) {
  const [expandedMetric, setExpandedMetric] = useState<string | null>(null);
  const latest = parsed.latestMetrics.slice(0, 16);
  const progress = summarizeProgress(parsed.progress).slice(0, 8);
  const params = parsed.params.slice(0, 24);
  const notes = parsed.notes.slice(-3);
  const metricFamilies = summarizeMetricFamilies(parsed.metrics).slice(0, 12);
  const initialSnapshotPending = liveState === "idle" || liveState === "loading";
  const catchingUp = liveState === "reconnecting" || liveState === "catching_up";
  const eventStateLabel = initialSnapshotPending
    ? t("eventsLoading")
    : catchingUp
      ? t("eventsCatchingUp")
      : liveState === "live"
        ? t("live")
        : liveState === "error"
          ? t("eventsUnavailable")
          : t("eventsSnapshot");
  const emptyEventLabel = initialSnapshotPending
    ? t("eventsLoading")
    : catchingUp
      ? t("eventsCatchingUp")
      : liveState === "error"
        ? t("eventsUnavailable")
        : "";
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
          <span className="muted mono event-path">{path} · {cursor || parsed.events.length} lines</span>
        </div>
        <span className={`event-transport-state ${liveState}`} role="status"><i aria-hidden="true" />{eventStateLabel}</span>
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
          {progress.length ? progress.map((row) => <ProgressStatusRow key={row.key} row={row} run={run} t={t} />) : <span className="muted">{emptyEventLabel || t("noProgressEvents")}</span>}
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
            )) : <span className="muted">{emptyEventLabel || t("noParamsYet")}</span>}
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
            )) : <span className="muted">{emptyEventLabel || t("noMetricsYet")}</span>}
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
            {!metricFamilies.length ? <span className="muted">{emptyEventLabel || t("noMetricFamiliesYet")}</span> : null}
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
  const details = progressDetails(latest, t);
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
        <span>{details || `${row.count} ${t("updates")}`}</span>
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
  const progressStatus = String(row.latest.status || "").toLowerCase();
  if (progressStatus === "early_stopped") return { label: t("earlyStopped"), tone: "done" };
  if (progressStatus === "completed" || progressStatus === "complete" || progressStatus === "done") return { label: t("complete"), tone: "done" };
  if ((progressStatus === "running" || progressStatus === "active") && isActiveRun(run)) return { label: t("active"), tone: "active" };
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

function progressDetails(point: ProgressPoint, t: T): string {
  const parts = [];
  if (point.best_epoch != null) parts.push(`${t("bestEpoch")} ${formatMetric(point.best_epoch)}`);
  return parts.join(" · ");
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
            {hasCurve ? <Suspense fallback={<AsyncState label={t("loading")} compact />}><MetricChart points={family.points} series={family.curveTrends} axisKind={family.axisKind} /></Suspense> : null}
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

function useParsedEvents(lines: string[]): ParsedEvents {
  const [parsed, setParsed] = useState<ParsedEvents>(() => parseEventLines([]));
  const linesKey = useMemo(() => lines.join("\n"), [lines]);
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
  }, [linesKey]);
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

function ResourceCard({ resource, actions, t }: { resource: Resource; actions?: ReactNode; t: T }) {
  const snap = resource.latest_snapshot;
  const gpus = parseGPUs(snap?.gpu_json);
  const memPct = snap?.mem_total_mb ? ((snap.mem_used_mb || 0) / snap.mem_total_mb) * 100 : 0;
  const telemetryAge = snap?.timestamp ? Date.now() - new Date(snap.timestamp).getTime() : null;
  const telemetryStale = telemetryAge != null && Number.isFinite(telemetryAge) && telemetryAge > 120_000;
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
      <div className={`resource-telemetry ${!snap ? "unknown" : telemetryStale ? "stale" : "fresh"}`}>
        <Clock size={13} />
        <span>{!snap ? t("telemetryUnavailable") : `${telemetryStale ? t("telemetryStale") : t("telemetryFresh")} · ${fmtShortTime(snap.timestamp)}`}</span>
      </div>
      {snap ? <div className="resource-meters">
        <ResourceMeter value={snap?.cpu_percent || 0} label="CPU" />
        <ResourceMeter value={memPct} label="RAM" detail={`${fmtMB(snap?.mem_used_mb)}/${fmtMB(snap?.mem_total_mb)}`} />
        {gpus.length ? gpus.flatMap((gpu: GPUInfo, index) => gpuMeters(gpu, index)) : (
          <div className="resource-meter-unavailable"><Cpu size={15} /><span>GPU</span><strong>{t("gpuUnavailable")}</strong></div>
        )}
      </div> : null}
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

function RunCard({ run, locale, resourceById, onOpen }: { run: Run; locale: Locale; resourceById: Map<string, Resource>; onOpen: () => void }) {
  const kind = run.kind || "formal";
	const presentation = runStatusPresentation(run, locale);
  return (
    <button className="run-card" onClick={onOpen} title={runTitle(run)}>
      <div className="card-head">
        <strong>{runTitle(run)}</strong>
        <StatusCapsule presentation={presentation} />
      </div>
      <div className="run-card-meta">
        <span className="run-resource-chip">{resourceById.get(run.resource_id)?.name || run.resource_id}</span>
        <span className={`run-kind-chip ${runKindClass(kind)}`}>{kind}</span>
        <span className="run-gpu-chip">GPU {runGPU(run.gpu_index)}</span>
      </div>
      <span className="mono muted">{fmtShortTime(run.created_at)}</span>
	  {presentation.uncertain ? <span className="run-observation-warning compact" title={presentation.detail}>{presentation.detail}</span> : null}
      <span className="run-command-line">{run.command || run.command_preview}</span>
    </button>
  );
}

function RunListCard({
  run,
	locale,
  resourceById,
  projectMeta,
  hideProject,
  bookmark,
  selected,
  trash,
  onOpen,
  onSelect,
  onToggleBookmark,
  onArchive,
  onRestore,
  onDelete,
  t
}: {
  run: Run;
	locale: Locale;
  resourceById: Map<string, Resource>;
  projectMeta?: RunProjectMeta;
  hideProject?: boolean;
  bookmark?: RunBookmark;
  selected: boolean;
  trash: boolean;
  onOpen: () => void;
  onSelect: (checked: boolean) => void;
  onToggleBookmark: () => void;
  onArchive: () => void;
  onRestore: () => void;
  onDelete: () => void;
  t: T;
}) {
  const compareEligible = isCompareEligible(run) && !trash;
  const kind = run.kind || "formal";
	const presentation = runStatusPresentation(run, locale);
  const resourceName = resourceById.get(run.resource_id)?.name || run.resource_id;
  const gpu = runGPU(run.gpu_index);
  const createdAt = fmtShortTime(run.created_at);
  const projectName = projectMeta?.projectName || run.project_id || t("unassignedRuns");
  const hasProject = Boolean(run.project_id);
  return (
    <article className="run-list-card">
      <div className="run-list-card-head">
        <button className="run-title-cell" type="button" onClick={onOpen}>
          <strong>{runTitle(run)}</strong>
          <span className="mono muted">{run.id}</span>
        </button>
        <StatusCapsule presentation={presentation} />
      </div>
      {!hideProject ? <div className="run-project-stack">
        <div className={`run-project-line${hasProject ? "" : " none"}`}>
          <span className="run-project-tag">{t("projects")}</span>
          <strong>{projectName}</strong>
        </div>
      </div> : null}
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
      <div className="run-list-observation">
        {presentation.uncertain ? <div className="run-observation-warning" title={presentation.detail}>{presentation.detail}</div> : null}
        <RunObservationMeta run={run} locale={locale} showError={false} />
      </div>
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

function markTone(kind?: string) {
  return kind === "failure" ? "bad" : kind === "key_result" ? "good" : kind === "followup" ? "accent" : "neutral";
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

function Pager({ t, total, page, pageSize, setPage }: { t: T; total: number; page: number; pageSize: number; setPage: (page: number) => void }) {
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

function AsyncState({ label, tone, compact, actionLabel, onAction }: { label: string; tone?: "bad"; compact?: boolean; actionLabel?: string; onAction?: () => void }) {
	return (
    <div className={`async-state${tone ? ` ${tone}` : ""}${compact ? " compact" : ""}`} role={tone ? "alert" : "status"} aria-live="polite">
      {!tone ? <RefreshCcw className="spin" size={compact ? 13 : 15} /> : null}
      <span>{label}</span>
      {actionLabel && onAction ? <button type="button" onClick={onAction}>{actionLabel}</button> : null}
    </div>
  );
}

function defaultResource(): Partial<Resource> {
  return { type: "ssh", user: "root", port: 22, status: "unknown" };
}

function statusTone(status?: string): "good" | "bad" | "warn" | "neutral" | "accent" {
  const s = (status || "").toLowerCase();
  if (["running", "idle", "ok", "succeeded"].includes(s)) return "good";
  if (["failed", "lost", "unreachable", "container_expired"].includes(s)) return "bad";
  if (["starting", "queued", "preflighting", "busy", "created", "unknown", "ssh_unreachable", "run_lost_but_events_cached"].includes(s)) return "warn";
  if (["formal", "ablation"].includes(s)) return "accent";
  return "neutral";
}

function labelForTab(tab: Tab, t: T) {
  const map: Record<Tab, I18nKey> = { dashboard: "dashboard", resources: "resources", dataCenter: "dataCenter", launchpad: "launchpad", projects: "projects", journal: "journal", literature: "literature", projectAssets: "assets", matrices: "matrices", evidence: "evidenceChains", runs: "runs", favorites: "favorites", execs: "execs", settings: "settings" };
  return t(map[tab]);
}

function filterProjects(projects: ProjectDefinition[], query: string) {
  const q = query.trim().toLowerCase();
  if (!q) return projects;
  return projects.filter((project) =>
    [
      project.id,
      project.name,
      project.description,
      project.local_root,
      project.source_repo,
      project.default_recipe
    ].some((part) => text(part).toLowerCase().includes(q))
  );
}

function projectCardFilterValue(id: string) {
  return `card:${id}`;
}

function buildRunProjectIndex(projects: ProjectDefinition[], runs: Run[]) {
  const out = new Map<string, RunProjectMeta>();
  const projectByID = new Map(projects.map((project) => [project.id, project]));
  for (const run of runs) {
    if (!run.project_id) continue;
    const project = projectByID.get(run.project_id);
    out.set(run.id, {
      projectId: run.project_id,
      projectName: project?.name || run.project_id
    });
  }
  return out;
}

function readDeepLinkRun() {
  const match = window.location.pathname.match(/^\/ui-v2\/runs\/([^/]+)/);
  return match ? decodeURIComponent(match[1]) : null;
}

function readInitialTab(): Tab {
  const path = window.location.pathname;
  if (readEvidenceProjectFromPath()) return "evidence";
  if (/^\/ui-v2\/projects\/[^/]+\/assets\/?$/.test(path)) return "projectAssets";
  if (/^\/ui-v2\/projects\/[^/]+\/literature\/?$/.test(path)) return "literature";
  if (/^\/ui-v2\/projects\/[^/]+\/runs\/?$/.test(path)) return "runs";
  if (/^\/ui-v2\/projects\/[^/]+(?:\/journal)?\/?$/.test(path)) return "journal";
  if (path.startsWith("/ui-v2/resources")) return "resources";
	if (path.startsWith("/ui-v2/data-center")) return "dataCenter";
  if (path.startsWith("/ui-v2/launchpad")) return "launchpad";
  if (path.startsWith("/ui-v2/projects")) return "projects";
  if (path.startsWith("/ui-v2/matrices")) return "matrices";
  if (path.startsWith("/ui-v2/evidence-chains")) return "projects";
  if (path.startsWith("/ui-v2/runs")) return "runs";
  if (path.startsWith("/ui-v2/favorites")) return "favorites";
  if (path.startsWith("/ui-v2/execs")) return "execs";
  if (path.startsWith("/ui-v2/settings")) return "settings";
  return "projects";
}

function readEvidenceProjectFromPath() {
  const route = parseProjectRoute(window.location.pathname);
  return route?.section === "research-graph" ? route.projectId : "";
}

function readProjectFromPath() {
  return parseProjectRoute(window.location.pathname)?.projectId || "";
}

function projectOverviewPath(projectID: string) {
  return `/ui-v2/projects/${encodeURIComponent(projectID)}`;
}

function projectJournalPath(projectID: string) {
  return `${projectOverviewPath(projectID)}/journal`;
}

function projectRunsPath(projectID: string) {
  return `${projectOverviewPath(projectID)}/runs`;
}

function projectLiteraturePath(projectID: string) {
  return `${projectOverviewPath(projectID)}/literature`;
}

function projectAssetsPath(projectID: string) {
  return `${projectOverviewPath(projectID)}/assets`;
}

function projectResearchGraphPath(projectID: string) {
  return `/ui-v2/projects/${encodeURIComponent(projectID)}/research-graph`;
}

function pathForTab(tab: Tab, evidenceProjectID = "", projectID = "") {
  const map: Record<Tab, string> = {
    dashboard: "/ui-v2/",
    resources: "/ui-v2/resources",
	dataCenter: "/ui-v2/data-center",
    launchpad: "/ui-v2/launchpad",
    projects: "/ui-v2/projects",
    journal: projectID ? projectJournalPath(projectID) : "/ui-v2/projects",
    literature: projectID ? projectLiteraturePath(projectID) : "/ui-v2/projects",
    projectAssets: "/ui-v2/projects",
    matrices: "/ui-v2/matrices",
    evidence: evidenceProjectID ? projectResearchGraphPath(evidenceProjectID) : "/ui-v2/projects",
    runs: "/ui-v2/runs",
    favorites: "/ui-v2/favorites",
    execs: "/ui-v2/execs",
    settings: "/ui-v2/settings"
  };
  return map[tab];
}

type T = ReturnType<typeof makeT>;
