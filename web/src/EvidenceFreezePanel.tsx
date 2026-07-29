import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, Database, FileCheck2, LoaderCircle, ShieldX } from "lucide-react";
import { createEvidenceSnapshot, createRunFreeze, evaluateEvidenceRelease, getEvidenceReleases, getEvidenceSnapshots, getRunFreezeManifest, getRunFreezes, planRunFreeze } from "./api";
import type { EvidenceSnapshot, Run, RunFreeze, RunFreezePlan } from "./types";

const active = (state:string) => ["queued","collecting","transferring","verifying","frozen","aggregating","gate_checking"].includes(state);
const bytes = (n:number) => n ? `${(n/1024/1024).toFixed(n>1024*1024*1024?1:0)} MiB` : "0 B";
export const selectRunFreeze=(history:RunFreeze[],id:string|null)=>history.find(f=>f.id===id)||history[0];

export function EvidenceFreezePanel({token,run}:{token:string;run:Run}) {
  const qc=useQueryClient(); const [to,setTo]=useState(""); const [workspace,setWorkspace]=useState(""); const [config,setConfig]=useState(""); const [plan,setPlan]=useState<RunFreezePlan|null>(null); const [selectedFreezeID,setSelectedFreezeID]=useState<string|null>(null);
  const snapshots=useQuery({queryKey:["evidence-snapshots",token,run.id],queryFn:()=>getEvidenceSnapshots(token,run.id)});
  const createSnapshot=useMutation({mutationFn:()=>createEvidenceSnapshot(token,run.id),onSuccess:async()=>{await qc.invalidateQueries({queryKey:["evidence-snapshots",token,run.id]})}});
  const freezes=useQuery({queryKey:["run-freezes",token,run.id],queryFn:()=>getRunFreezes(token,run.id),refetchInterval:(q)=>((q.state.data||[]).some(f=>active(f.state))?1500:10000)});
  const latest=selectRunFreeze(freezes.data||[],selectedFreezeID);
  const manifest=useQuery({queryKey:["freeze-manifest",token,latest?.id],queryFn:()=>getRunFreezeManifest(token,latest!.id),enabled:!!latest&&!active(latest.state),retry:false});
  const planMutation=useMutation({mutationFn:()=>planRunFreeze(token,run.id,{profile:"paper",to:to||undefined,workspace:workspace||undefined,project_config:config||undefined}),onSuccess:setPlan});
  const createMutation=useMutation({mutationFn:()=>createRunFreeze(token,run.id,{profile:"paper",to:to||undefined,workspace:workspace||undefined,project_config:config||undefined,expected_plan_hash:plan!.plan_sha256}),onSuccess:async(f)=>{setSelectedFreezeID(f.id);await qc.invalidateQueries({queryKey:["run-freezes",token,run.id]})}});
  const history=freezes.data||[];
  return <div className="freeze-panel">
    <section className="snapshot-primary"><header><div><span className="panel-kicker">Evidence Snapshot</span><h3><FileCheck2 size={18}/> 冻结已发布证据引用</h3></div><button className="primary" disabled={createSnapshot.isPending} onClick={()=>createSnapshot.mutate()}>{createSnapshot.isPending?"创建中…":"创建 Snapshot"}</button></header><p>Snapshot 只引用 final RunManifest 与已经校验发布的输出 revision；不会重新扫描、复制或传输文件。</p>{createSnapshot.error?<div className="freeze-error">{String(createSnapshot.error)}</div>:null}{(snapshots.data||[]).map(snapshot=><SnapshotRecord token={token} snapshot={snapshot} key={snapshot.id}/>)}{!snapshots.isPending&&!(snapshots.data||[]).length?<div className="muted">还没有 Snapshot。若输出尚未完成发布，系统会返回明确 blocker。</div>:null}</section>
    <details className="legacy-freeze-tools"><summary>Legacy Freeze（仅历史 artifact 兼容）</summary><div className="legacy-freeze-tools-body">
    <section className="freeze-readiness"><h3><FileCheck2 size={18}/> Legacy paper Freeze readiness</h3><div className="freeze-facts"><span>status <b>{run.status}</b></span><span>grade <b>{run.evidence_grade||"missing"}</b></span><span>config <b>{run.project_config_sha256?"captured":"missing"}</b></span><span>dataset <b>{run.datasets_json&&run.datasets_json!=="[]"?"captured":"missing"}</b></span><span>seeds <b>{run.seeds_json&&run.seeds_json!=="[]"?"captured":"missing"}</b></span></div></section>
    <section className="freeze-form"><label>NAS destination<input value={to} onChange={e=>setTo(e.target.value)} placeholder="aexp://project/paper-evidence"/></label><label>Paper workspace (optional projection)<input value={workspace} onChange={e=>setWorkspace(e.target.value)} placeholder="/path/to/paper/evidence/runs"/></label><label>Project config<input value={config} onChange={e=>setConfig(e.target.value)} placeholder="/path/to/project/.aexp.yaml"/></label><button disabled={planMutation.isPending} onClick={()=>planMutation.mutate()}>{planMutation.isPending?"Planning…":"Plan paper freeze"}</button></section>
    {planMutation.error?<div className="freeze-error">{String(planMutation.error)}</div>:null}
    {plan?<section className={`freeze-plan ${plan.eligible?"eligible":"blocked"}`}><header>{plan.eligible?<CheckCircle2 size={18}/>:<ShieldX size={18}/>}<b>{plan.eligible?"Ready to freeze":"Blocked"}</b><span>{plan.file_count} files · {bytes(plan.total_bytes)}</span></header><div className="freeze-topology">{plan.transfer_path} · local payload: {String(plan.local_data_path)}</div>{plan.blockers.map((b,i)=><div className="freeze-blocker" key={`${b.code}-${i}`}><AlertTriangle size={14}/><code>{b.code}</code><span>{b.message}</span></div>)}<div className="freeze-role-list">{Object.entries(plan.files.reduce<Record<string,number>>((m,f)=>(m[f.role]=(m[f.role]||0)+1,m),{})).map(([r,n])=><span key={r}>{r} <b>{n}</b></span>)}</div><button disabled={!plan.eligible||createMutation.isPending} onClick={()=>createMutation.mutate()}>{createMutation.isPending?"Starting…":"Freeze evidence"}</button></section>:null}
    {createMutation.error?<div className="freeze-error">{String(createMutation.error)}</div>:null}
    {latest?<section className={`freeze-status ${latest.state}`}><header>{active(latest.state)?<LoaderCircle className="spin" size={18}/>:latest.state==="released"?<CheckCircle2 size={18}/>:<Database size={18}/>}<b>{latest.state}</b><span>{latest.stage}</span></header><progress max={Math.max(latest.total_bytes,1)} value={latest.bytes_done}/><small>{latest.files_done}/{latest.file_count} files · {bytes(latest.bytes_done)}/{bytes(latest.total_bytes)}</small>{latest.raw_transfer_id?<small>raw transfer <code>{latest.raw_transfer_id}</code></small>:null}{latest.workspace_transfer_id?<small>workspace transfer <code>{latest.workspace_transfer_id}</code></small>:null}{latest.state==="blocked"?<div className="freeze-warning">Raw evidence is frozen, but it is not releasable for the paper.</div>:null}{latest.last_error?<pre>{latest.error_code}: {latest.last_error}</pre>:null}{latest.raw_manifest_sha256?<code>{latest.raw_manifest_sha256}</code>:null}</section>:null}
    {manifest.data?<details><summary>Immutable file manifest ({manifest.data.files.length})</summary><div className="freeze-files">{manifest.data.files.map(f=><div key={f.id}><span>{f.kind}/{f.role}</span><code>{f.relative_path}</code><code>{f.sha256.slice(0,20)}…</code></div>)}</div></details>:null}
    {history.length?<section><h3>Freeze history</h3>{history.map(f=><button className="freeze-history" key={f.id} onClick={()=>setSelectedFreezeID(f.id)}><code>{f.id}</code><span>{f.state}</span><span>{new Date(f.created_at).toLocaleString()}</span></button>)}</section>:null}
    </div></details>
  </div>;
}

function SnapshotRecord({token,snapshot}:{token:string;snapshot:EvidenceSnapshot}) {
  const qc=useQueryClient();
  const releases=useQuery({queryKey:["evidence-releases",token,snapshot.id],queryFn:()=>getEvidenceReleases(token,snapshot.id)});
  const evaluate=useMutation({mutationFn:()=>evaluateEvidenceRelease(token,snapshot.id),onSuccess:async()=>{await qc.invalidateQueries({queryKey:["evidence-releases",token,snapshot.id]})}});
  const latest=releases.data?.[0];
  return <div className="snapshot-record"><div><b>{snapshot.id}</b><span>{new Date(snapshot.created_at).toLocaleString()}</span></div><code>{snapshot.manifest_sha256}</code><div className="snapshot-release"><span className={`snapshot-release-state ${latest?.state||"draft"}`}>{latest?.state||"not released"}</span><button disabled={evaluate.isPending} onClick={()=>evaluate.mutate()}>{evaluate.isPending?"Gate…":"运行 Release Gate"}</button></div>{evaluate.error?<div className="freeze-error">{String(evaluate.error)}</div>:latest?.last_error?<small>{latest.last_error}</small>:null}</div>;
}
