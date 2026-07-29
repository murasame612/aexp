import type { Paginated, Run } from "./types";

export function replaceRunInPage(page: Paginated<Run> | undefined, updated: Run): Paginated<Run> | undefined {
  if (!page) return page;
  const index = page.items.findIndex((run) => run.id === updated.id);
  if (index < 0) return page;
  const items = page.items.slice();
  items[index] = updated;
  return { ...page, items };
}
