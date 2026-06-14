import type { MetricPoint, ParsedEvents, ProgressPoint, UIEvent } from "./types";
import { latestMetricByName } from "./utils";

export interface MetricFamilySummary {
  name: string;
  count: number;
  first?: MetricPoint;
  latest?: MetricPoint;
  min: number;
  max: number;
  delta: number;
  deltaPct?: number;
  axisStart?: number;
  axisEnd?: number;
  series: MetricPoint[];
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
    if ((typ === "metric" || typ === "metrics" || typ === "eval" || typ === "scalar" || value !== undefined) && name && value !== undefined) {
      metrics.push({
        name,
        value,
        step: asNumber(ev.step),
        epoch: asNumber(ev.epoch),
        time: asNumber(ev.time),
        series: eventSeries(ev)
      });
      continue;
    }
    const current = asNumber(ev.current);
    if ((typ === "progress" || current !== undefined) && name && current !== undefined) {
      const total = asNumber(ev.total);
      progress.push({
        name,
        current,
        total,
        percent: total ? (current / total) * 100 : undefined,
        time: asNumber(ev.time)
      });
      continue;
    }
    if (typ === "note" || typ === "log" || typ === "message") notes.push(ev);
  }

  return { events, metrics, latestMetrics: latestMetricByName(metrics), progress, notes, errors };
}

function metricAxis(point: MetricPoint, fallback: number): number {
  return point.step ?? point.epoch ?? point.time ?? fallback;
}

export function summarizeMetricFamilies(points: MetricPoint[]): MetricFamilySummary[] {
  const grouped = new Map<string, MetricPoint[]>();
  for (const point of points) {
    grouped.set(point.name, [...(grouped.get(point.name) || []), point]);
  }
  return Array.from(grouped.entries()).map(([name, rows]) => {
    const latestBySeries = new Map<string, MetricPoint>();
    for (const row of rows) {
      latestBySeries.set(row.series || "", row);
    }
    const finiteRows = rows.filter((row) => Number.isFinite(row.value));
    const first = finiteRows[0];
    const latest = finiteRows[finiteRows.length - 1];
    const values = finiteRows.map((row) => row.value);
    const firstIndex = first ? rows.indexOf(first) : 0;
    const latestIndex = latest ? rows.indexOf(latest) : rows.length - 1;
    const delta = first && latest ? latest.value - first.value : NaN;
    const deltaPct = first && latest && first.value !== 0 ? (delta / Math.abs(first.value)) * 100 : undefined;
    return {
      name,
      count: rows.length,
      first,
      latest,
      min: values.length ? Math.min(...values) : NaN,
      max: values.length ? Math.max(...values) : NaN,
      delta,
      deltaPct,
      axisStart: first ? metricAxis(first, firstIndex) : undefined,
      axisEnd: latest ? metricAxis(latest, latestIndex) : undefined,
      series: Array.from(latestBySeries.values()).slice(0, 5)
    };
  });
}
