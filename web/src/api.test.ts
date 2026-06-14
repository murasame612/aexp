import { describe, expect, it } from "vitest";
import { unwrapPaginated } from "./api";
import type { Run } from "./types";

describe("api helpers", () => {
  it("keeps legacy array responses usable as paginated payloads", () => {
    const runs = [{ id: "run_1", resource_id: "rsrc", status: "running", command: "echo ok" }] as Run[];
    expect(unwrapPaginated(runs)).toEqual({ items: runs, total: 1, limit: 1, offset: 0 });
  });

  it("passes through explicit paginated responses", () => {
    const payload = { items: [] as Run[], total: 42, limit: 20, offset: 40 };
    expect(unwrapPaginated(payload)).toBe(payload);
  });
});
