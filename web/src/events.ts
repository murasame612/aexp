import type { MetricPoint, ParsedEvents, ProgressPoint, UIEvent } from "./types";
import { latestMetricByName } from "./utils";

export interface MetricFamilySummary {
  name: string;
  unit?: string;
  scaleKey: string;
  scaleLabel: string;
  count: number;
  first?: MetricPoint;
  latest?: MetricPoint;
  min: number;
  max: number;
  delta: number;
  deltaPct?: number;
  axisStart?: number;
  axisEnd?: number;
  trend: Array<{ axis: number; value: number }>;
  series: MetricPoint[];
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

function eventSeries(ev: UIEvent): string | undefined {
  const parts = [ev.series, ev.run, ev.variant, ev.split, ev.stage].map((v) => String(v || "").trim()).filter(Boolean);
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

export function parseEventLines(lines: string[]): ParsedEvents {
  const events: UIEvent[] = [];
  const metrics: MetricPoint[] = [];
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
    if ((typ === "metric" || typ === "metrics" || typ === "eval" || typ === "scalar" || (typ === "" && ev.metric != null)) && name && value !== undefined) {
      metrics.push({
        name,
        value,
        step: asNumber(ev.step),
        epoch: asNumber(ev.epoch),
        time: asNumber(ev.time),
        series: eventSeries(ev),
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
        label: eventLabel(ev)
      });
      continue;
    }
    if (typ === "note" || typ === "log" || typ === "message") notes.push(ev);
  }

  return { events, metrics, latestMetrics: latestMetricByName(metrics), progress, notes, errors };
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
        done: latest.total != null && latest.current >= latest.total
      };
    })
    .sort((a, b) => {
      if (a.done !== b.done) return a.done ? 1 : -1;
      return (b.latest.time || 0) - (a.latest.time || 0);
    });
}

function metricAxis(point: MetricPoint, fallback: number): number {
  return point.step ?? point.epoch ?? point.time ?? fallback;
}

function sampleTrend(rows: MetricPoint[], limit = 24): Array<{ axis: number; value: number }> {
  const finiteRows = rows.filter((row) => Number.isFinite(row.value));
  if (finiteRows.length <= limit) {
    return finiteRows.map((row, index) => ({ axis: metricAxis(row, rows.indexOf(row)), value: row.value }));
  }
  return Array.from({ length: limit }, (_, index) => {
    const sourceIndex = Math.round((index / (limit - 1)) * (finiteRows.length - 1));
    const row = finiteRows[sourceIndex];
    return { axis: metricAxis(row, rows.indexOf(row)), value: row.value };
  });
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
      first,
      latest,
      min: values.length ? Math.min(...values) : NaN,
      max: values.length ? Math.max(...values) : NaN,
      delta,
      deltaPct,
      axisStart: first ? metricAxis(first, firstIndex) : undefined,
      axisEnd: latest ? metricAxis(latest, latestIndex) : undefined,
      trend: sampleTrend(rows),
      series: Array.from(latestBySeries.values()).slice(0, 5)
    };
  });
}
