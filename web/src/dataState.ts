import type { PathPlacement, Run, TransferJob } from "./types";

export const placementStates = ["present", "missing", "unknown", "unreachable", "conflict"] as const;
export const transferStates = ["queued", "planning", "transferring", "verifying", "promoting", "completed", "blocked", "failed", "cancelled"] as const;

export function placementDisplayState(placement: Pick<PathPlacement, "observed_state" | "freshness">) {
  return placement.freshness === "stale" ? "stale" : (placement.observed_state || "unknown");
}

export function transferPresentation(job: Pick<TransferJob, "state" | "bytes_done" | "total_bytes">) {
  const terminal = ["completed", "blocked", "failed", "cancelled"].includes(job.state);
  const percent = Math.round(100 * (job.bytes_done || 0) / Math.max(job.total_bytes || 1, 1));
  return { state: job.state || "queued", terminal, percent: Math.max(0, Math.min(100, percent)) };
}

export function transferErrorSummary(errorCode?: string, lastError?: string, maxLength = 180) {
  const firstLine = (lastError || "").split(/\r?\n/, 1)[0].trim();
  const prefix = errorCode ? `${errorCode}: ` : "";
  const available = Math.max(24, maxLength - prefix.length);
  const compact = firstLine.length > available ? `${firstLine.slice(0, available - 1)}…` : firstLine;
  return `${prefix}${compact || "Transfer failed"}`;
}

export function runDataFinalizationPresentation(run: Pick<Run, "status" | "data_finalization_state">) {
  const state = run.data_finalization_state || "legacy / unknown";
  const archiveProblem = run.status === "succeeded" && ["blocked", "failed"].includes(state);
  return {
    state,
    archiveProblem,
    message: archiveProblem ? "Computation succeeded; durable data finalization is incomplete." : ""
  };
}
