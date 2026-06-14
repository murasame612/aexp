import { describe, expect, it } from "vitest";
import { parseEventLines } from "./events";

describe("event parsing", () => {
  it("parses metric, progress, and note events", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "metric", name: "val/loss", value: "0.123", step: 10 }),
      JSON.stringify({ type: "progress", name: "epoch", current: 2, total: 5 }),
      JSON.stringify({ type: "note", text: "halfway" })
    ]);
    expect(parsed.metrics).toHaveLength(1);
    expect(parsed.metrics[0]).toMatchObject({ name: "val/loss", value: 0.123, step: 10 });
    expect(parsed.progress[0].percent).toBe(40);
    expect(parsed.notes).toHaveLength(1);
  });

  it("keeps malformed lines as parse errors", () => {
    const parsed = parseEventLines(["{"]);
    expect(parsed.errors.length).toBe(1);
  });
});
