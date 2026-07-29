import { describe, expect, it } from "vitest";
import { initialLiveLogMachine, reduceLiveLog, type LiveLogMachine } from "./liveLogMachine";

describe("live log HTTP/WebSocket interaction", () => {
  it("fills a reconnect gap, deduplicates overlap, and preserves lines on completion", () => {
    let state = reduceLiveLog(initialLiveLogMachine, { type: "snapshot.start" });
    state = reduceLiveLog(state, { type: "snapshot.received", payload: { run_id: "r", source: "stdout", total_lines: 2, offset: 0, limit: 500, lines: [{ line_no: 1, source: "stdout", content: "one" }, { line_no: 2, source: "stdout", content: "two" }], next_cursor: 2 } });
    state = reduceLiveLog(state, { type: "socket.connect" });
    state = reduceLiveLog(state, { type: "socket.open" });
    state = reduceLiveLog(state, { type: "socket.line", line: { line_no: 2, source: "stdout", content: "two" } });
    state = reduceLiveLog(state, { type: "socket.line", line: { line_no: 3, source: "stdout", content: "three" } });
    state = reduceLiveLog(state, { type: "socket.closed" });
    state = reduceLiveLog(state, { type: "snapshot.received", payload: { run_id: "r", source: "stdout", total_lines: 5, offset: 0, limit: 500, lines: [{ line_no: 3, source: "stdout", content: "three" }, { line_no: 4, source: "stdout", content: "four" }, { line_no: 5, source: "stdout", content: "five" }], next_cursor: 5 } });
    state = reduceLiveLog(state, { type: "complete" });
    expect(state.phase).toBe("complete");
    expect(state.cursor).toBe(5);
    expect(state.lines.map((line) => line.content)).toEqual(["one", "two", "three", "four", "five"]);
  });

  it("replaces the previous file generation when the snapshot requests reset", () => {
    let state: LiveLogMachine = { ...initialLiveLogMachine, cursor: 20, lines: [{ line_no: 20, source: "stdout", content: "old" }] };
    state = reduceLiveLog(state, { type: "snapshot.received", payload: { run_id: "r", source: "stdout", total_lines: 1, offset: 0, limit: 500, reset: true, lines: [{ line_no: 1, source: "stdout", content: "new" }], next_cursor: 1 } });
    expect(state.cursor).toBe(1);
    expect(state.lines.map((line) => line.content)).toEqual(["new"]);
  });
});
