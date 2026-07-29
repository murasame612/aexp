import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Activity, CheckCircle2, Clock3, Database, HardDrive, LoaderCircle, Network, Pencil, Plus, RefreshCw, Server, ShieldCheck, Trash2, X, XCircle } from "lucide-react";
import { deleteLogicalRoot, deleteStorageTarget, getDatasetVersions, getLogicalRoots, getStorageTargets, getTransfers, inspectLogicalPath, locateLogicalPath, saveLogicalRoot, saveResource, saveStorageTarget, testStorageTarget, updateStorageTarget } from "./api";
import type { Locale, Resource, StorageTarget } from "./types";
import { storageChecks, storageDataPlane } from "./storageHealth";
import { placementDisplayState, transferErrorSummary, transferPresentation } from "./dataState";

function bytes(value?: number) {
  if (!value) return "—";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let n = value, i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i += 1; }
  return `${n.toFixed(i > 2 ? 1 : 0)} ${units[i]}`;
}

function checkedLabel(value: string | undefined, zh: boolean) {
  if (!value) return zh ? "从未检查" : "Never checked";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(zh ? "zh-CN" : "en", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(date);
}

function isStale(value?: string) {
  return !value || Date.now() - new Date(value).getTime() > 10 * 60 * 1000;
}

type NASForm = { name: string; host: string; user: string; port: string; root: string; authRef: string };
const emptyForm: NASForm = { name: "", host: "", user: "root", port: "22", root: "/data/aexp", authRef: "~/.ssh/id_ed25519" };

export function DataCenterPage({ token, locale, resources }: { token: string; locale: Locale; resources: Resource[] }) {
  const zh = locale === "zh";
  const queryClient = useQueryClient();
  const [showForm, setShowForm] = useState(false);
  const [editingID, setEditingID] = useState<string | null>(null);
  const [form, setForm] = useState<NASForm>(emptyForm);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [rootForm,setRootForm]=useState({workspace:"",prefix:"",storage_target_id:"",physical_root:""});
  const [pathURI,setPathURI]=useState("");
  const targets = useQuery({ queryKey: ["storage-targets", token], queryFn: () => getStorageTargets(token), refetchInterval: 15000 });
  const datasets = useQuery({ queryKey: ["dataset-versions", token], queryFn: () => getDatasetVersions(token), refetchInterval: 10000 });
  const logicalRoots=useQuery({queryKey:["logical-roots",token],queryFn:()=>getLogicalRoots(token),enabled:advancedOpen,refetchInterval:advancedOpen?15000:false});
  const transfers=useQuery({queryKey:["transfers",token],queryFn:()=>getTransfers(token),refetchInterval:(q)=>(q.state.data?.items||[]).some(i=>!["completed","failed","blocked","cancelled"].includes(i.job.state))?1500:10000});
  const placements=useQuery({queryKey:["path-placements",token,pathURI],queryFn:()=>locateLogicalPath(token,pathURI),enabled:advancedOpen&&pathURI.startsWith("aexp://")});
  const resourceByID = useMemo(() => new Map(resources.map((resource) => [resource.id, resource])), [resources]);
  const targetNames = new Map((targets.data?.items || []).map((target) => [target.id, target.name]));

  const closeForm = () => { setShowForm(false); setEditingID(null); setForm(emptyForm); };
  const editTarget = (target: StorageTarget) => {
    const resource = resourceByID.get(target.resource_id);
    setEditingID(target.id);
    setForm({ name: target.name, host: resource?.host || "", user: resource?.user || "root", port: String(resource?.port || 22), root: target.root_path, authRef: resource?.auth_ref || "" });
    setShowForm(true);
  };

  const saveNAS = useMutation({
    mutationFn: async () => {
      const name = form.name.trim(), root = form.root.trim();
      if (!name || !form.host.trim() || !root.startsWith("/")) throw new Error(zh ? "名称、地址和绝对根目录不能为空" : "Name, host and an absolute root are required");
      if (editingID) {
        const target = targets.data?.items.find((item) => item.id === editingID);
        const resource = target ? resourceByID.get(target.resource_id) : undefined;
        if (!target || !resource) throw new Error(zh ? "NAS 对应的 SSH 资源不存在" : "The backing SSH resource is missing");
        await saveResource(token, { ...resource, host: form.host.trim(), port: Number(form.port) || 22, user: form.user.trim() || "root", auth_ref: form.authRef.trim(), root_dir: root });
        return updateStorageTarget(token, target.id, { name, resource_id: resource.id, root_path: root });
      }
      const resource = await saveResource(token, { name: `nas-${name}`, type: "ssh", host: form.host.trim(), port: Number(form.port) || 22, user: form.user.trim() || "root", root_dir: root, auth_ref: form.authRef.trim(), status: "unknown" });
      return saveStorageTarget(token, { name, resource_id: resource.id, root_path: root });
    },
    onSuccess: async () => {
      closeForm();
      await Promise.all([queryClient.invalidateQueries({ queryKey: ["resources", token] }), queryClient.invalidateQueries({ queryKey: ["storage-targets", token] })]);
    }
  });

  const checkTarget = useMutation({
    mutationFn: (id: string) => testStorageTarget(token, id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["storage-targets", token] })
  });
  const removeTarget = useMutation({
    mutationFn: (id: string) => deleteStorageTarget(token, id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["storage-targets", token] })
  });
  const createRoot=useMutation({mutationFn:()=>saveLogicalRoot(token,rootForm),onSuccess:async()=>{setRootForm({workspace:"",prefix:"",storage_target_id:"",physical_root:""});await queryClient.invalidateQueries({queryKey:["logical-roots",token]})}});
  const removeRoot=useMutation({mutationFn:(id:string)=>deleteLogicalRoot(token,id),onSuccess:()=>queryClient.invalidateQueries({queryKey:["logical-roots",token]})});
  const inspectPath=useMutation({mutationFn:()=>inspectLogicalPath(token,pathURI),onSuccess:()=>queryClient.invalidateQueries({queryKey:["path-placements",token,pathURI]})});

  const confirmDelete = (target: StorageTarget) => {
    const message = zh
      ? `删除 ${target.name} 的 aexp 存储登记？\n\n不会删除 NAS 上的任何文件，也不会删除底层 SSH Resource。有数据集或冻结证据引用时，系统会拒绝删除。`
      : `Delete the aexp registration for ${target.name}?\n\nNo NAS files or backing SSH Resource will be deleted. The operation is refused when datasets or freezes reference it.`;
    if (window.confirm(message)) removeTarget.mutate(target.id);
  };

  return <div className="data-center-page">
    <section className="data-center-topology">
      <div><HardDrive size={20}/><strong>{zh ? "大文件留在 NAS" : "Large files stay on NAS"}</strong><span>{zh ? "Mac 只保留状态、清单和明确需要的小文件。" : "The Mac keeps status, manifests, and explicitly selected small files."}</span></div>
      <div><Server size={20}/><strong>{zh ? "AI 自动搬运" : "AI moves data automatically"}</strong><span>{zh ? "训练前送到计算节点，完成后直接收回 NAS。" : "Data is staged before training and returned directly to NAS afterwards."}</span></div>
      <div><ShieldCheck size={20}/><strong>{zh ? "需要复现时再冻结" : "Freeze only for reproducibility"}</strong><span>{zh ? "日常文件可以变化；正式实验用版本和哈希固定身份。" : "Working files may change; versions and hashes identify formal evidence."}</span></div>
    </section>

    <section className="data-center-section data-center-storage">
      <div className="section-heading"><div><HardDrive size={18}/><h2>{zh ? "NAS 存储节点" : "NAS storage nodes"}</h2></div><button className="data-center-add" onClick={() => showForm ? closeForm() : setShowForm(true)}>{showForm ? <X size={15}/> : <Plus size={15}/>} {showForm ? (zh ? "取消" : "Cancel") : (zh ? "添加 NAS" : "Add NAS")}</button></div>
      {showForm ? <form className="nas-register-form" onSubmit={(event) => { event.preventDefault(); saveNAS.mutate(); }}>
        <strong className="nas-form-title">{editingID ? (zh ? "编辑 NAS 节点" : "Edit NAS node") : (zh ? "注册 NAS 节点" : "Register NAS node")}</strong>
        <label><span>{zh ? "存储名称" : "Storage name"}</span><input value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} placeholder="feiniu" required /></label>
        <label><span>{zh ? "NAS 地址" : "NAS host"}</span><input value={form.host} onChange={(event) => setForm({ ...form, host: event.target.value })} placeholder="192.168.1.20" required /></label>
        <label><span>{zh ? "SSH 用户" : "SSH user"}</span><input value={form.user} onChange={(event) => setForm({ ...form, user: event.target.value })} required /></label>
        <label><span>{zh ? "SSH 端口" : "SSH port"}</span><input value={form.port} onChange={(event) => setForm({ ...form, port: event.target.value })} inputMode="numeric" required /></label>
        <label className="wide"><span>{zh ? "NAS 数据根目录" : "NAS data root"}</span><input value={form.root} onChange={(event) => setForm({ ...form, root: event.target.value })} placeholder="/data/aexp" required /></label>
        <label className="wide"><span>{zh ? "Mac 上的 SSH 私钥" : "SSH key on Mac"}</span><input value={form.authRef} onChange={(event) => setForm({ ...form, authRef: event.target.value })} /></label>
        <div className="nas-form-note">{zh ? "保存连接配置不会移动数据；aexp 会优先使用 NAS 上的专用密钥主动连接训练节点，并在可用时回退到训练节点主动连接。" : "Saving connection metadata moves no data; aexp prefers the NAS-side transfer identity and falls back to compute-initiated access when available."}</div>
        {saveNAS.isError ? <p className="error-text">{saveNAS.error instanceof Error ? saveNAS.error.message : String(saveNAS.error)}</p> : null}
        <button type="submit" disabled={saveNAS.isPending}>{saveNAS.isPending ? (zh ? "正在保存…" : "Saving…") : (editingID ? (zh ? "保存修改" : "Save changes") : (zh ? "创建 NAS 数据中心" : "Create NAS data center"))}</button>
      </form> : null}
      {targets.isError ? <p className="error-text data-center-notice">{String(targets.error)}</p> : null}
      {checkTarget.isError ? <p className="error-text data-center-notice">{checkTarget.error instanceof Error ? checkTarget.error.message : String(checkTarget.error)}</p> : null}
      {removeTarget.isError ? <p className="error-text data-center-notice">{removeTarget.error instanceof Error ? removeTarget.error.message : String(removeTarget.error)}</p> : null}
      <div className="storage-target-grid">
        {(targets.data?.items || []).map((target) => {
          const resource = resourceByID.get(target.resource_id);
          const health = target.health;
          const dataPlane = storageDataPlane(health);
          const checks = storageChecks(health);
          const stale = isStale(target.last_checked_at);
          return <article className="storage-target-card" key={target.id}>
            <header>
              <div><HardDrive size={18}/><div><strong>{target.name}</strong><span>storage://{target.name}/</span></div></div>
              <span className={`storage-state ${stale ? "stale" : target.status}`}>{target.status === "healthy" && !stale ? <CheckCircle2 size={14}/> : target.status === "unreachable" ? <XCircle size={14}/> : <Activity size={14}/>} {stale ? (zh ? "状态过期" : "Stale") : target.status}</span>
            </header>
            <div className="storage-target-path" title={resource ? `${resource.user}@${resource.host}:${resource.port}${target.root_path}` : target.root_path}><code>{resource ? `${resource.user}@${resource.host}:${resource.port}${target.root_path}` : target.root_path}</code><span><Clock3 size={13}/>{checkedLabel(target.last_checked_at, zh)}</span></div>
            <div className="storage-readiness">
              <div className={health?.control_plane === "healthy" ? "ok" : "bad"}><span>{zh ? "控制面 Mac → NAS" : "Control plane Mac → NAS"}</span><strong>{health ? health.control_plane : (zh ? "未检查" : "unchecked")}</strong><small>{health ? `${health.latency_ms} ms · ${health.hostname || "NAS"}` : (zh ? "检查 SSH、rsync、目录读写和容量" : "Checks SSH, rsync, root access and capacity")}</small></div>
              <div className={health?.usable ? "ok" : "bad"}><span>{zh ? "数据面 NAS ⇄ 训练节点" : "Data plane NAS ⇄ compute"}</span><strong>{health?.usable ? (zh ? "可用于同步/冻结" : "Ready for sync/freeze") : (zh ? "当前不可用" : "Not ready")}</strong><small>{health ? `${dataPlane.filter((edge) => edge.status === "healthy").length}/${dataPlane.length} ${zh ? "条路径可用（NAS 主动优先）" : "paths ready (NAS initiated preferred)"}` : (zh ? "尚未验证双向连接发起路径" : "Connection initiators not checked")}</small></div>
            </div>
            {health?.total_bytes ? <div className="storage-capacity"><div><span>{zh ? "容量" : "Capacity"}</span><strong>{bytes(health.available_bytes)} {zh ? "可用" : "free"}</strong></div><progress value={health.used_percent || 0} max={100}/><small>{bytes(health.used_bytes)} / {bytes(health.total_bytes)} · {health.used_percent}% {zh ? "已用" : "used"} · {health.filesystem}</small></div> : null}
            {health ? <div className="storage-checks">{Object.entries(checks).map(([name, check]) => <span className={check.ok ? "ok" : "bad"} key={name}>{check.ok ? <CheckCircle2 size={13}/> : <XCircle size={13}/>} {name.replaceAll("_", " ")} <small>{check.detail}</small></span>)}</div> : null}
            {dataPlane.length ? <details className="storage-data-paths"><summary>{zh ? "双向连接发起详情" : "Connection initiator details"}</summary>{dataPlane.map((edge) => <div key={edge.resource_id}><span className={`storage-state ${edge.status}`}>{edge.status === "healthy" ? <CheckCircle2 size={13}/> : <XCircle size={13}/>} {edge.resource_name} · {edge.selected_initiator === "nas" ? (zh ? "选用 NAS 主动" : "NAS initiated") : (zh ? "选用训练节点主动" : "compute initiated")}</span><small>{zh ? "NAS 主动" : "NAS initiated"}: {edge.nas_initiated?.status || "unchecked"} ({edge.nas_initiated?.latency_ms ?? "—"} ms) · {zh ? "训练节点主动" : "Compute initiated"}: {edge.compute_initiated?.status || edge.status} ({edge.compute_initiated?.latency_ms ?? edge.latency_ms} ms)</small>{edge.nas_initiated?.error ? <code>{zh ? "NAS 主动：" : "NAS initiated: "}{edge.nas_initiated.error}</code> : null}{edge.compute_initiated?.error ? <code>{zh ? "训练节点主动：" : "Compute initiated: "}{edge.compute_initiated.error}</code> : null}</div>)}</details> : null}
            {health?.control_plane !== "healthy" && (health?.error || target.last_error) ? <div className="storage-target-error"><XCircle size={14}/><span>{health?.error || target.last_error}</span></div> : null}
            <footer>
              <button onClick={() => checkTarget.mutate(target.id)} disabled={checkTarget.isPending}>{checkTarget.isPending && checkTarget.variables === target.id ? <LoaderCircle className="spin" size={14}/> : <RefreshCw size={14}/>} {zh ? "检查可用性" : "Check readiness"}</button>
              <button onClick={() => editTarget(target)}><Pencil size={14}/>{zh ? "编辑" : "Edit"}</button>
              <button className="danger" onClick={() => confirmDelete(target)} disabled={removeTarget.isPending}><Trash2 size={14}/>{zh ? "删除登记" : "Delete"}</button>
            </footer>
          </article>;
        })}
        {!targets.isPending && !(targets.data?.items || []).length ? <div className="data-center-empty">{zh ? "尚未注册 NAS。点击“添加 NAS”直接创建 SSH 资源和存储目标。" : "No NAS registered. Add one to create its SSH resource and storage target."}</div> : null}
      </div>
    </section>

    <section className="data-center-section data-center-snapshots">
      <div className="section-heading"><div><Database size={18}/><h2>{zh ? "冻结的数据版本" : "Frozen data versions"}</h2></div><span>{zh?"正式实验才需要":"Only needed for formal runs"}</span></div>
      {datasets.isError ? <p className="error-text">{String(datasets.error)}</p> : null}
      <div className="dataset-grid">
        {(datasets.data?.items || []).map((dataset) => <article className="dataset-card" key={dataset.id}>
          <div className="dataset-card-title"><div><strong>{dataset.dataset_id}@{dataset.version}</strong><span>{targetNames.get(dataset.storage_target_id) || dataset.storage_target_id} · {dataset.storage_path}</span></div><span className="storage-state">{dataset.state}</span></div>
          <div className="dataset-facts"><span>{bytes(dataset.total_bytes)}</span><span>{dataset.file_count ? `${dataset.file_count.toLocaleString()} files` : "files —"}</span><span>{dataset.manifest_sha256 ? `sha256 ${dataset.manifest_sha256.slice(0, 12)}…` : zh ? "未记录清单哈希" : "manifest hash missing"}</span></div>
          <div className="placement-list">{(dataset.materializations || []).length ? dataset.materializations.map((placement) => <div key={placement.id}><span>{resourceByID.get(placement.resource_id)?.name || placement.resource_id}</span><code>{placement.local_path}</code><span className={`storage-state ${placement.state}`}>{placement.state}</span>{placement.last_error ? <small>{placement.last_error}</small> : null}</div>) : <p>{zh ? "尚未分发到训练节点" : "Not materialized on a compute node"}</p>}</div>
        </article>)}
        {!datasets.isPending && !(datasets.data?.items || []).length ? <div className="data-center-empty">{zh ? "目前只有可变的日常文件。正式实验需要固定输入时，再把 NAS 目录冻结为带 manifest 哈希的版本。" : "Only mutable working files exist. Freeze a NAS directory with a manifest hash when a formal run needs fixed inputs."}</div> : null}
      </div>
    </section>

    <section className="data-center-section data-center-transfers">
      <div className="section-heading"><div><Activity size={18}/><h2>{zh?"后台传输":"Background transfers"}</h2></div><span>{zh?"NAS ⇄ 训练节点直传，失败自动保留进度":"NAS ⇄ compute directly, with resumable progress"}</span></div>
      <div className="dataset-grid">{(transfers.data?.items||[]).map(item=>{const job=item.job,view=transferPresentation(job);return <article className="dataset-card" key={job.id}><div className="dataset-card-title"><div><strong>{job.state}</strong><span>{bytes(job.bytes_done)} / {bytes(job.total_bytes)}</span></div><span className={`storage-state ${view.state}`}>{view.percent}%</span></div><progress max={Math.max(job.total_bytes,1)} value={job.bytes_done}/><div className="placement-list"><div><code>{item.source?.uri||"source unavailable"}</code><span>→</span><code>{item.destination?.uri||"destination unavailable"}</code></div></div>{job.last_error?<details className="transfer-error"><summary>{transferErrorSummary(job.error_code,job.last_error)}</summary><pre>{job.last_error}</pre></details>:null}<details className="transfer-mechanics"><summary>{zh?"技术详情":"Technical details"}</summary><div><code>{job.id}</code><span>{job.stage}</span><span>{item.initiator||job.initiator||"pending"} initiated</span><span>{item.payload_direction||"source_to_destination"}</span><span>local payload: {String(item.local_data_path)}</span></div></details></article>})}</div>
      {!transfers.isPending && !(transfers.data?.items||[]).length?<div className="data-center-empty">{zh?"当前没有数据在搬运。AI 在训练前后触发的同步会显示在这里。":"No data is moving. Syncs started by the AI before and after training will appear here."}</div>:null}
    </section>

    <details className="data-center-section data-center-advanced" onToggle={(event)=>setAdvancedOpen(event.currentTarget.open)}>
      <summary className="section-heading"><div><Network size={18}/><h2>{zh?"高级：路径映射":"Advanced: path mappings"}</h2></div><code>aexp://workspace/path</code></summary>
      <p className="data-center-explainer">{zh?"通常不需要配置这里。它只把稳定的 aexp:// 别名解析到某个 NAS 目录；文件内容是否可信仍由冻结版本的 manifest 哈希决定。":"Most users do not need this. It only resolves a stable aexp:// alias to a NAS directory; frozen manifest hashes still determine content identity."}</p>
      <form className="nas-register-form" onSubmit={e=>{e.preventDefault();createRoot.mutate()}}>
        <label><span>Workspace</span><input value={rootForm.workspace} onChange={e=>setRootForm({...rootForm,workspace:e.target.value})} placeholder="dam-displacement" required/></label>
        <label><span>{zh?"路径别名":"Path alias"}</span><input value={rootForm.prefix} onChange={e=>setRootForm({...rootForm,prefix:e.target.value})} placeholder="data"/></label>
        <label><span>{zh?"主存储位置":"Primary storage"}</span><select value={rootForm.storage_target_id} onChange={e=>setRootForm({...rootForm,storage_target_id:e.target.value})} required><option value="">—</option>{(targets.data?.items||[]).map(t=><option key={t.id} value={t.id}>{t.name}</option>)}</select></label>
        <label><span>{zh?"相对目录":"Relative directory"}</span><input value={rootForm.physical_root} onChange={e=>setRootForm({...rootForm,physical_root:e.target.value})} placeholder="projects/dam/data" required/></label>
        <button type="submit" disabled={createRoot.isPending}>{zh?"添加路径映射":"Add path mapping"}</button>
        {createRoot.error?<p className="error-text">{String(createRoot.error)}</p>:null}
      </form>
      <div className="dataset-grid">{(logicalRoots.data?.items||[]).map(root=><article className="dataset-card" key={root.id}><div className="dataset-card-title"><div><strong>aexp://{root.workspace}/{root.prefix}</strong><span>{targetNames.get(root.storage_target_id)||root.storage_target_id} · {root.physical_root}</span></div><button className="danger" onClick={()=>removeRoot.mutate(root.id)}><Trash2 size={14}/></button></div></article>)}</div>
      <div className="nas-register-form"><label className="wide"><span>{zh?"检查文件位置":"Inspect file location"}</span><input value={pathURI} onChange={e=>setPathURI(e.target.value)} placeholder="aexp://workspace/data/raw"/></label><button type="button" disabled={!pathURI.startsWith("aexp://")||inspectPath.isPending} onClick={()=>inspectPath.mutate()}>{zh?"检查主存储副本":"Inspect primary copy"}</button></div>
      <div className="placement-list">{(placements.data?.items||[]).map(p=>{const state=placementDisplayState(p);return <div key={p.id}><span>{resourceByID.get(p.resource_id)?.name||p.resource_id} · {p.role}</span><code>{p.physical_path}</code><span className={`storage-state ${state}`}>{state} · {p.freshness||"unknown"}</span><small>{p.observation_source||"unobserved"} · {checkedLabel(p.checked_at,zh)}</small>{p.observation_error?<small className="error-text">{p.observation_error}</small>:null}</div>})}</div>
    </details>
  </div>;
}
