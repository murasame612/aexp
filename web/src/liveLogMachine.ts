import { logCursor, mergeCursorLogLines } from "./logs";
import type { LogLine, LogsResponse } from "./types";
export type LiveLogState = "idle" | "loading" | "connecting" | "live" | "reconnecting" | "catching_up" | "complete" | "error";

export interface LiveLogMachine {
  phase: LiveLogState;
  lines: LogLine[];
  cursor: number;
  error: string | null;
}

export type LiveLogEvent =
  | { type: "reset" }
  | { type: "snapshot.start" }
  | { type: "snapshot.received"; payload: LogsResponse }
  | { type: "socket.connect" }
  | { type: "socket.open" }
  | { type: "socket.line"; line: LogLine }
  | { type: "socket.closed" }
  | { type: "complete" }
  | { type: "error"; error: string };

export const initialLiveLogMachine: LiveLogMachine = { phase: "idle", lines: [], cursor: 0, error: null };

// This reducer is deliberately transport-agnostic: tests can replay the exact
// HTTP snapshot → WS overlap → disconnect → HTTP catch-up interaction without
// a browser or timing sleeps.
export function reduceLiveLog(machine: LiveLogMachine, event: LiveLogEvent): LiveLogMachine {
  switch (event.type) {
    case "reset":
      return initialLiveLogMachine;
    case "snapshot.start":
      return { ...machine, phase: machine.cursor > 0 ? "catching_up" : "loading", error: null };
    case "snapshot.received": {
      const lines = mergeCursorLogLines(machine.lines, event.payload.lines || [], Boolean(event.payload.reset));
      return { ...machine, lines, cursor: event.payload.next_cursor ?? logCursor(lines), error: null };
    }
    case "socket.connect":
      return { ...machine, phase: machine.cursor > 0 ? "reconnecting" : "connecting" };
    case "socket.open":
      return { ...machine, phase: "live", error: null };
    case "socket.line": {
      if (event.line.line_no != null && event.line.line_no <= machine.cursor) return machine;
      const lines = mergeCursorLogLines(machine.lines, [event.line]);
      return { ...machine, lines, cursor: Math.max(machine.cursor, event.line.line_no || 0) };
    }
    case "socket.closed":
      return { ...machine, phase: "catching_up" };
    case "complete":
      return { ...machine, phase: "complete" };
    case "error":
      return { ...machine, phase: "error", error: event.error };
  }
}
