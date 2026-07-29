import type { QueryClient } from "@tanstack/react-query";
import type { Paginated, RunChangeItem, RunSummary } from "./types";

export const runSummaryKeys = {
  root: (token: string) => ["run-summaries", token] as const,
  active: (token: string) => ["run-summaries", token, "active"] as const,
  list: (token: string, page: number, status: string, resource: string, trash: boolean, projectScope = "", kindGroup = "", query = "") =>
    ["run-summaries", token, "list", page, status, resource, trash, projectScope, kindGroup, query] as const
};

export function isActiveLifecycleStatus(status: string) {
  return ["created", "queued", "preflighting", "starting", "running", "ssh_unreachable"].includes(status);
}

export function runListEnabledForTab(tab: string) {
  return tab === "runs" || tab === "favorites";
}

export function invalidateRunQueriesOnReturn(client: QueryClient, token: string, detailRunId: string | null, visibility: DocumentVisibilityState | string) {
  if (visibility === "hidden") return false;
  void client.invalidateQueries({ queryKey: runSummaryKeys.root(token) });
  if (detailRunId) void client.invalidateQueries({ queryKey: ["run", token, detailRunId] });
  return true;
}

function newestFirst(a: RunSummary, b: RunSummary) {
  return String(b.created_at || "").localeCompare(String(a.created_at || ""));
}

function replaceExisting(page: Paginated<RunSummary> | undefined, change: RunChangeItem) {
  if (!page) return page;
  const index = page.items.findIndex((run) => run.id === change.run_id);
  if (index < 0) {
	// A cache key contains server-side project/status/kind/query filters, but
	// this event does not. Inserting an unseen run here can leak it into the
	// wrong project or make pagination totals drift. The active refetch below
	// is the authoritative way to admit a newly matching row.
	return page;
  }
  if (change.operation === "delete" || !change.run) {
    return { ...page, items: page.items.filter((run) => run.id !== change.run_id), total: Math.max(0, page.total - 1) };
  }
  const items = [...page.items];
  items[index] = change.run;
  return { ...page, items };
}

export function applyRunChange(client: QueryClient, token: string, change: RunChangeItem) {
  client.setQueryData<Paginated<RunSummary>>(runSummaryKeys.active(token), (page) => {
    const current = page || { items: [], total: 0, limit: 100, offset: 0 };
    const without = current.items.filter((run) => run.id !== change.run_id);
    if (change.operation === "delete" || !change.run || !isActiveLifecycleStatus(change.run.lifecycle_status || change.run.status)) {
      return { ...current, items: without, total: without.length };
    }
    const items = [change.run, ...without].sort(newestFirst);
    return { ...current, items, total: items.length };
  });

  client.setQueriesData<Paginated<RunSummary>>(
    { queryKey: ["run-summaries", token, "list"] },
    (page) => replaceExisting(page, change)
  );
  void client.invalidateQueries({ queryKey: ["run-summaries", token, "list"], refetchType: "active" });
  void client.invalidateQueries({ queryKey: ["run", token, change.run_id], refetchType: "active" });
	for (const key of ["run-manifest", "artifacts", "artifact-collection", "run-freezes", "run-data-bindings"] as const) {
		void client.invalidateQueries({ queryKey: [key, token, change.run_id], refetchType: "active" });
	}
  void client.invalidateQueries({ queryKey: ["stats", token], refetchType: "active" });
}
