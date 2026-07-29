import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, FolderGit2, LoaderCircle, PlayCircle, Plus, RefreshCcw, Server } from "lucide-react";
import {
  getProjectDefinitions,
  getProjectTargetPreparePlan,
  getProjectTargets,
  prepareProjectTarget,
  saveProjectDefinition,
  saveProjectTarget
} from "./api";
import type { Locale, ProjectDefinition, ProjectTarget, ProjectTargetPreparePlan, Resource, Run } from "./types";
import { canStartPrepare, targetDraftErrors } from "./projectPrepare";

export function ProjectLaunchpadPage({ token, locale, resources, onOpenRun }: { token: string; locale: Locale; resources: Resource[]; onOpenRun: (runID: string) => void }) {
  const queryClient = useQueryClient();
  const zh = locale === "zh";
  const projects = useQuery({ queryKey: ["project-definitions", token], queryFn: () => getProjectDefinitions(token), refetchInterval: 12_000 });
  const [showCreate, setShowCreate] = useState(false);
  const [draft, setDraft] = useState<Partial<ProjectDefinition>>({ name: "", config_path: "", config_hash: "", aggregate_command: "", gate_command: "" });
  const create = useMutation({
    mutationFn: () => saveProjectDefinition(token, draft),
    onSuccess: async () => {
      setShowCreate(false);
      setDraft({ name: "", config_path: "", config_hash: "", aggregate_command: "", gate_command: "" });
      await queryClient.invalidateQueries({ queryKey: ["project-definitions", token] });
    }
  });

  return (
    <div className="launchpad-page">
      <div className="section-heading launchpad-heading">
        <div>
          <span className="eyebrow">{zh ? "项目与运行环境" : "Projects and environments"}</span>
          <h2>{zh ? "项目设置" : "Project setup"}</h2>
          <p className="muted">{zh ? "管理项目定义，以及项目在各计算资源上的准备环境。" : "Manage project definitions and their preparation environments on compute resources."}</p>
        </div>
        <div className="toolbar-actions">
          <button onClick={() => void projects.refetch()} disabled={projects.isFetching}><RefreshCcw className={projects.isFetching ? "spin" : ""} size={15} /> {zh ? "刷新" : "Refresh"}</button>
          <button className="primary" onClick={() => setShowCreate((value) => !value)}><Plus size={15} /> {zh ? "添加项目" : "Add project"}</button>
        </div>
      </div>

      {showCreate ? (
        <form className="launchpad-create-panel" onSubmit={(event) => { event.preventDefault(); create.mutate(); }}>
          <label>{zh ? "名称" : "Name"}<input required value={draft.name || ""} onChange={(event) => setDraft({ ...draft, name: event.target.value })} /></label>
          <label>{zh ? "配置路径" : "Config path"}<input value={draft.config_path || ""} onChange={(event) => setDraft({ ...draft, config_path: event.target.value })} placeholder="/project/.aexp.yaml" /></label>
          <label>{zh ? "配置指纹" : "Config fingerprint"}<input value={draft.config_hash || ""} onChange={(event) => setDraft({ ...draft, config_hash: event.target.value })} placeholder="sha256:…" /></label>
          <label>{zh ? "聚合命令" : "Aggregate command"}<input value={draft.aggregate_command || ""} onChange={(event) => setDraft({ ...draft, aggregate_command: event.target.value })} placeholder="python scripts/paper/build_evidence.py" /></label>
          <label>{zh ? "发布门禁命令" : "Release gate command"}<input value={draft.gate_command || ""} onChange={(event) => setDraft({ ...draft, gate_command: event.target.value })} placeholder="python scripts/paper/release_gate.py" /></label>
          <button className="primary" disabled={create.isPending} type="submit">{create.isPending ? (zh ? "保存中…" : "Saving…") : (zh ? "创建" : "Create")}</button>
          {create.error ? <span className="action-error">{String(create.error)}</span> : null}
        </form>
      ) : null}

      {projects.isPending ? <div className="async-state"><LoaderCircle className="spin" /> {zh ? "正在加载项目…" : "Loading projects…"}</div> : null}
      {projects.isError ? <div className="async-state error"><AlertTriangle /> {String(projects.error)}</div> : null}
      {!projects.isPending && !projects.isError && !(projects.data?.length) ? <div className="empty-launchpad">{zh ? "还没有项目。添加项目后即可配置它的运行环境。" : "No projects yet. Add one to configure its runtime environments."}</div> : null}
      <div className="launchpad-project-list">
        {(projects.data || []).map((project) => <LaunchProjectCard key={project.id} token={token} locale={locale} project={project} resources={resources} onOpenRun={onOpenRun} />)}
      </div>
    </div>
  );
}

function LaunchProjectCard({ token, locale, project, resources, onOpenRun }: { token: string; locale: Locale; project: ProjectDefinition; resources: Resource[]; onOpenRun: (runID: string) => void }) {
  const queryClient = useQueryClient();
  const zh = locale === "zh";
  const targets = useQuery({ queryKey: ["project-targets", token, project.id], queryFn: () => getProjectTargets(token, project.id), refetchInterval: 3_000 });
  const [adding, setAdding] = useState(false);
  const [draft, setDraft] = useState<Partial<ProjectTarget>>({ name: "", resource_id: resources[0]?.id || "", cwd: "", env_strategy: "auto", default_gpu: -1, env_json: "{}", readiness: "unknown" });
  const createTarget = useMutation({
    mutationFn: () => saveProjectTarget(token, project.id, draft),
    onSuccess: async () => {
      setAdding(false);
      setDraft({ name: "", resource_id: resources[0]?.id || "", cwd: "", env_strategy: "auto", default_gpu: -1, env_json: "{}", readiness: "unknown" });
      await queryClient.invalidateQueries({ queryKey: ["project-targets", token, project.id] });
    }
  });
  return (
    <article className="launch-project-card">
      <header>
        <div><FolderGit2 size={18} /><div><h3>{project.name}</h3><span className="mono muted">{project.config_path || project.id}</span></div></div>
        <button onClick={() => setAdding((value) => !value)}><Plus size={14} /> {zh ? "运行环境" : "Environment"}</button>
      </header>
      {project.config_hash ? <div className="config-fingerprint">{zh ? "配置" : "config"} <code>{project.config_hash}</code></div> : <div className="launch-warning"><AlertTriangle size={14} /> {zh ? "未记录配置指纹，无法完整检测配置漂移。" : "No config fingerprint; drift detection is limited."}</div>}
      {adding ? (
        <form className="target-create-grid" onSubmit={(event) => { event.preventDefault(); createTarget.mutate(); }}>
          <label>{zh ? "名称" : "Name"}<input required value={draft.name || ""} onChange={(event) => setDraft({ ...draft, name: event.target.value })} placeholder="mu" /></label>
          <label>{zh ? "计算资源" : "Resource"}<select required value={draft.resource_id || ""} onChange={(event) => setDraft({ ...draft, resource_id: event.target.value })}><option value="">{zh ? "请选择…" : "Select…"}</option>{resources.map((resource) => <option key={resource.id} value={resource.id}>{resource.name}</option>)}</select></label>
          <label>{zh ? "工作目录" : "Working directory"}<input required value={draft.cwd || ""} onChange={(event) => setDraft({ ...draft, cwd: event.target.value })} placeholder="/workspace/project" /></label>
          <label>{zh ? "环境策略" : "Environment strategy"}<select value={draft.env_strategy || "auto"} onChange={(event) => setDraft({ ...draft, env_strategy: event.target.value })}><option value="auto">auto</option><option value="raw">raw</option></select></label>
          <label>Conda env<input value={draft.conda_env || ""} onChange={(event) => setDraft({ ...draft, conda_env: event.target.value })} /></label>
          <label className="wide">{zh ? "环境准备命令" : "Prepare command"}<input required value={draft.prepare_command || ""} onChange={(event) => setDraft({ ...draft, prepare_command: event.target.value })} placeholder="uv sync --frozen" /></label>
          <button className="primary" disabled={createTarget.isPending || targetDraftErrors(draft).length > 0}>{zh ? "保存环境" : "Save environment"}</button>
          {createTarget.error ? <span className="action-error">{String(createTarget.error)}</span> : null}
        </form>
      ) : null}
      {targets.isPending ? <div className="async-state"><LoaderCircle className="spin" /> {zh ? "正在加载运行环境…" : "Loading environments…"}</div> : null}
      <div className="target-card-grid">
        {(targets.data || []).map((target) => <TargetCard key={target.id} token={token} locale={locale} project={project} target={target} resource={resources.find((resource) => resource.id === target.resource_id)} onOpenRun={onOpenRun} />)}
      </div>
    </article>
  );
}

function TargetCard({ token, locale, project, target, resource, onOpenRun }: { token: string; locale: Locale; project: ProjectDefinition; target: ProjectTarget; resource?: Resource; onOpenRun: (runID: string) => void }) {
  const queryClient = useQueryClient();
  const zh = locale === "zh";
  const [plan, setPlan] = useState<ProjectTargetPreparePlan | null>(null);
  const planMutation = useMutation({ mutationFn: () => getProjectTargetPreparePlan(token, project.id, target.id), onSuccess: setPlan });
  const prepare = useMutation({
    mutationFn: () => prepareProjectTarget(token, project.id, target.id),
    onSuccess: async (response) => {
      queryClient.setQueryData<Run>(["run", token, response.run.id], response.run);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["project-targets", token, project.id] }),
        queryClient.invalidateQueries({ queryKey: ["run-summaries", token] })
      ]);
      onOpenRun(response.run.id);
    }
  });
  const busy = target.readiness === "checking" || prepare.isPending;
  return (
    <section className={`target-card readiness-${target.readiness}`}>
      <div className="target-card-title"><Server size={16} /><div><strong>{target.name}</strong><span>{resource?.name || target.resource_id}</span></div><ReadinessBadge locale={locale} value={target.readiness} /></div>
      <dl><div><dt>cwd</dt><dd className="mono">{target.cwd}</dd></div><div><dt>env</dt><dd>{target.env_strategy}{target.conda_env ? ` · ${target.conda_env}` : ""}</dd></div><div><dt>prepare</dt><dd className="mono">{target.prepare_command || (zh ? "未配置" : "not configured")}</dd></div></dl>
      {target.readiness_error ? <div className="launch-warning"><AlertTriangle size={14} /> {target.readiness_error}</div> : null}
      {plan ? <div className="prepare-plan"><strong>{zh ? "环境准备计划 · 不产生实验结论" : "Prepare plan · evidence grade: none"}</strong><ol>{plan.stages.map((stage) => <li key={stage.name}><span>{stage.name}</span>{stage.description}{stage.mutates ? <em>{zh ? "会修改远端" : "remote change"}</em> : null}</li>)}</ol>{plan.warnings.map((warning) => <p key={warning} className="launch-warning"><AlertTriangle size={13} /> {warning}</p>)}</div> : null}
      <footer>
        {target.last_prepare_run_id ? <button onClick={() => onOpenRun(target.last_prepare_run_id!)}><PlayCircle size={14} /> {zh ? "打开准备记录" : "Open prepare run"}</button> : null}
        <button disabled={planMutation.isPending} onClick={() => planMutation.mutate()}>{planMutation.isPending ? <LoaderCircle className="spin" size={14} /> : null} {zh ? "检查计划" : "Inspect plan"}</button>
        <button className="primary" disabled={busy || !canStartPrepare(target, Boolean(plan))} onClick={() => prepare.mutate()}>{busy ? <LoaderCircle className="spin" size={14} /> : <PlayCircle size={14} />} {zh ? "准备环境" : "Prepare"}</button>
      </footer>
      {prepare.error || planMutation.error ? <div className="action-error">{String(prepare.error || planMutation.error)}</div> : null}
    </section>
  );
}

function ReadinessBadge({ locale, value }: { locale: Locale; value: ProjectTarget["readiness"] }) {
  const labels: Record<ProjectTarget["readiness"], string> = locale === "zh"
    ? { unknown: "未知", checking: "检查中", ready: "就绪", drifted: "有漂移", failed: "失败" }
    : { unknown: "unknown", checking: "checking", ready: "ready", drifted: "drifted", failed: "failed" };
  return <span className={`readiness-badge ${value}`}>{value === "ready" ? <CheckCircle2 size={13} /> : value === "checking" ? <LoaderCircle className="spin" size={13} /> : value === "failed" || value === "drifted" ? <AlertTriangle size={13} /> : null}{labels[value]}</span>;
}
