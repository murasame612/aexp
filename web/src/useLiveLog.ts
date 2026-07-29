import { useEffect, useReducer, useRef } from "react";
import { ApiError, getLogs, wsURL } from "./api";
import { isEmptyRemotePathSnapshot, logCursor, logSnapshotError } from "./logs";
import type { LogLine, LogsResponse } from "./types";
import { initialLiveLogMachine, reduceLiveLog } from "./liveLogMachine";

export type { LiveLogState } from "./liveLogMachine";
export type LiveLogQuery = { source?: string; path?: string };

function message(cause: unknown) {
  if (cause instanceof ApiError) return cause.details || cause.message;
  return cause instanceof Error ? cause.message : String(cause);
}

export function useLiveLog(token: string, runId: string, query: LiveLogQuery | null, live = true) {
  const [machine, dispatch] = useReducer(reduceLiveLog, initialLiveLogMachine);
  const cursorRef = useRef(0);
  const identityRef = useRef("");

  useEffect(() => {
    let closed = false;
    let ws: WebSocket | null = null;
    let retryTimer: number | undefined;
    const identity = `${runId}:${query?.source || ""}:${query?.path || ""}`;
    if (identityRef.current !== identity) {
      identityRef.current = identity;
      cursorRef.current = 0;
      dispatch({ type: "reset" });
    }
    if (!query) {
      return;
    }

    const mergeResponse = (logs: LogsResponse) => {
      cursorRef.current = logs.next_cursor ?? Math.max(cursorRef.current, logCursor(logs.lines || []));
      dispatch({ type: "snapshot.received", payload: logs });
    };
    const fetchSnapshot = async (afterLine: number, attempt = 0): Promise<boolean> => {
      try {
        const logs = await getLogs(token, runId, { ...query, limit: query.path ? 5000 : 500, afterLine, tail: true });
        if (closed) return false;
        const snapshotError = logSnapshotError(logs);
        if (snapshotError) throw new Error(snapshotError);
        if (isEmptyRemotePathSnapshot(logs) && attempt < 5) {
          await new Promise((resolve) => { retryTimer = window.setTimeout(resolve, 700 * (attempt + 1)); });
          return fetchSnapshot(afterLine, attempt + 1);
        }
        mergeResponse(logs);
        return true;
      } catch (cause) {
        if (closed) return false;
        if (attempt < 3) {
          await new Promise((resolve) => { retryTimer = window.setTimeout(resolve, 900 * 2 ** attempt); });
          return fetchSnapshot(afterLine, attempt + 1);
        }
        dispatch({ type: "error", error: message(cause) });
        return false;
      }
    };
    const connect = async () => {
      if (closed) return;
      dispatch({ type: "snapshot.start" });
      await fetchSnapshot(cursorRef.current);
      if (closed) return;
      if (!live) {
        dispatch({ type: "complete" });
        return;
      }
      dispatch({ type: "socket.connect" });
      ws = new WebSocket(wsURL(`/ws/runs/${encodeURIComponent(runId)}/logs`, token, { ...query, after_line: String(cursorRef.current), snapshot: "false" }));
      ws.onopen = () => { if (!closed) dispatch({ type: "socket.open" }); };
      ws.onmessage = (event) => {
        if (closed) return;
        try {
          const payload = JSON.parse(event.data);
          if (payload.type === "log.line") {
            const line: LogLine = { content: payload.content || "", line_no: payload.line_no, source: payload.source };
            if (line.line_no != null && line.line_no <= cursorRef.current) return;
            cursorRef.current = Math.max(cursorRef.current, line.line_no || 0);
            dispatch({ type: "socket.line", line });
          }
        } catch {
          dispatch({ type: "socket.line", line: { content: String(event.data) } });
        }
      };
      ws.onclose = () => { if (!closed) { dispatch({ type: "socket.closed" }); retryTimer = window.setTimeout(() => void connect(), 1500); } };
      ws.onerror = () => { if (!closed) dispatch({ type: "error", error: "WebSocket connection failed" }); };
    };
    void connect();
    return () => {
      closed = true;
      window.clearTimeout(retryTimer);
      ws?.close();
    };
  }, [token, runId, query?.source, query?.path, live]);

  return { lines: machine.lines, state: machine.phase, error: machine.error };
}
