import { useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Clock3, Power, Printer, RefreshCw, TestTube2, XCircle } from "lucide-react";
import { configurePrinter, getPrinterJobs, getPrinterStatus, retryPrinterJob, testPrinter } from "./api";
import type { Locale, PrinterJob } from "./types";

function printStateLabel(state: PrinterJob["state"], zh: boolean) {
  const labels: Record<PrinterJob["state"], [string, string]> = {
    queued: ["等待提交", "Queued"], submitting: ["正在提交", "Submitting"], spooled: ["已进入 CUPS 队列", "Spooled to CUPS"],
    failed: ["提交失败", "Failed"], uncertain: ["结果不确定", "Outcome uncertain"]
  };
  return labels[state]?.[zh ? 0 : 1] || state;
}

export function SettingsPage({ token, locale }: { token: string; locale: Locale }) {
  const zh = locale === "zh";
  const queryClient = useQueryClient();
  const status = useQuery({ queryKey: ["printer-status", token], queryFn: () => getPrinterStatus(token), refetchInterval: 12_000 });
  const jobs = useQuery({
    queryKey: ["printer-jobs", token], queryFn: () => getPrinterJobs(token, 20),
    refetchInterval: (query) => query.state.data?.items.some((job) => job.state === "queued" || job.state === "submitting") ? 1_500 : 12_000
  });
  const invalidate = () => Promise.all([
    queryClient.invalidateQueries({ queryKey: ["printer-status", token] }),
    queryClient.invalidateQueries({ queryKey: ["printer-jobs", token] })
  ]);
  const configure = useMutation({
    mutationFn: (enabled: boolean) => configurePrinter(token, enabled, status.data?.queue || "Printer_POS_80"), onSuccess: invalidate
  });
  const test = useMutation({ mutationFn: () => testPrinter(token), onSuccess: invalidate });
  const retry = useMutation({ mutationFn: (id: string) => retryPrinterJob(token, id), onSuccess: invalidate });
  const error = [status.error, jobs.error, configure.error, test.error, retry.error].find(Boolean);
  const summary = useMemo(() => {
    if (!status.data) return zh ? "正在读取本机打印服务…" : "Reading local print service…";
    if (!status.data.enabled) return zh ? "自动小票已停用" : "Automatic receipts are disabled";
    if (!status.data.available) return zh ? "自动小票已启用，但打印队列当前不可用" : "Automatic receipts are enabled, but the queue is unavailable";
    return zh ? "实验开始和结束时各打印一张并切纸" : "Print and cut once when a run starts and once when it ends";
  }, [status.data, zh]);

  return <div className="settings-page">
    <section className="printer-panel">
      <header className="printer-panel-head">
        <div className="printer-title"><Printer size={20}/><div><h2>{zh ? "实验小票" : "Experiment receipts"}</h2><p>{summary}</p></div></div>
        <span className={`printer-availability ${status.data?.available ? "good" : "bad"}`}>
          {status.data?.available ? <CheckCircle2 size={15}/> : <XCircle size={15}/>} {status.data?.queue || "Printer_POS_80"} · {status.data?.queue_state || "unknown"}
        </span>
      </header>
      <div className="printer-actions">
        <button onClick={() => configure.mutate(!status.data?.enabled)} disabled={!status.data || configure.isPending}>
          <Power size={15}/>{status.data?.enabled ? (zh ? "停用自动小票" : "Disable receipts") : (zh ? "启用未来实验" : "Enable future runs")}
        </button>
        <button onClick={() => test.mutate()} disabled={test.isPending}><TestTube2 size={15}/>{test.isPending ? (zh ? "正在排队…" : "Queuing…") : (zh ? "打印并切纸测试票" : "Print and cut test")}</button>
        <button onClick={invalidate}><RefreshCw size={15}/>{zh ? "刷新状态" : "Refresh"}</button>
      </div>
      <div className="printer-safety-note">
        {zh ? "打印由本机 aexp 服务统一排队；并发实验不会同时争抢打印机。打印失败不会影响、取消或改变实验状态。“已进入 CUPS 队列”不等于已确认物理出纸。首次启用只监听之后的新事件，不补打历史实验。" : "The local aexp service serializes all receipts, so concurrent runs do not compete for the printer. Print failures never change or cancel a run. “Spooled” means CUPS accepted the job, not confirmed physical delivery. First enable listens only to future events."}
      </div>
      {error ? <p className="printer-error">{error instanceof Error ? error.message : String(error)}</p> : null}
      <div className="printer-counters">
        <span><Clock3 size={14}/>{status.data?.queued_jobs || 0} {zh ? "等待" : "queued"}</span>
        <span><XCircle size={14}/>{status.data?.failed_jobs || 0} {zh ? "失败" : "failed"}</span>
        <span>{status.data?.uncertain_jobs || 0} {zh ? "不确定" : "uncertain"}</span>
      </div>
    </section>

    <section className="printer-jobs-panel">
      <header><h2>{zh ? "最近打印任务" : "Recent print jobs"}</h2><span>{zh ? "独立于实验状态" : "Independent from run state"}</span></header>
      <div className="printer-job-list">
        {(jobs.data?.items || []).map((job) => <article key={job.id} className={`printer-job ${job.state}`}>
          <div className="printer-job-main"><strong>{job.phase === "start" ? (zh ? "开始票" : "START") : job.phase === "end" ? (zh ? "结束票" : "END") : (zh ? "测试票" : "TEST")}</strong><span>{job.run_id || job.id}</span></div>
          <span className="printer-job-state">{printStateLabel(job.state, zh)}</span>
          <code>{job.cups_job_id || job.queue}</code>
          {(job.state === "failed" || job.state === "uncertain") ? <button onClick={() => retry.mutate(job.id)} disabled={retry.isPending}><RefreshCw size={13}/>{zh ? "重试" : "Retry"}</button> : null}
          {job.last_error ? <p>{job.last_error}</p> : null}
        </article>)}
        {!jobs.isPending && !(jobs.data?.items || []).length ? <div className="printer-empty">{zh ? "还没有打印任务。" : "No print jobs yet."}</div> : null}
      </div>
    </section>
  </div>;
}
