import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import type { Paginated, RunChangeItem, RunSummary } from "./types";
import { applyRunChange, invalidateRunQueriesOnReturn, isActiveLifecycleStatus, runListEnabledForTab, runSummaryKeys } from "./runSync";

function summary(id: string, status: string): RunSummary {
  return { id, resource_id: "r", name: id, status, lifecycle_status: status, observation_state: "reachable", status_source: "remote_tmux", status_freshness: "fresh", kind: "formal", task_role: "train", evidence_grade: "formal", experiment_role: "treatment", gpu_index: 0, created_at: "2026-07-15T00:00:00Z" };
}

describe("run summary synchronization", () => {
  it("keeps the dashboard active query independent from list filters", () => {
    const token = "token";
    expect(runSummaryKeys.active(token)).not.toEqual(runSummaryKeys.list(token, 1, "succeeded", "r", false));
  });

  it("applies external lifecycle changes to the active cache", () => {
    const client = new QueryClient();
    const token = "token";
    const activeKey = runSummaryKeys.active(token);
    const filteredKey = runSummaryKeys.list(token, 1, "succeeded", "r", false);
    const visibleListKey = runSummaryKeys.list(token, 0, "", "", false);
    client.setQueryData<Paginated<RunSummary>>(activeKey, { items: [], total: 0, limit: 100, offset: 0 });
    client.setQueryData<Paginated<RunSummary>>(filteredKey, { items: [], total: 0, limit: 100, offset: 100 });
    client.setQueryData<Paginated<RunSummary>>(visibleListKey, { items: [summary("run_external", "running")], total: 1, limit: 100, offset: 0 });
    client.setQueryData(["run", token, "run_external"], { id: "run_external", status: "running" });
    client.setQueryData(["stats", token], { running: 1 });

    const running: RunChangeItem = { seq: 1, run_id: "run_external", operation: "upsert", changed_at: "2026-07-15T00:00:01Z", run: summary("run_external", "running") };
    applyRunChange(client, token, running);
    expect(client.getQueryData<Paginated<RunSummary>>(activeKey)?.items.map((run) => run.id)).toEqual(["run_external"]);
    expect(client.getQueryData<Paginated<RunSummary>>(filteredKey)?.items).toEqual([]);

    applyRunChange(client, token, { ...running, seq: 2, run: summary("run_external", "succeeded") });
    expect(client.getQueryData<Paginated<RunSummary>>(activeKey)?.items).toEqual([]);
    expect(client.getQueryData<Paginated<RunSummary>>(visibleListKey)?.items[0].status).toBe("succeeded");
    expect(client.getQueryState(["run", token, "run_external"])?.isInvalidated).toBe(true);
    expect(client.getQueryState(["stats", token])?.isInvalidated).toBe(true);
  });

  it("does not inject an unseen event into filtered first-page caches", () => {
    const client = new QueryClient();
    const token = "token";
    const projectA = runSummaryKeys.list(token, 0, "", "", false, "project-a", "experiments", "");
    client.setQueryData<Paginated<RunSummary>>(projectA, { items: [], total: 0, limit: 100, offset: 0 });
    applyRunChange(client, token, {
      seq: 3,
      run_id: "run_project_b",
      operation: "upsert",
      changed_at: "2026-07-15T00:00:01Z",
      run: { ...summary("run_project_b", "running"), project_id: "project-b" }
    });
    expect(client.getQueryData<Paginated<RunSummary>>(projectA)?.items).toEqual([]);
    expect(client.getQueryData<Paginated<RunSummary>>(projectA)?.total).toBe(0);
  });

  it("treats queued and preflighting as active without making them remote probes", () => {
    expect(["created", "queued", "preflighting", "starting", "running"].every(isActiveLifecycleStatus)).toBe(true);
    expect(isActiveLifecycleStatus("succeeded")).toBe(false);
  });

  it("gates the paginated run list without gating global active summaries", () => {
	  expect(runListEnabledForTab("dashboard")).toBe(false);
	  expect(runListEnabledForTab("resources")).toBe(false);
	  expect(runListEnabledForTab("runs")).toBe(true);
	  expect(runListEnabledForTab("favorites")).toBe(true);
  });

  it("invalidates run summaries and an open detail immediately on focus return", async () => {
	  const client = new QueryClient();
	  const token = "token";
	  client.setQueryData(runSummaryKeys.active(token), { items: [], total: 0 });
	  client.setQueryData(runSummaryKeys.list(token, 0, "", "", false), { items: [], total: 0 });
	  client.setQueryData(["run", token, "run_focus"], summary("run_focus", "running"));
	  expect(invalidateRunQueriesOnReturn(client, token, "run_focus", "hidden")).toBe(false);
	  expect(client.getQueryState(runSummaryKeys.active(token))?.isInvalidated).toBe(false);
	  expect(invalidateRunQueriesOnReturn(client, token, "run_focus", "visible")).toBe(true);
	  await Promise.resolve();
	  expect(client.getQueryState(runSummaryKeys.active(token))?.isInvalidated).toBe(true);
	  expect(client.getQueryState(["run", token, "run_focus"])?.isInvalidated).toBe(true);
  });
});
