import type { MetricPoint, ParsedEvents, ProgressPoint, UIEvent } from "./types";
import { latestMetricByName } from "./utils";

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
