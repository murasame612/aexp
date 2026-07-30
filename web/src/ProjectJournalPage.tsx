import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import ReactMarkdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  BookOpen,
  Check,
  ChevronDown,
  ChevronRight,
  Circle,
  Link2,
  Plus,
  Search,
  X
} from "lucide-react";
import {
  createProjectJournalEntry,
  getProjectJournal,
  getRunSummaries,
  setProjectJournalNextAction
} from "./api";
import { groupJournalByDate, journalPreview } from "./projectJournal";
import type { Locale, ProjectDefinition, ProjectJournalEntry, Run } from "./types";

export function ProjectJournalPage({
  token,
  locale,
  project,
  onOpenRun
}: {
  token: string;
  locale: Locale;
  project: ProjectDefinition;
  onOpenRun: (runID: string) => void;
}) {
  const queryClient = useQueryClient();
  const initial = useMemo(() => new URLSearchParams(window.location.search), [project.id]);
  const initialRunID = initial.get("run")?.trim() || "";
  const [search, setSearch] = useState("");
  const [runFilter, setRunFilter] = useState(initial.get("run")?.trim() || "");
  const [openNextOnly, setOpenNextOnly] = useState(false);
  const [expandedID, setExpandedID] = useState(initial.get("entry")?.trim() || "");
  const [composerOpen, setComposerOpen] = useState(initial.get("compose") === "1");
  const [title, setTitle] = useState("");
  const [bodyMD, setBodyMD] = useState("");
  const [nextAction, setNextAction] = useState("");
  const [relatedRunIDs, setRelatedRunIDs] = useState<string[]>(initialRunID ? [initialRunID] : []);
  const [runDraft, setRunDraft] = useState("");

  const journal = useQuery({
    queryKey: ["project-journal", token, project.id, search, runFilter, openNextOnly],
    queryFn: () => getProjectJournal(token, project.id, {
      limit: 100,
      query: search.trim(),
      runId: runFilter.trim(),
      nextActionStatus: openNextOnly ? "open" : ""
    }),
    refetchInterval: 10_000,
    refetchOnWindowFocus: "always"
  });
  const projectRuns = useQuery({
    queryKey: ["project-journal-runs", token, project.id],
    queryFn: () => getRunSummaries(token, {
      limit: 100,
      offset: 0,
      projectScope: project.id,
      kindGroup: "all",
      refresh: false
    }),
    staleTime: 30_000
  });
  const createEntry = useMutation({
    mutationFn: () => createProjectJournalEntry(token, project.id, {
      actor: "human",
      title: title.trim(),
      body_md: bodyMD.trim(),
      next_action: nextAction.trim(),
      run_ids: relatedRunIDs
    }),
    onSuccess: async (entry) => {
      setTitle("");
      setBodyMD("");
      setNextAction("");
      setRelatedRunIDs([]);
      setRunDraft("");
      setComposerOpen(false);
      setExpandedID(entry.id);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["project-journal", token, project.id] }),
        queryClient.invalidateQueries({ queryKey: ["run-journal", token] })
      ]);
    }
  });
  const updateNextAction = useMutation({
    mutationFn: ({ entryID, status }: { entryID: string; status: "open" | "done" }) =>
      setProjectJournalNextAction(token, entryID, status),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["project-journal", token, project.id] }),
        queryClient.invalidateQueries({ queryKey: ["run-journal", token] })
      ]);
    }
  });

  useEffect(() => {
    if (!initialRunID) return;
    setRelatedRunIDs((current) => current.includes(initialRunID) ? current : [...current, initialRunID]);
  }, [initialRunID]);

  const runOptions = projectRuns.data?.items || [];
  const runByID = useMemo(() => new Map(runOptions.map((run) => [run.id, run])), [runOptions]);
  const groups = groupJournalByDate(journal.data || [], locale);
  const zh = locale === "zh";

  function addRunReference() {
    const runID = runDraft.trim();
    if (!runID || relatedRunIDs.includes(runID)) return;
    setRelatedRunIDs((current) => [...current, runID]);
    setRunDraft("");
  }

  return (
    <section className="project-journal-page">
      <div className="journal-toolbar">
        <label className="journal-search">
          <Search size={15} />
          <span className="sr-only">{zh ? "搜索工作日志" : "Search project journal"}</span>
          <input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={zh ? "搜索标题、正文或下一步" : "Search title, body, or next action"}
          />
        </label>
        <label className="journal-run-filter">
          <Link2 size={14} />
          <span>{zh ? "关联实验" : "Related run"}</span>
          <input
            list="project-journal-run-filter-options"
            value={runFilter}
            onChange={(event) => setRunFilter(event.target.value)}
            placeholder={zh ? "全部" : "All"}
          />
        </label>
        <button
          className={openNextOnly ? "journal-filter-toggle active" : "journal-filter-toggle"}
          type="button"
          aria-pressed={openNextOnly}
          onClick={() => setOpenNextOnly((current) => !current)}
        >
          <Circle size={13} />
          {zh ? "仅看待办" : "Open next actions"}
        </button>
        <button className="primary journal-compose-trigger" type="button" onClick={() => setComposerOpen((current) => !current)}>
          <Plus size={15} />
          {zh ? "写工作日志" : "Write journal"}
        </button>
      </div>
      <datalist id="project-journal-run-filter-options">
        {runOptions.map((run) => <option key={run.id} value={run.id}>{run.name || run.id}</option>)}
      </datalist>

      {composerOpen ? (
        <form
          className="journal-composer"
          onSubmit={(event) => {
            event.preventDefault();
            if (title.trim()) createEntry.mutate();
          }}
        >
          <div className="journal-composer-heading">
            <div>
              <span className="panel-kicker">{zh ? "新日志" : "New journal entry"}</span>
              <strong>{zh ? "记录判断、问题和下一步" : "Record reasoning, issues, and the next action"}</strong>
            </div>
            <button className="icon-button" type="button" aria-label={zh ? "关闭编辑器" : "Close composer"} onClick={() => setComposerOpen(false)}>
              <X size={16} />
            </button>
          </div>
          <label>
            <span>{zh ? "标题" : "Title"}</span>
            <input autoFocus value={title} onChange={(event) => setTitle(event.target.value)} required />
          </label>
          <label>
            <span>{zh ? "正文（Markdown）" : "Body (Markdown)"}</span>
            <textarea value={bodyMD} onChange={(event) => setBodyMD(event.target.value)} rows={8} />
          </label>
          <label>
            <span>{zh ? "下一步（可选）" : "Next action (optional)"}</span>
            <input value={nextAction} onChange={(event) => setNextAction(event.target.value)} />
          </label>
          <div className="journal-run-editor">
            <span>{zh ? "关联实验（可选）" : "Related runs (optional)"}</span>
            <div className="journal-run-add">
              <input
                list="project-journal-run-options"
                value={runDraft}
                onChange={(event) => setRunDraft(event.target.value)}
                placeholder="run_…"
              />
              <button type="button" onClick={addRunReference}>{zh ? "添加" : "Add"}</button>
            </div>
            <datalist id="project-journal-run-options">
              {runOptions.map((run) => <option key={run.id} value={run.id}>{run.name || run.id}</option>)}
            </datalist>
            {relatedRunIDs.length ? (
              <div className="journal-run-chips">
                {relatedRunIDs.map((runID) => (
                  <button key={runID} type="button" onClick={() => setRelatedRunIDs((current) => current.filter((id) => id !== runID))}>
                    {runByID.get(runID)?.name || runID}
                    <X size={12} />
                  </button>
                ))}
              </div>
            ) : null}
          </div>
          {createEntry.error ? <div className="action-error">{String(createEntry.error)}</div> : null}
          <div className="journal-composer-actions">
            <button type="button" onClick={() => setComposerOpen(false)}>{zh ? "取消" : "Cancel"}</button>
            <button className="primary" type="submit" disabled={!title.trim() || createEntry.isPending}>
              {createEntry.isPending ? (zh ? "保存中…" : "Saving…") : (zh ? "追加到时间线" : "Append to timeline")}
            </button>
          </div>
        </form>
      ) : null}

      <details className="journal-project-context">
        <summary>{zh ? "项目上下文" : "Project context"}</summary>
        <dl>
          <div><dt>{zh ? "项目" : "Project"}</dt><dd>{project.name || project.id}</dd></div>
          <div><dt>{zh ? "本地目录" : "Local root"}</dt><dd>{project.local_root || "—"}</dd></div>
          <div><dt>{zh ? "默认配方" : "Default recipe"}</dt><dd>{project.default_recipe || "—"}</dd></div>
          <div><dt>{zh ? "发布门禁" : "Release gate"}</dt><dd>{project.gate_command || "—"}</dd></div>
        </dl>
      </details>

      {journal.isPending ? <div className="journal-empty">{zh ? "正在读取工作日志…" : "Loading project journal…"}</div> : null}
      {journal.error ? <div className="journal-empty bad">{String(journal.error)}</div> : null}
      {!journal.isPending && !journal.error && !groups.length ? (
        <div className="journal-empty">
          <BookOpen size={24} />
          <strong>{zh ? "还没有工作日志" : "No journal entries yet"}</strong>
          <span>{zh ? "先记录一个判断或下一步；它不需要挂在实验上。" : "Start with a decision or next action. A Run link is optional."}</span>
        </div>
      ) : null}

      <div className="journal-timeline">
        {groups.map((group) => (
          <section className="journal-day" key={group.key}>
            <h2>{group.label}</h2>
            <div className="journal-day-entries">
              {group.entries.map((entry) => (
                <JournalEntryRow
                  key={entry.id}
                  entry={entry}
                  expanded={expandedID === entry.id}
                  locale={locale}
                  runByID={runByID}
                  onToggle={() => setExpandedID((current) => current === entry.id ? "" : entry.id)}
                  onOpenRun={onOpenRun}
                  onToggleNextAction={(status) => updateNextAction.mutate({ entryID: entry.id, status })}
                  nextActionBusy={updateNextAction.isPending}
                />
              ))}
            </div>
          </section>
        ))}
      </div>
    </section>
  );
}

export function JournalEntryRow({
  entry,
  expanded,
  locale,
  runByID,
  onToggle,
  onOpenRun,
  onToggleNextAction,
  nextActionBusy
}: {
  entry: ProjectJournalEntry;
  expanded: boolean;
  locale: Locale;
  runByID: Map<string, Run>;
  onToggle: () => void;
  onOpenRun: (runID: string) => void;
  onToggleNextAction: (status: "open" | "done") => void;
  nextActionBusy: boolean;
}) {
  const zh = locale === "zh";
  const time = new Intl.DateTimeFormat(zh ? "zh-CN" : "en", { hour: "2-digit", minute: "2-digit" }).format(new Date(entry.created_at));
  const panelID = `journal-entry-${entry.id}`;
  return (
    <article className={expanded ? "journal-entry expanded" : "journal-entry"}>
      <time dateTime={entry.created_at}>{time}</time>
      <span className="journal-rail-dot" aria-hidden="true" />
      <div className="journal-entry-content">
        <button
          className="journal-entry-toggle"
          type="button"
          aria-expanded={expanded}
          aria-controls={panelID}
          onClick={onToggle}
        >
          <span className="journal-entry-title-line">
            <strong>{entry.title}</strong>
            {entry.next_action_status === "open" ? <span className="journal-open-next">{zh ? "待办" : "Next"}</span> : null}
            {entry.next_action_status === "done" ? <Check className="journal-done-mark" size={14} /> : null}
          </span>
          {!expanded ? (
            <span className="journal-entry-preview">{journalPreview(entry.body_md || entry.next_action || "")}</span>
          ) : null}
          <span className="journal-entry-meta">
            <span>{entry.actor}</span>
            {entry.run_ids.length ? <span><Link2 size={12} />{entry.run_ids.length} {zh ? "个实验" : entry.run_ids.length === 1 ? "run" : "runs"}</span> : null}
          </span>
          {expanded ? <ChevronDown size={17} /> : <ChevronRight size={17} />}
        </button>
        {expanded ? (
          <div className="journal-entry-detail" id={panelID}>
            {entry.body_md ? (
              <div className="markdown-body">
                <ReactMarkdown
                  remarkPlugins={[remarkGfm]}
                  urlTransform={(url) => defaultUrlTransform(url)}
                  components={{ a: ({ ...props }) => <a {...props} target="_blank" rel="noreferrer" /> }}
                >
                  {entry.body_md}
                </ReactMarkdown>
              </div>
            ) : <p className="muted">{zh ? "没有正文。" : "No body."}</p>}
            {entry.run_ids.length ? (
              <div className="journal-entry-runs">
                <span>{zh ? "关联实验" : "Related runs"}</span>
                <div>
                  {entry.run_ids.map((runID) => (
                    <button type="button" key={runID} onClick={() => onOpenRun(runID)}>
                      {runByID.get(runID)?.name || runID}
                    </button>
                  ))}
                </div>
              </div>
            ) : null}
            {entry.next_action ? (
              <div className={entry.next_action_status === "done" ? "journal-next-action done" : "journal-next-action"}>
                <div>
                  {entry.next_action_status === "done" ? <Check size={16} /> : <Circle size={14} />}
                  <span><small>{zh ? "下一步" : "Next action"}</small>{entry.next_action}</span>
                </div>
                <button
                  type="button"
                  disabled={nextActionBusy}
                  onClick={() => onToggleNextAction(entry.next_action_status === "done" ? "open" : "done")}
                >
                  {entry.next_action_status === "done" ? (zh ? "重新打开" : "Reopen") : (zh ? "标记完成" : "Mark done")}
                </button>
              </div>
            ) : null}
            <p className="journal-curation-hint">
              {zh ? "这是一条工作记录。只有会改变论文结论的内容才需要晋升到证据图。" : "This is a work record. Promote only conclusion-changing evidence to an Evidence Map."}
            </p>
          </div>
        ) : null}
      </div>
    </article>
  );
}
