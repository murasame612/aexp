import type { Locale, Run } from "./types";
import { isActiveLifecycleStatus } from "./runSync";

export type RunStatusTone = "good" | "warn" | "bad" | "neutral" | "accent";

export interface RunStatusPresentation {
  lifecycle: string;
  label: string;
  tone: RunStatusTone;
  uncertain: boolean;
  detail: string;
}

export interface RunObservationPresentation {
  freshness: string;
  source: string;
  checked: string;
  error: string;
  label: string;
}

export function runObservationPresentation(run: Run, locale: Locale): RunObservationPresentation {
  const freshness = run.status_freshness || "unknown";
  const source = run.status_source || "local_cache";
  const checked = run.status_checked_at || "-";
  const error = run.status_check_error || run.observation_error?.message || "";
  const labels = locale === "zh"
    ? [`新鲜度 ${freshness}`, `来源 ${source}`, `最后检查 ${checked}`]
    : [`freshness ${freshness}`, `source ${source}`, `last checked ${checked}`];
  if (error) labels.push(`${locale === "zh" ? "错误" : "error"} ${error}`);
  return { freshness, source, checked, error, label: labels.join(" · ") };
}

function lifecycleTone(status: string): RunStatusTone {
  if (["succeeded", "idle", "ok", "ready", "released"].includes(status)) return "good";
  if (["failed", "error", "unreachable", "cancelled", "lost", "container_expired"].includes(status)) return "bad";
  if (["running", "busy", "collecting", "transferring", "verifying"].includes(status)) return "good";
  if (["created", "queued", "preflighting", "starting", "unknown", "ssh_unreachable", "run_lost_but_events_cached"].includes(status)) return "warn";
  return "neutral";
}

export function runStatusPresentation(run: Run, locale: Locale): RunStatusPresentation {
  const lifecycle = run.lifecycle_status || run.status;
  const uncertain = isActiveLifecycleStatus(lifecycle) && (
    run.observation_state === "unreachable" ||
    run.status_freshness === "stale" ||
    Boolean(run.status_check_error)
  );
  if (!uncertain) {
    return { lifecycle, label: lifecycle, tone: lifecycleTone(lifecycle), uncertain: false, detail: "" };
  }
  const checked = run.status_checked_at
    ? `${locale === "zh" ? "最后检查" : "last checked"} ${run.status_checked_at}`
    : locale === "zh" ? "尚无可靠远端确认" : "no reliable remote observation";
  const detail = [
    `${locale === "zh" ? "上次生命周期" : "last lifecycle"}: ${lifecycle}`,
    checked,
    run.status_source || "local_cache",
    run.status_check_error || run.observation_error?.message || ""
  ].filter(Boolean).join(" · ");
  return { lifecycle, label: locale === "zh" ? "状态未知" : "status unknown", tone: "warn", uncertain: true, detail };
}
