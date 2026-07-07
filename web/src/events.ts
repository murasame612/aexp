import type { MetricPoint, ParamPoint, ParsedEvents, ProgressPoint, UIEvent } from "./types";
import { latestMetricByName } from "./utils";

export interface MetricFamilySummary {
  name: string;
  unit?: string;
  scaleKey: string;
  scaleLabel: string;
  count: number;
  axisKind: "epoch" | "step" | "sample";
  first?: MetricPoint;
  latest?: MetricPoint;
  min: number;
  max: number;
  delta: number;
  deltaPct?: number;
  axisStart?: number;
  axisEnd?: number;
  trend: Array<{ axis: number; value: number }>;
  points: MetricPoint[];
  series: MetricPoint[];
  trends: MetricSeriesSummary[];
  curveTrends: MetricSeriesSummary[];
  referenceTrends: MetricSeriesSummary[];
}

export interface MetricSeriesSummary {
  key: string;
  label: string;
  fullLabel: string;
  latest: MetricPoint;
  count: number;
  role: "curve" | "reference";
  trend: Array<{ axis: number; value: number }>;
  points: MetricPoint[];
}

export interface ProgressSummary {
  key: string;
  name: string;
  series?: string;
  label?: string;
  latest: ProgressPoint;
  count: number;
  done: boolean;
}

function asNumber(value: unknown): number | undefined {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string" && value.trim()) {
    const parsed = Number(value);
    if (Number.isFinite(parsed)) return parsed;
  }
  return undefined;
}

function eventName(ev: UIEvent): string {
  return String(ev.name || ev.metric || ev.key || ev.label || "").trim();
}

function splitLegacyContextName(name: string, series?: string): { name: string; series?: string } {
  if (series || !name.includes("/")) return { name, series };
  const parts = name.split("/").map((part) => part.trim()).filter(Boolean);
  if (parts.length < 2) return { name, series };
  const leaf = parts[parts.length - 1];
  const previous = parts[parts.length - 2];
  const identity = metricLeafIdentity(previous, leaf);
  const contextParts = identity.consumedPrevious ? parts.slice(0, -2) : parts.slice(0, -1);
  if (!contextParts.length) return { name: identity.name, series };
  const context = contextParts.join("/");
  if (!looksLikeExperimentContext(context, parts)) return { name, series };
  return { name: identity.name, series: context };
}

function normalizeMetricLeaf(name: string): string {
  const match = name.match(/^(train|val|valid|validation|test|eval)_(.+)$/i);
  if (!match) return name;
  const split = match[1].toLowerCase();
  return `${split === "valid" || split === "validation" ? "val" : split}/${match[2]}`;
}

function metricLeafIdentity(previous: string | undefined, leaf: string): { name: string; consumedPrevious: boolean } {
  const normalized = normalizeMetricLeaf(leaf);
  if (normalized !== leaf) return { name: normalized, consumedPrevious: false };
  const prev = String(previous || "").trim().toLowerCase();
  if (/^(train|val|valid|validation|test|eval)$/.test(prev)) {
    return { name: `${prev === "valid" || prev === "validation" ? "val" : prev}/${leaf}`, consumedPrevious: true };
  }
  return { name: leaf, consumedPrevious: false };
}

function looksLikeExperimentContext(context: string, parts: string[]): boolean {
  if (!context) return false;
  if (context.length > 20 || parts.length > 2) return true;
  return /[_\s-]/.test(context) && !/^(train|val|valid|validation|test|eval)$/i.test(context);
}

function eventContext(ev: UIEvent, key: string, prefix?: string): string | undefined {
  const value = String(ev[key] || "").trim();
  if (!value) return undefined;
  return prefix ? `${prefix}:${value}` : value;
}

function eventSeries(ev: UIEvent): string | undefined {
  const rawParts = [
    eventContext(ev, "series"),
    eventContext(ev, "run"),
    eventContext(ev, "variant"),
    eventContext(ev, "trial", "trial"),
    eventContext(ev, "seed", "seed"),
    eventContext(ev, "fold", "fold"),
    eventContext(ev, "split"),
    eventContext(ev, "stage"),
    eventContext(ev, "fusion", "fusion")
  ].filter(Boolean) as string[];
  const parts = rawParts.filter((part, index) => rawParts.indexOf(part) === index);
  return parts.length ? parts.join("/") : undefined;
}

function eventUnit(ev: UIEvent): string | undefined {
  const unit = String(ev.unit || ev.units || ev.value_unit || ev.valueUnit || "").trim();
  return unit || undefined;
}

function eventLabel(ev: UIEvent): string | undefined {
  const hasExplicitName = String(ev.name || ev.metric || ev.key || "").trim() !== "";
  const label = String(ev.title || ev.label_text || ev.display || ev.caption || (hasExplicitName ? ev.label : "") || "").trim();
  return label || undefined;
}

function eventValue(ev: UIEvent): string {
  const raw = ev.value ?? ev.text ?? ev.message ?? "";
  if (typeof raw === "string") return raw;
  if (typeof raw === "number" || typeof raw === "boolean") return String(raw);
  if (raw == null) return "";
  try {
    return JSON.stringify(raw);
  } catch {
    return String(raw);
  }
}

export function parseEventLines(lines: string[]): ParsedEvents {
  const events: UIEvent[] = [];
  const metrics: MetricPoint[] = [];
  const params: ParamPoint[] = [];
  const progress: ProgressPoint[] = [];
  const notes: UIEvent[] = [];
  const errors: string[] = [];

  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    let ev: UIEvent;
    try {
      ev = JSON.parse(trimmed) as UIEvent;
    } catch (err) {
      errors.push(err instanceof Error ? err.message : String(err));
      continue;
    }
    events.push(ev);
    const typ = String(ev.type || "").toLowerCase();
    const name = eventName(ev);
    const value = asNumber(ev.value);
    if ((typ === "param" || typ === "parameter") && name) {
      params.push({
        name,
        value: eventValue(ev),
        time: asNumber(ev.time),
        series: eventSeries(ev)
      });
      continue;
    }
    if ((typ === "metric" || typ === "metrics" || typ === "eval" || typ === "scalar" || (typ === "" && ev.metric != null)) && name && value !== undefined) {
      const identity = splitLegacyContextName(name, eventSeries(ev));
      metrics.push({
        name: identity.name,
        value,
        step: asNumber(ev.step),
        epoch: asNumber(ev.epoch),
        time: asNumber(ev.time),
        series: identity.series,
        unit: eventUnit(ev)
      });
      continue;
    }
    const current = asNumber(ev.current);
    if ((typ === "progress" || current !== undefined) && name && current !== undefined) {
      const total = asNumber(ev.total);
      const identity = progressIdentity(name, eventSeries(ev));
      progress.push({
        name: identity.name,
        current,
        total,
        percent: total ? (current / total) * 100 : undefined,
        time: asNumber(ev.time),
        series: identity.series,
        label: eventLabel(ev),
        status: String(ev.status || ev.state || "").trim() || undefined,
        best_epoch: asNumber(ev.best_epoch ?? ev.bestEpoch)
      });
      continue;
    }
    if (typ === "note" || typ === "log" || typ === "message" || typ === "warning") notes.push(ev);
  }

  return { events, metrics, latestMetrics: latestMetricByName(metrics), params: latestParams(params), progress, notes, errors };
}

function latestParams(params: ParamPoint[]): ParamPoint[] {
  const latest = new Map<string, ParamPoint>();
  for (const param of params) {
    latest.set(`${param.series || ""}\u0000${param.name}`, param);
  }
  return Array.from(latest.values()).sort((a, b) => a.name.localeCompare(b.name));
}

function progressIdentity(name: string, series?: string) {
  if (series) return { name, series };
  const parts = name.split("/").map((part) => part.trim()).filter(Boolean);
  if (parts.length < 2) return { name, series };
  const leaf = parts[parts.length - 1];
  const context = parts.slice(0, -1).join("/");
  if (/^(epoch|step|trial|fold|batch|iter|iteration|phase|train|eval|validation|test|progress)$/i.test(leaf) || context.length > 20) {
    return { name: leaf, series: context };
  }
  return { name, series };
}

export function summarizeProgress(points: ProgressPoint[]): ProgressSummary[] {
  const grouped = new Map<string, ProgressPoint[]>();
  for (const point of points) {
    const key = `${point.series || ""}\u0000${point.name}`;
    grouped.set(key, [...(grouped.get(key) || []), point]);
  }
  return Array.from(grouped.entries())
    .map(([key, rows]) => {
      const latest = rows[rows.length - 1];
      return {
        key,
        name: latest.name,
        series: latest.series,
        label: latest.label,
        latest,
        count: rows.length,
        done: progressIsDone(latest)
      };
    })
    .sort((a, b) => {
      if (a.done !== b.done) return a.done ? 1 : -1;
      return (b.latest.time || 0) - (a.latest.time || 0);
    });
}

function progressIsDone(point: ProgressPoint): boolean {
  const status = String(point.status || "").toLowerCase();
  if (status === "completed" || status === "complete" || status === "done" || status === "early_stopped") return true;
  if (status === "running" || status === "active") return false;
  return point.total != null && point.current >= point.total;
}

function uniqueFiniteValues(values: Array<number | undefined>): number[] {
  return Array.from(new Set(values.filter((value): value is number => value != null && Number.isFinite(value)))).sort((a, b) => a - b);
}

function resolveMetricAxisKind(rows: MetricPoint[]): "epoch" | "step" | "sample" {
  if (uniqueFiniteValues(rows.map((row) => row.epoch)).length > 1) return "epoch";
  if (uniqueFiniteValues(rows.map((row) => row.step)).length > 1) return "step";
  return "sample";
}

function metricAxis(point: MetricPoint, fallback: number, axisKind: "epoch" | "step" | "sample"): number {
  if (axisKind === "epoch") return point.epoch ?? fallback;
  if (axisKind === "step") return point.step ?? fallback;
  return fallback;
}

function sampleTrend(rows: MetricPoint[], axisKind: "epoch" | "step" | "sample", limit = 24): Array<{ axis: number; value: number }> {
  if (axisKind === "sample") return [];
  const finiteRows = rows.filter((row) => Number.isFinite(row.value));
  if (finiteRows.length <= limit) {
    return finiteRows.map((row, index) => ({ axis: metricAxis(row, index, axisKind), value: row.value }));
  }
  return Array.from({ length: limit }, (_, index) => {
    const sourceIndex = Math.round((index / (limit - 1)) * (finiteRows.length - 1));
    const row = finiteRows[sourceIndex];
    return { axis: metricAxis(row, sourceIndex, axisKind), value: row.value };
  });
}

function metricSeriesKey(point: MetricPoint): string {
  return point.series || "";
}

function compactSeriesLabels(keys: string[]): Map<string, string> {
  const labels = new Map<string, string>();
  if (!keys.length) return labels;
  const parts = keys.map((key) => key.split("/").map((part) => part.trim()).filter(Boolean));
  let common = 0;
  while (parts.every((segments) => segments[common] && segments[common] === parts[0][common])) common += 1;
  const used = new Map<string, number>();
  keys.forEach((key, index) => {
    const segments = parts[index];
    let label = semanticSeriesLabel(segments, common);
    if (!label) label = "default";
    const seen = used.get(label) || 0;
    used.set(label, seen + 1);
    labels.set(key, seen ? `${label} #${seen + 1}` : label);
  });
  return labels;
}

function semanticSeriesLabel(segments: string[], commonPrefixLength: number): string {
  const suffix = segments.slice(commonPrefixLength);
  const all = suffix.length ? suffix : segments.slice(Math.max(0, commonPrefixLength - 1));
  const fusion = all.find((part) => part.startsWith("fusion:"))?.replace(/^fusion:/, "");
  const stage = all.find((part) => /^(final|baseline|reference|ref)$/i.test(part));
  const split = all.find((part) => /^(train|val|valid|validation|test|eval)$/i.test(part));
  const trial = all.find((part) => part.startsWith("trial:"));
  const seed = all.find((part) => part.startsWith("seed:"));
  const pieces = [fusion, normalizeStageLabel(stage), normalizeSplitLabel(split), trial].filter(Boolean) as string[];
  if (pieces.length) return pieces.join(" · ");
  const variant = all.find((part) => part.length > 18 && /[_-]/.test(part));
  if (variant) return compactVariantLabel(variant);
  if (seed) return seed;
  return all.join(" / ");
}

function normalizeStageLabel(stage?: string) {
  if (!stage) return undefined;
  return stage === "ref" ? "reference" : stage;
}

function normalizeSplitLabel(split?: string) {
  if (!split) return undefined;
  if (split === "valid" || split === "validation") return "val";
  return split;
}

function compactVariantLabel(value: string) {
  const clean = value.replace(/[_-]+/g, " ");
  const tokens = clean.split(/\s+/).filter(Boolean);
  const cues = ["finetune", "frozen", "baseline", "raw", "saits", "clean", "cnn", "wavelet", "residual", "linear"];
  const picked = cues.filter((cue) => tokens.some((token) => token.toLowerCase() === cue));
  if (picked.length) return picked.slice(0, 3).join(" · ");
  return tokens.slice(0, 3).join(" ");
}

function seriesRole(key: string, rows: MetricPoint[], axisKind: "epoch" | "step" | "sample"): "curve" | "reference" {
  if (axisKind === "sample") return "reference";
  const segments = key.split("/").map((part) => part.trim()).filter(Boolean);
  if (segments.some((part) => /^(final|baseline|reference|ref)$/i.test(part))) return "reference";
  const axes = uniqueFiniteValues(rows.map((row, index) => metricAxis(row, index, axisKind)));
  return axes.length > 1 && rows.length > 1 ? "curve" : "reference";
}

function summarizeMetricSeries(rows: MetricPoint[], axisKind: "epoch" | "step" | "sample"): MetricSeriesSummary[] {
  const grouped = new Map<string, MetricPoint[]>();
  for (const row of rows) {
    const key = metricSeriesKey(row);
    grouped.set(key, [...(grouped.get(key) || []), row]);
  }
  const labelByKey = compactSeriesLabels(Array.from(grouped.keys()));
  return Array.from(grouped.entries()).map(([key, seriesRows]) => {
    const finiteRows = seriesRows.filter((row) => Number.isFinite(row.value));
    const latest = finiteRows[finiteRows.length - 1] || seriesRows[seriesRows.length - 1];
    return {
      key,
      label: labelByKey.get(key) || key || "default",
      fullLabel: key || "default",
      latest,
      count: finiteRows.length,
      role: seriesRole(key, finiteRows, axisKind),
      trend: sampleTrend(finiteRows, axisKind),
      points: finiteRows
    };
  }).filter((row) => row.latest);
}

function metricScale(unit: string | undefined, values: number[]) {
  if (unit) return { key: unit, label: unit };
  const magnitudes = values.map((value) => Math.abs(value)).filter((value) => value > 0 && Number.isFinite(value)).sort((a, b) => a - b);
  if (!magnitudes.length) return { key: "value:0", label: "value 0" };
  const median = magnitudes[Math.floor(magnitudes.length / 2)];
  const exponent = Math.floor(Math.log10(median));
  const label = exponent === 0 ? "value 1" : `value 1e${exponent}`;
  return { key: `value:1e${exponent}`, label };
}

export function summarizeMetricFamilies(points: MetricPoint[]): MetricFamilySummary[] {
  const grouped = new Map<string, MetricPoint[]>();
  for (const point of points) {
    const unit = point.unit || "";
    const key = `${point.name}\u0000${unit}`;
    grouped.set(key, [...(grouped.get(key) || []), point]);
  }
  return Array.from(grouped.values()).map((rows) => {
    const name = rows[0]?.name || "";
    const unit = rows[0]?.unit;
    const latestBySeries = new Map<string, MetricPoint>();
    for (const row of rows) {
      latestBySeries.set(row.series || "", row);
    }
    const finiteRows = rows.filter((row) => Number.isFinite(row.value));
    const first = finiteRows[0];
    const latest = finiteRows[finiteRows.length - 1];
    const values = finiteRows.map((row) => row.value);
    const scale = metricScale(unit, values);
    const axisKind = resolveMetricAxisKind(finiteRows);
    const trends = summarizeMetricSeries(finiteRows, axisKind);
    const curveTrends = trends.filter((trend) => trend.role === "curve");
    const referenceTrends = trends.filter((trend) => trend.role === "reference");
    const axisRows = curveTrends.length ? curveTrends.flatMap((trend) => trend.points) : finiteRows;
    const axisValues = axisKind === "sample"
      ? []
      : axisRows.map((row, index) => metricAxis(row, finiteRows.indexOf(row) >= 0 ? finiteRows.indexOf(row) : index, axisKind)).filter((value) => Number.isFinite(value));
    const firstIndex = first ? rows.indexOf(first) : 0;
    const latestIndex = latest ? rows.indexOf(latest) : rows.length - 1;
    const delta = first && latest ? latest.value - first.value : NaN;
    const deltaPct = first && latest && first.value !== 0 ? (delta / Math.abs(first.value)) * 100 : undefined;
    return {
      name,
      unit,
      scaleKey: scale.key,
      scaleLabel: scale.label,
      count: rows.length,
      axisKind,
      first,
      latest,
      min: values.length ? Math.min(...values) : NaN,
      max: values.length ? Math.max(...values) : NaN,
      delta,
      deltaPct,
      axisStart: axisValues.length ? Math.min(...axisValues) : first && axisKind !== "sample" ? metricAxis(first, firstIndex, axisKind) : undefined,
      axisEnd: axisValues.length ? Math.max(...axisValues) : latest && axisKind !== "sample" ? metricAxis(latest, latestIndex, axisKind) : undefined,
      trend: sampleTrend(rows, axisKind),
      points: finiteRows,
      series: Array.from(latestBySeries.values()).slice(0, 5),
      trends,
      curveTrends,
      referenceTrends
    };
  });
}
