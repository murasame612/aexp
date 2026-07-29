import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { replaceRunInPage } from "./runCache";
import { runSummaryKeys } from "./runSync";
import type { Paginated, Run } from "./types";

describe("React Query run status interaction", () => {
  it("updates detail and every cached list page immediately after status-check", () => {
    const client = new QueryClient();
    const token = "token";
    const running: Run = { id: "run_1", resource_id: "r", status: "running", command: "train" };
    const other: Run = { id: "run_2", resource_id: "r", status: "running", command: "other" };
    client.setQueryData(["run", token, running.id], running);
    const firstPage = runSummaryKeys.list(token, 0, "", "", false);
    const secondPage = runSummaryKeys.list(token, 1, "", "", false);
    client.setQueryData<Paginated<Run>>(firstPage, { items: [running], total: 2, limit: 1, offset: 0 });
    client.setQueryData<Paginated<Run>>(secondPage, { items: [other], total: 2, limit: 1, offset: 1 });
    const succeeded = { ...running, status: "succeeded", status_freshness: "fresh" };

    client.setQueryData(["run", token, running.id], succeeded);
    client.setQueriesData<Paginated<Run>>({ queryKey: ["run-summaries", token, "list"] }, (page) => replaceRunInPage(page, succeeded));

    expect(client.getQueryData<Run>(["run", token, running.id])?.status).toBe("succeeded");
    expect(client.getQueryData<Paginated<Run>>(firstPage)?.items[0].status).toBe("succeeded");
    expect(client.getQueryData<Paginated<Run>>(secondPage)?.items[0]).toBe(other);
  });
});
