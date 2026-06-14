import { describe, expect, it } from "vitest";
import { parseEventLines, summarizeMetricFamilies } from "./events";

describe("event parsing", () => {
  it("parses metric, progress, and note events", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "metric", name: "val/loss", value: "0.123", step: 10, unit: "mse" }),
      JSON.stringify({ type: "progress", name: "epoch", current: 2, total: 5 }),
      JSON.stringify({ type: "note", text: "halfway" })
    ]);
    expect(parsed.metrics).toHaveLength(1);
    expect(parsed.metrics[0]).toMatchObject({ name: "val/loss", value: 0.123, step: 10, unit: "mse" });
    expect(parsed.progress[0].percent).toBe(40);
    expect(parsed.notes).toHaveLength(1);
  });

  it("keeps malformed lines as parse errors", () => {
    const parsed = parseEventLines(["{"]);
    expect(parsed.errors.length).toBe(1);
  });

  it("builds series labels from run, variant, split, and stage", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "metric", metric: "mse", value: "1.5", epoch: "2", time: "99", run: "raw", variant: "seed42", split: "val", stage: "eval" })
    ]);
    expect(parsed.metrics[0]).toMatchObject({
      name: "mse",
      value: 1.5,
      epoch: 2,
      time: 99,
      series: "raw/seed42/val/eval"
    });
  });

  it("summarizes metric families across scales and series", () => {
    const families = summarizeMetricFamilies([
      { name: "loss", value: 10, step: 1, series: "raw" },
      { name: "loss", value: 8, step: 2, series: "raw" },
      { name: "loss", value: 7, epoch: 3, series: "saits" },
      { name: "loss", value: 6, time: 4, series: "saits" },
      { name: "latency_ms", value: 1200 },
      { name: "latency_ms", value: 900 }
    ]);
    const loss = families.find((family) => family.name === "loss");
    expect(loss).toMatchObject({
      count: 4,
      min: 6,
      max: 10,
      delta: -4,
      deltaPct: -40,
      axisStart: 1,
      axisEnd: 4
    });
    expect(loss?.trend).toEqual([
      { axis: 1, value: 10 },
      { axis: 2, value: 8 },
      { axis: 3, value: 7 },
      { axis: 4, value: 6 }
    ]);
    expect(loss?.latest?.value).toBe(6);
    expect(loss?.series.map((point) => [point.series, point.value])).toEqual([
      ["raw", 8],
      ["saits", 6]
    ]);

    const latency = families.find((family) => family.name === "latency_ms");
    expect(latency).toMatchObject({ min: 900, max: 1200, delta: -300, axisStart: 0, axisEnd: 1 });
  });

  it("caps metric trend samples for dense event streams", () => {
    const dense = summarizeMetricFamilies(
      Array.from({ length: 60 }, (_, index) => ({ name: "train/loss", value: 100 - index, step: index }))
    )[0];
    expect(dense.trend).toHaveLength(24);
    expect(dense.trend[0]).toEqual({ axis: 0, value: 100 });
    expect(dense.trend[dense.trend.length - 1]).toEqual({ axis: 59, value: 41 });
  });
});
