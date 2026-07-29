import type { RunChangeItem, RunChangeResponse } from "./types";

export interface RunChangeCheckpoint {
  cursor: number;
  updatedSince?: string;
}

// The first summary response already represents all changes up to its cursor.
// Starting catch-up at zero would replay the complete database.
export function seedRunChangeCheckpoint(checkpoint: RunChangeCheckpoint, summaryCursor?: number): RunChangeCheckpoint {
  if (checkpoint.cursor !== 0 || summaryCursor == null || summaryCursor <= 0) return checkpoint;
  return { ...checkpoint, cursor: summaryCursor };
}

export async function catchUpRunChanges(
  checkpoint: RunChangeCheckpoint,
  fetchChanges: (afterSeq: number, updatedSince?: string) => Promise<RunChangeResponse>
) {
  const changes:RunChangeItem[]=[];
  let cursor=checkpoint.cursor;
  let serverTime=checkpoint.updatedSince;
  for (let page=0;page<50;page+=1) {
    const response=await fetchChanges(cursor, page===0?checkpoint.updatedSince:undefined);
    const fresh=response.items.filter(change=>change.seq>cursor);
    changes.push(...fresh);
    serverTime=response.server_time||serverTime;
    const next=fresh.reduce((value,change)=>Math.max(value,change.seq),Math.max(cursor,response.next_seq||0));
    if (next<=cursor || response.items.length<200) { cursor=next; break; }
    cursor=next;
  }
  return {
    changes,
    checkpoint: { cursor, updatedSince: serverTime } satisfies RunChangeCheckpoint
  };
}

export interface RunChangeSSEParser {
  feed(chunk: string): void;
  cursor(): number;
}

export function createRunChangeSSEParser(onChange: (change: RunChangeItem) => void, initialCursor = 0): RunChangeSSEParser {
  let buffer = "";
  let lastCursor = initialCursor;
  return {
    feed(chunk: string) {
      buffer += chunk.replaceAll("\r\n", "\n");
      while (true) {
        const boundary = buffer.indexOf("\n\n");
        if (boundary < 0) return;
        const frame = buffer.slice(0, boundary);
        buffer = buffer.slice(boundary + 2);
        if (!frame || frame.startsWith(":")) continue;
        let id = 0;
        let event = "message";
        const data: string[] = [];
        for (const line of frame.split("\n")) {
          if (line.startsWith("id:")) id = Number(line.slice(3).trim());
          else if (line.startsWith("event:")) event = line.slice(6).trim();
          else if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
        }
        if (event !== "run-change" || !data.length) continue;
        const change = JSON.parse(data.join("\n")) as RunChangeItem;
        const cursor = Number(change.seq || id);
        if (!Number.isFinite(cursor) || cursor <= lastCursor) continue;
        lastCursor = cursor;
        onChange(change);
      }
    },
    cursor() {
      return lastCursor;
    }
  };
}

export async function readRunChangeStream(
  token: string,
  afterSeq: number,
  onChange: (change: RunChangeItem) => void,
  signal: AbortSignal,
  fetchImpl: typeof fetch = fetch
) {
  const response = await fetchImpl(`/api/v1/runs/changes/stream?after_seq=${afterSeq}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : undefined,
    signal
  });
  if (!response.ok || !response.body) throw new Error(`run change stream failed: ${response.status}`);
  const parser = createRunChangeSSEParser(onChange, afterSeq);
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    parser.feed(decoder.decode(value, { stream: true }));
  }
  parser.feed(decoder.decode());
  return parser.cursor();
}
