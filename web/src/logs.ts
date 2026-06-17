import type { LogLine, LogsResponse } from "./types";

export function logSnapshotError(logs: LogsResponse): string | null {
  if (logs.lines?.length) return null;
  const kind = logs.error_kind?.trim();
  const message = logs.error?.trim();
  if (!kind && !message) return null;
  if (kind && message) return `${kind}: ${message}`;
  return kind || message || null;
}

export function isEmptyRemotePathSnapshot(logs: LogsResponse): boolean {
  return Boolean(logs.path && logs.remote && logs.total_lines === 0 && !logs.lines?.length && !logs.error && !logs.error_kind);
}

export function mergeLogSnapshot(snapshot: LogLine[], current: LogLine[]): LogLine[] {
  if (!current.length) return snapshot;
  if (!snapshot.length) return current;

  const seen = new Set(snapshot.map(logLineKey));
  const liveOnly = current.filter((line) => {
    const key = logLineKey(line);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
  return [...snapshot, ...liveOnly];
}

function logLineKey(line: LogLine): string {
  if (line.source || line.line_no != null) return `${line.source || ""}:${line.line_no ?? ""}:${line.content}`;
  return line.content;
}
