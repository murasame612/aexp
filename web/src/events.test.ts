import { describe, expect, it } from "vitest";
import { parseEventLines, summarizeMetricFamilies, summarizeProgress } from "./events";

describe("event parsing", () => {
  it("parses metric, progress, and note events", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "metric", name: "val/loss", value: "0.123", step: 10, unit: "mse" }),
      JSON.stringify({ type: "progress", name: "epoch", current: 2, total: 5 }),
      JSON.stringify({ type: "note", text: "halfway" }),
      JSON.stringify({ type: "warning", kind: "event_quality", message: "event semantics look suspicious" })
    ]);
    expect(parsed.metrics).toHaveLength(1);
    expect(parsed.metrics[0]).toMatchObject({ name: "val/loss", value: 0.123, step: 10, unit: "mse" });
    expect(parsed.progress[0].percent).toBe(40);
    expect(parsed.notes).toHaveLength(2);
  });

  it("keeps malformed lines as parse errors", () => {
    const parsed = parseEventLines(["{"]);
    expect(parsed.errors.length).toBe(1);
  });

  it("builds series labels from run, variant, trial, split, and stage", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "metric", metric: "mse", value: "1.5", epoch: "2", time: "99", run: "raw", variant: "saits", trial: "7", split: "val", stage: "eval" })
    ]);
    expect(parsed.metrics[0]).toMatchObject({
      name: "mse",
      value: 1.5,
      epoch: 2,
      time: 99,
      series: "raw/saits/trial:7/val/eval"
    });
  });

  it("separates hyperparameter trials that emit the same metric name", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "metric", name: "train/loss", value: 0.9, epoch: 1, trial: "1" }),
      JSON.stringify({ type: "metric", name: "train/loss", value: 0.7, epoch: 1, trial: "2" }),
      JSON.stringify({ type: "metric", name: "train/loss", value: 0.6, epoch: 2, trial: "2" })
    ]);
    expect(parsed.metrics.map((metric) => metric.series)).toEqual(["trial:1", "trial:2", "trial:2"]);

    const family = summarizeMetricFamilies(parsed.metrics).find((row) => row.name === "train/loss");
    expect(family?.series.map((point) => [point.series, point.value])).toEqual([
      ["trial:1", 0.9],
      ["trial:2", 0.6]
    ]);
  });

  it("keeps baseline points separate from a continuing metric curve", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "metric", name: "val/observed_mse", value: 0.1434, epoch: 0, series: "residual_itransformer_downstream", variant: "ResidualITransformer_cnn_frozen_dam_2h_saits_clean_mask_inputs_sl192_pl96", split: "val", stage: "baseline" }),
      JSON.stringify({ type: "metric", name: "val/observed_mse", value: 0.148, epoch: 1, series: "residual_itransformer_downstream", variant: "ResidualITransformer_cnn_frozen_dam_2h_saits_clean_mask_inputs_sl192_pl96", split: "val" }),
      JSON.stringify({ type: "metric", name: "val/observed_mse", value: 0.1566, epoch: 2, series: "residual_itransformer_downstream", variant: "ResidualITransformer_cnn_frozen_dam_2h_saits_clean_mask_inputs_sl192_pl96", split: "val" }),
      JSON.stringify({ type: "metric", name: "val/observed_mse", value: 0.1538, epoch: 5, series: "residual_itransformer_downstream", variant: "ResidualITransformer_cnn_frozen_dam_2h_saits_clean_mask_inputs_sl192_pl96", split: "val" })
    ]);
    const family = summarizeMetricFamilies(parsed.metrics).find((row) => row.name === "val/observed_mse");
    expect(family?.trends).toHaveLength(2);
    expect(family?.trends.map((row) => [row.label, row.count, row.latest.value])).toEqual([
      ["baseline", 1, 0.1434],
      ["val", 3, 0.1538]
    ]);
    expect(family?.referenceTrends.map((row) => row.label)).toEqual(["baseline"]);
    expect(family?.curveTrends.map((row) => row.label)).toEqual(["val"]);
    expect(family?.trends[0].trend).toEqual([{ axis: 0, value: 0.1434 }]);
    expect(family?.trends[1].trend.map((point) => point.axis)).toEqual([1, 2, 5]);
  });

  it("splits fusion modes and keeps final metrics out of runtime curves", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "metric", name: "test/fusion_mse", value: 0.0981, epoch: 1, step: 1, split: "test", fusion: "global", series: "late_fusion_downstream", variant: "residual_wavelet_sl192_pl96", seed: 2021 }),
      JSON.stringify({ type: "metric", name: "test/fusion_mse", value: 0.0980, epoch: 2, step: 2, split: "test", fusion: "global", series: "late_fusion_downstream", variant: "residual_wavelet_sl192_pl96", seed: 2021 }),
      JSON.stringify({ type: "metric", name: "test/fusion_mse", value: 0.0979, epoch: 1, step: 1, split: "test", fusion: "channel", series: "late_fusion_downstream", variant: "residual_wavelet_sl192_pl96", seed: 2021 }),
      JSON.stringify({ type: "metric", name: "test/fusion_mse", value: 0.0978, epoch: 2, step: 2, split: "test", fusion: "channel", series: "late_fusion_downstream", variant: "residual_wavelet_sl192_pl96", seed: 2021 }),
      JSON.stringify({ type: "metric", name: "test/fusion_mse", value: 0.0980, step: 1, split: "test", fusion: "global", series: "late_fusion_downstream", variant: "residual_wavelet_sl192_pl96", stage: "final" }),
      JSON.stringify({ type: "metric", name: "test/fusion_mse", value: 0.0978, step: 2, split: "test", fusion: "channel", series: "late_fusion_downstream", variant: "residual_wavelet_sl192_pl96", stage: "final" })
    ]);
    const family = summarizeMetricFamilies(parsed.metrics).find((row) => row.name === "test/fusion_mse");
    expect(parsed.metrics.map((metric) => metric.series)).toEqual([
      "late_fusion_downstream/residual_wavelet_sl192_pl96/seed:2021/test/fusion:global",
      "late_fusion_downstream/residual_wavelet_sl192_pl96/seed:2021/test/fusion:global",
      "late_fusion_downstream/residual_wavelet_sl192_pl96/seed:2021/test/fusion:channel",
      "late_fusion_downstream/residual_wavelet_sl192_pl96/seed:2021/test/fusion:channel",
      "late_fusion_downstream/residual_wavelet_sl192_pl96/test/final/fusion:global",
      "late_fusion_downstream/residual_wavelet_sl192_pl96/test/final/fusion:channel"
    ]);
    expect(family?.curveTrends.map((row) => [row.label, row.count])).toEqual([
      ["global · test", 2],
      ["channel · test", 2]
    ]);
    expect(family?.referenceTrends.map((row) => [row.label, row.count])).toEqual([
      ["global · final · test", 1],
      ["channel · final · test", 1]
    ]);
  });

  it("normalizes legacy metric names that embed long series context", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "metric", name: "WaveletITransformer_dam_2h_raw_linear_mask_inputs_sl96_pl48/train_loss", value: 0.08, epoch: 1 }),
      JSON.stringify({ type: "metric", name: "WaveletITransformer_dam_2h_raw_linear_mask_inputs_sl96_pl48/val_observed_mse", value: 0.12, epoch: 1 })
    ]);

    expect(parsed.metrics.map((metric) => [metric.name, metric.series, metric.value])).toEqual([
      ["train/loss", "WaveletITransformer_dam_2h_raw_linear_mask_inputs_sl96_pl48", 0.08],
      ["val/observed_mse", "WaveletITransformer_dam_2h_raw_linear_mask_inputs_sl96_pl48", 0.12]
    ]);
  });

  it("keeps split metric names as leaf metrics and moves experiment prefixes into series", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "metric", name: "test/observed_mae", value: 0.18, epoch: 1 }),
      JSON.stringify({ type: "metric", name: "itransformer_downstream/dam_2h_saits_clean_mask_inputs/test/final", value: 0.17 }),
      JSON.stringify({ type: "metric", name: "trial/saits_clean/test_observed_mae", value: 0.176 })
    ]);
    expect(parsed.metrics.map((metric) => [metric.name, metric.series, metric.value])).toEqual([
      ["test/observed_mae", undefined, 0.18],
      ["test/final", "itransformer_downstream/dam_2h_saits_clean_mask_inputs", 0.17],
      ["test/observed_mae", "trial/saits_clean", 0.176]
    ]);
  });

  it("does not turn numeric params into metric series", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "param", name: "epochs", value: 30 }),
      JSON.stringify({ type: "param", name: "batch_size", value: 16 }),
      JSON.stringify({ type: "metric", name: "train/loss", value: 0.12, epoch: 1 })
    ]);
    expect(parsed.metrics).toHaveLength(1);
    expect(parsed.params).toEqual([
      { name: "batch_size", value: "16", series: undefined, time: undefined },
      { name: "epochs", value: "30", series: undefined, time: undefined }
    ]);
    expect(parsed.metrics[0]).toMatchObject({ name: "train/loss", value: 0.12 });
    expect(parsed.latestMetrics.map((metric) => metric.name)).toEqual(["train/loss"]);
  });

  it("keeps the latest value for repeated params", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "param", name: "learning_rate", value: "1e-3", series: "raw" }),
      JSON.stringify({ type: "param", name: "learning_rate", value: "5e-4", series: "raw" }),
      JSON.stringify({ type: "parameter", name: "seed", value: 42 })
    ]);
    expect(parsed.params).toEqual([
      { name: "learning_rate", value: "5e-4", series: "raw", time: undefined },
      { name: "seed", value: "42", series: undefined, time: undefined }
    ]);
  });

  it("keeps progress context and summarizes repeated updates", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "progress", name: "epoch", current: 4, total: 30, series: "iTransformer/raw", stage: "train", label: "raw input", time: 1 }),
      JSON.stringify({ type: "progress", name: "epoch", current: 5, total: 30, series: "iTransformer/raw", stage: "train", label: "raw input", time: 2 }),
      JSON.stringify({ type: "progress", name: "epoch", current: 2, total: 30, series: "iTransformer/saits", stage: "train", label: "saits input", time: 3 })
    ]);
    expect(parsed.progress[0]).toMatchObject({
      name: "epoch",
      series: "iTransformer/raw/train",
      label: "raw input"
    });
    const summary = summarizeProgress(parsed.progress);
    expect(summary).toHaveLength(2);
    expect(summary.map((row) => [row.name, row.series, row.latest.current, row.count])).toEqual([
      ["epoch", "iTransformer/saits/train", 2, 1],
      ["epoch", "iTransformer/raw/train", 5, 2]
    ]);
  });

  it("splits legacy slash progress names into context and dimension", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "progress", name: "iTransformer/raw/epoch", current: 8, total: 30 }),
      JSON.stringify({ type: "progress", name: "iTransformer/raw/epoch", current: 9, total: 30 })
    ]);
    expect(parsed.progress[0]).toMatchObject({
      name: "epoch",
      series: "iTransformer/raw"
    });
    const summary = summarizeProgress(parsed.progress);
    expect(summary).toHaveLength(1);
    expect(summary[0]).toMatchObject({
      name: "epoch",
      series: "iTransformer/raw",
      count: 2
    });
    expect(summary[0].latest.current).toBe(9);
  });

  it("treats explicit early-stopped training progress as terminal without forcing current to total", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "progress", name: "epoch", current: 62, total: 100, status: "early_stopped", best_epoch: 32, stage: "train" })
    ]);
    const summary = summarizeProgress(parsed.progress);
    expect(summary).toHaveLength(1);
    expect(summary[0].done).toBe(true);
    expect(summary[0].latest).toMatchObject({
      name: "epoch",
      current: 62,
      total: 100,
      status: "early_stopped",
      best_epoch: 32
    });
  });

  it("keeps optuna trial-local epoch progress isolated by trial context", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "progress", name: "trial", current: 2, total: 12 }),
      JSON.stringify({ type: "progress", name: "epoch", current: 7, total: 30, status: "early_stopped", best_epoch: 4, trial: "trial001", variant: "lr=1e-3" }),
      JSON.stringify({ type: "progress", name: "epoch", current: 11, total: 30, status: "running", trial: "trial002", variant: "lr=3e-4" })
    ]);
    const summary = summarizeProgress(parsed.progress);
    const trialOne = summary.find((row) => row.series === "lr=1e-3/trial:trial001");
    const trialTwo = summary.find((row) => row.series === "lr=3e-4/trial:trial002");
    const sweep = summary.find((row) => row.name === "trial");
    expect(trialOne?.done).toBe(true);
    expect(trialOne?.latest.current).toBe(7);
    expect(trialTwo?.done).toBe(false);
    expect(trialTwo?.latest.current).toBe(11);
    expect(sweep?.done).toBe(false);
  });

  it("summarizes metric families by metric name, unit, and series", () => {
    const families = summarizeMetricFamilies([
      { name: "loss", value: 10, step: 1, series: "raw", unit: "mse" },
      { name: "loss", value: 8, step: 2, series: "raw", unit: "mse" },
      { name: "loss", value: 7, epoch: 3, series: "saits", unit: "mse" },
      { name: "loss", value: 6, time: 4, series: "saits", unit: "mse" },
      { name: "loss", value: 92, step: 1, series: "raw", unit: "%" },
      { name: "loss", value: 95, step: 2, series: "raw", unit: "%" },
      { name: "latency_ms", value: 1200 },
      { name: "latency_ms", value: 900 },
      { name: "train/loss", value: 0.012 },
      { name: "train/loss", value: 0.008 }
    ]);
    const loss = families.find((family) => family.name === "loss" && family.unit === "mse");
    expect(loss).toMatchObject({
      unit: "mse",
      axisKind: "step",
      scaleKey: "mse",
      count: 4,
      min: 6,
      max: 10,
      delta: -4,
      deltaPct: -40,
      axisStart: 1,
      axisEnd: 3
    });
    expect(loss?.trend).toEqual([
      { axis: 1, value: 10 },
      { axis: 2, value: 8 },
      { axis: 2, value: 7 },
      { axis: 3, value: 6 }
    ]);
    expect(loss?.points.map((point) => [point.series, point.value])).toEqual([
      ["raw", 10],
      ["raw", 8],
      ["saits", 7],
      ["saits", 6]
    ]);
    expect(loss?.latest?.value).toBe(6);
    expect(loss?.series.map((point) => [point.series, point.value])).toEqual([
      ["raw", 8],
      ["saits", 6]
    ]);

    const percentLoss = families.find((family) => family.name === "loss" && family.unit === "%");
    expect(percentLoss).toMatchObject({ count: 2, min: 92, max: 95, delta: 3, scaleKey: "%" });

    const latency = families.find((family) => family.name === "latency_ms");
    expect(latency).toMatchObject({ axisKind: "sample", scaleKey: "value:1e3", scaleLabel: "value 1e3", min: 900, max: 1200, delta: -300, axisStart: undefined, axisEnd: undefined });
    expect(latency?.trend).toEqual([]);

    const trainLoss = families.find((family) => family.name === "train/loss");
    expect(trainLoss).toMatchObject({ scaleKey: "value:1e-2", scaleLabel: "value 1e-2", min: 0.008, max: 0.012 });
  });

  it("does not draw a curve for repeated final metrics without a varying axis", () => {
    const parsed = parseEventLines([
      JSON.stringify({ type: "metric", name: "trial/raw/test_observed_mae", value: 0.18, epoch: 1 }),
      JSON.stringify({ type: "metric", name: "trial/raw/test_observed_mae", value: 0.17, epoch: 1 }),
      JSON.stringify({ type: "metric", name: "trial/saits/test_observed_mae", value: 0.16, epoch: 1 })
    ]);
    const family = summarizeMetricFamilies(parsed.metrics).find((row) => row.name === "test/observed_mae");
    expect(family).toMatchObject({
      axisKind: "sample",
      count: 3,
      axisStart: undefined,
      axisEnd: undefined
    });
    expect(family?.trend).toEqual([]);
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
